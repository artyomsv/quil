package daemon

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
)

// stubBranchFacts answers the branch half: every path lives in repo /repo,
// the default branch is refs/heads/master, and facts come from fn. It returns
// the number of default-branch resolutions.
func stubBranchFacts(t *testing.T, fn func(path string) (gitworktree.BranchFacts, error)) *int {
	t.Helper()
	prevList, prevFacts, prevDef := worktreeListFn, branchFactsFn, defaultBranchRefFn
	worktreeListFn = func(context.Context, string) ([]gitworktree.Worktree, error) {
		return []gitworktree.Worktree{{Path: "/repo"}}, nil
	}
	resolutions := 0
	defaultBranchRefFn = func(context.Context, string) string {
		resolutions++
		return "refs/heads/master"
	}
	branchFactsFn = func(_ context.Context, _, path, _ string) (gitworktree.BranchFacts, error) {
		return fn(path)
	}
	t.Cleanup(func() { worktreeListFn, branchFactsFn, defaultBranchRefFn = prevList, prevFacts, prevDef })
	return &resolutions
}

// The dialog lists files and prices the branch rows from this one answer, so
// every field must survive the trip.
//
// Fails if fillBranchFacts drops a field, or if Files is not copied.
func TestWorktreeStatusResponse_CarriesFilesAndBranchFacts(t *testing.T) {
	prev := worktreeStatusFn
	worktreeStatusFn = func(context.Context, string) (gitworktree.StatusReport, error) {
		return gitworktree.StatusReport{Changes: 3, Files: []gitworktree.FileChange{{Code: "??", Path: "a b.txt"}}, Truncated: true}, nil
	}
	t.Cleanup(func() { worktreeStatusFn = prev })
	stubBranchFacts(t, func(string) (gitworktree.BranchFacts, error) {
		return gitworktree.BranchFacts{Branch: "feat", DefaultRef: "refs/heads/master", Unmerged: 2,
			Remote: "origin", RemoteBranch: "feat", Unpushed: 1}, nil
	})

	got := worktreeStatusResponse(ipc.WorktreeStatusReqPayload{Paths: []string{"/w/feat"}})

	st := got.Statuses[0]
	if st.Changes != 3 || !st.FilesTruncated || len(st.Files) != 1 || st.Files[0] != (ipc.WorktreeFile{Code: "??", Path: "a b.txt"}) {
		t.Errorf("files = %d %v %#v, want 3, truncated, one entry", st.Changes, st.FilesTruncated, st.Files)
	}
	if st.Branch != "feat" || !st.BranchDeletable || st.Unmerged != 2 || st.DefaultBranch != "master" ||
		st.Remote != "origin" || st.RemoteBranch != "feat" || st.Unpushed != 1 {
		t.Errorf("branch facts = %+v", st)
	}
}

// A facts error must leave the branch NOT deletable with a reason, while the
// change count still arrives — one failing git call must not hide the files.
func TestWorktreeStatusResponse_FactsErrorIsNotDeletable(t *testing.T) {
	stubWorktreeStatus(t, func(string) (int, error) { return 1, nil })
	stubBranchFacts(t, func(string) (gitworktree.BranchFacts, error) {
		return gitworktree.BranchFacts{}, errors.New("boom")
	})

	st := worktreeStatusResponse(ipc.WorktreeStatusReqPayload{Paths: []string{"/w/feat"}}).Statuses[0]

	if st.BranchDeletable || st.BranchReason == "" || st.Changes != 1 {
		t.Errorf("status = %+v, want not deletable with a reason and Changes 1", st)
	}
}

