package ipc

import (
	"bytes"
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

// captureLogs sends the logger to a buffer at level and puts the previous
// logger back when the test ends. Not for a t.Parallel test: the logger is
// process-wide.
func captureLogs(t *testing.T, level string) *lockedBuffer {
	t.Helper()
	buf := new(lockedBuffer)
	t.Cleanup(logger.Save())
	logger.Init(level, buf)
	return buf
}

// refuseThree fills the pending cap and has three more conns refused at
// accept: one logged line, two held back.
func refuseThree(t *testing.T, h *tcpHarness) {
	t.Helper()
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
}

// A peer that opens conns in a loop past the pending cap gets one quild.log
// line per minute, not one per conn.
func TestStartTCP_RefusalLogLimited(t *testing.T) {
	buf := captureLogs(t, "debug")
	h := newTCPHarness(t)
	refuseThree(t, h)
	if n := strings.Count(buf.String(), "tcp conn refused at accept"); n != 1 {
		t.Fatalf("%d refusal lines for 3 refused conns in a minute, want 1", n)
	}
}

// The count held back is logged when the listener closes, so the end of a
// flood is not lost.
func TestStartTCP_RefusalCountFlushedAtClose(t *testing.T) {
	buf := captureLogs(t, "info")
	h := newTCPHarness(t)
	refuseThree(t, h)
	h.s.Stop()
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(buf.String(), "2 refusals at accept were not logged") {
		if time.Now().After(deadline) {
			t.Fatalf("no flush line after Stop; log:\n%s", buf.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A loopback connect/close loop stays under the pending cap, so the refusal
// limit never runs. Such conns never logged in and must write no Info line
// (connect or disconnect); a login is logged at Info.
func TestStartTCP_UnauthenticatedConnsLogNoInfo(t *testing.T) {
	buf := captureLogs(t, "info")
	h := newTCPHarness(t)
	const n = 5
	for i := 0; i < n; i++ {
		h.dial(t).Close()
	}
	h.waitAccepted(t, n)
	if !h.s.WaitConns(3 * time.Second) {
		t.Fatal("the closed conns' handlers did not return")
	}
	if out := buf.String(); strings.Contains(out, "client connected") || strings.Contains(out, "client disconnected") {
		t.Fatalf("Info lines for %d conns that never logged in:\n%s", n, out)
	}

	h.dial(t)
	c := h.waitAccepted(t, n+1)
	if !c.MarkAuthenticated(NewTokenAuth("0a1b2c3d", "t", RightsFull)) {
		t.Fatal("login refused")
	}
	if got := strings.Count(buf.String(), "tcp client logged in"); got != 1 {
		t.Fatalf("%d login lines, want 1", got)
	}
	// The line names who logged in: the peer and the token, or two logins
	// from one host read the same in quild.log.
	if out := buf.String(); !strings.Contains(out, "token=0a1b2c3d") || !strings.Contains(out, "peer=tcp:127.0.0.1:") {
		t.Fatalf("the login line names neither peer nor token:\n%s", out)
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
