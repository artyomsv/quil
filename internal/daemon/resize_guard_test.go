package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
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
