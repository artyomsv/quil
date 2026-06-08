package ipc

import (
	"bytes"
	"errors"
	"net"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/artyomsv/quil/internal/logger"
)

// sendBufSize is the per-connection queue depth. A wedged or slow client can
// build up at most this many in-flight frames before the server marks the
// connection as overflowed and tears it down — guaranteeing that one bad
// client cannot block the daemon's broadcast loop and starve healthy peers.
//
// 64 frames is comfortably more than any healthy client lags by (a TUI's
// Bubble Tea event loop typically drains everything in <50 ms) and small
// enough that an overflowed conn is detected within a few milliseconds.
const sendBufSize = 64

// writeDeadline bounds how long a single raw.Write may block inside sendLoop
// before we give up on the peer. Belt-and-suspenders alongside the sendCh
// overflow detection: under a wedged kernel buffer + a peer that doesn't
// error on TCP RST, the overflow path is still triggered (sendCh fills →
// next sendFrame trips overflow), but the deadline guarantees a deterministic
// cleanup ceiling instead of an indefinite block.
const writeDeadline = 30 * time.Second

// ErrSendOverflow is returned by Conn.Send when the per-conn send buffer is
// full. The connection has been scheduled for close; future Sends short-
// circuit with the same error.
var ErrSendOverflow = errors.New("ipc: send buffer overflow (slow client)")

// MessageHandler is called for each incoming message on a connection.
type MessageHandler func(conn *Conn, msg *Message)

// Conn wraps a net.Conn with message framing.
//
// Sends are non-blocking: each Conn owns a 64-slot queue and a dedicated
// goroutine that drains the queue into the underlying socket. A slow or
// wedged peer drains its own queue; if the queue overflows, the offending
// conn is closed in the background and Send returns ErrSendOverflow. Other
// connections are never affected by one client's slowness — closing the
// wedge-incident class where a single stuck TUI or MCP bridge stalled the
// daemon's broadcast for every other client.
type Conn struct {
	raw       net.Conn
	sendCh    chan []byte
	done      chan struct{}
	closeOnce sync.Once
	closed    atomic.Bool
	overflow  atomic.Bool
}

func newConn(raw net.Conn) *Conn {
	c := &Conn{
		raw:    raw,
		sendCh: make(chan []byte, sendBufSize),
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
	var buf bytes.Buffer
	if err := WriteMessage(&buf, msg); err != nil {
		return err
	}
	// Per-Conn ownership: buf.Bytes() backs a stack-local Buffer whose
	// lifetime ends when Send returns, but the channel reference keeps the
	// backing array alive until sendLoop's Write completes. No defensive
	// copy needed here — only Broadcast (which fans the same frame across
	// N conns) clones to decouple the shared slice from its source.
	return c.sendFrame(buf.Bytes())
}

// sendFrame queues a pre-encoded wire frame. Used by Broadcast to share one
// marshal allocation across N conns. The frame []byte is read-only — both
// sendFrame and sendLoop only read it, never mutate it.
//
// The closed/overflow check here is the race-safe gate that sits next to the
// channel send — necessary because Send's outer check is only a fast-path
// optimization (avoids JSON marshal). A future "cleanup" that drops either
// check would either reintroduce the marshal cost for dead conns or open a
// race where overflow flips between check and send.
func (c *Conn) sendFrame(frame []byte) error {
	if c.closed.Load() || c.overflow.Load() {
		return ErrSendOverflow
	}
	select {
	case c.sendCh <- frame:
		return nil
	default:
		// Buffer full — slow client. CAS the overflow flag so only the
		// first concurrent overflow spawns the Close goroutine and emits
		// the log line; all subsequent failed sends short-circuit silently.
		// Without the CAS, a wedged peer would log once per broadcast and
		// spawn N redundant Close goroutines (each no-ops via closeOnce
		// but still pays goroutine spawn cost).
		if c.overflow.CompareAndSwap(false, true) {
			logger.Warn("ipc: dropping slow client (send buffer overflow)")
			go c.Close()
		}
		return ErrSendOverflow
	}
}

func (c *Conn) sendLoop() {
	for {
		select {
		case <-c.done:
			return
		case frame := <-c.sendCh:
			// Bound the per-frame Write to writeDeadline so a peer with a
			// wedged kernel buffer or stalled connection cannot block this
			// goroutine indefinitely. Deadline errors are reported the same
			// way as any other Write failure — sendLoop exits, the read
			// side detects the matching error and runs handleConn's defer.
			_ = c.raw.SetWriteDeadline(time.Now().Add(writeDeadline))
			if _, err := c.raw.Write(frame); err != nil {
				return
			}
		}
	}
}

func (c *Conn) Receive() (*Message, error) {
	return ReadMessage(c.raw)
}

// Close shuts down the conn. Idempotent — safe to call concurrently from any
// goroutine. Any frames still queued in sendCh at close time are intentionally
// discarded: by the time Close is called we are either tearing down an
// overflowed (already broken) peer or shutting down the server entirely, and
// in both cases delivery guarantees no longer apply.
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
// all per-conn send queues. A slow or wedged conn is dropped from the fan-out
// (logged once, per CAS-guarded sendFrame) without affecting the others.
func (s *Server) Broadcast(msg *Message) {
	var buf bytes.Buffer
	if err := WriteMessage(&buf, msg); err != nil {
		logger.Error("ipc: broadcast marshal: %v", err)
		return
	}
	// IMPORTANT: clone the bytes BEFORE fan-out. The slice returned by
	// buf.Bytes() aliases a stack-local Buffer whose backing array would
	// today survive via channel references — but if a future contributor
	// pools the Buffer (sync.Pool, freelist), reuse would silently corrupt
	// frames still being read by per-conn sendLoops. The clone decouples
	// the shared frame from its source so the contract holds across any
	// future Buffer reuse strategy.
	// TODO(perf): if broadcast rate ever dominates daemon CPU, consider a
	// sync.Pool of [][]byte AND remove this clone — but then every callsite
	// that aliases the frame must be re-audited.
	frame := slices.Clone(buf.Bytes())

	// IMPORTANT: do not remove the slice copy below. The `conns` snapshot
	// must be independent of s.conns so the lock-free fan-out cannot race
	// with accept/removeConn mutations. Reusing s.conns directly here would
	// reintroduce the slow-conn-blocks-everyone bug this whole rewrite fixed.
	s.mu.Lock()
	conns := make([]*Conn, len(s.conns))
	copy(conns, s.conns)
	s.mu.Unlock()

	for _, c := range conns {
		if err := c.sendFrame(frame); err != nil && !errors.Is(err, ErrSendOverflow) {
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
