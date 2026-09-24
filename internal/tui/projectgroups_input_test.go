package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// grpMenuRow returns the index of the open menu's row with id and, for a
// group row, that group's name ("" for every other row).
func grpMenuRow(t *testing.T, s ctxMenuState, id ctxMenuAction, name string) int {
	t.Helper()
	for i, it := range s.items {
		if it.id == id && it.groupName == name {
			return i
		}
	}
	t.Fatalf("menu has no row id=%d name=%q (open=%v, %d rows)", id, name, s.open(), len(s.items))
	return -1
}

// grpChoose left-clicks row i of the open menu through Update.
func grpChoose(t *testing.T, m Model, i int) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(tea.MouseClickMsg{X: m.ctxMenu.x + 1, Y: m.ctxMenu.itemScreenY(i), Button: tea.MouseLeft})
	return updated.(Model), cmd
}

// grpType sends each rune of s through Update as a printable key press.
func grpType(m Model, s string) Model {
	for _, r := range s {
		updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated.(Model)
	}
	return m
}

// grpKey sends one special key (tea.KeyEnter, tea.KeyEscape, …).
func grpKey(m Model, code rune) (Model, tea.Cmd) {
	updated, cmd := m.Update(tea.KeyPressMsg{Code: code})
	return updated.(Model), cmd
}

// grpLabels joins an open menu's row labels.
func grpLabels(s ctxMenuState) string {
	out := make([]string, len(s.items))
	for i, it := range s.items {
		out[i] = it.label
	}
	return strings.Join(out, "|")
}

// grpOpenGroupList right-clicks the project row at y and chooses Move to group….
func grpOpenGroupList(t *testing.T, m Model, y int) Model {
	t.Helper()
	m, _ = grpPress(m, y, tea.MouseRight)
	if m.ctxMenu.projectID == "" {
		t.Fatalf("setup: a right-click at y=%d opened no project menu", y)
	}
	m, _ = grpChoose(t, m, grpMenuRow(t, m.ctxMenu, ctxActGroupList, ""))
	if n := len(m.ctxMenu.items); !m.ctxMenu.open() || n == 0 || m.ctxMenu.items[n-1].id != ctxActUngroup {
		t.Fatalf("setup: Move to group… did not re-populate the menu (open=%v, rows=%q)", m.ctxMenu.open(), grpLabels(m.ctxMenu))
	}
	return m
}

// grpAt sends a mouse message at an explicit column — grpPress / grpMotion /
// grpRelease all pin X inside the strip.
func grpAt(m Model, msg tea.Msg) (Model, tea.Cmd) {
	updated, cmd := m.Update(msg)
	return updated.(Model), cmd
}

// newRankSidebarModel: one group G holding a local La, gpu01's Rb and a local
// Lc, nothing ungrouped. Rows: 0 PROJECTS, 1 ▾ G (3), 2 La, 3 Rb, 4 Rb's host, 5 Lc.
func newRankSidebarModel(t *testing.T) (*Model, *fakeConn) {
	t.Helper()
	fake := newFakeConn()
	m := newSplitDragTestModel(t)
	m.client = fake
	m.sidebarOpen = true
	m.sidebarWidth = 22
	m.projects[0].ID, m.projects[0].Name = "la", "La"
	m.projects = append(m.projects,
		&ProjectModel{ID: "rb", Name: "Rb", Dest: "gpu01"},
		&ProjectModel{ID: "lc", Name: "Lc"},
	)
	m.groups = projectGroups{Groups: []projectGroup{{Name: "G", Members: []groupMember{
		{ID: "la"}, {Dest: "gpu01", ID: "rb"}, {ID: "lc"},
	}}}}
	return m, fake
}

func TestSidebarGroups_HeaderDragReordersGroupsWithoutToggling(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	got, _ := grpPress(*m, 6, tea.MouseLeft) // G-B's header
	got, _ = grpMotion(got, 1)               // an ungrouped row: no group under the pointer
	if names := grpNames(got.groups); names != "G-A,G-B" {
		t.Fatalf("hovering an ungrouped row reordered the groups: %s", names)
	}
	got, cmd := grpMotion(got, 2) // G-A's header: the upper half of its 4-row block
	if names := grpNames(got.groups); names != "G-B,G-A" {
		t.Fatalf("order = %s after crossing G-A's midpoint, want G-B,G-A", names)
	}
	if cmd != nil {
		t.Error("a group drag saves on release, not per motion event")
	}
	if got.groupDragIdx != 0 || !got.groupDragMoved {
		t.Fatalf("drag = (%d, moved %v), want (0, true)", got.groupDragIdx, got.groupDragMoved)
	}
	got, _ = grpMotion(got, 2) // stationary, now over G-B itself
	if names := grpNames(got.groups); names != "G-B,G-A" {
		t.Fatalf("order flipped on a stationary pointer: %s", names)
	}
	seq := got.groupsSeq
	got, _ = grpRelease(got, 2)
	if !got.groups.Groups[0].Collapsed || got.groups.Groups[1].Collapsed {
		t.Fatal("releasing a drag toggled a group")
	}
	if got.groupsSeq != seq+1 {
		t.Errorf("groupsSeq = %d, want %d — the reorder must be saved on release", got.groupsSeq, seq+1)
	}
	if got.groupDragging {
		t.Fatal("release must end the group drag")
	}
}

// A header pressed, dragged OFF, and released anywhere but that header is
// neither a click nor a reorder: nothing toggles and nothing is saved.
func TestSidebarGroups_HeaderPressReleasedElsewhereDoesNotToggle(t *testing.T) {
	for _, tc := range []struct {
		name string
		x, y int
		move bool // a motion event at (x, y) before the release
	}{
		{"moved into the pane area and released there", 50, 10, true},
		{"moved onto an ungrouped project row and released there", 3, 1, true},
		// No motion: crossing G-A's midpoint would be a real reorder.
		{"released on the other group's header", 3, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newGroupsSidebarModel(t)
			got, _ := grpPress(*m, 6, tea.MouseLeft) // G-B's header
			if tc.move {
				got, _ = grpAt(got, tea.MouseMotionMsg{X: tc.x, Y: tc.y, Button: tea.MouseLeft})
			}
			got, _ = grpAt(got, tea.MouseReleaseMsg{X: tc.x, Y: tc.y, Button: tea.MouseLeft})
			if !got.groups.Groups[1].Collapsed || got.groups.Groups[0].Collapsed {
				t.Fatalf("collapsed = [%v %v], want [false true] — a release off the header toggled a group",
					got.groups.Groups[0].Collapsed, got.groups.Groups[1].Collapsed)
			}
			if got.groupsSeq != 0 {
				t.Errorf("groupsSeq = %d, want 0 — nothing changed, nothing to save", got.groupsSeq)
			}
			if got.groupDragging {
				t.Fatal("release must end the group drag")
			}
		})
	}
}

