package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// Regression tests for the defects a code review found in the worktree-replace
// work. Each one fails against the code as it was reviewed.

// wsBroadcast builds a realistic workspace_state for a tab mid-replace: every
// pane still in the layout, PLUS whatever extra ids the caller names.
//
// Listing only the extras would drop the tab's surviving panes and restructure
// the tree, which is a different scenario (panes closed elsewhere) and would
// make these tests fail for a reason that has nothing to do with the bug.
//
// A worktree replace runs for SECONDS, so ordinary broadcasts land inside it:
// the git-fingerprint ticker alone fires every 5 s, and a child toggling mouse
// modes or another client will do it too.
func wsBroadcast(tab *TabModel, extra ...string) WorkspaceStateMsg {
	ids := make([]string, 0, len(extra)+2)
	for _, leaf := range tab.Leaves() {
		ids = append(ids, leaf.ID)
	}
	ids = append(ids, extra...)
	panes := make([]PaneInfo, 0, len(ids))
	for _, id := range ids {
		panes = append(panes, PaneInfo{ID: id, TabID: tab.ID, Type: "terminal"})
	}
	return WorkspaceStateMsg{
		ActiveTab: tab.ID,
		Tabs:      []TabInfo{{ID: tab.ID, Name: tab.Name, Panes: ids}},
		Panes:     panes,
	}
}

// The critical one. While the add runs the daemon has NOT swapped yet, so it
// keeps reporting the old pane id — which is absent from the tree because the
// client detached it. rebuildTabs read that as a NEW pane: it built an empty
// PaneModel for a live one, consumed the reserved leaf, and cleared
// worktreeCreates. Both settling paths gate on that map, so the held model was
// leaked for the session and the reserved leaf was gone.
func TestReplace_MidFlightBroadcastKeepsTheBookkeeping(t *testing.T) {
	m, tabID, oldPaneID := armedWorktreeReplace(t)

	m.applyWorkspaceState(wsBroadcast(m.tabByID(tabID), oldPaneID), "")

	if m.worktreeCreates[tabID] == "" {
		t.Error("a mid-flight broadcast cleared the create — neither the response nor the timeout can settle it now")
	}
	if m.worktreeReplaced[tabID] == nil {
		t.Error("a mid-flight broadcast dropped the held pane — it can no longer be restored or disposed")
	}
	if _, ok := m.pendingSplit[tabID]; !ok {
		t.Error("a mid-flight broadcast consumed the reserved leaf — the new pane would land elsewhere")
	}

	// And the failure path must still work afterwards.
	updated, _ := m.Update(createPaneRespMsg{Resp: ipc.CreatePaneRespPayload{
		TabID:    tabID,
		Error:    "fatal: a branch named 'feat/x' already exists",
		Worktree: &ipc.WorktreeSpec{RepoRoot: "/repo", Branch: "feat/x"},
	}})
	got := updated.(Model)
	if tab := got.tabByID(tabID); tab == nil || tab.Root.FindLeaf(oldPaneID) == nil {
		t.Error("after a mid-flight broadcast the failure path could no longer restore the pane")
	}
}

// Success arrives as a BROADCAST before the response — the daemon calls
// broadcastState() and then respondTo, and both frames are must-deliver on one
// serial reader. So the broadcast that fills the leaf is what must dispose the
// held pane; leaving it to applyCreatePaneResp leaked the model on every
// successful replace, because that handler bails on the worktreeCreates entry
// the same broadcast just cleared.
func TestReplace_BroadcastFillingTheLeafDisposesTheHeldPane(t *testing.T) {
	m, tabID, _ := armedWorktreeReplace(t)
	held := m.worktreeReplaced[tabID]
	if held == nil || held.vt == nil {
		t.Fatal("fixture: the held pane has no VT to observe")
	}

	m.applyWorkspaceState(wsBroadcast(m.tabByID(tabID), "pane-brandnew1"), "")

	if m.worktreeReplaced[tabID] != nil {
		t.Error("the held pane outlived the broadcast that confirmed the swap")
	}
	if held.vt != nil {
		t.Error("the held pane was dropped without Dispose() — its emulator and drain goroutine leak")
	}
}

