package tui

import (
	"runtime"
	"testing"
	"time"
)

// TestPaneModel_Dispose_StopsDrainGoroutine: every PaneModel starts a
// drainVTResponses goroutine parked on the emulator's response pipe; only
// emulator Close unblocks it. Dispose must close the emulator so pruned
// panes don't leak one goroutine + a 10k-line scrollback each.
func TestPaneModel_Dispose_StopsDrainGoroutine(t *testing.T) {
	// No t.Parallel(): this test measures the global goroutine count and
	// must not interleave with other goroutine-spawning tests.

	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()

	const n = 8
	panes := make([]*PaneModel, n)
	for i := range panes {
		panes[i] = NewPaneModel("pane-dispose-test", 1024)
	}
	time.Sleep(50 * time.Millisecond)
	if got := runtime.NumGoroutine(); got < before+n {
		t.Fatalf("expected %d drain goroutines to start, before=%d now=%d", n, before, got)
	}

	for _, p := range panes {
		p.Dispose()
	}

	// Poll for the drain goroutines to exit instead of a fixed sleep — a
	// fixed interval false-positives when a goroutine exits slightly late.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if got := runtime.NumGoroutine(); got <= before+1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("drain goroutines did not exit within 2s: before=%d, after=%d",
		before, runtime.NumGoroutine())
}

func TestPaneModel_Dispose_Idempotent(t *testing.T) {
	p := NewPaneModel("pane-dispose", 1024)
	p.Dispose()
	// Second Dispose must be a no-op (vt nil-guard), not a second
	// vt.Close()/drain-stop attempt.
	p.Dispose()
}
