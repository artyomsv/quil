package ipc

import (
	"bufio"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/artyomsv/quil/internal/logger"
)

// sendBufSize is the depth of EACH of a connection's two send queues (critical
// and droppable). A wedged or slow client can build up at most this many
// in-flight frames per queue. The critical queue (state, responses, ghost
// replay, lifecycle) overflowing tears the connection down — guaranteeing that
// one bad client cannot block the daemon's broadcast loop and starve healthy
// peers. The droppable queue (live PTY output) overflowing drops the frame
// instead (cosmetic — superseded by the next output frame), so a busy-but-alive
// client is never disconnected by an output storm.
//
// 64 frames is comfortably more than any healthy client lags by (a TUI's
// Bubble Tea event loop typically drains everything in <50 ms) and small
// enough that an overflowed critical queue is detected within a few
// milliseconds.
const sendBufSize = 64

// flushPollInterval is how often Flush re-checks the pending count. Matches
// SendBlocking's poll interval — same trade between exit latency and spinning.
const flushPollInterval = 2 * time.Millisecond

// writeDeadline bounds how long a single raw.Write may block inside sendLoop
// before we re-examine the peer. Belt-and-suspenders alongside the critCh
// overflow detection: under a wedged kernel buffer + a peer that doesn't
// error on TCP RST, the overflow path is still triggered (critCh fills →
// next critical send trips overflow → close), but the deadline guarantees a
// deterministic cleanup ceiling instead of an indefinite block. Overflow-close
// applies only to the critical queue; droppable output (outCh) is shed when
// full and never triggers a close.
//
// It is a PROGRESS window, not a patience limit, and reading it as the latter
// is what shipped the 2026-08-11 incident: expiry closes the conn only when the
// peer moved zero bytes in the whole window (see write). A peer draining
// steadily keeps its conn however many windows the frame takes, so this value
// governs how fast a genuinely wedged peer is detected and nothing else — which
// is why 30 s stays comfortable rather than needing to cover the worst frame a
// slow client might take.
// Tests shrink it per CONN (newConnWithWriteWindow), never by reassigning this
// — 30 s is unwaitable in a test, but a mutable package var is read by the
// sendLoop of every conn alive in the process, including ones outliving the
// parallel test that made them, which is a data race the race detector catches
// only sometimes. A per-conn value set at construction has no window at all.
const writeDeadline = 30 * time.Second

// ErrSendOverflow is returned by Conn.Send when the per-conn send buffer is
// full. The connection has been scheduled for close; future Sends short-
// circuit with the same error.
var ErrSendOverflow = errors.New("ipc: send buffer overflow (slow client)")

// ErrConnClosed is returned by SendBlocking when the conn closes (locally,
// via the overflow path, or by the peer) while waiting for queue space.
var ErrConnClosed = errors.New("ipc: conn closed")

// ErrSendCanceled is returned by SendBlocking when the caller's cancel
// channel fires while waiting for queue space.
var ErrSendCanceled = errors.New("ipc: blocking send canceled")

// sendHeadroom is the critical-queue depth a SendBlocking caller waits for
// before enqueuing. Capping bulk transfers at half the queue reserves the
// other half for concurrent Broadcast criticals (state updates, pane events),
// so a replay-saturated queue can never trip the overflow close for traffic
// the bulk sender didn't produce.
const sendHeadroom = sendBufSize / 2

// MessageHandler is called for each incoming message on a connection.
type MessageHandler func(conn *Conn, msg *Message)

