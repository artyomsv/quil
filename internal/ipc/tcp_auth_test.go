package ipc

import (
	"encoding/binary"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type tcpHarness struct {
	s        *Server
	sock     string
	addr     string
	mu       sync.Mutex
	accepted []*Conn
	rejected []string
	got      chan *Message
}

func newTCPHarness(t *testing.T) *tcpHarness {
	t.Helper()
	h := &tcpHarness{got: make(chan *Message, 16)}
	h.sock = filepath.Join(t.TempDir(), "s")
	h.s = NewServer(h.sock, func(_ *Conn, m *Message) { h.got <- m }, nil)
	if err := h.s.Start(); err != nil {
		t.Fatal(err)
	}
	addr, err := h.s.StartTCP("127.0.0.1:0", TCPHooks{
		Accepted: func(c *Conn) { h.mu.Lock(); h.accepted = append(h.accepted, c); h.mu.Unlock() },
		Rejected: func(r string, _ *Conn) { h.mu.Lock(); h.rejected = append(h.rejected, r); h.mu.Unlock() },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.addr = addr.String()
	t.Cleanup(func() { h.s.Stop() })
	return h
}

func (h *tcpHarness) dial(t *testing.T) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func (h *tcpHarness) waitAccepted(t *testing.T, n int) *Conn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		if len(h.accepted) >= n {
			c := h.accepted[n-1]
			h.mu.Unlock()
			return c
		}
		h.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("fewer than %d TCP conns accepted", n)
	return nil
}

// waitRejected polls until the Rejected hook has run n times and returns a
// copy. The hook runs on the accept or conn goroutine, with no ordering
// against what the client observes (a close at accept can reach the client
// before the hook appends), so every test that reads h.rejected waits here
// rather than reading once.
func (h *tcpHarness) waitRejected(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.mu.Lock()
		rej := append([]string(nil), h.rejected...)
		h.mu.Unlock()
		if len(rej) >= n || !time.Now().Before(deadline) {
			return rej
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// readsNothing reports whether raw delivers zero bytes within d.
func readsNothing(raw net.Conn, d time.Duration) bool {
	raw.SetReadDeadline(time.Now().Add(d))
	defer raw.SetReadDeadline(time.Time{})
	var b [1]byte
	n, err := raw.Read(b[:])
	var ne net.Error
	return n == 0 && errors.As(err, &ne) && ne.Timeout()
}

func TestConn_LocalReadsFullAuth(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	piped := newConnWithWriteWindow(a, writeDeadline)
	defer piped.Close()
	for _, c := range []*Conn{nil, new(Conn), piped} {
		a := c.Auth()
		if a == nil || a.Level != RightsFull || a.Transport != TransportLocal || a.Principal() != PrincipalLocal {
			t.Fatalf("local auth = %+v", a)
		}
	}
	if piped.MarkAuthenticated(NewTokenAuth("0a1b2c3d", "t", RightsReadOnly)) {
		t.Fatal("a local conn accepted a token login")
	}
}

// Each local conn has its OWN auth state, so the refusal-audit rate state is
// per conn and dies with it.
func TestConn_LocalAuthIsPerConn(t *testing.T) {
	a1, b1 := net.Pipe()
	defer b1.Close()
	a2, b2 := net.Pipe()
	defer b2.Close()
	one, two := newConnWithWriteWindow(a1, writeDeadline), newConnWithWriteWindow(a2, writeDeadline)
	defer one.Close()
	defer two.Close()
	if one.Auth() == two.Auth() {
		t.Fatal("two local conns share one auth state")
	}
	if one.Auth() != one.Auth() {
		t.Fatal("a local conn's auth state changed between reads")
	}
	zero := new(Conn)
	if zero.Auth() != zero.Auth() {
		t.Fatal("a zero Conn's auth state changed between reads")
	}
	now := time.Unix(1_800_000_000, 0)
	if !one.Auth().ShouldAuditRefusal(MsgPaneInput, now) || !two.Auth().ShouldAuditRefusal(MsgPaneInput, now) {
		t.Fatal("one local conn's refusal audit rate-limited another's")
	}
}

// Stop is idempotent. Several tests here call Stop AND register it in
// t.Cleanup; without the sync.Once the second close(s.done) panics.
func TestStop_TwiceIsSafe(t *testing.T) {
	h := newTCPHarness(t)
	if err := h.s.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

func TestStartTCP_RefusesNonLoopback(t *testing.T) {
	s := NewServer(filepath.Join(t.TempDir(), "s"), func(*Conn, *Message) {}, nil)
	if _, err := s.StartTCP("0.0.0.0:0", TCPHooks{}); err == nil {
		t.Fatal("StartTCP bound a non-loopback address")
	}
}

func TestBroadcast_UnauthTCPGetsNothing(t *testing.T) {
	h := newTCPHarness(t)
	raw := h.dial(t)
	conn := h.waitAccepted(t, 1)
	local, err := NewClient(h.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	waitConns(t, h.s, 2)

	h.s.Broadcast(&Message{Type: MsgWorkspaceState})
	local.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := local.Receive(); err != nil {
		t.Fatalf("the local conn missed the broadcast: %v", err)
	}
	if !readsNothing(raw, 300*time.Millisecond) {
		t.Fatal("an unauthenticated TCP conn received a broadcast")
	}

	conn.MarkAuthenticated(NewTokenAuth("0a1b2c3d", "t", RightsReadOnly))
	h.s.Broadcast(&Message{Type: MsgWorkspaceState})
	raw.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := ReadMessage(raw); err != nil {
		t.Fatalf("an authenticated conn missed the broadcast: %v", err)
	}

	conn.Auth().Revoke()
	h.s.Broadcast(&Message{Type: MsgWorkspaceState})
	if !readsNothing(raw, 300*time.Millisecond) {
		t.Fatal("a revoked conn received a broadcast")
	}
}

func waitConns(t *testing.T, s *Server, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for s.ConnCount() < n && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if s.ConnCount() < n {
		t.Fatalf("ConnCount = %d, want %d", s.ConnCount(), n)
	}
}

// Every send path of a revoked conn drops all but an error: the must-deliver
// Send, the droppable telemetry path and the blocking broadcast path.
func TestSend_RevokedSendsOnlyErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		send func(c *Conn, m *Message) error
	}{
		{"Send", func(c *Conn, m *Message) error { return c.Send(m) }},
		{"SendDroppable", func(c *Conn, m *Message) error { return c.SendDroppable(m) }},
		{"SendBlocking", func(c *Conn, m *Message) error { return c.SendBlocking(m, nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := net.Pipe()
			defer b.Close()
			c := newTCPConn(a, nil)
			defer c.Close()
			c.MarkAuthenticated(NewTokenAuth("0a1b2c3d", "t", RightsFull))
			c.Auth().Revoke()
			if err := tc.send(c, &Message{Type: MsgWorkspaceState}); err != nil {
				t.Fatal(err)
			}
			if err := tc.send(c, &Message{Type: MsgError, ID: "x"}); err != nil {
				t.Fatal(err)
			}
			b.SetReadDeadline(time.Now().Add(2 * time.Second))
			msg, err := ReadMessage(b)
			if err != nil || msg.Type != MsgError {
				t.Fatalf("first frame on a revoked conn = %v, %v; want only the error", msg, err)
			}
		})
	}
}

func TestStartTCP_NinthPendingClosed(t *testing.T) {
	h := newTCPHarness(t)
	var raws []net.Conn
	for i := 0; i < MaxPendingTCP; i++ {
		raws = append(raws, h.dial(t))
	}
	first := h.waitAccepted(t, MaxPendingTCP)
	ninth := h.dial(t)
	ninth.SetReadDeadline(time.Now().Add(2 * time.Second))
	var b [1]byte
	if n, err := ninth.Read(b[:]); n != 0 || err == nil {
		t.Fatalf("the 9th pending conn was served: n=%d err=%v", n, err)
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("the 9th pending conn was left open instead of closed at accept")
	}
	// raw.Close in acceptTCP can reach this client before the hook appends.
	rej := h.waitRejected(t, 1)
	if len(rej) != 1 || rej[0] != RejectTooMany {
		t.Fatalf("rejected = %v, want [%q]", rej, RejectTooMany)
	}
	// Logging one in frees its slot.
	first.MarkAuthenticated(NewTokenAuth("0a1b2c3d", "t", RightsFull))
	h.dial(t)
	h.waitAccepted(t, MaxPendingTCP+1)
	_ = raws
}

// The pre-login cap is exact: a frame of PreLoginFrameMax bytes reaches the
// handler, one byte more closes the conn at its length prefix, as does a
// length far past it. "Closed" means the read ENDED — a read that merely timed
// out is a conn left open, and must not pass for one that was closed.
func TestReceive_PreLoginFrameCap(t *testing.T) {
	for _, tc := range []struct {
		name   string
		length uint32
		closed bool
	}{
		{"10 MiB", 10 << 20, true},
		{"cap plus one", PreLoginFrameMax + 1, true},
		{"exactly the cap", PreLoginFrameMax, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTCPHarness(t)
			raw := h.dial(t)
			h.waitAccepted(t, 1)
			if tc.closed {
				var hdr [4]byte
				binary.BigEndian.PutUint32(hdr[:], tc.length)
				if _, err := raw.Write(hdr[:]); err != nil {
					t.Fatal(err)
				}
				raw.SetReadDeadline(time.Now().Add(2 * time.Second))
				var b [1]byte
				_, err := raw.Read(b[:])
				var ne net.Error
				if err == nil || (errors.As(err, &ne) && ne.Timeout()) {
					t.Fatalf("conn still open after a %d-byte pre-login frame (read: %v)", tc.length, err)
				}
				if rej := h.waitRejected(t, 1); len(rej) != 1 || rej[0] != RejectTooLarge {
					t.Fatalf("rejected = %v, want [%q]", rej, RejectTooLarge)
				}
				return
			}
			frame := capSizedFrame(t, int(tc.length))
			if _, err := raw.Write(frame); err != nil {
				t.Fatal(err)
			}
			select {
			case m := <-h.got:
				if m.Type != MsgHello {
					t.Fatalf("handler got %q, want hello", m.Type)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("a %d-byte pre-login frame never reached the handler", tc.length)
			}
			if rej := h.waitRejected(t, 0); len(rej) != 0 {
				t.Fatalf("a frame at the cap was rejected: %v", rej)
			}
		})
	}
}

// capSizedFrame is a length-prefixed hello whose JSON body is exactly n bytes,
// padded through its ID.
func capSizedFrame(t *testing.T, n int) []byte {
	t.Helper()
	const head, tail = `{"type":"hello","id":"`, `"}`
	pad := n - len(head) - len(tail)
	if pad < 0 {
		t.Fatalf("setup: %d bytes cannot hold a hello", n)
	}
	body := head + strings.Repeat("a", pad) + tail
	frame := make([]byte, 4, 4+len(body))
	binary.BigEndian.PutUint32(frame, uint32(len(body)))
	return append(frame, body...)
}

// After login the 4 KiB cap lifts. The login is completed INSIDE the handler,
// as the daemon does: a read already parked when MarkAuthenticated ran keeps
// the cap it started with, so only the NEXT frame may be large.
func TestReceive_CapLiftsAfterLogin(t *testing.T) {
	got := make(chan *Message, 4)
	s := NewServer(filepath.Join(t.TempDir(), "s"), func(c *Conn, m *Message) {
		if m.Type == MsgHello {
			c.MarkAuthenticated(NewTokenAuth("0a1b2c3d", "t", RightsFull))
		}
		got <- m
	}, nil)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	addr, err := s.StartTCP("127.0.0.1:0", TCPHooks{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := WriteMessage(raw, &Message{Type: MsgHello, ID: "h"}); err != nil {
		t.Fatal(err)
	}
	<-got
	big := make([]byte, 5000)
	for i := range big {
		big[i] = 'a'
	}
	msg, err := NewMessage(MsgPaneInput, PaneInputPayload{PaneID: "p", Data: big})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteMessage(raw, msg); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m.Type != MsgPaneInput {
			t.Fatalf("got %s", m.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a logged-in conn's 5 KB frame never reached the handler")
	}
}

// An accepted TCP conn is counted for WaitConns before its Accepted hook runs:
// it is already registered, so a Stop during the hook closes it, and its
// handler and disconnect callback are still to come.
func TestWaitConns_CountsAConnWhileItsAcceptHookRuns(t *testing.T) {
	inHook, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	s := NewServer(filepath.Join(t.TempDir(), "s"), func(*Conn, *Message) {}, nil)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	addr, err := s.StartTCP("127.0.0.1:0", TCPHooks{Accepted: func(*Conn) {
		close(inHook)
		<-release
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		s.Stop()
	})
	raw, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	select {
	case <-inHook:
	case <-time.After(3 * time.Second):
		t.Fatal("the Accepted hook never ran")
	}
	if s.WaitConns(50 * time.Millisecond) {
		t.Fatal("WaitConns reported every handler returned while a conn was still in its accept hook")
	}
	releaseOnce.Do(func() { close(release) })
	raw.Close()
	if !s.WaitConns(3 * time.Second) {
		t.Fatal("the conn's handler never returned after its client closed")
	}
}

func TestStop_ClosesTCPListener(t *testing.T) {
	h := newTCPHarness(t)
	h.s.Stop()
	if c, err := net.DialTimeout("tcp", h.addr, 500*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("TCP listener still accepting after Stop")
	}
}

func TestAuthState_ParkAndAuditRate(t *testing.T) {
	a := NewTokenAuth("0a1b2c3d", "t", RightsStandard)
	for i := 0; i < 4; i++ {
		if !a.TryPark(4) {
			t.Fatalf("park %d refused", i)
		}
	}
	if a.TryPark(4) {
		t.Fatal("a 5th park was admitted")
	}
	a.Unpark()
	if !a.TryPark(4) {
		t.Fatal("unpark did not free a slot")
	}
	now := time.Unix(1_800_000_000, 0)
	if !a.ShouldAuditRefusal(MsgPaneInput, now) || a.ShouldAuditRefusal(MsgPaneInput, now.Add(30*time.Second)) {
		t.Fatal("refusal audit not limited to once per type per minute")
	}
	if !a.ShouldAuditRefusal(MsgPaneInput, now.Add(61*time.Second)) {
		t.Fatal("refusal audit never re-armed")
	}
}
