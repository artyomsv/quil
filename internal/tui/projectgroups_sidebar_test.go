package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/config"
)

// newGroupsSidebarModel is newSplitDragTestModel (tab T: p1|p2) with the
// sidebar open at 22 columns, four projects and two groups. Rows:
//
//	0 PROJECTS            (ungroupDrop)
//	1 ▸ L1                (project 0, ACTIVE, owns tab T)
//	2 ▾ G-A (2)           (group 0)
//	3     R1              (project 1, on gpu01)
//	4        @gpu01       (project 1's host row)
//	5     L2              (project 2)
//	6 ▸ G-B (1)           (group 1, collapsed — L3 is hidden)
//	7 (blank)  8 PANES  9 T heading  10 p1  11 p2
func newGroupsSidebarModel(t *testing.T) (*Model, *fakeConn) {
	t.Helper()
	fake := newFakeConn()
	m := newSplitDragTestModel(t)
	m.client = fake
	m.sidebarOpen = true
	m.sidebarWidth = 22
	m.projects[0].ID, m.projects[0].Name = "l1", "L1"
	m.projects = append(m.projects,
		&ProjectModel{ID: "r1", Name: "R1", Dest: "gpu01"},
		&ProjectModel{ID: "l2", Name: "L2"},
		&ProjectModel{ID: "l3", Name: "L3"},
	)
	m.groups = projectGroups{Groups: []projectGroup{
		{Name: "G-A", Members: []groupMember{{ID: "l2"}, {Dest: "gpu01", ID: "r1"}}},
		{Name: "G-B", Collapsed: true, Members: []groupMember{{ID: "l3"}}},
	}}
	return m, fake
}

// newTwinsSidebarModel holds two projects that share the ID "proj-1" — one
// local ("here", active), one on gpu01 ("there") — and two empty groups.
// Rows: 0 PROJECTS, 1 here, 2 there, 3 its host, 4 ▾ G (0), 5 ▾ H (0).
func newTwinsSidebarModel(t *testing.T) (*Model, *fakeConn) {
	t.Helper()
	fake := newFakeConn()
	m := newSplitDragTestModel(t)
	m.client = fake
	m.sidebarOpen = true
	m.sidebarWidth = 22
	m.projects[0].ID, m.projects[0].Name = "proj-1", "here"
	m.projects = append(m.projects, &ProjectModel{ID: "proj-1", Name: "there", Dest: "gpu01"})
	m.groups = grpFromNames("G", "H")
	return m, fake
}

// grpShape renders the PROJECTS section as tokens — "h" the heading, "gN" a
// header, "pN" an ungrouped project row, "pN@gM" a member row — so a row-order
// assertion reads as one string.
func grpShape(m *Model, w int) string {
	rows, panesStart := m.sidebarRows(w)
	var out []string
	for _, r := range rows[:panesStart-2] { // drop the blank and the PANES heading
		switch {
		case r.ungroupDrop:
			out = append(out, "h")
		case r.kind == sidebarRowGroup:
			out = append(out, fmt.Sprintf("g%d", r.index))
		case r.kind == sidebarRowProject && r.inGroup:
			out = append(out, fmt.Sprintf("p%d@g%d", r.index, r.group))
		case r.kind == sidebarRowProject:
			out = append(out, fmt.Sprintf("p%d", r.index))
		default:
			out = append(out, "?")
		}
	}
	return strings.Join(out, " ")
}

// grpCountKind counts rows of one kind.
func grpCountKind(rows []sidebarRow, kind string) int {
	n := 0
	for _, r := range rows {
		if r.kind == kind {
			n++
		}
	}
	return n
}

func grpPress(m Model, y int, b tea.MouseButton) (Model, tea.Cmd) {
	updated, cmd := m.Update(tea.MouseClickMsg{X: 3, Y: y, Button: b})
	return updated.(Model), cmd
}

func grpRelease(m Model, y int) (Model, tea.Cmd) {
	updated, cmd := m.Update(tea.MouseReleaseMsg{X: 3, Y: y, Button: tea.MouseLeft})
	return updated.(Model), cmd
}

