package daemon

import (
	"sync"
	"testing"
	"time"
)

// shortRedrawCooldown shrinks the throttle so a test does not wait seconds.
//
// No test in this file may call t.Parallel(): redrawKeyCooldown is a package
// var, and a parallel test mutating it races every other test that reads it —
// green on a serial local run, red under CI's `go test -race ./...`.
func shortRedrawCooldown(t *testing.T, d time.Duration) {
	t.Helper()
	prev := redrawKeyCooldown
	redrawKeyCooldown = d
	t.Cleanup(func() { redrawKeyCooldown = prev })
}

// A restart installs a fresh child that has received nothing, so it must not
// inherit the previous child's stamp — otherwise its own first kick is held for
// up to a cooldown and the restarted pane sits blank in front of a live
// process. Driven through spawnPane rather than the field, because the reset
// belongs in the same locked span that zeroes the applied-size guard for
// exactly the same reason.
func TestSpawnPane_ResetsTheRedrawCooldown(t *testing.T) {
	d := newTestDaemon(t)
	pane := &Pane{ID: "pane-0000000f", CWD: t.TempDir(), Type: "terminal"}

	pane.PluginMu.Lock()
	pane.lastRedrawAt = time.Now() // the previous child's kick
	pane.redrawSeq = 7
	pane.PluginMu.Unlock()

	if err := d.spawnPane(pane, newRestoredPTY(paneSize(pane)), false); err != nil {
		t.Fatalf("spawnPane: %v", err)
	}

	pane.PluginMu.Lock()
	stamp, seq := pane.lastRedrawAt, pane.redrawSeq
	pane.PluginMu.Unlock()

	if !stamp.IsZero() {
		t.Errorf("lastRedrawAt = %v after a fresh spawn, want the zero time — the new "+
			"child has received no redraw key", stamp)
	}
	if seq != 0 {
		t.Errorf("redrawSeq = %d after a fresh spawn, want 0", seq)
	}
}

// The shipped value has to EXCEED the window claude-code reads as /clear.
// Pinned here because the whole fix is the number: shrinking it below two
// seconds silently reintroduces issue #169 with every other test still green.
func TestRedrawKeyCooldown_ExceedsClaudeClearWindow(t *testing.T) {
	const claudeClearWindow = 2 * time.Second // code.claude.com/docs/en/fullscreen
	if redrawKeyCooldown <= claudeClearWindow {
		t.Fatalf("redrawKeyCooldown = %v, must exceed %v — claude-code runs /clear "+
			"on two Ctrl+L inside that window (issue #169)", redrawKeyCooldown, claudeClearWindow)
	}
}

// The bug in one test: two kicks that would land together must not.
func TestSendRedrawKey_SecondKickInsideCooldownIsHeld(t *testing.T) {
	shortRedrawCooldown(t, 300*time.Millisecond)
	d := daemonWithPlugin(t, "claude-code", "\f", false)
	pty := &recordingSession{}
	pane := &Pane{ID: "p1", Type: "claude-code", PTY: pty}
	t.Cleanup(pane.StopInput)

	d.sendRedrawKey(pane, "claude-code", "\f")
	d.sendRedrawKey(pane, "claude-code", "\f")

	if !waitForInput(t, pty, "\f") {
		t.Fatalf("child received %q, want one form feed immediately", pty.got())
	}
	time.Sleep(150 * time.Millisecond) // still inside the cooldown
	if got := pty.got(); got != "\f" {
		t.Fatalf("child received %q inside the cooldown, want a single form feed", got)
	}
	if !waitForInput(t, pty, "\f\f") {
		t.Fatalf("child received %q, want the held kick delivered after the cooldown", pty.got())
	}
}

// A burst is one leading kick plus ONE coalesced trailing kick, however many
// arrive — a window drag produces a resize per settle.
func TestSendRedrawKey_BurstCoalescesToOneHeldKick(t *testing.T) {
	shortRedrawCooldown(t, 200*time.Millisecond)
	d := daemonWithPlugin(t, "claude-code", "\f", false)
	pty := &recordingSession{}
	pane := &Pane{ID: "p1", Type: "claude-code", PTY: pty}
	t.Cleanup(pane.StopInput)

	for i := 0; i < 5; i++ {
		d.sendRedrawKey(pane, "claude-code", "\f")
	}

	if !waitForInput(t, pty, "\f\f") {
		t.Fatalf("child received %q, want leading + one coalesced kick", pty.got())
	}
	time.Sleep(300 * time.Millisecond)
	if got := pty.got(); got != "\f\f" {
		t.Fatalf("burst delivered %d kicks, want exactly 2", len(got))
	}
}

