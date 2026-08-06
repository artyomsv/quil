package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

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
// the daemon answers. Also returns the tab the create belongs to.
func armedWorktreeCreate(t *testing.T) (Model, string) {
	t.Helper()
	m := newBranchModel(t)
	m.client = &fakeSender{}
	m.selectedPlugin = "terminal"
	m.selectedCWD = "/repo"
	m.worktreeNewBranch = "feat/x"
	m.dialogCursor = 0
	tabID := m.curTabs()[0].ID

	updated, _ := m.handleCreatePaneSplit()
	got := updated.(Model)
	if !got.worktreeCreates[tabID] {
		t.Fatal("the create did not record its tab — nothing could unwind it")
	}
	if len(got.pendingSplit) == 0 {
		t.Fatal("the create armed no pending split")
	}
	return got, tabID
}

// wtResp builds a daemon answer carrying the echoed keys the client correlates
// on. A response with no spec belongs to somebody else's create.
func wtResp(tabID, errMsg string) createPaneRespMsg {
	return createPaneRespMsg{Resp: ipc.CreatePaneRespPayload{
		TabID:    tabID,
		Error:    errMsg,
		Worktree: &ipc.WorktreeSpec{RepoRoot: "/repo", Branch: "feat/x"},
	}}
}

// A failed add must unwind the placeholder the client armed before the send.
func TestCreatePaneResp_FailureUnwindsThePlaceholder(t *testing.T) {
	m, tabID := armedWorktreeCreate(t)

	updated, _ := m.Update(wtResp(tabID, "fatal: '/wt/feat-x' already exists"))
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
	m, tabID := armedWorktreeCreate(t)

	updated, _ := m.Update(wtResp(tabID, "already used by worktree '/x/feat-y'"))
	if !strings.Contains(updated.(Model).flashText, "/x/feat-y") {
		t.Errorf("flash %q drops git's own message", updated.(Model).flashText)
	}
}

// Sanitized AND bounded — two different jobs. sanitizeRemoteText removes
// escapes without shortening anything, and the status bar drops its whole
// right half rather than wrapping when a flash outgrows it.
func TestCreatePaneResp_SanitizesAndBoundsTheError(t *testing.T) {
	m, tabID := armedWorktreeCreate(t)

	updated, _ := m.Update(wtResp(tabID, "boom \x1b]52;c;cGF5bG9hZA==\x07"+strings.Repeat("x", 4000)))
	flash := updated.(Model).flashText
	if strings.Contains(flash, "\x1b]52") {
		t.Error("an OSC 52 in the daemon's error survived into the flash")
	}
	if w := lipgloss.Width(flash); w > createErrFlashCap+40 {
		t.Errorf("flash is %d cells, unbounded", w)
	}
}

// SUCCESS leaves the placeholder alone: the pane arrives in the next workspace
// broadcast and fills it. Pruning here would delete the slot it lands in.
func TestCreatePaneResp_SuccessLeavesThePlaceholder(t *testing.T) {
	m, tabID := armedWorktreeCreate(t)

	updated, _ := m.Update(wtResp(tabID, ""))
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
	m, tabID := armedWorktreeCreate(t)
	_, cmd := m.Update(wtResp(tabID, ""))
	if cmd == nil {
		t.Error("no command returned — the IPC listen loop would die for the session")
	}
}

