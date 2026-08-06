package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The zone is TWO columns — the sidebar's own last column and the first pane
// column — mirroring the split border's "both drawn glyphs grab the line"
// rule. Neither is claimed by anything else: there is no split at the sidebar
// boundary, so hitTestSplitBorder never returns it.
func TestHitTestSidebarEdge(t *testing.T) {
	// width 200 is well above minWidthForSidebar, so projectSidebarWidth()
	// returns the configured 22 and the boundary sits at columns 21 and 22.
	m := Model{width: 200, height: 50, sidebarOpen: true, sidebarWidth: 22}

	tests := []struct {
		name string
		x, y int
		want bool
	}{
		{"sidebar's last column", 21, 10, true},
		{"first pane column", 22, 10, true},
		{"one left of the zone", 20, 10, false},
		{"one right of the zone", 23, 10, false},
		{"row 0 is included", 21, 0, true},
		{"status bar row is excluded", 21, 49, false},
		{"below the status bar", 21, 60, false},
		{"negative y", 21, -1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.hitTestSidebarEdge(tt.x, tt.y); got != tt.want {
				t.Errorf("hitTestSidebarEdge(%d, %d) = %v, want %v", tt.x, tt.y, got, tt.want)
			}
		})
	}
}

// A closed sidebar and a terminal too narrow to spare one both report width 0
// through the same accessor. Neither may offer an edge to grab: there is no
// strip painted, so a press at column -1/0 would arm a drag on nothing.
func TestHitTestSidebarEdge_NoEdgeWithoutASidebar(t *testing.T) {
	closed := Model{width: 200, height: 50, sidebarOpen: false, sidebarWidth: 22}
	if closed.hitTestSidebarEdge(0, 10) || closed.hitTestSidebarEdge(21, 10) {
		t.Error("a closed sidebar offers a drag edge")
	}
	narrow := Model{width: minWidthForSidebar - 1, height: 50, sidebarOpen: true, sidebarWidth: 22}
	if narrow.hitTestSidebarEdge(0, 10) || narrow.hitTestSidebarEdge(21, 10) {
		t.Error("a sidebar suppressed by terminal width offers a drag edge")
	}
}

// The zone must track the ACTUAL rendered width, not the configured one:
// sidebarWidth() clamps, so a configured value the terminal cannot afford is
// rendered narrower and the edge has to be where the strip really ends.
func TestHitTestSidebarEdge_FollowsTheClampedWidth(t *testing.T) {
	m := Model{width: minWidthForSidebar, height: 50, sidebarOpen: true, sidebarWidth: 500}
	w := m.projectSidebarWidth()
	if w <= 0 {
		t.Fatalf("fixture renders no sidebar (width %d)", w)
	}
	if !m.hitTestSidebarEdge(w-1, 10) {
		t.Errorf("column %d (the clamped last column) is not on the edge", w-1)
	}
	if m.hitTestSidebarEdge(499, 10) {
		t.Error("the edge tracks the configured width rather than the rendered one")
	}
}

// The drag must NOT move m.sidebarWidth while it is in flight. View() calls
// tab.Resize on every frame, and an emulator resize unpaired with a PTY redraw
// permanently garbles pane content at the narrowest width crossed (the
// 2026-07-15 corruption bug). Mid-drag only the pending value moves.
func TestSidebarDrag_DoesNotResizeMidDrag(t *testing.T) {
	m := Model{width: 200, height: 50, sidebarOpen: true, sidebarWidth: 22}
	m.beginSidebarDrag()
	m.trackSidebarDrag(40)

	if m.sidebarWidth != 22 {
		t.Errorf("m.sidebarWidth = %d mid-drag, want it pinned at 22", m.sidebarWidth)
	}
	if m.sidebarDragW != 41 {
		t.Errorf("sidebarDragW = %d, want 41 (column 40 is the last sidebar column)", m.sidebarDragW)
	}
	if !m.sidebarDragging {
		t.Error("sidebarDragging false while a drag is in flight")
	}
}

// Release is the single commit point: it moves the real width, persists it,
// and flags the config so the value survives exit.
func TestSidebarDrag_ReleaseCommitsTheWidth(t *testing.T) {
	m := Model{width: 200, height: 50, sidebarOpen: true, sidebarWidth: 22}
	m.cfg.UI.SidebarWidth = 22
	m.beginSidebarDrag()
	m.trackSidebarDrag(40)
	m.finishSidebarDrag()

	if m.sidebarWidth != 41 {
		t.Errorf("m.sidebarWidth = %d after release, want 41", m.sidebarWidth)
	}
	if m.cfg.UI.SidebarWidth != 41 {
		t.Errorf("cfg.UI.SidebarWidth = %d, want 41", m.cfg.UI.SidebarWidth)
	}
	if !m.configChanged {
		t.Error("configChanged not set — the dragged width would be lost on exit")
	}
	if m.sidebarDragging {
		t.Error("sidebarDragging still set after release")
	}
}

// The clamp is sidebarWidth()'s, not a second copy: a drag past the right edge
// must land on the same value the renderer would use, or the sidebar silently
// disagrees with where the user let go.
func TestSidebarDrag_ClampsThroughSidebarWidth(t *testing.T) {
	m := Model{width: 200, height: 50, sidebarOpen: true, sidebarWidth: 22}
	m.beginSidebarDrag()
	m.trackSidebarDrag(500) // far past the terminal's right edge
	m.finishSidebarDrag()

	want := sidebarWidth(200, true, 500)
	if m.sidebarWidth != want {
		t.Errorf("m.sidebarWidth = %d, want %d (sidebarWidth's own clamp)", m.sidebarWidth, want)
	}
	if m.sidebarWidth >= m.width {
		t.Errorf("m.sidebarWidth = %d leaves no room for panes at width %d", m.sidebarWidth, m.width)
	}
}

