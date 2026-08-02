package tui

import (
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

// Row geometry, once, for every test below: View() draws the full-width tab
// bar at screen row 0 and joins the sidebar into tabContent underneath it,
// so sidebar row k is screen row k+1. Row 0 of the sidebar is the PROJECTS
// heading, so project i sits at screen row i+2.
func TestClickingProjectRowSwitchesProject(t *testing.T) {
	m := Model{
		projects:     []*ProjectModel{{ID: "proj-a", Name: "alpha"}, {ID: "proj-b", Name: "beta"}},
		sidebarOpen:  true,
		sidebarWidth: 22, width: 200, height: 40,
	}
	if kind, idx := m.sidebarHit(3, 1); kind != "" {
		t.Fatalf("the PROJECTS heading is chrome, got (%q, %d)", kind, idx)
	}
	if kind, idx := m.sidebarHit(3, 2); kind != sidebarRowProject || idx != 0 {
		t.Fatalf("sidebarHit(3, 2) = (%q, %d), want (project, 0)", kind, idx)
	}
	kind, idx := m.sidebarHit(3, 3) // second project row, under the heading
	if kind != sidebarRowProject || idx != 1 {
		t.Fatalf("sidebarHit = (%q, %d), want (project, 1)", kind, idx)
	}
	// The tab bar and the status bar are drawn full width, above and below
	// the sidebar — a press on either belongs to them.
	if kind, _ := m.sidebarHit(3, 0); kind != "" {
		t.Error("row 0 is the tab bar, not the sidebar")
	}
	if kind, _ := m.sidebarHit(3, m.height-1); kind != "" {
		t.Error("the last row is the status bar, not the sidebar")
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
	lines := strings.Split(m.renderSidebar(m.height-chromeHeight), "\n")
	const screenY = 3
	kind, idx := m.sidebarHit(3, screenY)
	if kind != sidebarRowProject || idx != 1 {
		t.Fatalf("sidebarHit(3, %d) = (%q, %d), want (project, 1)", screenY, kind, idx)
	}
	if got := lines[screenY-1]; !strings.Contains(got, "beta") {
		t.Fatalf("painted sidebar row %d = %q, but the hit test calls it project 1 (beta)", screenY-1, got)
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
	// rows: 0 PROJECTS, 1 alpha, 2 spacer, 3 PANES, 4 tab heading, 5+ panes.
	if kind, idx := m.sidebarHit(3, 6); kind != sidebarRowPane || idx != 0 {
		t.Fatalf("sidebarHit(3, 6) = (%q, %d), want (pane, 0)", kind, idx)
	}
	if kind, idx := m.sidebarHit(3, 7); kind != sidebarRowPane || idx != 1 {
		t.Fatalf("sidebarHit(3, 7) = (%q, %d), want (pane, 1)", kind, idx)
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
	if m.activeProject != 1 || m.prevProject != 0 {
		t.Fatalf("activeProject/prevProject = %d/%d, want 1/0", m.activeProject, m.prevProject)
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
	before := pane.scrollBack

	updated, _ := m.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelUp})
	got := updated.(Model)
	if after := got.curTabs()[0].ActivePaneModel().scrollBack; after != before {
		t.Errorf("pane scrollBack = %d, want %d — the wheel over the sidebar scrolled a pane it is not over", after, before)
	}
}

func TestClickOnProjectRowSwitchesThroughUpdate(t *testing.T) {
	fake := newFakeConn()
	m := newSplitDragTestModel(t)
	m.client = fake
	m.sidebarOpen = true
	m.sidebarWidth = 22
	m.projects = append(m.projects, &ProjectModel{ID: "proj-b", Name: "beta", Dest: "gpu01"})

	// Screen row 3 is the second project row (row 0 tab bar, row 1 heading).
	updated, cmd := m.Update(tea.MouseClickMsg{X: 3, Y: 3, Button: tea.MouseLeft})
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
func TestReleaseClickFocusesThePaneUnderTheCursor(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.sidebarOpen = true
	m.sidebarWidth = 22
	m.mouseDown = true
	// Column 70 is inside the RIGHT pane once the sidebar shifts the area
	// ([61, 100)); with the old 0-based walk it resolved to the left one.
	m.mouseStartX, m.mouseStartY = 70, 10

	updated, _ := m.Update(tea.MouseReleaseMsg{X: 70, Y: 10, Button: tea.MouseLeft})
	after := updated.(Model)
	if got := after.curTabs()[0].ActivePane; got != "p2" {
		t.Errorf("ActivePane = %q, want p2 — the release resolved the pane at the wrong column", got)
	}
}

// ---------------------------------------------------------------------------
// Toggle keybinding
// ---------------------------------------------------------------------------

func TestSidebarToggleKeyFlipsResizesAndPersists(t *testing.T) {
	m := newSplitDragTestModel(t)
	m.cfg = config.Default()
	if m.cfg.Keybindings.SidebarToggle != "alt+shift+s" {
		t.Fatalf("default sidebar_toggle = %q, want alt+shift+s", m.cfg.Keybindings.SidebarToggle)
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
	// And back.
	updated, _ = got.handleKey(tea.KeyPressMsg{Mod: tea.ModAlt | tea.ModShift, Code: 's'})
	if updated.(Model).sidebarOpen {
		t.Error("second press must close the sidebar")
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
