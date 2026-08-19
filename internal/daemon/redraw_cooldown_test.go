package daemon

import (
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
