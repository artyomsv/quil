// Package transport supplies the connection backends a quil client can use to
// reach a daemon: the local Unix socket, or an SSH channel to another host.
package transport

import (
	"net"
	"os"
	"os/exec"
	"sync"
	"time"
)

// readChunk is the pump's buffer size. IPC frames are usually far smaller;
// this only bounds how much one Read syscall can pull at once.
const readChunk = 32 * 1024

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
// and silently drops bytes. The fix is this documented constraint, not a
// wider lock — holding mu across the blocked select in Read would also block
// SetReadDeadline, which is exactly the call the version handshake uses to
// break a blocked Read out of that same select.
type stdioConn struct {
	r    *os.File // parent's read end of the child's stdout
	w    *os.File // parent's write end of the child's stdin
	cmd  *exec.Cmd
	desc string

	readCh chan []byte
	done   chan struct{}

	mu       sync.Mutex // guards held, deadline, pumpErr
	held     []byte     // remainder of the last chunk not yet returned
	deadline time.Time
	pumpErr  error

	closeOnce sync.Once

	// stderr holds ssh's captured diagnostics when the dial ran in batch mode.
	// Nil on interactive dials, where stderr went straight to the terminal.
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
	go c.pump()
	return c
}

// pump drains the pipe into readCh until the pipe errors or the conn closes.
// Closing readCh is what turns a dead child into a read error rather than a
// permanent block.
func (c *stdioConn) pump() {
	defer close(c.readCh)
	buf := make([]byte, readChunk)
	for {
		n, err := c.r.Read(buf)
		if n > 0 {
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
		_ = c.w.Close()
		_ = c.r.Close()
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
			_ = c.cmd.Wait()
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

// compile-time proof the adapter satisfies the interface ipc.DialFunc returns.
var _ net.Conn = (*stdioConn)(nil)
