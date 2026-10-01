package daemon

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/clientauth"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/logger"
)

// setLoginVar sets one of auth.go's test-shortenable package vars for this
// test. Call it BEFORE newAuthHarness: initAuth copies the vars into the
// authService, so a later write never reaches a running login.
func setLoginVar(t *testing.T, v *time.Duration, d time.Duration) {
	t.Helper()
	prev := *v
	*v = d
	t.Cleanup(func() { *v = prev })
}

func TestLogin_FullFlowCarriesRights(t *testing.T) {
	h := newAuthHarness(t)
	tok := h.mint(t, "laptop", clientauth.LevelReadOnly, nil)
	c, resp := h.login(t, tok)
	if resp.Rights != ipc.RightsReadOnly || resp.TokenName != "laptop" || resp.ServerSig == "" {
		t.Fatalf("hello_resp = %+v", resp)
	}
	roundTrip(t, c, ipc.MsgListTabsReq, ipc.MsgListTabsResp, struct{}{})
	h.waitAudit(t, "login_ok", func(e auditEntry) bool { return e.Event == "login_ok" && e.TokenName == "laptop" })
}

func TestLogin_RefusalsArriveBeforeEOF(t *testing.T) {
	// Six refused proofs below; keep the guessing backoff out of the runtime.
	setLoginVar(t, &loginBackoffBase, time.Millisecond)
	h := newAuthHarness(t)
	good := h.mint(t, "good", clientauth.LevelFull, nil)
	past := time.Now().Add(-time.Hour)
	expired := h.mint(t, "expired", clientauth.LevelFull, &past)
	revoked := h.mint(t, "revoked", clientauth.LevelFull, nil)
	if _, err := h.d.tokens.Revoke("revoked", nil); err != nil {
		t.Fatal(err)
	}
	wrong, _, _ := clientauth.NewToken()
	goodID, _ := clientauth.ParseToken(good)
	wrongWithGoodID := "qtk_" + goodID + wrong[len("qtk_")+8:]

	// Refused at the FIRST frame: login required.
	for _, tc := range []struct {
		name    string
		typ, id string
		payload any
	}{
		{"non-hello first frame", ipc.MsgListTabsReq, "x", struct{}{}},
		{"hello without id", ipc.MsgHello, "", testLoginHello()},
		{"hello without token", ipc.MsgHello, "x", testLoginHello()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := h.dialRaw(t)
			writeRaw(t, c, tc.typ, tc.id, tc.payload)
			expectRefusal(t, c, "login required", 3*time.Second)
			expectEOF(t, c, 3*time.Second)
		})
	}
	// Refused after the proof: token refused, one reason for every cause —
	// the frame RECEIVED, then EOF, on a raw conn so nothing between the test
	// and the socket can hide either.
	for name, tok := range map[string]string{"wrong": wrongWithGoodID, "expired": expired, "revoked": revoked} {
		t.Run(name, func(t *testing.T) {
			c := h.dialRaw(t)
			sendHelloAndProof(t, c, tok)
			expectRefusal(t, c, "token refused", 3*time.Second)
			expectEOF(t, c, 3*time.Second)
		})
		// The same refusal through the client side of the login.
		t.Run(name+" via ClientLogin", func(t *testing.T) {
			_, err := clientauth.ClientLogin(h.dialClient(t), tok, testLoginHello(), 5*time.Second)
			var refused *clientauth.RefusedError
			if !errors.As(err, &refused) || refused.Reason != "token refused" {
				t.Fatalf("err = %v, want refused: token refused", err)
			}
		})
	}
	// The connect and both login_failed reasons are audited.
	h.waitAudit(t, "tcp_connect", func(e auditEntry) bool {
		return e.Event == "tcp_connect" && e.Transport == ipc.TransportTCP
	})
	h.waitAudit(t, "login_failed login required", func(e auditEntry) bool {
		return e.Event == "login_failed" && e.Reason == "login required"
	})
	h.waitAudit(t, "login_failed token refused", func(e auditEntry) bool {
		return e.Event == "login_failed" && e.Reason == "token refused"
	})
}