// sidebarRow.group is 0 on every row outside a group — the PROJECTS heading
// and the PANES rows included — which is group 0's own index. A header drag
// over them must read inGroup first, or it would treat them as group 0.
func TestSidebarGroups_HeaderDragOverNonGroupRowsMovesNothing(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	rows, _ := m.sidebarRows(22)
	for _, y := range []int{0, 10} {
		if rows[y].inGroup || rows[y].group != 0 {
			t.Fatalf("setup: row %d = inGroup %v group %d, want a non-group row reading group 0", y, rows[y].inGroup, rows[y].group)
		}
	}
	got, _ := grpPress(*m, 6, tea.MouseLeft) // G-B's header
	for _, y := range []int{0, 10} {         // the PROJECTS heading, pane p1's row
		got, _ = grpMotion(got, y)
		if names := grpNames(got.groups); names != "G-A,G-B" || got.groupDragMoved {
			t.Fatalf("hovering y=%d moved the groups to %s (moved %v)", y, names, got.groupDragMoved)
		}
	}
	if start, size := groupBlockSpanIn(rows, 0); start != 2 || size != 4 {
		t.Errorf("group 0's block = (%d, %d), want (2, 4) — rows outside the group were counted", start, size)
	}
}

func TestSidebarGroups_ProjectDroppedOnAHeaderJoinsThatGroup(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	got, _ := grpPress(*m, 1, tea.MouseLeft) // L1, ungrouped
	got, cmd := grpMotion(got, 6)
	if cmd != nil || projectNames(got) != "L1,R1,L2,L3" {
		t.Fatal("hovering a header must not reorder or send anything")
	}
	seq := got.groupsSeq
	got, _ = grpRelease(got, 6)
	if g := got.groups.groupOf("", "l1"); g != 1 {
		t.Fatalf("L1 is in group %d after the drop, want 1 (G-B)", g)
	}
	if got.groupsSeq != seq+1 {
		t.Errorf("groupsSeq = %d, want %d", got.groupsSeq, seq+1)
	}
	// L1 is active, so collapsed G-B shows it under the header.
	if shape := grpShape(&got, 22); shape != "h g0 p1@g0 p1@g0 p2@g0 g1 p0@g1" {
		t.Fatalf("shape = %q", shape)
	}
	// Dropped on its OWN group's header: nothing changes, nothing is saved.
	seq = got.groupsSeq
	got, _ = grpPress(got, 6, tea.MouseLeft) // L1's row under G-B
	got, _ = grpMotion(got, 5)               // a real drag: a click decides nothing
	got, _ = grpRelease(got, 5)              // G-B's header
	if got.groupsSeq != seq || got.groups.groupOf("", "l1") != 1 {
		t.Fatalf("a drop on its own header changed state (seq %d→%d, group %d)", seq, got.groupsSeq, got.groups.groupOf("", "l1"))
	}
}

// A plain CLICK never changes membership — nor does a click whose pointer
// jitters sideways on the press row. The press switches the active project,
// and the collapsed G1 above was showing the OLD active project, so its row
// (two for a remote one) vanishes and every row below moves up: the press row
// y=5 now names G3's header, although the pointer never left it.
func TestSidebarGroups_ClickWithoutMotionNeverRegroups(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dest    string // P0's host
		pressed int    // the project on row 5 before the press
		jitter  bool   // a motion one cell sideways on the press row
	}{
		// Rows: 0 PROJECTS, 1 ▸ G1, 2 P0, 3 ▾ G2, 4 P1, 5 P2, 6 ▾ G3, 7 P3.
		{"a local active member", "", 2, false},
		{"a local active member, sideways jitter", "", 2, true},
		// Rows: 0 PROJECTS, 1 ▸ G1, 2 P0, 3 P0's host, 4 ▾ G2, 5 P1, 6 P2, 7 ▾ G3, 8 P3.
		{"a remote active member", "gpu01", 1, false},
		{"a remote active member, sideways jitter", "gpu01", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newSplitDragTestModel(t)
			m.client = newFakeConn()
			m.sidebarOpen = true
			m.sidebarWidth = 22
			m.projects[0].ID, m.projects[0].Name, m.projects[0].Dest = "p0", "P0", tc.dest
			m.projects = append(m.projects,
				&ProjectModel{ID: "p1", Name: "P1"},
				&ProjectModel{ID: "p2", Name: "P2"},
				&ProjectModel{ID: "p3", Name: "P3"},
			)
			m.groups = projectGroups{Groups: []projectGroup{
				{Name: "G1", Collapsed: true, Members: []groupMember{{Dest: tc.dest, ID: "p0"}}},
				{Name: "G2", Members: []groupMember{{ID: "p1"}, {ID: "p2"}}},
				{Name: "G3", Members: []groupMember{{ID: "p3"}}},
			}}
			got, _ := grpPress(*m, 5, tea.MouseLeft)
			if got.activeProject != tc.pressed {
				t.Fatalf("setup: the press activated project %d, want %d", got.activeProject, tc.pressed)
			}
			// Not vacuous: after the press the press row IS G3's header.
			rows, _ := got.sidebarRows(22)
			if r := rows[5]; r.kind != sidebarRowGroup || r.index != 2 {
				t.Fatalf("setup: row 5 after the press = %+v, want G3's header", r)
			}
			if tc.jitter {
				got, _ = grpAt(got, tea.MouseMotionMsg{X: 4, Y: 5, Button: tea.MouseLeft})
			}
			got, _ = grpRelease(got, 5)
			id := got.projects[tc.pressed].ID
			if g := got.groups.groupOf("", id); g != 1 {
				t.Fatalf("%s is in group %d after a click, want 1 (G2)", id, g)
			}
			if got.groupsSeq != 0 {
				t.Errorf("groupsSeq = %d, want 0 — a click saves nothing", got.groupsSeq)
			}
		})
	}
}

