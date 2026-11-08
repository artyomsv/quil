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

// The delete is git's compare-and-delete on the FULLY-QUALIFIED ref, so a
// dash-led branch name never reaches git as an option, and the confirmed tip
// is the old value git compares against.
func TestDeleteLocalBranch_ComparesAndDeletesTheQualifiedRef(t *testing.T) {
	calls := stubGitScript(t, func(args []string) (string, error) {
		switch args[0] {
		case "worktree":
			return "worktree /repo\nHEAD abc\nbranch refs/heads/master\n\n", nil
		case "rev-parse", "update-ref":
			return "", nil
		}
		return "", errors.New("unexpected git call: " + strings.Join(args, " "))
	})
	if err := DeleteLocalBranch(context.Background(), "/repo", "-evil", "tip-1"); err != nil {
		t.Fatalf("DeleteLocalBranch: %v", err)
	}
	last := (*calls)[len(*calls)-1]
	if got := strings.Join(last[1:], " "); got != "update-ref -d refs/heads/-evil tip-1" {
		t.Errorf("argv = %q, want \"update-ref -d refs/heads/-evil tip-1\"", got)
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