// The throttle is per pane. A workspace-wide reattach kicks every AI pane at
// once; one pane's kick must not swallow another's.
func TestSendRedrawKey_CooldownIsPerPane(t *testing.T) {
	shortRedrawCooldown(t, 500*time.Millisecond)
	d := daemonWithPlugin(t, "claude-code", "\f", false)
	ptyA, ptyB := &recordingSession{}, &recordingSession{}
	paneA := &Pane{ID: "a", Type: "claude-code", PTY: ptyA}
	paneB := &Pane{ID: "b", Type: "claude-code", PTY: ptyB}
	t.Cleanup(paneA.StopInput)
	t.Cleanup(paneB.StopInput)

	d.sendRedrawKey(paneA, "claude-code", "\f")
	d.sendRedrawKey(paneB, "claude-code", "\f")

	if !waitForInput(t, ptyA, "\f") || !waitForInput(t, ptyB, "\f") {
		t.Fatalf("pane A got %q, pane B got %q — each pane owns its own cooldown",
			ptyA.got(), ptyB.got())
	}
}

// A held kick must not be delivered to a process that has since exited.
func TestSendRedrawKey_HeldKickSkippedWhenThePaneExited(t *testing.T) {
	shortRedrawCooldown(t, 150*time.Millisecond)
	d := daemonWithPlugin(t, "claude-code", "\f", false)
	pty := &recordingSession{}
	pane := &Pane{ID: "p1", Type: "claude-code", PTY: pty}
	t.Cleanup(pane.StopInput)

	d.sendRedrawKey(pane, "claude-code", "\f")
	d.sendRedrawKey(pane, "claude-code", "\f")

	// Wait for the leading kick to reach the child before the pane dies, or
	// this test races its own setup: EnqueueInput hands off to the writer
	// goroutine, so an undrained kick would be indistinguishable from the held
	// one this asserts about.
	if !waitForInput(t, pty, "\f") {
		t.Fatalf("child received %q, want the leading kick before the pane exits", pty.got())
	}
	code := 0
	pane.PluginMu.Lock()
	pane.ExitCode = &code
	pane.PluginMu.Unlock()

	time.Sleep(400 * time.Millisecond)
	if got := pty.got(); got != "\f" {
		t.Fatalf("child received %q, want only the leading kick — the process is gone", got)
	}
}

// The cooldown is measured on the daemon's clock, but the window it exists to
// respect belongs to the CHILD. A child that has stopped reading its stdin —
// the wedge this daemon has hit in production — leaves the leading kick sitting
// in the queue, so a held kick delivered three seconds later on our clock still
// arrives back-to-back with it the moment the child resumes. That is the
// original /clear chord, rebuilt by the fix meant to prevent it.
//
// The held kick is therefore DROPPED while the earlier one is undelivered. The
// queued byte is itself the repaint, and it will be read at the child's current
// geometry, so nothing is lost.
func TestSendRedrawKey_HeldKickDroppedWhileTheEarlierOneIsStillQueued(t *testing.T) {
	shortRedrawCooldown(t, 150*time.Millisecond)
	d := daemonWithPlugin(t, "claude-code", "\f", false)
	pty := newWedgedSession() // Write blocks like a child that stopped reading
	pane := &Pane{ID: "p1", Type: "claude-code", PTY: pty}
	release := releaseWedgeOnce(pty)
	t.Cleanup(pane.StopInput)
	t.Cleanup(release)

	d.sendRedrawKey(pane, "claude-code", "\f") // leading: enters the PTY write and parks
	d.sendRedrawKey(pane, "claude-code", "\f") // held behind the cooldown

	// Guard: the leading kick must have REACHED the blocked Write, or this
	// test would be asserting about an empty queue.
	waitUntil(t, "the leading kick to reach the child's blocked Write",
		func() bool { return pty.writeCount() == 1 })

	time.Sleep(400 * time.Millisecond) // well past the cooldown

	if got := pty.writeCount(); got != 1 {
		t.Fatalf("child received %d kicks while the first was still unread, want 1 — "+
			"two arriving together is what claude-code reads as /clear", got)
	}

	// And the drop is permanent rather than deferred: releasing the child must
	// not produce a second byte out of nowhere.
	release()
	time.Sleep(200 * time.Millisecond)
	if got := pty.writeCount(); got != 1 {
		t.Errorf("child received %d kicks after the wedge cleared, want 1", got)
	}
}

// releaseWedgeOnce unblocks a wedgedSession's Write/Close, idempotently — the
// test both releases explicitly and registers the same call as cleanup, so an
// early t.Fatalf cannot leave the writer goroutine parked.
func releaseWedgeOnce(w *wedgedSession) func() {
	var once sync.Once
	return func() { once.Do(func() { close(w.release) }) }
}

