package ipc

import (
	"bufio"
	"errors"
	"net"
	"os"
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

// writeDeadline bounds how long a single raw.Write may block inside sendLoop
// before we give up on the peer. Belt-and-suspenders alongside the critCh
// overflow detection: under a wedged kernel buffer + a peer that doesn't
// error on TCP RST, the overflow path is still triggered (critCh fills →
// next critical send trips overflow → close), but the deadline guarantees a
// deterministic cleanup ceiling instead of an indefinite block. Overflow-close
// applies only to the critical queue; droppable output (outCh) is shed when
// full and never triggers a close.
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
	closed    atomic.Bool
	overflow  atomic.Bool
	dropped   atomic.Uint64
}

func newConn(raw net.Conn) *Conn {
	c := &Conn{
		raw:    raw,
		br:     bufio.NewReader(raw),
		critCh: make(chan []byte, sendBufSize),
		outCh:  make(chan []byte, sendBufSize),
		done:   make(chan struct{}),
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
		return nil
	default:
		// Critical buffer full — slow client. CAS the overflow flag so only
		// the first concurrent overflow spawns the Close goroutine and emits
		// the log line; all subsequent failed sends short-circuit silently.
		// Without the CAS, a wedged peer would log once per broadcast and
		// spawn N redundant Close goroutines (each no-ops via closeOnce but
		// still pays goroutine spawn cost).
		if c.overflow.CompareAndSwap(false, true) {
			logger.Warn("ipc: dropping slow client (critical send buffer overflow)")
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
func (c *Conn) sendLoop() {
	for {
		// Priority: take any pending critical frame before considering output.
		select {
		case <-c.done:
			return
		case frame := <-c.critCh:
			if !c.write(frame) {
				return
			}
			continue
		default:
		}
		select {
		case <-c.done:
			return
		case frame := <-c.critCh:
			if !c.write(frame) {
				return
			}
		case frame := <-c.outCh:
			if !c.write(frame) {
				return
			}
		}
	}
}

// write applies the per-frame write deadline and writes. Returns false on any
// error so sendLoop exits (the read side detects the matching error + runs
// handleConn's defer cleanup). Bounding each Write to writeDeadline prevents a
// peer with a wedged kernel buffer or stalled connection from blocking this
// goroutine indefinitely.
func (c *Conn) write(frame []byte) bool {
	_ = c.raw.SetWriteDeadline(time.Now().Add(writeDeadline))
	_, err := c.raw.Write(frame)
	return err == nil
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
func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		close(c.done)
		err = c.raw.Close()
	})
	return err
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
		if err := c.enqueue(frame, droppable); err != nil && !errors.Is(err, ErrSendOverflow) {
			// ErrSendOverflow is already logged at the overflow site (CAS
			// guarantees exactly one log per conn). Any other error is
			// genuinely unexpected.
			logger.Error("ipc: broadcast send: %v", err)
		}
	}
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
