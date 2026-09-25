package tui

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// Layout sync between several TUIs on one daemon (spec §7.2). Every decision
// is driven through Update with a workspace_state broadcast, the only thing a
// client ever learns another client's tree from.

const lsProject = "proj-ls"

// lsLeaf and lsSplit build a stored tree the way the daemon holds it.
func lsLeaf(id string) *SerializedNode { return &SerializedNode{PaneID: id} }

func lsSplit(dir SplitDir, ratio float64, l, r *SerializedNode) *SerializedNode {
	return &SerializedNode{Split: &dir, Ratio: ratio, Left: l, Right: r}
}

// lsWire renders a stored tree as it reaches a client: parseWorkspaceState
// decodes the broadcast into map[string]any and re-marshals the layout, so the
// bytes are map-ordered (see broadcastLayout).
func lsWire(t *testing.T, s *SerializedNode) json.RawMessage {
	t.Helper()
	if s == nil {
		return nil
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out, err := json.Marshal(generic)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	return out
}

// lsState is a one-project, one-tab ("t1") broadcast at layout revision rev.
func lsState(rev uint64, layout json.RawMessage, panes ...string) WorkspaceStateMsg {
	st := WorkspaceStateMsg{
		ActiveProject: lsProject,
		ActiveTab:     "t1",
		Projects:      []ProjectInfo{{ID: lsProject, Name: "ls", TabIDs: []string{"t1"}, ActiveTab: "t1"}},
		Tabs:          []TabInfo{{ID: "t1", Name: "t1", ProjectID: lsProject, Panes: panes, Layout: layout, LayoutRev: rev}},
	}
	for _, p := range panes {
		st.Panes = append(st.Panes, PaneInfo{ID: p, TabID: "t1", Type: "terminal"})
	}
	return st
}

func newLayoutSyncModel(t *testing.T) Model {
	t.Helper()
	m := newModelForTest(nil, 0)
	m.width, m.height = 120, 40
	m.client = &echoRecorder{}
	m.notifications = NewNotificationCenter(30, 200)
	m.mcpHighlights = make(map[string]bool)
	return m
}

// lsApply delivers st through Update, runs every command it returned, and
// returns the update_layout frames those commands sent.
func lsApply(t *testing.T, m Model, st WorkspaceStateMsg) (Model, []ipc.UpdateLayoutPayload) {
	t.Helper()
	rec := &echoRecorder{}
	m.client = rec
	next, cmd := m.Update(st)
	runCmd(cmd)
	return next.(Model), lsLayouts(t, rec)
}

// lsRun runs a user action's command against a fresh recorder and returns the
// update_layout frames it sent.
func lsRun(t *testing.T, m *Model, action func() tea.Cmd) []ipc.UpdateLayoutPayload {
	t.Helper()
	rec := &echoRecorder{}
	m.client = rec
	runCmd(action())
	return lsLayouts(t, rec)
}

func lsLayouts(t *testing.T, rec *echoRecorder) []ipc.UpdateLayoutPayload {
	t.Helper()
	var out []ipc.UpdateLayoutPayload
	for _, msg := range rec.sent {
		if msg.Type != ipc.MsgUpdateLayout {
			continue
		}
		var p ipc.UpdateLayoutPayload
		if err := msg.DecodePayload(&p); err != nil {
			t.Fatalf("decode update_layout: %v", err)
		}
		out = append(out, p)
	}
	return out
}

func lsTab(t *testing.T, m *Model) *TabModel {
	t.Helper()
	tab := m.tabByID("t1")
	if tab == nil {
		t.Fatal("tab t1 is missing")
	}
	return tab
}

// lsTree is the tab's tree in its stored form, placeholders omitted.
func lsTree(t *testing.T, m *Model) *SerializedNode {
	t.Helper()
	return SerializeLayout(lsTab(t, m).Root)
}

// sentLayoutIs reports whether a sent layout describes root, structurally
// (the wire bytes may be struct- or map-ordered).
func sentLayoutIs(raw json.RawMessage, root *LayoutNode) bool {
	s, err := UnmarshalLayout(raw)
	return err == nil && s != nil && reflect.DeepEqual(s, SerializeLayout(root))
}

func lsSentTree(t *testing.T, p ipc.UpdateLayoutPayload) *SerializedNode {
	t.Helper()
	s, err := UnmarshalLayout(p.Layout)
	if err != nil {
		t.Fatalf("sent layout does not parse: %v", err)
	}
	return s
}

func lsBase(p ipc.UpdateLayoutPayload) string {
	if p.BaseRev == nil {
		return "nil"
	}
	return strconv.FormatUint(*p.BaseRev, 10)
}

// lsParentOf returns node's parent in root and whether node is its left child.
func lsParentOf(root, node *LayoutNode) (*LayoutNode, bool) {
	if root == nil || root.IsLeaf() {
		return nil, false
	}
	if root.Left == node {
		return root, true
	}
	if root.Right == node {
		return root, false
	}
	if p, l := lsParentOf(root.Left, node); p != nil {
		return p, l
	}
	return lsParentOf(root.Right, node)
}

func TestLayoutSync_HigherRevAdoptedKeepsPaneModels(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newLayoutSyncModel(t)
	m, _ = lsApply(t, m, lsState(3, lsWire(t, lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsLeaf("p2"))), "p1", "p2"))
	tab := lsTab(t, &m)
	p1, p2 := mpPane(t, tab, "p1"), mpPane(t, tab, "p2")

	theirs := lsSplit(SplitVertical, 0.3, lsLeaf("p2"), lsLeaf("p1"))
	m, sent := lsApply(t, m, lsState(4, lsWire(t, theirs), "p1", "p2"))

	if got := lsTree(t, &m); !reflect.DeepEqual(got, theirs) {
		t.Fatalf("tree = %s, want the adopted %s", layoutString(got), layoutString(theirs))
	}
	tab = lsTab(t, &m)
	if mpPane(t, tab, "p1") != p1 || mpPane(t, tab, "p2") != p2 {
		t.Error("adoption built new PaneModels — the panes' emulators and scrollback were lost")
	}
	if tab.layoutRev != 4 {
		t.Errorf("layoutRev = %d, want 4", tab.layoutRev)
	}
	if len(sent) != 0 {
		t.Errorf("adoption sent %d update_layout frames, want 0", len(sent))
	}
}

