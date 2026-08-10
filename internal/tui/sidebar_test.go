package tui

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// tabWith builds a tab holding the given panes, splitting left-to-right so a
// caller can hand a project several panes in one call. The single-pane
// sibling (tabWithPane, router_test.go) predates this and is reused as-is.
func tabWith(panes ...*PaneModel) *TabModel {
	t := NewTabModel("tab-"+panes[0].ID, "T")
	node := NewLeaf(panes[0])
	for _, p := range panes[1:] {
		node = &LayoutNode{Left: node, Right: NewLeaf(p), Ratio: 0.5}
	}
	t.Root = node
	t.ActivePane = panes[0].ID
	return t
}

func TestSidebarShowsProjectCountsAndBlockedReason(t *testing.T) {
	working := &PaneModel{ID: "pane-1"}
	working.working = true
	blocked := &PaneModel{ID: "pane-2"}
	blocked.blockedSince = time.Now()
	blocked.blockedReason = "Bash"

	tab := NewTabModel("tab-1", "AI")
	tab.Root = &LayoutNode{Left: NewLeaf(working), Right: NewLeaf(blocked), Ratio: 0.5}

	m := Model{
		projects:     []*ProjectModel{{ID: "proj-a", Name: "quil", tabs: []*TabModel{tab}}},
		sidebarOpen:  true,
		sidebarWidth: 22,
	}

	out := m.renderSidebar(20)
	if !strings.Contains(out, "quil") {
		t.Fatal("project name missing")
	}
	if !strings.Contains(out, "Bash") {
		t.Fatal("blocked reason missing — a bare ⚠ does not say what it wants")
	}
}

func TestSidebarSanitizesRemoteStrings(t *testing.T) {
	m := Model{
		projects:     []*ProjectModel{{ID: "proj-a", Name: "evil‮coffee", Dest: "gpu01"}},
		sidebarOpen:  true,
		sidebarWidth: 22,
	}
	if strings.ContainsRune(m.renderSidebar(10), '‮') {
		t.Fatal("a bidi override from a remote host reached the screen")
	}
}

func TestSidebarWidthZeroWhenClosedOrNarrow(t *testing.T) {
	if got := sidebarWidth(200, false, 22); got != 0 {
		t.Fatalf("closed = %d, want 0", got)
	}
	if got := sidebarWidth(60, true, 22); got != 0 {
		t.Fatalf("narrow terminal must auto-collapse, got %d", got)
	}
	if got := sidebarWidth(200, true, 22); got != 22 {
		t.Fatalf("width = %d, want 22", got)
	}
	// A configured width larger than the terminal must not drive
	// paneAreaWidth() negative — it reaches tab.Resize and lipgloss.Width()
	// downstream. Clamped to leave at least minTermWidth for panes.
	if got := sidebarWidth(200, true, 5000); got != 200-minTermWidth {
		t.Fatalf("oversized configured width = %d, want %d (200-minTermWidth)", got, 200-minTermWidth)
	}
}

// ---------------------------------------------------------------------------
// Sidebar hit-testing
// ---------------------------------------------------------------------------

// Row geometry, once, for every test below: View() joins the sidebar to the
// LEFT of the pane column, tab bar included, so the strip starts at screen
// row 0 and sidebar row k is screen row k. Row 0 is the PROJECTS heading —
// level with the tab names, as the design's mockup draws it — so project i
// sits at screen row i+1.
func TestClickingProjectRowSwitchesProject(t *testing.T) {
	m := Model{
		projects:     []*ProjectModel{{ID: "proj-a", Name: "alpha"}, {ID: "proj-b", Name: "beta"}},
		sidebarOpen:  true,
		sidebarWidth: 22, width: 200, height: 40,
	}
	if kind, idx := m.sidebarHit(3, 0); kind != "" {
		t.Fatalf("the PROJECTS heading is chrome, got (%q, %d)", kind, idx)
	}
	if kind, idx := m.sidebarHit(3, 1); kind != sidebarRowProject || idx != 0 {
		t.Fatalf("sidebarHit(3, 1) = (%q, %d), want (project, 0)", kind, idx)
	}
	kind, idx := m.sidebarHit(3, 2) // second project row: 0 heading, 1 project 0
	if kind != sidebarRowProject || idx != 1 {
		t.Fatalf("sidebarHit = (%q, %d), want (project, 1)", kind, idx)
	}
	// The status bar is still drawn full width beneath the sidebar — a press
	// on it belongs to the status bar.
	if kind, _ := m.sidebarHit(3, m.height-1); kind != "" {
		t.Error("the last row is the status bar, not the sidebar")
	}
	// The tab bar no longer spans the frame: it starts where the sidebar
	// ends, so row 0 in these columns is the sidebar's, not the bar's.
	if !m.projectSidebarSwallowsMouse(3, 0) {
		t.Error("row 0 inside the sidebar's columns must belong to the sidebar")
	}
}

func TestClickBeyondSidebarIsNotSwallowed(t *testing.T) {
	m := Model{sidebarOpen: true, sidebarWidth: 22, width: 200, height: 40}
	if kind, _ := m.sidebarHit(40, 5); kind != "" {
		t.Fatalf("a click in the pane region must not be claimed by the sidebar, got %q", kind)
	}
	if m.projectSidebarSwallowsMouse(40, 5) {
		t.Error("column 40 is pane area with a 22-wide sidebar")
	}
	// Column 21 is the sidebar's last column, 22 is the pane area's first.
	if !m.projectSidebarSwallowsMouse(21, 5) {
		t.Error("column 21 is the sidebar's last column")
	}
	if m.projectSidebarSwallowsMouse(22, 5) {
		t.Error("column 22 is the first pane column")
	}
	m.sidebarOpen = false
	if m.projectSidebarSwallowsMouse(3, 5) {
		t.Error("a closed sidebar must swallow nothing")
	}
}

// The hit test and the paint must index the SAME row list. A hit test
// written as an independent second copy of the row layout drifts the moment
// a row is inserted, and the symptom — clicking one project and getting its
// neighbour — looks nothing like a rendering change.
func TestSidebarHitAgreesWithWhatIsPainted(t *testing.T) {
	m := Model{
		projects:     []*ProjectModel{{ID: "proj-a", Name: "alpha"}, {ID: "proj-b", Name: "beta"}},
		sidebarOpen:  true,
		sidebarWidth: 22, width: 200, height: 40,
	}
	lines := strings.Split(m.renderSidebar(m.sidebarContentHeight()), "\n")
	const screenY = 2 // 0 heading, 1 project 0
	kind, idx := m.sidebarHit(3, screenY)
	if kind != sidebarRowProject || idx != 1 {
		t.Fatalf("sidebarHit(3, %d) = (%q, %d), want (project, 1)", screenY, kind, idx)
	}
	if got := lines[screenY]; !strings.Contains(got, "beta") {
		t.Fatalf("painted sidebar row %d = %q, but the hit test calls it project 1 (beta)", screenY, got)
	}
}

// Pane rows are hit-testable too, and their ordinal is flat across the
// active project's tabs — Task 15's attention queue indexes the same list.
func TestSidebarHitResolvesPaneRows(t *testing.T) {
	first := &PaneModel{ID: "pane-1"}
	second := &PaneModel{ID: "pane-2"}
	m := Model{
		projects:     []*ProjectModel{{ID: "proj-a", Name: "alpha", tabs: []*TabModel{tabWith(first, second)}}},
		sidebarOpen:  true,
		sidebarWidth: 22, width: 200, height: 40,
	}
	// rows: 0 PROJECTS, 1 alpha, 2 spacer, 3 PANES, 4 tab heading, 5+ panes —
	// and sidebar row k is screen row k.
	if kind, idx := m.sidebarHit(3, 5); kind != sidebarRowPane || idx != 0 {
		t.Fatalf("sidebarHit(3, 5) = (%q, %d), want (pane, 0)", kind, idx)
	}
	if kind, idx := m.sidebarHit(3, 6); kind != sidebarRowPane || idx != 1 {
		t.Fatalf("sidebarHit(3, 6) = (%q, %d), want (pane, 1)", kind, idx)
	}
	// Past the last row there is nothing to hit, but the strip still
	// swallows — the pane area does not start there.
	if kind, _ := m.sidebarHit(3, 30); kind != "" {
		t.Error("rows below the content must resolve to no action")
	}
	if !m.projectSidebarSwallowsMouse(3, 30) {
		t.Error("empty sidebar rows must still be swallowed")
	}
}

// ---------------------------------------------------------------------------
// switchProject
// ---------------------------------------------------------------------------

func TestSwitchProjectNotifiesDaemonAndResyncsGeometry(t *testing.T) {
	fake := newFakeConn()
	m := Model{
		client: fake,
		projects: []*ProjectModel{
			{ID: "proj-a", Dest: ""},
			{ID: "proj-b", Dest: "gpu01", tabs: []*TabModel{tabWithPane("tab-9", "pane-9")}},
		},
		activeProject: 0,
	}

	if cmd := m.switchProject(1); cmd != nil {
		cmd()
	}

	var sawSwitch bool
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgSwitchProject {
			sawSwitch = true
			if msg.Origin != "gpu01" {
				t.Fatalf("switch Origin = %q, want gpu01", msg.Origin)
			}
		}
	}
	if !sawSwitch {
		t.Fatal("switchProject must send MsgSwitchProject or the daemon's activeProject goes stale")
	}
	if m.activeProject != 1 || m.prevProject != "proj-a" {
		t.Fatalf("activeProject/prevProject = %d/%q, want 1/proj-a", m.activeProject, m.prevProject)
	}
	// The incoming project's panes were last sized under whatever geometry
	// was current when it went to the background.
	var sawResize bool
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgResizePane {
			sawResize = true
		}
	}
	if !sawResize {
		t.Error("switchProject must resync the incoming project's PTY geometry")
	}
}

func TestSwitchProjectIgnoresOutOfRangeAndNoOp(t *testing.T) {
	fake := newFakeConn()
	m := Model{client: fake, projects: []*ProjectModel{{ID: "proj-a"}}, activeProject: 0}
	for _, i := range []int{-1, 0, 1, 99} {
		if cmd := m.switchProject(i); cmd != nil {
			t.Errorf("switchProject(%d) returned a cmd, want nil", i)
		}
	}
	if len(fake.sent) != 0 {
		t.Errorf("no-op switches must send nothing, got %d messages", len(fake.sent))
	}
}

// TestSwitchProjectResetsSidebarScroll pins the deliberate UX choice: a
// project switch always shows the incoming project's PANES body from the top,
// rather than carrying over an offset scrolled deep into the outgoing
// project's (possibly much longer) pane list.
func TestSwitchProjectResetsSidebarScroll(t *testing.T) {
	fake := newFakeConn()
	m := Model{
		client: fake,
		projects: []*ProjectModel{
			{ID: "proj-a"},
			{ID: "proj-b"},
		},
		activeProject: 0,
		sidebarScroll: 12,
	}

	if cmd := m.switchProject(1); cmd != nil {
		cmd()
	}

	if m.sidebarScroll != 0 {
		t.Errorf("sidebarScroll = %d after switching projects, want 0", m.sidebarScroll)
	}
}

// ---------------------------------------------------------------------------
// Mouse dispatch
// ---------------------------------------------------------------------------

func TestSidebarPressIsSwallowedAndDoesNotArmDragSelection(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.sidebarOpen = true
	m.sidebarWidth = 22
	m.projects[0].Name = "alpha"

	updated, _ := m.Update(tea.MouseClickMsg{X: 5, Y: 5, Button: tea.MouseLeft})
	got := updated.(Model)
	if got.mouseDown {
		t.Error("a press in the sidebar strip must not arm pane drag-selection")
	}
	if got.selection != nil {
		t.Error("a press in the sidebar strip must not start a selection")
	}
}

func TestSidebarWheelDoesNotScrollThePaneBeneath(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.sidebarOpen = true
	m.sidebarWidth = 22
	pane := m.curTabs()[0].ActivePaneModel()
	for i := 0; i < 200; i++ {
		pane.AppendOutput([]byte("filler line\r\n"))
	}
	// Control: maxScroll() is the VT's scrollback length, so on a fresh test
	// pane ScrollUp clamps straight back to 0 and "did not scroll" would be
	// true whether or not the swallow works.
	pane.ScrollUp(3)
	if pane.scrollBack == 0 {
		t.Fatal("fixture pane has no scrollback to scroll")
	}
	pane.ResetScroll()

	updated, _ := m.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelUp})
	got := updated.(Model)
	if after := got.curTabs()[0].ActivePaneModel().scrollBack; after != 0 {
		t.Errorf("pane scrolled behind the sidebar: 0 → %d", after)
	}
}