// A conn being refused stays in `refusing` until it closes. A frame that
// arrives while the refusal is still flushing is ignored — it must not close
// the conn early (the old code forgot the session at once, and the next frame
// found none and called conn.Close mid-flush).
func TestLogin_FrameDuringRefusalIgnored(t *testing.T) {
	setLoginVar(t, &loginStepTimeout, 200*time.Millisecond)
	h := newAuthHarness(t)
	inFlush := make(chan struct{})
	release := make(chan struct{})
	hook := func() { close(inFlush); <-release }
	h.d.auth.beforeRefusalFlush.Store(&hook) // atomic: the timer goroutine reads it
	c := h.dialRaw(t)
	select {
	case <-inFlush: // the timer refused the silent conn and queued the frame
	case <-time.After(3 * time.Second):
		t.Fatal("the login timer never refused the silent conn")
	}
	nonce, _ := clientauth.NewNonce()
	hello := testLoginHello()
	hello.TokenID, hello.Nonce = "0a1b2c3d", nonce
	writeRaw(t, c, ipc.MsgHello, "late", hello) // dispatched while the refusal flushes
	expectRefusal(t, c, "login timeout", 3*time.Second)
	expectNoFrame(t, c, 300*time.Millisecond) // still open: the late frame closed nothing
	close(release)
	expectEOF(t, c, 3*time.Second)
}

// The same holds for an oversized frame during the flush: the ipc layer
// closes the conn as soon as its reject hook returns, so the hook waits for
// the refusal in flight instead of cutting it short.
func TestLogin_OversizeDuringRefusalKeepsTheFlush(t *testing.T) {
	setLoginVar(t, &loginStepTimeout, 200*time.Millisecond)
	h := newAuthHarness(t)
	inFlush := make(chan struct{})
	release := make(chan struct{})
	hook := func() { close(inFlush); <-release }
	h.d.auth.beforeRefusalFlush.Store(&hook)
	c := h.dialRaw(t)
	select {
	case <-inFlush:
	case <-time.After(3 * time.Second):
		t.Fatal("the login timer never refused the silent conn")
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 10<<20)
	if _, err := c.Write(hdr[:]); err != nil {
		t.Fatal(err)
	}
	expectRefusal(t, c, "login timeout", 3*time.Second)
	expectNoFrame(t, c, 300*time.Millisecond) // the oversized frame closed nothing yet
	close(release)
	expectEOF(t, c, 3*time.Second)
}

// The dummy-key path: an unknown id still gets a challenge, and is refused
// only after the proof, with the same reason.
func TestLogin_UnknownIDStillChallenged(t *testing.T) {
	h := newAuthHarness(t)
	c := h.dialRaw(t)
	helloAndChallenge(t, c, "U1", loginHelloFor("deadbeef"))
	writeRaw(t, c, ipc.MsgAuthProof, "U1", ipc.AuthProofPayload{Proof: strings.Repeat("A", 43)})
	expectRefusal(t, c, "token refused", 3*time.Second)
	expectEOF(t, c, 3*time.Second)
}

func TestLogin_SilentConnTimesOut(t *testing.T) {
	setLoginVar(t, &loginStepTimeout, 300*time.Millisecond)
	h := newAuthHarness(t)
	c := h.dialRaw(t)
	expectRefusal(t, c, "login timeout", 3*time.Second)
	expectEOF(t, c, 3*time.Second)
	h.waitAudit(t, "login_failed timeout", func(e auditEntry) bool { return e.Event == "login_failed" && e.Reason == "timeout" })
	h.waitAudit(t, "tcp_disconnect", func(e auditEntry) bool { return e.Event == "tcp_disconnect" })
}

// "A request at accept + 7 s is answered" against the 5 s deadline, at a 1 s
// step timer with the same shape: the request runs 1 s past the deadline.
// Fails if the first-frame deadline is not cleared at login.
func TestLogin_UsablePastTheDeadline(t *testing.T) {
	setLoginVar(t, &loginStepTimeout, time.Second)
	h := newAuthHarness(t)
	tok := h.mint(t, "a", clientauth.LevelFull, nil)
	start := time.Now()
	c, _ := h.login(t, tok)
	time.Sleep(time.Until(start.Add(2 * time.Second)))
	roundTrip(t, c, ipc.MsgListTabsReq, ipc.MsgListTabsResp, struct{}{})
}

