package daemon

import (
	"testing"
	"time"
)

// Regression tests for the bell-event self-deadlock (production wedge,
// 2026-06-12 watchdog goroutine dump): detectBellEvent held the pane's
// PluginMu across d.emitEvent, which re-locks the same PluginMu for the
// mute check. Go mutexes are not reentrant, so the first un-cooldowned
// terminal bell (Claude rings one on every "waiting for your input")
// parked the pane-output goroutine forever and starved snapshot/idle/
// memreport/switch-tab behind the held lock.

// bellData is a raw BEL outside any OSC sequence — the form that passes
// detectBellEvent's oscBellRe filter and actually fires the event.
var bellData = []byte("ding\x07")

func runWithDeadline(t *testing.T, name string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s deadlocked (self-deadlock regression)", name)
	}
}

func TestDetectBellEvent_DoesNotSelfDeadlock(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	runWithDeadline(t, "detectBellEvent", func() {
		d.detectBellEvent(pane, pane.ID, bellData)
	})

	// The lock must be released afterwards — a leaked lock is the same
	// daemon-wide starvation with a different shape.
	runWithDeadline(t, "PluginMu re-acquire", func() {
		pane.PluginMu.Lock()
		pane.PluginMu.Unlock() //nolint:staticcheck // empty critical section is the liveness probe
	})

	// And the event must actually have been emitted.
	events := d.events.Events()
	if len(events) != 1 || events[0].Type != "bell" {
		t.Fatalf("expected exactly one bell event, got %+v", events)
	}
}

func TestDetectBellEvent_MutedPane_NoDeadlockNoEvent(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.PluginMu.Lock()
	pane.Muted = true
	pane.PluginMu.Unlock()

	runWithDeadline(t, "detectBellEvent on muted pane", func() {
		d.detectBellEvent(pane, pane.ID, bellData)
	})
	if events := d.events.Events(); len(events) != 0 {
		t.Fatalf("muted pane must not emit, got %+v", events)
	}
}

func TestDetectBellEvent_CooldownSuppressesSecondBell(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	runWithDeadline(t, "first bell", func() { d.detectBellEvent(pane, pane.ID, bellData) })
	runWithDeadline(t, "second bell (cooldown)", func() { d.detectBellEvent(pane, pane.ID, bellData) })

	if events := d.events.Events(); len(events) != 1 {
		t.Fatalf("cooldown should suppress the second bell, got %d events", len(events))
	}
}

// TestFlushPaneOutput_BellPath_EndToEnd drives the full output path the
// production wedge took: flushPaneOutput → detectBellEvent → emitEvent.
func TestFlushPaneOutput_BellPath_EndToEnd(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	runWithDeadline(t, "flushPaneOutput with BEL", func() {
		d.flushPaneOutput(pane.ID, bellData)
	})

	// Snapshot of the same pane must remain possible (it locks PluginMu).
	runWithDeadline(t, "post-bell snapshot", func() {
		d.snapshot()
	})
}