// Conn wraps a net.Conn with message framing.
//
// Sends are non-blocking: each Conn owns TWO 64-slot queues and a dedicated
// goroutine that drains them into the underlying socket. The critical queue
// carries must-deliver frames (state, responses, ghost replay, lifecycle); the
// droppable queue carries live PTY output broadcasts. The send loop drains
// critical first (priority) so an output flood can never starve state. A slow
// or wedged peer drains its own queues; if the CRITICAL queue overflows the
// offending conn is closed in the background and Send returns ErrSendOverflow.
// If the DROPPABLE queue overflows the frame is dropped (cosmetic — the next
// output frame supersedes it) and the conn survives. Other connections are
// never affected by one client's slowness — closing the wedge-incident class
// where a single stuck TUI or MCP bridge stalled the daemon's broadcast for
// every other client, AND the busy-but-alive class where an output storm
// force-closed a TUI mid-restore.
type Conn struct {
	raw       net.Conn
	br        *bufio.Reader // buffered read side — reduces syscalls from 2 per message to 1
	critCh    chan []byte   // must-deliver: state, responses, ghost replay, lifecycle
	outCh     chan []byte   // droppable: live PaneOutput broadcast frames
	done      chan struct{}
	closeOnce sync.Once
	deadOnce  sync.Once
	closed    atomic.Bool
	overflow  atomic.Bool
	dropped   atomic.Uint64
	// deadlineRefused gates reportDeadlineRefused to one line per conn.
	deadlineRefused atomic.Bool
	// noPaneOutput opts this conn out of the live MsgPaneOutput stream.
	//
	// Inverted so the ZERO VALUE means subscribed: a client that never says
	// anything — every TUI, and any build older than this field — keeps
	// receiving exactly what it did before. Only a client that explicitly asks
	// to be excused loses frames.
	//
	// Atomic because the opt-out arrives on the conn's read goroutine while
	// Broadcast reads it from whichever goroutine is emitting.
	noPaneOutput atomic.Bool
	// pending counts must-deliver frames accepted by Send but not yet written
	// to the socket. Send is non-blocking — it hands the frame to sendLoop —
	// so an empty critCh does NOT mean the peer has it. Flush needs to know
	// the difference; see Flush.
	pending atomic.Int64
	// writeWindow is this conn's progress window (see writeDeadline). Fixed at
	// construction and never written again, so sendLoop reads it without
	// synchronisation.
	writeWindow time.Duration
}

func newConn(raw net.Conn) *Conn { return newConnWithWriteWindow(raw, writeDeadline) }

// newConnWithWriteWindow is newConn with an explicit progress window, so a test
// can exercise a stall without waiting out the production one. The window is
// passed at construction rather than set afterwards because sendLoop starts
// here and would race any later write to the field.
func newConnWithWriteWindow(raw net.Conn, window time.Duration) *Conn {
	c := &Conn{
		raw:         raw,
		br:          bufio.NewReader(raw),
		critCh:      make(chan []byte, sendBufSize),
		outCh:       make(chan []byte, sendBufSize),
		done:        make(chan struct{}),
		writeWindow: window,
	}
	go c.sendLoop()
	return c
}

// Send marshals msg into the wire frame and queues it for transmission. Returns
// ErrSendOverflow when the per-conn buffer is full — the conn has been
// scheduled for async close at that point.
//
// The closed/overflow short-circuit here is the fast path: it skips the JSON
// marshal entirely for a known-dead conn. The actual race-safe check happens
// inside sendFrame next to the channel send — do not remove either one.
func (c *Conn) Send(msg *Message) error {
	if c.closed.Load() || c.overflow.Load() {
		return ErrSendOverflow
	}
	frame, err := EncodeFrame(msg)
	if err != nil {
		return err
	}
	// Per-Conn ownership: frame is freshly allocated by EncodeFrame on each
	// call. The channel reference keeps it alive until sendLoop's Write
	// completes — no defensive copy needed.
	return c.sendFrame(frame)
}

// sendFrame queues a must-deliver frame. Retained for Send and the existing
// tests that exercise the critical-overflow → close path. It is a thin wrapper
// over enqueue with droppable=false.
//
// The closed/overflow check inside enqueue is the race-safe gate that sits next
// to the channel send — necessary because Send's outer check is only a fast-path
// optimization (avoids JSON marshal). A future "cleanup" that drops either
// check would either reintroduce the marshal cost for dead conns or open a
// race where overflow flips between check and send.
func (c *Conn) sendFrame(frame []byte) error {
	return c.enqueue(frame, false)
}

