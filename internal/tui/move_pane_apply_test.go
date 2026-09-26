package tui

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/persist"
	"github.com/artyomsv/quil/internal/plugin"
)

// Client reconciliation of a pane the daemon moved between tabs. Every test
// builds the "before" state with one broadcast and the "after" state with a
// second, both through Update — the move has no other trigger on the client.

const mpProject = "proj-mp"

// mpTab is one tab of a move-pane fixture broadcast, in tree-insertion order.
type mpTab struct {
	id    string
	panes []string
}

// mpState builds a one-project broadcast holding tabs in the given order.
// activeTab is both the project's and the workspace's active tab, which is
// what the daemon reports after a pane move: it never changes the source
// project's ActiveTab for one.
func mpState(activeTab string, tabs ...mpTab) WorkspaceStateMsg {
	ids := make([]string, 0, len(tabs))
	infos := make([]TabInfo, 0, len(tabs))
	var panes []PaneInfo
	for _, tb := range tabs {
		ids = append(ids, tb.id)
		infos = append(infos, TabInfo{ID: tb.id, Name: tb.id, ProjectID: mpProject, Panes: tb.panes})
		for _, p := range tb.panes {
			panes = append(panes, PaneInfo{ID: p, TabID: tb.id, Type: "terminal"})
		}
	}
	return WorkspaceStateMsg{
		ActiveProject: mpProject,
		ActiveTab:     activeTab,
		Projects:      []ProjectInfo{{ID: mpProject, Name: "mp", TabIDs: ids, ActiveTab: activeTab}},
		Tabs:          infos,
		Panes:         panes,
	}
}

func newMovePaneModel(t *testing.T, w, h int) Model {
	t.Helper()
	m := newModelForTest(nil, 0)
	m.width, m.height = w, h
	m.client = newFakeConn()
	m.notifications = NewNotificationCenter(30, 200)
	m.mcpHighlights = make(map[string]bool)
	return m
}

func mpApply(t *testing.T, m Model, st WorkspaceStateMsg) Model {
	t.Helper()
	updated, _ := m.Update(st)
	return updated.(Model)
}

// mpTabOf fetches a tab that must exist.
func mpTabOf(t *testing.T, m *Model, id string) *TabModel {
	t.Helper()
	tab := m.tabByID(id)
	if tab == nil {
		t.Fatalf("tab %s is missing", id)
	}
	return tab
}

// mpPane fetches a pane model that must be a leaf of tab.
func mpPane(t *testing.T, tab *TabModel, id string) *PaneModel {
	t.Helper()
	leaf := tab.Root.FindLeaf(id)
	if leaf == nil {
		t.Fatalf("pane %s is not in tab %s", id, tab.ID)
	}
	return leaf.Pane
}

// treeHoldsNode reports whether node is reachable from root — a detached
// placeholder is the failure these tests exist to catch.
func treeHoldsNode(root, node *LayoutNode) bool {
	if root == nil {
		return false
	}
	if root == node {
		return true
	}
	return treeHoldsNode(root.Left, node) || treeHoldsNode(root.Right, node)
}

// The base move: source [p1 p2], target [p3], p2 moves to the target.
var (
	mpBefore = []mpTab{{"tab-src", []string{"p1", "p2"}}, {"tab-tgt", []string{"p3"}}}
	mpAfter  = []mpTab{{"tab-src", []string{"p1"}}, {"tab-tgt", []string{"p3", "p2"}}}
)

func TestMovedPane_KeepsItsPaneModel(t *testing.T) {
	t.Parallel()
	m := newMovePaneModel(t, 120, 40)
	m = mpApply(t, m, mpState("tab-src", mpBefore...))
	moved := mpPane(t, mpTabOf(t, &m, "tab-src"), "p2")

	m = mpApply(t, m, mpState("tab-src", mpAfter...))

	if got := mpPane(t, mpTabOf(t, &m, "tab-tgt"), "p2"); got != moved {
		t.Error("the target holds a NEW PaneModel for the moved pane — its emulator and scrollback were lost")
	}
	if moved.vt == nil {
		t.Error("the moved pane's emulator was closed — the dispose sweep treated it as gone")
	}
}