func TestClickOnProjectRowSwitchesThroughUpdate(t *testing.T) {
	fake := newFakeConn()
	m := newSplitDragTestModel(t)
	m.client = fake
	m.sidebarOpen = true
	m.sidebarWidth = 22
	m.projects = append(m.projects, &ProjectModel{ID: "proj-b", Name: "beta", Dest: "gpu01"})

	// Screen row 2 is the second project row: 0 heading, 1 project 0.

	updated, cmd := m.Update(tea.MouseClickMsg{X: 3, Y: 2, Button: tea.MouseLeft})
	if got := updated.(Model).activeProject; got != 1 {
		t.Fatalf("activeProject = %d, want 1 after clicking the second project row", got)
	}
	if cmd != nil {
		cmd()
	}
	var sawSwitch bool
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgSwitchProject {
			sawSwitch = true
		}
	}
	if !sawSwitch {
		t.Error("a click on a project row must reach switchProject")
	}
}

// ---------------------------------------------------------------------------
// X-origin: the pane area begins at the sidebar's right edge
// ---------------------------------------------------------------------------

// Every screen column the mouse reports is absolute, but the pane rects are
// derived from a tree walk that used to start at column 0. With the sidebar
// open the pane area genuinely starts at column sidebarWidth, so an
// unshifted walk puts every pane, every split line and every scrollbar that
// many columns left of where the user sees it.
func TestPaneGeometryStartsAtTheSidebarEdge(t *testing.T) {
	without := newSplitDragTestModel(t) // 100x40, H-split p1|p2, boundary col 49-50
	with := newSplitDragTestModel(t)
	with.sidebarOpen = true
	with.sidebarWidth = 22

	rectWithout := without.activePaneRect()
	rectWith := with.activePaneRect()
	if rectWithout == nil || rectWith == nil {
		t.Fatal("active pane rect resolved to nil")
	}
	if rectWithout.OX != 0 {
		t.Fatalf("baseline OX = %d, want 0 (sidebar closed)", rectWithout.OX)
	}
	if rectWith.OX != 22 {
		t.Fatalf("active pane OX = %d, want 22 — panes start where the sidebar ends", rectWith.OX)
	}

	// paneRectAt: the strip is not a pane, and the first pane column is the
	// sidebar's right edge.
	if r := with.paneRectAt(10, 10); r != nil {
		t.Errorf("paneRectAt(10, 10) = %q, want nil (that column is the sidebar)", r.Pane.ID)
	}
	if r := with.paneRectAt(22, 10); r == nil || r.Pane.ID != "p1" {
		t.Errorf("paneRectAt(22, 10) = %v, want p1", r)
	}

	// Split border: pane area is 78 wide, so the line sits at 22 + 39 = 61.
	// Column 50 is where it lived with the sidebar closed — now interior.
	if hit := with.hitTestSplitBorder(61, 10); hit == nil {
		t.Error("hitTestSplitBorder(61, 10) = nil, want the root split line")
	}
	if hit := with.hitTestSplitBorder(50, 10); hit != nil {
		t.Error("column 50 is the no-sidebar boundary; with the sidebar open it is pane interior")
	}

	// Scrollbar: left pane spans [22, 61), so its scrollbar column is
	// 22 + 39 - 2 = 59. Column 48 is the no-sidebar position.
	if r := with.hitTestScrollbar(59, 10); r == nil || r.Pane.ID != "p1" {
		t.Errorf("hitTestScrollbar(59, 10) = %v, want p1's scrollbar", r)
	}
	if r := with.hitTestScrollbar(48, 10); r != nil {
		t.Errorf("hitTestScrollbar(48, 10) = %q, want nil (the no-sidebar scrollbar column)", r.Pane.ID)
	}
}

// Drag-selection resolves its anchor through FindPaneRectAt in split mode and
// through a bare ox in focus mode — two sites, both of which convert a screen
// column into a pane-local one and so both of which need the offset.
func TestDragSelectionAnchorsAtTheSidebarEdge(t *testing.T) {
	for _, focus := range []bool{false, true} {
		m := newSplitDragTestModel(t)
		m.sidebarOpen = true
		m.sidebarWidth = 22
		tab := m.curTabs()[0]
		if focus {
			tab.ToggleFocus()
		}
		// Column 23 is one cell inside the left pane's border, i.e. its
		// content column 0.
		m.mouseStartX, m.mouseStartY = 23, 2
		m.updateMouseSelection(tab, 25, 2, m.height-chromeHeight)
		if m.selection == nil {
			t.Fatalf("focus=%v: no selection produced", focus)
		}
		if m.selection.Anchor.Col != 0 {
			t.Errorf("focus=%v: anchor col = %d, want 0 — screen column 23 is the pane's first content column",
				focus, m.selection.Anchor.Col)
		}
	}
}

// The click-to-focus path on mouse release walks the tree with its own
// FindPaneAt call, which is easy to miss when auditing the rect helpers.
//
// Column 45 is chosen because it DISCRIMINATES. The pane area is 78 wide
// either way, so a 0-seeded walk splits it [0,39)/[39,78) and a
// sidebar-seeded one splits it [22,61)/[61,100): column 45 is p2 under the
// old geometry and p1 under the correct one. A column such as 70 lands in
// p2 under both and would pass against the unfixed code.
func TestReleaseClickFocusesThePaneUnderTheCursor(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.sidebarOpen = true
	m.sidebarWidth = 22
	m.curTabs()[0].ActivePane = "p2" // start on the OTHER pane, so a no-op fails
	m.mouseDown = true
	m.mouseStartX, m.mouseStartY = 45, 10

	updated, _ := m.Update(tea.MouseReleaseMsg{X: 45, Y: 10, Button: tea.MouseLeft})
	after := updated.(Model)
	if got := after.curTabs()[0].ActivePane; got != "p1" {
		t.Errorf("ActivePane = %q, want p1 — column 45 is inside p1's rect [22, 61); "+
			"a 0-seeded walk puts it in p2", got)
	}
}

// ---------------------------------------------------------------------------
// Tab-bar origin: the bar labels the panes, so it starts where they do
// ---------------------------------------------------------------------------

// tabBarLabelCol reports the screen column of tab idx's label text in the
// painted frame's row 0, or -1. Deliberately measured off View()'s real
// output rather than off renderTabBar/hitTestTab: the shipped bug was that
// those two agreed with each other perfectly while both described a bar
// drawn in the wrong place, so a test written against either cannot see it.
func tabBarLabelCol(t *testing.T, m *Model, idx int) int {
	t.Helper()
	row0 := strings.Split(stripANSI(m.View().Content), "\n")[0]
	// The label carries the "* " active prefix and per-style padding; the
	// "<n>:<name>" core is the part that is stable across both.
	return strings.Index(row0, fmt.Sprintf("%d:%s", idx+1, m.curTabs()[idx].Name))
}

// The shipped bug: View() appended the tab bar as its own FULL-WIDTH vertical
// section, so screen row 0 spanned the terminal and the sidebar started at row
// 1. The tabs were painted from column 0 — over the sidebar, not over the
// panes they name — while the design puts the sidebar's PROJECTS heading on
// that same row beside them.
func TestTabBarStartsAtThePaneColumn(t *testing.T) {
	closed := newSplitDragTestModel(t)
	open := newSplitDragTestModel(t)
	open.sidebarOpen = true
	open.sidebarWidth = 22

	sw := open.projectSidebarWidth()
	if sw != 22 || closed.projectSidebarWidth() != 0 {
		t.Fatalf("fixture widths are %d open / %d closed, want 22 / 0",
			sw, closed.projectSidebarWidth())
	}

	colClosed := tabBarLabelCol(t, closed, 0)
	colOpen := tabBarLabelCol(t, open, 0)
	if colClosed < 0 || colOpen < 0 {
		t.Fatalf("tab label not painted on row 0 (closed=%d open=%d)", colClosed, colOpen)
	}
	if want := colClosed + sw; colOpen != want {
		t.Errorf("tab label sits at column %d with the sidebar open, want %d "+
			"(%d closed + %d sidebar) — the bar must start at the pane column, not column 0",
			colOpen, want, colClosed, sw)
	}

	// And row 0's left columns are the sidebar's own first row, which is
	// what makes PROJECTS level with the tab names.
	row0 := strings.Split(stripANSI(open.View().Content), "\n")[0]
	wantLeft := stripANSI(strings.Split(open.renderSidebar(open.sidebarContentHeight()), "\n")[0])
	if got := row0[:sw]; got != wantLeft {
		t.Errorf("row 0 columns [0,%d) = %q, want the sidebar's first row %q", sw, got, wantLeft)
	}

	// The hit test has to follow the paint, not just itself: the label's
	// painted column resolves to its tab, and the sidebar's columns resolve
	// to no tab at all.
	if got := open.hitTestTab(colOpen); got != 0 {
		t.Errorf("hitTestTab(%d) = %d, want 0 — that column is where tab 0 is painted", colOpen, got)
	}
	if got := open.hitTestTab(colClosed); got != -1 {
		t.Errorf("hitTestTab(%d) = %d, want -1 — column %d is inside the sidebar",
			colClosed, got, colClosed)
	}
}

// The bar's WIDTH moves with its origin: it has paneAreaWidth() to spend, not
// the terminal's. A budget left at m.width overflows the frame by the
// sidebar's columns, and — because the same budget decides which tabs are
// dropped — silently paints tabs that no longer fit.
func TestTabBarFitsThePaneColumnWhenTabsOverflow(t *testing.T) {
	names := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		names = append(names, fmt.Sprintf("tab-name-%d", i))
	}
	m := newModelForTest(names, 0)
	m.notifications = NewNotificationCenter(30, 200)
	m.width, m.height = 100, 40
	m.sidebarOpen = true
	m.sidebarWidth = 22
	m.projects[0].Name = "alpha"

	bar := m.renderTabBar()
	if got := lipgloss.Width(bar); got != m.paneAreaWidth() {
		t.Errorf("tab bar is %d cells, want paneAreaWidth = %d", got, m.paneAreaWidth())
	}
	if strings.Contains(bar, "\n") {
		t.Fatal("the tab bar wrapped onto a second line — every row below it shifts")
	}
	// Control: the fixture must actually overflow the pane column, or the
	// budget under test is never consulted.
	if !strings.Contains(stripANSI(bar), "more»") {
		t.Fatalf("fixture does not overflow paneAreaWidth=%d — this test cannot fail: %q",
			m.paneAreaWidth(), stripANSI(bar))
	}

	// Every frame row still measures exactly the terminal width.
	for i, line := range strings.Split(stripANSI(m.View().Content), "\n") {
		if got := lipgloss.Width(line); got != m.width {
			t.Fatalf("frame row %d measured %d cells, want %d", i, got, m.width)
		}
	}
}

// A single tab label wider than the bar escapes every budget check — the
// overflow path includes the active tab unconditionally — and lipgloss's
// .Width() WRAPS rather than truncates. Row 0 then becomes two lines and the
// whole frame shifts down one row while sidebarRowAt and every pane rect
// still compute against the unshifted layout.
//
// The fixture discriminates THIS regression rather than generic overflow: the
// label overflows paneAreaWidth() but still fits m.width, so it is only
// reachable because the bar was narrowed to the pane column.
func TestTabBarNeverWrapsWhenOneLabelOverflowsThePaneColumn(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.sidebarOpen = true
	m.sidebarWidth = 22
	m.projects[0].Name = "alpha"
	m.curTabs()[0].Name = strings.Repeat("feature-branch-", 6)[:84]

	// Control: without both bounds this is a generic-overflow test that the
	// pre-sidebar code would also have passed.
	labelW := lipgloss.Width(m.tabStyle(0).Render(m.tabLabel(0)))
	if labelW <= m.paneAreaWidth() || labelW > m.width {
		t.Fatalf("fixture label is %d cells; it must exceed paneAreaWidth=%d and still fit m.width=%d",
			labelW, m.paneAreaWidth(), m.width)
	}

	bar := m.renderTabBar()
	// lipgloss.Width of a WRAPPED bar reports the max line width, which is
	// already barW — so the width check alone passes on the broken code.
	// The line count is what actually catches it.
	if n := strings.Count(bar, "\n") + 1; n != 1 {
		t.Errorf("tab bar wrapped onto %d lines", n)
	}
	if got := lipgloss.Width(bar); got != m.paneAreaWidth() {
		t.Errorf("tab bar is %d cells, want %d", got, m.paneAreaWidth())
	}

	lines := strings.Split(stripANSI(m.View().Content), "\n")
	if len(lines) != m.height {
		t.Fatalf("frame has %d rows, want %d — a wrapped tab bar pushed every row down",
			len(lines), m.height)
	}
	if got := lipgloss.Width(lines[0]); got != m.width {
		t.Errorf("row 0 measured %d cells, want %d", got, m.width)
	}
	// Row 1 is the pane top border, not the tail of a wrapped tab label.
	if strings.Contains(lines[1], "feature-branch") {
		t.Errorf("row 1 carries wrapped tab-label text: %q", lines[1])
	}
	if !strings.Contains(lines[1], "╭") {
		t.Errorf("row 1 = %q, want the pane top border", lines[1])
	}
}