// The default branch and a detached checkout are never deletable.
func TestWorktreeStatusResponse_DefaultAndDetachedAreNotDeletable(t *testing.T) {
	stubWorktreeStatus(t, func(string) (int, error) { return 0, nil })
	stubBranchFacts(t, func(path string) (gitworktree.BranchFacts, error) {
		if path == "/w/main" {
			return gitworktree.BranchFacts{Branch: "master", IsDefault: true}, nil
		}
		return gitworktree.BranchFacts{Detached: true}, nil
	})

	got := worktreeStatusResponse(ipc.WorktreeStatusReqPayload{Paths: []string{"/w/main", "/w/det"}})

	for _, st := range got.Statuses {
		if st.BranchDeletable || st.BranchReason == "" {
			t.Errorf("%s: deletable=%v reason=%q, want not deletable with a reason", st.Path, st.BranchDeletable, st.BranchReason)
		}
	}
}

// One repository, three worktrees: the default branch is asked for ONCE.
// Fails if the per-request cache is dropped.
func TestWorktreeStatusResponse_DefaultBranchResolvedOncePerRepo(t *testing.T) {
	stubWorktreeStatus(t, func(string) (int, error) { return 0, nil })
	resolutions := stubBranchFacts(t, func(string) (gitworktree.BranchFacts, error) {
		return gitworktree.BranchFacts{Branch: "feat"}, nil
	})

	worktreeStatusResponse(ipc.WorktreeStatusReqPayload{Paths: []string{"/w/a", "/w/b", "/w/c"}})

	if *resolutions != 1 {
		t.Errorf("default branch resolved %d times, want 1", *resolutions)
	}
}

// Facts cost several git spawns each on one shared deadline, so only the rows
// the dialog draws get them. The rest still get their change count.
func TestWorktreeStatusResponse_FactsOnlyForFirstSixPaths(t *testing.T) {
	stubWorktreeStatus(t, func(string) (int, error) { return 1, nil })
	asked := 0
	stubBranchFacts(t, func(string) (gitworktree.BranchFacts, error) {
		asked++
		return gitworktree.BranchFacts{Branch: "feat"}, nil
	})
	var paths []string
	for i := 0; i < 8; i++ {
		paths = append(paths, fmt.Sprintf("/w/%d", i))
	}

	got := worktreeStatusResponse(ipc.WorktreeStatusReqPayload{Paths: paths})

	if asked != maxBranchFactsPaths {
		t.Errorf("facts asked %d times, want %d", asked, maxBranchFactsPaths)
	}
	last := got.Statuses[7]
	if last.BranchDeletable || last.BranchReason == "" || last.Changes != 1 {
		t.Errorf("row 8 = %+v, want a change count and a not-checked reason", last)
	}
}

// With no default branch the branch is not offered: fail closed.
func TestWorktreeStatusResponse_UnknownDefaultIsNotDeletable(t *testing.T) {
	stubWorktreeStatus(t, func(string) (int, error) { return 0, nil })
	stubBranchFacts(t, func(string) (gitworktree.BranchFacts, error) {
		return gitworktree.BranchFacts{Branch: "feat"}, nil
	})

	st := worktreeStatusResponse(ipc.WorktreeStatusReqPayload{Paths: []string{"/w/feat"}}).Statuses[0]

	if st.BranchDeletable || st.BranchReason == "" {
		t.Errorf("status = %+v, want not deletable with a reason", st)
	}
}

// Slow branch checks never hold back the file counts: past the budget they
// are skipped with a reason, and every count is there.
func TestWorktreeStatusResponse_FactsBudgetKeepsFileCounts(t *testing.T) {
	prev := branchFactsBudget
	branchFactsBudget = 0
	t.Cleanup(func() { branchFactsBudget = prev })
	stubWorktreeStatus(t, func(string) (int, error) { return 3, nil })
	asked := 0
	stubBranchFacts(t, func(string) (gitworktree.BranchFacts, error) {
		asked++
		return gitworktree.BranchFacts{Branch: "feat"}, nil
	})

	got := worktreeStatusResponse(ipc.WorktreeStatusReqPayload{Paths: []string{"/w/a", "/w/b"}})

	if asked != 0 {
		t.Errorf("facts read %d times past the budget", asked)
	}
	for _, st := range got.Statuses {
		if st.Changes != 3 || st.BranchReason == "" {
			t.Errorf("status = %+v, want the count and a not-checked reason", st)
		}
	}
}