// A drag to the far left must not produce a zero or negative width: that would
// make the strip vanish with no edge left to grab it back by. Collapsing is
// what the toggle is for.
func TestSidebarDrag_NeverCollapsesToZero(t *testing.T) {
	m := Model{width: 200, height: 50, sidebarOpen: true, sidebarWidth: 22}
	m.beginSidebarDrag()
	m.trackSidebarDrag(-10)
	m.finishSidebarDrag()

	if m.sidebarWidth < minSidebarWidth {
		t.Errorf("m.sidebarWidth = %d, want at least minSidebarWidth (%d)", m.sidebarWidth, minSidebarWidth)
	}
}

// A click with no motion must commit nothing: the user grabbed the edge and
// let go, which is not a resize.
func TestSidebarDrag_ClickWithoutMotionChangesNothing(t *testing.T) {
	m := Model{width: 200, height: 50, sidebarOpen: true, sidebarWidth: 22}
	m.cfg.UI.SidebarWidth = 22
	m.beginSidebarDrag()

	if cmd := m.finishSidebarDrag(); cmd != nil {
		t.Error("a click with no motion returned a relayout command")
	}
	if m.sidebarWidth != 22 || m.configChanged {
		t.Errorf("a click with no motion changed state: width=%d changed=%v", m.sidebarWidth, m.configChanged)
	}
}

// clearDragState zeroes every mutually-exclusive drag flag in one place, so a
// new drag mode is added by extending the helper rather than auditing each
// click handler.
func TestClearDragState_ClearsTheSidebarDrag(t *testing.T) {
	m := Model{width: 200, height: 50, sidebarOpen: true, sidebarWidth: 22}
	m.beginSidebarDrag()
	m.trackSidebarDrag(40)
	m.clearDragState()

	if m.sidebarDragging || m.sidebarDragW != 0 {
		t.Errorf("clearDragState left sidebarDragging=%v sidebarDragW=%d", m.sidebarDragging, m.sidebarDragW)
	}
}

// Motion with no drag armed must be inert — the mouse crosses the boundary
// column constantly during ordinary pane selection.
func TestSidebarDrag_TrackIsInertWithoutADrag(t *testing.T) {
	m := Model{width: 200, height: 50, sidebarOpen: true, sidebarWidth: 22}
	m.trackSidebarDrag(40)

	if m.sidebarDragW != 0 {
		t.Errorf("sidebarDragW = %d with no drag armed", m.sidebarDragW)
	}
}

// Driven through Update rather than by calling the verbs directly, because the
// decision under test is BRANCH ORDER: the edge check must run ahead of
// projectSidebarSwallowsMouse, which owns the sidebar's own last column and
// would otherwise swallow the press as a row click. A direct-call test passes
// against an edge the click handler never reaches.
func TestSidebarDrag_ThroughUpdate(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.sidebarOpen = true
	m.sidebarWidth = 22
	m.cfg.UI.SidebarWidth = 22
	if w := m.projectSidebarWidth(); w != 22 {
		t.Fatalf("fixture renders a %d-column sidebar, want 22", w)
	}

	// Column 21 is the sidebar's OWN last column — the side the user aims at
	// the edge from, and the one the swallow branch claims.
	updated, _ := m.Update(tea.MouseClickMsg{X: 21, Y: 10, Button: tea.MouseLeft})
	got := updated.(Model)
	if !got.sidebarDragging {
		t.Fatal("a press on the sidebar's last column did not arm the drag — the swallow branch took it")
	}

	updated, _ = got.Update(tea.MouseMotionMsg{X: 40, Y: 10})
	got = updated.(Model)
	if got.sidebarWidth != 22 {
		t.Errorf("m.sidebarWidth = %d mid-drag, want it pinned at 22", got.sidebarWidth)
	}
	if got.sidebarDragW != 41 {
		t.Errorf("sidebarDragW = %d, want 41", got.sidebarDragW)
	}

	updated, cmd := got.Update(tea.MouseReleaseMsg{X: 40, Y: 10, Button: tea.MouseLeft})
	got = updated.(Model)
	if got.sidebarWidth != 41 {
		t.Errorf("m.sidebarWidth = %d after release, want 41", got.sidebarWidth)
	}
	if cmd == nil {
		t.Error("release returned no command — the panes would keep their pre-drag size")
	}
	if got.sidebarDragging {
		t.Error("sidebarDragging still set after release")
	}
}

// The edge check must not over-claim: a press well inside the strip is a row
// click and has to stay one.
func TestSidebarDrag_InteriorPressIsStillARowClick(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.sidebarOpen = true
	m.sidebarWidth = 22

	updated, _ := m.Update(tea.MouseClickMsg{X: 5, Y: 2, Button: tea.MouseLeft})
	if updated.(Model).sidebarDragging {
		t.Error("a press inside the sidebar armed an edge drag")
	}
}

// Right-click on the edge belongs to the context menu, not the drag: the
// sidebar's own right-click opens rename/destroy, and stealing it would make
// those unreachable on the strip's last column.
func TestSidebarDrag_RightClickDoesNotArm(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.sidebarOpen = true
	m.sidebarWidth = 22

	updated, _ := m.Update(tea.MouseClickMsg{X: 21, Y: 2, Button: tea.MouseRight})
	if updated.(Model).sidebarDragging {
		t.Error("a right-click on the edge armed a drag")
	}
}
