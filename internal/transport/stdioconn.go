// Package transport supplies the connection backends a quil client can use to
// reach a daemon: the local Unix socket, or an SSH channel to another host.
package transport

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// readChunk is the pump's buffer size. IPC frames are usually far smaller;
// this only bounds how much one Read syscall can pull at once.
const readChunk = 32 * 1024

// noExitCode marks a child that has not been reaped — still running, still
// connecting, or never started. Distinct from every real exit status, including
// the -1 that os/exec reports for a signalled process, only in meaning: both
// say "no status the caller can act on".
const noExitCode = -1

// stdioAddr reports a stdio-backed connection's endpoint for net.Conn's
// LocalAddr/RemoteAddr. Purely descriptive — nothing routes on it.
type stdioAddr string

func (a stdioAddr) Network() string { return "stdio" }
func (a stdioAddr) String() string  { return string(a) }

// stdioConn adapts a child process's stdin/stdout pipes to net.Conn.
//
// Reads go through a pump goroutine rather than straight to the pipe so that
// read deadlines work identically on every platform. On Unix os.Pipe is
// pollable and SetReadDeadline would work directly, but on Windows the handles
// are non-overlapped and it fails with os.ErrNoDeadline — and three call sites
// depend on read deadlines (cmd/quil/handshake.go and cmd/quil/status.go).
// One uniform implementation is simpler than a platform split and is what the
// tests pin.
//
// Write deadlines are honestly unsupported: the only caller
// (internal/ipc/server.go:260) discards the error, and OpenSSH's
// ServerAliveInterval provides the wedged-peer detection the deadline would.
//
// Callers must ensure a single reader at a time per conn, same discipline as
// ipc.Conn.Receive (internal/ipc/server.go:265-268): stdioConn is only ever
// consumed through ipc.Conn, whose bufio.Reader is already single-reader by
// construction. Two concurrent Read calls can each land a held-over remainder
// in the shared `held` field (see Read); the second write clobbers the first
// and silently drops bytes. The fix is this documented constraint, not a wider
// lock — holding mu across the blocked select in Read would also block
// SetReadDeadline, and the two would deadlock.
//
// One deviation from net.Conn, deliberate and load-bearing for the callers:
// "a deadline can be set even if a read is already blocked" does NOT hold here.
// Read samples the deadline once on entry and builds its timer before parking,
// so a SetReadDeadline made while a Read is in flight applies only to
// SUBSEQUENT reads. Every current caller sets the deadline before the read it
// governs (cmd/quil/handshake.go, cmd/quil/status.go), so this is invisible to
// them — but code that tries to unwedge a parked Read by setting a deadline
// from another goroutine will hang instead. Close is what interrupts a blocked
// Read.
type stdioConn struct {
	r    *os.File // parent's read end of the child's stdout
	w    *os.File // parent's write end of the child's stdin
	cmd  *exec.Cmd
	desc string

	readCh chan []byte
	done   chan struct{}

	// bytesIn counts what the far side has delivered. Atomic rather than
	// mu-guarded so Established() cannot be blocked by, or block, a Read
	// parked in its select — the caller asks precisely when a read has just
	// failed. Written only by pump, which is the single writer.
	bytesIn atomic.Int64

	mu       sync.Mutex // guards held, deadline, pumpErr
	held     []byte     // remainder of the last chunk not yet returned
	deadline time.Time
	pumpErr  error

	closeOnce sync.Once

	// exitCode is the child's wait status once reaped, or noExitCode while it
	// is still running. Atomic for the same reason as bytesIn: it is read
	// precisely when a read has just failed, and must not be able to block or
	// be blocked by a parked Read.
	exitCode atomic.Int32

	// reapOnce guards cmd.Wait, which must not be called twice. Both pump()
	// (on pipe EOF) and Close() reach it.
	reapOnce sync.Once

	// stderr holds ssh's captured diagnostics when the dial ran in batch mode.
	// Nil on interactive dials, where stderr went straight to the terminal.
	// Assigned by SSH (ssh.go) AFTER newStdioConn returns and pump() is
	// already running — safe only because pump() never reads or writes this
	// field; see newStdioConn's assignment site for the other half of the
	// argument.
	stderr *lockedBuffer
}

