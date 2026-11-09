package gitworktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The tip is read from the FULLY-QUALIFIED ref, and the delete is git's own
// `branch -D` with `--`, so a dash-led branch name never reaches git as an
// option and git keeps its own refusals and config cleanup.
func TestDeleteLocalBranch_ChecksTheTipThenDeletesWithBranchD(t *testing.T) {
	calls := stubGitScript(t, func(args []string) (string, error) {
		switch args[0] {
		case "rev-parse":
			return "tip-1\n", nil
		case "branch":
			return "", nil
		}
		return "", errors.New("unexpected git call: " + strings.Join(args, " "))
	})
	if err := DeleteLocalBranch(context.Background(), "/repo", "-evil", "tip-1"); err != nil {
		t.Fatalf("DeleteLocalBranch: %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("git calls = %v, want rev-parse then branch -D", *calls)
	}
	if got := strings.Join((*calls)[0][1:], " "); got != "rev-parse --verify --quiet refs/heads/-evil^{commit}" {
		t.Errorf("tip read = %q", got)
	}
	if got := strings.Join((*calls)[1][1:], " "); got != "branch -D -- -evil" {
		t.Errorf("argv = %q, want \"branch -D -- -evil\"", got)
	}
}

// A tip that differs is ErrBranchMoved and nothing is deleted; a git
// failure of the delete itself is NOT reported as a moved branch.
func TestDeleteLocalBranch_MovedOnlyForATipMismatch(t *testing.T) {
	calls := stubGitScript(t, func(args []string) (string, error) {
		if args[0] == "rev-parse" {
			return "other\n", nil
		}
		return "", nil
	})
	if err := DeleteLocalBranch(context.Background(), "/repo", "feat/a", "tip-1"); !errors.Is(err, ErrBranchMoved) {
		t.Fatalf("err = %v, want ErrBranchMoved", err)
	}
	if len(*calls) != 1 {
		t.Errorf("git ran %v after a tip mismatch", *calls)
	}

	stubGitScript(t, func(args []string) (string, error) {
		if args[0] == "rev-parse" {
			return "tip-1\n", nil
		}
		return "", errors.New("cannot lock ref")
	})
	if err := DeleteLocalBranch(context.Background(), "/repo", "feat/a", "tip-1"); err == nil || errors.Is(err, ErrBranchMoved) {
		t.Errorf("err = %v, want a plain failure, not ErrBranchMoved", err)
	}
}

// No confirmed tip, no delete: an empty old value would make update-ref
// delete whatever the branch holds.
func TestDeleteLocalBranch_NoTipRefused(t *testing.T) {
	calls := stubGit(t, "", nil)
	if err := DeleteLocalBranch(context.Background(), "/repo", "feat/a", ""); err == nil {
		t.Fatal("deleted a branch with no confirmed tip")
	}
	if len(*calls) != 0 {
		t.Errorf("git ran %v", *calls)
	}
}

// Real git: once the worktree is removed, its branch can be deleted at the
// confirmed tip, and the branch is really gone afterwards.
func TestDeleteLocalBranch_RealGit_AfterWorktreeRemoved(t *testing.T) {
	repo := realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	path := DerivePath(repo, "feat/gone")
	if _, err := Add(ctx, repo, path, "feat/gone"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "x.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGitIn(t, path, "add", "x.txt")
	runGitIn(t, path, "commit", "-qm", "unmerged work")
	tip := strings.TrimSpace(runGitIn(t, repo, "rev-parse", "refs/heads/feat/gone"))
	if err := RemoveWorktree(ctx, repo, path); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	if err := DeleteLocalBranch(ctx, repo, "feat/gone", tip); err != nil {
		t.Fatalf("DeleteLocalBranch: %v", err)
	}
	if out := runGitIn(t, repo, "branch", "--list", "feat/gone"); out != "" {
		t.Errorf("branch still listed after delete: %q", out)
	}
}

// Real git: the branch got a commit after the user confirmed its tip. The
// delete is refused as ErrBranchMoved, and the new commit survives.
//
// Fails if the delete goes by name.
func TestDeleteLocalBranch_RealGit_MovedBranchSurvives(t *testing.T) {
	repo := realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	path := DerivePath(repo, "feat/moving")
	if _, err := Add(ctx, repo, path, "feat/moving"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	confirmed := strings.TrimSpace(runGitIn(t, repo, "rev-parse", "refs/heads/feat/moving"))
	if err := os.WriteFile(filepath.Join(path, "late.txt"), []byte("late\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, path, "add", "late.txt")
	runGitIn(t, path, "commit", "-qm", "late work")
	if err := RemoveWorktree(ctx, repo, path); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	if err := DeleteLocalBranch(ctx, repo, "feat/moving", confirmed); !errors.Is(err, ErrBranchMoved) {
		t.Fatalf("err = %v, want ErrBranchMoved", err)
	}
	if out := runGitIn(t, repo, "log", "-1", "--format=%s", "feat/moving"); strings.TrimSpace(out) != "late work" {
		t.Errorf("branch tip = %q, want the late commit to survive", out)
	}
}

// Real git: a branch some worktree still has checked out is refused, as
// `branch -D` would.
func TestDeleteLocalBranch_RealGit_CheckedOutBranchRefused(t *testing.T) {
	repo := realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	path := DerivePath(repo, "feat/busy")
	if _, err := Add(ctx, repo, path, "feat/busy"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	tip := strings.TrimSpace(runGitIn(t, repo, "rev-parse", "refs/heads/feat/busy"))
	if err := DeleteLocalBranch(ctx, repo, "feat/busy", tip); err == nil {
		t.Fatal("deleted a branch a worktree has checked out")
	}
	if out := runGitIn(t, repo, "branch", "--list", "feat/busy"); strings.TrimSpace(out) == "" {
		t.Error("the checked-out branch is gone")
	}
}

// ACCEPTED RESIDUAL, pinned so it is a decision and not an accident: a move of
// the branch BETWEEN the tip read and `branch -D` (here an explicit ref
// update; equally a checkout, a commit and a switch away) is not caught, and
// the moved branch is deleted. The owner accepted this as a same-user gap
// rather than lose git's own refusals and config cleanup to an atomic
// update-ref delete.
func TestDeleteLocalBranch_RealGit_MoveInsideTheWindowIsTheAcceptedResidual(t *testing.T) {
	repo := realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	path := DerivePath(repo, "feat/window")
	if _, err := Add(ctx, repo, path, "feat/window"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "w.txt"), []byte("w\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, path, "add", "w.txt")
	runGitIn(t, path, "commit", "-qm", "late")
	late := strings.TrimSpace(runGitIn(t, repo, "rev-parse", "refs/heads/feat/window"))
	if err := RemoveWorktree(ctx, repo, path); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	// The user confirmed the branch at master's tip; inside the window it is
	// moved to the "late" commit.
	confirmed := strings.TrimSpace(runGitIn(t, repo, "rev-parse", "refs/heads/master"))
	runGitIn(t, repo, "update-ref", "refs/heads/feat/window", confirmed)
	prev := betweenTipAndDelete
	betweenTipAndDelete = func() { runGitIn(t, repo, "update-ref", "refs/heads/feat/window", late) }
	t.Cleanup(func() { betweenTipAndDelete = prev })

	if err := DeleteLocalBranch(ctx, repo, "feat/window", confirmed); err != nil {
		t.Fatalf("err = %v; the accepted residual is that the delete goes through", err)
	}
	if out := runGitIn(t, repo, "branch", "--list", "feat/window"); strings.TrimSpace(out) != "" {
		t.Errorf("branch survived (%q): the residual changed, update the rule", out)
	}
}
