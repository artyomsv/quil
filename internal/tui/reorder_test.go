package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// dragSlot is the one rule every reorder drag in the TUI shares — tab bar
// (columns) and sidebar (rows) alike. These cases pin the hysteresis it exists
// for: the dragged item does not move until the pointer passes the MIDDLE of
// the neighbour it is over, and a pointer that lands in the near half of a
// neighbour further away slots in just before it rather than on top of it.
func TestDragSlot(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                     string
		from, target, pos, start int
		size                     int
		want                     int
	}{
		// Same item: nothing to do, whatever the position.
		{"over itself", 1, 1, 7, 5, 4, 1},
		// Moving to a higher index: the near (left/top) half does not move it,
		// the far half does.
		{"next, near half", 0, 1, 10, 10, 4, 0},
		{"next, just before middle", 0, 1, 11, 10, 4, 0},
		{"next, at middle", 0, 1, 12, 10, 4, 1},
		{"next, far half", 0, 1, 13, 10, 4, 1},
		// Moving to a lower index: mirror image.
		{"prev, near half", 3, 2, 13, 10, 4, 3},
		{"prev, near half edge", 3, 2, 12, 10, 4, 3},
		{"prev, far half edge", 3, 2, 11, 10, 4, 2},
		{"prev, far half", 3, 2, 10, 10, 4, 2},
		// A one-cell item has no near half: hovering it always moves.
		{"next, single cell", 0, 1, 10, 10, 1, 1},
		{"prev, single cell", 2, 1, 10, 10, 1, 1},
		// Odd sizes: the exact middle cell counts as "past" in both directions.
		{"next, odd size middle", 0, 1, 11, 10, 3, 1},
		{"prev, odd size middle", 2, 1, 11, 10, 3, 1},
		// A fast pointer skipping several items: landing in the near half of a
		// distant neighbour slots in just before it.
		{"skip ahead, near half", 0, 3, 30, 30, 6, 2},
		{"skip ahead, far half", 0, 3, 33, 30, 6, 3},
		{"skip back, near half", 3, 0, 5, 0, 6, 1},
		{"skip back, far half", 3, 0, 1, 0, 6, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := dragSlot(tc.from, tc.target, tc.pos, tc.start, tc.size); got != tc.want {
				t.Errorf("dragSlot(from=%d, target=%d, pos=%d, start=%d, size=%d) = %d, want %d",
					tc.from, tc.target, tc.pos, tc.start, tc.size, got, tc.want)
			}
		})
	}
}

// tabBarX converts a bar-local column of the tab at idx into the SCREEN column
// a mouse event carries. offset is how far into the tab the column sits.
func tabBarX(t *testing.T, m *Model, idx, offset int) int {
	t.Helper()
	for _, s := range m.tabSpans() {
		if s.index == idx {
			return m.projectSidebarWidth() + s.start + offset
		}
	}
	t.Fatalf("tab %d is not visible in the bar", idx)
	return -1
}

func tabSpanWidth(t *testing.T, m *Model, idx int) int {
	t.Helper()
	for _, s := range m.tabSpans() {
		if s.index == idx {
			return s.width
		}
	}
	t.Fatalf("tab %d is not visible in the bar", idx)
	return -1
}

