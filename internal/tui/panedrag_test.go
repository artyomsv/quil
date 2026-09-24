package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/artyomsv/quil/internal/ipc"
)

// pdMod is the chord that arms a pane drag (Task 3 Step 0 decides it).
var pdMod = tea.ModAlt

var pdTabs = []mpTab{{"tab-a", []string{"p1", "p2", "p3"}}, {"tab-b", []string{"p4"}}, {"tab-c", []string{"p5"}}}

// newPaneDragTestModel builds, through a broadcast, project proj-mp with tab-a
// (ACTIVE: p1|(p2/p3)), tab-b (p4) and tab-c (p5) at 100x40, no project
// sidebar. Screen rects: p1 (0,1,50,38), p2 (50,1,50,19), p3 (50,20,50,19).
func newPaneDragTestModel(t *testing.T) Model {
	t.Helper()
	tree := &LayoutNode{Split: SplitHorizontal, Ratio: 0.5, Left: NewLeaf(newTestPane("p1")),
		Right: &LayoutNode{Split: SplitVertical, Ratio: 0.5, Left: NewLeaf(newTestPane("p2")), Right: NewLeaf(newTestPane("p3"))}}
	m := newMovePaneModel(t, 100, 40)
	m = mpApply(t, m, withLayout(t, mpState("tab-a", pdTabs...), "tab-a", tree))
	if got := mpTreeOf(t, &m, "tab-a"); got != "(p1|(p2/p3))" {
		t.Fatalf("fixture: tab-a tree = %s, want (p1|(p2/p3))", got)
	}
	if w := m.projectSidebarWidth(); w != 0 {
		t.Fatalf("fixture assumes no project sidebar, got width %d", w)
	}
	if r := m.paneRectAt(75, 10); r == nil || r.Pane.ID != "p2" || r.OX != 50 || r.OY != 1 || r.W != 50 || r.H != 19 {
		t.Fatalf("fixture: rect at (75,10) = %+v, want p2 at (50,1,50,19)", r)
	}
	return m
}

func pdPress(t *testing.T, m Model, x, y int, mod tea.KeyMod) Model {
	t.Helper()
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft, Mod: mod})
	return updated.(Model)
}

func pdMove(t *testing.T, m Model, x, y int) Model {
	t.Helper()
	updated, _ := m.Update(tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft, Mod: pdMod})
	return updated.(Model)
}

func pdRelease(t *testing.T, m Model, x, y int) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	return updated.(Model), cmd
}

// pdSent returns every message of type typ the fake connection received.
func pdSent(m Model, typ string) []*ipc.Message {
	var out []*ipc.Message
	for _, msg := range m.client.(*fakeConn).sent {
		if msg.Type == typ {
			out = append(out, msg)
		}
	}
	return out
}

// pdCell is the stripped text of one screen cell of a rendered frame.
func pdCell(lines []string, col, row int) string {
	if row < 0 || row >= len(lines) {
		return ""
	}
	return ansi.Strip(ansi.Cut(lines[row], col, col+1))
}

