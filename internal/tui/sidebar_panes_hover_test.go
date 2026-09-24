package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// paneHoverBGSpec is the hovered pane's darker grey as an SGR colour spec.
const paneHoverBGSpec = "5;249"

// newPanesHoverModel is newGroupsSidebarModel with a git row under p1 and a
// second tab U holding p3. Rows: 0 PROJECTS, 1 L1, 2 ▾ G-A, 3 R1, 4 host,
// 5 L2, 6 ▸ G-B, 7 blank, 8 PANES, 9 "1:T", 10 p1, 11 p1's git row, 12 p2,
// 13 blank, 14 "2:U", 15 p3.
func newPanesHoverModel(t *testing.T) Model {
	t.Helper()
	m, _ := newGroupsSidebarModel(t)
	tabs := m.curTabs()
	tabs[0].Root.Left.Pane.GitBranch = "main"
	u := NewTabModel("U", "U")
	p3 := NewPaneModel("p3", 1024)
	u.Root = NewLeaf(p3)
	u.ActivePane = "p3"
	m.appendTab(u)
	rows, _ := m.sidebarRows(22)
	want := []struct {
		y    int
		kind string
		what string
	}{{9, sidebarRowTab, "T"}, {10, sidebarRowPane, "p1"}, {11, "", "p1"}, {12, sidebarRowPane, "p2"}, {14, sidebarRowTab, "U"}, {15, sidebarRowPane, "p3"}}
	for _, w := range want {
		r := rows[w.y]
		got := r.paneID
		if w.kind == sidebarRowTab {
			got = m.curTabs()[r.tabIdx].ID
		} else if w.kind == "" {
			got = r.gitOf
		}
		if r.kind != w.kind || got != w.what {
			t.Fatalf("setup: row %d = %+v, want %s %s", w.y, r, w.kind, w.what)
		}
	}
	return *m
}

// assertRowsBG checks the background of each listed row, "" meaning none.
func assertRowsBG(t *testing.T, m Model, want map[int]string) {
	t.Helper()
	for y := 7; y <= 15; y++ {
		bg, listed := want[y]
		if !listed {
			bg = ""
		}
		text := grpRowText(m, y)
		if bg == "" {
			assertRowNoBG(t, fmt.Sprintf("row %d", y), text)
			continue
		}
		assertRowBG(t, fmt.Sprintf("row %d", y), text, bg)
	}
}

func TestSidebarPanesHover_TabHeadingShadesItsWholeBlock(t *testing.T) {
	got, _ := grpHover(newPanesHoverModel(t), 3, 9)
	if want := (sidebarHoverKey{tabID: "T"}); got.sidebarHover != want {
		t.Fatalf("sidebarHover = %+v, want %+v", got.sidebarHover, want)
	}
	// The block — heading, both panes, the git row — and nothing of U's; the
	// blank rows between blocks belong to neither.
	assertRowsBG(t, got, map[int]string{9: hoverBGSpec, 10: hoverBGSpec, 11: hoverBGSpec, 12: hoverBGSpec})
}

func TestSidebarPanesHover_PaneRowIsDarkerOnItsShadedBlock(t *testing.T) {
	got, _ := grpHover(newPanesHoverModel(t), 3, 12) // p2
	if want := (sidebarHoverKey{tabID: "T", paneID: "p2"}); got.sidebarHover != want {
		t.Fatalf("sidebarHover = %+v, want %+v", got.sidebarHover, want)
	}
	assertRowsBG(t, got, map[int]string{9: hoverBGSpec, 10: hoverBGSpec, 11: hoverBGSpec, 12: paneHoverBGSpec})
	// The state glyph keeps its own colour on the grey (the idle ○ in 243),
	// while the label beside it turns dark.
	text := grpRowText(got, 12)
	if !strings.Contains(text, "38;5;243;48;5;249m"+glyphIdle) {
		t.Errorf("the idle glyph lost its colour on the hovered row: %q", text)
	}
	if !strings.Contains(text, "38;5;235;48;5;249m p2") {
		t.Errorf("the hovered pane's label is not dark on the grey: %q", text)
	}
}

// A pane's git row is part of that pane: hovering it shades the pane and the
// git row darker, the rest of the block the block's grey.
func TestSidebarPanesHover_GitRowHoversItsPane(t *testing.T) {
	got, _ := grpHover(newPanesHoverModel(t), 3, 11)
	if want := (sidebarHoverKey{tabID: "T", paneID: "p1"}); got.sidebarHover != want {
		t.Fatalf("sidebarHover = %+v, want %+v", got.sidebarHover, want)
	}
	assertRowsBG(t, got, map[int]string{9: hoverBGSpec, 10: paneHoverBGSpec, 11: paneHoverBGSpec, 12: hoverBGSpec})
}

func TestSidebarPanesHover_LeavingThePanesSectionClearsIt(t *testing.T) {
	for _, p := range [][2]int{{3, 13}, {3, 8}, {50, 10}, {3, 1}} {
		got, _ := grpHover(newPanesHoverModel(t), 3, 12)
		got, _ = grpHover(got, p[0], p[1])
		if got.sidebarHover.tabID != "" {
			t.Fatalf("(%d,%d): sidebarHover = %+v, want no tab", p[0], p[1], got.sidebarHover)
		}
		assertRowsBG(t, got, nil)
	}
}