// "A valid hello at accept + 4.9 s while backoff is active still logs in",
// scaled: a 3 s step timer, the hello at accept + 2 s — a full 1 s margin
// before the deadline, enough under -race — and a 2 s backoff that ends at
// accept + ~4 s, PAST the deadline. It logs in only because the delay runs
// after the first frame, with the timer already cleared.
func TestLogin_LateHelloUnderBackoff(t *testing.T) {
	setLoginVar(t, &loginStepTimeout, 3*time.Second)
	setLoginVar(t, &loginBackoffBase, time.Second)
	setLoginVar(t, &loginBackoffCap, 2*time.Second)
	h := newAuthHarness(t)
	tok := h.mint(t, "a", clientauth.LevelFull, nil)
	h.d.auth.failures.Store(2) // min(1 s * 2^1, 2 s) = 2 s backoff
	c := h.dialRaw(t)
	time.Sleep(2 * time.Second)
	manualLogin(t, c, tok)
}

func TestLogin_UnauthConnReadsNoBroadcast(t *testing.T) {
	h := newAuthHarness(t)
	raw := h.dialRaw(t)
	local := attachTestClient(t, h.sock)
	defer local.Close()
	tab := h.d.session.CreateTab("t")
	sendNoID(t, local, ipc.MsgCreatePane, ipc.CreatePanePayload{TabID: tab.ID})
	expectNoFrame(t, raw, 500*time.Millisecond)
}

func TestLogin_OversizeFirstFrameClosed(t *testing.T) {
	h := newAuthHarness(t)
	c := h.dialRaw(t)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 10<<20)
	if _, err := c.Write(hdr[:]); err != nil {
		t.Fatal(err)
	}
	expectEOF(t, c, 3*time.Second)
	h.waitAudit(t, "too large", func(e auditEntry) bool { return e.Event == "login_failed" && e.Reason == ipc.RejectTooLarge })
}

func TestLogin_NinthPendingClosedAtAccept(t *testing.T) {
	h := newAuthHarness(t)
	for i := 0; i < ipc.MaxPendingTCP; i++ {
		h.dialRaw(t)
	}
	waitUntil(t, "8 pending conns", func() bool { return h.d.server.ConnCount() >= ipc.MaxPendingTCP })
	c := h.dialRaw(t)
	expectEOF(t, c, 3*time.Second)
	h.waitAudit(t, "too many", func(e auditEntry) bool { return e.Event == "login_failed" && e.Reason == ipc.RejectTooMany })
}

// Hello-then-hang-up and connect-then-close release slots.
func TestLogin_AbandonedLoginsFreeSlots(t *testing.T) {
	h := newAuthHarness(t)
	tok := h.mint(t, "owner", clientauth.LevelFull, nil)
	for i := 0; i < 2*ipc.MaxPendingTCP; i++ {
		c := h.dialRaw(t)
		if i%2 == 0 {
			nonce, _ := clientauth.NewNonce()
			hello := testLoginHello()
			hello.TokenID, hello.Nonce = "0a1b2c3d", nonce
			writeRaw(t, c, ipc.MsgHello, "A", hello)
		}
		c.Close()
	}
	waitUntil(t, "abandoned conns reaped", func() bool { return h.d.server.ConnCount() == 0 })
	h.login(t, tok)
}

func TestLogin_CorrectTokenAfterFailures(t *testing.T) {
	setLoginVar(t, &loginBackoffBase, 5*time.Millisecond)
	h := newAuthHarness(t)
	tok := h.mint(t, "a", clientauth.LevelFull, nil)
	id, _ := clientauth.ParseToken(tok)
	wrong, _, _ := clientauth.NewToken()
	wrongSameID := "qtk_" + id + wrong[len("qtk_")+8:]
	for i := 0; i < 5; i++ {
		if _, err := clientauth.ClientLogin(h.dialClient(t), wrongSameID, testLoginHello(), 5*time.Second); err == nil {
			t.Fatal("a wrong token logged in")
		}
	}
	h.login(t, tok)
	if n := h.d.auth.failures.Load(); n != 0 {
		t.Fatalf("failures = %d after a success, want 0", n)
	}
}

func TestBackoff_Curve(t *testing.T) {
	var a authService
	for n, want := range map[int64]time.Duration{0: 0, 1: 250 * time.Millisecond, 2: 500 * time.Millisecond,
		3: time.Second, 4: 2 * time.Second, 9: 2 * time.Second} {
		a.failures.Store(n)
		if got := a.backoff(); got != want {
			t.Errorf("backoff(%d) = %v, want %v", n, got, want)
		}
	}
}