func TestSidebarGroups_GroupedProjectDroppedInTheUngroupedSectionLeavesItsGroup(t *testing.T) {
	for _, tc := range []struct {
		name string
		y    int
	}{
		{"on the PROJECTS heading", 0},
		{"on an ungrouped project row", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, fake := newGroupsSidebarModel(t)
			got, _ := grpPress(*m, 5, tea.MouseLeft) // L2, in G-A (now active)
			seq := got.groupsSeq
			got, _ = grpMotion(got, tc.y) // a real drag: a click decides nothing
			got, _ = grpRelease(got, tc.y)
			if g := got.groups.groupOf("", "l2"); g != -1 {
				t.Fatalf("L2 is still in group %d", g)
			}
			if got.groupsSeq != seq+1 {
				t.Errorf("groupsSeq = %d, want %d", got.groupsSeq, seq+1)
			}
			if len(got.projects) != 4 {
				t.Fatalf("%d projects, want 4 — leaving a group closes nothing", len(got.projects))
			}
			if shape := grpShape(&got, 22); shape != "h p0 p2 g0 p1@g0 p1@g0 g1" {
				t.Fatalf("shape = %q, want L2 back among the ungrouped, in project order", shape)
			}
			if msgs := reorderProjectMessages(t, fake.sent); len(msgs) != 0 {
				t.Fatalf("leaving a group sent %d reorder_project message(s)", len(msgs))
			}
		})
	}
}

func TestSidebarGroups_HoveringAnotherSectionNeverReorders(t *testing.T) {
	m, fake := newGroupsSidebarModel(t)
	got, _ := grpPress(*m, 5, tea.MouseLeft) // L2, in G-A
	for _, y := range []int{1, 0, 6} {       // an ungrouped row, the heading, G-B's header
		var cmd tea.Cmd
		got, cmd = grpMotion(got, y)
		if cmd != nil || projectNames(got) != "L1,R1,L2,L3" {
			t.Fatalf("hovering y=%d reordered to %s", y, projectNames(got))
		}
		if !got.projectDragging {
			t.Fatalf("hovering y=%d cancelled the drag", y)
		}
	}
	if msgs := reorderProjectMessages(t, fake.sent); len(msgs) != 0 {
		t.Fatalf("hovering produced %d reorder_project message(s)", len(msgs))
	}
}

// The other direction: an UNGROUPED project dragged over a group's member rows
// stays where it is — joining a group is a drop on its header, never motion.
// The dragged project is NOT the first of its section, and the hovered rows
// include a remote project's lower (host) row: a row of another section has no
// position in this one, and read as position -1 the midpoint rule would slot
// the drag at 0 from exactly that row.
func TestSidebarGroups_UngroupedDragOverAGroupsMembersNeverReorders(t *testing.T) {
	m, fake := newGroupsSidebarModel(t)
	m.groups.Groups[1].Members = nil // L3 ungrouped too
	// Rows: 0 PROJECTS, 1 L1, 2 L3, 3 ▾ G-A (2), 4 R1, 5 its host, 6 L2, 7 ▾ G-B (0).
	got, _ := grpPress(*m, 2, tea.MouseLeft) // L3: second of the ungrouped [L1, L3]
	for _, y := range []int{4, 5, 6} {       // R1, its host, L2
		var cmd tea.Cmd
		got, cmd = grpMotion(got, y)
		if cmd != nil || projectNames(got) != "L1,R1,L2,L3" {
			t.Fatalf("hovering y=%d reordered to %s", y, projectNames(got))
		}
	}
	if got.groups.groupOf("", "l3") != -1 {
		t.Fatal("hovering a group's rows put L3 into it")
	}
	if msgs := reorderProjectMessages(t, fake.sent); len(msgs) != 0 {
		t.Fatalf("hovering produced %d reorder_project message(s)", len(msgs))
	}
}

func TestSidebarGroups_ReorderInsideAGroupSendsTheDaemonRankOrNothing(t *testing.T) {
	m, fake := newRankSidebarModel(t)
	got, _ := grpPress(*m, 5, tea.MouseLeft) // Lc
	got, cmd := grpMotion(got, 3)            // Rb's upper row: past its midpoint going up
	if names := projectNames(got); names != "La,Lc,Rb" {
		t.Fatalf("order = %s, want La,Lc,Rb", names)
	}
	if cmd != nil {
		cmd()
	}
	if msgs := reorderProjectMessages(t, fake.sent); len(msgs) != 0 {
		t.Fatalf("messages = %+v, want none — Lc passed only gpu01's project, so it is still the local daemon's second", msgs)
	}
	got, cmd = grpMotion(got, 2) // La: past it
	if names := projectNames(got); names != "Lc,La,Rb" {
		t.Fatalf("order = %s, want Lc,La,Rb", names)
	}
	if cmd == nil {
		t.Fatal("Lc became the local daemon's first project; the daemon must be told")
	}
	cmd()
	msgs := reorderProjectMessages(t, fake.sent)
	if len(msgs) != 1 || msgs[0].ProjectID != "lc" || msgs[0].NewIndex != 0 || msgs[0].dest != "" {
		t.Fatalf("messages = %+v, want one moving lc to index 0 on the local daemon", msgs)
	}
	if got.groups.groupOf("", "lc") != 0 {
		t.Error("a reorder inside a group moved the project out of it")
	}
}

