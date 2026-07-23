package tui

import (
	"reflect"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func cmd(label string, kw ...string) paletteCommand {
	return paletteCommand{action: palActNone, label: label, keywords: kw, enabled: true}
}

func TestFuzzyScore_Subsequence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		query, target string
		wantMatch     bool
	}{
		{"empty query matches", "", "anything", true},
		{"exact substring", "split", "Split horizontal", true},
		{"scattered subsequence", "sph", "Split horizontal", true},
		{"case insensitive", "SPLIT", "split horizontal", true},
		{"not a subsequence", "xyz", "Split horizontal", false},
		{"subsequence across words", "sh", "Split horizontal", true},
		{"reverse order fails", "hs", "Split horizontal", false},
		{"reverse order fails short", "ts", "st", false},
	} {
		_, ok := fuzzyScore(tc.query, tc.target)
		if ok != tc.wantMatch {
			t.Errorf("%s: matched=%v, want %v", tc.name, ok, tc.wantMatch)
		}
	}
}

func TestFuzzyScore_Ranking(t *testing.T) {
	t.Parallel()
	// Consecutive/prefix beats scattered.
	pre, _ := fuzzyScore("spl", "Split pane")
	scat, _ := fuzzyScore("spl", "special loop")
	if pre <= scat {
		t.Errorf("prefix-consecutive %d should beat scattered %d", pre, scat)
	}
	// Word-boundary match scores positively.
	boundary, _ := fuzzyScore("h", "Split horizontal")
	if boundary == 0 {
		t.Error("boundary match should have positive score")
	}
}

func TestCommandScore_BestOfLabelAndKeywords(t *testing.T) {
	t.Parallel()
	c := cmd("Split horizontal", "hsplit", "wide")
	if _, ok := commandScore("hsplit", c); !ok {
		t.Error("should match on keyword the label lacks")
	}
	if _, ok := commandScore("zzz", c); ok {
		t.Error("should not match")
	}
}

func TestFilterPalette_EmptyReturnsAllInOrder(t *testing.T) {
	t.Parallel()
	in := []paletteCommand{cmd("Alpha"), cmd("Beta"), cmd("Gamma")}
	got := filterPalette("", in)
	if !reflect.DeepEqual(got, in) {
		t.Errorf("empty query must return all in registry order, got %v", got)
	}
}

func TestFilterPalette_RanksAndStableTies(t *testing.T) {
	t.Parallel()
	in := []paletteCommand{
		cmd("Close pane"),       // 'close' matches at start
		cmd("Close tab"),        // 'close' matches at start, registry order after pane
		cmd("Split horizontal"), // no match
	}
	got := filterPalette("close", in)
	if len(got) != 2 {
		t.Fatalf("want 2 matches, got %d", len(got))
	}
	if got[0].label != "Close pane" || got[1].label != "Close tab" {
		t.Errorf("stable tie order broken: %q, %q", got[0].label, got[1].label)
	}
}

func hasEnabledAction(cmds []paletteCommand, a paletteAction) bool {
	for _, c := range cmds {
		if c.action == a && c.enabled {
			return true
		}
	}
	return false
}

func TestBuildPaletteCommands_NavigationAndGates(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t) // panes p1, p2 on tab 0
	cmds := m.buildPaletteCommands()

	var gotoP1, gotoP2, history, lazygit bool
	for _, c := range cmds {
		switch {
		case c.action == palActGoToPane && c.arg == "p1":
			gotoP1 = true
		case c.action == palActGoToPane && c.arg == "p2":
			gotoP2 = true
		case c.action == palActHistory:
			history = c.enabled
		case c.action == palActLazygit:
			lazygit = c.enabled
		}
	}
	if !gotoP1 || !gotoP2 {
		t.Errorf("both panes must have Go-to commands (p1=%v p2=%v)", gotoP1, gotoP2)
	}
	if history {
		t.Error("history must be disabled without a record_history plugin")
	}
	if lazygit {
		t.Error("lazygit must be disabled without an available plugin")
	}
	if !hasEnabledAction(cmds, palActClosePane) || !hasEnabledAction(cmds, palActSplitH) {
		t.Error("close-pane and split-horizontal must always be present and enabled")
	}
}

func TestRenderCommandPalette_WidthAndCursor(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.palette = paletteState{query: "close"}
	m.palette.commands = m.buildPaletteCommands()
	m.palette.filtered = filterPalette("close", m.palette.commands)
	out := renderCommandPalette(*m)
	if out == "" {
		t.Fatal("render produced empty output")
	}
	if !strings.Contains(out, "close") {
		t.Error("query text should appear in the rendered palette")
	}
}

func TestRenderCommandPalette_EmptyResults(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.palette = paletteState{query: "zzzzzz"}
	m.palette.commands = m.buildPaletteCommands()
	m.palette.filtered = filterPalette("zzzzzz", m.palette.commands)
	out := renderCommandPalette(*m)
	if !strings.Contains(out, "No matching") {
		t.Errorf("empty results should show a 'No matching' row, got:\n%s", out)
	}
}