// withLayout stores tree as tabID's layout in st, so a tab this client sees
// for the first time is restored with that exact shape (restoreTabLayout)
// rather than built by the layout-less top|bottom fallback.
func withLayout(t *testing.T, st WorkspaceStateMsg, tabID string, tree *LayoutNode) WorkspaceStateMsg {
	t.Helper()
	data, err := MarshalLayout(tree)
	if err != nil {
		t.Fatalf("MarshalLayout: %v", err)
	}
	st.Tabs = append([]TabInfo(nil), st.Tabs...)
	for i := range st.Tabs {
		if st.Tabs[i].ID == tabID {
			st.Tabs[i].Layout = data
		}
	}
	return st
}

// mpTreeOf renders a tab's tree as `a|(b/c)` (layoutString, tab_test.go).
func mpTreeOf(t *testing.T, m *Model, tabID string) string {
	t.Helper()
	return layoutString(SerializeLayout(mpTabOf(t, m, tabID).Root))
}

func TestMovedPane_SingleTargetPaneSplitsLeftRight(t *testing.T) {
	t.Parallel()
	m := newMovePaneModel(t, 120, 40)
	m = mpApply(t, m, mpState("tab-src", mpBefore...))
	m = mpApply(t, m, mpState("tab-src", mpAfter...))

	if got, want := mpTreeOf(t, &m, "tab-tgt"), "(p3|p2)"; got != want {
		t.Errorf("target tree = %s, want %s", got, want)
	}
}

// The user's scenario: moving a pane into a tab that already holds p3|p4 used
// to make three columns. It spirals instead — the LAST pane (p4) splits
// against its parent's direction — and a second move spirals one level
// further.
func TestMovedPane_SpiralsIntoTheTargetsLastPane(t *testing.T) {
	t.Parallel()
	pair := &LayoutNode{Split: SplitHorizontal, Ratio: 0.5,
		Left: NewLeaf(newTestPane("p3")), Right: NewLeaf(newTestPane("p4"))}

	m := newMovePaneModel(t, 120, 40)
	m = mpApply(t, m, withLayout(t, mpState("tab-src",
		mpTab{"tab-src", []string{"p1", "p2", "pX"}}, mpTab{"tab-tgt", []string{"p3", "p4"}}), "tab-tgt", pair))
	if got := mpTreeOf(t, &m, "tab-tgt"); got != "(p3|p4)" {
		t.Fatalf("setup: target tree = %s, want (p3|p4)", got)
	}

	m = mpApply(t, m, withLayout(t, mpState("tab-src",
		mpTab{"tab-src", []string{"p1", "pX"}}, mpTab{"tab-tgt", []string{"p3", "p4", "p2"}}), "tab-tgt", pair))
	if got, want := mpTreeOf(t, &m, "tab-tgt"), "(p3|(p4/p2))"; got != want {
		t.Fatalf("after the first move: target tree = %s, want %s", got, want)
	}

	m = mpApply(t, m, withLayout(t, mpState("tab-src",
		mpTab{"tab-src", []string{"p1"}}, mpTab{"tab-tgt", []string{"p3", "p4", "p2", "pX"}}), "tab-tgt", pair))
	if got, want := mpTreeOf(t, &m, "tab-tgt"), "(p3|(p4/(p2|pX)))"; got != want {
		t.Errorf("after the second move: target tree = %s, want %s", got, want)
	}
}

func TestMovedPane_NarrowTargetSplitsTopBottom(t *testing.T) {
	t.Parallel()
	// 18 cells: a left|right split would leave 9-cell halves, under minPaneW.
	m := newMovePaneModel(t, 18, 40)
	m = mpApply(t, m, mpState("tab-src", mpBefore...))
	m = mpApply(t, m, mpState("tab-src", mpAfter...))

	root := mpTabOf(t, &m, "tab-tgt").Root
	if root == nil || root.IsLeaf() {
		t.Fatalf("target root = %+v, want a split", root)
	}
	if root.Split != SplitVertical {
		t.Errorf("target split = %v, want SplitVertical (top|bottom) below minPaneW", root.Split)
	}
	if root.Right == nil || !root.Right.IsLeaf() || root.Right.Pane.ID != "p2" {
		t.Errorf("target Right = %+v, want leaf p2", root.Right)
	}
}

