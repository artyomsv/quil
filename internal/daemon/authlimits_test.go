package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/clientauth"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/sandbox"
)

// A sandbox probe waits for Docker on a worker of its own. Uncounted, a
// read-only token could stack any number of them, each holding its message;
// counted, the fifth concurrent one is refused, and a slot comes back with
// the answer.
func TestRights_SandboxCapRequestsCapped(t *testing.T) {
	block := make(chan struct{})
	var unblock sync.Once
	prev := sandboxProbeFn
	sandboxProbeFn = func(context.Context) (sandbox.Info, error) {
		<-block
		return sandbox.Info{ServerVersion: "1", OSType: "linux", Arch: "amd64"}, nil
	}
	t.Cleanup(func() { sandboxProbeFn = prev })
	t.Cleanup(func() { unblock.Do(func() { close(block) }) })

	h := newAuthHarness(t)
	viewer, _ := h.login(t, h.mint(t, "viewer", clientauth.LevelReadOnly, nil))
	capReq := func(id string) *ipc.Message {
		return mustMessage(t, ipc.MsgSandboxCapReq, id, ipc.SandboxCapReqPayload{})
	}
	for i := 0; i < maxParkedPerConn; i++ {
		if sendAndProbe(t, viewer, capReq("cap-"+string(rune('a'+i)))) {
			t.Fatalf("probe %d refused below the cap", i)
		}
	}
	if !sendAndProbe(t, viewer, capReq("cap-over")) {
		t.Fatal("a fifth waiting sandbox probe was admitted")
	}
	unblock.Do(func() { close(block) })
	answered := 0
	viewer.SetReadDeadline(time.Now().Add(5 * time.Second))
	for answered < maxParkedPerConn {
		f, err := viewer.Receive()
		if err != nil {
			t.Fatalf("%d of %d probes answered: %v", answered, maxParkedPerConn, err)
		}
		if f.Type == ipc.MsgSandboxCapResp {
			answered++
		}
	}
	viewer.SetReadDeadline(time.Time{})
	if sendAndProbe(t, viewer, capReq("cap-after")) {
		t.Fatal("the slots did not come back with the answers")
	}
	// The local socket keeps its trust: no cap there.
	local := h.local(t)
	roundTrip(t, local, ipc.MsgHello, ipc.MsgHelloResp, testLoginHello())
	for i := 0; i < maxParkedPerConn+1; i++ {
		if sendAndProbe(t, local, capReq("lcap-"+string(rune('a'+i)))) {
			t.Fatal("the local socket is capped too")
		}
	}
}

// Anyone who reaches loopback can fail logins without a token. Past the
// per-minute cap the lines are counted, not written, so a flood cannot
// rotate the logins and privileged requests out of audit.log — and a real
// login is still written in the middle of it.
func TestPreLoginAudit_FloodIsCappedAndCounted(t *testing.T) {
	h := newAuthHarness(t)
	const flood = 3 * preLoginAuditPerMinute
	for i := 0; i < flood; i++ {
		h.d.onTCPRejected(ipc.RejectTooMany, nil)
	}
	c, _ := h.login(t, h.mint(t, "owner", clientauth.LevelFull, nil))
	finishDispatch(t, c)
	h.d.flushPreLoginAudit(time.Now(), true)

	failed, ok, suppressed := 0, 0, ""
	for _, e := range h.auditEntries(t) {
		switch e.Event {
		case "login_failed":
			failed++
		case "login_ok":
			ok++
		case "audit_suppressed":
			suppressed = e.Reason
		}
	}
	if failed > preLoginAuditPerMinute {
		t.Fatalf("%d login_failed lines, want at most %d a minute", failed, preLoginAuditPerMinute)
	}
	if ok != 1 {
		t.Fatalf("%d login_ok lines, want 1 — the cap swallowed a real login", ok)
	}
	// The login's own tcp_connect also spent a pre-login line, so the count
	// is at least the flood past the cap.
	if suppressed == "" || !strings.Contains(suppressed, "not written") {
		t.Fatalf("no audit_suppressed line counting the flood (reason %q)", suppressed)
	}
}