func TestPaneDragModifier(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		mod  tea.KeyMod
		want bool
	}{
		{"alt", tea.ModAlt, true},
		{"alt+shift", tea.ModAlt | tea.ModShift, true},
		{"ctrl+alt", tea.ModCtrl | tea.ModAlt, false},
		{"ctrl", tea.ModCtrl, false},
		{"none", 0, false},
	} {
		if got := paneDragModifier(tc.mod); got != tc.want {
			t.Errorf("%s: paneDragModifier = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDropZoneAt(t *testing.T) {
	t.Parallel()
	wide := PaneRect{OX: 50, OY: 1, W: 50, H: 19}
	square := PaneRect{OX: 0, OY: 0, W: 12, H: 12}
	for _, tc := range []struct {
		name string
		r    PaneRect
		x, y int
		want dropZone
	}{
		{"left band", wide, 52, 10, zoneLeft},
		{"right band", wide, 97, 10, zoneRight},
		{"top band", wide, 75, 2, zoneTop},
		{"bottom band", wide, 75, 18, zoneBottom},
		{"centre", wide, 75, 10, zoneCenter},
		{"last left-band column (11.5/50 < 1/4)", wide, 61, 10, zoneLeft},
		{"first centre column (12.5/50 = 1/4)", wide, 62, 10, zoneCenter},
		{"corner nearer the top edge", wide, 55, 1, zoneTop},
		{"corner nearer the left edge", wide, 50, 3, zoneLeft},
		{"corner tie goes to left/right", square, 0, 0, zoneLeft},
		{"opposite corner tie goes to left/right", square, 11, 11, zoneRight},
		{"outside the rect", wide, 10, 10, zoneNone},
	} {
		if got := dropZoneAt(tc.r, tc.x, tc.y); got != tc.want {
			t.Errorf("%s: dropZoneAt(%d,%d) = %d, want %d", tc.name, tc.x, tc.y, got, tc.want)
		}
	}
}

func TestDropPreviewRect(t *testing.T) {
	t.Parallel()
	r := PaneRect{OX: 50, OY: 1, W: 50, H: 19}
	for _, tc := range []struct {
		zone dropZone
		want PaneRect
	}{
		{zoneLeft, PaneRect{OX: 50, OY: 1, W: 25, H: 19}},
		{zoneRight, PaneRect{OX: 75, OY: 1, W: 25, H: 19}},
		{zoneTop, PaneRect{OX: 50, OY: 1, W: 50, H: 9}},
		{zoneBottom, PaneRect{OX: 50, OY: 10, W: 50, H: 10}},
		{zoneCenter, r},
	} {
		if got := dropPreviewRect(r, tc.zone); got != tc.want {
			t.Errorf("zone %d: preview = %+v, want %+v", tc.zone, got, tc.want)
		}
	}
}

func TestOverlayOutline(t *testing.T) {
	t.Parallel()
	base := strings.TrimSuffix(strings.Repeat(strings.Repeat(".", 20)+"\n", 6), "\n")
	out := strings.Split(ansi.Strip(overlayOutline(base, PaneRect{OX: 2, OY: 1, W: 6, H: 4}, 20)), "\n")
	want := []string{
		"....................",
		"..┏━━━━┓............",
		"..┃....┃............",
		"..┃....┃............",
		"..┗━━━━┛............",
		"....................",
	}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("row %d = %q, want %q", i, out[i], want[i])
		}
	}
}

func TestPaneDrag_DropOnEachZoneOfAnotherPane(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		x, y int
		want string
	}{
		{"left of p2", 52, 10, "((p1|p2)/p3)"},
		{"right of p2", 97, 10, "((p2|p1)/p3)"},
		{"top of p2", 75, 2, "((p1/p2)/p3)"},
		{"bottom of p2", 75, 18, "((p2/p1)/p3)"},
		{"centre of p2 swaps", 75, 10, "(p2|(p1/p3))"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newPaneDragTestModel(t)
			root := mpTabOf(t, &m, "tab-a").Root
			m = pdPress(t, m, 20, 10, pdMod)
			m = pdMove(t, m, tc.x, tc.y)
			if tab := mpTabOf(t, &m, "tab-a"); tab.Root != root || mpTreeOf(t, &m, "tab-a") != "(p1|(p2/p3))" {
				t.Fatal("the tree changed mid-drag — only the highlight may move before release")
			}

			m, cmd := pdRelease(t, m, tc.x, tc.y)
			runCmd(cmd)

			if got := mpTreeOf(t, &m, "tab-a"); got != tc.want {
				t.Errorf("tree = %s, want %s", got, tc.want)
			}
			tab := mpTabOf(t, &m, "tab-a")
			if tab.ActivePane != "p1" || !mpPane(t, tab, "p1").Active {
				t.Errorf("ActivePane = %q, want the dragged pane p1 active", tab.ActivePane)
			}
			if m.paneDrag.active() {
				t.Error("the drag must end on release")
			}
			if sends := pdSent(m, ipc.MsgUpdateLayout); len(sends) != 1 {
				t.Errorf("MsgUpdateLayout sent %d times, want exactly 1", len(sends))
			}
		})
	}
}

