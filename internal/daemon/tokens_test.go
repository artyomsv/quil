package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/clientauth"
	"github.com/artyomsv/quil/internal/ipc"
)

// expectClientClosed reads c until the daemon CLOSES it: EOF, a reset, or a
// closed conn. A read TIMEOUT fails — a conn that was silenced but left open
// is exactly what closeAuthConns without its Close produces, and a loop that
// accepted any error would pass it.
func expectClientClosed(t *testing.T, c *ipc.Client, within time.Duration) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(within))
	defer c.SetReadDeadline(time.Time{})
	for {
		_, err := c.Receive()
		if err == nil {
			continue // a frame queued before the close
		}
		var ne net.Error
		switch {
		case errors.As(err, &ne) && ne.Timeout():
			t.Fatalf("conn still open %v after revoke: %v", within, err)
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF),
			errors.Is(err, net.ErrClosed), errors.Is(err, syscall.ECONNRESET):
			return
		default:
			t.Fatalf("read ended with %v, want EOF or a closed conn", err)
		}
	}
}

func TestTokens_CreateListRevokeLocal(t *testing.T) {
	h := newAuthHarness(t)
	local := h.local(t)
	cr := decodeInto[ipc.TokenCreateRespPayload](t, roundTrip(t, local, ipc.MsgTokenCreateReq, ipc.MsgTokenCreateResp,
		ipc.TokenCreateReqPayload{Name: "laptop", Rights: "read-only", Expires: "30d"}))
	if cr.Error != "" || cr.Rights != "read-only" || cr.Expires == "" {
		t.Fatalf("create = %+v", cr)
	}
	if _, err := clientauth.ParseToken(cr.Token); err != nil {
		t.Fatalf("created token unparseable: %v", err)
	}
	listFrame := roundTrip(t, local, ipc.MsgTokenListReq, ipc.MsgTokenListResp, struct{}{})
	lr := decodeInto[ipc.TokenListRespPayload](t, listFrame)
	if len(lr.Tokens) != 1 || lr.Tokens[0].ID != cr.ID {
		t.Fatalf("list = %+v", lr)
	}
	// The payload AS SENT, not a re-encoding of the decoded struct: a decode
	// into TokenInfo would drop any extra key the daemon put on the wire.
	raw := string(listFrame.Payload)
	for _, secret := range []string{cr.Token, "stored_key", "server_key", "qtk_"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("token_list_resp carries %q: %s", secret, raw)
		}
	}
	rr := decodeInto[ipc.TokenRevokeRespPayload](t, roundTrip(t, local, ipc.MsgTokenRevokeReq, ipc.MsgTokenRevokeResp,
		ipc.TokenRevokeReqPayload{Target: "LAPTOP"}))
	if rr.Error != "" || rr.ID != cr.ID {
		t.Fatalf("revoke = %+v", rr)
	}
	h.waitAudit(t, "token_created", func(e auditEntry) bool { return e.Event == "token_created" && e.TokenID == cr.ID })
	h.waitAudit(t, "token_revoked", func(e auditEntry) bool { return e.Event == "token_revoked" && e.TokenID == cr.ID })
}

// A token request from a TCP conn is refused whatever its rights: token
// management is the local socket's alone.
func TestTokens_RefusedOverTCP(t *testing.T) {
	h := newAuthHarness(t)
	c, _ := h.login(t, h.mint(t, "remote-full", clientauth.LevelFull, nil))
	for _, typ := range []string{ipc.MsgTokenCreateReq, ipc.MsgTokenListReq, ipc.MsgTokenRevokeReq} {
		f := roundTrip(t, c, typ, ipc.MsgError, ipc.TokenCreateReqPayload{Name: "sneaky"})
		if p := decodeInto[ipc.ErrorPayload](t, f); p.Code != ipc.ErrCodeRefused {
			t.Fatalf("%s over TCP got %+v, want refused", typ, p)
		}
	}
	if n := len(h.d.tokens.List()); n != 1 {
		t.Fatalf("store holds %d tokens after refused TCP requests, want 1", n)
	}
}