func TestTruncateToWidth(t *testing.T) {
	t.Parallel()
	if got := truncateToWidth("hello", 10); got != "hello" {
		t.Errorf("no truncation needed: got %q, want hello", got)
	}
	if got := truncateToWidth("hello world", 5); lipgloss.Width(got) > 5 {
		t.Errorf("ascii truncation width %d > 5: %q", lipgloss.Width(got), got)
	}
	// Wide glyphs must be counted by cell, not rune.
	if got := truncateToWidth("你好世界", 3); lipgloss.Width(got) > 3 {
		t.Errorf("wide truncation width %d > 3: %q", lipgloss.Width(got), got)
	}
	if got := truncateToWidth("x", 0); got != "" {
		t.Errorf("w=0: got %q, want empty", got)
	}
}

func TestRenderPaletteRow_WideLabelNoOverflow(t *testing.T) {
	t.Parallel()
	// A wide-glyph label (a tab/pane name is user-settable) plus a long detail,
	// at a narrow inner width, must never render wider than inner — otherwise
	// lipgloss wraps the row and breaks the palette box border.
	c := paletteCommand{label: "🚀🚀🚀🚀🚀🚀🚀🚀 deploy service", detail: "alt+f2 / alt+shift+r", enabled: true}
	for _, inner := range []int{20, 30, 68} {
		row := renderPaletteRow(c, false, inner)
		if w := lipgloss.Width(row); w > inner {
			t.Errorf("inner=%d: row width = %d, want <= %d", inner, w, inner)
		}
	}
}

func TestLastCellsToWidth(t *testing.T) {
	t.Parallel()
	if got := lastCellsToWidth("hello", 10); got != "hello" {
		t.Errorf("no trim: got %q", got)
	}
	if got := lastCellsToWidth("hello world", 5); got != "world" {
		t.Errorf("tail: got %q, want world", got)
	}
	if got := lastCellsToWidth("你好世界", 4); lipgloss.Width(got) > 4 {
		t.Errorf("wide tail width %d > 4: %q", lipgloss.Width(got), got)
	}
	if got := lastCellsToWidth("x", 0); got != "" {
		t.Errorf("w=0: got %q, want empty", got)
	}
}

// Greptile P1: on a narrow terminal the old inner-width floor (20) exceeded the
// clamped box and long queries escaped the border. Assert every rendered content
// line fits inside paletteInnerWidth for both a long query and a full result set.
func TestRenderCommandPalette_NarrowTerminalNoOverflow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		width int
		query string
	}{
		{"long query narrow", 30, strings.Repeat("verylongquery ", 20)},
		{"full list narrow", 26, ""},
		{"full list mid", 50, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newSplitDragTestModel(t)
			m.width = tc.width
			m.palette.query = tc.query
			m.palette.commands = m.buildPaletteCommands()
			m.palette.filtered = filterPalette(tc.query, m.palette.commands)
			inner := m.paletteInnerWidth()
			for i, line := range strings.Split(renderCommandPalette(*m), "\n") {
				if w := lipgloss.Width(line); w > inner {
					t.Errorf("line %d width %d exceeds inner %d: %q", i, w, inner, line)
				}
			}
		})
	}
}

// Regression: dialogBorder draws its border inside Width, so a full-width row
// (label + right-aligned shortcut) must not soft-wrap the shortcut onto the next
// rendered line. Render through the real renderDialog path and assert the label
// and its shortcut land on the SAME rendered row.
func TestRenderCommandPalette_ShortcutNotWrapped(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.width, m.height = 100, 40
	m.dialog = dialogCommandPalette
	m.palette.commands = m.buildPaletteCommands()
	m.palette.filtered = filterPalette("", m.palette.commands)

	out := m.renderDialog()
	var row string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Split horizontal") {
			row = line
			break
		}
	}
	if row == "" {
		t.Fatal("Split horizontal row not found in rendered dialog")
	}
	if !strings.Contains(row, "alt+shift+h") {
		t.Errorf("shortcut wrapped to another line — label and shortcut must share a row:\n%q", row)
	}
}

func TestPaletteInnerWidth_NeverExceedsBox(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	for _, w := range []int{8, 10, 20, 26, 40, 100} {
		m.width = w
		boxW := paletteWidth
		if boxW > w-2 {
			boxW = w - 2
		}
		content := boxW - 4
		if got := m.paletteInnerWidth(); content >= 1 && got > content {
			t.Errorf("width=%d: inner %d exceeds content area %d", w, got, content)
		}
	}
}

func TestPaletteWindow_KeepsCursorVisible(t *testing.T) {
	t.Parallel()
	// n below the cap: full list, no scroll.
	if s, e := paletteWindow(0, 5); s != 0 || e != 5 {
		t.Errorf("small list: got [%d,%d), want [0,5)", s, e)
	}
	// Cursor past the first window shifts the window down.
	s, e := paletteWindow(paletteVisibleRows+3, 40)
	if s == 0 {
		t.Error("cursor beyond first window should shift start > 0")
	}
	if cursor := paletteVisibleRows + 3; cursor < s || cursor >= e {
		t.Errorf("cursor %d not in window [%d,%d)", cursor, s, e)
	}
	// Cursor at the end clamps to the last window.
	s, e = paletteWindow(39, 40)
	if e != 40 || s != 40-paletteVisibleRows {
		t.Errorf("end cursor: got [%d,%d), want [%d,40)", s, e, 40-paletteVisibleRows)
	}
}