func reorderTabMessages(sent []*ipc.Message) []ipc.ReorderTabPayload {
	var out []ipc.ReorderTabPayload
	for _, msg := range sent {
		if msg.Type != ipc.MsgReorderTab {
			continue
		}
		var p ipc.ReorderTabPayload
		if err := msg.DecodePayload(&p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// TestTabSpansMatchThePaintedBarWhenTheBarOverflows is the test that can
// actually fail.
//
// Its sibling below compares hitTestTab with tabSpans, and hitTestTab now
// DELEGATES to tabSpanAt — so that one compares tabSpans with itself and is
// tautological. It is kept only as a cheap guard on the delegation surviving.
// This one compares tabSpans against the PAINTED row, which is the claim that
// matters, and it does it in the OVERFLOW branch: the fit-everything branch is
// a straight walk, while the overflow branch decides which tabs get painted at
// all, and that is where a divergence would hide.
func TestTabSpansMatchThePaintedBarWhenTheBarOverflows(t *testing.T) {
	t.Parallel()
	m := newModelForTest([]string{"Aaaaaaaaaa", "Bbbbbbbbbb", "Cccccccccc", "Dddddddddd", "Eeeeeeeeee"}, 2)
	// 60 columns, NOT 40. At 40 the reserve leaves room for the active tab
	// alone, so exactly one span is painted, every separator is skipped and the
	// cursor arithmetic this test exists to check is never executed — verified
	// by mutation: desyncing the separator from renderTabBar's join survived a
	// 40-column fixture and fails a 60-column one. 60 paints three of five.
	m.width, m.height = 60, 40

	spans := m.tabSpans()
	if len(spans) == len(m.curTabs()) {
		t.Fatal("fixture does not overflow — this test cannot discriminate, " +
			"widen the tabs or narrow the terminal")
	}
	if len(spans) < 2 {
		t.Fatalf("fixture paints %d tab(s); at least 2 are needed or the "+
			"separator arithmetic is never exercised", len(spans))
	}

	row0 := stripANSI(m.renderTabBar())
	for _, s := range spans {
		want := fmt.Sprintf("%d:%s", s.index+1, m.curTabs()[s.index].Name)
		got := strings.Index(row0, want)
		if got < 0 {
			t.Errorf("tab %d is in tabSpans but not painted in the bar: %q", s.index, row0)
			continue
		}
		if got < s.start || got >= s.start+s.width {
			t.Errorf("tab %d is painted at column %d but tabSpans puts it at [%d,%d)",
				s.index, got, s.start, s.start+s.width)
		}
	}

	// The overflow indicator has to agree with the same set, or the bar tells
	// the user a different number of tabs is hidden than actually is.
	if hidden := len(m.curTabs()) - len(spans); !strings.Contains(row0, fmt.Sprintf("«%d more»", hidden)) {
		t.Errorf("bar does not report %d hidden tabs: %q", hidden, row0)
	}
}

// TestTabSpansAgreeWithHitTestTab: hitTestTab is the mouse's authority on which
// tab is under a column and tabSpans is what the drag reads its geometry from.
// Every column of every span must hit-test to that span's tab, and the
// separator columns between spans to none.
//
// NOTE: hitTestTab delegates to tabSpanAt, so this is a guard on the delegation
// and not an independent check of the geometry. The test above is the one that
// can catch a real divergence.
func TestTabSpansAgreeWithHitTestTab(t *testing.T) {
	t.Parallel()
	m := newModelForTest([]string{"A", "Bb", "Ccccccccccc", "D"}, 2)
	m.width, m.height = 100, 40
	spans := m.tabSpans()
	if len(spans) != 4 {
		t.Fatalf("tabSpans = %d spans, want 4 (all fit in 100 columns)", len(spans))
	}
	for _, s := range spans {
		for x := s.start; x < s.start+s.width; x++ {
			if got := m.hitTestTab(x); got != s.index {
				t.Errorf("hitTestTab(%d) = %d, want %d (span %+v)", x, got, s.index, s)
			}
		}
		if got := m.hitTestTab(s.start + s.width); got != -1 && s.index != 3 {
			t.Errorf("separator after tab %d hit-tests to %d, want -1", s.index, got)
		}
	}
}

// A narrow tab dragged over a wide one used to flip back and forth on every
// motion event: the swap put the wide tab under the pointer, the next event saw
// a different tab there and swapped again. The midpoint rule is what stops it.
func TestTabBarDragWaitsForTheMidpointAndDoesNotFlipBack(t *testing.T) {
	t.Parallel()
	fake := newFakeConn()
	m := newModelForTest([]string{"A", "B", "Cccccccccccccccc"}, 0)
	m.client = fake
	m.notifications = NewNotificationCenter(30, 200)
	m.width, m.height = 100, 40

	press := func(mm Model, x int) Model {
		updated, _ := mm.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseLeft})
		return updated.(Model)
	}
	drag := func(mm Model, x int) (Model, tea.Cmd) {
		updated, cmd := mm.Update(tea.MouseMotionMsg{X: x, Y: 0, Button: tea.MouseLeft})
		return updated.(Model), cmd
	}

	got := press(m, tabBarX(t, &m, 1, 1))
	if got.tabDragFromIdx != 1 {
		t.Fatalf("tabDragFromIdx = %d after pressing tab 1, want 1", got.tabDragFromIdx)
	}
	// Geometry AFTER the press: the active marker changed tab widths.
	nearX := tabBarX(t, &got, 2, 0)
	midX := tabBarX(t, &got, 2, tabSpanWidth(t, &got, 2)/2)

	got, cmd := drag(got, nearX)
	if names := strings.Join(tabNames(t, got), ","); names != "A,B,Cccccccccccccccc" {
		t.Fatalf("order after entering the NEAR half of tab 2 = %s, want unchanged", names)
	}
	if cmd != nil {
		t.Fatal("a motion that does not cross the midpoint must send nothing")
	}

	got, cmd = drag(got, midX)
	if names := strings.Join(tabNames(t, got), ","); names != "A,Cccccccccccccccc,B" {
		t.Fatalf("order after passing the midpoint = %s, want A,Cccccccccccccccc,B", names)
	}
	if got.tabDragFromIdx != 2 || got.activeTabIdx() != 2 {
		t.Fatalf("drag index / active = %d / %d after the move, want 2 / 2", got.tabDragFromIdx, got.activeTabIdx())
	}
	if cmd == nil {
		t.Fatal("a move must tell the daemon")
	}
	cmd()
	msgs := reorderTabMessages(fake.sent)
	if len(msgs) != 1 || msgs[0].NewIndex != 2 || msgs[0].TabID != got.curTabs()[2].ID {
		t.Fatalf("reorder_tab messages = %+v, want one moving B to index 2", msgs)
	}

	// The pointer has not moved. The wide tab now sits under it, but the
	// dragged tab must stay where the user put it.
	got, cmd = drag(got, midX)
	if names := strings.Join(tabNames(t, got), ","); names != "A,Cccccccccccccccc,B" {
		t.Fatalf("order flipped back on a stationary pointer: %s", names)
	}
	if cmd != nil {
		t.Fatal("a stationary pointer must send nothing")
	}
}

// The keyboard slides the active tab one slot and reports the move the same
// way a drag does. At the end of the strip the key is a no-op with no traffic.
func TestTabMoveKeysSlideTheActiveTab(t *testing.T) {
	t.Parallel()
	fake := newFakeConn()
	m := newModelForTest([]string{"A", "B", "C"}, 0)
	m.client = fake
	m.notifications = NewNotificationCenter(30, 200)
	m.width, m.height = 100, 40

	right := tea.KeyPressMsg{Code: tea.KeyPgDown, Mod: tea.ModAlt | tea.ModShift}
	left := tea.KeyPressMsg{Code: tea.KeyPgUp, Mod: tea.ModAlt | tea.ModShift}

	updated, cmd := m.handleKey(right)
	got := updated.(Model)
	if names := strings.Join(tabNames(t, got), ","); names != "B,A,C" {
		t.Fatalf("order after move right = %s, want B,A,C", names)
	}
	if got.activeTabIdx() != 1 {
		t.Fatalf("active tab = %d after move right, want 1 (follows the moved tab)", got.activeTabIdx())
	}
	if cmd == nil {
		t.Fatal("move right must tell the daemon")
	}
	cmd()
	msgs := reorderTabMessages(fake.sent)
	if len(msgs) != 1 || msgs[0].NewIndex != 1 || msgs[0].TabID != got.curTabs()[1].ID {
		t.Fatalf("reorder_tab messages = %+v, want one moving A to index 1", msgs)
	}

	updated, cmd = got.handleKey(left)
	got = updated.(Model)
	if names := strings.Join(tabNames(t, got), ","); names != "A,B,C" {
		t.Fatalf("order after move left = %s, want A,B,C", names)
	}
	if cmd == nil {
		t.Fatal("move left must tell the daemon")
	}
	cmd()

	before := len(reorderTabMessages(fake.sent))
	updated, cmd = got.handleKey(left)
	got = updated.(Model)
	if names := strings.Join(tabNames(t, got), ","); names != "A,B,C" {
		t.Fatalf("order after move left at the edge = %s, want unchanged", names)
	}
	if cmd != nil {
		cmd()
	}
	if after := len(reorderTabMessages(fake.sent)); after != before {
		t.Fatalf("an edge no-op sent %d reorder_tab message(s)", after-before)
	}
}

func projectNames(m Model) string {
	out := make([]string, 0, len(m.projects))
	for _, p := range m.projects {
		out = append(out, p.Name)
	}
	return strings.Join(out, ",")
}

// sentReorderProject is one reorder_project the fake conn saw, with the
// destination it was stamped for ("" = local).
type sentReorderProject struct {
	ipc.ReorderProjectPayload
	dest string
}

func reorderProjectMessages(t *testing.T, sent []*ipc.Message) []sentReorderProject {
	t.Helper()
	var out []sentReorderProject
	for _, msg := range sent {
		if msg.Type != ipc.MsgReorderProject {
			continue
		}
		var p ipc.ReorderProjectPayload
		if err := msg.DecodePayload(&p); err != nil {
			t.Fatalf("decode reorder_project: %v", err)
		}
		dest, _ := routeDest(msg.Origin)
		out = append(out, sentReorderProject{p, dest})
	}
	return out
}

// The keyboard slides the ACTIVE project one slot in the sidebar and the active
// marker follows it. The edge is a silent no-op with no traffic.
func TestProjectMoveKeysSlideTheActiveProject(t *testing.T) {
	t.Parallel()
	fake := newFakeConn()
	m := newModelForTest([]string{"T"}, 0)
	m.client = fake
	m.notifications = NewNotificationCenter(30, 200)
	m.width, m.height = 100, 40
	m.projects = []*ProjectModel{{ID: "pa", Name: "A"}, {ID: "pb", Name: "B"}, {ID: "pc", Name: "C"}}
	m.activeProject = 1

	down := tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModAlt | tea.ModShift}
	up := tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt | tea.ModShift}

	updated, cmd := m.handleKey(down)
	got := updated.(Model)
	if names := projectNames(got); names != "A,C,B" {
		t.Fatalf("order after move down = %s, want A,C,B", names)
	}
	if got.activeProject != 2 {
		t.Fatalf("activeProject = %d after move down, want 2 (follows the moved project)", got.activeProject)
	}
	if cmd == nil {
		t.Fatal("move down must tell the daemon")
	}
	cmd()
	msgs := reorderProjectMessages(t, fake.sent)
	if len(msgs) != 1 || msgs[0].ProjectID != "pb" || msgs[0].NewIndex != 2 || msgs[0].dest != "" {
		t.Fatalf("reorder_project messages = %+v, want one moving pb to index 2 on the local daemon", msgs)
	}

	for i, want := range []string{"A,B,C", "B,A,C"} {
		updated, cmd = got.handleKey(up)
		got = updated.(Model)
		if names := projectNames(got); names != want {
			t.Fatalf("order after move up #%d = %s, want %s", i+1, names, want)
		}
		if cmd == nil {
			t.Fatalf("move up #%d must tell the daemon", i+1)
		}
		cmd()
	}
	if got.activeProject != 0 {
		t.Fatalf("activeProject = %d after two moves up, want 0", got.activeProject)
	}

	before := len(reorderProjectMessages(t, fake.sent))
	updated, cmd = got.handleKey(up)
	got = updated.(Model)
	if names := projectNames(got); names != "B,A,C" {
		t.Fatalf("order after move up at the top = %s, want unchanged", names)
	}
	if cmd != nil {
		cmd()
	}
	if after := len(reorderProjectMessages(t, fake.sent)); after != before {
		t.Fatalf("an edge no-op sent %d reorder_project message(s)", after-before)
	}
}

