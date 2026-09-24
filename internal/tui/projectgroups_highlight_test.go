package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The two highlight backgrounds as SGR colour specs (the part after "48;").
const (
	hoverBGSpec = "5;252"
	dragBGSpec  = "5;153"
)

// cellBackgrounds returns, for every printed rune of s, the background SGR
// spec in force when it was printed ("" = none). The whole-width assertions
// read it: a highlight that stopped short — an unstyled indent or pad — shows
// up as a "" cell.
func cellBackgrounds(s string) []string {
	var out []string
	bg := ""
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			end := strings.IndexByte(s[i:], 'm')
			if end < 0 {
				break
			}
			params := strings.Split(s[i+2:i+end], ";")
			for j := 0; j < len(params); j++ {
				switch params[j] {
				case "", "0", "49":
					bg = ""
				case "38", "48", "58":
					n := 2 // 5;N
					if j+1 < len(params) && params[j+1] == "2" {
						n = 4 // 2;R;G;B
					}
					if params[j] == "48" && j+n < len(params) {
						bg = strings.Join(params[j+1:j+1+n], ";")
					}
					j += n
				}
			}
			i += end + 1
			continue
		}
		r := []rune(s[i:])[0]
		out = append(out, bg)
		i += len(string(r))
	}
	return out
}

// assertRowBG fails unless every cell of text is painted on bg — the WHOLE
// width, indent and padding included.
func assertRowBG(t *testing.T, what, text, bg string) {
	t.Helper()
	cells := cellBackgrounds(text)
	if len(cells) == 0 {
		t.Fatalf("%s: empty row", what)
	}
	for i, got := range cells {
		if got != bg {
			t.Fatalf("%s: cell %d background %q, want %q across the whole row: %q", what, i, got, bg, text)
		}
	}
}

// assertRowNoBG fails when any cell of text carries a background.
func assertRowNoBG(t *testing.T, what, text string) {
	t.Helper()
	for i, got := range cellBackgrounds(text) {
		if got != "" {
			t.Fatalf("%s: cell %d carries background %q, want none: %q", what, i, got, text)
		}
	}
}

// grpHover sends BUTTONLESS motion — what all-motion reporting delivers while
// the pointer merely moves.
func grpHover(m Model, x, y int) (Model, tea.Cmd) {
	updated, cmd := m.Update(tea.MouseMotionMsg{X: x, Y: y})
	return updated.(Model), cmd
}

// grpRowText is the painted text of sidebar row y at the fixture's width.
func grpRowText(m Model, y int) string {
	rows, _ := m.sidebarRows(22)
	return rows[y].text
}

// Rows (newGroupsSidebarModel): 0 PROJECTS, 1 L1, 2 ▾ G-A, 3 R1, 4 R1's host,
// 5 L2, 6 ▸ G-B.
func TestSidebarHover_ProjectRowIsPaintedLightGrey(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	got, _ := grpHover(*m, 3, 1)
	if want := (sidebarHoverKey{projectID: "l1"}); got.sidebarHover != want {
		t.Fatalf("sidebarHover = %+v, want %+v", got.sidebarHover, want)
	}
	assertRowBG(t, "L1's row", grpRowText(got, 1), hoverBGSpec)
	for _, y := range []int{0, 2, 3, 4, 5, 6} {
		assertRowNoBG(t, fmt.Sprintf("row %d", y), grpRowText(got, y))
	}
	// The plain text turns dark on the grey; the name is still there.
	if !strings.Contains(stripANSI(grpRowText(got, 1)), "L1") {
		t.Errorf("the hovered row lost its name: %q", stripANSI(grpRowText(got, 1)))
	}
}

// A remote project is two rows, and either one hovers the whole project.
func TestSidebarHover_RemoteHostRowHighlightsBothRows(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	got, _ := grpHover(*m, 3, 4) // R1's host row
	if want := (sidebarHoverKey{dest: "gpu01", projectID: "r1"}); got.sidebarHover != want {
		t.Fatalf("sidebarHover = %+v, want %+v", got.sidebarHover, want)
	}
	assertRowBG(t, "R1's name row", grpRowText(got, 3), hoverBGSpec)
	assertRowBG(t, "R1's host row", grpRowText(got, 4), hoverBGSpec)
	assertRowNoBG(t, "G-A's header", grpRowText(got, 2))
}