func TestPaneDrag_DropOnATabSendsMovePane(t *testing.T) {
	t.Parallel()
	for _, withMotion := range []bool{true, false} {
		m := newPaneDragTestModel(t)
		x := tabBarX(t, &m, 1, 1)
		m = pdPress(t, m, 20, 10, pdMod)
		if withMotion {
			m = pdMove(t, m, x, 0)
		}
		// Without motion only the release-time re-track can find the tab.
		m, cmd := pdRelease(t, m, x, 0)
		runCmd(cmd)

		moves := pdSent(m, ipc.MsgMovePane)
		if len(moves) != 1 {
			t.Fatalf("motion=%v: MsgMovePane sent %d times, want 1", withMotion, len(moves))
		}
		var p ipc.MovePanePayload
		if err := moves[0].DecodePayload(&p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if p.PaneID != "p1" || p.TabID != "tab-b" {
			t.Errorf("motion=%v: MsgMovePane = %+v, want p1 -> tab-b", withMotion, p)
		}
		if got := mpTreeOf(t, &m, "tab-a"); got != "(p1|(p2/p3))" {
			t.Errorf("tab-a tree = %s — a move is never applied locally before the broadcast", got)
		}
	}
}

func TestPaneDrag_DropOnAnInFlightTabCancels(t *testing.T) {
	t.Parallel()
	t.Run("in flight from the start", func(t *testing.T) {
		m := newPaneDragTestModel(t)
		m.worktreeCreates = map[string]string{"tab-b": "feat-x"}
		x := tabBarX(t, &m, 1, 1)
		m = pdPress(t, m, 20, 10, pdMod)
		m = pdMove(t, m, x, 0)
		if m.paneDrag.overTabID != "" {
			t.Errorf("an in-flight tab became the drop target %q", m.paneDrag.overTabID)
		}
		m, cmd := pdRelease(t, m, x, 0)
		runCmd(cmd)
		if n := len(pdSent(m, ipc.MsgMovePane)); n != 0 {
			t.Errorf("sent %d MsgMovePane to a tab movePaneCandidates excludes", n)
		}
	})
	t.Run("went in flight after the last motion", func(t *testing.T) {
		m := newPaneDragTestModel(t)
		x := tabBarX(t, &m, 1, 1)
		m = pdPress(t, m, 20, 10, pdMod)
		m = pdMove(t, m, x, 0)
		if m.paneDrag.overTabID != "tab-b" {
			t.Fatalf("setup: drop target = %q, want tab-b", m.paneDrag.overTabID)
		}
		m.worktreeCreates = map[string]string{"tab-b": "feat-x"}
		m, cmd := pdRelease(t, m, x, 0)
		runCmd(cmd)
		if n := len(pdSent(m, ipc.MsgMovePane)); n != 0 {
			t.Errorf("sent %d MsgMovePane to a tab that went in flight before the release", n)
		}
	})
}

func TestPaneDrag_ReleaseWithNothingToDoCancels(t *testing.T) {
	t.Parallel()
	probe := newPaneDragTestModel(t)
	if idx := probe.hitTestTab(99); idx >= 0 {
		t.Fatalf("fixture: column 99 of the tab bar hits tab %d, want empty bar", idx)
	}
	for _, tc := range []struct {
		name string
		at   func(t *testing.T, m *Model) (x, y int)
	}{
		{"on itself", func(*testing.T, *Model) (int, int) { return 30, 12 }},
		{"on its own tab", func(t *testing.T, m *Model) (int, int) { return tabBarX(t, m, 0, 1), 0 }},
		{"on empty tab-bar space", func(*testing.T, *Model) (int, int) { return 99, 0 }},
		{"on the status bar", func(*testing.T, *Model) (int, int) { return 20, 39 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newPaneDragTestModel(t)
			x, y := tc.at(t, &m)
			m = pdPress(t, m, 20, 10, pdMod)
			m = pdMove(t, m, x, y)
			m, cmd := pdRelease(t, m, x, y)
			if cmd != nil {
				t.Error("a release with nothing to do returned a command")
			}
			if m.paneDrag.active() {
				t.Error("the drag must end on release")
			}
			if got := mpTreeOf(t, &m, "tab-a"); got != "(p1|(p2/p3))" {
				t.Errorf("tree = %s, want it untouched", got)
			}
		})
	}
}

func TestPaneDrag_EscCancels(t *testing.T) {
	t.Parallel()
	m := newPaneDragTestModel(t)
	m = pdPress(t, m, 20, 10, pdMod)
	m = pdMove(t, m, 52, 10)
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	if m.paneDrag.active() {
		t.Fatal("Esc must cancel the drag")
	}
	m, cmd := pdRelease(t, m, 52, 10)
	runCmd(cmd)
	if got := mpTreeOf(t, &m, "tab-a"); got != "(p1|(p2/p3))" {
		t.Errorf("tree = %s — the release after Esc still dropped", got)
	}
}

func TestPaneDrag_AltPressStartsNoSelectionBorderOrScrollbarDrag(t *testing.T) {
	t.Parallel()
	probe := newPaneDragTestModel(t)
	if probe.hitTestSplitBorder(50, 10) == nil {
		t.Fatal("fixture: (50,10) must be on the p1|p2 split line")
	}
	if probe.hitTestScrollbar(47, 10) == nil {
		t.Fatal("fixture: (47,10) must be on p1's scrollbar")
	}
	for _, tc := range []struct {
		name    string
		x, y    int
		wantSrc string
	}{
		{"on the split line", 50, 10, "p2"},
		{"on the scrollbar", 47, 10, "p1"},
		{"inside the pane", 20, 10, "p1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := pdPress(t, newPaneDragTestModel(t), tc.x, tc.y, pdMod)
			if m.paneDrag.srcPaneID != tc.wantSrc {
				t.Errorf("drag source = %q, want %q", m.paneDrag.srcPaneID, tc.wantSrc)
			}
			if m.splitDragNode != nil || m.scrollDragPaneID != "" || m.mouseDown || m.selection != nil {
				t.Errorf("an Alt+press armed another drag: split=%v scroll=%q mouseDown=%v selection=%v",
					m.splitDragNode != nil, m.scrollDragPaneID, m.mouseDown, m.selection != nil)
			}
		})
	}
}

