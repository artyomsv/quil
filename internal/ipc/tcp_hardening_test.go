package ipc

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/logger"
)

// A zero Conn has no auth state stored and no transport. It is not a TCP
// conn, so it must refuse a login rather than store one.
func TestMarkAuthenticated_ZeroConnRefused(t *testing.T) {
	c := new(Conn)
	if c.MarkAuthenticated(NewTokenAuth("0a1b2c3d", "t", RightsReadOnly)) {
		t.Fatal("a zero Conn accepted a token login")
	}
	if a := c.Auth(); a == nil || a.Transport != TransportLocal || a.Level != RightsFull {
		t.Fatalf("zero Conn auth after a refused login = %+v, want local full", a)
	}
}

// nil is a TCP conn before login: nobody, never "local".
func TestAuthState_NilPrincipalIsEmpty(t *testing.T) {
	var a *AuthState
	if p := a.Principal(); p != "" {
		t.Fatalf("nil Principal() = %q, want empty", p)
	}
}

func TestStartTCP_SecondCallRefused(t *testing.T) {
	h := newTCPHarness(t)
	if addr, err := h.s.StartTCP("127.0.0.1:0", TCPHooks{}); err == nil {
		t.Fatalf("a second StartTCP bound %s", addr)
	}
	if got := h.s.TCPAddr(); got == nil || got.String() != h.addr {
		t.Fatalf("TCPAddr = %v after a refused second call, want the first listener %s", got, h.addr)
	}
}

func TestStartTCP_AfterStopRefused(t *testing.T) {
	s := NewServer(filepath.Join(t.TempDir(), "s"), func(*Conn, *Message) {}, nil)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	s.Stop()
	if addr, err := s.StartTCP("127.0.0.1:0", TCPHooks{}); err == nil {
		t.Fatalf("StartTCP after Stop bound %s", addr)
	}
	if got := s.TCPAddr(); got != nil {
		t.Fatalf("TCPAddr = %v after Stop, want nil", got)
	}
}

// The host must be a loopback IP literal, refused before the bind. A name is
// refused even when it resolves to loopback: the resolver must not pick the
// bind address. The error text is the pre-bind one, not the post-bind check.
func TestStartTCP_AddressCheckedBeforeBind(t *testing.T) {
	for _, addr := range []string{"localhost:0", "0.0.0.0:0", "192.0.2.1:0", "no-port"} {
		s := NewServer(filepath.Join(t.TempDir(), "s"), func(*Conn, *Message) {}, nil)
		bound, err := s.StartTCP(addr, TCPHooks{})
		if err == nil {
			s.Stop()
			t.Errorf("StartTCP(%q) bound %s", addr, bound)
			continue
		}
		if !strings.Contains(err.Error(), "TCP listener address") {
			t.Errorf("StartTCP(%q) = %v, want the pre-bind refusal", addr, err)
		}
		if s.TCPAddr() != nil {
			t.Errorf("StartTCP(%q) left a listener", addr)
		}
		s.Stop()
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// A peer that opens conns in a loop past the pending cap gets one quild.log
// line per minute, not one per conn.
func TestStartTCP_RefusalLogLimited(t *testing.T) {
	var buf lockedBuffer
	logger.Init("debug", &buf)
	t.Cleanup(func() { logger.Init("info", io.Discard) })
	h := newTCPHarness(t)
	for i := 0; i < MaxPendingTCP; i++ {
		h.dial(t)
	}
	h.waitAccepted(t, MaxPendingTCP)
	const refused = 3
	for i := 0; i < refused; i++ {
		h.dial(t)
	}
	if rej := h.waitRejected(t, refused); len(rej) != refused {
		t.Fatalf("rejected = %v, want %d", rej, refused)
	}
	if n := strings.Count(buf.String(), "tcp conn refused at accept"); n != 1 {
		t.Fatalf("%d refusal lines for %d refused conns in a minute, want 1", n, refused)
	}
}

func TestRefusalLog_ReportsSuppressedCount(t *testing.T) {
	var r refusalLog
	t0 := time.Unix(1000, 0)
	if ok, n := r.note(t0); !ok || n != 0 {
		t.Fatalf("first = %v, %d; want logged, 0", ok, n)
	}
	for i := 1; i <= 2; i++ {
		if ok, _ := r.note(t0.Add(time.Duration(i) * time.Second)); ok {
			t.Fatalf("refusal %d inside the window was logged", i)
		}
	}
	if ok, n := r.note(t0.Add(refusalLogWindow)); !ok || n != 2 {
		t.Fatalf("after the window = %v, %d; want logged, 2", ok, n)
	}
}