// ---------------------------------------------------------------------------
// Toggle keybinding
// ---------------------------------------------------------------------------

// The toggle must REFLOW, not just re-send. resizeAllPanes reads
// pane.Width/Height and ships them; only tab.Resize (via resizeTabs) writes
// them. The assertion is on a BACKGROUND tab because View() happens to
// resize the active one, which would mask a missing resizeTabs.
func TestSidebarToggleKeyFlipsResizesAndPersists(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.cfg = config.Default()
	m.initKeymap() // handleKey dispatches through the keymap; NewModel builds it
	if m.cfg.Keybindings.SidebarToggle != "alt+shift+s" {
		t.Fatalf("default sidebar_toggle = %q, want alt+shift+s", m.cfg.Keybindings.SidebarToggle)
	}
	background := NewTabModel("tab-bg", "BG")
	bgPane := NewPaneModel("p-bg", 1024)
	background.Root = NewLeaf(bgPane)
	background.ActivePane = bgPane.ID
	m.projects[0].tabs = append(m.projects[0].tabs, background)
	m.resizeTabs()
	widthBefore := bgPane.Width
	if widthBefore != 100 {
		t.Fatalf("background pane width = %d, want 100 before the toggle", widthBefore)
	}

	// Text must be empty; Mod carries both so String() → "alt+shift+s".
	updated, cmd := m.handleKey(tea.KeyPressMsg{Mod: tea.ModAlt | tea.ModShift, Code: 's'})
	got := updated.(Model)
	if !got.sidebarOpen {
		t.Fatal("sidebar_toggle did not open the sidebar")
	}
	if !got.cfg.UI.SidebarOpen || !got.configChanged {
		t.Error("the preference must reach cfg AND set configChanged, or it is lost on exit")
	}
	if cmd == nil {
		t.Error("toggling reserved layout width must return a cmd (ClearScreen + resizeAllPanes)")
	}
	if bgPane.Width != got.paneAreaWidth() {
		t.Errorf("background pane width = %d, want %d (paneAreaWidth) — the toggle shipped "+
			"pane sizes without recomputing them", bgPane.Width, got.paneAreaWidth())
	}
	if background.CanvasW != got.paneAreaWidth() {
		t.Errorf("background tab CanvasW = %d, want %d", background.CanvasW, got.paneAreaWidth())
	}

	// And back.
	updated, _ = got.handleKey(tea.KeyPressMsg{Mod: tea.ModAlt | tea.ModShift, Code: 's'})
	back := updated.(Model)
	if back.sidebarOpen {
		t.Error("second press must close the sidebar")
	}
	if bgPane.Width != widthBefore {
		t.Errorf("background pane width = %d after closing, want %d", bgPane.Width, widthBefore)
	}
}

// TestViewKeepsTheSidebarWhenTheActiveProjectHasNoTabs: View() used to build
// the whole tab-content section — the project sidebar with it — only when
// activeTabModel() was non-nil, so an empty active project rendered a blank
// screen AND took away the navigation needed to leave it.
func TestViewKeepsTheSidebarWhenTheActiveProjectHasNoTabs(t *testing.T) {
	m := Model{
		width: 120, height: 30,
		sidebarOpen:  true,
		sidebarWidth: 22,
		projects: []*ProjectModel{
			{ID: "proj-empty", Name: "emptyproj"},
			{ID: "proj-other", Name: "otherproj", tabs: []*TabModel{tabWith(&PaneModel{ID: "pane-1"})}},
		},
		activeProject: 0,
		notifications: NewNotificationCenter(30, 50),
	}
	if m.activeTabModel() != nil {
		t.Fatal("setup invariant broken: the active project must have no tabs")
	}

	out := stripANSI(m.View().Content)
	if !strings.Contains(out, "PROJECTS") {
		t.Errorf("the project sidebar is missing, so there is no way back to another "+
			"project by mouse:\n%s", out)
	}
	if !strings.Contains(out, "otherproj") {
		t.Error("the sidebar rendered without the other project's row")
	}
	if !strings.Contains(out, "emptyproj") {
		t.Error("the empty project's own row is missing from the sidebar")
	}
	// And the pane area says what happened rather than sitting blank.
	if !strings.Contains(out, "No tabs in") {
		t.Errorf("the empty pane area gives the user nothing to act on:\n%s", out)
	}
	// The frame must still fit: the sidebar reserves layout width, so a join
	// against a wrongly-sized placeholder overflows the terminal.
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > m.width {
			t.Errorf("line %d measured %d cells, wider than the %d-cell frame", i, got, m.width)
		}
	}
}

// TestSidebarToggleKeyRefusedOnANarrowTerminal: below minWidthForSidebar
// sidebarWidth() answers 0 regardless of sidebarOpen, so flipping the flag
// repaints nothing — but it still wrote cfg.UI.SidebarOpen to disk, so a
// narrow session silently decided how the next wide one comes up.
func TestSidebarToggleKeyRefusedOnANarrowTerminal(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.cfg = config.Default()
	m.initKeymap() // handleKey dispatches through the keymap; NewModel builds it
	m.width = minWidthForSidebar - 1
	if m.projectSidebarWidth() != 0 {
		t.Fatalf("setup invariant broken: projectSidebarWidth = %d at width %d, want 0",
			m.projectSidebarWidth(), m.width)
	}

	updated, cmd := m.handleKey(tea.KeyPressMsg{Mod: tea.ModAlt | tea.ModShift, Code: 's'})
	got := updated.(Model)

	if got.sidebarOpen {
		t.Error("sidebarOpen flipped on a terminal that cannot render the sidebar")
	}
	if got.cfg.UI.SidebarOpen || got.configChanged {
		t.Error("a no-op toggle wrote the sidebar preference to config")
	}
	if got.flashText == "" {
		t.Error("the refusal is silent — the key must explain why nothing happened")
	}
	if cmd == nil {
		t.Error("the flash needs its expiry cmd to repaint")
	}
}

// Each project owns its activeTab, so switchProject changes the active tab
// implicitly and inherits switchTab's obligation to tear notes down first.
// Left open, the editor paints beside the incoming project's panes still
// bound to the outgoing project's pane, still claiming notesPanelWidth().
func TestSwitchProjectExitsNotesMode(t *testing.T) {
	fake := newFakeConn()
	m := newSplitDragTestModel(t)
	m.client = fake
	m.projects = append(m.projects, &ProjectModel{ID: "proj-b", Name: "beta"})

	// Built against a temp dir rather than through toggleNotesMode, which
	// resolves config.NotesDir() to the real ~/.quil on a developer machine.
	editor, err := NewNotesEditor(t.TempDir(), "p1", "p1", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	boundTab := m.curTabs()[0]
	boundTab.ToggleFocus()
	m.notesMode = true
	m.notesEditor = editor
	m.notesEnteredFocus = boundTab.FocusMode()
	if !boundTab.FocusMode() || m.notesPanelWidth() == 0 {
		t.Fatal("fixture did not reach the state a real notes toggle produces")
	}

	m.switchProject(1)

	if m.notesMode {
		t.Error("notes mode survived a project switch — the editor is now bound to a background project's pane")
	}
	if m.notesPanelWidth() != 0 {
		t.Error("the notes panel is still claiming layout width after the switch")
	}
	if boundTab.FocusMode() {
		t.Error("exitNotesModeInPlace must revert focus mode on the OUTGOING project's tab")
	}
}

// ---------------------------------------------------------------------------
// Sidebar row budget and cell-width measurement
// ---------------------------------------------------------------------------

// lipgloss's .Height pads but never clips, so an unbounded row list grows
// the composited block past the terminal and pushes the status bar off.
func TestSidebarCapsRowsToTheAvailableHeight(t *testing.T) {
	var tabs []*TabModel
	for i := 0; i < 10; i++ {
		panes := make([]*PaneModel, 0, 4)
		for j := 0; j < 4; j++ {
			panes = append(panes, &PaneModel{ID: fmt.Sprintf("p-%d-%d", i, j)})
		}
		tab := tabWith(panes...)
		tab.ID = fmt.Sprintf("tab-%d", i)
		tabs = append(tabs, tab)
	}
	m := Model{
		projects: []*ProjectModel{
			{ID: "proj-a", Name: "alpha", tabs: tabs},
			{ID: "proj-b", Name: "beta"},
			{ID: "proj-c", Name: "gamma"},
			{ID: "proj-d", Name: "delta"},
		},
		sidebarOpen:  true,
		sidebarWidth: 22, width: 200, height: 40,
	}
	h := m.sidebarContentHeight()
	if all, _ := m.sidebarRows(22); len(all) <= h {
		t.Fatal("fixture does not overflow — the cap would not be exercised")
	}
	lines := strings.Split(m.renderSidebar(h), "\n")
	if len(lines) != h {
		t.Fatalf("renderSidebar emitted %d lines for a %d-row area", len(lines), h)
	}
	// The overflow is marked rather than silently ending, so the PROJECTS block
	// — the navigation the sidebar exists for — always survives above it. The
	// marker names a COUNT now that the body scrolls: the rows below are
	// reachable, so "there is more" is no longer the whole story.
	// TestSidebarVisibleRows_ShortStripFallsBackToTailTruncation keeps the
	// bare-" …" tail cap, which is still what a strip too short to scroll gets.
	if last := lines[len(lines)-1]; !strings.Contains(last, glyphMore) ||
		!strings.Contains(last, "below") {
		t.Errorf("last row = %q, want an overflow marker naming the rows below it", last)
	}
	if !strings.Contains(lines[1], "alpha") {
		t.Errorf("row 1 = %q, want the first project", lines[1])
	}
	// The hit test must cap identically, or it indexes rows that were never
	// painted. Screen row h-1 IS the marker row (sidebar row k = screen row
	// k); h-1 is also the last row of the strip, so this is the boundary.
	if kind, _ := m.sidebarHit(3, h-1); kind != "" {
		t.Errorf("the overflow marker row must not be actionable, got %q", kind)
	}
}

// wideGlyphSidebarModel is the fixture both wide-glyph tests use: a CJK
// project name plus a parked link (⚡, U+26A1). Both are two cells per rune
// and one rune per rune — the gap rune counting cannot see.
func wideGlyphSidebarModel(t *testing.T) Model {
	t.Helper()
	blocked := &PaneModel{ID: "pane-cjk", Name: "构建服务数据库迁移工具链"}
	blocked.blockedSince = time.Now()
	blocked.blockedReason = "编辑文件"

	// The name is one unbroken run of wide glyphs, deliberately: lipgloss
	// word-wraps, so a row whose excess is a run of SPACES is collapsed back
	// inside the budget and hides the bug. Only solid content forces the
	// wrap that shifts the rows below.
	//
	// The blocked pane must NOT be the focused one, and an idle sibling put
	// ahead of it is what keeps it unfocused (tabWith focuses panes[0]).
	// paneRow suppresses the blocked presentation for the focused pane, so
	// with one pane in the tab the CJK blockedReason never reaches the row and
	// this fixture stops exercising a wide-glyph SUFFIX at all — silently, with
	// both tests below still green on the project name alone.
	idle := &PaneModel{ID: "pane-idle", Name: "shell"}
	m := Model{
		projects: []*ProjectModel{
			{ID: "proj-a", Name: "数据库迁移服务集群控制台", Dest: "gpu01", tabs: []*TabModel{tabWith(idle, blocked)}},
			{ID: "proj-b", Name: "beta"},
		},
		sidebarOpen:  true,
		sidebarWidth: 22, width: 200, height: 40,
		links: map[string]*reconnectState{"gpu01": {parked: true}},
	}
	// Control: if lipgloss ever measured these as one cell the fixture would
	// stop discriminating and both tests below would pass vacuously.
	if m.linkGlyph("gpu01", nil) != "⚡" {
		t.Fatalf("fixture did not produce the parked glyph, got %q", m.linkGlyph("gpu01", nil))
	}
	if lipgloss.Width("⚡") != 2 || lipgloss.Width("构") != 2 {
		t.Fatalf("fixture glyphs are not wide (⚡=%d 构=%d) — this test cannot fail",
			lipgloss.Width("⚡"), lipgloss.Width("构"))
	}
	// Second control: the wide-glyph blocked reason must actually reach a row.
	// It does so only while the blocked pane is unfocused, which is a property
	// of the fixture's pane ORDER — nothing else here would notice it changing.
	var sawReason bool
	for _, r := range rowsOf(&m, 22) {
		if strings.Contains(r.text, "编辑") {
			sawReason = true
		}
	}
	if !sawReason {
		t.Fatal("no row carries the wide-glyph blocked reason — the fixture no longer " +
			"exercises a wide suffix (is the blocked pane focused?)")
	}
	return m
}

// rowsOf is the row list alone, for fixtures that only assert over the rows.
func rowsOf(m *Model, w int) []sidebarRow {
	rows, _ := m.sidebarRows(w)
	return rows
}

// No row may exceed its column budget. Asserted on sidebarRows, BEFORE
// renderSidebar's closing .Width(w) — that pass WRAPS an over-wide line onto
// a second painted line rather than truncating it, so measuring the rendered
// output would see every line within budget and miss this entirely.
func TestSidebarRowsNeverExceedTheirColumnBudget(t *testing.T) {
	m := wideGlyphSidebarModel(t)
	rows, _ := m.sidebarRows(22)
	for i, row := range rows {
		w := lipgloss.Width(row.text)
		if w > 22 {
			t.Errorf("row %d is %d cells wide against a 22-cell budget — .Width(22) will wrap it: %q",
				i, w, row.text)
		}
		// Project and pane rows are padded to exactly the budget so the
		// block joins flush against the tab content.
		if row.kind != "" && w != 22 {
			t.Errorf("row %d (%s) is %d cells, want exactly 22: %q", i, row.kind, w, row.text)
		}
	}
}

// The user-visible symptom of a wrapped row: it consumes two painted lines
// while sidebarRowAt still maps screen row y to rows[y], so every row below
// it answers for its neighbour. Click project 1, select project 0.
func TestSidebarHitAgreesWithPaintUnderWideGlyphs(t *testing.T) {
	m := wideGlyphSidebarModel(t)
	lines := strings.Split(m.renderSidebar(m.sidebarContentHeight()), "\n")

	// Row 0 PROJECTS, row 1 project 0's name, row 2 its host (proj-a is
	// remote), row 3 project 1.
	const screenY = 3
	kind, idx := m.sidebarHit(3, screenY)
	if kind != sidebarRowProject || idx != 1 {
		t.Fatalf("sidebarHit(3, %d) = (%q, %d), want (project, 1)", screenY, kind, idx)
	}
	// The host row belongs to the project above it: a click there must select
	// that project rather than falling through to the pane underneath the
	// sidebar.
	if kind, idx := m.sidebarHit(3, 2); kind != sidebarRowProject || idx != 0 {
		t.Errorf("sidebarHit(3, 2) = (%q, %d), want (project, 0) — the host row is part of its project", kind, idx)
	}
	if got := lines[screenY]; !strings.Contains(got, "beta") {
		t.Fatalf("painted sidebar row %d = %q, but the hit test calls it project 1 (beta) — "+
			"a wide-glyph row above it wrapped and shifted the paint", screenY, got)
	}
}

func TestTruncateCellsDropsStraddlingWideGlyphs(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		w    int
		want string
	}{
		{"fits", "abc", 5, "abc"},
		{"exact", "abc", 3, "abc"},
		{"ascii cut", "abcdef", 3, "abc"},
		{"zero width", "abc", 0, ""},
		{"wide glyph fits", "⚡x", 3, "⚡x"},
		{"wide glyph straddles the boundary", "a⚡", 2, "a"},
		{"cjk straddles", "a构建", 4, "a构"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateCells(tc.in, tc.w); got != tc.want {
				t.Errorf("truncateCells(%q, %d) = %q, want %q", tc.in, tc.w, got, tc.want)
			}
			if got := lipgloss.Width(padOrTrunc(tc.in, tc.w)); got != max(tc.w, 0) {
				t.Errorf("padOrTrunc(%q, %d) is %d cells, want exactly %d", tc.in, tc.w, got, tc.w)
			}
		})
	}
}

