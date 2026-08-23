package gitworktree

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// The dialog compares a typed branch name against this list, so the names must
// arrive in the same spelling the user types: short, no refs/heads/ prefix.
func TestBranches_ReturnsShortNames(t *testing.T) {
	stubGit(t, "master\nfeat/x\nfix/nationality-filter\n", nil)

	got, truncated, err := Branches(context.Background(), "/repo/internal/tui")
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	if truncated {
		t.Error("truncated = true for a three-branch repository")
	}
	want := []string{"master", "feat/x", "fix/nationality-filter"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Branches() = %v, want %v", got, want)
	}
}

// refs/heads ONLY. A remote-tracking ref is not a name `git worktree add -b`
// would refuse, so including one would refuse a branch the user can legitimately
// create — the false-positive direction this check must never take.
func TestBranches_AsksForLocalHeadsOnly(t *testing.T) {
	calls := stubGit(t, "", nil)

	if _, _, err := Branches(context.Background(), "/repo"); err != nil {
		t.Fatalf("Branches: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("made %d git calls, want 1", len(*calls))
	}
	args := strings.Join((*calls)[0], " ")
	if !strings.Contains(args, "refs/heads") {
		t.Errorf("git args %q do not scope the listing to refs/heads", args)
	}
	if strings.Contains(args, "refs/remotes") {
		t.Errorf("git args %q include remote refs", args)
	}
}

// Trailing blank lines are git's, not a branch called "". An empty entry in the
// list matches an empty typed name and would refuse the branch field's own
// initial state.
func TestBranches_DropsBlankLines(t *testing.T) {
	stubGit(t, "master\n\nfeat/x\n\n", nil)

	got, _, err := Branches(context.Background(), "/repo")
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	want := []string{"master", "feat/x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Branches() = %v, want %v", got, want)
	}
}

// The list rides a must-deliver response frame, so it is capped. The FLAG is
// what makes the cap safe: a caller that cannot see the whole list must not
// conclude a name is free, so it has to be able to tell a complete answer from a
// clipped one.
func TestBranches_CapsTheListAndSaysSo(t *testing.T) {
	var lines []string
	for i := 0; i < maxBranchList+50; i++ {
		lines = append(lines, fmt.Sprintf("feat/branch-%d", i))
	}
	stubGit(t, strings.Join(lines, "\n"), nil)

	got, truncated, err := Branches(context.Background(), "/repo")
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	if len(got) != maxBranchList {
		t.Errorf("returned %d branches, want the cap %d", len(got), maxBranchList)
	}
	if !truncated {
		t.Error("truncated = false after clipping the list — the caller cannot tell it is incomplete")
	}
}

// A directory outside any repository is the ORDINARY case for the setup
// dialog's browser, exactly as it is for List. It must not surface as an error
// beside the branch field.
func TestBranches_NonRepositoryIsNotAnError(t *testing.T) {
	stubGit(t, "", &exec.ExitError{})

	got, truncated, err := Branches(context.Background(), "/not/a/repo")
	if err != nil {
		t.Errorf("Branches on a non-repository returned %v, want no error", err)
	}
	if len(got) != 0 || truncated {
		t.Errorf("Branches() = %v truncated=%v, want an empty complete answer", got, truncated)
	}
}

// A missing git binary and an expired context are REAL failures and must not
// collapse into "this repository has no branches" — the same narrow collapse
// List documents, for the same reason.
func TestBranches_ReportsAMissingGitBinary(t *testing.T) {
	stubGit(t, "", exec.ErrNotFound)

	if _, _, err := Branches(context.Background(), "/repo"); !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("Branches: %v, want exec.ErrNotFound", err)
	}
}

func TestBranches_ReportsAnExpiredContext(t *testing.T) {
	stubGit(t, "", errors.New("signal: killed"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, err := Branches(ctx, "/repo"); err == nil {
		t.Error("Branches returned no error for an expired context")
	}
}