func TestProjectMoveKeys_StayInsideTheirSection(t *testing.T) {
	t.Parallel()
	fake := newFakeConn()
	m := newModelForTest([]string{"T"}, 0)
	m.client = fake
	m.notifications = NewNotificationCenter(30, 200)
	m.width, m.height = 100, 40
	m.projects = []*ProjectModel{{ID: "l1", Name: "L1"}, {ID: "l2", Name: "L2"}, {ID: "l3", Name: "L3"}, {ID: "l4", Name: "L4"}}
	m.groups = projectGroups{Groups: []projectGroup{{Name: "G", Members: []groupMember{{ID: "l2"}, {ID: "l4"}}}}}
	m.activeProject = 3
	up := tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt | tea.ModShift}
	down := tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModAlt | tea.ModShift}

	// L4 up: its section is G = [L2, L4], so it lands before L2, past L3.
	updated, cmd := m.handleKey(up)
	got := updated.(Model)
	if names := projectNames(got); names != "L1,L4,L2,L3" {
		t.Fatalf("order = %s, want L1,L4,L2,L3", names)
	}
	cmd()
	msgs := reorderProjectMessages(t, fake.sent)
	if len(msgs) != 1 || msgs[0].ProjectID != "l4" || msgs[0].NewIndex != 1 {
		t.Fatalf("messages = %+v, want l4 to index 1", msgs)
	}
	// At the top of G, up does nothing — it never leaves the group.
	updated, cmd = got.handleKey(up)
	got = updated.(Model)
	if names := projectNames(got); names != "L1,L4,L2,L3" || cmd != nil || got.groups.groupOf("", "l4") != 0 {
		t.Fatalf("up at the top of G = %s (cmd %v, group %d), want a no-op", names, cmd != nil, got.groups.groupOf("", "l4"))
	}
	// L1 down: its section is the ungrouped [L1, L3].
	got.activeProject = 0
	updated, cmd = got.handleKey(down)
	got = updated.(Model)
	if names := projectNames(got); names != "L4,L2,L3,L1" {
		t.Fatalf("order = %s, want L4,L2,L3,L1", names)
	}
	if got.groups.groupOf("", "l1") != -1 {
		t.Fatal("a move key put L1 into a group")
	}
	cmd()
	msgs = reorderProjectMessages(t, fake.sent)
	if len(msgs) != 2 || msgs[1].ProjectID != "l1" || msgs[1].NewIndex != 3 {
		t.Fatalf("messages = %+v, want a second moving l1 to index 3", msgs)
	}
}

func TestPalette_MoveProjectRowsFollowTheSection(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.projects = []*ProjectModel{{ID: "l1", Name: "L1"}, {ID: "l2", Name: "L2"}, {ID: "l3", Name: "L3"}, {ID: "l4", Name: "L4"}}
	m.groups = projectGroups{Groups: []projectGroup{{Name: "G", Members: []groupMember{{ID: "l2"}, {ID: "l4"}}}}}
	for _, tc := range []struct {
		name     string
		active   int
		up, down bool
	}{
		{"L1, top of the ungrouped", 0, false, true},
		{"L2, top of G", 1, false, true},
		{"L3, bottom of the ungrouped", 2, true, false},
		{"L4, bottom of G", 3, true, false},
	} {
		m.activeProject = tc.active
		up, down := paletteRows(m, palActMoveProjectUp), paletteRows(m, palActMoveProjectDown)
		if len(up) != 1 || len(down) != 1 {
			t.Fatalf("%s: %d up / %d down rows, want 1 / 1", tc.name, len(up), len(down))
		}
		if up[0].enabled != tc.up || down[0].enabled != tc.down {
			t.Errorf("%s: up %v / down %v, want %v / %v", tc.name, up[0].enabled, down[0].enabled, tc.up, tc.down)
		}
	}
}

func TestProjectCtxMenu_MoveToGroupListsGroupsNewGroupAndNoGroup(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	got := grpOpenGroupList(t, *m, 5) // L2, in G-A
	if labels := grpLabels(got.ctxMenu); labels != "✓ G-A|  G-B|  New group…|  No group" {
		t.Fatalf("rows = %q", labels)
	}
	if got.ctxMenu.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (the current group)", got.ctxMenu.cursor)
	}
	seq := got.groupsSeq
	got, _ = grpChoose(t, got, grpMenuRow(t, got.ctxMenu, ctxActSetGroup, "G-B"))
	if got.ctxMenu.open() {
		t.Error("choosing a group must close the menu")
	}
	if g := got.groups.groupOf("", "l2"); g != 1 {
		t.Fatalf("L2 is in group %d, want 1", g)
	}
	if got.groupsSeq != seq+1 {
		t.Errorf("groupsSeq = %d, want %d", got.groupsSeq, seq+1)
	}

	// An ungrouped project's list marks No group.
	got = grpOpenGroupList(t, got, 1) // L1
	if labels := grpLabels(got.ctxMenu); labels != "  G-A|  G-B|  New group…|✓ No group" {
		t.Fatalf("rows = %q", labels)
	}
	if got.ctxMenu.cursor != 3 {
		t.Errorf("cursor = %d, want 3 (No group)", got.ctxMenu.cursor)
	}
}

// A group name may be 32 runes — 64 cells when wide — but the menu box is
// capped at ctxMenuTitleCap and pads each label by (box - label): an uncut
// name made that count negative and panicked the render. The row is cut; the
// group it moves the project into is still the full name.
func TestProjectCtxMenu_LongGroupNameIsCutAndStillChosen(t *testing.T) {
	for _, name := range []string{strings.Repeat("W", maxGroupNameRunes), strings.Repeat("构", maxGroupNameRunes)} {
		m, _ := newGroupsSidebarModel(t)
		m.groups.Groups[1].Name = name
		got := grpOpenGroupList(t, *m, 1) // L1
		_ = renderCtxMenu(got.ctxMenu)    // must not panic
		for _, it := range got.ctxMenu.items {
			if w := lipgloss.Width(it.label); w > ctxMenuTitleCap {
				t.Fatalf("row %q is %d cells, over the %d-cell box", it.label, w, ctxMenuTitleCap)
			}
		}
		got, _ = grpChoose(t, got, grpMenuRow(t, got.ctxMenu, ctxActSetGroup, name))
		if g := got.groups.groupOf("", "l1"); g != 1 {
			t.Fatalf("L1 is in group %d, want 1 (the long-named group)", g)
		}
	}
}

func TestProjectCtxMenu_NoGroupTakesAProjectOutOfItsGroup(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	got := grpOpenGroupList(t, *m, 3) // R1, in G-A
	got, _ = grpChoose(t, got, grpMenuRow(t, got.ctxMenu, ctxActUngroup, ""))
	if g := got.groups.groupOf("gpu01", "r1"); g != -1 {
		t.Fatalf("R1 is still in group %d", g)
	}
	if got.groupsSeq != 1 {
		t.Errorf("groupsSeq = %d, want 1", got.groupsSeq)
	}
}

