package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// A client launched with no console attached — `quil.exe --version` from a
// non-interactive shell is the observed case — gets a 1x1 geometry from Bubble
// Tea. Every resize fan-out computed from it floors at 1x1 (paneVTSize does so
// deliberately, for genuinely narrow SPLIT panes), so the daemon reflowed every
// PTY in the workspace to one column and each child permanently re-wrapped its
// whole transcript. The daemon log recorded it as
// `attach: client connected (1x1), tabs=48, restored=true` on 2026-08-25 and
// again on 2026-09-05.
//
// View() already refuses to paint below minTermWidth x minTermHeight, so a
// geometry under that describes nothing on screen and must not reach a PTY.
//
// These tests drive Model.Update rather than the fan-out functions directly:
// the bug is that the CALL SITES invoke them unconditionally, so a test that
// calls resizeAllPanes/diffResizes/overlayResizeCmd itself would stay green
// against a fix the call site makes unreachable.

// tinyTermModel builds a one-project / one-tab / one-pane Model wired to a
// fakeConn, with no size reported yet.
func tinyTermModel(t *testing.T) (Model, *fakeConn) {
	t.Helper()
	pane := NewPaneModel("pane-1", testRingBufSize)
	t.Cleanup(pane.Dispose)
	tab := NewTabModel("tab-1", "Shell")
	tab.Root = NewLeaf(pane)
	tab.ActivePane = "pane-1"
	conn := newFakeConn()
	m := Model{
		cfg:            config.Default(),
		client:         conn,
		tabDragFromIdx: -1,
		projects: []*ProjectModel{{
			ID: "proj-1", Name: "Default", tabs: []*TabModel{tab},
		}},
	}
	return m, conn
}

// countResizes reports how many individual pane resizes reached the wire,
// across every MsgResizePanes batch (resizeAllPanes/diffResizes/
// overlayResizeCmd all batch into one frame per destination now).
func countResizes(t *testing.T, conn *fakeConn) int {
	t.Helper()
	n := 0
	conn.mu.Lock()
	defer conn.mu.Unlock()
	for _, msg := range conn.sent {
		if msg.Type != ipc.MsgResizePanes {
			continue
		}
		var p ipc.ResizePanesPayload
		if err := msg.DecodePayload(&p); err != nil {
			t.Fatalf("decode resize_panes payload: %v", err)
		}
		n += len(p.Panes)
	}
	return n
}

func clearSent(conn *fakeConn) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.sent = nil
}

// The first WindowSizeMsg applies immediately (no debounce), so a degenerate
// launch geometry reaches every pane in one pass.
func TestUpdate_FirstWindowSizeBelowMinimum_ShipsNoPaneResize(t *testing.T) {
	m, conn := tinyTermModel(t)

	_, cmd := m.Update(tea.WindowSizeMsg{Width: 1, Height: 1})
	runCmd(cmd)

	if n := countResizes(t, conn); n != 0 {
		t.Fatalf("MsgResizePane count = %d, want 0 — a 1x1 terminal is below "+
			"minTermWidth(%d)xminTermHeight(%d) and View() refuses to paint it, "+
			"so no PTY may be resized to it", n, minTermWidth, minTermHeight)
	}
}

// The guard must be a floor, not a ban: the smallest terminal the TUI agrees to
// paint still has to size its panes.
func TestUpdate_FirstWindowSizeAtMinimum_ShipsPaneResize(t *testing.T) {
	m, conn := tinyTermModel(t)

	_, cmd := m.Update(tea.WindowSizeMsg{Width: minTermWidth, Height: minTermHeight})
	runCmd(cmd)

	if n := countResizes(t, conn); n != 1 {
		t.Fatalf("MsgResizePane count = %d, want 1 — %dx%d is exactly the "+
			"minimum the TUI paints, so its panes must still be sized",
			n, minTermWidth, minTermHeight)
	}
}

