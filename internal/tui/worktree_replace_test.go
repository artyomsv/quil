package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// armedWorktreeReplace returns a model mid-flight on the REPLACE path: the
// active pane has been detached from its leaf and held back, and the request
// carrying the worktree spec has been sent. Returns the tab and the id of the
// pane being replaced.
func armedWorktreeReplace(t *testing.T) (Model, string, string) {
	t.Helper()
	m := newBranchModel(t)
	m.client = &fakeSender{}
	m.selectedPlugin = "terminal"
	m.selectedCWD = "/repo"
	m.worktreeNewBranch = "feat/x"
	m.dialogCursor = 2 // Replace current pane

	tab := m.curTabs()[0]
	oldPaneID := tab.ActivePaneModel().ID

	updated, _ := m.handleCreatePaneSplit()
	got := updated.(Model)
	if got.worktreeCreates[tab.ID] == "" {
		t.Fatal("the replace did not record its tab — nothing could unwind it")
	}
	return got, tab.ID, oldPaneID
}

// The wire proof that replace carries the spec at all. Without it the daemon
// runs its ordinary replace and the new pane lands in the browsed directory —
// silently, which is how the reported failure looked.
func TestReplace_SendsTheWorktreeSpec(t *testing.T) {
	m := newBranchModel(t)
	sender := &fakeSender{}
	m.client = sender
	m.selectedPlugin = "terminal"
	m.selectedCWD = "/repo"
	m.worktreeNewBranch = "feat/x"
	m.dialogCursor = 2

	oldPaneID := m.curTabs()[0].ActivePaneModel().ID
	_, cmd := m.handleCreatePaneSplit()
	runCmd(cmd)

	var payload *ipc.CreatePanePayload
	for _, msg := range sender.sent {
		if msg.Type != ipc.MsgCreatePane {
			continue
		}
		var decoded ipc.CreatePanePayload
		if err := msg.DecodePayload(&decoded); err != nil {
			t.Fatalf("decode: %v", err)
		}
		payload = &decoded
	}
	if payload == nil {
		t.Fatal("no create_pane was sent — the replace was refused")
	}
	if payload.ReplacePaneID != oldPaneID {
		t.Errorf("ReplacePaneID = %q, want %q", payload.ReplacePaneID, oldPaneID)
	}
	if payload.Worktree == nil {
		t.Fatal("the replace carried no worktree spec")
	}
	if payload.Worktree.Branch != "feat/x" {
		t.Errorf("branch = %q, want feat/x", payload.Worktree.Branch)
	}
	if payload.Worktree.RepoRoot != "/repo" {
		t.Errorf("repo root = %q, want the main checkout /repo", payload.Worktree.RepoRoot)
	}
}

// The ordering fix. An ordinary replace disposes the old pane at send time
// because the daemon destroys it immediately; a worktree replace is ANSWERED,
// and the daemon creates the worktree BEFORE touching the pane — so on failure
// the pane is still alive on both sides and must come back.
//
// Disposing at send time is what made this combination look unsafe enough to
// refuse. It was never the operation that was unsafe, only the timing.
func TestReplace_FailedAddPutsTheOldPaneBack(t *testing.T) {
	m, tabID, oldPaneID := armedWorktreeReplace(t)

	// Held, not disposed, while the request is in flight.
	if m.worktreeReplaced[tabID] == nil {
		t.Fatal("the replaced pane was not held — a failure could not restore it")
	}

	updated, _ := m.Update(createPaneRespMsg{Resp: ipc.CreatePaneRespPayload{
		TabID:    tabID,
		Error:    "fatal: a branch named 'feat/x' already exists",
		Worktree: &ipc.WorktreeSpec{RepoRoot: "/repo", Branch: "feat/x"},
	}})
	got := updated.(Model)

	tab := got.tabByID(tabID)
	if tab == nil {
		t.Fatal("the tab vanished")
	}
	if leaf := tab.Root.FindLeaf(oldPaneID); leaf == nil {
		t.Error("the replaced pane did not come back — a failed git add cost a live pane")
	}
	if got.worktreeReplaced[tabID] != nil {
		t.Error("the held pane outlived the request that armed it")
	}
	if _, ok := got.pendingSplit[tabID]; ok {
		t.Error("the pending split is still armed after a failed replace")
	}
	if got.flashText == "" {
		t.Error("the failure was not reported")
	}
}

// A timeout is the same situation: nothing proved the swap happened, and the
// pane we detached is still ours. Restoring is recoverable; losing it is not.
func TestReplace_TimeoutPutsTheOldPaneBack(t *testing.T) {
	m, tabID, oldPaneID := armedWorktreeReplace(t)

	updated, _ := m.Update(createPaneTimeoutMsg{tabID: tabID})
	got := updated.(Model)

	tab := got.tabByID(tabID)
	if tab == nil {
		t.Fatal("the tab vanished")
	}
	if leaf := tab.Root.FindLeaf(oldPaneID); leaf == nil {
		t.Error("a timed-out replace lost the pane it detached")
	}
	if got.worktreeReplaced[tabID] != nil {
		t.Error("the held pane survived the timeout")
	}
}