// peerLabelMax caps the rendered label. quil.log rotates with a fixed archive
// count, so an unbounded label evicts unrelated records.
const peerLabelMax = 120

// peerLabel describes the far end of a connection for a log line. A Unix socket
// reports an empty remote address, so fall back to the local one and always
// name the network — "unix" versus "pipe" already separates a real daemon
// connection from an in-process test.
//
// The value is sanitized and capped even though no remote peer supplies it
// today: the daemon only listens on a Unix socket (empty RemoteAddr, so this
// falls to the local end), and a client's address is the ssh destination from
// the user's own config. It is written into a log the F1 viewer RENDERS, which
// is the same reason ssh stderr already passes through terminalSanitizer, and
// the cost of not having to re-derive that argument later is one function.
func peerLabel(raw net.Conn) string {
	if raw == nil {
		return "unknown"
	}
	if addr := raw.RemoteAddr(); addr != nil && addr.String() != "" {
		return clampLabel(addr.Network() + ":" + addr.String())
	}
	if addr := raw.LocalAddr(); addr != nil {
		return clampLabel(addr.Network()+":"+addr.String()) + " (local end)"
	}
	return "unknown"
}

// clampLabel replaces C0/DEL with '?' and truncates. '?' rather than deletion
// so a label that was tampered with still looks wrong instead of merely short.
func clampLabel(s string) string {
	if len(s) > peerLabelMax {
		s = s[:peerLabelMax]
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			b.WriteByte('?')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// enqueue queues a pre-encoded frame. The frame []byte is read-only — both
// enqueue and sendLoop only read it, never mutate it.
//
// Droppable frames (live PTY output) are dropped silently when the output queue
// is full — a busy client sheds cosmetic output (the next frame supersedes it)
// instead of being disconnected. Critical frames use the bounded critical
// queue; if THAT overflows the peer cannot drain 64 low-volume frames and is
// genuinely wedged, so it is closed (the original slow-client defense, now
// scoped to critical traffic only).
func (c *Conn) enqueue(frame []byte, droppable bool) error {
	if c.closed.Load() || c.overflow.Load() {
		return ErrSendOverflow
	}
	if droppable {
		select {
		case c.outCh <- frame:
		default:
			// Throttle the log: during an output storm one line per dropped
			// frame is noisy exactly when an operator is reading the log. The
			// counter increment stays unconditional — only the log is gated to
			// the first drop and every 256th thereafter.
			if n := c.dropped.Add(1); n == 1 || n%256 == 0 {
				logger.Debug("ipc: dropped output frame (slow client, total=%d)", n)
			}
		}
		return nil
	}
	select {
	case c.critCh <- frame:
		c.pending.Add(1)
		return nil
	default:
		// Critical buffer full — slow client. CAS the overflow flag so only
		// the first concurrent overflow spawns the Close goroutine and emits
		// the log line; all subsequent failed sends short-circuit silently.
		// Without the CAS, a wedged peer would log once per broadcast and
		// spawn N redundant Close goroutines (each no-ops via closeOnce but
		// still pays goroutine spawn cost).
		if c.overflow.CompareAndSwap(false, true) {
			// Identify the peer. This line is emitted by code shared between
			// the daemon and every client, so on its own it names neither —
			// attributing the 2026-08-09 occurrence took reasoning about which
			// binaries call logger.Init, because the only signal was which log
			// file it had landed in.
			logger.Warn("ipc: dropping slow client (critical send buffer overflow; peer=%s queued=%d cap=%d)",
				peerLabel(c.raw), len(c.critCh), sendBufSize)
			go c.Close()
		}
		return ErrSendOverflow
	}
}

// SendBlocking queues a must-deliver frame, waiting for the critical queue to
// drain below sendHeadroom instead of tripping the slow-client overflow close.
// For unicast bulk transfers (ghost replay, event replay during attach) that
// run on the sender's own goroutine: backpressure slows only this client's
// replay, while a genuinely wedged peer is still bounded by sendLoop's
// writeDeadline (deadline trips → conn closes → done fires → this returns).
// cancel (typically the daemon shutdown channel) may be nil.
//
// Without this, a freshly attached TUI busy applying workspace state was
// force-closed whenever replay volume exceeded sendBufSize frames — two full
// 256 KB ghost buffers were enough — locking the client out on every attach.
func (c *Conn) SendBlocking(msg *Message, cancel <-chan struct{}) error {
	frame, err := EncodeFrame(msg)
	if err != nil {
		return err
	}
	const pollInterval = 2 * time.Millisecond
	for {
		if c.closed.Load() || c.overflow.Load() {
			return ErrConnClosed
		}
		if len(c.critCh) < sendHeadroom {
			select {
			case c.critCh <- frame:
				// Counted like enqueue's critical path: sendLoop decrements
				// per frame written, so an enqueue that skips this drives
				// pending negative and makes Flush's `pending > 0` loop exit
				// immediately — reporting delivery it never waited for.
				c.pending.Add(1)
				return nil
			default:
				// Lost a race with concurrent broadcast enqueues — wait.
			}
		}
		select {
		case <-c.done:
			return ErrConnClosed
		case <-cancel:
			return ErrSendCanceled
		case <-time.After(pollInterval):
		}
	}
}

// Dropped returns the number of droppable (live-output) frames discarded
// because this conn's output queue was full. Test/metrics observability.
func (c *Conn) Dropped() uint64 { return c.dropped.Load() }

// sendLoop drains the two queues, draining critical first so an output flood
// can never starve state/responses.
//
// The deferred markDead makes the invariant structural rather than a property
// of each return path: this goroutine is the ONLY thing that ever drains
// critCh, so a conn that outlives it is one whose must-deliver queue can never
// move again — readable, un-writable, and silent about it. write() already
// retires the conn on the failures it owns; this covers every other way out,
// including ones added later. Idempotent, so the ordinary done path (where
// Close has already run) costs nothing.
func (c *Conn) sendLoop() {
	defer c.markDead()
	for {
		// Priority: take any pending critical frame before considering output.
		select {
		case <-c.done:
			return
		case frame := <-c.critCh:
			ok := c.write(frame)
			c.pending.Add(-1)
			if !ok {
				return
			}
			continue
		default:
		}
		select {
		case <-c.done:
			return
		case frame := <-c.critCh:
			ok := c.write(frame)
			c.pending.Add(-1)
			if !ok {
				return
			}
		case frame := <-c.outCh:
			if !c.write(frame) {
				return
			}
		}
	}
}

// write applies the per-frame write deadline and writes, CLOSING the conn on
// any failure it does not retry. Returns false once the conn is dead.
//
// The close is the load-bearing half, and its absence was a production
// incident (2026-08-11). This used to return false and let sendLoop exit on
// the theory that "the read side detects the matching error + runs handleConn's
// defer cleanup" — true for a peer disconnect, FALSE for a deadline, which is a
// local timeout on a live socket the reader sees nothing wrong with. The result
// was a conn that read forever and could never write again: the daemon went on
// accepting requests and queueing must-deliver answers for four minutes until
// critCh hit 64/64 and the overflow path closed it, while the TUI sat starved
// behind a banner claiming the daemon was gone. Two documented contracts
// depended on the close that never happened — SendBlocking's ("deadline trips →
// conn closes → done fires → this returns", which instead spun on its 2 ms poll
// until daemon shutdown) and Flush's, since pending stops decrementing the
// moment sendLoop is gone.
//
// A deadline expiry WITH PROGRESS is not a failure and must not close. The
// deadline bounds one Write call, not the peer's health: a large state frame
// into a nearly-full kernel buffer can legitimately outlast the window while
// the peer drains continuously — which is exactly the slow-but-alive TUI the
// incident dropped. Zero bytes moved in a whole window is the wedge; anything
// else is backpressure, and the enqueue-side overflow close remains the bound
// on a peer that is merely too slow to keep up.
//
// The retry MUST resume at frame[n:]. Write reports how much it placed before
// giving up, so re-sending the whole frame would duplicate that prefix and
// desynchronise a length-prefixed stream — corruption instead of a drop.
//
// A refused write deadline is NOT fatal, but it is no longer silent. The ssh
// transport (transport.stdioConn) answers os.ErrNoDeadline for every frame,
// because Windows non-overlapped pipe handles cannot carry one — so remote
// conns run with no deadline at all and the enqueue-side overflow close is
// their only bound. That is a real gap in the ceiling this function otherwise
// provides, and discarding the error left it discoverable only by reading the
// transport (techdebt 3-2-conn-write-deadline-absent-over-ssh). It is reported
// once per conn instead.
func (c *Conn) write(frame []byte) bool {
	for {
		if derr := c.raw.SetWriteDeadline(time.Now().Add(c.writeWindow)); derr != nil {
			c.reportDeadlineRefused(derr)
		}
		n, err := c.raw.Write(frame)
		if err == nil {
			return true
		}
		frame = frame[n:]
		if errors.Is(err, os.ErrDeadlineExceeded) && n > 0 {
			continue // draining, just slower than one window — not wedged
		}
		logger.Warn("ipc: write failed, retiring conn (peer=%s undelivered=%dB): %v",
			peerLabel(c.raw), len(frame), err)
		// SYNCHRONOUS, never handed to another goroutine: closed must be true
		// before sendLoop exits. Retiring asynchronously leaves a window where
		// the drain goroutine is already gone while SendBlocking's closed guard
		// still passes — the window the client-side half of the incident lived
		// in, where every send paid the full clientSendTimeout for five hours.
		c.markDead()
		return false
	}
}

// reportDeadlineRefused says once, per conn, that this transport will not carry
// a write deadline — so the missing ceiling is visible in the log rather than
// inferred from reading the transport.
//
// Once per conn, not once per frame: sendLoop calls SetWriteDeadline before
// EVERY write, and on ssh every one of them fails, so an unguarded line would
// be one log record per frame for the life of the session.
//
// os.ErrNoDeadline is DEBUG because it is a documented property of a supported
// transport rather than a fault — an operator reading a remote session's log at
// info level does not need it, and a warning per remote conn would train them
// to ignore the level. Anything else is a Unix socket refusing something it
// should support, which is genuinely unexpected.
func (c *Conn) reportDeadlineRefused(err error) {
	if !c.deadlineRefused.CompareAndSwap(false, true) {
		return
	}
	if errors.Is(err, os.ErrNoDeadline) {
		logger.Debug("ipc: transport carries no write deadline (peer=%s); the critical-queue "+
			"overflow close is the only bound on a wedged write here", peerLabel(c.raw))
		return
	}
	logger.Warn("ipc: write deadline not installed (peer=%s): %v", peerLabel(c.raw), err)
}

// Receive reads the next message from the connection. Callers must ensure a
// single reader at a time per conn — daemon: handleConn's goroutine; client:
// the version handshake, then the receive loop, sequentially — so br needs
// no locking.
func (c *Conn) Receive() (*Message, error) {
	return ReadMessage(c.br)
}

// Close shuts down the conn. Idempotent — safe to call concurrently from any
// goroutine. Any frames still queued in critCh or outCh at close time are
// intentionally discarded: by the time Close is called we are either tearing
// down an overflowed (already broken) peer or shutting down the server
// entirely, and in both cases delivery guarantees no longer apply.
// Flush waits until every must-deliver frame accepted by Send has reached the
// socket, or until timeout. Call it before Close when the frames still queued
// matter: Close signals done, and sendLoop returns on done without writing what
// is left, so closing straight after Send discards frames the caller was told
// were accepted. The TUI exit path pairs the two — final keystrokes are exactly
// this case.
//
// Droppable output frames are deliberately not counted; they are droppable by
// design and a busy pane could keep the count above zero indefinitely.
//
// Returns true if the queue drained. A closed connection returns immediately:
// sendLoop is gone, so nothing further will ever be written and waiting could
// only burn the whole timeout.
func (c *Conn) Flush(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for c.pending.Load() > 0 {
		if c.closed.Load() || time.Now().After(deadline) {
			return false
		}
		time.Sleep(flushPollInterval)
	}
	return true
}

func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.markDead()
		err = c.raw.Close()
	})
	return err
}