func grpMotion(m Model, y int) (Model, tea.Cmd) {
	updated, cmd := m.Update(tea.MouseMotionMsg{X: 3, Y: y, Button: tea.MouseLeft})
	return updated.(Model), cmd
}

func TestSidebarGroups_UngroupedFirstThenEachGroupInItsOrder(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	if got, want := grpShape(m, 22), "h p0 g0 p1@g0 p1@g0 p2@g0 g1"; got != want {
		t.Fatalf("shape = %q, want %q", got, want)
	}
	rows, _ := m.sidebarRows(22)
	for _, tc := range []struct {
		y    int
		want string
	}{
		{1, "▸ L1"},
		{2, "▾ G-A (2)"},
		{3, "    R1"},
		{4, "     @gpu01"},
		{5, "    L2"},
		{6, "▸ G-B (1)"},
	} {
		if got := strings.TrimRight(stripANSI(rows[tc.y].text), " "); got != tc.want {
			t.Errorf("row %d = %q, want %q", tc.y, got, tc.want)
		}
	}
	// Expanded, G-B lists its member indented, in project order.
	m.groups.Groups[1].Collapsed = false
	if got, want := grpShape(m, 22), "h p0 g0 p1@g0 p1@g0 p2@g0 g1 p3@g1"; got != want {
		t.Fatalf("expanded shape = %q, want %q", got, want)
	}
}

func TestSidebarGroups_CollapsedGroupShowsOnlyTheActiveProject(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	m.groups.Groups[1].Members = append(m.groups.Groups[1].Members, groupMember{ID: "l1"})
	// L1 (active) and L3 are both in collapsed G-B: only L1's row shows.
	if got, want := grpShape(m, 22), "h g0 p1@g0 p1@g0 p2@g0 g1 p0@g1"; got != want {
		t.Fatalf("shape = %q, want %q", got, want)
	}
	rows, _ := m.sidebarRows(22)
	if got := strings.TrimRight(stripANSI(rows[6].text), " "); got != "  ▸ L1" {
		t.Errorf("row 6 = %q, want the active marker on the indented row", got)
	}
	if got := strings.TrimRight(stripANSI(rows[5].text), " "); got != "▸ G-B (2)" {
		t.Errorf("header = %q, want every member counted, not only the one shown", got)
	}
}

func TestSidebarGroups_SameIDOnTwoDaemonsRendersInItsOwnSection(t *testing.T) {
	m, _ := newTwinsSidebarModel(t)
	m.groups.Groups[0].Members = []groupMember{{ID: "proj-1"}}
	// Only the LOCAL proj-1 is in G; gpu01's proj-1 stays ungrouped.
	if got, want := grpShape(m, 22), "h p1 p1 g0 p0@g0 g1"; got != want {
		t.Fatalf("shape = %q, want %q", got, want)
	}
}

func TestSidebarGroups_HeaderRollsUpItsMembersBadges(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	tabOf := func(id string, panes ...*PaneModel) *TabModel {
		tab := NewTabModel(id, id)
		tab.Root = columnsLayout(panes)
		tab.ActivePane = panes[0].ID
		return tab
	}
	blocked, done := newTestPane("b1"), newTestPane("d1")
	blocked.blockedSince = time.Now()
	done.unseen = true
	working, pinned := newTestPane("w1"), newTestPane("n1")
	working.working = true
	pinned.pinnedAttention = true
	m.projects[2].tabs = []*TabModel{tabOf("t-l2", blocked, done)}
	m.projects[1].tabs = []*TabModel{tabOf("t-r1", working, pinned)}
	m.projects[1].Offline = &OfflineState{Kind: offlineNeedsInstall}

	rows, _ := m.sidebarRows(40)
	head := stripANSI(rows[2].text)
	for _, want := range []string{
		"▾ G-A (2)",
		" " + glyphBlocked + "1",
		" " + workingGlyph(m.workSpinnerFrame) + "1",
		" " + glyphDone + "1",
		" " + glyphPinned + "1",
		" " + glyphLinkParked,
	} {
		if !strings.Contains(head, want) {
			t.Errorf("header %q is missing %q", head, want)
		}
	}
	if w := lipgloss.Width(rows[2].text); w != 40 {
		t.Errorf("header is %d cells, want 40", w)
	}
	if other := stripANSI(rows[6].text); strings.ContainsAny(other, glyphBlocked+glyphDone+glyphPinned) {
		t.Errorf("G-B's header %q carries G-A's badges", other)
	}
}