func TestLayoutSync_EqualRevDirtyKeepsLocal(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newLayoutSyncModel(t)
	stored := lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsLeaf("p2"))
	m, _ = lsApply(t, m, lsState(3, lsWire(t, stored), "p1", "p2"))

	sent := lsRun(t, &m, func() tea.Cmd { return m.arrangeTab(lsTab(t, &m), layoutRows) })
	if len(sent) != 1 || lsBase(sent[0]) != "3" {
		t.Fatalf("the arrangement sent %d frames, want 1 with base 3", len(sent))
	}
	local := lsTree(t, &m)

	// Our write is in flight: a broadcast still at rev 3 carries the OLD tree.
	m, sent = lsApply(t, m, lsState(3, lsWire(t, stored), "p1", "p2"))
	if got := lsTree(t, &m); !reflect.DeepEqual(got, local) {
		t.Errorf("tree = %s, want the local %s kept while our write is in flight", layoutString(got), layoutString(local))
	}
	if len(sent) != 0 {
		t.Errorf("sent %d frames, want 0", len(sent))
	}
}

// Two local changes inside one round trip: the second would carry the same
// base as the first and be refused behind it, then lost to the first one's
// echo. It is held instead, and sent on that echo with the new base.
func TestLayoutSync_ChangeWhileInFlightIsSentOnTheEcho(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newLayoutSyncModel(t)
	m, _ = lsApply(t, m, lsState(3, lsWire(t, lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsLeaf("p2"))), "p1", "p2"))

	first := lsRun(t, &m, func() tea.Cmd { return m.arrangeTab(lsTab(t, &m), layoutRows) })
	if len(first) != 1 || lsBase(first[0]) != "3" {
		t.Fatalf("the first change sent %d frames, want 1 with base 3", len(first))
	}
	lsTab(t, &m).Root.Ratio = 0.7 // a second change, as a border drag leaves it
	if sent := lsRun(t, &m, func() tea.Cmd { return m.markLayoutChanged("", lsTab(t, &m)) }); len(sent) != 0 {
		t.Fatalf("the second change sent %d frames while the first was in flight, want 0", len(sent))
	}
	second := lsTree(t, &m)

	m, sent := lsApply(t, m, lsState(4, lsWire(t, lsSentTree(t, first[0])), "p1", "p2"))
	if len(sent) != 1 || lsBase(sent[0]) != "4" {
		t.Fatalf("the echo sent %d frames, want the held change once with base 4", len(sent))
	}
	if got := lsSentTree(t, sent[0]); !reflect.DeepEqual(got, second) {
		t.Errorf("sent %s, want the held %s", layoutString(got), layoutString(second))
	}
	if got := lsTree(t, &m); !reflect.DeepEqual(got, second) {
		t.Errorf("tree = %s, want the local %s kept through our own echo", layoutString(got), layoutString(second))
	}
}