// markDead retires the conn WITHOUT releasing the underlying transport.
//
// This is the half of teardown the IPC layer owns: stop accepting sends, unpark
// everyone waiting, and make every guard report the conn as gone. Releasing the
// transport is the OWNER's half, and the two must stay apart — on ssh,
// raw.Close() kills and reaps the child, and cmd/quil's redial path has to read
// LinkErr BEFORE that (Close can clear pumpErr) and ExitCode AFTER (Close is
// what makes the status final). A first attempt at this fix closed raw straight
// from write(), which put the IPC layer ahead of that sequence: the ssh stderr
// was cleared before anyone read it, so ClassifyLinkFailure saw empty text,
// never parked, and a permanently-rejected key would retry until fail2ban
// noticed. TestRedialRemote_ReadsLinkErrBeforeCloseAndExitCodeAfter caught it.
//
// Unparking a parked Receive is a READ DEADLINE, not a socket close, for the
// same reason. The daemon's handleConn then errors out of Receive and runs its
// own defer, which is where the real Close, removeConn and onDisconnect belong.
//
// That unpark is load-bearing on the DAEMON side and best-effort elsewhere, and
// the distinction is worth stating rather than implying. The daemon serves Unix
// sockets, where the poller applies a deadline to a read that is already
// blocked — which is the case this whole change is about. stdioConn (ssh) also
// implements read deadlines, but its Read builds its timer from the deadline it
// observes ON ENTRY, so one installed while a read is already parked is not
// seen until the next call. Nothing is lost there: a write failing on that
// transport means the pipe to ssh is broken, so the pump's own read is about to
// fail anyway, and the client additionally bounds itself with
// clientSendTimeout. Retiring the conn is still correct and immediate — only
// the unpark is deferred to whatever ends that read.
//
// The deadline error is IGNORED rather than escalated to a close. Every
// transport here implements read deadlines on a live connection, so the error
// means the fd is already gone — a reader on it is already unparked or about to
// be, and closing again would only re-introduce the ordering violation above.
// (Closing on the error was tried: net.Pipe reports io.ErrClosedPipe once
// either end is done, so the fallback fired on exactly the dead-transport case
// the ordering tests exercise.)
func (c *Conn) markDead() {
	c.deadOnce.Do(func() {
		c.closed.Store(true)
		close(c.done)
		_ = c.raw.SetReadDeadline(time.Now())
	})
}