func TestSidebarGroups_HeaderClickTogglesAndSaves(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m, _ := newGroupsSidebarModel(t)
	m.SetProjectGroups(ProjectGroupsState{groups: m.groups}, config.ProjectGroupsPath())

	got, _ := grpPress(*m, 6, tea.MouseLeft)
	if !got.groupDragging || got.groupDragIdx != 1 {
		t.Fatalf("drag = (%v, %d) after pressing G-B's header, want (true, 1)", got.groupDragging, got.groupDragIdx)
	}
	if !got.groups.Groups[1].Collapsed {
		t.Fatal("the PRESS toggled the group; the toggle belongs to the release, or every drag would collapse it")
	}
	got, cmd := grpRelease(got, 6)
	if got.groups.Groups[1].Collapsed {
		t.Fatal("the release did not expand G-B")
	}
	if got.groupDragging {
		t.Fatal("the release must end the group drag")
	}
	if cmd == nil {
		t.Fatal("a toggle must save")
	}
	runCmd(cmd)
	saved, err := loadProjectGroups(config.ProjectGroupsPath())
	if err != nil || len(saved.Groups) != 2 || saved.Groups[1].Collapsed {
		t.Fatalf("saved = %+v (err %v), want G-B expanded", saved, err)
	}

	got, _ = grpPress(got, 6, tea.MouseLeft)
	got, _ = grpRelease(got, 6)
	if !got.groups.Groups[1].Collapsed {
		t.Fatal("a second click did not collapse G-B again")
	}
}

func TestProjectGroups_SaveFailureFlashesAndKeepsTheChange(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QUIL_HOME", dir)
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, _ := newGroupsSidebarModel(t)
	// The parent of the path is a FILE, so the save's MkdirAll fails.
	m.SetProjectGroups(ProjectGroupsState{groups: m.groups}, filepath.Join(blocker, "project-groups.json"))

	got, _ := grpPress(*m, 6, tea.MouseLeft)
	got, cmd := grpRelease(got, 6)
	if cmd == nil {
		t.Fatal("the toggle issued no save")
	}
	msg := cmd()
	if _, ok := msg.(projectGroupsSaveFailedMsg); !ok {
		t.Fatalf("save result = %T, want projectGroupsSaveFailedMsg", msg)
	}
	// The returned cmd is the 3 s flash tick: not run.
	updated, _ := got.Update(msg)
	got = updated.(Model)
	if got.flashText != groupSaveFailedFlash {
		t.Errorf("flash = %q, want %q", got.flashText, groupSaveFailedFlash)
	}
	if got.groups.Groups[1].Collapsed {
		t.Error("a failed save must keep the in-memory change")
	}
}

func TestSaveGroupsCmd_SnapshotIsTakenWhenTheCmdIsBuilt(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newModelForTest(nil, 0)
	m.SetProjectGroups(ProjectGroupsState{groups: grpFromNames("before")}, config.ProjectGroupsPath())
	cmd := m.saveGroupsCmd()
	if m.groupsSeq != 1 {
		t.Fatalf("groupsSeq = %d, want 1", m.groupsSeq)
	}
	// Update keeps editing the groups before the Cmd's goroutine runs.
	if err := m.groups.renameGroup(0, "after"); err != nil {
		t.Fatal(err)
	}
	if msg := cmd(); msg != nil {
		t.Fatalf("save failed: %v", msg)
	}
	got, err := loadProjectGroups(config.ProjectGroupsPath())
	if err != nil || grpNames(got) != "before" {
		t.Fatalf("file holds %s (err %v), want before — the Cmd must write the snapshot taken on the Update goroutine", grpNames(got), err)
	}
}