func TestSidebarHover_GroupHeaderIsHighlightedAlone(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	got, _ := grpHover(*m, 3, 2) // ▾ G-A
	if want := (sidebarHoverKey{group: "G-A"}); got.sidebarHover != want {
		t.Fatalf("sidebarHover = %+v, want %+v", got.sidebarHover, want)
	}
	assertRowBG(t, "G-A's header", grpRowText(got, 2), hoverBGSpec)
	for _, y := range []int{3, 4, 5} {
		assertRowNoBG(t, fmt.Sprintf("member row %d", y), grpRowText(got, y))
	}
}

// Motion anywhere off the PROJECTS rows clears the hover: a pane, the tab bar,
// the status bar, the heading and the PANES section.
func TestSidebarHover_MotionOffTheProjectRowsClearsIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		x, y int
	}{
		{"a pane", 50, 10},
		{"the tab bar", 50, 0},
		{"the status bar", 3, 39},
		{"the PROJECTS heading", 3, 0},
		{"a pane row in the sidebar", 3, 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newGroupsSidebarModel(t)
			got, _ := grpHover(*m, 3, 1)
			if got.sidebarHover == (sidebarHoverKey{}) {
				t.Fatal("setup: no hover")
			}
			got, _ = grpHover(got, tc.x, tc.y)
			if got.sidebarHover != (sidebarHoverKey{}) {
				t.Fatalf("sidebarHover = %+v after motion over %s, want none", got.sidebarHover, tc.name)
			}
			assertRowNoBG(t, "L1's row", grpRowText(got, 1))
		})
	}
}

// All-motion reporting delivers every pointer move over the whole terminal. A
// move that keeps the same row hovered changes nothing, so it serves the
// cached frame — checked against a FORCED rebuild of the returned model (see
// view_coalesce_test.go for why never against the previous frame). A move to
// another row rebuilds.
func TestSidebarHover_UnchangedHoverServesTheCachedFrame(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m, _ := newGroupsSidebarModel(t)
	m.viewCache = &viewCacheBox{}
	got, _ := grpHover(*m, 3, 1)
	got.View()
	for _, tc := range []struct {
		name  string
		x, y  int
		skips bool
	}{
		{"same cell", 3, 1, true},
		{"same row, another column", 9, 1, true},
		{"another row", 3, 5, false},
		{"same row again", 4, 5, true},
		{"over a pane: hover cleared", 50, 10, false},
		{"another pane cell: still none", 60, 20, true},
	} {
		builds := got.viewCache.builds
		next, _ := grpHover(got, tc.x, tc.y)
		delivered := next.View()
		skipped := next.viewCache.builds == builds
		forced := next
		forced.skipRender = false
		honest := forced.View()
		if delivered.Content != honest.Content {
			t.Errorf("%s: the delivered frame is STALE against an honest rebuild", tc.name)
		}
		if delivered.MouseMode != honest.MouseMode {
			t.Errorf("%s: delivered MouseMode %v != honest %v", tc.name, delivered.MouseMode, honest.MouseMode)
		}
		if skipped != tc.skips {
			t.Errorf("%s: skipped = %v, want %v", tc.name, skipped, tc.skips)
		}
		got = next
	}
}