// Server listens for client connections over a Unix socket.
type Server struct {
	path         string
	handler      MessageHandler
	onDisconnect func(*Conn) // called when a client disconnects
	listener     net.Listener
	conns        []*Conn
	mu           sync.Mutex
	done         chan struct{}
}

func NewServer(socketPath string, handler MessageHandler, onDisconnect func(*Conn)) *Server {
	return &Server{
		path:         socketPath,
		handler:      handler,
		onDisconnect: onDisconnect,
		done:         make(chan struct{}),
	}
}

func (s *Server) Start() error {
	os.Remove(s.path) // Clean up stale socket

	ln, err := net.Listen("unix", s.path)
	if err != nil {
		return err
	}
	os.Chmod(s.path, 0600) // restrict socket permissions
	s.listener = ln

	go s.acceptLoop()
	return nil
}

// Stop closes the listener and all active connections. Frames queued in any
// conn's send buffer at the moment of Stop are discarded — Daemon.Stop's
// shutdown sequence does not rely on a final IPC broadcast reaching clients
// (the final-snapshot durability lives in the on-disk workspace.json path,
// not in the wire).
func (s *Server) Stop() error {
	close(s.done)
	s.mu.Lock()
	for _, c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	return s.listener.Close()
}

// ConnCount returns the number of currently-connected clients. Test-friendly
// alternative to the existing log-line scraping pattern; used to wait for
// connect/disconnect events without time-based sleeps.
func (s *Server) ConnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// Broadcast sends a message to all connected clients without blocking on any
// individual conn. Marshals the wire frame once and shares the bytes across
// all per-conn send queues. Live PTY output (MsgPaneOutput) is enqueued as
// droppable — a slow conn sheds it without being closed. All other message
// types are critical: a slow or wedged conn that overflows its critical queue
// is dropped from the fan-out (logged once, per CAS-guarded enqueue) without
// affecting the others.
func (s *Server) Broadcast(msg *Message) {
	frame, err := EncodeFrame(msg)
	if err != nil {
		logger.Error("ipc: broadcast marshal: %v", err)
		return
	}
	// frame is freshly allocated by EncodeFrame on each Broadcast call.
	// All per-conn sendLoops share the same slice read-only — no clone needed.

	// IMPORTANT: do not remove the slice copy below. The `conns` snapshot
	// must be independent of s.conns so the lock-free fan-out cannot race
	// with accept/removeConn mutations. Reusing s.conns directly here would
	// reintroduce the slow-conn-blocks-everyone bug this whole rewrite fixed.
	s.mu.Lock()
	conns := make([]*Conn, len(s.conns))
	copy(conns, s.conns)
	s.mu.Unlock()

	// Live PTY output is droppable: a busy client sheds it (the next frame
	// supersedes it) rather than being force-closed. Everything else is
	// must-deliver and routes to the critical queue (overflow → close).
	droppable := msg.Type == MsgPaneOutput
	for _, c := range conns {
		// A subscriber that excused itself from the live PTY stream (the MCP
		// bridge, which decodes every frame only to discard it) is skipped
		// before the frame reaches its queue.
		if !c.wantsFrame(msg.Type) {
			continue
		}
		if err := c.enqueue(frame, droppable); err != nil && !errors.Is(err, ErrSendOverflow) {
			// ErrSendOverflow is already logged at the overflow site (CAS
			// guarantees exactly one log per conn). Any other error is
			// genuinely unexpected.
			logger.Error("ipc: broadcast send: %v", err)
		}
	}
}