// TWO creates in flight, answered OUT OF ORDER — the case a single-slot cursor
// gets wrong. The daemon's single-flight rejects the second create INSTANTLY
// while the first is still checking out, so this ordering is the common one,
// not a rare race. Each response must unwind its OWN tab.
func TestCreatePaneResp_TwoCreatesUnwindTheirOwnTabs(t *testing.T) {
	m, tabA := armedWorktreeCreate(t)

	// A second create, on a different tab, in flight alongside the first.
	tabB := "tab-second"
	m.worktreeCreates[tabB] = true
	m.pendingSplit[tabB] = &LayoutNode{}

	// B is rejected immediately and answers FIRST.
	updated, _ := m.Update(wtResp(tabB, "another worktree is being created"))
	got := updated.(Model)
	if _, ok := got.pendingSplit[tabB]; ok {
		t.Error("B's placeholder was not unwound by B's own response")
	}
	if _, ok := got.pendingSplit[tabA]; !ok {
		t.Error("B's response unwound A's LIVE placeholder — A's pane would have nowhere to land")
	}

	// A's real failure lands later and must still find its own record.
	updated, _ = got.Update(wtResp(tabA, "fatal: cannot create worktree"))
	got = updated.(Model)
	if _, ok := got.pendingSplit[tabA]; ok {
		t.Error("A's placeholder was stranded — every later pane in that tab would be swallowed")
	}
}

// A response for a tab with no create in flight must be inert, so a duplicate
// or late answer cannot unwind a placeholder a LATER create owns.
func TestCreatePaneResp_UnknownTabIsInert(t *testing.T) {
	m, tabA := armedWorktreeCreate(t)

	updated, _ := m.Update(wtResp("tab-nobody", "boom"))
	got := updated.(Model)

	if _, ok := got.pendingSplit[tabA]; !ok {
		t.Error("a response for an unrelated tab unwound A's placeholder")
	}
	if got.flashText != "" {
		t.Errorf("an unrelated response flashed %q", got.flashText)
	}
}

// The MCP bridge answers ordinary creates with the SAME message type and no
// worktree spec. Acting on one would unwind a placeholder nobody armed.
func TestCreatePaneResp_NonWorktreeResponseIsIgnored(t *testing.T) {
	m, tabA := armedWorktreeCreate(t)

	updated, _ := m.Update(createPaneRespMsg{Resp: ipc.CreatePaneRespPayload{
		TabID: tabA, Error: "something else failed",
	}})
	got := updated.(Model)

	if _, ok := got.pendingSplit[tabA]; !ok {
		t.Error("a create_pane_resp with no worktree spec unwound a worktree placeholder")
	}
}

// A destroyed tab must not panic the response handler: the tab can be closed
// client-side during the seconds an add takes.
func TestCreatePaneResp_DestroyedTabDoesNotPanic(t *testing.T) {
	m, tabID := armedWorktreeCreate(t)
	if p := m.cur(); p != nil {
		p.tabs = nil // as closing the tab would
	}

	updated, _ := m.Update(wtResp(tabID, "fatal: cannot create worktree"))
	got := updated.(Model)

	if _, ok := got.pendingSplit[tabID]; ok {
		t.Error("the pending split survived a response for a destroyed tab")
	}
}

// A never-answered create must not leave the tab wedged behind a placeholder.
func TestCreatePane_TimeoutUnwindsThePlaceholder(t *testing.T) {
	m, tabID := armedWorktreeCreate(t)

	updated, cmd := m.Update(createPaneTimeoutMsg{tabID: tabID})
	got := updated.(Model)

	if _, ok := got.pendingSplit[tabID]; ok {
		t.Error("a timed-out create left the placeholder armed")
	}
	// A LOCAL timer, so it must NOT re-arm the IPC listener.
	if cmd != nil {
		t.Error("the timeout arm returned a command; local timers must not re-arm the listener")
	}
}

// A tick belonging to a create that already answered must be inert.
func TestCreatePane_StaleTimeoutIsInert(t *testing.T) {
	m, tabID := armedWorktreeCreate(t)

	updated, _ := m.Update(wtResp(tabID, ""))
	got := updated.(Model)
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
// placeholder the daemon is about to fill.
func TestCreatePaneTimeout_ExceedsTheDaemonAddTimeout(t *testing.T) {
	if createPaneTimeout <= worktreeAddTimeout {
		t.Errorf("createPaneTimeout (%v) must exceed worktreeAddTimeout (%v)", createPaneTimeout, worktreeAddTimeout)
	}
}
