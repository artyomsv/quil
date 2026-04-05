package ipc

import (
	"log"
	"net"
	"os"
	"sync"
)

// MessageHandler is called for each incoming message on a connection.
type MessageHandler func(conn *Conn, msg *Message)

// Conn wraps a net.Conn with message framing.
type Conn struct {
	raw net.Conn
	mu  sync.Mutex
}

func newConn(raw net.Conn) *Conn {
	return &Conn{raw: raw}
}

func (c *Conn) Send(msg *Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return WriteMessage(c.raw, msg)
}

func (c *Conn) Receive() (*Message, error) {
	return ReadMessage(c.raw)
}

func (c *Conn) Close() error {
	return c.raw.Close()
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

// Broadcast sends a message to all connected clients.
func (s *Server) Broadcast(msg *Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		if err := c.Send(msg); err != nil {
			log.Printf("broadcast send: %v", err)
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
