package tui

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/plugin"
)

// Spec 5b §3.2a: the daemon now substitutes, prunes and places panes in the
// stored tree itself, so the broadcast that announces a replace, move or close
// already carries the outcome at a HIGHER layout_rev. Every test here feeds
// that daemon shape through Update; the older tests in layout_sync_test.go and
// move_pane_apply_test.go keep feeding the pre-5b shape (same revision, tree
// unchanged), which must keep working against an older daemon.

// armOwnReplace runs this client's ordinary (no worktree) REPLACE of the
// active pane through the dialog's submit path.
func armOwnReplace(t *testing.T, m Model) Model {
	t.Helper()
	t.Setenv("QUIL_HOME", t.TempDir())
	m.client = &fakeSender{}
	m.pluginRegistry = plugin.NewRegistry()
	m.selectedPlugin = "terminal"
	m.worktreeNewBranch = ""
	m.dialogCursor = 2 // Replace current pane
	updated, _ := m.handleCreatePaneSplit()
	got := updated.(Model)
	tab := got.activeTabModel()
	if got.pendingSplit[tab.ID] == nil || !tab.reserveReplace {
		t.Fatal("setup: the replace did not reserve its leaf")
	}
	return got
}

// dtTab is one tab of a daemon-shaped broadcast: panes, stored tree, revision.
type dtTab struct {
	id    string
	panes []string
	tree  *SerializedNode
	rev   uint64
}

func dtState(t *testing.T, active string, tabs ...dtTab) WorkspaceStateMsg {
	t.Helper()
	mp := make([]mpTab, len(tabs))
	for i, tb := range tabs {
		mp[i] = mpTab{tb.id, tb.panes}
	}
	st := mpState(active, mp...)
	for i, tb := range tabs {
		st.Tabs[i].Layout = lsWire(t, tb.tree)
		st.Tabs[i].LayoutRev = tb.rev
	}
	return st
}

func TestDaemonTree_SubstitutedReplaceFillsTheReservation(t *testing.T) {
	m := newLayoutSyncModel(t)
	m, _ = lsApply(t, m, lsState(1, lsWire(t, lsSplit(SplitHorizontal, 0.3, lsLeaf("p1"), lsLeaf("p2"))), "p1", "p2"))
	lsTab(t, &m).ActivePane = "p2"
	m = armOwnReplace(t, m)

	m, sent := lsApply(t, m, lsState(2, lsWire(t, lsSplit(SplitHorizontal, 0.3, lsLeaf("p1"), lsLeaf("p-new"))), "p1", "p-new"))

	tab := lsTab(t, &m)
	if got, want := lsTree(t, &m), lsSplit(SplitHorizontal, 0.3, lsLeaf("p1"), lsLeaf("p-new")); !reflect.DeepEqual(got, want) {
		t.Errorf("tree = %s, want %s", layoutString(got), layoutString(want))
	}
	if tab.Root.HasPlaceholder() {
		t.Error("a blank slot was re-seated beside the pane that already filled it")
	}
	if m.pendingSplit["t1"] != nil || tab.reserveReplace {
		t.Error("the reservation is still armed after its pane landed")
	}
	if tab.ActivePane != "p-new" {
		t.Errorf("ActivePane = %q, want the replacing pane", tab.ActivePane)
	}
	if len(sent) != 0 {
		t.Errorf("sent %d update_layout frames, want 0 — the daemon already stored it", len(sent))
	}
}