// The token is printed by the CLI and nowhere else: no daemon log line and
// no audit line may carry it, across create, a login with it, list and revoke.
func TestTokens_SecretNeverLogged(t *testing.T) {
	var buf safeBuffer
	t.Cleanup(captureLog(&buf))
	h := newAuthHarness(t)
	local := h.local(t)
	cr := decodeInto[ipc.TokenCreateRespPayload](t, roundTrip(t, local, ipc.MsgTokenCreateReq, ipc.MsgTokenCreateResp,
		ipc.TokenCreateReqPayload{Name: "logged"}))
	if cr.Error != "" {
		t.Fatalf("create = %+v", cr)
	}
	// The verifier is as secret as the token for these logs: it is what a
	// login is checked against. Read before the revoke removes it.
	stored, server := storedVerifierHex(t, h.home, cr.ID)
	c, _ := h.login(t, cr.Token)
	roundTrip(t, local, ipc.MsgTokenListReq, ipc.MsgTokenListResp, struct{}{})
	roundTrip(t, local, ipc.MsgTokenRevokeReq, ipc.MsgTokenRevokeResp, ipc.TokenRevokeReqPayload{Target: cr.ID})
	expectClientClosed(t, c, 3*time.Second)
	_, secret, _ := strings.Cut(strings.TrimPrefix(cr.Token, "qtk_"), "_")
	audit, err := os.ReadFile(filepath.Join(h.home, auditFile))
	if err != nil {
		t.Fatal(err)
	}
	for where, text := range map[string]string{"quild.log": buf.String(), "audit.log": string(audit)} {
		for what, s := range map[string]string{"token": cr.Token, "token secret": secret,
			"stored key": stored, "server key": server} {
			if strings.Contains(text, s) {
				t.Fatalf("%s carries the %s:\n%s", where, what, text)
			}
		}
	}
}

// storedVerifierHex reads token id's stored and server keys, as hex, from
// tokens.json.
func storedVerifierHex(t *testing.T, home, id string) (stored, server string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Tokens []clientauth.Entry `json:"tokens"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("decode tokens.json: %v", err)
	}
	for _, e := range file.Tokens {
		if e.ID == id {
			if e.StoredKey == "" || e.ServerKey == "" {
				t.Fatalf("setup: token %s has an empty verifier", id)
			}
			return e.StoredKey, e.ServerKey
		}
	}
	t.Fatalf("setup: token %s not in tokens.json", id)
	return "", ""
}

func TestRevoke_ClosesConnKeepsOthers(t *testing.T) {
	h := newAuthHarness(t)
	victimTok := h.mint(t, "victim", clientauth.LevelFull, nil)
	otherTok := h.mint(t, "other", clientauth.LevelFull, nil)
	victim, _ := h.login(t, victimTok)
	other, _ := h.login(t, otherTok)
	if _, n, err := h.d.revokeToken("victim"); err != nil || n != 1 {
		t.Fatalf("revoke: n=%d err=%v", n, err)
	}
	if p := waitErrorFrame(t, victim, 3*time.Second); p.Code != ipc.ErrCodeRefused || p.Message != "token revoked" {
		t.Fatalf("victim got %+v, want error refused (token revoked)", p)
	}
	expectClientClosed(t, victim, 3*time.Second) // a real EOF, not a timeout
	data, err := os.ReadFile(filepath.Join(h.home, "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"victim"`) {
		t.Fatal("tokens.json still lists the revoked token")
	}
	roundTrip(t, other, ipc.MsgListTabsReq, ipc.MsgListTabsResp, struct{}{})
	if _, err := clientauth.ClientLogin(h.dialClient(t), victimTok, testLoginHello(), 5*time.Second); err == nil {
		t.Fatal("a revoked token logged in again")
	}
}

func TestRevoke_EveryConnOfTokenCloses(t *testing.T) {
	h := newAuthHarness(t)
	tok := h.mint(t, "shared", clientauth.LevelReadOnly, nil)
	a, _ := h.login(t, tok)
	b, _ := h.login(t, tok)
	if _, n, err := h.d.revokeToken("shared"); err != nil || n != 2 {
		t.Fatalf("revoke closed %d conns (err %v), want 2", n, err)
	}
	for _, c := range []*ipc.Client{a, b} {
		expectClientClosed(t, c, 3*time.Second)
	}
}