func TestElideEnd_CutsToTheBudgetWithAnEllipsis(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		s    string
		w    int
		want string
	}{
		{"Work", 10, "Work"},
		{"Workshop", 5, "Work…"},
		{"构建构建", 5, "构建…"},
		{"Workshop", 1, "W"},
		{"构建", 1, ""},
		{"Workshop", 0, ""},
	} {
		got := elideEnd(tc.s, tc.w)
		if got != tc.want {
			t.Errorf("elideEnd(%q, %d) = %q, want %q", tc.s, tc.w, got, tc.want)
		}
		if lipgloss.Width(got) > tc.w {
			t.Errorf("elideEnd(%q, %d) is %d cells, over the budget", tc.s, tc.w, lipgloss.Width(got))
		}
	}
}

func TestSidebarGroups_EveryRowIsExactlyTheStripWidth(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	m.groups.Groups[0].Name = "构建构建构建构建构建构建构建构建"
	m.groups.Groups[1].Collapsed = false
	m.projects[1].Name = "远程机器的名字很长很长"
	for _, w := range []int{1, 2, 3, 4, 5, 8, 12, 22, 40} {
		rows, _ := m.sidebarRows(w)
		for y, r := range rows {
			if r.kind != sidebarRowProject && r.kind != sidebarRowGroup {
				continue
			}
			if got := lipgloss.Width(r.text); got != w {
				t.Errorf("w=%d row %d (%s %d, inGroup %v) is %d cells, want exactly %d — "+
					"renderSidebar's .Width(w) would wrap it", w, y, r.kind, r.index, r.inGroup, got, w)
			}
			// The NAME gives way, never the member count: renderStyledSegments
			// would cut an over-wide head to w on its own, so the width check
			// above cannot tell an elided name from a count cut off the end.
			if r.kind == sidebarRowGroup && w >= 12 {
				want := fmt.Sprintf(" (%d)", len(m.groups.Groups[r.index].Members))
				if got := strings.TrimRight(stripANSI(r.text), " "); !strings.HasSuffix(got, want) {
					t.Errorf("w=%d header %q lost its count %q — the name must be cut instead", w, got, want)
				}
			}
		}
	}
}

func TestSidebarGroups_PaintAndHitTestAgreeOnHeaderAndIndentedRows(t *testing.T) {
	build := func(t *testing.T, w int) *Model {
		m, _ := newGroupsSidebarModel(t)
		m.sidebarWidth = w
		m.groups.Groups[0].Name = "构建构建构建构建构建构建构建构建"
		m.groups.Groups[1].Collapsed = false
		m.projects[1].Name = "远程机器的名字很长很长"
		return m
	}
	for _, w := range []int{22, 12, 5} {
		t.Run(fmt.Sprintf("w=%d", w), func(t *testing.T) {
			m := build(t, w)
			h := m.sidebarContentHeight()
			rows := m.sidebarVisibleRows(w, h)
			_, panesStart := m.sidebarRows(w)
			painted := strings.Split(m.renderSidebar(h), "\n")
			var headers []int
			for y := 0; y < panesStart; y++ {
				got := strings.TrimRight(stripANSI(painted[y]), " ")
				want := strings.TrimRight(stripANSI(rows[y].text), " ")
				if got != want {
					t.Fatalf("screen row %d painted %q but sidebarRowAt resolves it to %q — a row "+
						"wrapped, and every click below it now lands one row off", y, got, want)
				}
				if rows[y].kind == sidebarRowGroup {
					headers = append(headers, y)
				}
			}
			if len(headers) != 2 {
				t.Fatalf("headers at %v, want 2", headers)
			}
			// Each header, clicked, toggles the group it paints — and only that one.
			for _, y := range headers {
				m := build(t, w)
				g := rows[y].index
				got, _ := grpPress(*m, y, tea.MouseLeft)
				got, _ = grpRelease(got, y)
				for i := range got.groups.Groups {
					if want := i == g; got.groups.Groups[i].Collapsed != want {
						t.Errorf("clicking row %d (group %d): group %d collapsed = %v, want %v", y, g, i, got.groups.Groups[i].Collapsed, want)
					}
				}
			}
			// An indented member row activates the project it paints.
			for y := 0; y < panesStart; y++ {
				if r := rows[y]; r.kind == sidebarRowProject && r.inGroup && r.index == 2 {
					got, _ := grpPress(*build(t, w), y, tea.MouseLeft)
					if got.activeProject != 2 {
						t.Errorf("clicking L2's indented row (y=%d) activated project %d, want 2", y, got.activeProject)
					}
					return
				}
			}
			t.Fatal("L2's indented row is not on screen")
		})
	}
}

