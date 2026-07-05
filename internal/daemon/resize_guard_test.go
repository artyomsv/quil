package daemon

import (
	"fmt"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// The TUI re-sends every pane's size on each workspace broadcast
// (resizeAllPanes). Re-applying an unchanged size to ConPTY is pure churn;
// handleResizePane must skip it. The guard compares against the size last
// applied to the CURRENT PTY — spawnPane zeroes it on PTY install so a
// fresh PTY always receives its first resize.

func resizeMsg(t *testing.T, paneID string, cols, rows uint16) *ipc.Message {
	t.Helper()
	msg, err := ipc.NewMessage(ipc.MsgResizePane, ipc.ResizePanePayload{
		PaneID: paneID, Cols: cols, Rows: rows,
	})
	if err != nil {
		t.Fatalf("build resize msg: %v", err)
	}
	return msg
}

func TestHandleResizePane_DuplicateSize_SkipsPTYResize(t *testing.T) {
	d := &Daemon{session: NewSessionManager(4096)}
	fake := &fakeSession{}
	pane := &Pane{ID: "p1", PTY: fake}
	d.session.panes["p1"] = pane

	d.handleResizePane(resizeMsg(t, "p1", 100, 40))
	d.handleResizePane(resizeMsg(t, "p1", 100, 40))
	if len(fake.resizes) != 1 {
		t.Fatalf("PTY.Resize called %d times, want 1 (duplicate must be skipped)", len(fake.resizes))
	}
	d.handleResizePane(resizeMsg(t, "p1", 120, 40))
	if len(fake.resizes) != 2 {
		t.Fatalf("PTY.Resize called %d times, want 2 (changed size must apply)", len(fake.resizes))
	}
	if pane.Cols != 120 || pane.Rows != 40 {
		t.Errorf("pane size = %dx%d, want 120x40", pane.Cols, pane.Rows)
	}
}

func TestHandleResizePane_FreshPTY_AcceptsSameSize(t *testing.T) {
	d := &Daemon{session: NewSessionManager(4096)}
	fake := &fakeSession{}
	pane := &Pane{ID: "p1", PTY: fake}
	d.session.panes["p1"] = pane

	d.handleResizePane(resizeMsg(t, "p1", 100, 40))

	// Simulate restart: new PTY installed the way spawnPane does it.
	fake2 := &fakeSession{}
	pane.PluginMu.Lock()
	pane.PTY = fake2
	pane.appliedCols, pane.appliedRows = 0, 0
	pane.PluginMu.Unlock()

	d.handleResizePane(resizeMsg(t, "p1", 100, 40))
	if len(fake2.resizes) != 1 {
		t.Fatalf("fresh PTY got %d resizes, want 1 (guard must reset on PTY install)", len(fake2.resizes))
	}
}

func TestHandleResizePane_NilPTY_NoApply(t *testing.T) {
	d := &Daemon{session: NewSessionManager(4096)}
	pane := &Pane{ID: "p1"}
	d.session.panes["p1"] = pane
	d.handleResizePane(resizeMsg(t, "p1", 100, 40)) // must not panic
	if pane.Cols != 0 {
		t.Errorf("pane.Cols = %d, want 0 (no PTY, nothing applied)", pane.Cols)
	}
	// A later resize with a real PTY must go through even though the same
	// size was requested while the PTY was nil.
	fake := &fakeSession{}
	pane.PluginMu.Lock()
	pane.PTY = fake
	pane.PluginMu.Unlock()
	d.handleResizePane(resizeMsg(t, "p1", 100, 40))
	if len(fake.resizes) != 1 {
		t.Fatalf("PTY got %d resizes, want 1 (nil-PTY request must not poison the guard)", len(fake.resizes))
	}
}

// TestSpawnPane_ResetsResizeGuard exercises the REAL reset path (spawnPane),
// not a hand-set of the fields: a fresh PTY must always accept its first
// resize even if the pane's guard still holds a stale size from the
// previous PTY. Regression cover for the same-size guard's fresh-PTY reset.
func TestSpawnPane_ResetsResizeGuard(t *testing.T) {
	// A fully-constructed test daemon: spawnPane launches streamPTYOutput,
	// whose reader hits the fakeSession's Read error and calls onPaneExit →
	// emitEvent. That path needs a real event queue (d.events) or it nil-
	// derefs; d.broadcast is already nil-safe (guards on d.server).
	d := &Daemon{
		registry: plugin.NewRegistry(),
		session:  NewSessionManager(4096),
		events:   newEventQueue(16),
	}
	pane := &Pane{ID: "p-reset", Type: "terminal"}
	// Simulate a prior PTY's last-applied size lingering on the pane.
	pane.appliedCols, pane.appliedRows = 100, 40
	// Register the pane BEFORE spawnPane so the write happens-before the
	// streamPTYOutput goroutine (which reads the session map under sm.mu);
	// writing it after spawnPane would race that reader.
	d.session.panes["p-reset"] = pane

	fake := &fakeSession{}
	if err := d.spawnPane(pane, fake, false); err != nil {
		t.Fatalf("spawnPane: %v", err)
	}
	if pane.appliedCols != 0 || pane.appliedRows != 0 {
		t.Fatalf("spawnPane left guard at %dx%d, want 0x0 (fresh PTY must accept first resize)",
			pane.appliedCols, pane.appliedRows)
	}
	// A resize at the old size now goes through to the new PTY.
	d.handleResizePane(resizeMsg(t, "p-reset", 100, 40))
	if len(fake.resizes) < 1 {
		t.Errorf("fresh PTY got %d resizes at the old size, want at least 1", len(fake.resizes))
	}
}

// TestHandleResizePane_FailedResizeDoesNotStickGuard: a Resize error must
// leave the guard unchanged so the next identical broadcast retries rather
// than being silently swallowed.
func TestHandleResizePane_FailedResizeDoesNotStickGuard(t *testing.T) {
	d := &Daemon{session: NewSessionManager(4096)}
	fake := &failingResizeSession{fail: true}
	pane := &Pane{ID: "p-fail", PTY: fake}
	d.session.panes["p-fail"] = pane

	d.handleResizePane(resizeMsg(t, "p-fail", 90, 30)) // fails
	if pane.appliedCols != 0 || pane.appliedRows != 0 {
		t.Fatalf("failed resize stuck the guard at %dx%d, want 0x0", pane.appliedCols, pane.appliedRows)
	}
	fake.fail = false
	d.handleResizePane(resizeMsg(t, "p-fail", 90, 30)) // retry succeeds
	if pane.appliedCols != 90 || pane.appliedRows != 30 {
		t.Errorf("retry after failure did not apply: guard %dx%d, want 90x30", pane.appliedCols, pane.appliedRows)
	}
	if fake.okResizes != 1 {
		t.Errorf("successful resizes = %d, want 1 (first failed, retry succeeded)", fake.okResizes)
	}
}

// failingResizeSession fails Resize while fail is true, then succeeds.
type failingResizeSession struct {
	fakeSession
	fail      bool
	okResizes int
}

func (f *failingResizeSession) Resize(rows, cols uint16) error {
	if f.fail {
		return fmt.Errorf("simulated resize failure")
	}
	f.okResizes++
	return nil
}