// The sidebar interleaves projects from several daemons, but each daemon only
// knows its own. The index the daemon is told is the project's position among
// THAT daemon's projects, and the message is stamped for that daemon.
func TestProjectMoveTellsEachDaemonItsOwnIndex(t *testing.T) {
	t.Parallel()
	fake := newFakeConn()
	m := newModelForTest([]string{"T"}, 0)
	m.client = fake
	m.notifications = NewNotificationCenter(30, 200)
	m.width, m.height = 100, 40
	m.projects = []*ProjectModel{
		{ID: "l1", Name: "L1"},
		{ID: "r1", Name: "R1", Dest: "gpu01"},
		{ID: "l2", Name: "L2"},
	}
	m.activeProject = 2
	up := tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt | tea.ModShift}

	updated, cmd := m.handleKey(up)
	got := updated.(Model)
	if names := projectNames(got); names != "L1,L2,R1" {
		t.Fatalf("order = %s, want L1,L2,R1", names)
	}
	cmd()
	msgs := reorderProjectMessages(t, fake.sent)
	if len(msgs) != 1 || msgs[0].ProjectID != "l2" || msgs[0].NewIndex != 1 || msgs[0].dest != "" {
		t.Fatalf("messages = %+v, want l2 to index 1 (second LOCAL project) on the local daemon", msgs)
	}

	// Now move the remote project above L2: it is the only project on gpu01,
	// so its index there is 0 whatever the sidebar shows.
	got.activeProject = 2
	updated, cmd = got.handleKey(up)
	got = updated.(Model)
	if names := projectNames(got); names != "L1,R1,L2" {
		t.Fatalf("order = %s, want L1,R1,L2", names)
	}
	cmd()
	msgs = reorderProjectMessages(t, fake.sent)
	if len(msgs) != 2 || msgs[1].ProjectID != "r1" || msgs[1].NewIndex != 0 || msgs[1].dest != "gpu01" {
		t.Fatalf("messages = %+v, want a second one moving r1 to index 0 on gpu01", msgs)
	}
}