func TestSidebarGroups_CollapsedActiveRowIsPartOfThePinnedHead(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	// Real PaneModels: L3 becomes the ACTIVE project, so every Update below
	// walks these panes (focus ack, the press handlers), not just the render.
	panes := make([]*PaneModel, 6)
	for i := range panes {
		panes[i] = NewPaneModel(fmt.Sprintf("q%d", i+1), 1024)
	}
	work := NewTabModel("t-l3", "Work")
	work.Root = columnsLayout(panes)
	work.ActivePane = "q1"
	m.projects[3].tabs = []*TabModel{work}
	// Setup: L3 lives in collapsed G-B, reached in real use through Alt+P.
	m.switchProject(3)
	// Rows: 0 PROJECTS, 1 L1, 2 ▾ G-A (2), 3 R1, 4 host, 5 L2, 6 ▸ G-B (1),
	// 7 ▸ L3 (indented), 8 blank, 9 PANES, 10 Work, 11-16 q1..q6.
	m.height = 15 // content height 14: 17 rows, a 10-row head, a 4-row window
	rows := m.sidebarVisibleRows(22, m.sidebarContentHeight())
	if r := rows[7]; r.kind != sidebarRowProject || r.index != 3 || !r.inGroup || r.group != 1 {
		t.Fatalf("row 7 = %+v, want L3's row under G-B", r)
	}
	if got := strings.TrimRight(stripANSI(rows[7].text), " "); got != "  ▸ L3" {
		t.Errorf("row 7 = %q, want the indented active row", got)
	}
	got, _ := grpPress(*m, 7, tea.MouseLeft)
	if !got.projectDragging || got.projectDragIdx != 3 {
		t.Fatalf("a press on the collapsed group's active row armed (%v, %d), want (true, 3)", got.projectDragging, got.projectDragIdx)
	}
	got, _ = grpRelease(got, 7)

	// Two rows shorter, the head leaves fewer than minPaneRows for the body:
	// the whole strip reverts to the tail cap, and the active row is still there.
	got.height = 13
	rows = got.sidebarVisibleRows(22, got.sidebarContentHeight())
	if len(rows) != 12 || rows[11].kind != "" || !strings.Contains(stripANSI(rows[11].text), "…") {
		t.Fatalf("at 12 content rows the strip must be the tail cap (12 rows, last one \" …\"), got %d rows", len(rows))
	}
	if r := rows[7]; r.kind != sidebarRowProject || r.index != 3 {
		t.Fatalf("row 7 = %+v under the tail cap, want L3", r)
	}
	if n := grpCountKind(rows, sidebarRowPane); n != 0 {
		t.Fatalf("%d pane rows visible under the tail cap, want 0", n)
	}

	// Collapsing G-A takes three rows out of the head and gives the PANES
	// section its window back.
	got, _ = grpPress(got, 2, tea.MouseLeft)
	got, _ = grpRelease(got, 2)
	if !got.groups.Groups[0].Collapsed {
		t.Fatal("the click on G-A's header did not collapse it")
	}
	rows = got.sidebarVisibleRows(22, got.sidebarContentHeight())
	if n := grpCountKind(rows, sidebarRowPane); n == 0 {
		t.Fatal("collapsing a group gave the PANES section no room")
	}
	if r := rows[4]; r.kind != sidebarRowProject || r.index != 3 {
		t.Fatalf("row 4 = %+v, want L3 moved up under G-B's header", r)
	}
}

