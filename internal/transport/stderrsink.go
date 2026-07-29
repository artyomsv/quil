package transport

import (
	"io"
	"sync"
)

// switchWriter is an io.Writer whose destination can change while writes are in
// flight.
//
// ssh's stderr must reach the terminal during the dial — that is where host-key
// confirmation and passphrase prompts appear — and must stop reaching it the
// moment the TUI takes over the screen, because ssh keeps that descriptor for
// the whole session and a late diagnostic would land mid-render.
//
// A mutex rather than an atomic pointer: writes are rare (ssh diagnostics only),
// and the lock gives Set a happens-before edge with any in-flight Write, so a
// swap cannot interleave inside a single message.
type switchWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// Write sends p to the current sink.
//
// A nil sink discards and reports a full write. exec's copier goroutine outlives
// the TUI's log file, so there is a window where the sink is legitimately gone;
// returning an error there would only be reported to the writer that just went
// away, and returning a short count would make the copier retry forever.
func (s *switchWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	w := s.w
	s.mu.Unlock()
	if w == nil {
		return len(p), nil
	}
	return w.Write(p)
}

// Set redirects subsequent writes. Pass nil to discard them.
func (s *switchWriter) Set(w io.Writer) {
	s.mu.Lock()
	s.w = w
	s.mu.Unlock()
}

// StderrRedirector is implemented by transports holding a child process whose
// stderr is attached to the operator's terminal. cmd/quil asserts for it after
// dialling, the same way it asserts for LinkStatus.
type StderrRedirector interface {
	// RedirectStderr sends any further child diagnostics to w. Pass nil to
	// discard them entirely.
	RedirectStderr(w io.Writer)
}

// Compile-time assertion: the TUI's redirect is silently disabled if this ever
// stops holding, and a missed redirect shows up only as corrupted rendering
// during an ssh failure — the hardest case to reproduce deliberately.
var _ StderrRedirector = (*stdioConn)(nil)

// RedirectStderr moves ssh's diagnostics off the terminal.
//
// No-op on a batch dial, where stderr was captured into a buffer for the error
// message and never reached the screen in the first place.
func (c *stdioConn) RedirectStderr(w io.Writer) {
	if c.termErr != nil {
		c.termErr.Set(w)
	}
}