func TestLayoutSync_LocalSplitSendsOneUpdateWithBase(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newLayoutSyncModel(t)
	m, _ = lsApply(t, m, lsState(5, lsWire(t, lsLeaf("p1")), "p1"))

	if sent := lsRun(t, &m, func() tea.Cmd { return m.splitPane(SplitHorizontal) }); len(sent) != 0 {
		t.Fatalf("the split sent %d layouts before its pane existed, want 0", len(sent))
	}
	m, sent := lsApply(t, m, lsState(5, lsWire(t, lsLeaf("p1")), "p1", "p-new"))

	want := lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsLeaf("p-new"))
	if len(sent) != 1 {
		t.Fatalf("sent %d update_layout frames, want exactly 1", len(sent))
	}
	if sent[0].TabID != "t1" || lsBase(sent[0]) != "5" {
		t.Errorf("sent tab %q base %s, want t1 base 5", sent[0].TabID, lsBase(sent[0]))
	}
	if got := lsSentTree(t, sent[0]); !reflect.DeepEqual(got, want) {
		t.Errorf("sent %s, want %s", layoutString(got), layoutString(want))
	}

	// The echo of our own write: adopted without a word.
	m, sent = lsApply(t, m, lsState(6, lsWire(t, want), "p1", "p-new"))
	if len(sent) != 0 {
		t.Errorf("the echo produced %d frames, want 0", len(sent))
	}
	if tab := lsTab(t, &m); tab.layoutRev != 6 || tab.layoutDirty {
		t.Errorf("after the echo layoutRev=%d dirty=%v, want 6 and clean", tab.layoutRev, tab.layoutDirty)
	}
}

func TestLayoutSync_RatioOnlyDifferenceSendsNothing(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newLayoutSyncModel(t)
	m, _ = lsApply(t, m, lsState(7, lsWire(t, lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsLeaf("p2"))), "p1", "p2"))

	differs := lsState(7, lsWire(t, lsSplit(SplitHorizontal, 0.6, lsLeaf("p1"), lsLeaf("p2"))), "p1", "p2")
	for i := 0; i < 3; i++ {
		var sent []ipc.UpdateLayoutPayload
		m, sent = lsApply(t, m, differs)
		if len(sent) != 0 {
			t.Fatalf("broadcast %d sent %d update_layout frames, want 0 — a clean tab at the "+
				"stored revision never sends on a mere disagreement", i+1, len(sent))
		}
	}
}

func TestLayoutSync_AdoptionCancelsDrag(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	stored := lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsLeaf("p2"))
	theirs := lsSplit(SplitVertical, 0.5, lsLeaf("p1"), lsLeaf("p2"))

	t.Run("split border", func(t *testing.T) {
		m := newLayoutSyncModel(t)
		m, _ = lsApply(t, m, lsState(1, lsWire(t, stored), "p1", "p2"))
		next, _ := m.Update(tea.MouseClickMsg{X: 60, Y: 10, Button: tea.MouseLeft})
		m = next.(Model)
		if m.splitDragNode == nil {
			t.Fatal("setup: the press did not arm a border drag")
		}
		m, _ = lsApply(t, m, lsState(2, lsWire(t, theirs), "p1", "p2"))
		if m.splitDragNode != nil {
			t.Error("the border drag survived adoption of another tree")
		}
		next, cmd := m.Update(tea.MouseReleaseMsg{X: 90, Y: 10, Button: tea.MouseLeft})
		m = next.(Model)
		rec := &echoRecorder{}
		m.client = rec
		runCmd(cmd)
		if sent := lsLayouts(t, rec); len(sent) != 0 {
			t.Errorf("the release after adoption sent %d layouts, want 0", len(sent))
		}
		if got := lsTree(t, &m); !reflect.DeepEqual(got, theirs) {
			t.Errorf("tree = %s, want the adopted %s", layoutString(got), layoutString(theirs))
		}
	})

	t.Run("pane drag", func(t *testing.T) {
		m := newLayoutSyncModel(t)
		m, _ = lsApply(t, m, lsState(1, lsWire(t, stored), "p1", "p2"))
		next, _ := m.Update(tea.MouseClickMsg{X: 20, Y: 10, Button: tea.MouseLeft, Mod: tea.ModAlt})
		m = next.(Model)
		if !m.paneDrag.active() {
			t.Fatal("setup: Alt+press did not arm a pane drag")
		}
		m, _ = lsApply(t, m, lsState(2, lsWire(t, theirs), "p1", "p2"))
		if m.paneDrag.active() {
			t.Error("the pane drag survived adoption of another tree")
		}
	})
}