// The debounced arm is the second fan-out: a terminal that SHRINKS below the
// minimum after a healthy start reaches the panes through resizeTickMsg, and an
// open overlay is resized from there too.
func TestUpdate_ResizeTickBelowMinimum_ShipsNoPaneResize(t *testing.T) {
	m, conn := tinyTermModel(t)

	// A healthy first size, so the model is sized and later resizes debounce.
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 172, Height: 48})
	runCmd(cmd)
	m = next.(Model)
	if countResizes(t, conn) == 0 {
		t.Fatal("setup is wrong: a healthy first resize shipped nothing")
	}

	overlay := NewPaneModel("overlay-1", testRingBufSize)
	t.Cleanup(overlay.Dispose)
	tab := m.projects[0].tabs[0]
	tab.overlayPane = overlay
	tab.overlayVisible = true
	clearSent(conn)

	// Shrink. The tea.Tick is deliberately not run — the arm under test is the
	// resizeTickMsg one, delivered here directly through Update.
	next, _ = m.Update(tea.WindowSizeMsg{Width: 1, Height: 1})
	m = next.(Model)
	_, tickCmd := m.Update(resizeTickMsg{seq: m.resizeSeq})
	runCmd(tickCmd)

	if n := countResizes(t, conn); n != 0 {
		t.Fatalf("MsgResizePane count = %d, want 0 — the debounced arm must "+
			"honour the same minimum as the first resize, for tree panes and "+
			"for the overlay pane", n)
	}
}

// The third fan-out: every daemon broadcast diffs the pane sizes and pushes
// what disagrees. At 1x1 that is every pane in the workspace, on every
// broadcast, which is what reached 48 tabs.
func TestUpdate_WorkspaceStateBelowMinimum_ShipsNoPaneResize(t *testing.T) {
	m, conn := tinyTermModel(t)

	next, cmd := m.Update(tea.WindowSizeMsg{Width: 1, Height: 1})
	runCmd(cmd)
	m = next.(Model)
	clearSent(conn)

	// Receive() must not park the listen command the broadcast arm re-arms.
	close(conn.recv)

	_, cmd = m.Update(WorkspaceStateMsg{
		Dest:          "",
		ActiveProject: "proj-1",
		ActiveTab:     "tab-1",
		Projects:      []ProjectInfo{{ID: "proj-1", Name: "Default", TabIDs: []string{"tab-1"}}},
		Tabs:          []TabInfo{{ID: "tab-1", Name: "Shell", ProjectID: "proj-1", Panes: []string{"pane-1"}}},
		Panes:         []PaneInfo{{ID: "pane-1", TabID: "tab-1", Type: "terminal"}},
	})
	runCmd(cmd)

	if n := countResizes(t, conn); n != 0 {
		t.Fatalf("MsgResizePane count = %d, want 0 — a broadcast must not push "+
			"pane sizes derived from a terminal the TUI refuses to paint", n)
	}
}

// The gate's whole safety argument is that nothing is permanently suppressed:
// the panes keep their last good size and are resized normally once a usable
// geometry is reported. Until this test existed that rested on reading alone —
// a gate that withheld a resize and never released it would look identical to
// this one in every other test in the file, all of which assert an absence.
func TestUpdate_GrowBackAboveMinimum_ShipsPaneResize(t *testing.T) {
	m, conn := tinyTermModel(t)

	next, cmd := m.Update(tea.WindowSizeMsg{Width: 1, Height: 1})
	runCmd(cmd)
	m = next.(Model)
	if n := countResizes(t, conn); n != 0 {
		t.Fatalf("setup is wrong: the degenerate size shipped %d resizes", n)
	}

	// The window is restored. The tea.Tick is not run; the arm under test is
	// resizeTickMsg, delivered here directly.
	next, _ = m.Update(tea.WindowSizeMsg{Width: 172, Height: 48})
	m = next.(Model)
	_, tickCmd := m.Update(resizeTickMsg{seq: m.resizeSeq})
	runCmd(tickCmd)

	if n := countResizes(t, conn); n != 1 {
		t.Fatalf("MsgResizePane count = %d, want 1 — a pane whose resize was "+
			"withheld must be sized again as soon as the terminal is usable", n)
	}
}

