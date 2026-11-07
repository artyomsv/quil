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

// factsScript answers the git calls ReadBranchFacts makes. upstream is what
// `@{upstream}` resolves to ("" = no upstream); remotes are `git remote`'s
// names; refs is the set of refs that exist.
func factsScript(t *testing.T, path, branch, upstream string, remotes []string, refs map[string]bool) *[][]string {
	t.Helper()
	return stubGitScript(t, func(args []string) (string, error) {
		switch {
		case args[0] == "worktree":
			return porcelainFor(path, branch), nil
		case args[0] == "rev-list":
			return "3\n", nil
		case args[0] == "remote":
			return strings.Join(remotes, "\n") + "\n", nil
		case args[0] == "rev-parse" && args[1] == "--abbrev-ref":
			if upstream == "" {
				return "", errors.New("no upstream")
			}
			return upstream + "\n", nil
		case args[0] == "rev-parse" && args[1] == "--verify":
			if refs[args[len(args)-1]] {
				return "cafebabe\n", nil
			}
			return "", errors.New("missing")
		}
		return "", errors.New("unexpected git call: " + strings.Join(args, " "))
	})
}

// The default branch is the one branch the dialog must never offer to delete.
// Fails if IsDefault compares the fully-qualified ref against the short name.
func TestReadBranchFacts_DefaultBranchIsNotDeletable(t *testing.T) {
	factsScript(t, "/w/main-wt", "main", "", nil, nil)

	f, err := ReadBranchFacts(context.Background(), "/repo", "/w/main-wt", "refs/remotes/origin/main")
	if err != nil {
		t.Fatalf("ReadBranchFacts: %v", err)
	}
	if !f.IsDefault {
		t.Errorf("IsDefault = false for branch %q against default refs/remotes/origin/main", f.Branch)
	}
}

// A configured upstream is the branch's own answer to "where is it pushed",
// so it outranks guessing from whichever remote-tracking refs exist.
// Fails if the ref scan runs first and picks the other remote.
func TestReadBranchFacts_UpstreamWinsOverRefScan(t *testing.T) {
	factsScript(t, "/w/feat", "feat", "fork/feat", []string{"origin", "fork"}, map[string]bool{
		"refs/remotes/origin/feat": true,
		"refs/remotes/fork/feat":   true,
	})

	f, err := ReadBranchFacts(context.Background(), "/repo", "/w/feat", "refs/heads/master")
	if err != nil {
		t.Fatalf("ReadBranchFacts: %v", err)
	}
	if f.Remote != "fork" || f.RemoteBranch != "feat" {
		t.Errorf("Remote = %q/%q, want fork/feat (the upstream)", f.Remote, f.RemoteBranch)
	}
}

// "origin/feat/x" is remote origin + branch feat/x, not remote origin/feat +
// branch x. The split must use the remote NAMES, longest match first.
func TestReadBranchFacts_SlashInBranchResolvesRemote(t *testing.T) {
	factsScript(t, "/w/feat-x", "feat/x", "origin/feat/x", []string{"origin"}, map[string]bool{
		"refs/remotes/origin/feat/x": true,
	})

	f, err := ReadBranchFacts(context.Background(), "/repo", "/w/feat-x", "refs/heads/master")
	if err != nil {
		t.Fatalf("ReadBranchFacts: %v", err)
	}
	if f.Remote != "origin" || f.RemoteBranch != "feat/x" {
		t.Errorf("Remote = %q/%q, want origin/feat/x", f.Remote, f.RemoteBranch)
	}
}

// With no upstream, a branch that exists on two remotes has no single answer,
// and guessing one would push a delete to the wrong place.
func TestReadBranchFacts_TwoRemotesIsAmbiguous(t *testing.T) {
	factsScript(t, "/w/feat", "feat", "", []string{"origin", "fork"}, map[string]bool{
		"refs/remotes/origin/feat": true,
		"refs/remotes/fork/feat":   true,
	})

	f, err := ReadBranchFacts(context.Background(), "/repo", "/w/feat", "refs/heads/master")
	if err != nil {
		t.Fatalf("ReadBranchFacts: %v", err)
	}
	if f.Remote != "" || f.RemoteReason != "branch exists on several remotes" {
		t.Errorf("Remote = %q, reason %q; want none and the ambiguity reason", f.Remote, f.RemoteReason)
	}
}

// No remote-tracking ref anywhere: the dialog greys the remote row with this
// reason rather than offering a push that has nothing to delete.
func TestReadBranchFacts_NoRemoteBranch(t *testing.T) {
	factsScript(t, "/w/feat", "feat", "", []string{"origin"}, nil)

	f, err := ReadBranchFacts(context.Background(), "/repo", "/w/feat", "refs/heads/master")
	if err != nil {
		t.Fatalf("ReadBranchFacts: %v", err)
	}
	if f.Remote != "" || f.RemoteReason != "no remote branch" {
		t.Errorf("Remote = %q, reason %q; want none and \"no remote branch\"", f.Remote, f.RemoteReason)
	}
}

// A path git does not list is not a worktree this function can describe, and
// facts invented for it would offer a delete of a branch it never held.
func TestReadBranchFacts_UnlistedPathIsAnError(t *testing.T) {
	factsScript(t, "/w/feat", "feat", "", nil, nil)

	if _, err := ReadBranchFacts(context.Background(), "/repo", "/w/other", "refs/heads/master"); err == nil {
		t.Error("ReadBranchFacts returned no error for a path git does not list")
	}
}

// A feature branch whose upstream is the remote DEFAULT branch must never
// offer a remote delete: that push would delete origin/main.
//
// Fails if the remote target is not compared with the default branch.
func TestReadBranchFacts_UpstreamOnDefaultIsNoRemoteTarget(t *testing.T) {
	factsScript(t, "/w/feat", "feat", "origin/main", []string{"origin"}, map[string]bool{
		"refs/remotes/origin/main": true,
	})

	f, err := ReadBranchFacts(context.Background(), "/repo", "/w/feat", "refs/remotes/origin/main")
	if err != nil {
		t.Fatalf("ReadBranchFacts: %v", err)
	}
	if f.Remote != "" || f.RemoteReason == "" {
		t.Errorf("Remote = %q/%q reason %q; want no remote target and a reason", f.Remote, f.RemoteBranch, f.RemoteReason)
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

// The counts are what the dialog's warnings print, so they are checked against
// real git: a branch 2 commits past master, whose remote-tracking ref is 1
// commit behind it, found by the ref scan (no upstream is configured).
func TestReadBranchFacts_RealGit_UnmergedAndUnpushed(t *testing.T) {
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
		if i == 0 {
			runGitIn(t, repo, "remote", "add", "origin", repo)
			runGitIn(t, repo, "update-ref", "refs/remotes/origin/feat", "refs/heads/feat")
		}
	}
	f, err := ReadBranchFacts(ctx, repo, path, "refs/heads/master")
	if err != nil {
		t.Fatalf("ReadBranchFacts: %v", err)
	}
	if f.Branch != "feat" || f.Unmerged != 2 || f.Remote != "origin" || f.Unpushed != 1 {
		t.Errorf("facts = %+v; want branch feat, Unmerged 2, Remote origin, Unpushed 1", f)
	}
}