// A second hello on a logged-in conn (sendClientHello runs one on every
// dial) is an ordinary hello: no re-login, no refusal.
func TestLogin_SecondHelloIsOrdinary(t *testing.T) {
	h := newAuthHarness(t)
	tok := h.mint(t, "a", clientauth.LevelStandard, nil)
	c, _ := h.login(t, tok)
	hello := testLoginHello()
	hello.TokenID, hello.Nonce = "ffffffff", "garbage"
	resp := decodeInto[ipc.HelloRespPayload](t, roundTrip(t, c, ipc.MsgHello, ipc.MsgHelloResp, hello))
	if resp.Rights != ipc.RightsStandard || resp.ServerSig != "" {
		t.Fatalf("second hello_resp = %+v", resp)
	}
	roundTrip(t, c, ipc.MsgListTabsReq, ipc.MsgListTabsResp, struct{}{})
}

// The local socket is unchanged.
// GUARD (ruling P-7): passes on the tree before this change; it pins that the
// pre-login routing never catches a local conn.
func TestLocalSocket_UnchangedByAuth(t *testing.T) {
	h := newAuthHarness(t)
	probe := h.local(t) // a conn that never says hello, like `quil status`
	roundTrip(t, probe, ipc.MsgVersionReq, ipc.MsgVersionResp, struct{}{})
	c := h.local(t)
	sendNoID(t, c, ipc.MsgHello, testLoginHello()) // id-less hello: ignored
	roundTrip(t, c, ipc.MsgVersionReq, ipc.MsgVersionResp, struct{}{})
	tui := attachTestClient(t, h.sock)
	defer tui.Close()
	tui.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		msg, err := tui.Receive()
		if err != nil {
			t.Fatalf("local attach got no workspace_state: %v", err)
		}
		if msg.Type == ipc.MsgWorkspaceState {
			break
		}
	}
}

