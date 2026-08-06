package tui

import "testing"

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
