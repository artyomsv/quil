package tui

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// countPlaceholders walks the tree for leaves with no pane — what a pending
// split leaves behind until the created pane arrives.
func countPlaceholders(n *LayoutNode) int {
	if n == nil {
		return 0
	}
	if n.IsLeaf() {
		if n.Pane == nil {
			return 1
		}
		return 0
	}
	return countPlaceholders(n.Left) + countPlaceholders(n.Right)
}

// armedWorktreeCreate returns a model mid-flight: the layout has been split
// and pendingSplit armed, exactly as handleCreatePaneSplit leaves it before
// the daemon answers.
func armedWorktreeCreate(t *testing.T) Model {
	t.Helper()
	m := newBranchModel(t)
	m.client = &fakeSender{}
	m.selectedPlugin = "terminal"
	m.selectedCWD = "/repo"
	m.worktreeNewBranch = "feat/x"
	m.dialogCursor = 0

	updated, cmd := m.handleCreatePaneSplit()
	_ = cmd
	got := updated.(Model)
	if got.worktreeCreateTab == "" {
		t.Fatal("the create did not record its tab — nothing could unwind it")
	}
	if len(got.pendingSplit) == 0 {
		t.Fatal("the create armed no pending split")
	}
	return got
}

// A failed add must unwind the placeholder the client armed before the send.
// Nothing else will: applyWorkspaceState only FILLS placeholders, it never
// retires one whose pane never arrives — so the tab would keep a dead leaf
// that swallows the next pane created anywhere in it.
func TestCreatePaneResp_FailureUnwindsThePlaceholder(t *testing.T) {
	m := armedWorktreeCreate(t)
	tabID := m.worktreeCreateTab

	updated, _ := m.Update(createPaneRespMsg{Resp: ipc.CreatePaneRespPayload{
		Error:    "fatal: '/wt/feat-x' already exists",
		Worktree: &ipc.WorktreeSpec{RepoRoot: "/repo", Branch: "feat/x"},
	}})
	got := updated.(Model)

	if _, ok := got.pendingSplit[tabID]; ok {
		t.Error("the pending split is still armed after a failed create")
	}
	if tab := got.tabByID(tabID); tab != nil && countPlaceholders(tab.Root) != 0 {
		t.Error("a placeholder leaf survived a failed create")
	}
	if got.flashText == "" {
		t.Error("the failure was not reported to the user")
	}
}

// git's own stderr reaches the user: "already used by worktree '/x/feat-y'"
// names the pane to go look at, and no message Quil could invent would.
func TestCreatePaneResp_ShowsGitsOwnError(t *testing.T) {
	m := armedWorktreeCreate(t)

	updated, _ := m.Update(createPaneRespMsg{Resp: ipc.CreatePaneRespPayload{
		Error: "already used by worktree '/x/feat-y'",
	}})
	if !strings.Contains(updated.(Model).flashText, "/x/feat-y") {
		t.Errorf("flash %q drops git's own message", updated.(Model).flashText)
	}
}

// The message comes from a daemon the user may not control under --remote.
func TestCreatePaneResp_SanitizesTheError(t *testing.T) {
	m := armedWorktreeCreate(t)

	updated, _ := m.Update(createPaneRespMsg{Resp: ipc.CreatePaneRespPayload{
		Error: "boom \x1b]52;c;cGF5bG9hZA==\x07",
	}})
	if strings.Contains(updated.(Model).flashText, "\x1b]52") {
		t.Error("an OSC 52 in the daemon's error survived into the flash")
	}
}

// SUCCESS leaves the placeholder alone: the pane arrives in the next workspace
// broadcast and fills it, exactly as an ordinary create does. Pruning here
// would delete the slot the pane is about to land in.
func TestCreatePaneResp_SuccessLeavesThePlaceholder(t *testing.T) {
	m := armedWorktreeCreate(t)
	tabID := m.worktreeCreateTab

	updated, _ := m.Update(createPaneRespMsg{Resp: ipc.CreatePaneRespPayload{PaneID: "pane-0000000f"}})
	got := updated.(Model)

	if _, ok := got.pendingSplit[tabID]; !ok {
		t.Error("a successful create unwound its own placeholder — the pane would have nowhere to land")
	}
	if got.flashText != "" {
		t.Errorf("a successful create flashed %q", got.flashText)
	}
}

// Every IPC response arm must re-arm the listener or the loop dies for the
// session — a bug this package has shipped before.
func TestCreatePaneResp_ReArmsTheListener(t *testing.T) {
	m := armedWorktreeCreate(t)
	_, cmd := m.Update(createPaneRespMsg{Resp: ipc.CreatePaneRespPayload{PaneID: "p9"}})
	if cmd == nil {
		t.Error("no command returned — the IPC listen loop would die for the session")
	}
}

// A never-answered create must not leave the tab wedged behind a placeholder.
func TestCreatePane_TimeoutUnwindsThePlaceholder(t *testing.T) {
	m := armedWorktreeCreate(t)
	tabID := m.worktreeCreateTab

	updated, cmd := m.Update(createPaneTimeoutMsg{tabID: tabID})
	got := updated.(Model)

	if _, ok := got.pendingSplit[tabID]; ok {
		t.Error("a timed-out create left the placeholder armed")
	}
	// A LOCAL timer, so it must NOT re-arm the IPC listener — doing so would
	// stack a second listen goroutine on every tick.
	if cmd != nil {
		t.Error("the timeout arm returned a command; local timers must not re-arm the listener")
	}
}

// A tick belonging to a create that already answered must be inert, or it
// unwinds a placeholder a LATER create is legitimately holding.
func TestCreatePane_StaleTimeoutIsInert(t *testing.T) {
	m := armedWorktreeCreate(t)
	tabID := m.worktreeCreateTab

	// The create answers first.
	updated, _ := m.Update(createPaneRespMsg{Resp: ipc.CreatePaneRespPayload{PaneID: "p9"}})
	got := updated.(Model)
	// Then its late tick arrives.
	updated, _ = got.Update(createPaneTimeoutMsg{tabID: tabID})
	got = updated.(Model)

	if _, ok := got.pendingSplit[tabID]; !ok {
		t.Error("a late tick unwound a placeholder the answered create still owns")
	}
	if got.flashText != "" {
		t.Errorf("a stale timeout flashed %q", got.flashText)
	}
}

// The client must give up LATER than the daemon does, or it prunes a
// placeholder the daemon is about to fill and the pane arrives with nowhere to
// go. Asserted rather than left as two independent numbers that drift.
func TestCreatePaneTimeout_ExceedsTheDaemonAddTimeout(t *testing.T) {
	if createPaneTimeout <= worktreeAddTimeout {
		t.Errorf("createPaneTimeout (%v) must exceed worktreeAddTimeout (%v)", createPaneTimeout, worktreeAddTimeout)
	}
}