// The teardown case the other checks cannot see: releasePanes calls StopInput
// but leaves PTY and ExitCode untouched, so a held kick that fired and is
// parked on PluginMu finds a pane that looks perfectly healthy.
//
// Called directly rather than through the timer, deliberately: the race is
// between Timer.Stop and an already-running callback, which no test can
// schedule reliably. This asserts the predicate that decides it.
func TestDeferredRedrawKey_SkippedAfterTeardown(t *testing.T) {
	shortRedrawCooldown(t, 150*time.Millisecond)
	d := daemonWithPlugin(t, "claude-code", "\f", false)
	pty := &recordingSession{}
	pane := &Pane{ID: "p1", Type: "claude-code", PTY: pty}
	t.Cleanup(pane.StopInput)

	d.sendRedrawKey(pane, "claude-code", "\f")
	if !waitForInput(t, pty, "\f") {
		t.Fatalf("child received %q, want the leading kick", pty.got())
	}

	pane.StopInput() // what releasePanes does before closing the PTY

	// The pane still looks alive by every other measure — that is the point.
	pane.PluginMu.Lock()
	alive := pane.PTY != nil && pane.ExitCode == nil
	pane.PluginMu.Unlock()
	if !alive {
		t.Fatal("setup: teardown must leave PTY and ExitCode untouched for this to mean anything")
	}

	d.deferredRedrawKey(pane, "claude-code", "\f", pty)

	time.Sleep(100 * time.Millisecond)
	if got := pty.got(); got != "\f" {
		t.Errorf("child received %q after teardown, want only the leading kick", got)
	}
}

// StopInput is what releasePanes — the single pane-teardown funnel — calls
// before closing the PTY, and it is the correctness half of the held kick's
// lifetime: deferredRedrawKey's own staleness check cannot cover a destroy,
// because releasePanes does not nil pane.PTY, so the captured session still
// compares equal when the timer fires.
//
// The assertion is on the timer rather than only on the bytes, deliberately.
// After StopInput the pane's writer goroutine has returned, so a kick that DID
// fire would sit unread in the queue and write nothing — "nothing reached the
// child" therefore holds whether or not the cancel exists, and a test resting
// on it alone would survive deleting the code it is meant to protect.
func TestStopInput_CancelsAHeldRedrawKick(t *testing.T) {
	shortRedrawCooldown(t, 200*time.Millisecond)
	d := daemonWithPlugin(t, "claude-code", "\f", false)
	pty := &recordingSession{}
	pane := &Pane{ID: "p1", Type: "claude-code", PTY: pty}
	t.Cleanup(pane.StopInput)

	d.sendRedrawKey(pane, "claude-code", "\f")
	d.sendRedrawKey(pane, "claude-code", "\f") // held behind the cooldown
	if !waitForInput(t, pty, "\f") {
		t.Fatalf("child received %q, want the leading kick", pty.got())
	}

	// Guard: without an armed timer this test asserts nothing.
	pane.PluginMu.Lock()
	armed := pane.redrawTimer != nil
	pane.PluginMu.Unlock()
	if !armed {
		t.Fatal("setup: no kick was held, so there is nothing for StopInput to cancel")
	}

	pane.StopInput()

	pane.PluginMu.Lock()
	pending := pane.redrawTimer
	pane.PluginMu.Unlock()
	if pending != nil {
		t.Error("StopInput left a held kick armed — releasePanes calls it before " +
			"closing the PTY, so the timer would outlive the pane it belongs to")
	}

	time.Sleep(400 * time.Millisecond) // past the cooldown the kick was waiting on
	if got := pty.got(); got != "\f" {
		t.Errorf("child received %q after teardown, want only the leading kick", got)
	}
}

// A restart swaps the pane's PTY. The fresh child gets its own kick from the
// attach and resize paths, so a held one would pair with THAT one — the exact
// pairing this throttle exists to prevent.
func TestSendRedrawKey_HeldKickSkippedAfterAPTYSwap(t *testing.T) {
	shortRedrawCooldown(t, 150*time.Millisecond)
	d := daemonWithPlugin(t, "claude-code", "\f", false)
	oldPTY, newPTY := &recordingSession{}, &recordingSession{}
	pane := &Pane{ID: "p1", Type: "claude-code", PTY: oldPTY}
	t.Cleanup(pane.StopInput)

	d.sendRedrawKey(pane, "claude-code", "\f")
	d.sendRedrawKey(pane, "claude-code", "\f")

	// The leading kick has to reach the OLD child before the swap. The input
	// writer resolves pane.PTY when it drains, not when the kick is enqueued,
	// so swapping first would deliver the leading kick to the new child and
	// this test would fail for a reason that has nothing to do with the timer.
	if !waitForInput(t, oldPTY, "\f") {
		t.Fatalf("old child received %q, want the leading kick before the restart", oldPTY.got())
	}
	pane.PluginMu.Lock()
	pane.PTY = newPTY
	pane.PluginMu.Unlock()

	time.Sleep(400 * time.Millisecond)
	if got := newPTY.got(); got != "" {
		t.Fatalf("restarted child received %q, want nothing", got)
	}
	if got := oldPTY.got(); got != "\f" {
		t.Fatalf("old child received %q, want only the leading kick", got)
	}
}