// grpOpenNewGroup right-clicks the project row at y and chooses Move to group…
// → New group…, which must open the group-name dialog.
func grpOpenNewGroup(t *testing.T, m Model, y int) Model {
	t.Helper()
	got := grpOpenGroupList(t, m, y)
	got, _ = grpChoose(t, got, grpMenuRow(t, got.ctxMenu, ctxActNewGroup, ""))
	if got.ctxMenu.open() || got.dialog != dialogGroupName || got.groupEdit.mode != groupEditNew {
		t.Fatalf("New group… did not open the name dialog (menu open %v, dialog %d, mode %d)",
			got.ctxMenu.open(), got.dialog, got.groupEdit.mode)
	}
	return got
}

// grpDialogBox finds the one bordered box in a rendered frame and returns its
// rows (ANSI stripped) with the box's left column and width. It fails when a
// row between the corners does not carry BOTH side borders at the corners'
// columns. A WRAPPED line keeps its borders — lipgloss wraps inside the box —
// so wrapping shows as extra rows, which callers count (grpDialogRows).
func grpDialogBox(t *testing.T, frame string) (rows []string, left, width int) {
	t.Helper()
	lines := strings.Split(stripANSI(frame), "\n")
	top, bottom := -1, -1
	for i, l := range lines {
		if top < 0 && strings.Contains(l, "╭") {
			top = i
		}
		if strings.Contains(l, "╰") {
			bottom = i
		}
	}
	if top < 0 || bottom <= top {
		t.Fatalf("no dialog box in the frame:\n%s", strings.Join(lines, "\n"))
	}
	cellOf := func(l string, i int) int {
		if i < 0 {
			return -1
		}
		return lipgloss.Width(l[:i])
	}
	left = cellOf(lines[top], strings.Index(lines[top], "╭"))
	width = cellOf(lines[top], strings.Index(lines[top], "╮")) - left + 1
	for i := top; i <= bottom; i++ {
		l := lines[i]
		first, last := "│", "│"
		if i == top {
			first, last = "╭", "╮"
		} else if i == bottom {
			first, last = "╰", "╯"
		}
		if cellOf(l, strings.Index(l, first)) != left || cellOf(l, strings.LastIndex(l, last)) != left+width-1 {
			t.Fatalf("box row %d is not a whole row of the box %d..%d: %q", i-top, left, left+width-1, l)
		}
		rows = append(rows, l)
	}
	return rows, left, width
}

// grpBoxRowWith returns the box row containing s, failing when none does.
func grpBoxRowWith(t *testing.T, rows []string, s string) string {
	t.Helper()
	for _, r := range rows {
		if strings.Contains(r, s) {
			return r
		}
	}
	t.Fatalf("no box row contains %q:\n%s", s, strings.Join(rows, "\n"))
	return ""
}

// New group… opens a CENTRED dialog — the status-bar editor it replaced was
// easy enough to miss that the menu row read as doing nothing.
func TestGroupNameDialog_NewGroupNamesAGroupAndPutsTheProjectInIt(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m, _ := newGroupsSidebarModel(t)
	m.SetProjectGroups(ProjectGroupsState{groups: m.groups}, config.ProjectGroupsPath())
	got := grpOpenNewGroup(t, *m, 1) // L1, ungrouped
	got = grpType(got, "ops")
	frame := got.View().Content
	rows, left, width := grpDialogBox(t, frame)
	grpBoxRowWith(t, rows, "New group")
	grpBoxRowWith(t, rows, "Name: ops▎")
	grpBoxRowWith(t, rows, "Enter save · Esc cancel")
	if right := got.width - left - width; left-right > 1 || right-left > 1 {
		t.Errorf("box spans columns %d..%d of %d — not centred", left, left+width-1, got.width)
	}
	if strings.Contains(stripANSI(frame), "New group: ") {
		t.Error("the status-bar editor is still drawn")
	}
	got, cmd := grpKey(got, tea.KeyEnter)
	if got.dialog != dialogNone {
		t.Fatal("Enter on a valid name must close the dialog")
	}
	if names := grpNames(got.groups); names != "G-A,G-B,ops" {
		t.Fatalf("groups = %s, want the new group last", names)
	}
	if g := got.groups.groupOf("", "l1"); g != 2 {
		t.Fatalf("L1 is in group %d, want 2 (ops)", g)
	}
	runCmd(cmd)
	saved, err := loadProjectGroups(config.ProjectGroupsPath())
	if err != nil || grpNames(saved) != "G-A,G-B,ops" || grpMembers(saved, 2) != "/l1" {
		t.Fatalf("saved %s / %q (err %v), want ops holding /l1", grpNames(saved), grpMembers(saved, 2), err)
	}
}

func TestGroupNameDialog_RefusesEmptyAndDuplicateNamesAndStaysOpen(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	got := grpOpenNewGroup(t, *m, 1)
	// Enter on nothing. The returned cmd is the 3 s flash tick: not run.
	got, _ = grpKey(got, tea.KeyEnter)
	if got.groupNameRefusal() != groupNameEmptyFlash || got.dialog != dialogGroupName {
		t.Fatalf("empty name: refusal %q, dialog %d — want %q and still open", got.groupNameRefusal(), got.dialog, groupNameEmptyFlash)
	}
	got = grpType(got, "g-a")
	if got.groupNameRefusal() != "" {
		t.Error("typing must clear the refusal it no longer describes")
	}
	got, _ = grpKey(got, tea.KeyEnter)
	if got.groupNameRefusal() != groupNameTakenFlash || got.dialog != dialogGroupName || got.groupEdit.input != "g-a" {
		t.Fatalf("duplicate: refusal %q, dialog %d, input %q — want %q, open, input kept",
			got.groupNameRefusal(), got.dialog, got.groupEdit.input, groupNameTakenFlash)
	}
	if names := grpNames(got.groups); names != "G-A,G-B" {
		t.Fatalf("a refused name created a group: %s", names)
	}
	got, _ = grpKey(got, tea.KeyBackspace)
	if got.groupEdit.input != "g-" {
		t.Fatalf("input after backspace = %q, want g-", got.groupEdit.input)
	}
	got, _ = grpKey(got, tea.KeyEscape)
	if got.dialog != dialogNone || grpNames(got.groups) != "G-A,G-B" || got.groups.groupOf("", "l1") != -1 {
		t.Fatal("Esc must close the dialog and create nothing")
	}
}