func TestPaneDrag_PlainPressBehavesAsBefore(t *testing.T) {
	t.Parallel()
	m := pdPress(t, newPaneDragTestModel(t), 20, 10, 0)
	if m.paneDrag.active() || !m.mouseDown {
		t.Errorf("plain press: paneDrag=%v mouseDown=%v, want no drag and a selection press", m.paneDrag.active(), m.mouseDown)
	}
	m = pdPress(t, newPaneDragTestModel(t), 50, 10, 0)
	if m.paneDrag.active() || m.splitDragNode == nil {
		t.Error("a plain press on the split line must still arm the border drag")
	}
}

func TestPaneDrag_NotArmedInNotesModeOrOnABusyTab(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		arm  func(m *Model)
	}{
		{"notes mode", func(m *Model) { m.notesMode = true }},
		{"this client's own pending split", func(m *Model) {
			tab := mpTabOf(t, m, "tab-a")
			m.pendingSplit = map[string]*LayoutNode{"tab-a": tab.SplitAtPane("p3", SplitVertical)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newPaneDragTestModel(t)
			tc.arm(&m)
			m = pdPress(t, m, 20, 10, pdMod)
			if m.paneDrag.active() {
				t.Error("a pane drag armed")
			}
		})
	}
}

// Review Focus 5.
func TestPaneDrag_SourceMovedAwayByABroadcastCancels(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	moved := mpState("tab-a", mpTab{"tab-a", []string{"p2", "p3"}}, mpTab{"tab-b", []string{"p4", "p1"}}, mpTab{"tab-c", []string{"p5"}})

	t.Run("release on another tab", func(t *testing.T) {
		m := newPaneDragTestModel(t)
		m = pdPress(t, m, 20, 10, pdMod)
		m = pdMove(t, m, 52, 10)
		m = mpApply(t, m, moved)
		if strings.Contains(ansi.Strip(m.View().Content), "┏") {
			t.Error("the drop preview is still drawn for a pane that left its tab")
		}
		x := tabBarX(t, &m, 2, 1)
		m, cmd := pdRelease(t, m, x, 0)
		runCmd(cmd)
		if n := len(pdSent(m, ipc.MsgMovePane)); n != 0 {
			t.Errorf("sent %d MsgMovePane for a pane that is no longer in the tab it was dragged from", n)
		}
		if m.paneDrag.active() {
			t.Error("the drag must be over")
		}
	})
	t.Run("release on a pane", func(t *testing.T) {
		m := newPaneDragTestModel(t)
		m = pdPress(t, m, 20, 10, pdMod)
		m = mpApply(t, m, moved)
		before := mpTreeOf(t, &m, "tab-a")
		m, cmd := pdRelease(t, m, 75, 10)
		if cmd != nil {
			t.Error("the release returned a command")
		}
		if got := mpTreeOf(t, &m, "tab-a"); got != before {
			t.Errorf("tab-a tree = %s, want %s", got, before)
		}
	})
	t.Run("motion", func(t *testing.T) {
		m := newPaneDragTestModel(t)
		m = pdPress(t, m, 20, 10, pdMod)
		m = mpApply(t, m, moved)
		m = pdMove(t, m, 75, 10)
		if m.paneDrag.active() {
			t.Error("motion after the source left must cancel the drag")
		}
	})
}