// A replace on a SINGLE-pane tab detaches the root leaf, leaving a bare
// placeholder as the root. PrunePlaceholders only inspects a split node's
// CHILDREN, so it cannot repair that, and rebuildTabs' root-insert fallback did
// tab.Leaves()[0] — index out of range on an empty slice. Masked only because
// the broadcast used to refill the root in the same pass it consumed the leaf.
// Reached via the one settling path that leaves the leaf EMPTY: a swapped
// failure (the add succeeded, the swap happened, the PTY spawn failed). Nothing
// is restored, pendingSplit is dropped, and the root is left a bare
// placeholder — so the next new pane takes the root-insert fallback and finds
// no leaves to split.
func TestReplace_RootPlaceholderDoesNotPanicOnTheNextBroadcast(t *testing.T) {
	m := newBranchModel(t)
	m.client = &fakeSender{}
	m.selectedPlugin = "terminal"
	m.selectedCWD = "/repo"
	m.worktreeNewBranch = "feat/x"
	m.dialogCursor = 2

	tab := m.curTabs()[0]
	for _, extra := range tab.Leaves()[1:] {
		tab.RemovePane(extra.ID)
	}
	if got := len(tab.Leaves()); got != 1 {
		t.Fatalf("fixture has %d leaves, want 1", got)
	}
	tabID := tab.ID
	updated, _ := m.handleCreatePaneSplit()
	m = updated.(Model)

	// Swapped: the old pane is gone daemon-side, so nothing is put back and the
	// reserved leaf stays empty.
	updated, _ = m.Update(createPaneRespMsg{Resp: ipc.CreatePaneRespPayload{
		TabID:    tabID,
		Error:    "start PTY (dead pane removed): exec: not found",
		Swapped:  true,
		Worktree: &ipc.WorktreeSpec{RepoRoot: "/repo", Branch: "feat/x"},
	}})
	m = updated.(Model)
	if leaves := m.tabByID(tabID).Leaves(); len(leaves) != 0 {
		t.Fatalf("fixture: tab has %d leaves, want the bare-root-placeholder state", len(leaves))
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("a broadcast after a swapped-failure replace panicked: %v", r)
		}
	}()
	m.applyWorkspaceState(wsBroadcast(m.tabByID(tabID), "pane-brandnew1"), "")

	if leaves := m.tabByID(tabID).Leaves(); len(leaves) != 1 {
		t.Errorf("the tab has %d leaves after the broadcast, want the new pane to have landed", len(leaves))
	}
}

// A second create on the same tab overwrote worktreeCreates, worktreeReplaced
// and pendingSplit at once — leaking the first held pane and pointing the
// first's response at the second's leaf. Reachable from the keyboard: the
// dialog closes on submit, and ActivePaneModel falls back to another leaf once
// the first replace detaches its pane.
func TestCreatePaneSplit_RefusesASecondCreateWhileOneIsInFlight(t *testing.T) {
	m, tabID, _ := armedWorktreeReplace(t)
	firstHeld := m.worktreeReplaced[tabID]
	firstLeaf := m.pendingSplit[tabID]

	m.selectedPlugin = "terminal"
	m.selectedCWD = "/repo"
	m.worktreeNewBranch = "feat/second"
	m.dialogCursor = 0 // a SPLIT, so this cannot be read as a replace-only guard
	sender := &fakeSender{}
	m.client = sender
	// handleCreatePaneSplit TEARS DOWN the worktree listing on submit, so the
	// first create left worktrees.root empty. Restoring it is what makes the
	// second create reach the in-flight guard instead of being turned away by
	// the repository-root-unknown one — which refuses too, and would let this
	// test pass without the guard it exists to pin.
	m.worktrees = worktreeState{loaded: true, repo: true, root: "/repo"}

	updated, _ := m.handleCreatePaneSplit()
	got := updated.(Model)

	if len(sender.sent) != 0 {
		t.Error("a second worktree create was sent while one was in flight")
	}
	if !strings.Contains(got.flashText, "feat/x") {
		t.Errorf("flash %q does not name the create already running", got.flashText)
	}
	if got.worktreeCreates[tabID] != "feat/x" {
		t.Errorf("worktreeCreates = %q, want the FIRST create's branch", got.worktreeCreates[tabID])
	}
	if got.worktreeReplaced[tabID] != firstHeld {
		t.Error("the second create replaced the first held pane — the first is now leaked")
	}
	if got.pendingSplit[tabID] != firstLeaf {
		t.Error("the second create stole the first reserved leaf")
	}
}