func TestGroupNameDialog_InputStopsAt32Runes(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	got := grpOpenNewGroup(t, *m, 1)
	got = grpType(got, strings.Repeat("é", 40))
	if n := len([]rune(got.groupEdit.input)); n != maxGroupNameRunes {
		t.Fatalf("input holds %d runes, want %d", n, maxGroupNameRunes)
	}
}

func TestProjectCtxMenu_MoveToGroupTargetsTheRightDaemonsTwin(t *testing.T) {
	m, _ := newTwinsSidebarModel(t)
	// Rows: 0 PROJECTS, 1 here (local proj-1), 2 there (gpu01's proj-1), 3 its host, 4 ▾ G (0), 5 ▾ H (0).
	got := grpOpenGroupList(t, *m, 2)
	if got.ctxMenu.projectDest != "gpu01" {
		t.Fatalf("menu projectDest = %q, want gpu01", got.ctxMenu.projectDest)
	}
	got, _ = grpChoose(t, got, grpMenuRow(t, got.ctxMenu, ctxActSetGroup, "G"))
	if r, l := got.groups.groupOf("gpu01", "proj-1"), got.groups.groupOf("", "proj-1"); r != 0 || l != -1 {
		t.Fatalf("groupOf = gpu01 %d / local %d, want 0 / -1 — the menu grouped the other daemon's project", r, l)
	}
	// Rows now: 0 PROJECTS, 1 here, 2 ▾ G (1), 3 there, 4 its host, 5 ▾ H (0).
	// The remote twin's own list marks G — read by (gpu01, proj-1), not by
	// the local twin's (absent) membership.
	got = grpOpenGroupList(t, got, 3)
	if first := got.ctxMenu.items[0]; first.groupName != "G" || !strings.HasPrefix(first.label, "✓") {
		t.Fatalf("rows = %q, want G marked — the remote twin's list read the local twin's membership", grpLabels(got.ctxMenu))
	}
	got = grpOpenGroupList(t, got, 1)
	if last := got.ctxMenu.items[len(got.ctxMenu.items)-1]; !strings.HasPrefix(last.label, "✓") {
		t.Fatalf("rows = %q, want No group marked — the local twin's list read the remote twin's membership", grpLabels(got.ctxMenu))
	}
	got, _ = grpChoose(t, got, grpMenuRow(t, got.ctxMenu, ctxActSetGroup, "H"))
	if r, l := got.groups.groupOf("gpu01", "proj-1"), got.groups.groupOf("", "proj-1"); r != 0 || l != 1 {
		t.Fatalf("groupOf = gpu01 %d / local %d, want 0 / 1", r, l)
	}
}

func TestGroupCtxMenu_RowsFollowTheGroup(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	got, _ := grpPress(*m, 2, tea.MouseRight) // G-A: first, expanded
	if got.ctxMenu.groupName != "G-A" || got.ctxMenu.title != "G-A" {
		t.Fatalf("menu target %q title %q, want G-A", got.ctxMenu.groupName, got.ctxMenu.title)
	}
	if labels := grpLabels(got.ctxMenu); labels != "Rename group|Collapse|Move up|Move down|Delete group" {
		t.Fatalf("rows = %q", labels)
	}
	if got.ctxMenu.items[grpMenuRow(t, got.ctxMenu, ctxActGroupUp, "")].enabled {
		t.Error("Move up is enabled on the first group")
	}
	got, _ = grpPress(got, 6, tea.MouseRight) // re-targets to G-B: last, collapsed
	if got.ctxMenu.groupName != "G-B" {
		t.Fatalf("menu target %q, want G-B", got.ctxMenu.groupName)
	}
	if got.ctxMenu.items[grpMenuRow(t, got.ctxMenu, ctxActToggleGroup, "")].label != "Expand" {
		t.Error("a collapsed group's toggle row must read Expand")
	}
	if got.ctxMenu.items[grpMenuRow(t, got.ctxMenu, ctxActGroupDown, "")].enabled {
		t.Error("Move down is enabled on the last group")
	}
}

func TestGroupCtxMenu_Actions(t *testing.T) {
	open := func(t *testing.T, y int) (Model, *fakeConn) {
		t.Helper()
		m, fake := newGroupsSidebarModel(t)
		got, _ := grpPress(*m, y, tea.MouseRight)
		if got.ctxMenu.groupName == "" {
			t.Fatalf("setup: a right-click at y=%d opened no group menu", y)
		}
		return got, fake
	}
	t.Run("collapse", func(t *testing.T) {
		got, _ := open(t, 2)
		got, _ = grpChoose(t, got, grpMenuRow(t, got.ctxMenu, ctxActToggleGroup, ""))
		if !got.groups.Groups[0].Collapsed || got.groupsSeq != 1 {
			t.Fatalf("collapsed %v seq %d, want true / 1", got.groups.Groups[0].Collapsed, got.groupsSeq)
		}
	})
	t.Run("move down", func(t *testing.T) {
		got, _ := open(t, 2)
		got, _ = grpChoose(t, got, grpMenuRow(t, got.ctxMenu, ctxActGroupDown, ""))
		if names := grpNames(got.groups); names != "G-B,G-A" || got.groupsSeq != 1 {
			t.Fatalf("order %s seq %d, want G-B,G-A / 1", names, got.groupsSeq)
		}
	})
	t.Run("move up", func(t *testing.T) {
		got, _ := open(t, 6)
		got, _ = grpChoose(t, got, grpMenuRow(t, got.ctxMenu, ctxActGroupUp, ""))
		if names := grpNames(got.groups); names != "G-B,G-A" {
			t.Fatalf("order %s, want G-B,G-A", names)
		}
	})
	t.Run("delete keeps every project", func(t *testing.T) {
		got, fake := open(t, 2)
		got, _ = grpChoose(t, got, grpMenuRow(t, got.ctxMenu, ctxActDeleteGroup, ""))
		if names := grpNames(got.groups); names != "G-B" {
			t.Fatalf("groups = %s, want G-B", names)
		}
		if got.groups.groupOf("", "l2") != -1 || got.groups.groupOf("gpu01", "r1") != -1 || len(got.projects) != 4 {
			t.Fatal("deleting a group must ungroup its members and keep every project")
		}
		for _, msg := range fake.sent {
			if msg.Type == ipc.MsgDestroyProject {
				t.Fatal("deleting a group sent destroy_project")
			}
		}
	})
	t.Run("rename", func(t *testing.T) {
		got, _ := open(t, 2)
		got, _ = grpChoose(t, got, grpMenuRow(t, got.ctxMenu, ctxActRenameGroup, ""))
		if got.dialog != dialogGroupName || got.groupEdit.mode != groupEditRename || got.groupEdit.input != "G-A" {
			t.Fatalf("dialog %d, editor = %+v, want the name dialog seeded with G-A", got.dialog, got.groupEdit)
		}
		rows, _, _ := grpDialogBox(t, got.renderDialog())
		grpBoxRowWith(t, rows, "Rename group")
		grpBoxRowWith(t, rows, "Name: G-A▎")
		for range 3 {
			got, _ = grpKey(got, tea.KeyBackspace)
		}
		got = grpType(got, "ops")
		got, _ = grpKey(got, tea.KeyEnter)
		if names := grpNames(got.groups); names != "ops,G-B" || got.groups.groupOf("", "l2") != 0 {
			t.Fatalf("groups = %s (L2 in %d), want ops,G-B with L2 still in it", names, got.groups.groupOf("", "l2"))
		}
	})
}