// indexedConns is how many live conns the tokenID → conns index holds for id.
func indexedConns(d *Daemon, id string) int {
	d.auth.idxMu.Lock()
	defer d.auth.idxMu.Unlock()
	return len(d.auth.index[id])
}

// The index is what a revoke closes: every login must enter it, and a conn
// that went away must leave it, or a revoke reports (and works on) a conn
// that no longer exists.
func TestRevoke_IndexTracksLoginsAndDisconnects(t *testing.T) {
	h := newAuthHarness(t)
	tok := h.mint(t, "indexed", clientauth.LevelFull, nil)
	id, err := clientauth.ParseToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	gone, _ := h.login(t, tok)
	kept, _ := h.login(t, tok)
	if n := indexedConns(h.d, id); n != 2 {
		t.Fatalf("index holds %d conns after two logins, want 2", n)
	}
	gone.Close()
	// onTCPDisconnect drops the conn from the index BEFORE it writes this line.
	h.waitAudit(t, "tcp_disconnect", func(e auditEntry) bool {
		return e.Event == "tcp_disconnect" && e.TokenName == "indexed"
	})
	if n := indexedConns(h.d, id); n != 1 {
		t.Fatalf("index holds %d conns after one disconnected, want 1", n)
	}
	if _, n, err := h.d.revokeToken("indexed"); err != nil || n != 1 {
		t.Fatalf("revoke closed %d conns (err %v), want 1 — the disconnected one is gone", n, err)
	}
	expectClientClosed(t, kept, 3*time.Second)
}

// Phase 1 silences the conn before phase 2 has sent anything.
func TestRevoke_SilentBeforeRefusal(t *testing.T) {
	h := newAuthHarness(t)
	tok := h.mint(t, "v", clientauth.LevelFull, nil)
	c, _ := h.login(t, tok)
	paused := make(chan struct{})
	resume := make(chan struct{})
	h.d.auth.afterRevokeMark = func() { close(paused); <-resume }
	done := make(chan struct{})
	go func() { h.d.revokeToken("v"); close(done) }()
	<-paused
	h.d.broadcastState()
	msg := &ipc.Message{Type: ipc.MsgListTabsReq, ID: "after-mark"}
	if err := c.Send(msg); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		f, err := c.Receive()
		if err != nil {
			t.Fatalf("no answer to the request sent after phase 1: %v", err)
		}
		if f.Type == ipc.MsgWorkspaceState {
			t.Fatal("a revoked conn received a broadcast after phase 1")
		}
		if f.ID == "after-mark" {
			if f.Type != ipc.MsgError {
				t.Fatalf("a request after phase 1 got %s, want error refused", f.Type)
			}
			break
		}
	}
	c.SetReadDeadline(time.Time{})
	close(resume)
	<-done
	expectClientClosed(t, c, 3*time.Second)
}

// waitErrorFrame reads until an `error` frame arrives (broadcasts may come
// first on a logged-in conn) and returns its payload.
func waitErrorFrame(t *testing.T, c *ipc.Client, within time.Duration) ipc.ErrorPayload {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(within))
	defer c.SetReadDeadline(time.Time{})
	for {
		f, err := c.Receive()
		if err != nil {
			t.Fatalf("no error frame: %v", err)
		}
		if f.Type == ipc.MsgError {
			var p ipc.ErrorPayload
			if err := f.DecodePayload(&p); err != nil {
				t.Fatal(err)
			}
			return p
		}
	}
}