// `quil stdio` (cmd/quil/stdio.go proxyStdio) splices the unix socket onto
// ssh's stdin/stdout byte for byte, so a remote TUI is a LOCAL conn: full
// rights, no token, no login, the old dial order. This splices the same two
// io.Copy loops in-process.
// GUARD (ruling P-7): passes before this change; once the rights check exists
// it also proves an act request through the splice is not refused.
func TestLocalSocket_StdioSpliceIsLocal(t *testing.T) {
	h := newAuthHarness(t)
	daemonSide, err := net.Dial("unix", h.sock)
	if err != nil {
		t.Fatal(err)
	}
	tuiEnd, stdioEnd := net.Pipe() // the TUI's ssh stdio, and quil stdio's
	t.Cleanup(func() { daemonSide.Close(); tuiEnd.Close(); stdioEnd.Close() })
	go func() { io.Copy(daemonSide, stdioEnd) }()
	go func() { io.Copy(stdioEnd, daemonSide) }()
	c, err := ipc.NewClientWithDialer(context.Background(), func(context.Context) (net.Conn, error) { return tuiEnd, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	roundTrip(t, c, ipc.MsgVersionReq, ipc.MsgVersionResp, struct{}{}) // version_req before hello
	resp := decodeInto[ipc.HelloRespPayload](t, roundTrip(t, c, ipc.MsgHello, ipc.MsgHelloResp, testLoginHello()))
	if resp.Rights != "" || resp.TokenName != "" || resp.ServerSig != "" {
		t.Fatalf("a spliced local conn was answered as a token login: %+v", resp)
	}
	// An id-bearing act request is answered by its handler, not refused.
	in := decodeInto[ipc.PaneInputRespPayload](t, roundTrip(t, c, ipc.MsgPaneInput, ipc.MsgPaneInputResp,
		ipc.PaneInputPayload{PaneID: "no-such-pane", Data: []byte("x")}))
	if in.Delivered {
		t.Fatalf("pane_input_resp = %+v, want the handler's not-delivered answer", in)
	}
	for _, e := range h.auditEntries(t) {
		if e.Transport == ipc.TransportTCP {
			t.Fatalf("a spliced local conn produced a TCP audit line: %+v", e)
		}
	}
}

func TestListenerConfig_LoopbackOnly(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{{"localhost:0", true}, {":7878", false}, {"0.0.0.0:7878", false}} {
		t.Run(tc.value, func(t *testing.T) {
			cfg := config.Default()
			cfg.Listener.TCP = tc.value
			d := overlayTestDaemon(t, cfg)
			home := config.QuilDir()
			if err := d.initAuth(home); err != nil {
				t.Fatal(err)
			}
			defer d.closeAuth()
			d.server = ipc.NewServer(home+"/s.sock", d.handleMessage, d.onClientDisconnect)
			if err := d.server.Start(); err != nil {
				t.Fatal(err)
			}
			defer d.server.Stop()
			d.startConfiguredListener()
			addr := d.server.TCPAddr()
			if (addr != nil) != tc.want {
				t.Fatalf("TCPAddr = %v, want listener=%v", addr, tc.want)
			}
			if addr != nil && !strings.HasPrefix(addr.String(), "127.0.0.1:") {
				t.Fatalf("bound %s, want 127.0.0.1", addr)
			}
			c, err := ipc.NewClient(home + "/s.sock")
			if err != nil {
				t.Fatalf("unix socket down: %v", err)
			}
			c.Close()
		})
	}
}

// No secret in audit.log or quild.log at debug level.
func TestLogin_NoSecretsInLogs(t *testing.T) {
	var buf safeBuffer
	logger.Init("debug", &buf)
	t.Cleanup(func() { logger.Init("info", io.Discard) })
	// Short enough for the proof-step timeout below, long enough for the
	// logins that answer at once.
	setLoginVar(t, &loginStepTimeout, 500*time.Millisecond)
	setLoginVar(t, &loginBackoffBase, time.Millisecond)
	h := newAuthHarness(t)
	tok := h.mint(t, "a", clientauth.LevelFull, nil)
	nonceC, nonceS, proof, serverSig := manualLogin(t, h.dialRaw(t), tok)
	v := clientauth.DeriveVerifier(tok)
	h.waitAudit(t, "login_ok", func(e auditEntry) bool { return e.Event == "login_ok" })

	// A refused proof: its nonces and the proof itself.
	id, _ := clientauth.ParseToken(tok)
	wrong, _, _ := clientauth.NewToken()
	c := h.dialRaw(t)
	refusedC, refusedS, refusedProof := sendHelloAndProof(t, c, "qtk_"+id+wrong[len("qtk_")+8:])
	expectRefusal(t, c, "token refused", 3*time.Second)
	// A proof-step timeout: the nonces of a login that never finished.
	c2 := h.dialRaw(t)
	timedOutC, timedOutS := helloAndChallenge(t, c2, "T1", loginHelloFor(id))
	expectRefusal(t, c2, "login timeout", 3*time.Second)
	h.waitAudit(t, "login_failed token refused", func(e auditEntry) bool {
		return e.Event == "login_failed" && e.Reason == "token refused"
	})
	h.waitAudit(t, "login_failed timeout", func(e auditEntry) bool {
		return e.Event == "login_failed" && e.Reason == "timeout"
	})

	audit, _ := readFileString(h.home + "/" + auditFile)
	logs := buf.String()
	// Everything after "qtk_<8 hex>_". Not LastIndex("_"): the secret is
	// base64url, whose alphabet includes '_', so that cut could leave a
	// suffix short enough (even empty) to match any log by chance.
	secretPart := tok[len("qtk_")+8+1:]
	if len(secretPart) != 43 {
		t.Fatalf("token %q does not have the qtk_<id>_<secret> shape", tok)
	}
	for name, secret := range map[string]string{
		"token": secretPart, "stored key": hex.EncodeToString(v.StoredKey),
		"server key": hex.EncodeToString(v.ServerKey), "nonce_c": nonceC, "nonce_s": nonceS, "proof": proof,
		"server_sig":      serverSig,
		"refused nonce_c": refusedC, "refused nonce_s": refusedS, "refused proof": refusedProof,
		"timed-out nonce_c": timedOutC, "timed-out nonce_s": timedOutS,
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("quild.log contains the %s", name)
		}
		if strings.Contains(audit, secret) {
			t.Errorf("audit.log contains the %s", name)
		}
	}
}

func readFileString(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

// A token id that is not 8 lowercase hex digits is refused at the hello,
// before the store is asked: the login's signed message is framed on the id
// being exactly that shape.
func TestLogin_MalformedTokenIDRefusedAtHello(t *testing.T) {
	h := newAuthHarness(t)
	for _, id := range []string{"DEADBEEF", "deadbee", "deadbeef0", "dead,eef", "../../xx"} {
		t.Run(id, func(t *testing.T) {
			c := h.dialRaw(t)
			nonce, _ := clientauth.NewNonce()
			hello := testLoginHello()
			hello.TokenID, hello.Nonce = id, nonce
			writeRaw(t, c, ipc.MsgHello, "H1", hello)
			expectRefusalID(t, c, "login required", "H1", 3*time.Second)
			expectEOF(t, c, 3*time.Second)
		})
	}
}

// Every refusal after the hello carries the hello's request ID, whatever
// frame (or timer) caused it. Before any hello there is no ID to carry.
func TestLogin_EveryRefusalCarriesTheHelloID(t *testing.T) {
	setLoginVar(t, &loginStepTimeout, 500*time.Millisecond)
	setLoginVar(t, &loginBackoffBase, time.Millisecond)
	h := newAuthHarness(t)
	good := h.mint(t, "good", clientauth.LevelFull, nil)
	goodID, _ := clientauth.ParseToken(good)
	past := time.Now().Add(-time.Hour)
	expired := h.mint(t, "expired", clientauth.LevelFull, &past)
	badProof := ipc.AuthProofPayload{Proof: strings.Repeat("A", 43)}

	t.Run("wrong proof", func(t *testing.T) {
		c := h.dialRaw(t)
		helloAndChallenge(t, c, "B1", loginHelloFor(goodID))
		writeRaw(t, c, ipc.MsgAuthProof, "B1", badProof)
		expectRefusalID(t, c, "token refused", "B1", 3*time.Second)
	})
	t.Run("unknown id", func(t *testing.T) {
		c := h.dialRaw(t)
		helloAndChallenge(t, c, "B2", loginHelloFor("deadbeef"))
		writeRaw(t, c, ipc.MsgAuthProof, "B2", badProof)
		expectRefusalID(t, c, "token refused", "B2", 3*time.Second)
	})
	t.Run("expired", func(t *testing.T) {
		c := h.dialRaw(t)
		sendHelloAndProof(t, c, expired)
		expectRefusalID(t, c, "token refused", "L1", 3*time.Second)
	})
	t.Run("timeout at the proof step", func(t *testing.T) {
		c := h.dialRaw(t)
		helloAndChallenge(t, c, "B3", loginHelloFor(goodID))
		expectRefusalID(t, c, "login timeout", "B3", 3*time.Second)
		expectEOF(t, c, 3*time.Second)
	})
	t.Run("proof under another id", func(t *testing.T) {
		c := h.dialRaw(t)
		helloAndChallenge(t, c, "B4", loginHelloFor(goodID))
		writeRaw(t, c, ipc.MsgAuthProof, "other", badProof)
		expectRefusalID(t, c, "login required", "B4", 3*time.Second)
	})
	t.Run("another type at the proof step", func(t *testing.T) {
		c := h.dialRaw(t)
		helloAndChallenge(t, c, "B5", loginHelloFor(goodID))
		writeRaw(t, c, ipc.MsgListTabsReq, "B5", struct{}{})
		expectRefusalID(t, c, "login required", "B5", 3*time.Second)
	})
	t.Run("too large at the proof step", func(t *testing.T) {
		c := h.dialRaw(t)
		helloAndChallenge(t, c, "B6", loginHelloFor(goodID))
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], 10<<20)
		if _, err := c.Write(hdr[:]); err != nil {
			t.Fatal(err)
		}
		expectRefusalID(t, c, ipc.RejectTooLarge, "B6", 3*time.Second)
		expectEOF(t, c, 3*time.Second)
	})
	t.Run("timeout before any hello", func(t *testing.T) {
		c := h.dialRaw(t)
		expectRefusalID(t, c, "login timeout", "", 3*time.Second)
	})
}