// A project that is not in m.projects has no rank among its daemon's
// siblings, and the counting loop would fall off the end leaving an index one
// past that daemon's last project — a legal ordinal the daemon clamps and acts
// on, so a lookup failure would silently move a project to the end.
//
// Called DIRECTLY, unlike the drag and keyboard tests, and deliberately: both
// real call sites read p out of m.projects immediately beforehand, so the miss
// is unreachable through Update. That makes this a guard on the guard — the
// alternative is an untested branch that can be deleted with the suite green,
// which is how it was found.
func TestSendReorderProjectRefusesAProjectNotInTheList(t *testing.T) {
	t.Parallel()
	fake := newFakeConn()
	m := newModelForTest([]string{"T"}, 0)
	m.client = fake
	m.projects = []*ProjectModel{{ID: "pa", Name: "A"}, {ID: "pb", Name: "B"}}

	stray := &ProjectModel{ID: "pc", Name: "C"}
	if cmd := m.sendReorderProject(stray); cmd != nil {
		cmd()
		t.Fatal("sendReorderProject returned a command for a project absent from m.projects; " +
			"the index would be one past the daemon's last project")
	}
	if msgs := reorderProjectMessages(t, fake.sent); len(msgs) != 0 {
		t.Fatalf("a stray project produced %d reorder_project message(s): %+v", len(msgs), msgs)
	}

	// Control: the same call for a project that IS in the list must still send,
	// or this test would pass against a function that refuses everything.
	if cmd := m.sendReorderProject(m.projects[1]); cmd == nil {
		t.Fatal("sendReorderProject refused a project that is in the list")
	} else {
		cmd()
	}
	if msgs := reorderProjectMessages(t, fake.sent); len(msgs) != 1 || msgs[0].ProjectID != "pb" {
		t.Fatalf("control send = %+v, want one message for pb", msgs)
	}
}