// The window is a fixed minute: the count of an ended window is reported by
// the first line after it, and the new window starts empty.
func TestAuditBudget_WindowRolls(t *testing.T) {
	var b auditBudget
	t0 := time.Unix(1_800_000_000, 0)
	for i := 0; i < preLoginAuditPerMinute; i++ {
		if ok, _ := b.take(t0); !ok {
			t.Fatalf("line %d refused under the cap", i)
		}
	}
	if ok, _ := b.take(t0.Add(time.Second)); ok {
		t.Fatal("a line past the cap was allowed")
	}
	if n := b.drain(t0.Add(30*time.Second), false); n != 0 {
		t.Fatalf("drain reported %d inside a live window", n)
	}
	ok, ended := b.take(t0.Add(61 * time.Second))
	if !ok || ended != 1 {
		t.Fatalf("take after the window = (%v, %d), want (true, 1)", ok, ended)
	}
}

// An audit.log other accounts could read is treated as one that did not
// open: no listener.
func TestInitAuth_AuditLogUnprotectedKeepsTCPOff(t *testing.T) {
	prev := protectAuditFile
	protectAuditFile = func(string) error { return errors.New("acl refused") }
	t.Cleanup(func() { protectAuditFile = prev })
	d, logs, err := unreadableAuthHome(t, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "audit log") {
		t.Fatalf("initAuth = %v, want the audit log's error", err)
	}
	if a := d.server.TCPAddr(); a != nil {
		t.Fatalf("TCP listener on %s with an unprotected audit log", a)
	}
	if !strings.Contains(logs, "no TCP listener") {
		t.Fatalf("no log line says why the listener is off:\n%s", logs)
	}
}

// A quil folder that could not be restricted keeps the listener off, with
// the audit log and token store both open; the unix socket still serves.
func TestListener_UnprotectedHomeKeepsTCPOff(t *testing.T) {
	var buf safeBuffer
	restore := captureLog(&buf)
	t.Cleanup(restore)
	cfg := config.Default()
	cfg.Listener.TCP = "127.0.0.1:0"
	d := overlayTestDaemon(t, cfg)
	home := config.QuilDir()
	if err := d.initAuth(home); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.closeAuth)
	d.homeUnprotected = true
	d.server = ipc.NewServer(home+"/s.sock", d.handleMessage, d.onClientDisconnect)
	if err := d.server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.server.Stop() })
	d.startConfiguredListener()
	if a := d.server.TCPAddr(); a != nil {
		t.Fatalf("TCP listener on %s over an unprotected quil folder", a)
	}
	if !strings.Contains(buf.String(), "could not be restricted") {
		t.Fatalf("no log line says why the listener is off:\n%s", buf.String())
	}
	c, err := ipc.NewClient(home + "/s.sock")
	if err != nil {
		t.Fatalf("unix socket down: %v", err)
	}
	c.Close()
}

// closeAuth waits for a sweep that is running, so its token_expired line is
// written before audit.log closes rather than lost after it.
func TestCloseAuth_WaitsForTheExpirySweep(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	prevTick, prevClock := expiryTick, expiryClock
	expiryTick = 5 * time.Millisecond
	expiryClock = func() time.Time {
		once.Do(func() {
			close(entered)
			<-release
		})
		return time.Now().Add(2 * time.Hour)
	}
	t.Cleanup(func() { expiryTick, expiryClock = prevTick, prevClock })

	d := overlayTestDaemon(t, config.Default())
	home := config.QuilDir()
	// The token exists before the loop's first tick can read the clock.
	store, err := clientauth.OpenStore(home + "/tokens.json")
	if err != nil {
		t.Fatal(err)
	}
	soon := time.Now().Add(time.Hour)
	if _, _, err := store.Create("short", clientauth.LevelStandard, &soon); err != nil {
		t.Fatal(err)
	}
	if err := d.initAuth(home); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the expiry loop never ran")
	}
	done := make(chan struct{})
	go func() {
		d.closeAuth()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("closeAuth returned while a sweep was still running")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("closeAuth never returned")
	}
	for _, e := range readAudit(t, home) {
		if e.Event == "token_expired" {
			return
		}
	}
	t.Fatal("the sweep's token_expired line was lost")
}