// The worktree case: without the fill rule the reservation is re-seated and
// the spinner runs until the create timeout restores the OLD pane.
func TestDaemonTree_WorktreeReplaceCompletionClearsTheSpinner(t *testing.T) {
	m := newLayoutSyncModel(t)
	m, _ = lsApply(t, m, lsState(1, lsWire(t, lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsLeaf("p2"))), "p1", "p2"))
	lsTab(t, &m).ActivePane = "p2"
	m = armOwnWorktreeCreate(t, m, 2)
	held := m.worktreeReplaced["t1"]
	if held == nil || held.ID != "p2" {
		t.Fatal("setup: the worktree replace did not hold p2")
	}

	m, sent := lsApply(t, m, lsState(2, lsWire(t, lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsLeaf("p-wt"))), "p1", "p-wt"))

	tab := lsTab(t, &m)
	if got, want := lsTree(t, &m), lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsLeaf("p-wt")); !reflect.DeepEqual(got, want) {
		t.Errorf("tree = %s, want %s", layoutString(got), layoutString(want))
	}
	if m.pendingSplit["t1"] != nil || tab.Root.HasPlaceholder() {
		t.Error("the reservation survived the completed replace")
	}
	if m.worktreeCreates["t1"] != "" || tab.CreatingBranch != "" {
		t.Errorf("worktreeCreates=%q CreatingBranch=%q — the spinner keeps running", m.worktreeCreates["t1"], tab.CreatingBranch)
	}
	if m.worktreeReplaced["t1"] != nil || held.vt != nil {
		t.Error("the replaced pane is still held or was not disposed")
	}
	if tab.ActivePane != "p-wt" {
		t.Errorf("ActivePane = %q, want p-wt", tab.ActivePane)
	}
	if len(sent) != 0 {
		t.Errorf("sent %d update_layout frames, want 0", len(sent))
	}
}

func TestDaemonTree_MovedInIsAdoptedAndFocused(t *testing.T) {
	m := newMovePaneModel(t, 120, 40)
	m, _ = lsApply(t, m, dtState(t, "tab-src",
		dtTab{"tab-src", []string{"p1", "p2"}, lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsLeaf("p2")), 1},
		dtTab{"tab-tgt", []string{"p3", "p4"}, lsSplit(SplitHorizontal, 0.5, lsLeaf("p3"), lsLeaf("p4")), 1}))
	moved := mpPane(t, mpTabOf(t, &m, "tab-src"), "p2")
	tgt := mpTabOf(t, &m, "tab-tgt")
	tgt.ActivePane = "p3"
	tgt.ToggleFocus()
	if !tgt.FocusMode() {
		t.Fatal("setup: the target did not enter focus mode")
	}

	m, sent := lsApply(t, m, dtState(t, "tab-src",
		dtTab{"tab-src", []string{"p1"}, lsLeaf("p1"), 2},
		dtTab{"tab-tgt", []string{"p3", "p4", "p2"},
			lsSplit(SplitHorizontal, 0.5, lsLeaf("p3"), lsSplit(SplitVertical, 0.5, lsLeaf("p4"), lsLeaf("p2"))), 2}))

	tgt = mpTabOf(t, &m, "tab-tgt")
	if got := mpTreeOf(t, &m, "tab-tgt"); got != "(p3|(p4/p2))" {
		t.Errorf("target tree = %s, want the daemon's (p3|(p4/p2))", got)
	}
	if mpPane(t, tgt, "p2") != moved {
		t.Error("the moved pane got a new PaneModel — its scrollback was lost")
	}
	if tgt.FocusMode() || tgt.ActivePane != "p2" {
		t.Errorf("focus=%v ActivePane=%q, want focus off and p2 active (adoptMovedPane)", tgt.FocusMode(), tgt.ActivePane)
	}
	if len(sent) != 0 {
		t.Errorf("sent %d update_layout frames, want 0", len(sent))
	}
}

func TestDaemonTree_MovedOutExitsSourceFocusMode(t *testing.T) {
	m := newMovePaneModel(t, 120, 40)
	m, _ = lsApply(t, m, dtState(t, "tab-src",
		dtTab{"tab-src", []string{"p1", "p2", "p3"},
			lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsSplit(SplitVertical, 0.5, lsLeaf("p2"), lsLeaf("p3"))), 1},
		dtTab{"tab-tgt", []string{"p4"}, lsLeaf("p4"), 1}))
	src := mpTabOf(t, &m, "tab-src")
	src.ActivePane = "p2"
	src.ToggleFocus()

	m, _ = lsApply(t, m, dtState(t, "tab-src",
		dtTab{"tab-src", []string{"p1", "p3"}, lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsLeaf("p3")), 2},
		dtTab{"tab-tgt", []string{"p4", "p2"}, lsSplit(SplitHorizontal, 0.5, lsLeaf("p4"), lsLeaf("p2")), 2}))

	if mpTabOf(t, &m, "tab-src").FocusMode() {
		t.Error("the source still full-screens a pane nobody chose after its focused pane moved out")
	}
}