// renderSidebar sizes its own box off projectSidebarWidth(), not the raw
// configured field: the pane area clamps an oversized sidebar_width and the
// box has to clamp with it, or View()'s JoinHorizontal composites a frame
// wider than the terminal.
func TestRenderSidebarClampsOversizedConfiguredWidth(t *testing.T) {
	m := Model{
		projects:     []*ProjectModel{{ID: "proj-a", Name: "alpha"}},
		sidebarOpen:  true,
		sidebarWidth: 5000, width: 200, height: 40,
	}
	want := m.projectSidebarWidth()
	if want != 200-minTermWidth {
		t.Fatalf("projectSidebarWidth = %d, want %d", want, 200-minTermWidth)
	}
	for i, line := range strings.Split(m.renderSidebar(10), "\n") {
		if got := lipgloss.Width(line); got != want {
			t.Fatalf("row %d width = %d, want %d (the box ignored the clamp)", i, got, want)
		}
	}
}

// config can't import tui (tui already imports config), so UIConfig's
// shipped SidebarWidth default is a literal that has to be kept in sync by
// hand with defaultSidebarWidth here. This pins the two together so a future
// edit to one side shows up as a test failure instead of a silent drift.
func TestUIDefault_SidebarWidthMatchesTUIDefault(t *testing.T) {
	if got := config.Default().UI.SidebarWidth; got != defaultSidebarWidth {
		t.Fatalf("config.Default().UI.SidebarWidth = %d, want %d (defaultSidebarWidth)", got, defaultSidebarWidth)
	}
}

// TestSidebarMarksTheActiveTabAndFocusedPane: the sidebar marked the active
// PROJECT but nothing said which tab or pane you were actually in, so the
// PANES section read as a flat list with no "you are here". The marker is the
// same ▸ used for the project row, in the same column, so the three levels
// share one vocabulary.
//
// The focused-pane marker deliberately does NOT change the row's colour: that
// carries the pane's state (blocked / working / unseen), which is the more
// urgent signal — a blocked pane must stay visibly blocked while focused.
func TestSidebarMarksTheActiveTabAndFocusedPane(t *testing.T) {
	paneA, paneB := &PaneModel{ID: "pane-a"}, &PaneModel{ID: "pane-b"}
	first := tabWith(paneA)
	first.Name = "first"
	first.ActivePane = "pane-a"

	second := tabWith(paneB)
	second.Name = "second"
	second.ActivePane = "pane-b"

	m := Model{
		width: 120, height: 30, sidebarOpen: true, sidebarWidth: 22,
		notifications: NewNotificationCenter(30, 50),
		projects: []*ProjectModel{{
			ID: "proj-a", Name: "alpha",
			tabs:      []*TabModel{first, second},
			activeTab: 1, // "second" is active, so pane-b holds focus
		}},
	}

	allRows, _ := m.sidebarRows(m.sidebarWidth)
	var tabRows, paneRows []string
	for _, r := range allRows {
		switch {
		case strings.Contains(r.text, "first"), strings.Contains(r.text, "second"):
			tabRows = append(tabRows, r.text)
		case strings.Contains(r.text, "pane-a"), strings.Contains(r.text, "pane-b"):
			paneRows = append(paneRows, r.text)
		}
	}
	if len(tabRows) != 2 || len(paneRows) != 2 {
		t.Fatalf("expected 2 tab rows and 2 pane rows, got %d and %d", len(tabRows), len(paneRows))
	}

	// Only the ACTIVE tab is marked — a background tab also has an ActivePane,
	// and marking every one of them would claim "you are here" twice.
	if strings.Contains(tabRows[0], "▸") {
		t.Errorf("the inactive tab row is marked: %q", tabRows[0])
	}
	if !strings.Contains(tabRows[1], "▸") {
		t.Errorf("the active tab row is not marked: %q", tabRows[1])
	}
	if strings.Contains(paneRows[0], "▸") {
		t.Errorf("a pane in a non-active tab is marked as focused: %q", paneRows[0])
	}
	if !strings.Contains(paneRows[1], "▸") {
		t.Errorf("the focused pane is not marked: %q", paneRows[1])
	}

	// Every row still fills exactly the sidebar's column budget — the marker
	// replaces padding rather than widening the row, which is what keeps the
	// hit test's y->row mapping aligned with what is painted.
	for _, r := range allRows {
		if r.text == "" {
			continue
		}
		if got := lipgloss.Width(r.text); got > m.sidebarWidth {
			t.Errorf("row %q measures %d cells, want <= %d", r.text, got, m.sidebarWidth)
		}
	}
}

// TestProjectBadgeCountsFinishedPanes: a turn completing in a BACKGROUND
// project was invisible at the project level — counts() aggregated working and
// blocked but not unseen, so the only place you look when you are elsewhere
// never told you the work was ready. That is most of the reason to group panes
// by project at all.
//
// Also pins the ordering: a pane parked for input has ALSO finished its turn,
// and "needs you" outranks "is ready", so it must count once, as blocked.
func TestProjectBadgeCountsFinishedPanes(t *testing.T) {
	done := &PaneModel{ID: "pane-done"}
	done.unseen = true
	busy := &PaneModel{ID: "pane-busy"}
	busy.working = true
	parked := &PaneModel{ID: "pane-parked"}
	parked.unseen = true // a parked pane has finished its turn too
	parked.blockedSince = time.Now()

	p := &ProjectModel{tabs: []*TabModel{tabWith(done, busy, parked)}}
	c := p.counts()
	if c.working != 1 || c.blocked != 1 || c.done != 1 {
		t.Fatalf("counts() = %+v, want working 1, blocked 1, done 1 — "+
			"a parked pane must count once, as blocked", c)
	}

	// Built from the glyph constants rather than literals: the badge's job is
	// to carry a COUNT per state, and pinning the codepoints here would make a
	// deliberate glyph change (see TestSidebarGlyphs_OneCellAndNotEmojiCapable
	// for why one was needed) look like a counting regression.
	row := projectRow("alpha", c, "", false, 30, nil)
	for _, want := range []string{glyphBlocked + "1", glyphWorking + "1", glyphDone + "1"} {
		if !strings.Contains(row, want) {
			t.Errorf("project row %q is missing the %s badge", row, want)
		}
	}
}

// TestProjectCounts_PinnedIsIndependentOfTheStateRanking is the property that
// separates the pin from the other three counts. Those are one ORDERED
// classification — a pane parked for input has also finished its turn, and
// "needs you" outranks "is ready", so it contributes to exactly one. The pin
// is a second axis: a pinned pane is usually ALSO working or blocked, and
// folding it into the ranking would make a mark that exists to be un-loseable
// vanish the moment the pane got busy, which is when the user most needs to
// find it again.
func TestProjectCounts_PinnedIsIndependentOfTheStateRanking(t *testing.T) {
	t.Parallel()
	pinnedBusy := &PaneModel{ID: "pane-busy"}
	pinnedBusy.working = true
	pinnedBusy.pinnedAttention = true

	pinnedBlocked := &PaneModel{ID: "pane-parked"}
	pinnedBlocked.blockedSince = time.Now()
	pinnedBlocked.pinnedAttention = true

	pinnedIdle := &PaneModel{ID: "pane-idle"}
	pinnedIdle.pinnedAttention = true

	plain := &PaneModel{ID: "pane-plain"}

	p := &ProjectModel{tabs: []*TabModel{tabWith(pinnedBusy, pinnedBlocked, pinnedIdle, plain)}}
	c := p.counts()
	if c.pinned != 3 {
		t.Errorf("counts().pinned = %d, want 3 — a pin must count even when a "+
			"live state outranks it in the switch", c.pinned)
	}
	// The other three are unchanged by the pins: the ranking still puts each
	// pane in exactly one bucket, and an idle pinned pane is in none of them.
	if c.working != 1 || c.blocked != 1 || c.done != 0 {
		t.Errorf("counts() = %+v, want working 1, blocked 1, done 0 — pinning "+
			"must not move a pane between the ranked buckets", c)
	}
}

// TestProjectRow_ShowsThePinnedCount: the project row is the one place that
// lists every project at once, and it counted only what the AGENTS were doing.
// A pane the user marked by hand was invisible there — so the row the user
// scans to decide where to go next could not answer "where did I leave that
// mark", which is the whole job of a mark that never auto-clears.
func TestProjectRow_ShowsThePinnedCount(t *testing.T) {
	t.Parallel()
	row := projectRow("alpha", paneStateCounts{pinned: 2}, "", false, 30, nil)
	if want := glyphPinned + "2"; !strings.Contains(row, want) {
		t.Errorf("project row %q is missing the %s badge", row, want)
	}
	if sgr := styleSGR(t, sidebarPinnedStyle); sgr != "" && !strings.Contains(row, sgr+" "+glyphPinned+"2") {
		t.Errorf("project row %q does not paint the pin badge in the pin colour", row)
	}
	// Absent when there is nothing to report — the badge is a list of what is
	// true, not a fixed set of columns with zeroes in them.
	if plain := projectRow("alpha", paneStateCounts{}, "", false, 30, nil); strings.Contains(plain, glyphPinned) {
		t.Errorf("project row %q shows a pin badge with no pinned panes", plain)
	}
}

