package gitworktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// `--` keeps a dash-led branch name from reaching git as an option.
func TestDeleteLocalBranch_TerminatesOptions(t *testing.T) {
	calls := stubGit(t, "", nil)
	if err := DeleteLocalBranch(context.Background(), "/repo", "-evil"); err != nil {
		t.Fatalf("DeleteLocalBranch: %v", err)
	}
	if got := strings.Join((*calls)[0][1:], " "); got != "branch -D -- -evil" {
		t.Errorf("argv = %q, want \"branch -D -- -evil\"", got)
	}
}

// Real git: once the worktree is removed, its branch can be deleted, and the
// branch is really gone afterwards.
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
	if err := RemoveWorktree(ctx, repo, path); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	if err := DeleteLocalBranch(ctx, repo, "feat/gone"); err != nil {
		t.Fatalf("DeleteLocalBranch: %v", err)
	}
	if out := runGitIn(t, repo, "branch", "--list", "feat/gone"); out != "" {
		t.Errorf("branch still listed after delete: %q", out)
	}
}