// A DESTROYED pane keeps today's behaviour under the daemon shape too.
func TestDaemonTree_DestroyedKeepsSourceFocusMode(t *testing.T) {
	m := newMovePaneModel(t, 120, 40)
	m, _ = lsApply(t, m, dtState(t, "tab-src",
		dtTab{"tab-src", []string{"p1", "p2", "p3"},
			lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsSplit(SplitVertical, 0.5, lsLeaf("p2"), lsLeaf("p3"))), 1},
		dtTab{"tab-tgt", []string{"p4"}, lsLeaf("p4"), 1}))
	src := mpTabOf(t, &m, "tab-src")
	src.ActivePane = "p2"
	src.ToggleFocus()

	m, _ = lsApply(t, m, dtState(t, "tab-src",
		dtTab{"tab-src", []string{"p1", "p3"}, lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsLeaf("p3")), 2},
		dtTab{"tab-tgt", []string{"p4"}, lsLeaf("p4"), 1}))

	if !mpTabOf(t, &m, "tab-src").FocusMode() {
		t.Error("destroying the focused pane left focus mode — only a MOVE should")
	}
}

// Spec §3.2a "Closed": the pruned tree is adopted, nothing is sent, and the
// close request stays unconsumed (ids are unique; reattach clears it).
func TestDaemonTree_OwnCloseAdoptsWithoutSending(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newLayoutSyncModel(t)
	m, _ = lsApply(t, m, lsState(2, lsWire(t, lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsLeaf("p2"))), "p1", "p2"))
	lsTab(t, &m).ActivePane = "p2"
	next, _ := m.openClosePaneConfirm()
	m = next.(Model)
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = next.(Model)
	runCmd(cmd)

	m, sent := lsApply(t, m, lsState(3, lsWire(t, lsLeaf("p1")), "p1"))

	if len(sent) != 0 {
		t.Errorf("sent %d update_layout frames, want 0 — the daemon already pruned it", len(sent))
	}
	if got := lsTree(t, &m); !reflect.DeepEqual(got, lsLeaf("p1")) {
		t.Errorf("tree = %s, want p1", layoutString(got))
	}
	if !m.closeRequested[closeKey("", "p2")] {
		t.Error("the close request was consumed by an adoption that needed no send")
	}
}

// The replacing pane is always FRESH. A pane another client moved into the
// same tab in the same broadcast is also new to this tree, but it reuses an
// existing model and must not count against the fill.
func TestDaemonTree_ReplaceFillIgnoresAPaneThatMovedIn(t *testing.T) {
	m := newMovePaneModel(t, 120, 40)
	m, _ = lsApply(t, m, dtState(t, "tab-tgt",
		dtTab{"tab-src", []string{"p3", "p4"}, lsSplit(SplitHorizontal, 0.5, lsLeaf("p3"), lsLeaf("p4")), 1},
		dtTab{"tab-tgt", []string{"p1", "p2"}, lsSplit(SplitHorizontal, 0.3, lsLeaf("p1"), lsLeaf("p2")), 1}))
	mpTabOf(t, &m, "tab-tgt").ActivePane = "p2"
	m = armOwnReplace(t, m)
	movedModel := mpPane(t, mpTabOf(t, &m, "tab-src"), "p4")

	// The daemon substituted p-new for p2, then placed the moved p4 by the
	// spiral rule: one broadcast, two new leaves in the target.
	want := lsSplit(SplitHorizontal, 0.3, lsLeaf("p1"), lsSplit(SplitVertical, 0.5, lsLeaf("p-new"), lsLeaf("p4")))
	m, sent := lsApply(t, m, dtState(t, "tab-tgt",
		dtTab{"tab-src", []string{"p3"}, lsLeaf("p3"), 2},
		dtTab{"tab-tgt", []string{"p1", "p-new", "p4"}, want, 3}))

	tgt := mpTabOf(t, &m, "tab-tgt")
	if got := SerializeLayout(tgt.Root); !reflect.DeepEqual(got, want) {
		t.Errorf("tree = %s, want %s", layoutString(got), layoutString(want))
	}
	if tgt.Root.HasPlaceholder() || m.pendingSplit["tab-tgt"] != nil || tgt.reserveReplace {
		t.Error("the replace reservation was not filled — the moved pane counted as a candidate")
	}
	if mpPane(t, tgt, "p4") != movedModel {
		t.Error("the moved pane got a new PaneModel")
	}
	if tgt.ActivePane != "p-new" {
		t.Errorf("ActivePane = %q, want the replacing pane", tgt.ActivePane)
	}
	if len(sent) != 0 {
		t.Errorf("sent %d update_layout frames, want 0", len(sent))
	}
}

