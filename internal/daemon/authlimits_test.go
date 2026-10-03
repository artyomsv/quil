package daemon

import (
	"context"
	"errors"
	"fmt"
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

// The per-conn cap alone is not a bound: a new conn starts with an empty
// count. Waiting sandbox checks must therefore end with their conn, or a
// viewer logging in again and again stacks them without limit. The probe
// stays blocked throughout, so only the conn's close can end the waits.
func TestRights_SandboxCapWaitersEndWithTheirConn(t *testing.T) {
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
	tok := h.mint(t, "viewer", clientauth.LevelReadOnly, nil)
	for round := 0; round < 3; round++ {
		viewer, _ := h.login(t, tok)
		for i := 0; i < maxParkedPerConn; i++ {
			id := fmt.Sprintf("cap-%d-%d", round, i)
			if sendAndProbe(t, viewer, mustMessage(t, ipc.MsgSandboxCapReq, id, ipc.SandboxCapReqPayload{})) {
				t.Fatalf("round %d: probe %d refused below the cap", round, i)
			}
		}
		// Every request waits, the one that started the shared probe too.
		pollUntil(t, "the requests to wait on the probe", func() bool { return h.d.sandboxCap.waiting.Load() == maxParkedPerConn })
		if err := viewer.Close(); err != nil {
			t.Fatal(err)
		}
		pollUntil(t, "the waits to end with their conn", func() bool { return h.d.sandboxCap.waiting.Load() == 0 })
	}
	// The shared probe outlives every conn by design. Let it finish before
	// the cleanup restores sandboxProbeFn, which it read: this get waits on
	// the probe's own completion, which orders that read before the restore.
	unblock.Do(func() { close(block) })
	if got := h.d.sandboxCap.get(context.Background()); !got.Available {
		t.Fatalf("the shared probe's answer = %+v, want available", got)
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

// A caller that leaves gets the canceled answer at once — the one that
// started the probe as well as a later one — and the shared probe still
// completes and is cached for everyone else: one client's disconnect must not
// store "docker not available" for 30 seconds.
func TestSandboxCap_CanceledWaiterDoesNotPoisonTheCache(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	prev := sandboxProbeFn
	sandboxProbeFn = func(ctx context.Context) (sandbox.Info, error) {
		once.Do(func() { close(started) })
		select {
		case <-block:
		case <-ctx.Done():
			return sandbox.Info{}, ctx.Err()
		}
		return sandbox.Info{ServerVersion: "1", OSType: "linux", Arch: "amd64"}, nil
	}
	t.Cleanup(func() { sandboxProbeFn = prev })

	var c sandboxCap
	proberCtx, cancelProber := context.WithCancel(context.Background())
	probed := make(chan ipc.SandboxCapRespPayload, 1)
	go func() { probed <- c.get(proberCtx) }()
	<-started

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waited := make(chan ipc.SandboxCapRespPayload, 1)
	go func() { waited <- c.get(waiterCtx) }()
	pollUntil(t, "both callers to park", func() bool { return c.waiting.Load() == 2 })
	cancelWaiter()
	if got := <-waited; got.Error != errSandboxCapCanceled {
		t.Fatalf("canceled waiter got %+v, want the canceled answer", got)
	}
	// The caller that STARTED the probe leaves too, while it still runs: it
	// returns at once, and the probe goes on for everyone else.
	cancelProber()
	if got := <-probed; got.Error != errSandboxCapCanceled {
		t.Fatalf("canceled initiator got %+v, want the canceled answer", got)
	}
	close(block)
	if got := c.get(context.Background()); !got.Available {
		t.Fatalf("cached answer = %+v, want available", got)
	}
}

// The request that finds no probe running starts one and must still keep its
// OWN deadline: the plugin catalog waits at most 2 s for the sandbox answer,
// and its conn's dispatch goroutine — every later request on that conn —
// waits with it. A probe held past the deadline must not hold the catalog.
func TestPluginCatalog_FirstProbeKeepsTheCatalogDeadline(t *testing.T) {
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
	c, _ := h.login(t, h.mint(t, "owner", clientauth.LevelFull, nil))
	start := time.Now()
	for _, m := range []*ipc.Message{
		mustMessage(t, ipc.MsgPluginCatalogReq, "catalog", struct{}{}),
		mustMessage(t, ipc.MsgListTabsReq, "tabs", ipc.ListTabsReqPayload{}),
	} {
		if err := c.Send(m); err != nil {
			t.Fatal(err)
		}
	}
	c.SetReadDeadline(time.Now().Add(8 * time.Second))
	got := map[string]time.Duration{}
	for len(got) < 2 {
		f, err := c.Receive()
		if err != nil {
			t.Fatalf("answers so far %v: %v", got, err)
		}
		if f.ID == "catalog" || f.ID == "tabs" {
			got[f.ID] = time.Since(start)
			if f.ID == "catalog" {
				var p ipc.PluginCatalogRespPayload
				if err := f.DecodePayload(&p); err != nil {
					t.Fatal(err)
				}
				if p.SandboxAvailable {
					t.Fatal("the catalog reported a sandbox the probe never answered for")
				}
			}
		}
	}
	c.SetReadDeadline(time.Time{})
	for id, d := range got {
		if d > 3*time.Second {
			t.Fatalf("%s answered after %v; the catalog's 2 s budget was not kept", id, d)
		}
	}
	// Let the shared probe finish before the cleanup restores the seam.
	unblock.Do(func() { close(block) })
	if a := h.d.sandboxCap.get(context.Background()); !a.Available {
		t.Fatalf("the shared probe's answer = %+v, want available", a)
	}
}

// A revoke whose handler runs after closeAuth (the conn drain timed out while
// it was still writing the store) starts no close worker: audit.log is
// already closed, and Stop has already closed every conn.
func TestCloseAuthConns_AfterCloseAuthStartsNoWorker(t *testing.T) {
	h := newAuthHarness(t)
	tok := h.mint(t, "late", clientauth.LevelFull, nil)
	c, _ := h.login(t, tok)
	id, err := clientauth.ParseToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	h.d.closeAuth()
	h.d.closeAuthConns(h.d.auth.markRevoked(id), "token revoked")
	c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	defer c.SetReadDeadline(time.Time{})
	if f, err := c.Receive(); err == nil {
		t.Fatalf("a close worker ran after closeAuth: got %s %s", f.Type, f.ID)
	}
}