// newStdioConn wires an already-started command's pipes into a net.Conn and
// starts the read pump. r and w are the PARENT's ends; the caller must have
// already closed the child's ends.
func newStdioConn(cmd *exec.Cmd, r, w *os.File, desc string) *stdioConn {
	c := &stdioConn{
		r:      r,
		w:      w,
		cmd:    cmd,
		desc:   desc,
		readCh: make(chan []byte, 1),
		done:   make(chan struct{}),
	}
	c.exitCode.Store(noExitCode)
	go c.pump()
	return c
}

// reap waits for the child and records its exit status.
//
// Idempotent because exec.Cmd.Wait must not be called twice and both pump() and
// Close() reach here. Calling Wait from the pump goroutine is safe only because
// SSH() gives the command *os.File stdin/stdout rather than StdinPipe/
// StdoutPipe: exec therefore starts no copier goroutines, and Wait closes none
// of the parent's descriptors.
func (c *stdioConn) reap() {
	c.reapOnce.Do(func() {
		if c.cmd == nil {
			return
		}
		// The error here is the child's non-zero status, which is exactly what
		// this function exists to record — not a failure of the wait itself.
		_ = c.cmd.Wait()
		if st := c.cmd.ProcessState; st != nil {
			c.exitCode.Store(int32(st.ExitCode()))
		}
	})
}

// pump drains the pipe into readCh until the pipe errors or the conn closes.
// Closing readCh is what turns a dead child into a read error rather than a
// permanent block.
func (c *stdioConn) pump() {
	// Deferred LIFO: close(readCh) runs FIRST, unparking any blocked reader,
	// and only then does reap() park this goroutine in Wait. Reversed, a child
	// that closed stdout without exiting would hold every reader blocked until
	// Close killed it.
	defer c.reap()
	defer close(c.readCh)
	buf := make([]byte, readChunk)
	for {
		n, err := c.r.Read(buf)
		if n > 0 {
			// Counted here, before the handoff, so Established() is true as
			// soon as bytes EXIST rather than once a reader has taken them.
			c.bytesIn.Add(int64(n))
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			select {
			case c.readCh <- chunk:
			case <-c.done:
				return
			}
		}
		if err != nil {
			c.mu.Lock()
			c.pumpErr = err
			c.mu.Unlock()
			return
		}
	}
}

// Read is not safe for concurrent use by multiple goroutines — see the
// single-reader note on stdioConn. Two overlapping calls can each receive a
// chunk larger than their buffer and try to stash the remainder in `held`;
// whichever write lands second wins, and the other call's remainder is lost
// rather than merged.
func (c *stdioConn) Read(p []byte) (int, error) {
	// Checked before the held fast path below, or a Read after Close returns
	// buffered bytes instead of net.ErrClosed whenever a remainder happens to
	// be pending — a net.Conn contract violation that the obvious test misses,
	// because a test without a held remainder takes the other branch.
	select {
	case <-c.done:
		return 0, net.ErrClosed
	default:
	}

	c.mu.Lock()
	if len(c.held) > 0 {
		n := copy(p, c.held)
		c.held = c.held[n:]
		c.mu.Unlock()
		return n, nil
	}
	deadline := c.deadline
	c.mu.Unlock()

	var timeout <-chan time.Time
	if !deadline.IsZero() {
		t := time.NewTimer(time.Until(deadline))
		defer t.Stop()
		timeout = t.C
	}

	select {
	case chunk, ok := <-c.readCh:
		if !ok {
			c.mu.Lock()
			err := c.pumpErr
			c.mu.Unlock()
			if err == nil {
				err = net.ErrClosed
			}
			return 0, err
		}
		n := copy(p, chunk)
		if n < len(chunk) {
			c.mu.Lock()
			c.held = chunk[n:]
			c.mu.Unlock()
		}
		return n, nil
	case <-timeout:
		return 0, os.ErrDeadlineExceeded
	case <-c.done:
		return 0, net.ErrClosed
	}
}

func (c *stdioConn) Write(p []byte) (int, error) {
	select {
	case <-c.done:
		return 0, net.ErrClosed
	default:
	}
	return c.w.Write(p)
}

