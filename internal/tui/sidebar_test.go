package tui

import (
	"fmt"
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
	if len(m.sidebarRows(22)) <= h {
		t.Fatal("fixture does not overflow — the cap would not be exercised")
	}
	lines := strings.Split(m.renderSidebar(h), "\n")
	if len(lines) != h {
		t.Fatalf("renderSidebar emitted %d lines for a %d-row area", len(lines), h)
	}
	// The tail is dropped and marked, so the PROJECTS block — the navigation
	// the sidebar exists for — always survives.
	if !strings.Contains(lines[len(lines)-1], "…") {
		t.Errorf("last row = %q, want an overflow marker", lines[len(lines)-1])
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
	m := Model{
		projects: []*ProjectModel{
			{ID: "proj-a", Name: "数据库迁移服务集群控制台", Dest: "gpu01", tabs: []*TabModel{tabWith(blocked)}},
			{ID: "proj-b", Name: "beta"},
		},
		sidebarOpen:  true,
		sidebarWidth: 22, width: 200, height: 40,
		links: map[string]*reconnectState{"gpu01": {parked: true}},
	}
	// Control: if lipgloss ever measured these as one cell the fixture would
	// stop discriminating and both tests below would pass vacuously.
	if m.linkGlyph("gpu01") != "⚡" {
		t.Fatalf("fixture did not produce the parked glyph, got %q", m.linkGlyph("gpu01"))
	}
	if lipgloss.Width("⚡") != 2 || lipgloss.Width("构") != 2 {
		t.Fatalf("fixture glyphs are not wide (⚡=%d 构=%d) — this test cannot fail",
			lipgloss.Width("⚡"), lipgloss.Width("构"))
	}
	return m
}

// No row may exceed its column budget. Asserted on sidebarRows, BEFORE
// renderSidebar's closing .Width(w) — that pass WRAPS an over-wide line onto
// a second painted line rather than truncating it, so measuring the rendered
// output would see every line within budget and miss this entirely.
func TestSidebarRowsNeverExceedTheirColumnBudget(t *testing.T) {
	m := wideGlyphSidebarModel(t)
	for i, row := range m.sidebarRows(22) {
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

	var tabRows, paneRows []string
	for _, r := range m.sidebarRows(m.sidebarWidth) {
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
	for _, r := range m.sidebarRows(m.sidebarWidth) {
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
	working, blocked, finished := p.counts()
	if working != 1 || blocked != 1 || finished != 1 {
		t.Fatalf("counts() = (working %d, blocked %d, done %d), want (1, 1, 1) — "+
			"a parked pane must count once, as blocked", working, blocked, finished)
	}

	// Built from the glyph constants rather than literals: the badge's job is
	// to carry a COUNT per state, and pinning the codepoints here would make a
	// deliberate glyph change (see TestSidebarGlyphs_OneCellAndNotEmojiCapable
	// for why one was needed) look like a counting regression.
	row := projectRow("alpha", working, blocked, finished, "", false, 30)
	for _, want := range []string{glyphBlocked + "1", glyphWorking + "1", glyphDone + "1"} {
		if !strings.Contains(row, want) {
			t.Errorf("project row %q is missing the %s badge", row, want)
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