// A login whose proof arrives while a revoke of its token sits between its
// two phases is refused: phase 1 removed the entry under the admission lock,
// so there is nothing left to admit.
func TestAdmission_ProofBetweenRevokePhasesIsRefused(t *testing.T) {
	h := newAuthHarness(t)
	tok := h.mint(t, "inflight", clientauth.LevelFull, nil)
	id, err := clientauth.ParseToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	c := h.dialRaw(t)
	nonceC, nonceS := helloAndChallenge(t, c, "L1", loginHelloFor(id))
	paused := make(chan struct{})
	resume := make(chan struct{})
	h.d.auth.afterRevokeMark = func() { close(paused); <-resume }
	closed := make(chan int, 1)
	go func() {
		_, n, _ := h.d.revokeToken("inflight")
		closed <- n
	}()
	<-paused
	proof := clientauth.ClientProof(tok, clientauth.AuthMessage(id, nonceC, nonceS))
	writeRaw(t, c, ipc.MsgAuthProof, "L1", ipc.AuthProofPayload{Proof: proof})
	expectRefusalID(t, c, "token refused", "L1", 5*time.Second)
	expectEOF(t, c, 3*time.Second)
	close(resume)
	if n := <-closed; n != 0 {
		t.Fatalf("revoke closed %d conns, want 0 — the login never got in", n)
	}
	if connHasToken(h.d, "inflight") {
		t.Fatal("a conn is logged in with the revoked token")
	}
}

// A login racing a revoke of the same token ends refused or closed, never
// logged in.
func TestAdmission_RaceWithRevoke(t *testing.T) {
	setLoginVar(t, &loginBackoffBase, time.Millisecond) // before the harness
	setLoginVar(t, &loginBackoffCap, 5*time.Millisecond)
	h := newAuthHarness(t)
	for i := 0; i < 30; i++ {
		name := "race-" + strings.Repeat("x", i%5) + string(rune('a'+i%26))
		tok := h.mint(t, name, clientauth.LevelFull, nil)
		client := h.dialClient(t)
		var wg sync.WaitGroup
		var loginErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, loginErr = clientauth.ClientLogin(client, tok, testLoginHello(), 5*time.Second)
		}()
		go func() { defer wg.Done(); h.d.revokeToken(name) }()
		wg.Wait()
		if loginErr == nil {
			// Admitted before the revoke took the lock, so the revoke found it
			// in the index: the daemon must close it.
			expectClientClosed(t, client, 3*time.Second)
		}
		if connHasToken(h.d, name) {
			t.Fatalf("iteration %d: a conn stayed logged in with a revoked token", i)
		}
	}
}

func connHasToken(d *Daemon, name string) bool {
	for _, c := range d.server.ConnsSnapshot() {
		if a := c.Auth(); a != nil && a.TokenName == name && !a.Revoked() {
			return true
		}
	}
	return false
}

// The daemon's clock is INJECTED: the token expires an hour out, both logins
// complete at leisure, and only then does the ticker's clock jump two hours.
// No wall-clock race between "log in" and "expire".
func TestExpiryTicker_ClosesExpiredSkipsNever(t *testing.T) {
	var skew atomic.Int64
	prevTick, prevClock := expiryTick, expiryClock
	expiryTick = 20 * time.Millisecond
	expiryClock = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	t.Cleanup(func() { expiryTick, expiryClock = prevTick, prevClock })
	h := newAuthHarness(t) // initAuth hands both to expiryLoop as arguments
	inAnHour := time.Now().Add(time.Hour)
	short, _ := h.login(t, h.mint(t, "short", clientauth.LevelFull, &inAnHour))
	forever, _ := h.login(t, h.mint(t, "forever", clientauth.LevelFull, nil))
	skew.Store(int64(2 * time.Hour)) // the daemon's clock passes the expiry
	if p := waitErrorFrame(t, short, 5*time.Second); p.Message != "token expired" {
		t.Fatalf("expired conn got %+v, want error refused (token expired)", p)
	}
	expectClientClosed(t, short, 3*time.Second)
	roundTrip(t, forever, ipc.MsgListTabsReq, ipc.MsgListTabsResp, struct{}{})
	time.Sleep(200 * time.Millisecond)
	n := 0
	for _, e := range h.auditEntries(t) {
		if e.Event == "token_expired" {
			n++
			if e.TokenName != "short" {
				t.Fatalf("token_expired for %q, a token with no expiry", e.TokenName)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d token_expired lines, want 1 (once per token)", n)
	}
	// Expired, not revoked: the entry stays listed.
	if got := len(h.d.tokens.List()); got != 2 {
		t.Fatalf("store holds %d tokens after expiry, want 2", got)
	}
}