func TestGroupCtxMenu_ClosesWhenItsGroupIsDeletedElsewhere(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	got, _ := grpPress(*m, 6, tea.MouseRight)
	if got.ctxMenu.groupName != "G-B" {
		t.Fatalf("setup: menu target %q", got.ctxMenu.groupName)
	}
	got.groups.deleteGroup(1)
	updated, _ := got.Update(flashExpireMsg{})
	if updated.(Model).ctxMenu.open() {
		t.Fatal("a menu whose group vanished must close on the next message")
	}
}

func TestGroupKeys_ToggleTheActiveGroupAndCollapseAll(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	f9 := layBind(t, m, "project.group_toggle")
	// L1 is ungrouped: nothing to toggle and nothing saved.
	updated, _ := m.Update(f9)
	got := updated.(Model)
	if got.groupsSeq != 0 || got.groups.Groups[0].Collapsed {
		t.Fatalf("toggling with an ungrouped active project changed state (seq %d)", got.groupsSeq)
	}
	got, _ = grpPress(got, 5, tea.MouseLeft) // activate L2, in G-A
	got, _ = grpRelease(got, 5)
	updated, _ = got.Update(f9)
	got = updated.(Model)
	if !got.groups.Groups[0].Collapsed || got.groupsSeq != 1 {
		t.Fatalf("G-A collapsed %v seq %d, want true / 1", got.groups.Groups[0].Collapsed, got.groupsSeq)
	}
	f9 = layBind(t, &got, "project.groups_collapse_all")
	// Every group is collapsed now, so collapse-all expands them all.
	updated, _ = got.Update(f9)
	got = updated.(Model)
	if got.groups.Groups[0].Collapsed || got.groups.Groups[1].Collapsed {
		t.Fatal("with every group collapsed, collapse-all must expand them all")
	}
	updated, _ = got.Update(f9)
	got = updated.(Model)
	if !got.groups.Groups[0].Collapsed || !got.groups.Groups[1].Collapsed {
		t.Fatal("collapse-all must collapse every group")
	}
}

// A paste while the group-name dialog is open goes into the name — printable
// runes only, cut at the cap — and never to the pane behind it, where the
// trailing CR would run it.
func TestGroupNameDialog_PasteGoesToTheNameNeverToAPane(t *testing.T) {
	m, fake := newGroupsSidebarModel(t)
	got := grpOpenNewGroup(t, *m, 1)
	updated, cmd := got.Update(tea.PasteMsg{Content: "x\r"})
	got = updated.(Model)
	runCmd(cmd)
	if got.groupEdit.input != "x" || got.dialog != dialogGroupName {
		t.Errorf("name = %q, dialog %d — want x, still open", got.groupEdit.input, got.dialog)
	}
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgPaneInput {
			t.Fatal("a paste into the group-name dialog was sent to a pane")
		}
	}
	updated, cmd = got.Update(tea.PasteMsg{Content: strings.Repeat("é", 40)})
	got = updated.(Model)
	runCmd(cmd)
	if n := len([]rune(got.groupEdit.input)); n != maxGroupNameRunes {
		t.Errorf("input holds %d runes after an over-long paste, want %d", n, maxGroupNameRunes)
	}
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgPaneInput {
			t.Fatal("an over-long paste into the group-name dialog was sent to a pane")
		}
	}
}

// Clicks are swallowed while the dialog is open (modalSwallowsMouse): a
// right-click opens no project, group or pane menu — a project menu's New
// group… would replace the name being typed — and a left-click switches no
// project.
func TestGroupNameDialog_SwallowsClicks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		y      int
		button tea.MouseButton
	}{
		{"right-click a project row", 5, tea.MouseRight},
		{"right-click a group header", 2, tea.MouseRight},
		{"left-click a project row", 5, tea.MouseLeft},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newGroupsSidebarModel(t)
			got := grpOpenNewGroup(t, *m, 1)
			got = grpType(got, "ab")
			active := got.activeProject
			got, _ = grpPress(got, tc.y, tc.button)
			got, _ = grpRelease(got, tc.y)
			if got.ctxMenu.open() {
				t.Fatalf("a menu opened over the group-name dialog (rows %q)", grpLabels(got.ctxMenu))
			}
			if got.dialog != dialogGroupName || got.groupEdit.input != "ab" {
				t.Fatalf("dialog %d holding %q, want still open holding ab", got.dialog, got.groupEdit.input)
			}
			if got.activeProject != active || got.projectDragging || got.groupDragging {
				t.Error("a click behind the dialog acted on the sidebar")
			}
		})
	}
}