// Everything a client chose is cut to maxAuditField before it is written.
func TestLogin_AuditFieldsAreCut(t *testing.T) {
	setLoginVar(t, &loginBackoffBase, time.Millisecond)
	h := newAuthHarness(t)
	long := strings.Repeat("k", 300)

	c := h.dialRaw(t)
	hello := loginHelloFor("deadbeef")
	hello.ClientID, hello.Kind = long, long
	helloAndChallenge(t, c, "G1", hello)
	writeRaw(t, c, ipc.MsgAuthProof, "G1", ipc.AuthProofPayload{Proof: strings.Repeat("A", 43)})
	expectRefusal(t, c, "token refused", 3*time.Second)

	c2 := h.dialRaw(t)
	writeRaw(t, c2, "x"+long, "G2", struct{}{})
	expectRefusal(t, c2, "login required", 3*time.Second)

	h.waitAudit(t, "the long client id", func(e auditEntry) bool {
		return e.Event == "login_failed" && e.ClientID != ""
	})
	h.waitAudit(t, "the long type", func(e auditEntry) bool {
		return e.Event == "login_failed" && e.Type != ""
	})
	for _, e := range h.auditEntries(t) {
		for name, v := range map[string]string{"client_id": e.ClientID, "kind": e.Kind, "token_id": e.TokenID,
			"token_name": e.TokenName, "type": e.Type, "reason": e.Reason} {
			if len(v) > maxAuditField {
				t.Errorf("%s %s is %d bytes, want <= %d", e.Event, name, len(v), maxAuditField)
			}
		}
	}
}