// Dragging a project row in the sidebar follows the same midpoint rule as the
// tab bar, measured in rows. A remote project is two rows tall (name + host),
// so its lower row is the NEAR half when dragging upward over it.
func TestSidebarProjectDragReordersPastTheMidpoint(t *testing.T) {
	fake := newFakeConn()
	m := newSplitDragTestModel(t)
	m.client = fake
	m.sidebarOpen = true
	m.sidebarWidth = 22
	m.projects[0].ID, m.projects[0].Name = "pa", "alpha"
	m.projects = append(m.projects,
		&ProjectModel{ID: "pb", Name: "beta", Dest: "gpu01"},
		&ProjectModel{ID: "pc", Name: "gamma"},
	)
	// Rows: 0 PROJECTS, 1 alpha, 2 beta, 3 beta's host, 4 gamma.

	updated, _ := m.Update(tea.MouseClickMsg{X: 3, Y: 4, Button: tea.MouseLeft})
	got := updated.(Model)
	if got.activeProject != 2 {
		t.Fatalf("activeProject = %d after pressing gamma, want 2", got.activeProject)
	}
	if !got.projectDragging || got.projectDragIdx != 2 {
		t.Fatalf("drag = (%v, %d) after pressing a project row, want (true, 2)", got.projectDragging, got.projectDragIdx)
	}

	updated, cmd := got.Update(tea.MouseMotionMsg{X: 3, Y: 3, Button: tea.MouseLeft})
	got = updated.(Model)
	if names := projectNames(got); names != "alpha,beta,gamma" {
		t.Fatalf("order after entering beta's NEAR (lower) row = %s, want unchanged", names)
	}
	if cmd != nil {
		t.Fatal("no move, no traffic")
	}

	updated, cmd = got.Update(tea.MouseMotionMsg{X: 3, Y: 2, Button: tea.MouseLeft})
	got = updated.(Model)
	if names := projectNames(got); names != "alpha,gamma,beta" {
		t.Fatalf("order after crossing beta's midpoint = %s, want alpha,gamma,beta", names)
	}
	if got.activeProject != 1 || got.projectDragIdx != 1 {
		t.Fatalf("active / drag index = %d / %d after the move, want 1 / 1", got.activeProject, got.projectDragIdx)
	}
	if cmd == nil {
		t.Fatal("a move must tell the daemon")
	}
	cmd()
	msgs := reorderProjectMessages(t, fake.sent)
	if len(msgs) != 1 || msgs[0].ProjectID != "pc" || msgs[0].NewIndex != 1 || msgs[0].dest != "" {
		t.Fatalf("messages = %+v, want pc to index 1 on the local daemon", msgs)
	}

	// Stationary pointer, now over the dragged row itself: nothing moves.
	updated, cmd = got.Update(tea.MouseMotionMsg{X: 3, Y: 2, Button: tea.MouseLeft})
	got = updated.(Model)
	if names := projectNames(got); names != "alpha,gamma,beta" {
		t.Fatalf("order flipped on a stationary pointer: %s", names)
	}
	if cmd != nil {
		t.Fatal("a stationary pointer must send nothing")
	}

	updated, _ = got.Update(tea.MouseReleaseMsg{X: 3, Y: 2, Button: tea.MouseLeft})
	got = updated.(Model)
	if got.projectDragging {
		t.Fatal("release must end the project drag")
	}
}