// Swapped is a statement about what happened and cannot be inferred from Error:
// the swap precedes the new pane's PTY spawn, so a spawn failure is an error
// with the old pane already destroyed. Restoring there puts a pane the daemon
// no longer has back into the layout, and keystrokes aimed at it are dropped
// until a later broadcast prunes the leaf.
func TestCreatePaneResp_SwappedFailureDoesNotRestoreADeadPane(t *testing.T) {
	m, tabID, oldPaneID := armedWorktreeReplace(t)
	held := m.worktreeReplaced[tabID]

	updated, _ := m.Update(createPaneRespMsg{Resp: ipc.CreatePaneRespPayload{
		TabID:    tabID,
		Error:    `start PTY (dead pane removed): exec: "claude": not found`,
		Swapped:  true,
		Worktree: &ipc.WorktreeSpec{RepoRoot: "/repo", Branch: "feat/x"},
	}})
	got := updated.(Model)

	if tab := got.tabByID(tabID); tab != nil && tab.Root.FindLeaf(oldPaneID) != nil {
		t.Error("a pane the daemon had already destroyed was restored into the layout")
	}
	if got.worktreeReplaced[tabID] != nil {
		t.Error("the held pane is still held after a swapped failure")
	}
	if held != nil && held.vt != nil {
		t.Error("the held pane was neither restored nor disposed — it leaks")
	}
}

// An UNSWAPPED failure is the ordinary one — git refused the branch before the
// daemon touched anything — and must still restore. Stated separately so a fix
// that simply stopped restoring would not pass.
func TestCreatePaneResp_UnswappedFailureStillRestores(t *testing.T) {
	m, tabID, oldPaneID := armedWorktreeReplace(t)

	updated, _ := m.Update(createPaneRespMsg{Resp: ipc.CreatePaneRespPayload{
		TabID:    tabID,
		Error:    "fatal: a branch named 'feat/x' already exists",
		Worktree: &ipc.WorktreeSpec{RepoRoot: "/repo", Branch: "feat/x"},
	}})
	got := updated.(Model)

	if tab := got.tabByID(tabID); tab == nil || tab.Root.FindLeaf(oldPaneID) == nil {
		t.Error("a branch git refused cost the user a live pane")
	}
}

// renderPendingPane must FIT its rect. lipgloss Width/Height pad but do not
// truncate, and the 18-cell "Creating worktree " prefix was never counted —
// only the branch was. A box taller than the rect resizeNode recorded pushes
// every sibling below it down, the row-shifting family of bug this codebase has
// shipped before.
func TestRenderPendingPane_FitsItsRectAtEverySize(t *testing.T) {
	for _, tc := range []struct{ w, h int }{
		{10, 4}, // the documented minimum leaf size
		{8, 4}, {6, 4}, {3, 4}, {1, 4},
		{12, 2}, {12, 1}, {20, 3}, {80, 24},
	} {
		out := renderPendingPane("feat/some-quite-long-branch-name", tc.w, tc.h)
		if w := lipgloss.Width(out); w > tc.w {
			t.Errorf("%dx%d: rendered %d cells wide, want <= %d", tc.w, tc.h, w, tc.w)
		}
		if h := lipgloss.Height(out); h > tc.h {
			t.Errorf("%dx%d: rendered %d rows, want <= %d", tc.w, tc.h, h, tc.h)
		}
	}
}

// An ORDINARY split arms a placeholder too, and renderNode reaches this for any
// childless nil-Pane node. Claiming a worktree is being created there is a
// confidently wrong answer about what the pane is waiting for — and an ordinary
// create over ssh is not microseconds.
func TestRenderPendingPane_OrdinarySplitSaysNothingAboutWorktrees(t *testing.T) {
	out := renderPendingPane("", 60, 10)
	if strings.Contains(out, "Creating worktree") {
		t.Errorf("an ordinary split placeholder claims a worktree is being created:\n%q", out)
	}
	if lipgloss.Width(out) != 60 || lipgloss.Height(out) != 10 {
		t.Errorf("the blank placeholder is %dx%d, want 60x10", lipgloss.Width(out), lipgloss.Height(out))
	}
}

// The same thing through the real render path rather than the helper alone.
func TestTabView_OrdinarySplitPlaceholderSaysNothingAboutWorktrees(t *testing.T) {
	m := newBranchModel(t)
	tab := m.curTabs()[0]
	tab.SplitAtPane(tab.Leaves()[0].ID, SplitHorizontal)
	tab.Resize(100, 38)

	if strings.Contains(tab.View(), "Creating worktree") {
		t.Error("an ordinary split rendered a worktree-creation message")
	}
}