// The address is checked by the loopback validator BEFORE anything binds: the
// error is the validator's, not the post-bind refusal inside StartTCP.
func TestStartTCPListener_ValidatesBeforeBinding(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	home := config.QuilDir()
	if err := d.initAuth(home); err != nil {
		t.Fatal(err)
	}
	defer d.closeAuth()
	d.server = ipc.NewServer(filepath.Join(home, "s.sock"), d.handleMessage, d.onClientDisconnect)
	if err := d.server.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.server.Stop()
	for _, addr := range []string{":0", "0.0.0.0:0", "example.invalid:0"} {
		_, err := d.startTCPListener(addr)
		if err == nil || !strings.Contains(err.Error(), "ssh -L") {
			t.Errorf("startTCPListener(%q) = %v, want the loopback validator's refusal", addr, err)
		}
		if a := d.server.TCPAddr(); a != nil {
			t.Fatalf("startTCPListener(%q) bound %s", addr, a)
		}
	}
}

// unreadableAuthHome builds a daemon configured for a TCP listener whose
// home holds what setup puts there, then runs initAuth, starts the unix socket
// and the configured listener, and returns initAuth's error and the log.
func unreadableAuthHome(t *testing.T, setup func(home string)) (*Daemon, string, error) {
	t.Helper()
	var buf safeBuffer
	restore := captureLog(&buf)
	t.Cleanup(restore)
	cfg := config.Default()
	cfg.Listener.TCP = "127.0.0.1:0"
	d := overlayTestDaemon(t, cfg)
	home := config.QuilDir()
	setup(home)
	err := d.initAuth(home)
	t.Cleanup(d.closeAuth)
	sock := filepath.Join(home, "s.sock")
	d.server = ipc.NewServer(sock, d.handleMessage, d.onClientDisconnect)
	if serr := d.server.Start(); serr != nil {
		t.Fatalf("unix socket did not start: %v", serr)
	}
	t.Cleanup(func() { d.server.Stop() })
	d.startConfiguredListener()
	c, cerr := ipc.NewClient(sock)
	if cerr != nil {
		t.Fatalf("unix socket down: %v", cerr)
	}
	c.Close()
	return d, buf.String(), err
}

// A tokens.json this build cannot read keeps the TCP listener off; the unix
// socket starts regardless.
func TestInitAuth_UnreadableTokenStoreKeepsTCPOff(t *testing.T) {
	for name, content := range map[string]string{
		"corrupt": "{not json",
		"newer":   `{"version": 99, "tokens": []}`,
	} {
		t.Run(name, func(t *testing.T) {
			d, logs, err := unreadableAuthHome(t, func(home string) {
				if werr := os.WriteFile(filepath.Join(home, "tokens.json"), []byte(content), 0o600); werr != nil {
					t.Fatal(werr)
				}
			})
			if err == nil || !strings.Contains(err.Error(), "token store") {
				t.Fatalf("initAuth = %v, want the token store's error", err)
			}
			if a := d.server.TCPAddr(); a != nil {
				t.Fatalf("TCP listener on %s with no readable token store", a)
			}
			if !strings.Contains(logs, "no TCP listener") {
				t.Fatalf("no log line says why the listener is off:\n%s", logs)
			}
		})
	}
}