// styleSGR returns the ANSI prefix a style emits before its content, or "" when
// the active colour profile strips colour entirely. The colour assertions below
// are meaningless in that case, so they skip rather than pass silently — a
// vacuous green test is the failure mode a colour assertion is most prone to.
func styleSGR(t *testing.T, s lipgloss.Style) string {
	t.Helper()
	const probe = "x"
	out := s.Render(probe)
	i := strings.Index(out, probe)
	if i <= 0 {
		return ""
	}
	return out[:i]
}

// TestRenderStyledSegments drives the segment renderer DIRECTLY rather than
// only through projectRow, which is the one caller today.
//
// projectRow pre-sizes its head so the badge segments almost always land exactly
// on budget, so the helper's interesting branches — a segment cut mid-text, a
// segment whose first cluster does not fit at all, an empty segment, a
// degenerate width — are barely reachable through it. The helper states an
// unconditional contract ("returns exactly w cells", "segments are spent in
// order"), and a contract only pinned for the shapes one caller happens to emit
// is the next caller's bug.
func TestRenderStyledSegments(t *testing.T) {
	t.Parallel()
	a, b := sidebarBlockedStyle, sidebarWorkingStyle
	tests := []struct {
		name      string
		segs      []styledSegment
		w         int
		wantPlain string // the visible text, ANSI stripped
	}{
		{"nil segments pad to the full width", nil, 4, "    "},
		{"empty slice pads to the full width", []styledSegment{}, 3, "   "},
		{"zero width renders nothing", []styledSegment{{"abc", a}}, 0, ""},
		{"negative width renders nothing", []styledSegment{{"abc", a}}, -3, ""},
		{"segments concatenate in order", []styledSegment{{"ab", a}, {"cd", b}}, 4, "abcd"},
		{"a short row is padded, not stretched", []styledSegment{{"ab", a}}, 5, "ab   "},
		{
			// An empty segment contributes nothing and must NOT be read as a
			// segment that failed to fit — the two share a t == "" test if
			// anyone collapses the branches, and collapsing them ends the row
			// early on a caller that emits a conditional segment as "".
			"an empty segment does not end the row",
			[]styledSegment{{"", a}, {"ab", b}}, 4, "ab  ",
		},
		{"a later segment is truncated into what is left", []styledSegment{{"ab", a}, {"cdef", b}}, 4, "abcd"},
		{"an earlier segment is never sacrificed for a later one", []styledSegment{{"abcd", a}, {"ef", b}}, 4, "abcd"},
		{
			// The straddle: a 2-cell glyph with 1 cell left is dropped whole
			// rather than half-emitted, and the pad backfills the odd cell.
			"a wide glyph straddling the boundary is dropped and backfilled",
			[]styledSegment{{"abc", a}, {"⚡", b}}, 4, "abc ",
		},
		{
			// The ordering promise's second half. With one cell left, "⚡"
			// cannot start — and the row must END there rather than letting the
			// next segment render in its place, which would silently put one
			// state's glyph in another state's position.
			"a segment that cannot start ends the row",
			[]styledSegment{{"abc", a}, {"⚡", b}, {"!", a}}, 4, "abc ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := renderStyledSegments(tt.segs, tt.w)
			// Segment TEXTS, never the segments themselves: lipgloss.Style is
			// 648 bytes of mostly-zero fields, so %v over a []styledSegment
			// buries the one thing a failure needs to show under three screens
			// of struct dump.
			in := make([]string, len(tt.segs))
			for i := range tt.segs {
				in[i] = tt.segs[i].text
			}
			if plain := stripSGR(got); plain != tt.wantPlain {
				t.Errorf("renderStyledSegments(%q, %d) plain text = %q, want %q",
					in, tt.w, plain, tt.wantPlain)
			}
			// The invariant every sidebar row depends on: renderSidebar's
			// closing .Width(w) WRAPS an over-wide line rather than cutting it,
			// which shifts every row below while sidebarRowAt still maps screen
			// row y to rows[y].
			want := tt.w
			if want < 0 {
				want = 0
			}
			if n := lipgloss.Width(got); n != want {
				t.Errorf("renderStyledSegments(%q, %d) measures %d cells, want exactly %d",
					in, tt.w, n, want)
			}
		})
	}
}

// stripSGR removes ANSI SGR sequences so a test can assert on the text a
// segmented row actually shows. Deliberately minimal — the rows under test are
// built from foreground-only styles, so \x1b[...m is the only form that occurs.
func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// TestRenderStyledSegments_SegmentsMustStartOnAClusterBoundary documents the
// helper's one precondition that a caller can violate silently, by showing what
// it costs.
//
// A rune can change the width of the one before it — U+FE0F measures 0 alone
// and makes the pair before it measure 2 — so a segment STARTING with a
// combining mark joins the previous segment's last cluster and the independent
// per-segment sum understates the row. The non-obvious part, and why no
// measurement strategy inside the helper can fix it: whether the two runes
// really join depends on the STYLES, not on the text. An SGR sequence emitted
// between them separates them and the row measures as summed; two property-free
// styles emit nothing between them and it does not.
//
// Asserted as the divergence rather than as a bug to be fixed, so that anyone
// who later "corrects" the measurement finds the reason it is a caller
// requirement instead. projectRow satisfies it by construction: every badge
// segment begins with a space.
func TestRenderStyledSegments_SegmentsMustStartOnAClusterBoundary(t *testing.T) {
	t.Parallel()
	const w = 3
	split := []styledSegment{{"⚠", sidebarDimStyle}, {"️", sidebarBlockedStyle}}
	if lipgloss.Width("⚠")+lipgloss.Width("️") == lipgloss.Width("⚠️") {
		t.Skip("lipgloss no longer measures the pair wider than its parts — the precondition is moot")
	}
	// Coloured styles put an SGR between the runes, which keeps them apart and
	// keeps the sum honest. This is the case every caller in this file is in.
	if n := lipgloss.Width(renderStyledSegments(split, w)); n != w {
		t.Errorf("with SGR between the segments the row measures %d cells, want %d", n, w)
	}
	// Property-free styles emit nothing between them, the runes rejoin, and the
	// row overruns. Not a defect to fix here — it is the precondition being
	// violated, and renderSidebar's .Width(w) would wrap the excess.
	plain := []styledSegment{{"⚠", lipgloss.NewStyle()}, {"️", lipgloss.NewStyle()}}
	if n := lipgloss.Width(renderStyledSegments(plain, w)); n == w {
		t.Errorf("a segment starting mid-cluster measured exactly %d cells — this test no longer "+
			"demonstrates why the cluster-boundary precondition exists", w)
	}
}

// TestLinkGlyph_EveryStateHasItsOwnColour closes the gap a switch statement
// cannot express: linkGlyphStyle's fallback has to be SOME style, so a third
// link state added to linkGlyph without a matching entry renders in the
// fallback's colour and reads as a state it is not. Driving linkGlyph over
// every reconnectState combination is what makes this fail on the addition
// rather than on someone remembering to extend a hand-written list.
func TestLinkGlyph_EveryStateHasItsOwnColour(t *testing.T) {
	t.Parallel()
	m := &Model{links: map[string]*reconnectState{}}
	seen := map[string]bool{}
	for _, parked := range []bool{true, false} {
		for _, active := range []bool{true, false} {
			m.links["d"] = &reconnectState{parked: parked, active: active}
			if g := m.linkGlyph("d", nil); g != "" {
				seen[g] = true
			}
		}
	}
	if len(seen) == 0 {
		t.Fatal("linkGlyph produced no glyphs — this test cannot discriminate")
	}
	for glyph := range seen {
		if _, ok := linkGlyphStyles[glyph]; !ok {
			t.Errorf("linkGlyph can return %q but linkGlyphStyles has no entry for it — "+
				"it would render in the idle fallback colour", glyph)
		}
	}
	// Distinct colours, or the row says the same thing about two different
	// states. Skips rather than fails when colour is stripped entirely.
	if styleSGR(t, sidebarLinkParkedStyle) == "" {
		return
	}
	used := map[string]string{}
	for glyph := range seen {
		sgr := styleSGR(t, linkGlyphStyles[glyph])
		if prev, clash := used[sgr]; clash {
			t.Errorf("%q and %q are painted the same colour", prev, glyph)
		}
		used[sgr] = glyph
	}
	// The fallback must not be mistakable for a real state.
	if _, clash := used[styleSGR(t, sidebarDimStyle)]; clash {
		t.Error("a link state is painted in the same colour as linkGlyphStyle's idle fallback")
	}
}

// TestProjectRow_BadgesCarryTheirStateColour: the project badge is a ROLL-UP of
// the pane rows beneath it, and it read as one — same glyphs, same order — while
// being painted in a single flat colour. The whole line went through one
// style.Render, so ▲/◐/✓ inherited the row's grey and the summary said "three
// numbers" where the pane section says "one needs you, one is running, one is
// ready". Colour is most of what makes a badge scannable at 22 columns.
//
// Asserted against the SAME style values paneRow uses, not against literal SGR
// codes: the requirement is that the two sections agree, so a deliberate palette
// change must move both together rather than failing here.
func TestProjectRow_BadgesCarryTheirStateColour(t *testing.T) {
	t.Parallel()
	if styleSGR(t, sidebarBlockedStyle) == "" {
		t.Skip("lipgloss renders without colour here — these assertions cannot discriminate")
	}
	tests := []struct {
		name  string
		style lipgloss.Style
		badge string
	}{
		{"blocked", sidebarBlockedStyle, glyphBlocked + "1"},
		{"working", sidebarWorkingStyle, glyphWorking + "2"},
		{"done", sidebarUnseenStyle, glyphDone + "3"},
	}
	// The three styles must be mutually distinct, or the assertions below keep
	// passing (the glyph and the count still differ) while no longer testing
	// colour at all. The link test has the same guard for the same reason.
	seen := map[string]string{}
	for _, tt := range tests {
		sgr := styleSGR(t, tt.style)
		if prev, clash := seen[sgr]; clash {
			t.Fatalf("the %s and %s badge styles are identical — this test cannot discriminate",
				prev, tt.name)
		}
		seen[sgr] = tt.name
	}
	// active=true as well: the active row's own style is the BOLD one, and a
	// badge that inherits it is exactly the bug — being the active project does
	// not change what its panes are doing.
	for _, active := range []bool{false, true} {
		row := projectRow("alpha", paneStateCounts{working: 2, blocked: 1, done: 3}, "", active, 30, nil)
		for _, tt := range tests {
			want := styleSGR(t, tt.style) + " " + tt.badge
			if !strings.Contains(row, want) {
				t.Errorf("active=%v: projectRow = %q, want the %s badge painted with its own style (%q)",
					active, row, tt.name, want)
			}
		}
	}
}

// TestProjectRow_LinkGlyphCarriesItsOwnColour: the link glyph reports the
// DESTINATION's health rather than any pane's, so it is the one badge segment
// with no counterpart in the pane rows — and it was the easiest to lose in the
// flat grey, being a lone symbol with no count beside it.
//
// ⚡ (parked: the ladder gave up, nothing will happen until the user acts) takes
// the red spawnErrorStyle already uses for a dead pane; ⟳ (retrying: the machine
// is working, nothing is waiting on the user) takes the 208 orange the project
// form's busy line uses. Painting both amber would say "needs you" about the one
// state that does not.
func TestProjectRow_LinkGlyphCarriesItsOwnColour(t *testing.T) {
	t.Parallel()
	if styleSGR(t, sidebarLinkParkedStyle) == "" {
		t.Skip("lipgloss renders without colour here — these assertions cannot discriminate")
	}
	if styleSGR(t, sidebarLinkParkedStyle) == styleSGR(t, sidebarLinkRetryStyle) {
		t.Fatal("the parked and retrying link styles are identical — this test cannot discriminate")
	}
	for _, tt := range []struct {
		glyph string
		style lipgloss.Style
	}{
		{glyphLinkParked, sidebarLinkParkedStyle},
		{glyphLinkRetry, sidebarLinkRetryStyle},
	} {
		row := projectRow("alpha", paneStateCounts{}, tt.glyph, false, 30, nil)
		want := styleSGR(t, tt.style) + " " + tt.glyph
		if !strings.Contains(row, want) {
			t.Errorf("projectRow(link=%q) = %q, want the glyph painted with its own style (%q)",
				tt.glyph, row, want)
		}
	}
}