// A drag whose pointer wanders off the project rows must leave the order alone
// and stay armed. Without the row-kind guard, a pane row's `index` is a pane
// ordinal and a chrome row's is the zero value — both would be read as a
// project index and reorder something the pointer is nowhere near.
func TestSidebarProjectDragIgnoresNonProjectRowsAndColumnsOutsideTheStrip(t *testing.T) {
	fake := newFakeConn()
	m := newSplitDragTestModel(t)
	m.client = fake
	m.sidebarOpen = true
	m.sidebarWidth = 22
	m.projects[0].ID, m.projects[0].Name = "pa", "alpha"
	m.projects = append(m.projects,
		&ProjectModel{ID: "pb", Name: "beta"},
		&ProjectModel{ID: "pc", Name: "gamma"},
	)
	// Rows: 0 PROJECTS, 1 alpha, 2 beta, 3 gamma, 4 blank, 5 PANES, 6 tab, 7+ panes.

	updated, _ := m.Update(tea.MouseClickMsg{X: 3, Y: 3, Button: tea.MouseLeft})
	got := updated.(Model)
	if !got.projectDragging || got.projectDragIdx != 2 {
		t.Fatalf("drag = (%v, %d) after pressing gamma, want (true, 2)", got.projectDragging, got.projectDragIdx)
	}

	for _, tc := range []struct {
		name string
		x, y int
	}{
		{"PROJECTS heading", 3, 0},
		{"blank separator", 3, 4},
		{"PANES heading", 3, 5},
		{"a pane row", 3, 7},
		{"a column outside the strip", m.sidebarWidth + 4, 2},
		{"a negative column", -1, 2},
		{"the status bar row", 3, m.height - 1},
	} {
		updated, cmd := got.Update(tea.MouseMotionMsg{X: tc.x, Y: tc.y, Button: tea.MouseLeft})
		next := updated.(Model)
		if names := projectNames(next); names != "alpha,beta,gamma" {
			t.Errorf("%s (%d,%d) reordered to %s, want unchanged", tc.name, tc.x, tc.y, names)
		}
		if cmd != nil {
			t.Errorf("%s (%d,%d) sent traffic without a move", tc.name, tc.x, tc.y)
		}
		if !next.projectDragging {
			t.Errorf("%s (%d,%d) cancelled the drag; it must stay armed so the "+
				"pointer can wander and come back", tc.name, tc.x, tc.y)
		}
		got = next
	}
	if msgs := reorderProjectMessages(t, fake.sent); len(msgs) != 0 {
		t.Fatalf("non-actionable rows produced %d reorder_project message(s): %+v", len(msgs), msgs)
	}
}

// An offline project's destination has no connection and a synthetic one's ID
// exists only in this client, so Router.Send would DROP either and log it as
// delivered. The reorder still happens locally — the sidebar is the user's own
// view — but nothing may go on the wire.
func TestProjectMoveDoesNotReportAnOfflineOrSyntheticProject(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		project *ProjectModel
	}{
		{"offline", &ProjectModel{ID: "pb", Name: "B", Dest: "gpu01", Offline: &OfflineState{}}},
		{"synthetic", &ProjectModel{ID: interimProjectID, Name: "B"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := newFakeConn()
			m := newModelForTest([]string{"T"}, 0)
			m.client = fake
			m.notifications = NewNotificationCenter(30, 200)
			m.width, m.height = 100, 40
			m.projects = []*ProjectModel{{ID: "pa", Name: "A"}, tc.project}
			m.activeProject = 1

			updated, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt | tea.ModShift})
			got := updated.(Model)
			if names := projectNames(got); names != "B,A" {
				t.Fatalf("order = %s, want B,A — the local move must still happen", names)
			}
			if got.activeProject != 0 {
				t.Fatalf("activeProject = %d, want 0 (it follows the moved project)", got.activeProject)
			}
			if cmd != nil {
				cmd()
			}
			if msgs := reorderProjectMessages(t, fake.sent); len(msgs) != 0 {
				t.Fatalf("a %s project produced %d reorder_project message(s): %+v",
					tc.name, len(msgs), msgs)
			}
		})
	}
}