func TestMovedPane_BecomesTargetActivePane(t *testing.T) {
	t.Parallel()
	m := newMovePaneModel(t, 120, 40)
	m = mpApply(t, m, mpState("tab-src",
		mpTab{"tab-src", []string{"p1", "p2"}}, mpTab{"tab-tgt", []string{"p3", "p4"}}))
	tgt := mpTabOf(t, &m, "tab-tgt")
	prev := mpPane(t, tgt, tgt.ActivePane)
	if !prev.Active {
		t.Fatalf("setup: target's active pane %s is not flagged Active", prev.ID)
	}

	m = mpApply(t, m, mpState("tab-src",
		mpTab{"tab-src", []string{"p1"}}, mpTab{"tab-tgt", []string{"p3", "p4", "p2"}}))

	tgt = mpTabOf(t, &m, "tab-tgt")
	if tgt.ActivePane != "p2" {
		t.Errorf("target ActivePane = %q, want the moved pane p2", tgt.ActivePane)
	}
	if !mpPane(t, tgt, "p2").Active {
		t.Error("the moved pane is not flagged Active")
	}
	if prev.Active {
		t.Errorf("the target's previous active pane %s is still flagged Active", prev.ID)
	}
}

// TestMovedPane_BystanderViewingTargetKeepsItsOwnActivePane covers a review
// finding: adoptMovedPane used to steal focus from a SECOND client already
// looking at the target tab. Unlike every other move test in this file, which
// follows the mover and stays on the SOURCE, this Model's active tab IS the
// target — mid-keystroke in a pane the move does not touch — and the arriving
// pane must not disturb it.
func TestMovedPane_BystanderViewingTargetKeepsItsOwnActivePane(t *testing.T) {
	t.Parallel()
	m := newMovePaneModel(t, 120, 40)
	m = mpApply(t, m, mpState("tab-tgt",
		mpTab{"tab-src", []string{"p1", "p2"}}, mpTab{"tab-tgt", []string{"p3", "p4"}}))
	tgt := mpTabOf(t, &m, "tab-tgt")
	tgt.ActivePane = "p3"
	mpPane(t, tgt, "p3").Active = true
	tgt.ToggleFocus()
	if !tgt.FocusMode() {
		t.Fatal("setup: the bystander's target tab did not enter focus mode")
	}

	m = mpApply(t, m, mpState("tab-tgt",
		mpTab{"tab-src", []string{"p1"}}, mpTab{"tab-tgt", []string{"p3", "p4", "p2"}}))

	tgt = mpTabOf(t, &m, "tab-tgt")
	if tgt.ActivePane != "p3" {
		t.Errorf("ActivePane = %q, want unchanged p3 — the bystander was typing there", tgt.ActivePane)
	}
	if !mpPane(t, tgt, "p3").Active {
		t.Error("p3 lost its Active flag")
	}
	if !tgt.FocusMode() {
		t.Error("focus mode was exited — adoptMovedPane must not run for a bystander viewing the target")
	}
}

func TestMovedPane_SourceDropsLeafAndPromotesSibling(t *testing.T) {
	t.Parallel()
	m := newMovePaneModel(t, 120, 40)
	m = mpApply(t, m, mpState("tab-src", mpBefore...))
	src := mpTabOf(t, &m, "tab-src")
	// The moved pane is the source's active one, so the promotion is observable.
	src.ActivePane = "p2"
	mpPane(t, src, "p2").Active = true
	mpPane(t, src, "p1").Active = false

	m = mpApply(t, m, mpState("tab-src", mpAfter...))

	src = mpTabOf(t, &m, "tab-src")
	if src.Root == nil || !src.Root.IsLeaf() || src.Root.Pane.ID != "p1" {
		t.Fatalf("source root = %+v, want leaf p1", src.Root)
	}
	if !src.Root.Pane.Active {
		t.Error("p1 is not flagged Active after its sibling moved out")
	}
}