// grpDialogRows is the dialog box's height: two borders, two padding rows, and
// title, blank, name, blank, error, blank, hint. The error row is there even
// empty, so a refusal never moves the box — and any wrapped line adds a row.
const grpDialogRows = 11

// A refused name is shown INSIDE the dialog box. The status-bar editor this
// replaced flashed where the bar's right side was dropped whole at ordinary
// widths, so Enter looked like it did nothing.
func TestGroupNameDialog_RefusalIsShownInsideTheBox(t *testing.T) {
	for _, tc := range []struct {
		name, typed, existing, flash string
	}{
		{"empty name", "", "", groupNameEmptyFlash},
		{"taken wide name", strings.Repeat("构", maxGroupNameRunes), strings.Repeat("构", maxGroupNameRunes), groupNameTakenFlash},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newGroupsSidebarModel(t)
			if tc.existing != "" {
				m.groups.Groups[1].Name = tc.existing
			}
			got := grpOpenNewGroup(t, *m, 1)
			got = grpType(got, tc.typed)
			rows, _, _ := grpDialogBox(t, got.renderDialog())
			for _, r := range rows {
				if strings.Contains(r, "✗") {
					t.Fatalf("an error row before any refusal: %q", r)
				}
			}
			got, _ = grpKey(got, tea.KeyEnter) // the flash tick: not run
			if got.width != 100 || got.dialog != dialogGroupName {
				t.Fatalf("setup: width %d dialog %d, want 100 and the dialog still open", got.width, got.dialog)
			}
			rows, _, _ = grpDialogBox(t, got.renderDialog())
			if len(rows) != grpDialogRows {
				t.Fatalf("the box is %d rows, want %d — a line wrapped:\n%s", len(rows), grpDialogRows, strings.Join(rows, "\n"))
			}
			grpBoxRowWith(t, rows, "✗ "+tc.flash)
			grpBoxRowWith(t, rows, "▎")
		})
	}
}

// A 32-wide-rune name is 64 cells, wider than the box: it keeps its tail and
// caret and never wraps, down to the narrowest terminal.
func TestGroupNameDialog_LongNameNeverWrapsTheBox(t *testing.T) {
	for _, w := range []int{100, 60, minTermWidth} {
		m, _ := newGroupsSidebarModel(t)
		got := grpOpenNewGroup(t, *m, 1)
		got = grpType(got, strings.Repeat("构", maxGroupNameRunes-1)+"尾")
		got.width = w
		rows, _, width := grpDialogBox(t, got.renderDialog())
		if len(rows) != grpDialogRows {
			t.Fatalf("w=%d: the box is %d rows, want %d — a line wrapped:\n%s", w, len(rows), grpDialogRows, strings.Join(rows, "\n"))
		}
		if width > w {
			t.Errorf("w=%d: the box is %d cells wide", w, width)
		}
		grpBoxRowWith(t, rows, "尾▎")
	}
}

// Move to group… lists one row per group, so enough groups overflow the
// terminal. The list cannot open then, and says so rather than closing the
// menu silently.
func TestProjectCtxMenu_TooManyGroupsToListFlashes(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	for i := 0; i < 20; i++ {
		if _, err := m.groups.addGroup(fmt.Sprintf("extra-%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	m.height = 24
	got, _ := grpPress(*m, 1, tea.MouseRight) // L1
	if got.ctxMenu.projectID == "" {
		t.Fatal("setup: no project menu")
	}
	got, _ = grpChoose(t, got, grpMenuRow(t, got.ctxMenu, ctxActGroupList, "")) // the flash tick: not run
	if got.ctxMenu.open() {
		t.Fatalf("a %d-group list opened on a 24-row terminal", len(got.groups.Groups))
	}
	if got.flashText != groupListTooTallFlash {
		t.Fatalf("flash = %q, want %q", got.flashText, groupListTooTallFlash)
	}
}

// Right-clicking a PANE row in the sidebar opens no pane menu over the
// group-name dialog — and focuses nothing.
func TestSidebarPaneMenu_SwallowedWhileTheGroupNameDialogIsOpen(t *testing.T) {
	m, _ := newGroupsSidebarModel(t)
	rows, _ := m.sidebarRows(22)
	if r := rows[11]; r.kind != sidebarRowPane || r.paneID != "p2" {
		t.Fatalf("setup: row 11 = %+v, want pane p2's row", r)
	}
	if tab := m.activeTabModel(); tab == nil || tab.ActivePane == "p2" {
		t.Fatal("setup: p2 must not start focused, or the focus check below is vacuous")
	}
	got := grpOpenNewGroup(t, *m, 1)
	got = grpType(got, "ab")
	got, _ = grpPress(got, 11, tea.MouseRight)
	if got.ctxMenu.open() {
		t.Fatalf("a pane menu opened over the group-name dialog (rows %q)", grpLabels(got.ctxMenu))
	}
	if got.dialog != dialogGroupName || got.groupEdit.input != "ab" {
		t.Fatalf("dialog %d holding %q, want still open holding ab", got.dialog, got.groupEdit.input)
	}
	if tab := got.activeTabModel(); tab == nil || tab.ActivePane == "p2" {
		t.Error("the refused right-click still focused the pane")
	}
}

// renderCtxMenu caps the box at ctxMenuTitleCap, so a label can be wider than
// the box. It is cut, never allowed to make the pad count negative — which
// panicked the TUI — and every row keeps the box's width.
func TestRenderCtxMenu_OverWideLabelIsCutNotPanicking(t *testing.T) {
	t.Parallel()
	for _, label := range []string{strings.Repeat("W", 40), strings.Repeat("构", 40)} {
		s := ctxMenuState{projectID: "p", title: "t", cursor: 0, items: []ctxMenuItem{
			{id: ctxActRenameProject, label: label, enabled: true},
			{id: ctxActDestroyProject, label: label, enabled: true},
		}}
		w, _ := s.boxSize()
		out := renderCtxMenu(s)
		for i, line := range strings.Split(out, "\n") {
			if got := lipgloss.Width(line); got != w {
				t.Errorf("line %d is %d cells, want the box's %d", i, got, w)
			}
		}
		if !strings.Contains(stripANSI(out), "…") {
			t.Error("the over-wide label was not cut with an ellipsis")
		}
	}
}
