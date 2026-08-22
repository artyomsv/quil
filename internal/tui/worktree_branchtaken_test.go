package tui

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// takenBranchModel is newBranchModel plus a repository whose branch list holds a
// name with NO worktree — the shape that produced the reported failure. The
// worktree list alone cannot see it, which is the whole reason the branch list
// is carried.
func takenBranchModel(t *testing.T) Model {
	t.Helper()
	m := newBranchModel(t)
	m.worktrees.branches = []string{"master", "feat-a", "fix/nationality-filter"}
	return m
}

// The dialog refuses a name git will refuse, INSTEAD of spawning a create that
// cannot succeed. Before this the create was sent, git said "a branch named
// 'fix/nationality-filter' already exists", and the user's only notice was a
// three-second status-bar flash on a tab that looked like it had worked.
func TestWorktreeNewBranch_EnterRefusesAnExistingBranch(t *testing.T) {
	m := takenBranchModel(t)
	p := &plugin.PanePlugin{Name: "terminal"}
	m.worktreeNaming = true
	m.worktreeNewBranch = "fix/nationality-filter"

	updated, _ := m.handleSetupWorktreeKey(p, "enter")
	got := updated.(Model)
	if got.worktreeErr == "" {
		t.Fatal("an existing branch name was accepted with no message")
	}
	if !strings.Contains(got.worktreeErr, "already exists") {
		t.Errorf("worktreeErr = %q, want it to say the branch already exists", got.worktreeErr)
	}
	if !got.worktreeNaming {
		t.Error("the name field closed on a name that cannot be used")
	}
}

// A free name is unaffected. The check must not become a general refusal of
// names that merely resemble an existing one.
func TestWorktreeNewBranch_EnterAcceptsAFreeName(t *testing.T) {
	m := takenBranchModel(t)
	p := &plugin.PanePlugin{Name: "terminal"}
	m.worktreeNaming = true
	m.worktreeNewBranch = "fix/nationality-filter-2"

	updated, _ := m.handleSetupWorktreeKey(p, "enter")
	got := updated.(Model)
	if got.worktreeErr != "" {
		t.Errorf("worktreeErr = %q for a name no branch uses", got.worktreeErr)
	}
	if got.worktreeNaming {
		t.Error("the name field stayed open after accepting a free name")
	}
}

// The SUBMIT check is the load-bearing one. Tab is handled above the field
// dispatch, so it never reaches the name field's Enter — the user can type a
// taken name, Tab away, and press Continue without the field ever validating.
// submitSetupDialog already re-runs the syntax check for exactly this reason.
func TestSubmitSetup_RefusesAnExistingBranch(t *testing.T) {
	m := takenBranchModel(t)
	p := &plugin.PanePlugin{Name: "terminal", Command: plugin.CommandConfig{PromptsCWD: true}}
	m.worktreeNewBranch = "fix/nationality-filter"

	updated, _ := m.submitSetupDialog(p)
	got := updated.(Model)
	if got.worktreeErr == "" {
		t.Fatal("Continue accepted a branch name git will refuse")
	}
	if !strings.Contains(got.worktreeErr, "already exists") {
		t.Errorf("worktreeErr = %q, want it to say the branch already exists", got.worktreeErr)
	}
	// Refused means the flow does not advance: the dialog stays on the setup
	// step so the name can be corrected in place.
	if got.dialog != m.dialog || got.createPaneStep != m.createPaneStep {
		t.Error("a refused submit advanced the create flow")
	}
}

// Comparison is EXACT. A branch listing is git's spelling and the field is the
// user's; folding case here would refuse `Feat/X` on a repository that holds
// `feat/x`, which git accepts on a case-sensitive ref store. The daemon's own
// error stays the backstop for the platforms where it does not.
func TestWorktreeNewBranch_BranchMatchIsExact(t *testing.T) {
	m := takenBranchModel(t)
	p := &plugin.PanePlugin{Name: "terminal"}
	m.worktreeNaming = true
	m.worktreeNewBranch = "FIX/Nationality-Filter"

	updated, _ := m.handleSetupWorktreeKey(p, "enter")
	if got := updated.(Model); got.worktreeErr != "" {
		t.Errorf("worktreeErr = %q — the match folded case", got.worktreeErr)
	}
}

// A repository whose branches were never listed must refuse NOTHING. Absence
// from a list nobody obtained is not evidence a name is free OR taken, and
// refusing on it would block every create against an older daemon.
func TestWorktreeNewBranch_NoBranchListRefusesNothing(t *testing.T) {
	m := newBranchModel(t) // no branches set
	p := &plugin.PanePlugin{Name: "terminal"}
	m.worktreeNaming = true
	m.worktreeNewBranch = "feat-a" // a branch the WORKTREE list holds

	updated, _ := m.handleSetupWorktreeKey(p, "enter")
	if got := updated.(Model); got.worktreeErr != "" {
		t.Errorf("worktreeErr = %q with no branch listing available", got.worktreeErr)
	}
}

// The listing lands in the state the check reads, keyed like every other field
// on it.
func TestApplyWorktreeList_StoresTheBranches(t *testing.T) {
	m := newBranchModel(t)
	m.worktrees = worktreeState{path: "/repo", gen: "7", pending: true}

	m.applyWorktreeList(worktreeListMsg{
		Gen: "7",
		Resp: ipc.WorktreeListRespPayload{
			Path:              "/repo",
			Repo:              true,
			Branches:          []string{"master", "feat/x"},
			BranchesTruncated: true,
		},
	})
	if len(m.worktrees.branches) != 2 || m.worktrees.branches[1] != "feat/x" {
		t.Errorf("branches = %v, want the response's list", m.worktrees.branches)
	}
	if !m.worktrees.branchesTruncated {
		t.Error("branchesTruncated was dropped")
	}
}