// Buttonless motion never starts or advances a drag or a selection — not
// even a drag whose release was lost outside the window.
func TestSidebarHover_ButtonlessMotionNeverDrags(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	got := *m
	tabDrag := got.tabDragFromIdx
	// Sidebar rows, the sidebar edge, the split border, a pane, the tab bar.
	for _, p := range [][2]int{{3, 1}, {3, 2}, {3, 5}, {21, 10}, {49, 10}, {30, 10}, {50, 0}, {60, 20}} {
		got, _ = grpHover(got, p[0], p[1])
	}
	if got.projectDragging || got.groupDragging || got.sidebarTabDragging || got.sidebarDragging ||
		got.tabDragFromIdx != tabDrag || got.splitDragNode != nil || got.mouseDown || got.selection != nil ||
		got.paneDrag.active() || got.scrollDragPaneID != "" {
		t.Fatal("buttonless motion armed a drag or a selection")
	}
	// A project press whose release never arrived: the pointer moves on with
	// no button held, and the project must not follow it.
	got, _ = grpPress(got, 5, tea.MouseLeft) // L2
	order := grpProjectIDs(got)
	got, _ = grpHover(got, 3, 3)
	got, _ = grpHover(got, 3, 1)
	if got.projectDragMoved || grpProjectIDs(got) != order {
		t.Fatalf("buttonless motion advanced an armed drag: moved %v, order %s (was %s)", got.projectDragMoved, grpProjectIDs(got), order)
	}
}

// grpProjectIDs joins m.projects' IDs in order.
func grpProjectIDs(m Model) string {
	ids := make([]string, len(m.projects))
	for i, p := range m.projects {
		ids[i] = p.ID
	}
	return strings.Join(ids, ",")
}

// Hover needs buttonless motion, which only all-motion reporting delivers — so
// it is on whenever the project sidebar is painted, and off (cell motion) when
// neither the sidebar nor a context menu is.
func TestView_MouseModeFollowsTheProjectSidebar(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m, _ := newGroupsSidebarModel(t)
	if v := m.View(); v.MouseMode != tea.MouseModeAllMotion {
		t.Errorf("sidebar visible: MouseMode %v, want all-motion", v.MouseMode)
	}
	m.sidebarOpen = false
	if v := m.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("sidebar hidden, no menu: MouseMode %v, want cell-motion", v.MouseMode)
	}
	m.sidebarOpen = true
	got := grpOpenNewGroup(t, *m, 1)
	if v := got.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("a dialog is up (no sidebar painted): MouseMode %v, want cell-motion", v.MouseMode)
	}
}

// grpProjectRowY is the first row of project id in the painted slice.
func grpProjectRowY(t *testing.T, m Model, id string) int {
	t.Helper()
	rows, _ := m.sidebarRows(22)
	for y, r := range rows {
		if r.kind == sidebarRowProject && m.projects[r.index].ID == id {
			return y
		}
	}
	t.Fatalf("no row for project %s", id)
	return -1
}

// A project drag that has left its press row paints the dragged project light
// blue, following it as it reorders; the release clears it.
func TestSidebarDragHighlight_ProjectRowIsLightBlueUntilRelease(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	got, _ := grpPress(*m, 5, tea.MouseLeft) // L2, below R1 in G-A
	assertRowNoBG(t, "L2 pressed, not yet moved", grpRowText(got, 5))
	got, _ = grpMotion(got, 3) // R1's name row: L2 moves above R1
	if !got.projectDragMoved {
		t.Fatal("setup: the drag did not move")
	}
	y := grpProjectRowY(t, got, "l2")
	if y != 3 {
		t.Fatalf("setup: L2 is at row %d, want 3 after passing R1", y)
	}
	assertRowBG(t, "the dragged L2", grpRowText(got, y), dragBGSpec)
	for _, other := range []int{1, 2, 4, 5, 6} {
		assertRowNoBG(t, fmt.Sprintf("row %d", other), grpRowText(got, other))
	}
	got, _ = grpRelease(got, 3)
	for y := 1; y <= 6; y++ {
		assertRowNoBG(t, fmt.Sprintf("row %d after release", y), grpRowText(got, y))
	}
}