// The key is the tab's and pane's ID, so a rebuild that moves the rows — here
// the tabs trading places, as a broadcast or another client's reorder does —
// keeps the highlight on the same tab rather than on whatever now sits at the
// old position.
func TestSidebarPanesHover_KeyFollowsTheTabAcrossARowRebuild(t *testing.T) {
	got, _ := grpHover(newPanesHoverModel(t), 3, 14) // U's heading
	if !got.moveTab(1, 0) {
		t.Fatal("setup: moveTab refused")
	}
	// Rows now: 9 "1:U", 10 p3, 11 blank, 12 "2:T", 13 p1, 14 git, 15 p2.
	assertRowsBG(t, got, map[int]string{9: hoverBGSpec, 10: hoverBGSpec})
}

// A move along one row reuses the resolved key, and a PANES wheel scroll —
// which moves the rows under a still pointer — rebuilds the frame, so the
// next move at the same y resolves the row that is there now.
func TestSidebarPanesHover_SameRowReusesTheKeyAndAScrollReResolves(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newPanesHoverModel(t)
	m.height = 14 // PANES body is windowed: rows 9-11, then "N below"
	m.viewCache = &viewCacheBox{}
	m.View()
	c := m.viewCache
	got := grpHoverFrame(m, 3, 10) // p1
	if want := (sidebarHoverKey{tabID: "T", paneID: "p1"}); got.sidebarHover != want {
		t.Fatalf("sidebarHover = %+v, want %+v", got.sidebarHover, want)
	}
	got = grpHoverFrame(got, 5, 10)
	resolves := c.hoverResolves
	got = grpHoverFrame(got, 9, 10)
	got = grpHoverFrame(got, 12, 10)
	if c.hoverResolves != resolves {
		t.Fatalf("moves along row 10 resolved %d more times, want 0", c.hoverResolves-resolves)
	}
	next, _ := got.Update(tea.MouseWheelMsg{X: 3, Y: 10, Button: tea.MouseWheelDown})
	got = next.(Model)
	got.View()
	if got.sidebarScroll == 0 {
		t.Fatal("setup: the wheel did not scroll the PANES section")
	}
	fresh := got.resolveSidebarHover(3, 10)
	if fresh == (sidebarHoverKey{tabID: "T", paneID: "p1"}) {
		t.Fatal("setup: the scroll left p1 under y=10")
	}
	got = grpHoverFrame(got, 3, 10)
	if got.sidebarHover != fresh {
		t.Fatalf("after the scroll sidebarHover = %+v, want %+v — a stale cached key", got.sidebarHover, fresh)
	}
}

// No hover shading while a drag is active.
func TestSidebarPanesHover_SuppressedDuringADrag(t *testing.T) {
	got, _ := grpHover(newPanesHoverModel(t), 3, 12)
	// A header drag: a project press would switch projects and replace the
	// PANES section under test.
	got, _ = grpPress(got, 2, tea.MouseLeft) // ▾ G-A
	got, _ = grpMotion(got, 6)
	if !got.groupDragging {
		t.Fatal("setup: no drag armed")
	}
	assertRowsBG(t, got, nil)
}

// Every hover-shaded PANES row is exactly the strip width.
func TestSidebarPanesHover_ShadedRowsAreExactlyTheStripWidth(t *testing.T) {
	pane := NewPaneModel("a-pane-with-a-long-id", 1024)
	pane.Name = "构建构建构建构建构建"
	pane.working, pane.pinnedAttention = true, true
	pane.GitBranch, pane.GitWorktree, pane.GitUpstream, pane.GitAhead = "feat/构建-long-branch", true, true, 12
	for _, w := range []int{1, 2, 3, 4, 5, 8, 12, 22, 40} {
		for _, hl := range []rowHighlight{rowHighlightHover, rowHighlightPaneHover} {
			for _, focused := range []bool{false, true} {
				if n := lipgloss.Width(paneRowHL(pane, focused, w, hl)); n != w {
					t.Errorf("w=%d hl=%d focused=%v: pane row is %d cells", w, hl, focused, n)
				}
			}
			if n := lipgloss.Width(gitRowHL(pane, w, hl)); n != w {
				t.Errorf("w=%d hl=%d: git row is %d cells", w, hl, n)
			}
			if n := lipgloss.Width(sidebarTabHeadingHL("构建构建构建", 11, true, "#ff0000", w, hl)); n != w {
				t.Errorf("w=%d hl=%d: tab heading is %d cells", w, hl, n)
			}
		}
	}
	// Through the Model, every PANES row of a hovered block.
	m := newPanesHoverModel(t)
	for _, y := range []int{9, 10, 11, 12} {
		got, _ := grpHover(m, 3, y)
		rows, _ := got.sidebarRows(22)
		for i, r := range rows {
			if r.inTab {
				if n := lipgloss.Width(r.text); n != 22 {
					t.Errorf("hover y=%d: row %d is %d cells", y, i, n)
				}
			}
		}
	}
}