// Success is the one path that must NOT restore: the daemon really did swap the
// pane out, so the held model describes something that no longer exists.
// Putting it back would paint a dead pane; keeping it leaks its emulator.
func TestReplace_SuccessDisposesTheOldPane(t *testing.T) {
	m, tabID, oldPaneID := armedWorktreeReplace(t)

	updated, _ := m.Update(createPaneRespMsg{Resp: ipc.CreatePaneRespPayload{
		TabID:    tabID,
		PaneID:   "pane-new00001",
		Worktree: &ipc.WorktreeSpec{RepoRoot: "/repo", Branch: "feat/x"},
	}})
	got := updated.(Model)

	if got.worktreeReplaced[tabID] != nil {
		t.Error("the replaced pane is still held after the daemon confirmed the swap")
	}
	if tab := got.tabByID(tabID); tab != nil && tab.Root.FindLeaf(oldPaneID) != nil {
		t.Error("a confirmed swap left the replaced pane in the layout")
	}
	// The placeholder must survive: the new pane arrives in the next broadcast
	// and fills the slot the old one vacated.
	if _, ok := got.pendingSplit[tabID]; !ok {
		t.Error("a successful replace unwound its own placeholder")
	}
}

// An ordinary replace (no worktree) must keep its old behaviour exactly:
// disposed at send time, no give-up tick, no held model.
func TestReplace_WithoutAWorktreeStillDisposesImmediately(t *testing.T) {
	m := newBranchModel(t)
	m.client = &fakeSender{}
	m.selectedPlugin = "terminal"
	m.selectedCWD = "/repo"
	m.worktreeNewBranch = "" // no worktree
	m.dialogCursor = 2

	tab := m.curTabs()[0]
	updated, _ := m.handleCreatePaneSplit()
	got := updated.(Model)

	if got.worktreeReplaced[tab.ID] != nil {
		t.Error("an ordinary replace held its old pane — the daemon destroys it immediately, so there is nothing to restore")
	}
	if got.worktreeCreates[tab.ID] != "" {
		t.Error("an ordinary replace armed the worktree bookkeeping")
	}
}

// A placeholder used to render as the empty string: IsLeaf() is `Pane != nil`,
// so it fell through to the split arm and joined two empty children. That was
// invisible while a create took microseconds, and became a BLACK RECTANGLE for
// the 16 s a `git worktree add` takes against a large repository — on the
// replace path with the old pane already gone, so the whole tab was blank with
// nothing to say why.
func TestRenderPendingPane_NamesTheBranchItIsWaitingOn(t *testing.T) {
	out := renderPendingPane("feat/login", 60, 10)
	if out == "" {
		t.Fatal("a pending create rendered nothing at all")
	}
	if !strings.Contains(out, "feat/login") {
		t.Errorf("the pending box does not name the branch:\n%s", out)
	}
	if !strings.Contains(out, "Creating worktree") {
		t.Errorf("the pending box does not say what it is doing:\n%s", out)
	}
	if h := lipgloss.Height(out); h != 10 {
		t.Errorf("pending box is %d rows, want 10 — it must fill its rect exactly", h)
	}
	if w := lipgloss.Width(out); w != 60 {
		t.Errorf("pending box is %d cells wide, want 60", w)
	}
}

// The branch reaches here having round-tripped through a remote daemon's
// listing, so it is sanitized like any other rendered remote value — and
// bounded, because sanitizing does not shorten and this box has a fixed width.
func TestRenderPendingPane_SanitizesAndBoundsTheBranch(t *testing.T) {
	out := renderPendingPane("boom\x1b]52;c;cGF5bG9hZA==\x07"+strings.Repeat("x", 400), 40, 8)
	if strings.Contains(out, "\x1b]52") {
		t.Error("an OSC 52 in the branch survived into the pending box")
	}
	if w := lipgloss.Width(out); w != 40 {
		t.Errorf("pending box is %d cells wide, want exactly 40 — an unbounded branch overflowed its rect", w)
	}
}

// A zero rect means no resize has run yet. Rendering nothing there is the
// pre-existing behaviour and strictly better than a zero-width box.
func TestRenderPendingPane_ZeroRectRendersNothing(t *testing.T) {
	if out := renderPendingPane("feat/x", 0, 0); out != "" {
		t.Errorf("a zero rect rendered %q", out)
	}
}

// The size comes from resizeNode, which used to skip placeholders entirely —
// so without this the box would render at 0x0 forever and the fix would be
// invisible in exactly the case it exists for.
func TestResizeNode_RecordsThePlaceholderRect(t *testing.T) {
	ph := &LayoutNode{}
	resizeNode(ph, 80, 24, 80, 0, 0)
	if ph.phW != 80 || ph.phH != 24 {
		t.Errorf("placeholder rect = %dx%d, want 80x24", ph.phW, ph.phH)
	}
}