// TestProjectRow_NameKeepsTheRowStyle guards the other half of the split: the
// badge segments must not bleed their colour back over the name, and the name
// must not lose the active row's emphasis to them. Both are the failure mode of
// concatenating styled runs — an SGR left open, or a reset that closes the run
// it was supposed to end.
func TestProjectRow_NameKeepsTheRowStyle(t *testing.T) {
	t.Parallel()
	if styleSGR(t, sidebarProjectStyle) == "" {
		t.Skip("lipgloss renders without colour here — these assertions cannot discriminate")
	}
	for _, tt := range []struct {
		name  string
		style lipgloss.Style
		activ bool
	}{
		{"inactive", sidebarProjectStyle, false},
		{"active", sidebarActiveStyle, true},
	} {
		row := projectRow("alpha", paneStateCounts{working: 1, blocked: 1, done: 1}, glyphLinkParked, tt.activ, 30, nil)
		if want := styleSGR(t, tt.style); !strings.Contains(row, want) {
			t.Errorf("%s: projectRow = %q, want the name painted with the row style (%q)",
				tt.name, row, want)
		}
		// The badge is the LAST thing on the row, so an unterminated segment
		// leaks past the row into whatever the frame joins beside it.
		if !strings.HasSuffix(row, "\x1b[0m") && !strings.HasSuffix(row, "\x1b[m") {
			t.Errorf("%s: projectRow = %q, want it to end with an SGR reset", tt.name, row)
		}
	}
}

// TestPaneRowKeepsTheNameWhenTheReasonIsLong: the blocked reason is secondary
// detail; the label is what says WHICH pane. Subtracting the suffix from the
// budget first inverted that — at the default 22-column width a reason like
// "AskUserQuestion" left two cells for the name, so the row read
// "⚠ cl AskUserQuestion" and identified nothing.
func TestPaneRowKeepsTheNameWhenTheReasonIsLong(t *testing.T) {
	pane := &PaneModel{ID: "pane-b16e3850"}
	pane.blockedSince = time.Now()
	pane.blockedReason = "AskUserQuestion"

	row := paneRow(pane, false, defaultSidebarWidth)

	if !strings.Contains(row, "pane-b16") {
		t.Errorf("row %q dropped the pane name; the reason crowded it out", row)
	}
	if got := lipgloss.Width(row); got != defaultSidebarWidth {
		t.Errorf("row measures %d cells, want exactly %d", got, defaultSidebarWidth)
	}
	// A short reason still fits alongside a short name — the floor must not
	// truncate a suffix that had room.
	short := &PaneModel{ID: "p1"}
	short.blockedSince = time.Now()
	short.blockedReason = "Bash"
	if row := paneRow(short, false, defaultSidebarWidth); !strings.Contains(row, "Bash") {
		t.Errorf("row %q dropped a reason that had room", row)
	}
}

// TestPaneRow_RendersPinnedAttention pins item 6.3. pinnedAttention already
// drove the pane border and the tab label; the sidebar row — the one place
// that lists every pane at once — never showed it.
func TestPaneRow_RendersPinnedAttention(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		setup   func(p *PaneModel)
		wantSub string
	}{
		{"pinned alone", func(p *PaneModel) { p.pinnedAttention = true }, glyphPinned},
		{"pinned and blocked keeps the blocked glyph", func(p *PaneModel) {
			p.pinnedAttention = true
			p.blockedSince = time.Now()
		}, glyphBlocked},
		{"pinned and blocked still shows the pin", func(p *PaneModel) {
			p.pinnedAttention = true
			p.blockedSince = time.Now()
		}, glyphPinned},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane := &PaneModel{ID: "p1", Name: "agent"}
			tt.setup(pane)
			got := paneRow(pane, false, 30)
			if !strings.Contains(got, tt.wantSub) {
				t.Errorf("paneRow = %q, want it to contain %q", got, tt.wantSub)
			}
		})
	}
}

// TestPaneRow_BlockedFocusedSuppressesTheGlyph pins the render half of the
// revised item 6.2. The blocked STATE is kept when the user focuses the pane
// (ackFocusedPane no longer clears it — see TestAckFocusedPane_KeepsTheBlockedMark
// for why a spinner tick must not count as an answer); the PRESENTATION is what
// gives way, so "you are looking straight at the prompt" costs a glyph rather
// than a fact. The row must then read exactly as it would with no blocked mark
// at all, which is the fall-through this table walks: working still wins, then
// the pin, then unseen, then idle.
//
// Unfocused is the control row of every case — that is the state the whole
// feature exists to show, and a suppression that leaked into it would be the
// original defect (▲ never observable) in a new place.
func TestPaneRow_BlockedFocusedSuppressesTheGlyph(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		setup         func(p *PaneModel)
		wantFocused   string // the glyph the row shows while focused
		unwantFocused string // and the one it must NOT show
	}{
		{"blocked alone falls through to idle",
			func(p *PaneModel) {}, glyphIdle, glyphBlocked},
		{"blocked and working falls through to working",
			func(p *PaneModel) { p.working = true }, glyphWorking, glyphBlocked},
		{"blocked and pinned falls through to the pin",
			func(p *PaneModel) { p.pinnedAttention = true }, glyphPinned, glyphBlocked},
		{"blocked and unseen falls through to done",
			func(p *PaneModel) { p.unseen = true }, glyphDone, glyphBlocked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane := &PaneModel{ID: "p1", Name: "agent"}
			pane.blockedSince = time.Now()
			pane.blockedReason = "Bash"
			tt.setup(pane)

			focused := paneRow(pane, true, 30)
			if !strings.Contains(focused, tt.wantFocused) {
				t.Errorf("focused row = %q, want the %q glyph", focused, tt.wantFocused)
			}
			if strings.Contains(focused, tt.unwantFocused) {
				t.Errorf("focused row = %q, must not show %q", focused, tt.unwantFocused)
			}
			// The reason is the blocked presentation's other half; leaving it
			// behind would name the tool on a row carrying no blocked glyph.
			if strings.Contains(focused, "Bash") {
				t.Errorf("focused row = %q, must not carry the blocked reason", focused)
			}
			// Nothing about the suppression may change the row's width — it is
			// the one property every helper in this file has to agree on.
			if w := lipgloss.Width(focused); w != 30 {
				t.Errorf("focused row measures %d cells, want exactly 30", w)
			}

			unfocused := paneRow(pane, false, 30)
			if !strings.Contains(unfocused, glyphBlocked) {
				t.Errorf("unfocused row = %q, want the %q glyph", unfocused, glyphBlocked)
			}
			if !strings.Contains(unfocused, "Bash") {
				t.Errorf("unfocused row = %q, want the blocked reason", unfocused)
			}
		})
	}
}

// TestSidebarRows_SuppressesTheBlockedGlyphOnlyForTheFocusedPane drives the
// suppression through its real call site rather than paneRow's parameter. The
// `focused` argument is computed there as `onTab && pane.ID == tab.ActivePane`,
// which is exactly ackFocusedPane's own scope (the focused pane of the ACTIVE
// tab of the active project) — the two must not come apart, or a pane would
// either keep a mark nothing renders or lose one nothing cleared.
func TestSidebarRows_SuppressesTheBlockedGlyphOnlyForTheFocusedPane(t *testing.T) {
	t.Parallel()
	// Tab 0 is active: pane-0-0 is focused, pane-0-1 is its split sibling.
	// Tab 1 is a background tab holding a third blocked pane.
	m := newTestModelWithTabs(t, 2, 2)
	m.sidebarOpen, m.sidebarWidth = true, defaultSidebarWidth
	m.width, m.height = 120, 40
	for _, tab := range m.curTabs() {
		for _, p := range tab.Leaves() {
			p.blockedSince = time.Now()
		}
	}
	focusedID := m.curTabs()[0].ActivePane
	if focusedID == "" {
		t.Fatal("fixture must focus a pane on the active tab")
	}

	rows, _ := m.sidebarRows(defaultSidebarWidth)
	var seen int
	for _, r := range rows {
		if r.kind != sidebarRowPane {
			continue
		}
		seen++
		blocked := strings.Contains(r.text, glyphBlocked)
		if want := r.paneID != focusedID; blocked != want {
			t.Errorf("pane %q (focused=%v): row shows %s = %v, want %v — %q",
				r.paneID, r.paneID == focusedID, glyphBlocked, blocked, want, r.text)
		}
	}
	if seen != 4 {
		t.Fatalf("walked %d pane rows, want 4 — the fixture no longer covers both tabs", seen)
	}
	// The suppression is presentation only: every level above the row still
	// counts all four, including the focused one.
	if !m.tabBlocked(0) || !m.tabBlocked(1) {
		t.Error("both tabs must still read as blocked")
	}
	if blocked := m.projects[0].counts().blocked; blocked != 4 {
		t.Errorf("project badge counts %d blocked, want 4", blocked)
	}
}

// TestSidebarTabHeading_OrdinalAndColor pins item 2. The heading and the idle
// pane rows beneath it both painted with sidebarDimStyle (color 243), so the
// grouping the PANES section exists to show was invisible.
func TestSidebarTabHeading_OrdinalAndColor(t *testing.T) {
	t.Parallel()
	got := sidebarTabHeading("build", 1, false, "", 22)
	if !strings.Contains(got, "2:build") {
		t.Errorf("heading = %q, want the 1-based ordinal %q", got, "2:build")
	}
	if strings.Contains(got, "243") {
		t.Errorf("inactive heading still uses the dim colour shared with idle pane rows: %q", got)
	}
}

// TestSidebarTabHeading_ElidesNameKeepsOrdinal pins that the ordinal survives a
// narrow strip — it is the part that maps the row to Alt+1..9, so truncating it
// away would cost the row its only navigational value.
func TestSidebarTabHeading_ElidesNameKeepsOrdinal(t *testing.T) {
	t.Parallel()
	got := sidebarTabHeading("a-very-long-tab-name-indeed", 0, false, "", 14)
	if !strings.Contains(got, "1:") {
		t.Errorf("heading = %q, want it to keep the %q prefix", got, "1:")
	}
	if w := lipgloss.Width(got); w > 14 {
		t.Errorf("heading width = %d, want <= 14", w)
	}
}

// TestSidebarDoesNotRemodeAWideCanvasPane is the 2026-08-02 report: opening
// the sidebar on a 185-column terminal moved an even two-pane split from
// 92/93 to 81/82, straddling min_native_cols (80). One of two identical
// siblings flipped to a 161-column canvas cropped into 79 columns while the
// other stayed native at 80 — a 2x difference produced by one column of rect.
//
// The sidebar is chrome. It changes how much of a pane you can see; it must
// not change how the pane decides to render. Asserted on the WIRE, because
// the PTY size is what the child actually lays out against, and against a
// genuinely narrow terminal too so the fix cannot have simply disabled the
// threshold.
func TestSidebarDoesNotRemodeAWideCanvasPane(t *testing.T) {
	build := func(t *testing.T, width int, open bool) (*Model, *TabModel, *fakeConn) {
		t.Helper()
		a := NewPaneModel("a", testRingBufSize)
		t.Cleanup(a.Dispose)
		b := NewPaneModel("b", testRingBufSize)
		t.Cleanup(b.Dispose)
		for _, p := range []*PaneModel{a, b} {
			p.Type = "claude-code"
			p.WideCanvas = true
		}
		tab := &TabModel{
			ID: "t1",
			Root: &LayoutNode{
				Split: SplitHorizontal, Ratio: 0.5,
				Left:  &LayoutNode{Pane: a},
				Right: &LayoutNode{Pane: b},
			},
			ActivePane: "a",
		}
		conn := newFakeConn()
		m := &Model{
			client: conn, width: width, height: 54,
			sidebarOpen: open, sidebarWidth: defaultSidebarWidth,
			projects: []*ProjectModel{{ID: "p1", Name: "Default", tabs: []*TabModel{tab}}},
		}
		m.resizeTabs()
		if cmd := m.resizeAllPanes(); cmd != nil {
			cmd()
		}
		return m, tab, conn
	}

	wireCols := func(t *testing.T, conn *fakeConn, paneID string) int {
		t.Helper()
		for _, msg := range conn.sent {
			if msg.Type != ipc.MsgResizePane {
				continue
			}
			var p ipc.ResizePanePayload
			if err := msg.DecodePayload(&p); err != nil {
				t.Fatalf("decode resize: %v", err)
			}
			if p.PaneID == paneID {
				return int(p.Cols)
			}
		}
		t.Fatalf("no resize sent for pane %s", paneID)
		return 0
	}

	// Sidebar open at the reported geometry: both panes native, each at its
	// own rect, and the PTY the daemon is told matches the VT.
	_, tab, conn := build(t, 185, true)
	for _, p := range tab.Leaves() {
		if p.previewMode() {
			t.Errorf("pane %s flipped to canvas because the sidebar narrowed it (rect %d, native %d)",
				p.ID, p.Width, p.NativeW)
		}
		if got := wireCols(t, conn, p.ID); got != p.vt.Width() {
			t.Errorf("pane %s: wire %d cols, VT %d — PTY and emulator disagree", p.ID, got, p.vt.Width())
		}
	}
	if a, b := tab.Leaves()[0], tab.Leaves()[1]; a.vt.Width()*2 < b.vt.Width() || b.vt.Width()*2 < a.vt.Width() {
		t.Errorf("siblings of an even split got %d and %d columns", a.vt.Width(), b.vt.Width())
	}

	// Toggling the sidebar must not change the MODE either way.
	_, closedTab, _ := build(t, 185, false)
	for _, p := range closedTab.Leaves() {
		if p.previewMode() {
			t.Errorf("pane %s in preview with the sidebar closed — setup no longer models the report", p.ID)
		}
	}

	// A terminal genuinely too narrow still gets the canvas: the threshold is
	// chrome-blind now, not disabled.
	_, narrowTab, _ := build(t, 120, true)
	for _, p := range narrowTab.Leaves() {
		if !p.previewMode() {
			t.Errorf("pane %s native at a 120-col terminal (rect %d, native %d), want canvas",
				p.ID, p.Width, p.NativeW)
		}
	}
}