// SetPaneOutputWanted records whether this conn wants the live pane-output
// stream. Exported for the daemon's subscribe handler.
func (c *Conn) SetPaneOutputWanted(want bool) { c.setPaneOutputWanted(want) }

// PaneOutputWanted reports the current setting. Exported so the handler can log
// only on a real change rather than on every message it receives.
func (c *Conn) PaneOutputWanted() bool { return c.wantsPaneOutput() }

func (c *Conn) setPaneOutputWanted(want bool) { c.noPaneOutput.Store(!want) }

func (c *Conn) wantsPaneOutput() bool { return !c.noPaneOutput.Load() }

// wantsFrame reports whether a frame of this type should be delivered to this
// conn. Only the live pane-output stream is ever filtered: everything else is
// must-deliver, and a client excusing itself from PTY bytes still needs
// workspace state, its own responses, and lifecycle frames.
func (c *Conn) wantsFrame(msgType string) bool {
	if msgType != MsgPaneOutput {
		return true
	}
	return c.wantsPaneOutput()
}

func (s *Server) acceptLoop() {
	for {
		raw, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
				logger.Error("ipc: accept error: %v", err)
				continue
			}
		}

		conn := newConn(raw)
		s.mu.Lock()
		s.conns = append(s.conns, conn)
		count := len(s.conns)
		s.mu.Unlock()

		logger.Info("ipc: client connected (total=%d)", count)
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn *Conn) {
	defer func() {
		conn.Close()
		s.removeConn(conn)
		s.mu.Lock()
		count := len(s.conns)
		s.mu.Unlock()
		logger.Info("ipc: client disconnected (remaining=%d)", count)
		if s.onDisconnect != nil {
			s.onDisconnect(conn)
		}
	}()

	for {
		msg, err := conn.Receive()
		if err != nil {
			return
		}
		s.handler(conn, msg)
	}
}

func (s *Server) removeConn(conn *Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.conns {
		if c == conn {
			s.conns = append(s.conns[:i], s.conns[i+1:]...)
			return
		}
	}
}