// grpBroadcastModel is the echoModel shape (broadcast_echo_test.go): a Model
// whose listen loop is inert, holding two local projects and an OFFLINE remote
// one, all three in group G.
func grpBroadcastModel(t *testing.T) Model {
	t.Helper()
	m := Model{
		cfg:            config.Default(),
		notifications:  NewNotificationCenter(30, 50),
		mcpHighlights:  make(map[string]bool),
		tabDragFromIdx: -1,
		sized:          true,
		width:          100,
		height:         40,
		client:         &echoRecorder{},
	}
	m.projects = []*ProjectModel{
		{ID: "l1", Name: "one"},
		{ID: "l-gone", Name: "gone"},
		{ID: "r1", Name: "remote", Dest: "gpu01", Offline: &OfflineState{Kind: offlineRetrying}},
	}
	m.groups = projectGroups{Groups: []projectGroup{{Name: "G", Members: []groupMember{
		{ID: "l1"}, {ID: "l-gone"}, {Dest: "gpu01", ID: "r1"},
	}}}}
	return m
}

func TestProjectGroups_BroadcastPrunesOnlyItsOwnDestination(t *testing.T) {
	// noteWorkspaceState's update-notice path can resolve config paths.
	t.Setenv("QUIL_HOME", t.TempDir())

	t.Run("a local broadcast drops the missing local member and keeps the offline host's", func(t *testing.T) {
		m := grpBroadcastModel(t)
		updated, _ := m.Update(WorkspaceStateMsg{Projects: []ProjectInfo{{ID: "l1", Name: "one"}}, ActiveProject: "l1"})
		got := updated.(Model)
		if members := grpMembers(got.groups, 0); members != "/l1,gpu01/r1" {
			t.Fatalf("members = %s, want /l1,gpu01/r1", members)
		}
		if got.groupsSeq != m.groupsSeq+1 {
			t.Errorf("groupsSeq = %d, want %d — a prune that changed something must save", got.groupsSeq, m.groupsSeq+1)
		}
	})
	t.Run("a broadcast naming no project prunes nothing", func(t *testing.T) {
		m := grpBroadcastModel(t)
		updated, _ := m.Update(WorkspaceStateMsg{})
		got := updated.(Model)
		if members := grpMembers(got.groups, 0); members != "/l1,/l-gone,gpu01/r1" {
			t.Fatalf("members = %s, want all three", members)
		}
		if got.groupsSeq != m.groupsSeq {
			t.Errorf("groupsSeq moved without a change")
		}
	})
	t.Run("a broadcast from a disconnected destination is ignored", func(t *testing.T) {
		m := grpBroadcastModel(t)
		updated, _ := m.Update(WorkspaceStateMsg{Dest: "gpu01", Projects: []ProjectInfo{{ID: "other", Name: "x"}}})
		got := updated.(Model)
		if members := grpMembers(got.groups, 0); members != "/l1,/l-gone,gpu01/r1" {
			t.Fatalf("members = %s, want all three", members)
		}
	})
	t.Run("a broadcast that still holds every member saves nothing", func(t *testing.T) {
		m := grpBroadcastModel(t)
		updated, _ := m.Update(WorkspaceStateMsg{Projects: []ProjectInfo{{ID: "l1", Name: "one"}, {ID: "l-gone", Name: "gone"}}, ActiveProject: "l1"})
		got := updated.(Model)
		if got.groupsSeq != m.groupsSeq {
			t.Errorf("groupsSeq = %d, want %d — nothing was pruned", got.groupsSeq, m.groupsSeq)
		}
	})
}