// ---------------------------------------------------------------------------
// PANES-section scrolling
// ---------------------------------------------------------------------------

// newTestModelManyPanes builds a Model whose sidebar overflows any reasonable
// strip: `projects` projects, with `panes` panes on the active one, all in a
// single tab. The sidebar fields are set because the scroll assertions resolve
// their own width and height through projectSidebarWidth() /
// sidebarContentHeight(), which answer 0 on a zero-valued Model.
func newTestModelManyPanes(t *testing.T, projects, panes int) Model {
	t.Helper()
	ps := make([]*ProjectModel, projects)
	for i := range ps {
		ps[i] = &ProjectModel{ID: fmt.Sprintf("proj-%d", i), Name: fmt.Sprintf("project-%d", i)}
	}
	leaves := make([]*PaneModel, panes)
	for j := range leaves {
		leaves[j] = &PaneModel{ID: fmt.Sprintf("pane-%d", j)}
	}
	tab := tabWith(leaves...)
	tab.Name = "work"
	ps[0].tabs = []*TabModel{tab}
	return Model{
		projects:      ps,
		activeProject: 0,
		sidebarOpen:   true,
		sidebarWidth:  defaultSidebarWidth,
		width:         120,
		height:        40,
		// Update's MouseWheelMsg branch checks the notification sidebar
		// overlay before the project sidebar swallow — sidebarOverlayWidth
		// dereferences this unconditionally, so routing a wheel event through
		// Update() (rather than calling sidebarVisibleRows directly, as every
		// prior fixture use did) panics on a nil *NotificationCenter.
		notifications: NewNotificationCenter(30, 50),
	}
}

// TestSidebarVisibleRows_PanesScrollProjectsPinned pins item 5. Paint and hit
// test both call sidebarVisibleRows with the same height, deliberately — so the
// offset must live INSIDE it. A cap or an offset applied at the render site is
// the row-drift bug ("click project 3, select project 2") in another form.
func TestSidebarVisibleRows_PanesScrollProjectsPinned(t *testing.T) {
	t.Parallel()
	m := newTestModelManyPanes(t, 3, 8) // 3 projects, 8 panes on the active one
	w, height := 22, 12

	all, panesStart := m.sidebarRows(w)
	if len(all) <= height {
		t.Fatal("fixture must overflow the strip")
	}

	m.sidebarScroll = 0
	first := m.sidebarVisibleRows(w, height)
	if len(first) != height {
		t.Errorf("visible rows = %d, want exactly %d", len(first), height)
	}
	for i := 0; i < panesStart; i++ {
		if first[i].text != all[i].text {
			t.Errorf("row %d: PROJECTS block must be pinned, got %q want %q",
				i, first[i].text, all[i].text)
		}
	}

	m.sidebarScroll = 2
	scrolled := m.sidebarVisibleRows(w, height)
	for i := 0; i < panesStart; i++ {
		if scrolled[i].text != all[i].text {
			t.Errorf("row %d changed while scrolled — PROJECTS is not pinned", i)
		}
	}
	if scrolled[panesStart].text == first[panesStart].text {
		t.Error("the PANES body did not move when sidebarScroll changed")
	}
}

// TestSidebarVisibleRows_HitTestMatchesPaint is the assertion that matters:
// window the rows, then resolve a click at a screen row and check it lands on
// the pane actually painted there. Testing either alone cannot catch drift.
func TestSidebarVisibleRows_HitTestMatchesPaint(t *testing.T) {
	t.Parallel()
	m := newTestModelManyPanes(t, 3, 8)
	m.width, m.height = 100, 13 // sidebarContentHeight() == 12
	m.sidebarScroll = 3

	w := m.projectSidebarWidth()
	rows := m.sidebarVisibleRows(w, m.sidebarContentHeight())
	for y, row := range rows {
		if row.kind != sidebarRowPane {
			continue
		}
		gotRow, ok := m.sidebarRowAt(1, y)
		if !ok || gotRow.paneID != row.paneID {
			t.Fatalf("screen row %d paints pane %q but hit-tests to %q",
				y, row.paneID, gotRow.paneID)
		}
	}
}

// TestSidebarVisibleRows_ShortStripFallsBackToTailTruncation pins the
// degenerate case: when the pinned PROJECTS block alone would leave fewer than
// minPaneRows for the body, the whole strip reverts to the pre-scroll tail cap
// rather than hiding the PANES section outright.
func TestSidebarVisibleRows_ShortStripFallsBackToTailTruncation(t *testing.T) {
	t.Parallel()
	m := newTestModelManyPanes(t, 8, 4)
	rows := m.sidebarVisibleRows(22, 6)
	if len(rows) != 6 {
		t.Fatalf("visible rows = %d, want 6", len(rows))
	}
	if !strings.Contains(rows[5].text, "…") {
		t.Errorf("last row = %q, want the overflow marker", rows[5].text)
	}
}

// TestSidebarScrollClamp pins the two helpers the wheel handler drives the
// offset with. The window gives up a row to the "N above" marker the moment it
// scrolls at all, so the last page holds bodyH-1 rows and the largest useful
// offset is bodyLen-(bodyH-1). One off by one there and the last page is drawn
// short — every row after it shifts, and the hit test shifts with it.
func TestSidebarScrollClamp(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name             string
		bodyLen, bodyH   int
		wantMax          int
		off, wantClamped int
	}{
		{"body fits", 5, 8, 0, 3, 0},
		{"body exactly fills the window", 8, 8, 0, 1, 0},
		{"one row over", 9, 8, 2, 1, 1},
		{"far past the end", 20, 5, 16, 100, 16},
		{"negative offset", 20, 5, 16, -3, 0},
		{"in range", 20, 5, 16, 7, 7},
		{"single-row window", 10, 1, 0, 4, 0},
		{"no window at all", 10, 0, 0, 4, 0},
		{"empty body", 0, 5, 0, 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := maxSidebarScrollFor(tc.bodyLen, tc.bodyH); got != tc.wantMax {
				t.Errorf("maxSidebarScrollFor(%d, %d) = %d, want %d",
					tc.bodyLen, tc.bodyH, got, tc.wantMax)
			}
			if got := clampSidebarScroll(tc.off, tc.bodyLen, tc.bodyH); got != tc.wantClamped {
				t.Errorf("clampSidebarScroll(%d, %d, %d) = %d, want %d",
					tc.off, tc.bodyLen, tc.bodyH, got, tc.wantClamped)
			}
		})
	}
}

// isSidebarScrollMarker identifies a "N above"/"N below" overflow marker row
// by kind AND direction word, not by glyphMore alone: paneRow also spends
// glyphMore on a pane's subagent count, so a substring-only check would flag
// (or miscount) that row too and fail for a reason unrelated to scrolling the
// moment the fixture gains a pane with subagents.
func isSidebarScrollMarker(r sidebarRow) bool {
	return r.kind == "" && strings.Contains(r.text, glyphMore) &&
		(strings.Contains(r.text, "above") || strings.Contains(r.text, "below"))
}

// TestSidebarVisibleRows_WindowIsExactAtEveryOffset sweeps the offset past both
// ends. Two things have to hold at every one of them, and both are how the
// paint and the hit test stay in step: the strip is exactly `height` rows (the
// markers cost a row each and only on the side that has more, so the count is
// the fiddly part), and the body rows painted are a CONTIGUOUS run of the real
// list starting at the clamped offset — a window that skipped or repeated a row
// would still measure the right height.
//
// The last body row must be reachable at exactly the clamped maximum: earlier
// means the max is too large and the final page is drawn short, later means it
// is too small and the tail is unreachable — which is the defect this whole
// task exists to fix.
func TestSidebarVisibleRows_WindowIsExactAtEveryOffset(t *testing.T) {
	t.Parallel()
	m := newTestModelManyPanes(t, 3, 8)
	const w, height = 22, 12

	all, panesStart := m.sidebarRows(w)
	if len(all) <= height {
		t.Fatal("fixture must overflow the strip")
	}
	body := all[panesStart:]
	bodyH := height - panesStart

	for off := -2; off <= len(body)+2; off++ {
		m.sidebarScroll = off
		out := m.sidebarVisibleRows(w, height)
		if len(out) != height {
			t.Fatalf("offset %d: %d rows, want exactly %d", off, len(out), height)
		}
		for i := 0; i < panesStart; i++ {
			if out[i].text != all[i].text {
				t.Fatalf("offset %d row %d: the pinned PROJECTS block moved", off, i)
			}
		}

		var vis []sidebarRow
		for _, r := range out[panesStart:] {
			if isSidebarScrollMarker(r) {
				continue
			}
			vis = append(vis, r)
		}
		if len(vis) == 0 {
			t.Fatalf("offset %d painted no PANES rows at all", off)
		}
		c := clampSidebarScroll(off, len(body), bodyH)
		if c+len(vis) > len(body) {
			t.Fatalf("offset %d (clamped %d): window of %d runs past the %d-row body",
				off, c, len(vis), len(body))
		}
		for i, r := range vis {
			if r.text != body[c+i].text {
				t.Fatalf("offset %d (clamped %d): visible row %d = %q, want body row %d = %q",
					off, c, i, r.text, c+i, body[c+i].text)
			}
		}
		atMax := c == maxSidebarScrollFor(len(body), bodyH)
		if showsLast := c+len(vis) == len(body); showsLast != atMax {
			t.Errorf("offset %d (clamped %d): shows the last body row = %v, is at the max offset = %v",
				off, c, showsLast, atMax)
		}
	}
}

// TestSidebarVisibleRows_LeavesTheStoredOffsetAlone pins the purity rule.
// sidebarVisibleRows runs on the render path, and View has a VALUE receiver —
// so a clamp written back from there is discarded, while the same write from
// the hit test (reached through Update, on the pointer) sticks. The two paths
// would then hold different offsets and resolve a click to a row nobody
// painted. Clamping a local copy has neither half of that problem.
func TestSidebarVisibleRows_LeavesTheStoredOffsetAlone(t *testing.T) {
	t.Parallel()
	m := newTestModelManyPanes(t, 3, 8)
	for _, off := range []int{-5, 0, 3, 999} {
		m.sidebarScroll = off
		_ = m.sidebarVisibleRows(22, 12)
		if m.sidebarScroll != off {
			t.Errorf("sidebarScroll = %d after rendering at offset %d — "+
				"the render path must not write model state", m.sidebarScroll, off)
		}
	}
}

// TestSidebarVisibleRows_ScrollMarkersAreInert: the markers sit inside the
// strip, so a click on one has to be swallowed rather than fall through to the
// pane the sidebar displaced — but it must not select anything either. They
// carry no kind, which is what sidebarHit reads.
func TestSidebarVisibleRows_ScrollMarkersAreInert(t *testing.T) {
	t.Parallel()
	m := newTestModelManyPanes(t, 3, 8)
	m.width, m.height = 100, 13 // sidebarContentHeight() == 12
	m.sidebarScroll = 2         // mid-body: both markers are on screen

	rows := m.sidebarVisibleRows(m.projectSidebarWidth(), m.sidebarContentHeight())
	var markers int
	for y, row := range rows {
		if !isSidebarScrollMarker(row) {
			continue
		}
		markers++
		if row.kind != "" || row.paneID != "" {
			t.Errorf("screen row %d is a scroll marker but carries kind %q / pane %q",
				y, row.kind, row.paneID)
		}
		if kind, idx := m.sidebarHit(1, y); kind != "" || idx != -1 {
			t.Errorf("clicking the scroll marker at row %d resolved to (%q, %d), want no action",
				y, kind, idx)
		}
	}
	if markers != 2 {
		t.Fatalf("found %d scroll markers, want 2 (above and below) — "+
			"the fixture no longer scrolls with rows on both sides", markers)
	}
}

// TestSidebarWheel_ScrollsPanesSection pins that a wheel notch over the strip
// moves the PANES body. The swallow at model.go:1466 already stopped the wheel
// reaching the pane beneath — it just did nothing with it.
func TestSidebarWheel_ScrollsPanesSection(t *testing.T) {
	t.Parallel()
	m := newTestModelManyPanes(t, 3, 8)
	m.width, m.height = 100, 13

	updated, _ := m.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelDown})
	down := updated.(Model)
	if down.sidebarScroll == 0 {
		t.Fatal("wheel down over the sidebar should scroll the PANES section")
	}

	updated, _ = down.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelUp})
	up := updated.(Model)
	if up.sidebarScroll != 0 {
		t.Errorf("sidebarScroll = %d after scrolling back up, want 0", up.sidebarScroll)
	}
}