// Two fresh panes in one broadcast: which one replaced is unknowable, so the
// reservation is not filled and stays armed in the tree, as before.
func TestDaemonTree_TwoFreshPanesDoNotFillTheReplace(t *testing.T) {
	m := newLayoutSyncModel(t)
	m, _ = lsApply(t, m, lsState(1, lsWire(t, lsSplit(SplitHorizontal, 0.3, lsLeaf("p1"), lsLeaf("p2"))), "p1", "p2"))
	lsTab(t, &m).ActivePane = "p2"
	m = armOwnReplace(t, m)

	m, _ = lsApply(t, m, lsState(2, lsWire(t, lsSplit(SplitHorizontal, 0.3, lsLeaf("p1"),
		lsSplit(SplitVertical, 0.5, lsLeaf("p-new"), lsLeaf("p-mcp")))), "p1", "p-new", "p-mcp"))

	tab := lsTab(t, &m)
	ph := m.pendingSplit["t1"]
	if ph == nil || !treeHoldsNode(tab.Root, ph) || !tab.reserveReplace {
		t.Fatal("the reservation was retired although two fresh panes arrived")
	}
	ids := tab.Root.PaneIDs()
	for _, id := range []string{"p1", "p-new", "p-mcp"} {
		if !ids[id] {
			t.Errorf("pane %s is missing from the adopted tree", id)
		}
	}
}

// The bystander exception holds for a daemon-placed move too: a client
// sitting in the TARGET tab keeps its active pane and focus mode.
func TestDaemonTree_MovedIntoTheActiveTabKeepsFocus(t *testing.T) {
	m := newMovePaneModel(t, 120, 40)
	m, _ = lsApply(t, m, dtState(t, "tab-tgt",
		dtTab{"tab-src", []string{"p1", "p2"}, lsSplit(SplitHorizontal, 0.5, lsLeaf("p1"), lsLeaf("p2")), 1},
		dtTab{"tab-tgt", []string{"p3", "p4"}, lsSplit(SplitHorizontal, 0.5, lsLeaf("p3"), lsLeaf("p4")), 1}))
	moved := mpPane(t, mpTabOf(t, &m, "tab-src"), "p2")
	tgt := mpTabOf(t, &m, "tab-tgt")
	tgt.ActivePane = "p3"
	tgt.ToggleFocus()
	if !tgt.FocusMode() {
		t.Fatal("setup: the target did not enter focus mode")
	}

	m, sent := lsApply(t, m, dtState(t, "tab-tgt",
		dtTab{"tab-src", []string{"p1"}, lsLeaf("p1"), 2},
		dtTab{"tab-tgt", []string{"p3", "p4", "p2"},
			lsSplit(SplitHorizontal, 0.5, lsLeaf("p3"), lsSplit(SplitVertical, 0.5, lsLeaf("p4"), lsLeaf("p2"))), 2}))

	tgt = mpTabOf(t, &m, "tab-tgt")
	if got := mpTreeOf(t, &m, "tab-tgt"); got != "(p3|(p4/p2))" {
		t.Errorf("target tree = %s, want the daemon's (p3|(p4/p2))", got)
	}
	if mpPane(t, tgt, "p2") != moved {
		t.Error("the moved pane got a new PaneModel — its scrollback was lost")
	}
	if !tgt.FocusMode() || tgt.ActivePane != "p3" {
		t.Errorf("focus=%v ActivePane=%q, want focus kept on p3 — the arrival stole the bystander's pane", tgt.FocusMode(), tgt.ActivePane)
	}
	if len(sent) != 0 {
		t.Errorf("sent %d update_layout frames, want 0", len(sent))
	}
}
