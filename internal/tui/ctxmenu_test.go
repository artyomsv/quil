package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func testItems() []ctxMenuItem {
	return []ctxMenuItem{
		{ctxActHistory, "Input history", false},
		{ctxActFocus, "Focus mode", true},
		{ctxActClose, "Close pane…", true},
	}
}

func TestCtxMenuPos_Clamping(t *testing.T) {
	t.Parallel()
	// Screen 100x40: content rows 1..38 (row 0 tab bar, row 39 status bar).
	for _, tc := range []struct {
		name           string
		ax, ay, bw, bh int
		wantX, wantY   int
	}{
		{"prefers cursor+1", 10, 10, 20, 8, 11, 11},
		{"right edge shifts left", 95, 10, 20, 8, 80, 11},
		{"bottom edge shifts up", 10, 36, 20, 8, 11, 31},
		{"top clamps to row 1", 10, -5, 20, 8, 11, 1},
		{"left clamps to col 0", -5, 10, 20, 8, 0, 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, y := ctxMenuPos(tc.ax, tc.ay, tc.bw, tc.bh, 100, 40)
			if x != tc.wantX || y != tc.wantY {
				t.Errorf("%s: got (%d,%d), want (%d,%d)", tc.name, x, y, tc.wantX, tc.wantY)
			}
		})
	}
}

func TestCtxMenuBoxSize(t *testing.T) {
	t.Parallel()
	w, h := ctxMenuBoxSize(testItems())
	// Longest label "Input history" = 13 cells; +2 padding +2 border = 17.
	if w != 17 {
		t.Errorf("w = %d, want 17", w)
	}
	if h != 5 { // 3 items + 2 border rows
		t.Errorf("h = %d, want 5", h)
	}
}

func TestCtxMenuHitRow(t *testing.T) {
	t.Parallel()
	s := ctxMenuState{paneID: "p", x: 10, y: 5, items: testItems()}
	for _, tc := range []struct {
		name   string
		px, py int
		row    int
		inside bool
	}{
		{"outside left", 9, 6, -1, false},
		{"top border", 12, 5, -1, true},
		{"first item", 12, 6, 0, true},
		{"third item", 12, 8, 2, true},
		{"bottom border", 12, 9, -1, true},
		{"left border col", 10, 6, -1, true},
		{"below box", 12, 10, -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row, inside := ctxMenuHitRow(s, tc.px, tc.py)
			if row != tc.row || inside != tc.inside {
				t.Errorf("%s: got (%d,%v), want (%d,%v)", tc.name, row, inside, tc.row, tc.inside)
			}
		})
	}
}

func TestNextEnabled_SkipsDisabledAndWraps(t *testing.T) {
	t.Parallel()
	items := testItems() // 0 disabled, 1+2 enabled
	if got := firstEnabled(items); got != 1 {
		t.Errorf("firstEnabled = %d, want 1", got)
	}
	if got := nextEnabled(items, 1, +1); got != 2 {
		t.Errorf("down from 1 = %d, want 2", got)
	}
	if got := nextEnabled(items, 2, +1); got != 1 {
		t.Errorf("down from 2 wraps past disabled 0 to 1, got %d", got)
	}
	if got := nextEnabled(items, 1, -1); got != 2 {
		t.Errorf("up from 1 wraps past disabled 0 to 2, got %d", got)
	}
	none := []ctxMenuItem{{ctxActFocus, "x", false}}
	if got := firstEnabled(none); got != -1 {
		t.Errorf("all disabled: firstEnabled = %d, want -1", got)
	}
}

func TestBuildCtxMenuItems_LabelsAndGates(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	pane := m.tabs[0].Root.Left.Pane
	pane.Muted = true
	pane.pinnedAttention = true

	items := m.buildCtxMenuItems(pane)
	if len(items) != 9 {
		t.Fatalf("item count = %d, want 9", len(items))
	}
	byID := map[ctxMenuAction]ctxMenuItem{}
	for _, it := range items {
		byID[it.id] = it
	}
	if byID[ctxActMute].label != "Unmute notifications" {
		t.Errorf("mute label = %q", byID[ctxActMute].label)
	}
	if byID[ctxActAttention].label != "Unmark attention" {
		t.Errorf("attention label = %q", byID[ctxActAttention].label)
	}
	// Test model has no plugin registry → both gated items disabled.
	if byID[ctxActHistory].enabled {
		t.Error("history should be disabled without RecordHistory plugin")
	}
	if byID[ctxActLazygit].enabled {
		t.Error("lazygit should be disabled without an available plugin")
	}
	if !byID[ctxActClose].enabled || !byID[ctxActFocus].enabled {
		t.Error("close/focus must always be enabled")
	}
}

func TestRenderCtxMenu_Dimensions(t *testing.T) {
	t.Parallel()
	s := ctxMenuState{paneID: "p", cursor: 1, items: testItems()}
	out := renderCtxMenu(s)
	lines := strings.Split(out, "\n")
	w, h := ctxMenuBoxSize(s.items)
	if len(lines) != h {
		t.Fatalf("rendered height = %d, want %d", len(lines), h)
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != w {
			t.Errorf("line %d width = %d, want %d", i, got, w)
		}
	}
}