// twoTabSidebarModel is newSplitDragTestModel (tab "T": panes p1, p2) plus a
// second tab "U" holding one pane, with the sidebar open. Sidebar rows:
//
//	0 PROJECTS  1 project  2 (blank)  3 PANES
//	4 T heading 5 p1  6 p2
//	7 (blank)   8 U heading  9 p3
func twoTabSidebarModel(t *testing.T) (*Model, *fakeConn) {
	t.Helper()
	fake := newFakeConn()
	m := newSplitDragTestModel(t)
	m.client = fake
	m.sidebarOpen = true
	m.sidebarWidth = 22
	u := NewTabModel("tab-u", "U")
	u.Root = NewLeaf(NewPaneModel("p3", 1024))
	u.ActivePane = "p3"
	m.appendTab(u)
	return m, fake
}

// A tab heading in the sidebar used to be inert. Clicking it switches to that
// tab, the same way a click on the tab bar does.
func TestSidebarTabHeadingClickSwitchesTab(t *testing.T) {
	m, fake := twoTabSidebarModel(t)
	updated, cmd := m.Update(tea.MouseClickMsg{X: 3, Y: 8, Button: tea.MouseLeft})
	got := updated.(Model)
	if got.activeTabIdx() != 1 {
		t.Fatalf("activeTab = %d after clicking U's heading, want 1", got.activeTabIdx())
	}
	if !got.sidebarTabDragging || got.sidebarTabDragIdx != 1 {
		t.Fatalf("drag = (%v, %d) after pressing a tab heading, want (true, 1)", got.sidebarTabDragging, got.sidebarTabDragIdx)
	}
	if cmd != nil {
		cmd()
	}
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgSwitchTab {
			return
		}
	}
	t.Fatal("a click on a sidebar tab heading must reach switchTab so the daemon's active tab follows")
}

// Dragging a tab heading up or down the sidebar reorders the tab. A tab group
// is its heading plus its pane rows, so groups differ in height exactly as tabs
// differ in width on the bar — and the same midpoint rule keeps the drag from
// flip-flopping when a short group crosses a tall one.
func TestSidebarTabDragReordersPastTheMidpoint(t *testing.T) {
	m, fake := twoTabSidebarModel(t)
	updated, _ := m.Update(tea.MouseClickMsg{X: 3, Y: 8, Button: tea.MouseLeft})
	got := updated.(Model)

	// T's group spans rows 4-6 (size 3). Moving UP over it, row 6 is the near
	// half and does not move the tab; the middle row 5 counts as past the
	// midpoint (dragSlot's odd-size rule) and does.
	updated, cmd := got.Update(tea.MouseMotionMsg{X: 3, Y: 6, Button: tea.MouseLeft})
	got = updated.(Model)
	if names := strings.Join(tabNames(t, got), ","); names != "T,U" {
		t.Fatalf("order after entering T's lower row = %s, want unchanged", names)
	}
	if cmd != nil {
		t.Fatal("no move, no traffic")
	}

	updated, cmd = got.Update(tea.MouseMotionMsg{X: 3, Y: 5, Button: tea.MouseLeft})
	got = updated.(Model)
	if names := strings.Join(tabNames(t, got), ","); names != "U,T" {
		t.Fatalf("order after crossing T's midpoint = %s, want U,T", names)
	}
	if got.activeTabIdx() != 0 || got.sidebarTabDragIdx != 0 {
		t.Fatalf("active / drag index = %d / %d after the move, want 0 / 0", got.activeTabIdx(), got.sidebarTabDragIdx)
	}
	if cmd == nil {
		t.Fatal("a move must tell the daemon")
	}
	cmd()
	msgs := reorderTabMessages(fake.sent)
	if len(msgs) != 1 || msgs[0].TabID != "tab-u" || msgs[0].NewIndex != 0 {
		t.Fatalf("reorder_tab messages = %+v, want one moving tab-u to index 0", msgs)
	}

	// Rows now: 4 U heading, 5 p3, 6 (blank), 7 T heading, 8 p1, 9 p2. A
	// stationary pointer sits on U's own pane row; a pointer on the blank
	// separator is over nothing. Neither moves anything.
	for _, y := range []int{5, 6} {
		updated, cmd = got.Update(tea.MouseMotionMsg{X: 3, Y: y, Button: tea.MouseLeft})
		got = updated.(Model)
		if names := strings.Join(tabNames(t, got), ","); names != "U,T" {
			t.Fatalf("order changed at row %d with nothing to cross: %s", y, names)
		}
		if cmd != nil {
			t.Fatalf("row %d sent traffic without a move", y)
		}
	}

	updated, _ = got.Update(tea.MouseReleaseMsg{X: 3, Y: 6, Button: tea.MouseLeft})
	got = updated.(Model)
	if got.sidebarTabDragging {
		t.Fatal("release must end the sidebar tab drag")
	}
}