// audit.log is the only record of network logins: without it, no listener.
func TestInitAuth_AuditLogUnopenableKeepsTCPOff(t *testing.T) {
	d, logs, err := unreadableAuthHome(t, func(home string) {
		// A directory where the file belongs: the open fails on every OS.
		if merr := os.Mkdir(filepath.Join(home, auditFile), 0o700); merr != nil {
			t.Fatal(merr)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "audit log") {
		t.Fatalf("initAuth = %v, want the audit log's error", err)
	}
	if a := d.server.TCPAddr(); a != nil {
		t.Fatalf("TCP listener on %s with no audit log", a)
	}
	if !strings.Contains(logs, "no TCP listener") {
		t.Fatalf("no log line says why the listener is off:\n%s", logs)
	}
}

// Entries the store dropped on load are reported by count, never by content.
func TestInitAuth_DroppedEntriesLoggedAsCount(t *testing.T) {
	d, logs, err := unreadableAuthHome(t, func(home string) {
		body := `{"version": 1, "tokens": [{"id": "NOT-AN-ID", "name": "secret-name", "stored_key": "00",` +
			` "server_key": "00", "rights": "full", "created": "2026-01-01T00:00:00Z"}]}`
		if werr := os.WriteFile(filepath.Join(home, "tokens.json"), []byte(body), 0o600); werr != nil {
			t.Fatal(werr)
		}
	})
	if err != nil {
		t.Fatalf("initAuth = %v", err)
	}
	if d.server.TCPAddr() == nil {
		t.Fatal("a store with a dropped entry still opens: the listener should be up")
	}
	if !strings.Contains(logs, "tokens.json: 1 unreadable") {
		t.Fatalf("no warning with the dropped count:\n%s", logs)
	}
	if strings.Contains(logs, "NOT-AN-ID") || strings.Contains(logs, "secret-name") {
		t.Fatalf("the warning names a dropped entry:\n%s", logs)
	}
}

// An oversized frame on a conn that already logged in is not a failed login.
func TestLogin_OversizeAfterLoginIsNotAFailedLogin(t *testing.T) {
	h := newAuthHarness(t)
	tok := h.mint(t, "a", clientauth.LevelFull, nil)
	c := h.dialRaw(t)
	manualLogin(t, c, tok)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 11<<20) // past the post-login cap too
	if _, err := c.Write(hdr[:]); err != nil {
		t.Fatal(err)
	}
	expectEOF(t, c, 3*time.Second)
	// tcp_disconnect is written after the reject hook on the same goroutine.
	h.waitAudit(t, "tcp_disconnect", func(e auditEntry) bool { return e.Event == "tcp_disconnect" && e.TokenName == "a" })
	for _, e := range h.auditEntries(t) {
		if e.Event == "login_failed" {
			t.Fatalf("a logged-in conn's oversized frame was audited as a failed login: %+v", e)
		}
	}
}

// Stop closes the audit log only after every conn's disconnect callback has
// run: a TCP conn open at Stop gets its tcp_disconnect line, and a proof
// check still in its backoff when its conn was closed gets its login_failed.
func TestStop_AuditsTheConnsOpenAtStop(t *testing.T) {
	setLoginVar(t, &loginBackoffBase, 500*time.Millisecond)
	h := newAuthHarness(t)
	tok := h.mint(t, "a", clientauth.LevelFull, nil)
	h.login(t, tok) // logged in, and still open at Stop
	id, _ := clientauth.ParseToken(tok)
	wrong, _, _ := clientauth.NewToken()
	h.d.auth.failures.Store(1) // the next proof check sleeps 500 ms first
	c := h.dialRaw(t)
	sendHelloAndProof(t, c, "qtk_"+id+wrong[len("qtk_")+8:])
	time.Sleep(100 * time.Millisecond) // the proof is now in its backoff
	h.d.Stop()

	var loggedInGone, refused bool
	disconnects := 0
	for _, e := range h.auditEntries(t) {
		switch {
		case e.Event == "tcp_disconnect":
			disconnects++
			loggedInGone = loggedInGone || e.TokenName == "a"
		case e.Event == "login_failed" && e.Reason == "token refused":
			refused = true
		}
	}
	if !loggedInGone || !refused || disconnects < 2 {
		t.Fatalf("after Stop: logged-in disconnect=%v, in-flight refusal=%v, disconnects=%d; have %+v",
			loggedInGone, refused, disconnects, h.auditEntries(t))
	}
}
