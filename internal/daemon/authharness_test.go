package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/clientauth"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// authHarness is a daemon with BOTH listeners up: the unix socket and a real
// loopback TCP listener on an OS-chosen port, the token store and the audit
// log open, and the fake PTY spawn path (overlayTestDaemon).
type authHarness struct {
	d    *Daemon
	sock string
	addr string
	home string
}

func newAuthHarness(t *testing.T) *authHarness {
	t.Helper()
	return newAuthHarnessWith(t, nil)
}

// newAuthHarnessWith runs setup after initAuth and BEFORE either listener
// starts, so a field it sets on the authService (the sleep seam) is written
// before any conn goroutine exists to read it.
func newAuthHarnessWith(t *testing.T, setup func(*Daemon)) *authHarness {
	t.Helper()
	d := overlayTestDaemon(t, config.Default())
	registerShippedPlugins(t, d)
	home := config.QuilDir()
	if err := d.initAuth(home); err != nil {
		t.Fatalf("initAuth: %v", err)
	}
	if setup != nil {
		setup(d)
	}
	sock := filepath.Join(home, "s.sock")
	d.server = ipc.NewServer(sock, d.handleMessage, d.onClientDisconnect)
	if err := d.server.Start(); err != nil {
		t.Fatalf("start unix: %v", err)
	}
	addr, err := d.startTCPListener("127.0.0.1:0")
	if err != nil {
		t.Fatalf("start tcp: %v", err)
	}
	t.Cleanup(func() {
		d.shutdownOnce.Do(func() { close(d.shutdown) })
		d.server.Stop()
		d.closeAuth()
	})
	return &authHarness{d: d, sock: sock, addr: addr.String(), home: home}
}

func (h *authHarness) mint(t *testing.T, name string, level clientauth.Level, expires *time.Time) string {
	t.Helper()
	tok, _, err := h.d.tokens.Create(name, level, expires)
	if err != nil {
		t.Fatalf("mint %s: %v", name, err)
	}
	return tok
}

func (h *authHarness) dialRaw(t *testing.T) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func (h *authHarness) dialClient(t *testing.T) *ipc.Client {
	t.Helper()
	c, err := ipc.NewClientWithDialer(context.Background(), func(ctx context.Context) (net.Conn, error) {
		var dd net.Dialer
		return dd.DialContext(ctx, "tcp", h.addr)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func testLoginHello() ipc.HelloPayload {
	return ipc.HelloPayload{Kind: "script", Proto: ipc.ProtocolVersion, ClientID: "test-client", PID: os.Getpid()}
}

func (h *authHarness) login(t *testing.T, token string) (*ipc.Client, ipc.HelloRespPayload) {
	t.Helper()
	c := h.dialClient(t)
	resp, err := clientauth.ClientLogin(c, token, testLoginHello(), 5*time.Second)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return c, resp
}

func (h *authHarness) local(t *testing.T) *ipc.Client {
	t.Helper()
	c, err := ipc.NewClient(h.sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func (h *authHarness) auditEntries(t *testing.T) []auditEntry {
	t.Helper()
	return readAudit(t, h.home)
}

// finishDispatch returns once every frame c sent before it has been handled:
// one conn's frames dispatch in order, so the answer to a request sent now
// arrives only after the earlier handlers returned. A test that sent an
// id-less request whose handler spawns calls it before ending, or the
// handler can still be reading the package's spawn seam when the harness's
// cleanup restores it.
func finishDispatch(t *testing.T, c *ipc.Client) {
	t.Helper()
	roundTrip(t, c, ipc.MsgListTabsReq, ipc.MsgListTabsResp, struct{}{})
}

// waitAudit polls audit.log until pred matches an entry.
func (h *authHarness) waitAudit(t *testing.T, what string, pred func(auditEntry) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range h.auditEntries(t) {
			if pred(e) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no audit line for %s; have %+v", what, h.auditEntries(t))
}

func writeRaw(t *testing.T, c net.Conn, typ, id string, payload any) {
	t.Helper()
	msg := &ipc.Message{Type: typ, ID: id}
	if payload != nil {
		m, err := ipc.NewMessage(typ, payload)
		if err != nil {
			t.Fatal(err)
		}
		m.ID = id
		msg = m
	}
	if err := ipc.WriteMessage(c, msg); err != nil {
		t.Fatal(err)
	}
}

// readRefusal reads one frame, requires `error refused` with reason, and
// returns the frame.
func readRefusal(t *testing.T, c net.Conn, reason string, within time.Duration) *ipc.Message {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(within))
	defer c.SetReadDeadline(time.Time{})
	msg, err := ipc.ReadMessage(c)
	if err != nil {
		t.Fatalf("expected error refused (%s), read failed: %v", reason, err)
	}
	var p ipc.ErrorPayload
	if msg.Type != ipc.MsgError || msg.DecodePayload(&p) != nil || p.Code != ipc.ErrCodeRefused || p.Message != reason {
		t.Fatalf("got %s %s, want error refused %q", msg.Type, msg.Payload, reason)
	}
	return msg
}

// expectRefusal reads one frame and requires `error refused` with reason.
func expectRefusal(t *testing.T, c net.Conn, reason string, within time.Duration) {
	t.Helper()
	readRefusal(t, c, reason, within)
}

// expectRefusalID is expectRefusal that also pins the frame's request ID: the
// client reads the login's answers by the hello's ID and drops any other, so
// a refusal under another ID reaches the user as a bare EOF.
func expectRefusalID(t *testing.T, c net.Conn, reason, wantID string, within time.Duration) {
	t.Helper()
	if msg := readRefusal(t, c, reason, within); msg.ID != wantID {
		t.Fatalf("refusal %q carries id %q, want %q", reason, msg.ID, wantID)
	}
}

// loginHelloFor is the test hello opening a login for tokenID.
func loginHelloFor(tokenID string) ipc.HelloPayload {
	hello := testLoginHello()
	hello.TokenID = tokenID
	return hello
}

// helloAndChallenge sends hello (with a fresh nonce_c) under request id and
// reads the challenge it must earn. It returns both nonces.
func helloAndChallenge(t *testing.T, c net.Conn, id string, hello ipc.HelloPayload) (nonceC, nonceS string) {
	t.Helper()
	nonceC, err := clientauth.NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	hello.Nonce = nonceC
	writeRaw(t, c, ipc.MsgHello, id, hello)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer c.SetReadDeadline(time.Time{})
	ch, err := ipc.ReadMessage(c)
	if err != nil || ch.Type != ipc.MsgAuthChallenge || ch.ID != id {
		t.Fatalf("challenge: %v %v", ch, err)
	}
	var cp ipc.AuthChallengePayload
	if err := ch.DecodePayload(&cp); err != nil {
		t.Fatal(err)
	}
	return nonceC, cp.Nonce
}

// expectEOF requires the daemon to close the conn (not merely go silent).
func expectEOF(t *testing.T, c net.Conn, within time.Duration) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(within))
	defer c.SetReadDeadline(time.Time{})
	_, err := ipc.ReadMessage(c)
	var ne net.Error
	if err == nil || (errors.As(err, &ne) && ne.Timeout()) {
		t.Fatalf("conn still open: %v", err)
	}
}

// expectNoFrame requires silence for d.
func expectNoFrame(t *testing.T, c net.Conn, d time.Duration) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(d))
	defer c.SetReadDeadline(time.Time{})
	var b [1]byte
	n, err := c.Read(b[:])
	var ne net.Error
	if n != 0 || !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("expected no bytes, got n=%d err=%v", n, err)
	}
}