// An overlay pane sits OUTSIDE the layout tree, so resizeAllPanes never walks
// it and diffResizes keeps no sizedOnce ledger for it: the resizeTickMsg sweep
// is the only resize it ever receives. That made a background tab's overlay the
// one place the gate could withhold a resize with nothing owed afterwards —
// the pane kept its spawn-time size for the rest of its life.
func TestUpdate_GrowBackAboveMinimum_ResizesABackgroundTabsOverlay(t *testing.T) {
	m, conn := tinyTermModel(t)

	// A second tab, in the background, carrying a visible overlay.
	bg := NewTabModel("tab-2", "Git")
	bgLeaf := NewPaneModel("pane-2", testRingBufSize)
	t.Cleanup(bgLeaf.Dispose)
	bg.Root = NewLeaf(bgLeaf)
	overlay := NewPaneModel("overlay-2", testRingBufSize)
	t.Cleanup(overlay.Dispose)
	bg.overlayPane = overlay
	bg.overlayVisible = true
	m.projects[0].tabs = append(m.projects[0].tabs, bg)

	next, cmd := m.Update(tea.WindowSizeMsg{Width: 1, Height: 1})
	runCmd(cmd)
	m = next.(Model)
	clearSent(conn)

	next, _ = m.Update(tea.WindowSizeMsg{Width: 172, Height: 48})
	m = next.(Model)
	_, tickCmd := m.Update(resizeTickMsg{seq: m.resizeSeq})
	runCmd(tickCmd)

	if !sawResizeFor(t, conn, "overlay-2") {
		t.Fatal("the background tab's overlay pane was never resized — it is " +
			"outside the layout tree, so this sweep is the only resize it gets")
	}
}

// sawResizeFor reports whether a resize for paneID reached the wire, inside
// any MsgResizePanes batch.
func sawResizeFor(t *testing.T, conn *fakeConn, paneID string) bool {
	t.Helper()
	conn.mu.Lock()
	defer conn.mu.Unlock()
	for _, msg := range conn.sent {
		if msg.Type != ipc.MsgResizePanes {
			continue
		}
		var p ipc.ResizePanesPayload
		if err := msg.DecodePayload(&p); err != nil {
			t.Fatalf("decode resize_panes payload: %v", err)
		}
		for _, rp := range p.Panes {
			if rp.PaneID == paneID {
				return true
			}
		}
	}
	return false
}

// attachMessage is the one change that alters what the daemon SPAWNS: its
// Cols/Rows size the first PTY of an empty workspace. A floored 1x1 would start
// that child at one column; 0 lets handleAttach apply its own 80x24 default.
func TestAttachMessage_ReportsNoGeometryBelowTheMinimum(t *testing.T) {
	for _, tt := range []struct {
		name          string
		width, height int
		wantZero      bool
	}{
		{"console-less client", 1, 1, true},
		{"one column short", minTermWidth - 1, 40, true},
		{"one row short", 120, minTermHeight - 1, true},
		{"exactly the minimum", minTermWidth, minTermHeight, false},
		{"ordinary terminal", 172, 48, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{cfg: config.Default(), width: tt.width, height: tt.height}

			var p ipc.AttachPayload
			if err := m.attachMessage("", false).DecodePayload(&p); err != nil {
				t.Fatalf("decode attach payload: %v", err)
			}

			if tt.wantZero {
				if p.Cols != 0 || p.Rows != 0 {
					t.Fatalf("attach geometry = %dx%d at %dx%d, want 0x0 so the "+
						"daemon applies its own default", p.Cols, p.Rows, tt.width, tt.height)
				}
				return
			}
			if p.Cols <= 0 || p.Rows <= 0 {
				t.Fatalf("attach geometry = %dx%d at %dx%d, want a real size",
					p.Cols, p.Rows, tt.width, tt.height)
			}
		})
	}
}