func TestMovedPane_UserStaysInSourceTab(t *testing.T) {
	t.Parallel()
	m := newMovePaneModel(t, 120, 40)
	m = mpApply(t, m, mpState("tab-src", mpBefore...))
	wantProj, wantIdx := m.cur(), m.activeTabIdx()
	if wantProj == nil || m.activeTabModel().ID != "tab-src" {
		t.Fatal("setup: the source tab is not the active tab")
	}

	m = mpApply(t, m, mpState("tab-src", mpAfter...))

	if m.cur() != wantProj {
		t.Errorf("active project changed to %v", m.cur())
	}
	if m.activeTabIdx() != wantIdx || m.activeTabModel().ID != "tab-src" {
		t.Errorf("active tab = %d (%s), want %d (tab-src)", m.activeTabIdx(), m.activeTabModel().ID, wantIdx)
	}
}

func TestMovedPane_SourceDissolvedLandsOnSuccessor(t *testing.T) {
	t.Parallel()
	m := newMovePaneModel(t, 120, 40)
	m = mpApply(t, m, mpState("tab-src",
		mpTab{"tab-src", []string{"p1"}}, mpTab{"tab-tgt", []string{"p3"}}))
	wantProj := m.cur()
	moved := mpPane(t, mpTabOf(t, &m, "tab-src"), "p1")

	// The move emptied the source, so the daemon destroyed it and named the
	// target as the project's active tab.
	m = mpApply(t, m, mpState("tab-tgt", mpTab{"tab-tgt", []string{"p3", "p1"}}))

	if m.cur() != wantProj {
		t.Fatalf("active project changed to %v", m.cur())
	}
	if m.tabByID("tab-src") != nil {
		t.Error("the dissolved source tab is still held")
	}
	if got := m.activeTabModel(); got == nil || got.ID != "tab-tgt" {
		t.Errorf("active tab = %v, want the successor tab-tgt", got)
	}
	if got := mpPane(t, mpTabOf(t, &m, "tab-tgt"), "p1"); got != moved {
		t.Error("the moved pane's PaneModel was replaced")
	}
	if moved.vt == nil {
		t.Error("the moved pane was disposed along with its dissolved tab")
	}
}

// When the target is rebuilt BEFORE the source, the pane sits in both trees
// until the source prunes it. That must end with exactly one tree holding it
// and the model alive.
func TestMovedPane_TargetBeforeSourceInRebuildOrder(t *testing.T) {
	t.Parallel()
	m := newMovePaneModel(t, 120, 40)
	m = mpApply(t, m, mpState("tab-src",
		mpTab{"tab-tgt", []string{"p3"}}, mpTab{"tab-src", []string{"p1", "p2"}}))
	moved := mpPane(t, mpTabOf(t, &m, "tab-src"), "p2")

	m = mpApply(t, m, mpState("tab-src",
		mpTab{"tab-tgt", []string{"p3", "p2"}}, mpTab{"tab-src", []string{"p1"}}))

	holders := 0
	for _, tab := range m.allTabs() {
		if tab.Root != nil && tab.Root.FindLeaf("p2") != nil {
			holders++
			if tab.ID != "tab-tgt" {
				t.Errorf("p2 is held by %s", tab.ID)
			}
		}
	}
	if holders != 1 {
		t.Errorf("p2 is in %d trees, want exactly 1", holders)
	}
	if moved.vt == nil {
		t.Error("the moved pane was disposed")
	}
}

// focusedSourceFixture: a 3-pane source in focus mode on p2, plus a target.
// Three panes so the "tree reduced to one leaf" exit cannot mask the one
// under test.
func focusedSourceFixture(t *testing.T) Model {
	t.Helper()
	m := newMovePaneModel(t, 120, 40)
	m = mpApply(t, m, mpState("tab-src",
		mpTab{"tab-src", []string{"p1", "p2", "p3"}}, mpTab{"tab-tgt", []string{"p4"}}))
	src := mpTabOf(t, &m, "tab-src")
	src.ActivePane = "p2"
	src.ToggleFocus()
	if !src.FocusMode() {
		t.Fatal("setup: the source did not enter focus mode")
	}
	return m
}

func TestMovedPane_ExitsSourceFocusModeWhenFocusedPaneLeaves(t *testing.T) {
	t.Parallel()
	m := focusedSourceFixture(t)

	m = mpApply(t, m, mpState("tab-src",
		mpTab{"tab-src", []string{"p1", "p3"}}, mpTab{"tab-tgt", []string{"p4", "p2"}}))

	if mpTabOf(t, &m, "tab-src").FocusMode() {
		t.Error("the source still full-screens a pane nobody chose after its focused pane moved out")
	}
}