// sendHelloAndProof runs the client half of the login by hand on a raw conn,
// up to and including the proof, and reads nothing after it — the caller
// reads the answer (hello_resp, or the refusal). It returns everything a log
// must never contain.
func sendHelloAndProof(t *testing.T, c net.Conn, token string) (nonceC, nonceS, proof string) {
	t.Helper()
	id, err := clientauth.ParseToken(token)
	if err != nil {
		t.Fatal(err)
	}
	nonceC, nonceS = helloAndChallenge(t, c, "L1", loginHelloFor(id))
	proof = clientauth.ClientProof(token, clientauth.AuthMessage(id, nonceC, nonceS))
	writeRaw(t, c, ipc.MsgAuthProof, "L1", ipc.AuthProofPayload{Proof: proof})
	return nonceC, nonceS, proof
}

// manualLogin is sendHelloAndProof plus the hello_resp it must earn. It
// returns everything a log must never contain, server_sig included. Frames
// under another ID are skipped: the conn is authenticated (and so open to
// broadcasts) just before hello_resp is sent.
func manualLogin(t *testing.T, c net.Conn, token string) (nonceC, nonceS, proof, serverSig string) {
	t.Helper()
	nonceC, nonceS, proof = sendHelloAndProof(t, c, token)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer c.SetReadDeadline(time.Time{})
	var (
		resp *ipc.Message
		err  error
	)
	for resp == nil || resp.ID != "L1" {
		if resp, err = ipc.ReadMessage(c); err != nil {
			t.Fatalf("hello_resp: %v", err)
		}
	}
	if resp.Type != ipc.MsgHelloResp {
		t.Fatalf("hello_resp: got %s %s", resp.Type, resp.Payload)
	}
	var hr ipc.HelloRespPayload
	if err := resp.DecodePayload(&hr); err != nil || hr.ServerSig == "" {
		t.Fatalf("hello_resp without server_sig: %+v %v", hr, err)
	}
	return nonceC, nonceS, proof, hr.ServerSig
}