// TestSidebarWheel_ClampsAtBothEnds pins that the offset cannot run past the
// content in either direction — an unclamped offset paints an empty strip that
// still hit-tests to rows nobody can see.
func TestSidebarWheel_ClampsAtBothEnds(t *testing.T) {
	t.Parallel()
	m := newTestModelManyPanes(t, 3, 8)
	m.width, m.height = 100, 13

	for i := 0; i < 50; i++ {
		updated, _ := m.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelDown})
		m = updated.(Model)
	}
	rows, panesStart := m.sidebarRows(m.projectSidebarWidth())
	want := maxSidebarScrollFor(len(rows)-panesStart, m.sidebarContentHeight()-panesStart)
	if m.sidebarScroll != want {
		t.Errorf("sidebarScroll = %d after over-scrolling, want the max %d", m.sidebarScroll, want)
	}

	for i := 0; i < 50; i++ {
		updated, _ := m.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelUp})
		m = updated.(Model)
	}
	if m.sidebarScroll != 0 {
		t.Errorf("sidebarScroll = %d after over-scrolling up, want 0", m.sidebarScroll)
	}
}

// TestSidebarWheel_HorizontalDoesNotMoveTheBody pins the button match. A
// trackpad (or shift-scroll) emits tea.MouseWheelLeft / tea.MouseWheelRight, and
// the handler used to pass `msg.Button == tea.MouseWheelUp` as a BOOL — so both
// horizontal buttons read as "not up" and scrolled the PANES body DOWN by
// MouseScrollLines. Every other wheel consumer in this package matches the two
// vertical buttons explicitly and ignores the rest.
func TestSidebarWheel_HorizontalDoesNotMoveTheBody(t *testing.T) {
	t.Parallel()
	for _, btn := range []tea.MouseButton{tea.MouseWheelLeft, tea.MouseWheelRight} {
		m := newTestModelManyPanes(t, 3, 8)
		m.width, m.height = 100, 13

		// Park mid-body first: an assertion from offset 0 would also hold for a
		// handler that scrolled UP on every horizontal notch.
		updated, _ := m.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelDown})
		m = updated.(Model)
		before := m.sidebarScroll
		if before == 0 {
			t.Fatal("fixture must scroll on a vertical notch, or this test cannot fail")
		}

		updated, _ = m.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: btn})
		if after := updated.(Model).sidebarScroll; after != before {
			t.Errorf("%v over the strip: sidebarScroll %d → %d, want unchanged", btn, before, after)
		}
	}
}

// TestSidebarWheel_HorizontalIsStillSwallowed is the other half of the same
// branch: refusing to scroll must not let the event fall THROUGH to the pane
// area, because the pane under the cursor is the sidebar, not a pane. A tracking
// app would otherwise receive a wheel escape for a notch aimed at the strip.
//
// The control fires the same button at a PANE coordinate — only the coordinate
// differs between the two halves, so the assertion is about the swallow rather
// than about horizontal buttons being inert everywhere.
func TestSidebarWheel_HorizontalIsStillSwallowed(t *testing.T) {
	t.Parallel()
	fake := newFakeConn()
	m := newSplitDragTestModel(t)
	m.client = fake
	m.sidebarOpen = true
	m.sidebarWidth = 22
	m.curTabs()[0].ActivePaneModel().daemonMouseTracking = true

	updated, _ := m.Update(tea.MouseWheelMsg{X: 40, Y: 10, Button: tea.MouseWheelLeft})
	got := updated.(Model)
	if fake.sentCount() == 0 {
		t.Fatal("control: a wheel over the PANE must forward to a tracking app — " +
			"without it the swallow assertion below is vacuous")
	}
	sentBefore := fake.sentCount()

	updated, _ = got.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelLeft})
	if after := fake.sentCount(); after != sentBefore {
		t.Errorf("horizontal wheel over the strip forwarded %d message(s) to the pane beneath",
			after-sentBefore)
	}
}

// TestSidebarWheel_PathologicalScrollLinesCannotJumpToTheTop pins the cap on
// cfg.UI.MouseScrollLines. The value is a hand-editable config int that nothing
// downstream bounded, and off+lines OVERFLOWS at the top of the int range —
// signed overflow WRAPS in Go, so the sum went negative and clamped to 0.
//
// It takes two notches, which is why the floor alone never caught it: from
// offset 0 the sum is exactly MaxInt and clamps to the bottom correctly. The
// SECOND notch is the one that adds to a non-zero offset, wraps, and throws the
// strip back to the top — a wheel-DOWN moving the list up. bodyH is the ceiling,
// since one notch should never move further than its own window.
func TestSidebarWheel_PathologicalScrollLinesCannotJumpToTheTop(t *testing.T) {
	t.Parallel()
	m := newTestModelManyPanes(t, 3, 30)
	m.width, m.height = 100, 20
	m.cfg.UI.MouseScrollLines = math.MaxInt

	rows, panesStart := m.sidebarRows(m.projectSidebarWidth())
	bodyLen, bodyH := sidebarBodyGeometry(rows, panesStart, m.sidebarContentHeight())
	want := maxSidebarScrollFor(bodyLen, bodyH)
	if want == 0 {
		t.Fatal("fixture must overflow the strip")
	}

	for i := 0; i < 2; i++ {
		updated, _ := m.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelDown})
		m = updated.(Model)
	}
	if m.sidebarScroll != want {
		t.Errorf("sidebarScroll = %d after two wheel-DOWN notches with MouseScrollLines=MaxInt, "+
			"want the bottom %d (0 means off+lines wrapped negative and clamped to the top)",
			m.sidebarScroll, want)
	}
}

// TestSidebarWheel_ShortStripZeroesTheOffset drives scrollSidebar's degenerate
// early return through Update's wheel path. When the pinned PROJECTS block alone
// would leave fewer than minPaneRows for the body, sidebarVisibleRows reverts the
// WHOLE strip to the old tail cap — there is no window to offset into, so the
// only correct stored offset is zero. The paint-side fallback is pinned by
// TestSidebarVisibleRows_ShortStripFallsBackToTailTruncation; this is the writer
// half, which nothing reached through the real dispatch.
func TestSidebarWheel_ShortStripZeroesTheOffset(t *testing.T) {
	t.Parallel()
	m := newTestModelManyPanes(t, 8, 4)
	m.width, m.height = 100, 7 // sidebarContentHeight() == 6

	w := m.projectSidebarWidth()
	rows, panesStart := m.sidebarRows(w)
	if panesStart <= m.sidebarContentHeight()-minPaneRows || len(rows) <= m.sidebarContentHeight() {
		t.Fatalf("fixture is not the degenerate strip: panesStart=%d rows=%d height=%d",
			panesStart, len(rows), m.sidebarContentHeight())
	}

	m.sidebarScroll = 5 // stale, from a taller terminal
	updated, _ := m.Update(tea.MouseWheelMsg{X: 5, Y: 3, Button: tea.MouseWheelDown})
	if got := updated.(Model).sidebarScroll; got != 0 {
		t.Errorf("sidebarScroll = %d after a notch on the degenerate strip, want 0", got)
	}
}

// TestSidebarWheel_ReclampsAStaleOffsetBeforeAddingTheNotch pins the one route
// into the dead scroll plateau sidebarBodyGeometry's comment names but does not
// close. m.sidebarScroll is only ever clamped when the wheel moves it, so ANY
// geometry change between two notches leaves the stored value legitimately past
// the new maximum — sidebarContentHeight() is m.height-1, so a vertical resize
// moves bodyH, and closing panes moves bodyLen. The paint is unaffected
// (sidebarVisibleRows clamps its own local copy, which is the purity rule), so
// the symptom is not row drift: the next several wheel-up notches subtract from
// the stale value, clamp straight back to the same visible maximum, and the
// strip does not move.
//
// Adding to a re-clamped offset makes the FIRST notch move the strip, which is
// what "the bound the user hits is the bound they can see" has to mean.
func TestSidebarWheel_ReclampsAStaleOffsetBeforeAddingTheNotch(t *testing.T) {
	t.Parallel()
	m := newTestModelManyPanes(t, 3, 30)
	m.width, m.height = 100, 20

	w := m.projectSidebarWidth()
	rows, panesStart := m.sidebarRows(w)
	bodyLen, bodyH := sidebarBodyGeometry(rows, panesStart, m.sidebarContentHeight())
	tallMax := maxSidebarScrollFor(bodyLen, bodyH)
	if tallMax == 0 {
		t.Fatal("fixture must overflow the short strip")
	}

	// Park at the bottom of the SHORT strip.
	for i := 0; i < 20; i++ {
		updated, _ := m.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelDown})
		m = updated.(Model)
	}
	if m.sidebarScroll != tallMax {
		t.Fatalf("sidebarScroll = %d after scrolling to the bottom, want %d", m.sidebarScroll, tallMax)
	}

	// Grow the terminal: the window gets taller, so the maximum offset SHRINKS
	// and the stored value is now stale-high. Nothing re-clamps it.
	m.height = 30
	_, shortBodyH := sidebarBodyGeometry(rows, panesStart, m.sidebarContentHeight())
	shortMax := maxSidebarScrollFor(bodyLen, shortBodyH)
	if shortMax >= tallMax {
		t.Fatalf("fixture does not shrink the maximum (%d → %d) — this test cannot fail",
			tallMax, shortMax)
	}

	lines := m.cfg.UI.MouseScrollLines
	if lines < 1 {
		lines = 3
	}
	want := shortMax - lines
	if want < 0 {
		want = 0
	}

	updated, _ := m.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelUp})
	m = updated.(Model)
	if m.sidebarScroll != want {
		t.Errorf("sidebarScroll = %d after one wheel-up notch at the new geometry, want %d "+
			"(stale offset %d was clamped to %d and the notch subtracted from the stale value)",
			m.sidebarScroll, want, tallMax, shortMax)
	}
}

// TestScrollSidebarToPane_BringsOffscreenPaneIntoView pins that a pane reached
// from the palette, a hook jump or pane-history is not left below the cut.
func TestScrollSidebarToPane_BringsOffscreenPaneIntoView(t *testing.T) {
	t.Parallel()
	m := newTestModelManyPanes(t, 3, 12)
	m.width, m.height = 100, 13
	w := m.projectSidebarWidth()

	all, panesStart := m.sidebarRows(w)
	var last string
	for i := panesStart; i < len(all); i++ {
		if all[i].kind == sidebarRowPane {
			last = all[i].paneID
		}
	}
	if last == "" {
		t.Fatal("fixture must contain pane rows")
	}

	m.sidebarScroll = 0
	m.scrollSidebarToPane(last)

	rows := m.sidebarVisibleRows(w, m.sidebarContentHeight())
	found := false
	for _, r := range rows {
		if r.kind == sidebarRowPane && r.paneID == last {
			found = true
		}
	}
	if !found {
		t.Errorf("pane %q still off-screen at offset %d", last, m.sidebarScroll)
	}
}

// TestScrollSidebarToPane_AlreadyVisibleIsANoOp pins the guarantee
// focusSidebarPane depends on: scrolling to a pane the sidebar already shows
// must not move the strip out from under a click on it.
//
// The boundary matters here, not just "some visible pane": at sidebarScroll
// == 0 only the "N below" marker can show, so the REAL window is bodyH-1 rows
// — one wider than the bodyH-2 span scrollSidebarToPane uses to pick a NEW
// offset when a scroll is actually needed. Asserting against the LAST row
// sidebarVisibleRows actually paints (rather than an arbitrary early one)
// exercises exactly the row a naive reuse of that conservative span would
// wrongly judge off-screen.
func TestScrollSidebarToPane_AlreadyVisibleIsANoOp(t *testing.T) {
	t.Parallel()
	m := newTestModelManyPanes(t, 3, 12)
	m.width, m.height = 100, 13
	w := m.projectSidebarWidth()
	height := m.sidebarContentHeight()

	m.sidebarScroll = 0
	visible := m.sidebarVisibleRows(w, height)
	var lastVisiblePane string
	for _, r := range visible {
		if r.kind == sidebarRowPane {
			lastVisiblePane = r.paneID
		}
	}
	if lastVisiblePane == "" {
		t.Fatal("fixture must paint at least one pane row at offset 0")
	}

	m.scrollSidebarToPane(lastVisiblePane)

	if m.sidebarScroll != 0 {
		t.Errorf("sidebarScroll = %d after scrolling to already-visible pane %q, want 0 (no-op)",
			m.sidebarScroll, lastVisiblePane)
	}
}
