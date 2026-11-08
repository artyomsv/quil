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

// porcelainFor builds a `worktree list --porcelain` answer: the main checkout
// at /repo on master, plus one linked worktree at path on branch.
func porcelainFor(path, branch string) string {
	return strings.Join([]string{
		"worktree /repo",
		"HEAD 1111111111111111111111111111111111111111",
		"branch refs/heads/master",
		"",
		"worktree " + path,
		"HEAD 2222222222222222222222222222222222222222",
		"branch refs/heads/" + branch,
		"",
	}, "\n")
}

// factsScript answers the git calls ReadBranchFacts makes: the worktree
// listing and the unmerged count.
func factsScript(t *testing.T, path, branch string) *[][]string {
	t.Helper()
	return stubGitScript(t, func(args []string) (string, error) {
		switch args[0] {
		case "worktree":
			return porcelainFor(path, branch), nil
		case "rev-list":
			return "3\n", nil
		}
		return "", errors.New("unexpected git call: " + strings.Join(args, " "))
	})
}

// The default branch is the one branch the dialog must never offer to delete.
// Fails if IsDefault compares the fully-qualified ref against the short name.
func TestReadBranchFacts_DefaultBranchIsNotDeletable(t *testing.T) {
	factsScript(t, "/w/main-wt", "main")

	f, err := ReadBranchFacts(context.Background(), "/repo", "/w/main-wt", "refs/remotes/origin/main")
	if err != nil {
		t.Fatalf("ReadBranchFacts: %v", err)
	}
	if !f.IsDefault {
		t.Errorf("IsDefault = false for branch %q against default refs/remotes/origin/main", f.Branch)
	}
}

// A path git does not list is not a worktree this function can describe, and
// facts invented for it would offer a delete of a branch it never held.
func TestReadBranchFacts_UnlistedPathIsAnError(t *testing.T) {
	factsScript(t, "/w/feat", "feat")

	if _, err := ReadBranchFacts(context.Background(), "/repo", "/w/other", "refs/heads/master"); err == nil {
		t.Error("ReadBranchFacts returned no error for a path git does not list")
	}
}

func TestShortBranchName(t *testing.T) {
	for ref, want := range map[string]string{
		"refs/heads/main":            "main",
		"refs/remotes/origin/main":   "main",
		"refs/remotes/origin/feat/x": "feat/x",
		"":                           "",
	} {
		if got := ShortBranchName(ref); got != want {
			t.Errorf("ShortBranchName(%q) = %q, want %q", ref, got, want)
		}
	}
}

// The count is what the dialog's warning prints, so it is checked against
// real git: a branch 2 commits past master.
func TestReadBranchFacts_RealGit_Unmerged(t *testing.T) {
	repo := realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	path := DerivePath(repo, "feat")
	if _, err := Add(ctx, repo, path, "feat"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	for i, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(path, name), []byte("x\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		runGitIn(t, path, "add", name)
		runGitIn(t, path, "commit", "-qm", "c"+string(rune('1'+i)))
	}
	f, err := ReadBranchFacts(ctx, repo, path, "refs/heads/master")
	if err != nil {
		t.Fatalf("ReadBranchFacts: %v", err)
	}
	if f.Branch != "feat" || f.Unmerged != 2 || f.IsDefault {
		t.Errorf("facts = %+v; want branch feat, Unmerged 2, not the default", f)
	}
}