func TestLayoutSync_PendingSplitSurvivesBesideSibling(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newLayoutSyncModel(t)
	m, _ = lsApply(t, m, lsState(1, lsWire(t, lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsLeaf("p2"))), "p1", "p2"))
	tab := lsTab(t, &m)
	tab.ActivePane = "p2"
	_ = m.splitPane(SplitVertical)
	if m.pendingSplit["t1"] == nil {
		t.Fatal("setup: the split armed no reservation")
	}
	// Held open the way a worktree create holds it, so a broadcast that
	// carries no new pane does not prune it.
	m.worktreeCreates = map[string]string{"t1": "feat/x"}

	// Another client swapped the two panes.
	m, _ = lsApply(t, m, lsState(2, lsWire(t, lsSplit(SplitHorizontal, 0.5, lsLeaf("p2"), lsLeaf("p1"))), "p1", "p2"))

	tab = lsTab(t, &m)
	ph := m.pendingSplit["t1"]
	if ph == nil || !treeHoldsNode(tab.Root, ph) {
		t.Fatal("the reservation was lost on adoption — the requested pane would land nowhere")
	}
	parent, isLeft := lsParentOf(tab.Root, ph)
	if parent == nil || isLeft || parent.Split != SplitVertical ||
		parent.Left == nil || !parent.Left.IsLeaf() || parent.Left.Pane.ID != "p2" {
		t.Fatalf("reservation is not below its sibling p2: tree %s", layoutString(SerializeLayout(tab.Root)))
	}

	// The requested pane lands in it, and this client — the requester — sends.
	m, sent := lsApply(t, m, lsState(2, lsWire(t, lsSplit(SplitHorizontal, 0.5, lsLeaf("p2"), lsLeaf("p1"))), "p1", "p2", "p-new"))
	want := lsSplit(SplitHorizontal, 0.5, lsSplit(SplitVertical, 0.5, lsLeaf("p2"), lsLeaf("p-new")), lsLeaf("p1"))
	if got := lsTree(t, &m); !reflect.DeepEqual(got, want) {
		t.Errorf("tree = %s, want %s", layoutString(got), layoutString(want))
	}
	if len(sent) != 1 || lsBase(sent[0]) != "2" {
		t.Errorf("sent %d frames, want 1 with base 2", len(sent))
	}
}

// Spec 7a: only the requester of a pane sends the tree that places it.
func TestLayoutSync_ArrivalRace(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	before := lsState(3, lsWire(t, lsLeaf("p1")), "p1")
	arrived := lsState(3, lsWire(t, lsLeaf("p1")), "p1", "px")

	a, b := newLayoutSyncModel(t), newLayoutSyncModel(t)
	a, _ = lsApply(t, a, before)
	b, _ = lsApply(t, b, before)
	_ = a.splitPane(SplitHorizontal)

	a, sentA := lsApply(t, a, arrived)
	b, sentB := lsApply(t, b, arrived)
	if len(sentA) != 1 || lsBase(sentA[0]) != "3" {
		t.Fatalf("the requester sent %d frames, want 1 with base 3", len(sentA))
	}
	if len(sentB) != 0 {
		t.Fatalf("a bystander sent %d frames on the arrival, want 0 — its placement "+
			"would race the requester's on the same base", len(sentB))
	}

	next := lsState(4, lsWire(t, lsSentTree(t, sentA[0])), "p1", "px")
	a, sentA = lsApply(t, a, next)
	b, sentB = lsApply(t, b, next)
	if len(sentA)+len(sentB) != 0 {
		t.Errorf("rev 4 produced %d+%d frames, want none", len(sentA), len(sentB))
	}
	if ta, tb := lsTree(t, &a), lsTree(t, &b); !reflect.DeepEqual(ta, tb) {
		t.Errorf("trees differ after adoption: A=%s B=%s", layoutString(ta), layoutString(tb))
	}
}

