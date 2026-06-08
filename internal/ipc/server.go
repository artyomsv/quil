package ipc

import (
	"bytes"
	"errors"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
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
func (c *Conn) Send(msg *Message) error {
	if c.closed.Load() || c.overflow.Load() {
		return ErrSendOverflow
	}
	var buf bytes.Buffer
	if err := WriteMessage(&buf, msg); err != nil {
		return err
	}
	return c.sendFrame(buf.Bytes())
}

// sendFrame queues a pre-encoded wire frame. Used by Broadcast to share one
// marshal allocation across N conns. The frame []byte is read-only — both
// sendFrame and sendLoop only read it, never mutate it.
func (c *Conn) sendFrame(frame []byte) error {
	if c.closed.Load() || c.overflow.Load() {
		return ErrSendOverflow
	}
	select {
	case c.sendCh <- frame:
		return nil
	default:
		// Buffer full — slow client. Tear it down asynchronously so the
		// broadcaster never blocks on the close, and short-circuit all
		// future Sends.
		c.overflow.Store(true)
		go c.Close()
		return ErrSendOverflow
	}
}

func (c *Conn) sendLoop() {
	for {
		select {
		case <-c.done:
			return
		case frame := <-c.sendCh:
			if _, err := c.raw.Write(frame); err != nil {
				// Peer gone or socket error — exit. The read side will see
				// the matching error and clean up via handleConn's defer.
				return
			}
		}
	}
}

func (c *Conn) Receive() (*Message, error) {
	return ReadMessage(c.raw)
}

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

func (s *Server) Stop() error {
	close(s.done)
	s.mu.Lock()
	for _, c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	return s.listener.Close()
}

// Broadcast sends a message to all connected clients without blocking on any
// individual conn. Marshals the wire frame once and shares the bytes across
// all per-conn send queues. A slow or wedged conn is dropped from the fan-out
// (logged once) without affecting the others.
func (s *Server) Broadcast(msg *Message) {
	var buf bytes.Buffer
	if err := WriteMessage(&buf, msg); err != nil {
		log.Printf("broadcast marshal: %v", err)
		return
	}
	frame := buf.Bytes()

	// Snapshot the conns list under the lock so the per-conn sendFrame calls
	// below run lock-free — no risk of a slow send chain interleaving with
	// accept/disconnect bookkeeping.
	s.mu.Lock()
	conns := make([]*Conn, len(s.conns))
	copy(conns, s.conns)
	s.mu.Unlock()

	for _, c := range conns {
		if err := c.sendFrame(frame); err != nil {
			if errors.Is(err, ErrSendOverflow) {
				log.Printf("ipc: dropping slow client (send buffer overflow)")
			} else {
				log.Printf("broadcast send: %v", err)
			}
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
				log.Printf("accept error: %v", err)
				continue
			}
		}

		conn := newConn(raw)
		s.mu.Lock()
		s.conns = append(s.conns, conn)
		count := len(s.conns)
		s.mu.Unlock()

		log.Printf("ipc: client connected (total=%d)", count)
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
		log.Printf("ipc: client disconnected (remaining=%d)", count)
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