// Close tears down the pipes and reaps the child. Idempotent.
func (c *stdioConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		// Every error below is deliberately discarded: this is teardown of a
		// connection the caller has already finished with, and there is no
		// recovery for any of them. A pipe that fails to close is already
		// closed; Kill fails only when the child is already dead, which is the
		// outcome we wanted. Reporting any of it would turn a normal close into
		// a spurious failure. Close itself returns nil for the same reason.
		_ = c.w.Close()
		_ = c.r.Close()
		if c.cmd != nil && c.cmd.Process != nil {
			// Kill BEFORE reap, not after. sync.Once.Do blocks the second
			// caller until the first returns, so when pump is already parked in
			// Wait, reaping an unkilled child would hang Close for as long as
			// ssh stayed alive. Killing first guarantees that Wait returns.
			//
			// Killing a child that has already exited is a no-op on a PID that
			// cannot have been reused: it is an unreaped zombie precisely
			// because nothing has waited on it yet.
			_ = c.cmd.Process.Kill()
			c.reap()
		}
	})
	return nil
}

func (c *stdioConn) LocalAddr() net.Addr  { return stdioAddr("local") }
func (c *stdioConn) RemoteAddr() net.Addr { return stdioAddr(c.desc) }

func (c *stdioConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

func (c *stdioConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return nil
}

// SetWriteDeadline is not supported. See the type comment for why that is
// safe here rather than papered over with a lie that returns nil.
func (c *stdioConn) SetWriteDeadline(t time.Time) error {
	return os.ErrNoDeadline
}

// Stderr returns whatever the child wrote to stderr, or "" when stderr was not
// captured. Used to turn "read: EOF" into ssh's own explanation.
func (c *stdioConn) Stderr() string {
	if c.stderr == nil {
		return ""
	}
	return c.stderr.String()
}

// LinkStatus is implemented by connections carried over a child process. It
// exists so a caller can tell "the transport never came up" apart from "the
// peer answered, badly" — a distinction the net.Conn interface cannot express,
// and one that matters because both arrive at the same call site as a failed
// read.
type LinkStatus interface {
	// Established reports whether the far side has ever delivered a byte.
	//
	// This, not LinkErr, is the load-bearing test. LinkErr only becomes
	// non-nil once ssh has actually DIED, which covers the fast failures
	// (NXDOMAIN, immediate auth reject, "command not found") but not the slow
	// ones: against a firewalled port or a dead IP, ssh is still happily
	// waiting on TCP while the caller's much shorter handshake deadline
	// expires — so a liveness test keyed on "has it died yet" reports a
	// perfectly healthy link that has never carried a single byte.
	//
	// "Has anything arrived" has neither problem. It is monotonic, it cannot
	// race a still-connecting child, and it is exact by construction: a peer
	// that answered badly has produced bytes by definition, and one that never
	// connected has produced none.
	Established() bool

	// LinkErr reports why the transport is dead, or nil while it is alive —
	// including while it is merely still connecting. Use it for the
	// EXPLANATION once Established() has established there is a problem, never
	// as the test for whether one exists.
	LinkErr() error

	// ExitCode reports the child's exit status, or -1 if it has not been
	// reaped.
	//
	// For an ssh child this separates a failure on the far side from ssh's own:
	// 255 is ssh itself (auth, host key, DNS, refused connect), while the
	// remote shell's codes pass through untouched — 127 for a command it could
	// not find, 126 for one it found and could not execute. That distinction is
	// what makes "quil is not installed over there" actionable rather than just
	// another unreachable host.
	//
	// Read it AFTER Close, which is what reaps the child and makes the status
	// final. This is the mirror image of LinkErr, which must be read BEFORE
	// Close because Close can clear it.
	ExitCode() int
}

// Established reports whether the far side has ever delivered a byte. See
// LinkStatus for why this, not LinkErr, is the test for a link that never came
// up.
func (c *stdioConn) Established() bool { return c.bytesIn.Load() > 0 }

// ExitCode reports the child's exit status, or noExitCode if it has not been
// reaped. See LinkStatus.ExitCode for what the ssh values mean and why this is
// read after Close rather than before.
func (c *stdioConn) ExitCode() int { return int(c.exitCode.Load()) }

// LinkErr reports the child's failure once its stdout has closed, or nil while
// the pipe is still live.
//
// For an ssh conn this is the difference between a real diagnosis and a wrong
// one. exec.Cmd.Start succeeds as soon as the ssh BINARY launches — long before
// ssh resolves the hostname, authenticates, or verifies a host key — so the
// dial cannot fail for any network reason. Every such failure instead shows up
// later as a read error on a channel whose far end was never connected, which
// is indistinguishable from "the daemon did not answer" unless something asks
// the transport directly.
func (c *stdioConn) LinkErr() error {
	c.mu.Lock()
	err := c.pumpErr
	c.mu.Unlock()
	if err == nil {
		return nil
	}
	// ssh's own words beat ours; it explains exactly which stage failed. Only
	// present on batch dials — an interactive dial sent stderr to the terminal,
	// where the user has already seen it.
	//
	// Sanitized, because this is NOT purely ssh's own output: ssh multiplexes
	// the remote command's fd 2 (SSH_MSG_CHANNEL_EXTENDED_DATA) onto its own
	// stderr, so the buffer also carries whatever the remote quil --stdio
	// process and the remote shell's rc files wrote — verbatim, attacker
	// controlled if that host is compromised. The caller prints this to a
	// terminal, so unsanitized it is an escape-sequence injection into the
	// operator's local terminal (OSC 52 clipboard write, UI spoofing).
	if msg := strings.TrimSpace(sanitizeForTerminal(c.Stderr())); msg != "" {
		return fmt.Errorf("%s: %s", c.desc, truncateForMessage(msg))
	}
	return fmt.Errorf("%s: %w", c.desc, err)
}

// maxStderrInMessage bounds how much captured stderr an error message may
// carry. ssh's real diagnostics are one or two lines; anything beyond this is
// a remote shell's rc-file noise or a deliberate flood.
const maxStderrInMessage = 2000

// truncateForMessage caps remote-influenced text at a length a terminal can
// reasonably show.
//
// The buffer this draws from is filled by the far side (ssh relays the remote
// command's fd 2 onto its own stderr) and is otherwise unbounded, so without a
// cap a hostile or merely chatty host could push megabytes through a single
// error line. Cut on a rune boundary so the truncation cannot itself emit
// invalid UTF-8.
func truncateForMessage(s string) string {
	if len(s) <= maxStderrInMessage {
		return s
	}
	cut := maxStderrInMessage
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…[truncated]"
}

// terminalSanitizer strips the control bytes a terminal acts on from a stream
// before forwarding it, for output that is remote-influenced and terminal-bound.
//
// Filters at BYTE level, and only C0 (plus DEL), which is what makes it safe to
// apply to an arbitrary stream: every byte it removes is < 0x80, so it can
// never be part of a multi-byte UTF-8 sequence and a rune split across two
// Writes is untouched. The rune-level sanitizeForTerminal cannot offer that —
// it would mangle a sequence straddling a chunk boundary.
//
// C1 (0x80-0x9f) is deliberately left alone here for the same reason: at byte
// level those are indistinguishable from UTF-8 continuation bytes, and dropping
// them would corrupt legitimate text. Removing ESC and BEL already defangs the
// sequences that matter — a bare 0x9b CSI introducer is not treated as a
// control by terminals in UTF-8 mode.
type terminalSanitizer struct{ w io.Writer }

func (t *terminalSanitizer) Write(p []byte) (int, error) {
	clean := make([]byte, 0, len(p))
	for _, b := range p {
		switch {
		case b == '\n' || b == '\t':
			clean = append(clean, b)
		case b < 0x20 || b == 0x7f: // C0 controls and DEL
			// dropped
		default:
			clean = append(clean, b)
		}
	}
	if len(clean) > 0 {
		if _, err := t.w.Write(clean); err != nil {
			return 0, err
		}
	}
	// Report the caller's full length: the bytes were consumed, and a short
	// count would make io.Copy and exec's writer goroutine treat filtering as
	// a write error.
	return len(p), nil
}

// sanitizeForTerminal strips control bytes that a terminal would interpret,
// keeping newline and tab. Applied to any remote-influenced text that reaches
// the local terminal.
//
// Control characters are DROPPED rather than replaced: an escape introducer
// rendered as a visible glyph would split the sequence's payload into stray
// text, which is noisier than simply removing it. Same reasoning and same
// treatment as claudesessions.sanitizeTitle.
func sanitizeForTerminal(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20, r == 0x7f: // C0 and DEL
			return -1
		case r >= 0x80 && r <= 0x9f: // C1, including the 0x9b CSI introducer
			return -1
		default:
			return r
		}
	}, s)
}

// compile-time proof the adapter satisfies the interface ipc.DialFunc returns,
// and that a caller can interrogate a dead link.
var (
	_ net.Conn   = (*stdioConn)(nil)
	_ LinkStatus = (*stdioConn)(nil)
)