// A header drag paints the HEADER light blue — not its members — and the
// release clears it.
func TestSidebarDragHighlight_GroupHeaderOnlyUntilRelease(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	m.groups.Groups[1].Collapsed = false // G-B shows L3, so "not its members" is checked
	// Rows: 0 PROJECTS, 1 L1, 2 ▾ G-A, 3 R1, 4 host, 5 L2, 6 ▾ G-B, 7 L3.
	got, _ := grpPress(*m, 6, tea.MouseLeft)
	assertRowNoBG(t, "G-B pressed, not yet moved", grpRowText(got, 6))
	got, _ = grpMotion(got, 2) // over G-A's header: G-B moves first
	if names := grpNames(got.groups); names != "G-B,G-A" || !got.groupDragMoved {
		t.Fatalf("setup: order %s moved %v, want G-B,G-A / true", names, got.groupDragMoved)
	}
	// Rows now: 0 PROJECTS, 1 L1, 2 ▾ G-B, 3 L3, 4 ▾ G-A, 5 R1, 6 host, 7 L2.
	assertRowBG(t, "G-B's header", grpRowText(got, 2), dragBGSpec)
	for _, y := range []int{1, 3, 4, 5, 6, 7} {
		assertRowNoBG(t, fmt.Sprintf("row %d", y), grpRowText(got, y))
	}
	got, _ = grpRelease(got, 2)
	assertRowNoBG(t, "G-B's header after release", grpRowText(got, 2))
}

// Drag wins over hover on the same row.
func TestSidebarDragHighlight_BeatsHover(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	got, _ := grpHover(*m, 3, 5) // hover L2
	got, _ = grpPress(got, 5, tea.MouseLeft)
	got, _ = grpMotion(got, 3)
	if want := (sidebarHoverKey{projectID: "l2"}); got.sidebarHover != want {
		t.Fatalf("setup: sidebarHover = %+v, want %+v (a held-button motion moves no hover)", got.sidebarHover, want)
	}
	assertRowBG(t, "the dragged, hovered L2", grpRowText(got, grpProjectRowY(t, got, "l2")), dragBGSpec)
}

// Every highlighted row — hovered or dragged, project, host or header, indented
// or not — keeps exactly the strip width: .Width(w) would wrap a wider one and
// shift every row below the hit test.
func TestSidebarGroups_EveryHighlightedRowIsExactlyTheStripWidth(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	m.groups.Groups[0].Name = "构建构建构建构建构建构建构建构建"
	m.projects[1].Name = "远程机器的名字很长很长"
	m.projects[0].Name = "本地机器的名字很长很长"
	for _, w := range []int{1, 2, 3, 4, 5, 8, 12, 22, 40} {
		for _, hl := range []rowHighlight{rowHighlightHover, rowHighlightDrag} {
			for i := range m.projects {
				c := m.projects[i].counts()
				if n := lipgloss.Width(projectRow(m.projects[i].Name, c, 0, glyphLinkParked, i == 0, w, nil, hl)); n != w {
					t.Errorf("w=%d hl=%d project %d row is %d cells", w, hl, i, n)
				}
			}
			if n := lipgloss.Width(projectDestRow("user@a-very-long-host-name", w, hl)); n != w {
				t.Errorf("w=%d hl=%d host row is %d cells", w, hl, n)
			}
			if n := lipgloss.Width(groupHeaderRow(m.groups.Groups[0].Name, 2, false, paneStateCounts{blocked: 1, working: 2}, 0, glyphLinkRetry, w, hl)); n != w {
				t.Errorf("w=%d hl=%d header row is %d cells", w, hl, n)
			}
		}
	}
	// Through the Model: every hover target at the strip's own widths, the
	// indented member rows included.
	for _, w := range []int{5, 12, 22} {
		m.sidebarWidth = w
		for _, key := range []sidebarHoverKey{{projectID: "l1"}, {dest: "gpu01", projectID: "r1"}, {group: m.groups.Groups[0].Name}} {
			m.sidebarHover = key
			rows, _ := m.sidebarRows(w)
			for y, r := range rows {
				if r.kind != sidebarRowProject && r.kind != sidebarRowGroup {
					continue
				}
				if got := lipgloss.Width(r.text); got != w {
					t.Errorf("w=%d hover %+v row %d is %d cells, want %d", w, key, y, got, w)
				}
			}
		}
	}
}