// A DESTROYED pane is absent from the broadcast entirely, and keeps today's
// behaviour: focus mode stays on.
func TestDestroyedPane_KeepsFocusModeBehaviour(t *testing.T) {
	t.Parallel()
	m := focusedSourceFixture(t)

	m = mpApply(t, m, mpState("tab-src",
		mpTab{"tab-src", []string{"p1", "p3"}}, mpTab{"tab-tgt", []string{"p4"}}))

	if !mpTabOf(t, &m, "tab-src").FocusMode() {
		t.Error("destroying the focused pane left focus mode — only a MOVE should")
	}
}

func TestMovedPane_ExitsTargetFocusMode(t *testing.T) {
	t.Parallel()
	m := newMovePaneModel(t, 120, 40)
	m = mpApply(t, m, mpState("tab-src",
		mpTab{"tab-src", []string{"p1", "p2"}}, mpTab{"tab-tgt", []string{"p3", "p4"}}))
	tgt := mpTabOf(t, &m, "tab-tgt")
	tgt.ActivePane = "p3"
	tgt.ToggleFocus()
	if !tgt.FocusMode() {
		t.Fatal("setup: the target did not enter focus mode")
	}

	m = mpApply(t, m, mpState("tab-src",
		mpTab{"tab-src", []string{"p1"}}, mpTab{"tab-tgt", []string{"p3", "p4", "p2"}}))

	if mpTabOf(t, &m, "tab-tgt").FocusMode() {
		t.Error("the target still full-screens another pane after a pane moved in")
	}
}