// The blank separator between tab groups belongs to no tab. Its zero-value
// tabIdx is 0, so a drag that reads it without checking inTab computes a slot
// against TAB 0's extent — which is invisible while the dragged tab IS tab 0,
// because dragSlot then returns `from` for an unrelated reason. This drags the
// THIRD tab, where the guard is the only thing that can stop the move.
func TestSidebarTabDragIgnoresTheBlankRowBetweenGroups(t *testing.T) {
	fake := newFakeConn()
	m, _ := twoTabSidebarModel(t)
	m.client = fake
	v := NewTabModel("tab-v", "V")
	v.Root = NewLeaf(NewPaneModel("p4", 1024))
	v.ActivePane = "p4"
	m.appendTab(v)
	// Rows: 0 PROJECTS 1 project 2 blank 3 PANES
	//       4 T 5 p1 6 p2   7 blank   8 U 9 p3   10 blank   11 V 12 p4
	updated, _ := m.Update(tea.MouseClickMsg{X: 3, Y: 11, Button: tea.MouseLeft})
	got := updated.(Model)
	if got.sidebarTabDragIdx != 2 {
		t.Fatalf("drag index = %d after pressing V's heading, want 2", got.sidebarTabDragIdx)
	}

	// Row 7 is the blank between T's group and U's group. It carries tabIdx 0.
	// Tab 0 spans rows 4-6, and row 7 is past its end — so an unguarded read
	// computes "past tab 0's midpoint" and slides V to index 0.
	updated, cmd := got.Update(tea.MouseMotionMsg{X: 3, Y: 7, Button: tea.MouseLeft})
	got = updated.(Model)
	if names := strings.Join(tabNames(t, got), ","); names != "T,U,V" {
		t.Fatalf("a blank separator row reordered the tabs to %s, want T,U,V", names)
	}
	if cmd != nil {
		t.Fatal("a blank separator row must send nothing")
	}
	if !got.sidebarTabDragging {
		t.Fatal("a blank row must not cancel the drag")
	}
	if msgs := reorderTabMessages(fake.sent); len(msgs) != 0 {
		t.Fatalf("blank row produced %d reorder_tab message(s): %+v", len(msgs), msgs)
	}
}

// A git row is part of its tab's group but carries no kind, so only `inTab`
// ties it to its tab. Dropping that flag would shorten the measured group by a
// row and move the midpoint — so the drag would fire a row early or late.
func TestSidebarTabGroupSpanIncludesTheGitRow(t *testing.T) {
	m, _ := twoTabSidebarModel(t)
	// A branch is what makes gitRow render at all.
	m.curTabs()[0].Leaves()[0].GitBranch = "master"

	rows := m.sidebarVisibleRows(m.projectSidebarWidth(), m.sidebarContentHeight())
	var gitRows int
	for _, r := range rows {
		if r.kind == "" && r.inTab && strings.Contains(stripANSI(r.text), "master") {
			gitRows++
		}
	}
	if gitRows != 1 {
		t.Fatalf("fixture rendered %d git rows, want exactly 1 — the test cannot "+
			"discriminate without one", gitRows)
	}

	start, size := tabGroupSpanIn(rows, 0)
	// Heading + 2 pane rows + 1 git row.
	if size != 4 {
		t.Errorf("tab 0's group spans %d rows from %d, want 4 (heading, two panes, one git row) "+
			"— a git row missing its inTab flag shortens the group and moves the drag midpoint",
			size, start)
	}
}