// An MCP-created pane nobody requested: every client places it locally, and
// on the next broadcast that still lacks it, each sends once.
func TestLayoutSync_UnrequestedArrivalSendsOnceIfStillMissing(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	before := lsState(3, lsWire(t, lsLeaf("p1")), "p1")
	arrived := lsState(3, lsWire(t, lsLeaf("p1")), "p1", "p-mcp")

	a, b := newLayoutSyncModel(t), newLayoutSyncModel(t)
	a, _ = lsApply(t, a, before)
	b, _ = lsApply(t, b, before)

	a, sentA := lsApply(t, a, arrived)
	b, sentB := lsApply(t, b, arrived)
	if len(sentA)+len(sentB) != 0 {
		t.Fatalf("the arrival itself sent %d+%d frames, want none", len(sentA), len(sentB))
	}

	// The stored tree still lacks it: nobody asked, so each client sends once.
	a, sentA = lsApply(t, a, arrived)
	b, sentB = lsApply(t, b, arrived)
	if len(sentA) != 1 || len(sentB) != 1 || lsBase(sentA[0]) != "3" || lsBase(sentB[0]) != "3" {
		t.Fatalf("still missing: sent %d and %d frames, want 1 each with base 3", len(sentA), len(sentB))
	}

	// A's write won; B's was refused. Both adopt, and it stays quiet.
	winner := lsState(4, lsWire(t, lsSentTree(t, sentA[0])), "p1", "p-mcp")
	for i := 0; i < 2; i++ {
		a, sentA = lsApply(t, a, winner)
		b, sentB = lsApply(t, b, winner)
		if len(sentA)+len(sentB) != 0 {
			t.Errorf("rev 4 broadcast %d produced %d+%d frames, want none", i+1, len(sentA), len(sentB))
		}
	}
	if ta, tb := lsTree(t, &a), lsTree(t, &b); !reflect.DeepEqual(ta, tb) {
		t.Errorf("trees differ: A=%s B=%s", layoutString(ta), layoutString(tb))
	}
}

// Spec 7b: after a reattach the daemon is the authority, even when its
// revision is LOWER — a restarted daemon lost the debounced snapshot.
func TestLayoutSync_ReattachAdoptsLowerRev(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newLayoutSyncModel(t)
	m, _ = lsApply(t, m, lsState(9, lsWire(t, lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsLeaf("p2"))), "p1", "p2"))

	m.armReattachReset("")

	restored := lsSplit(SplitVertical, 0.4, lsLeaf("p2"), lsLeaf("p1"))
	m, sent := lsApply(t, m, lsState(2, lsWire(t, restored), "p1", "p2"))
	if got := lsTree(t, &m); !reflect.DeepEqual(got, restored) {
		t.Errorf("tree = %s, want the daemon's %s adopted after the reattach", layoutString(got), layoutString(restored))
	}
	if tab := lsTab(t, &m); tab.layoutRev != 2 {
		t.Errorf("layoutRev = %d, want 2", tab.layoutRev)
	}
	if len(sent) != 0 {
		t.Errorf("sent %d frames, want 0", len(sent))
	}
}

func TestLayoutSync_EmptyStoredLayoutSendsOnce(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newLayoutSyncModel(t)
	m, _ = lsApply(t, m, lsState(4, lsWire(t, lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsLeaf("p2"))), "p1", "p2"))
	local := lsTree(t, &m)

	empty := lsState(4, nil, "p1", "p2")
	m, sent := lsApply(t, m, empty)
	if len(sent) != 1 || lsBase(sent[0]) != "4" {
		t.Fatalf("an empty stored layout sent %d frames, want 1 with base 4", len(sent))
	}
	if got := lsSentTree(t, sent[0]); !reflect.DeepEqual(got, local) {
		t.Errorf("sent %s, want the local %s", layoutString(got), layoutString(local))
	}
	m, sent = lsApply(t, m, empty)
	if len(sent) != 0 {
		t.Errorf("a second empty broadcast sent %d more frames, want 0 — the first is in flight", len(sent))
	}
}

// A close THIS client asked for is a user mutation: the requester sends, the
// bystander only prunes and waits.
func TestLayoutSync_OwnCloseSendsBystanderWaits(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	before := lsState(2, lsWire(t, lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsLeaf("p2"))), "p1", "p2")
	closed := lsState(2, lsWire(t, lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsLeaf("p2"))), "p1")

	a, b := newLayoutSyncModel(t), newLayoutSyncModel(t)
	a, _ = lsApply(t, a, before)
	b, _ = lsApply(t, b, before)
	lsTab(t, &a).ActivePane = "p2"
	next, _ := a.openClosePaneConfirm()
	a = next.(Model)
	next, cmd := a.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	a = next.(Model)
	runCmd(cmd)

	a, sentA := lsApply(t, a, closed)
	b, sentB := lsApply(t, b, closed)
	if len(sentA) != 1 || lsBase(sentA[0]) != "2" {
		t.Fatalf("the closing client sent %d frames, want 1 with base 2", len(sentA))
	}
	if len(sentB) != 0 {
		t.Fatalf("the bystander sent %d frames on the prune, want 0", len(sentB))
	}
	if got := lsSentTree(t, sentA[0]); !reflect.DeepEqual(got, lsLeaf("p1")) {
		t.Errorf("sent %s, want p1", layoutString(got))
	}
}
