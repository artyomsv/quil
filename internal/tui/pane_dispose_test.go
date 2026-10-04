package tui

import (
	"bytes"
	"runtime"
	"testing"
	"time"
)

// drainGoroutines counts the live drainVTResponses goroutines. Counting
// every goroutine instead let an earlier test's goroutines move the count:
// with ~870 alive, one ending during the start window hides a drain (CI saw
// before=867 now=874 for eight drains, twice, October 2026).
func drainGoroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return bytes.Count(buf[:n], []byte("tui.drainVTResponses("))
		}
		buf = make([]byte, 2*len(buf))
	}
}

// waitDrains polls until ok(drainGoroutines()) or 2 s pass, and returns the
// last count.
func waitDrains(ok func(int) bool) int {
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := drainGoroutines()
		if ok(got) || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestPaneModel_Dispose_StopsDrainGoroutine: every PaneModel starts a
// drainVTResponses goroutine parked on the emulator's response pipe; only
// emulator Close unblocks it. Dispose must close the emulator so pruned
// panes don't leak one goroutine + a 10k-line scrollback each.
func TestPaneModel_Dispose_StopsDrainGoroutine(t *testing.T) {
	// No t.Parallel(): another test's panes would move the drain count.
	before := drainGoroutines()

	const n = 8
	panes := make([]*PaneModel, n)
	for i := range panes {
		panes[i] = NewPaneModel("pane-dispose-test", 1024)
	}
	if got := waitDrains(func(c int) bool { return c >= before+n }); got < before+n {
		t.Fatalf("expected %d drain goroutines to start, before=%d now=%d", n, before, got)
	}

	for _, p := range panes {
		p.Dispose()
	}

	if got := waitDrains(func(c int) bool { return c <= before }); got > before {
		t.Errorf("drain goroutines did not exit within 2s: before=%d, after=%d", before, got)
	}
}

func TestPaneModel_Dispose_Idempotent(t *testing.T) {
	p := NewPaneModel("pane-dispose", 1024)
	p.Dispose()
	// Second Dispose must be a no-op (vt nil-guard), not a second
	// vt.Close()/drain-stop attempt.
	p.Dispose()
}
