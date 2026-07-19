package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func testItems() []ctxMenuItem {
	return []ctxMenuItem{
		{ctxActHistory, "Input history", false, false},
		{ctxActFocus, "Focus mode", true, true}, // group boundary below this row
		{ctxActClose, "Close pane…", true, false},
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
	// Longest label "Input history" = 13 cells; short title doesn't widen;
	// +2 padding +2 border = 17.
	s := ctxMenuState{paneID: "p", title: "p1", spaced: true, items: testItems()}
	w, h := s.boxSize()
	if w != 17 {
		t.Errorf("spaced w = %d, want 17", w)
	}
	// title + separator (2) + 3 items + 1 group gap + borders (2) = 8.
	if h != 8 {
		t.Errorf("spaced h = %d, want 8", h)
	}

	s.spaced = false
	if _, h = s.boxSize(); h != 7 { // title + separator (2) + 3 items + borders (2)
		t.Errorf("compact h = %d, want 7", h)
	}

	// A long title widens the box only up to ctxMenuTitleCap.
	s.title = strings.Repeat("x", ctxMenuTitleCap+20)
	if w, _ = s.boxSize(); w != ctxMenuTitleCap+2+2 {
		t.Errorf("capped w = %d, want %d", w, ctxMenuTitleCap+4)
	}
}

func TestCtxMenuHitRow(t *testing.T) {
	t.Parallel()
	// Spaced box at (10,5), h=8: y=5 top border, y=6 title, y=7 separator,
	// items at y=8/9 (same group), group gap at y=10, item 2 at y=11,
	// y=12 bottom border.
	s := ctxMenuState{paneID: "p", x: 10, y: 5, spaced: true, items: testItems()}
	for _, tc := range []struct {
		name   string
		px, py int
		row    int
		inside bool
	}{
		{"outside left", 9, 6, -1, false},
		{"top border", 12, 5, -1, true},
		{"title row", 12, 6, -1, true},
		{"separator row", 12, 7, -1, true},
		{"first item", 12, 8, 0, true},
		{"second item same group", 12, 9, 1, true},
		{"group gap row", 12, 10, -1, true},
		{"third item", 12, 11, 2, true},
		{"bottom border", 12, 12, -1, true},
		{"left border col", 10, 8, -1, true},
		{"right border col", 26, 8, -1, true},
		{"below box", 12, 13, -1, false},
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
	none := []ctxMenuItem{{ctxActFocus, "x", false, false}}
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
	// Focus label tracks the active tab's focus-mode state.
	if byID[ctxActFocus].label != "Enter focus mode" {
		t.Errorf("focus label = %q, want Enter focus mode", byID[ctxActFocus].label)
	}
	m.tabs[0].ToggleFocus()
	items = m.buildCtxMenuItems(pane)
	for _, it := range items {
		if it.id == ctxActFocus && it.label != "Exit focus mode" {
			t.Errorf("focus label in focus mode = %q, want Exit focus mode", it.label)
		}
	}
}

func TestRenderCtxMenu_Dimensions(t *testing.T) {
	t.Parallel()
	for _, spaced := range []bool{true, false} {
		// Overlong title exercises the render-time truncation: the box must
		// keep its cell-exact geometry regardless of the title's raw length.
		s := ctxMenuState{
			paneID: "p",
			title:  strings.Repeat("t", ctxMenuTitleCap+10),
			cursor: 1,
			spaced: spaced,
			items:  testItems(),
		}
		out := renderCtxMenu(s)
		lines := strings.Split(out, "\n")
		w, h := s.boxSize()
		if len(lines) != h {
			t.Fatalf("spaced=%v: rendered height = %d, want %d", spaced, len(lines), h)
		}
		for i, l := range lines {
			if got := ansi.StringWidth(l); got != w {
				t.Errorf("spaced=%v: line %d width = %d, want %d", spaced, i, got, w)
			}
		}
	}
}
