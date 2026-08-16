package tui

import (
	"strings"
	"testing"
)

// armedCreate submits the create dialog and returns the model plus the tab the
// placeholder was armed in. plugin/branch/cursor are the three choices that
// decide which placeholder is produced.
func armedCreate(t *testing.T, pluginName, branch string, cursor int) (Model, *TabModel) {
	t.Helper()
	m := newBranchModel(t)
	m.client = &fakeSender{}
	m.selectedPlugin = pluginName
	m.selectedCWD = "/repo"
	m.worktreeNewBranch = branch
	m.dialogCursor = cursor

	tabID := m.curTabs()[0].ID
	updated, _ := m.handleCreatePaneSplit()
	got := updated.(Model)
	tab := got.tabByID(tabID)
	if tab == nil {
		t.Fatal("the tab vanished")
	}
	tab.Resize(100, 38)
	return got, tab
}

// The placeholder must NAME the branch from the moment the request leaves, not
// from the first daemon broadcast after it.
//
// Nothing guarantees a broadcast during the checkout: handleCreatePane hands a
// worktree create to a worker and returns without broadcasting, and gitWatcher
// only broadcasts when a fingerprint actually moved. So on an idle tab the only
// broadcast is the one that arrives when the add has already FINISHED — and
// tab.CreatingBranch, whose sole writer is rebuildTabs, was still empty for the
// whole wait. renderPendingPane then drew its blank branch, which on the replace
// path is the entire tab.
func TestReplace_PlaceholderNamesTheBranchBeforeAnyBroadcast(t *testing.T) {
	_, tab := armedCreate(t, "terminal", "feat/x", 2)
	if !strings.Contains(tab.View(), "Creating worktree feat/x") {
		t.Errorf("the replace placeholder does not name the branch:\n%q", tab.View())
	}
}

// Same, one leaf down: a split's placeholder is a sibling rather than the whole
// tab, and it is blank for exactly as long.
func TestSplit_PlaceholderNamesTheBranchBeforeAnyBroadcast(t *testing.T) {
	_, tab := armedCreate(t, "terminal", "feat/x", 0)
	if !strings.Contains(tab.View(), "Creating worktree feat/x") {
		t.Errorf("the split placeholder does not name the branch:\n%q", tab.View())
	}
}

// A create with NO worktree still leaves a placeholder, and over ssh — or into
// a daemon busy spawning — it is not microseconds. Saying nothing at all is the
// same blank rectangle, so the placeholder names what it is waiting for.
func TestSplit_PlaceholderNamesThePaneTypeWhenThereIsNoWorktree(t *testing.T) {
	_, tab := armedCreate(t, "ssh", "", 0)
	out := tab.View()
	if !strings.Contains(out, "Starting ssh") {
		t.Errorf("the split placeholder does not name the pane type:\n%q", out)
	}
	// The worktree message must not leak onto a create that has no worktree.
	if strings.Contains(out, "Creating worktree") {
		t.Errorf("an ordinary split claims a worktree is being created:\n%q", out)
	}
}

// The replace variant is the one that matters most: the pane it stands in for
// is disposed at send time, so on a single-pane tab the whole tab is the
// placeholder.
func TestReplace_PlaceholderNamesThePaneTypeWhenThereIsNoWorktree(t *testing.T) {
	_, tab := armedCreate(t, "ssh", "", 2)
	if !strings.Contains(tab.View(), "Starting ssh") {
		t.Errorf("the replace placeholder does not name the pane type:\n%q", tab.View())
	}
}

// The dialog is not the only thing that arms a placeholder. splitPane is what
// the split KEYBINDINGS and the command palette call, which makes it the split
// people actually use — and it labelled nothing, so the same wait explained
// itself through one entry point and not the other. It sends a payload with no
// Type, and the daemon normalises an empty type to terminal, so "terminal" is
// the honest label rather than a guess.
func TestSplitPane_PlaceholderNamesTheTerminalItIsWaitingFor(t *testing.T) {
	m := newBranchModel(t)
	m.client = &fakeSender{}
	tab := m.curTabs()[0]

	m.splitPane(SplitHorizontal)
	tab.Resize(100, 38)

	if !strings.Contains(tab.View(), "Starting terminal") {
		t.Errorf("the keyboard split's placeholder says nothing:\n%q", tab.View())
	}
}

// renderNode hands the TAB's CreatingBranch to every placeholder leaf, so the
// message is only correct while a tab holds at most one placeholder.
// handleCreatePaneSplit enforces that with its own in-flight refusal; splitPane
// had none, so a split key pressed during a `git worktree add` armed a SECOND
// placeholder that rendered "Creating worktree <branch>" while having no
// worktree at all — and overwrote pendingSplit, stranding the leaf the create
// had reserved for the pane still on its way.
func TestSplitPane_RefusedWhileAWorktreeCreateIsInFlight(t *testing.T) {
	m, tab := armedCreate(t, "terminal", "feat/x", 0)
	reserved := m.pendingSplit[tab.ID]
	if reserved == nil {
		t.Fatal("the create reserved no leaf")
	}

	m.splitPane(SplitVertical)

	if m.pendingSplit[tab.ID] != reserved {
		t.Error("the split overwrote the leaf the worktree create reserved")
	}
	if got := countPlaceholders(tab.Root); got != 1 {
		t.Errorf("tab holds %d placeholders, want 1 — a second one claims the same branch", got)
	}
	if m.flashText == "" {
		t.Error("the refusal was silent")
	}
}

// A placeholder that stops being one must not keep its label. RemoveLeaf
// promotes a sibling by copying its fields into the parent IN PLACE, so a
// placeholder promoted into a node that carried an older label rendered that
// older label — reachable because a REPLACE writes the label onto an EXISTING
// leaf, which keeps it after the pane lands.
func TestPlaceholderLabel_DoesNotSurviveThePaneThatFillsIt(t *testing.T) {
	// A replace-style arm: the label goes onto a leaf that already exists.
	p1, p2 := NewPaneModel("p1", 1024), NewPaneModel("p2", 1024)
	root := NewLeaf(p1)
	root.SplitLeaf("p1", SplitHorizontal)
	root.Right.phType = "claude-code"
	root.Right.Pane = p2

	// That leaf is split again; the fresh placeholder is the RIGHT child.
	root.Right.SplitLeaf("p2", SplitVertical)
	root.Right.Right.phType = "terminal"

	// p2 exits, so the fresh placeholder is promoted into the node that once
	// carried "claude-code".
	if !root.RemoveLeaf("p2") {
		t.Fatal("the pane could not be removed")
	}
	root.phW, root.phH = 60, 10
	resizeNode(root, 100, 38, 100, 0, 0)

	out := renderNode(root, "")
	if strings.Contains(out, "claude-code") {
		t.Errorf("the promoted placeholder wears a label from a pane that already landed:\n%q", out)
	}
	if !strings.Contains(out, "Starting terminal") {
		t.Errorf("the promoted placeholder lost its own label:\n%q", out)
	}
}