func TestPaneDrag_ViewDrawsTheDropPreview(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newPaneDragTestModel(t)
	m = pdPress(t, m, 20, 10, pdMod)
	baseRow0 := strings.Split(m.View().Content, "\n")[0]

	// Left zone of p2 (50,1,50,19): X takes columns 50..74, rows 1..19.
	m = pdMove(t, m, 52, 10)
	lines := strings.Split(m.View().Content, "\n")
	for _, c := range []struct {
		col, row int
		want     string
	}{{50, 1, "┏"}, {74, 1, "┓"}, {50, 19, "┗"}, {74, 19, "┛"}, {50, 10, "┃"}, {74, 10, "┃"}} {
		if got := pdCell(lines, c.col, c.row); got != c.want {
			t.Errorf("left zone: cell (%d,%d) = %q, want %q", c.col, c.row, got, c.want)
		}
	}

	// Centre: the whole of p2.
	m = pdMove(t, m, 75, 10)
	lines = strings.Split(m.View().Content, "\n")
	for _, c := range []struct {
		col, row int
		want     string
	}{{50, 1, "┏"}, {99, 1, "┓"}, {50, 19, "┗"}, {99, 19, "┛"}} {
		if got := pdCell(lines, c.col, c.row); got != c.want {
			t.Errorf("centre: cell (%d,%d) = %q, want %q", c.col, c.row, got, c.want)
		}
	}

	// Over tab-b: the tab is restyled in place, no pane outline.
	m = pdMove(t, m, tabBarX(t, &m, 1, 1), 0)
	frame := m.View().Content
	row0 := strings.Split(frame, "\n")[0]
	if ansi.Strip(row0) != ansi.Strip(baseRow0) {
		t.Errorf("tab bar text changed: %q, want %q", ansi.Strip(row0), ansi.Strip(baseRow0))
	}
	if row0 == baseRow0 {
		t.Error("the hovered tab is not highlighted")
	}
	if strings.Contains(ansi.Strip(frame), "┏") {
		t.Error("a pane outline is drawn while the pointer is over a tab")
	}
}