func TestMovedPane_NotesModeExitsWhenBoundPaneLeavesActiveTab(t *testing.T) {
	t.Parallel()
	m := newMovePaneModel(t, 120, 40)
	m = mpApply(t, m, mpState("tab-src", mpBefore...))
	src := mpTabOf(t, &m, "tab-src")
	src.ActivePane = "p2"

	dir := t.TempDir()
	ne, err := NewNotesEditor(dir, "p2", "Shell", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	ne.HandleKey("x")
	m.notesMode = true
	m.notesEditor = ne

	m = mpApply(t, m, mpState("tab-src", mpAfter...))

	if m.notesMode || m.notesEditor != nil {
		t.Errorf("notes mode survived its bound pane leaving the active tab (mode=%v editor=%v)",
			m.notesMode, m.notesEditor != nil)
	}
	got, err := persist.LoadNotes(dir, "p2")
	if err != nil {
		t.Fatalf("LoadNotes: %v", err)
	}
	if got != "x\n" {
		t.Errorf("notes on disk = %q, want %q — the teardown did not flush", got, "x\n")
	}
}

// armOwnWorktreeCreate drives THIS client's worktree create into the active
// tab through handleCreatePaneSplit, stopping before the daemon has answered:
// the placeholder is reserved and worktreeCreates is armed. dialogCursor 0 is
// a left|right split, 2 a replace.
func armOwnWorktreeCreate(t *testing.T, m Model, dialogCursor int) Model {
	t.Helper()
	prev := createPaneTimeout
	createPaneTimeout = time.Millisecond
	t.Cleanup(func() { createPaneTimeout = prev })
	t.Setenv("QUIL_HOME", t.TempDir())

	m.client = &fakeSender{}
	m.pluginRegistry = plugin.NewRegistry()
	m.selectedPlugin = "terminal"
	m.worktreeNewBranch = "feat-x"
	m.worktrees = worktreeState{loaded: true, repo: true, root: "/repo"}
	m.dialogCursor = dialogCursor

	updated, _ := m.handleCreatePaneSplit()
	got := updated.(Model)
	tabID := got.activeTabModel().ID
	if got.pendingSplit[tabID] == nil || got.worktreeCreates[tabID] != "feat-x" {
		t.Fatalf("setup: the worktree create did not arm (pendingSplit=%v worktreeCreates=%q)",
			got.pendingSplit[tabID], got.worktreeCreates[tabID])
	}
	return got
}

// The worktree create's fixture: the TARGET is the active tab and holds [p3];
// the source [p1 p2] is in the background, and p2 is moved by another client.
var (
	mpWtBefore = []mpTab{{"tab-src", []string{"p1", "p2"}}, {"tab-tgt", []string{"p3"}}}
	mpWtAfter  = []mpTab{{"tab-src", []string{"p1"}}, {"tab-tgt", []string{"p3", "p2"}}}
)

func TestMovedPane_NeverFillsAWorktreePlaceholder(t *testing.T) {
	m := newMovePaneModel(t, 120, 40)
	m = mpApply(t, m, mpState("tab-tgt", mpWtBefore...))
	m = armOwnWorktreeCreate(t, m, 0)
	ph := m.pendingSplit["tab-tgt"]

	m = mpApply(t, m, mpState("tab-tgt", mpWtAfter...))

	tgt := mpTabOf(t, &m, "tab-tgt")
	if ph.Pane != nil {
		t.Fatalf("the moved pane consumed the worktree reservation (it holds %s)", ph.Pane.ID)
	}
	if !treeHoldsNode(tgt.Root, ph) {
		t.Error("the worktree placeholder was detached from the target's tree")
	}
	if m.pendingSplit["tab-tgt"] != ph {
		t.Error("pendingSplit no longer names the reserved leaf")
	}
	if m.worktreeCreates["tab-tgt"] != "feat-x" {
		t.Errorf("worktreeCreates = %q, want feat-x — the create lost its bookkeeping", m.worktreeCreates["tab-tgt"])
	}
	// Placed by the spiral rule, which skips the reservation: p3 is the last
	// PANE leaf, its parent splits left|right, so p2 goes under it and the
	// reservation keeps its own half.
	if got, want := mpTreeOf(t, &m, "tab-tgt"), "((p3/p2)|·)"; got != want {
		t.Errorf("target tree = %s, want %s", got, want)
	}
	if tgt.Root.Right != ph {
		t.Error("the reservation moved out of its own half")
	}
}

// This client's own worktree create is still pending in the target (the
// daemon has not even started the add) when another client's move lands
// there. The moved pane is placed around the reservation, and the worktree
// pane that finally arrives still fills it.
func TestMovedPane_OwnPendingWorktreeCreateStillFillsItsReservation(t *testing.T) {
	m := newMovePaneModel(t, 120, 40)
	m = mpApply(t, m, mpState("tab-tgt", mpWtBefore...))
	m = armOwnWorktreeCreate(t, m, 0)
	ph := m.pendingSplit["tab-tgt"]

	m = mpApply(t, m, mpState("tab-tgt", mpWtAfter...))
	if ph.Pane != nil || m.worktreeCreates["tab-tgt"] == "" {
		t.Fatal("setup: the move consumed the reservation — see TestMovedPane_NeverFillsAWorktreePlaceholder")
	}

	m = mpApply(t, m, mpState("tab-tgt",
		mpTab{"tab-src", []string{"p1"}}, mpTab{"tab-tgt", []string{"p3", "p2", "p-wt"}}))

	tgt := mpTabOf(t, &m, "tab-tgt")
	if ph.Pane == nil || ph.Pane.ID != "p-wt" {
		t.Fatalf("the worktree pane did not fill its reservation (it holds %v)", ph.Pane)
	}
	if !treeHoldsNode(tgt.Root, ph) {
		t.Error("the worktree pane landed on a detached leaf — it is live but visible nowhere")
	}
	if _, armed := m.pendingSplit["tab-tgt"]; armed {
		t.Error("pendingSplit still armed after the worktree pane landed")
	}
	if m.worktreeCreates["tab-tgt"] != "" {
		t.Error("worktreeCreates still armed after the worktree pane landed")
	}
	for _, id := range []string{"p3", "p2"} {
		if tgt.Root.FindLeaf(id) == nil {
			t.Errorf("pane %s left the target", id)
		}
	}
}

// A worktree REPLACE on a single-pane target leaves the root itself as the
// reservation. The moved pane must be placed beside it, not over it.
func TestMovedPane_KeepsASinglePaneWorktreeReplaceReservation(t *testing.T) {
	m := newMovePaneModel(t, 120, 40)
	m = mpApply(t, m, mpState("tab-tgt", mpWtBefore...))
	m = armOwnWorktreeCreate(t, m, 2)
	ph := m.pendingSplit["tab-tgt"]
	if mpTabOf(t, &m, "tab-tgt").Root != ph {
		t.Fatal("setup: the replace did not leave the root as the reservation")
	}
	held := m.worktreeReplaced["tab-tgt"]

	// The daemon has not swapped yet, so it still lists p3 there.
	m = mpApply(t, m, mpState("tab-tgt", mpWtAfter...))

	tgt := mpTabOf(t, &m, "tab-tgt")
	if !treeHoldsNode(tgt.Root, ph) || ph.Pane != nil {
		t.Fatal("the moved pane overwrote the root reservation")
	}
	if tgt.Root.FindLeaf("p2") == nil {
		t.Error("the moved pane is not in the target")
	}
	if m.worktreeCreates["tab-tgt"] != "feat-x" || m.worktreeReplaced["tab-tgt"] != held {
		t.Error("the replace lost its bookkeeping")
	}

	// The swap: p3 is gone daemon-side and p-wt stands in its place.
	m = mpApply(t, m, mpState("tab-tgt",
		mpTab{"tab-src", []string{"p1"}}, mpTab{"tab-tgt", []string{"p-wt", "p2"}}))

	tgt = mpTabOf(t, &m, "tab-tgt")
	if ph.Pane == nil || ph.Pane.ID != "p-wt" || !treeHoldsNode(tgt.Root, ph) {
		t.Fatalf("the replacing pane did not land in its reservation (holds %v)", ph.Pane)
	}
	if tgt.Root.FindLeaf("p2") == nil {
		t.Error("the moved pane left the target when the replace completed")
	}
}

// The two-client split race: this client split the target (an ordinary
// Alt+Shift+V, reservation armed) and, before its own pane arrives, another
// client's move lands there. The moved pane must not take the reservation —
// it would steal the tab's ActivePane and leave this client's pane with no
// leaf — and the reservation must survive the placeholder prune of that same
// broadcast so its own pane can still fill it.
func TestMovedPane_NeverFillsAnOrdinaryPendingSplit(t *testing.T) {
	t.Parallel()
	m := newMovePaneModel(t, 120, 40)
	m = mpApply(t, m, mpState("tab-tgt", mpWtBefore...))
	_ = m.splitPane(SplitVertical)
	ph := m.pendingSplit["tab-tgt"]
	if ph == nil {
		t.Fatal("setup: the split did not arm a placeholder")
	}

	m = mpApply(t, m, mpState("tab-tgt", mpWtAfter...))

	tgt := mpTabOf(t, &m, "tab-tgt")
	if ph.Pane != nil {
		t.Fatalf("the moved pane took this client's reservation (it holds %s)", ph.Pane.ID)
	}
	if !treeHoldsNode(tgt.Root, ph) || m.pendingSplit["tab-tgt"] != ph {
		t.Fatal("the reservation was pruned or forgotten — this client's own pane would land nowhere")
	}
	// Spiral, skipping the reservation: p3 is the last pane leaf and its
	// parent splits top|bottom, so p2 goes beside it.
	if got, want := mpTreeOf(t, &m, "tab-tgt"), "((p3|p2)/·)"; got != want {
		t.Errorf("target tree = %s, want %s", got, want)
	}
	if tgt.ActivePane != "p3" {
		t.Errorf("target ActivePane = %q, want p3 — this client is in the target, the move must not steal it", tgt.ActivePane)
	}

	// This client's own pane arrives on the next broadcast.
	m = mpApply(t, m, mpState("tab-tgt",
		mpTab{"tab-src", []string{"p1"}}, mpTab{"tab-tgt", []string{"p3", "p2", "p-own"}}))

	tgt = mpTabOf(t, &m, "tab-tgt")
	if ph.Pane == nil || ph.Pane.ID != "p-own" || !treeHoldsNode(tgt.Root, ph) {
		t.Fatalf("this client's own pane did not land in its reservation (it holds %v)", ph.Pane)
	}
	if _, armed := m.pendingSplit["tab-tgt"]; armed {
		t.Error("pendingSplit still armed after its pane landed")
	}
	if got, want := mpTreeOf(t, &m, "tab-tgt"), "((p3|p2)/p-own)"; got != want {
		t.Errorf("target tree = %s, want %s", got, want)
	}
	if tgt.ActivePane != "p-own" {
		t.Errorf("target ActivePane = %q, want p-own", tgt.ActivePane)
	}
}

// A pane this client never held — an MCP create, another client's split —
// keeps the legacy placement: first leaf, top|bottom.
func TestNewPaneFromElsewhere_StillUsesLegacyPlacement(t *testing.T) {
	t.Parallel()
	m := newMovePaneModel(t, 120, 40)
	m = mpApply(t, m, mpState("tab-src", mpBefore...))

	m = mpApply(t, m, mpState("tab-src",
		mpTab{"tab-src", []string{"p1", "p2"}}, mpTab{"tab-tgt", []string{"p3", "p9"}}))

	root := mpTabOf(t, &m, "tab-tgt").Root
	if root == nil || root.IsLeaf() {
		t.Fatalf("target root = %+v, want a split", root)
	}
	if root.Split != SplitVertical {
		t.Errorf("target split = %v, want SplitVertical — a new pane must not take the moved-pane rule", root.Split)
	}
	if root.Left == nil || !root.Left.IsLeaf() || root.Left.Pane.ID != "p3" ||
		root.Right == nil || !root.Right.IsLeaf() || root.Right.Pane.ID != "p9" {
		t.Errorf("target = %+v / %+v, want p3 over p9", root.Left, root.Right)
	}
}

// Two clients with the same prior state and geometry place the moved pane
// identically, and once one of them has pushed its tree the other agrees with
// what the daemon stores — no layout thrash.
//
// Nobody on either client asked for the move's placement, so neither sends on
// the move itself; the next broadcast, whose stored trees still predate it,
// makes each send once (layoutsync.go).
func TestMovedPane_TwoClientsConverge(t *testing.T) {
	t.Parallel()
	stored := func(st WorkspaceStateMsg) WorkspaceStateMsg {
		st.Tabs = append([]TabInfo(nil), st.Tabs...)
		for i := range st.Tabs {
			switch st.Tabs[i].ID {
			case "tab-src":
				st.Tabs[i].Layout = lsWire(t, lsSplit(SplitVertical, 0.5, lsLeaf("p1"), lsLeaf("p2")))
			case "tab-tgt":
				st.Tabs[i].Layout = lsWire(t, lsSplit(SplitVertical, 0.5, lsLeaf("p3"), lsLeaf("p4")))
			}
		}
		return st
	}
	before := stored(mpState("tab-src",
		mpTab{"tab-src", []string{"p1", "p2"}}, mpTab{"tab-tgt", []string{"p3", "p4"}}))
	after := stored(mpState("tab-src",
		mpTab{"tab-src", []string{"p1"}}, mpTab{"tab-tgt", []string{"p3", "p4", "p2"}}))

	a := newMovePaneModel(t, 120, 40)
	b := newMovePaneModel(t, 120, 40)
	a = mpApply(t, mpApply(t, a, before), after)
	b = mpApply(t, mpApply(t, b, before), after)

	treeA := SerializeLayout(mpTabOf(t, &a, "tab-tgt").Root)
	treeB := SerializeLayout(mpTabOf(t, &b, "tab-tgt").Root)
	if !reflect.DeepEqual(treeA, treeB) {
		t.Fatalf("the two clients placed the moved pane differently:\nA=%+v\nB=%+v", treeA, treeB)
	}

	a, sentA := lsApply(t, a, after)
	var pushed json.RawMessage
	for _, s := range sentA {
		if s.TabID == "tab-tgt" {
			pushed = s.Layout
		}
	}
	if pushed == nil {
		t.Fatal("setup: client A would not push the target's new tree")
	}

	third := after
	third.Tabs = append([]TabInfo(nil), after.Tabs...)
	for i := range third.Tabs {
		if third.Tabs[i].ID == "tab-tgt" {
			third.Tabs[i].Layout = pushed
			third.Tabs[i].LayoutRev = 1
		}
	}
	b, sentB := lsApply(t, b, third)
	for _, s := range sentB {
		if s.TabID == "tab-tgt" {
			t.Error("client B disagrees with the tree client A stored — the two would re-send each other's layout forever")
		}
	}
	if got := SerializeLayout(mpTabOf(t, &b, "tab-tgt").Root); !reflect.DeepEqual(got, treeA) {
		t.Errorf("client B holds %+v, want A's stored %+v", got, treeA)
	}
}
