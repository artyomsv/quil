package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/hookevents"
	"github.com/artyomsv/quil/internal/ipc"
)

// identityOpts is a close of /w/feat-a by testBranchOwner, with its identity
// token recorded.
func identityOpts() branchDeleteOpts {
	return branchDeleteOpts{Owners: map[string]paneRef{"/w/feat-a": testBranchOwner},
		Tokens: map[string]string{"/w/feat-a": "test-token"}}
}

// oneKeptCard returns the single worktree-kept card, or fails.
func oneKeptCard(t *testing.T, d *Daemon) PaneEvent {
	t.Helper()
	cards := eventsOfType(d, "worktree_kept")
	if len(cards) != 1 {
		t.Fatalf("worktree_kept cards = %+v, want exactly one", cards)
	}
	if cards[0].PaneID != testBranchOwner.ID {
		t.Errorf("card names pane %q, want %q", cards[0].PaneID, testBranchOwner.ID)
	}
	return cards[0]
}

// A checkout replaced at the same path since Quil made it has no matching
// mark: the close keeps it and says why.
//
// Fails if the close removes by path alone.
func TestRemoveOwnedWorktrees_ReplacedCheckoutIsKept(t *testing.T) {
	d := newTestDaemon(t)
	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}, {Path: "/w/feat-a", Branch: "x"}}, nil)
	calls := stubRemoveWorktree(t, nil)
	ownerMarkMatchesFn = func(context.Context, string, string) (bool, error) { return false, nil }

	d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, identityOpts())

	if len(*calls) != 0 {
		t.Fatalf("removed %v: not the checkout Quil made", *calls)
	}
	if c := oneKeptCard(t, d); !strings.Contains(c.Message, "no longer the worktree Quil made") {
		t.Errorf("card message = %q", c.Message)
	}
}

// A worktree with no recorded identity (a pane from before identities) is
// never removed, and the card says so.
func TestRemoveOwnedWorktrees_NoIdentityIsKept(t *testing.T) {
	d := newTestDaemon(t)
	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}, {Path: "/w/feat-a", Branch: "x"}}, nil)
	calls := stubRemoveWorktree(t, nil)
	opt := identityOpts()
	opt.Tokens = nil

	d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, opt)

	if len(*calls) != 0 {
		t.Fatalf("removed %v with no identity recorded", *calls)
	}
	oneKeptCard(t, d)
}

// A worktree-only close (no branch delete) that is kept still says why:
// in use by another pane, or its repository could not be read.
//
// Fails if the kept card depends on a branch delete being asked.
func TestRemoveOwnedWorktrees_KeptWorktreeAlwaysShowsACard(t *testing.T) {
	t.Run("in use", func(t *testing.T) {
		d := newTestDaemon(t)
		tab := d.session.CreateTab("other")
		if _, err := d.session.CreatePane(tab.ID, "/w/feat-a"); err != nil {
			t.Fatal(err)
		}
		stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}, {Path: "/w/feat-a", Branch: "x"}}, nil)
		calls := stubRemoveWorktree(t, nil)

		d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, identityOpts())

		if len(*calls) != 0 {
			t.Fatalf("removed %v while a pane is in it", *calls)
		}
		if c := oneKeptCard(t, d); !strings.Contains(c.Message, "is still in it") {
			t.Errorf("card message = %q", c.Message)
		}
	})
	t.Run("unreadable listing", func(t *testing.T) {
		d := newTestDaemon(t)
		stubWorktreeList(t, nil, errors.New("permission denied"))
		calls := stubRemoveWorktree(t, nil)

		d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, identityOpts())

		if len(*calls) != 0 {
			t.Fatalf("removed %v with the listing unreadable", *calls)
		}
		if c := oneKeptCard(t, d); !strings.Contains(c.Message, "could not be read") {
			t.Errorf("card message = %q", c.Message)
		}
	})
}

// The close dialog's file list is bound to the checkout too: a replacement
// at the path is refused with a reason, and git status never runs there.
//
// Fails if the status check lists the files of whatever sits at the path.
func TestWorktreeStatusResponse_ReplacedCheckoutListsNoFiles(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "/w/feat-a")
	if err != nil {
		t.Fatal(err)
	}
	pane.WorktreeOwned, pane.WorktreePath, pane.WorktreeToken = true, "/w/feat-a", "test-token"
	asked := 0
	stubWorktreeStatus(t, func(string) (int, error) { asked++; return 3, nil })
	ownerMarkMatchesFn = func(context.Context, string, string) (bool, error) { return false, nil }

	got := d.worktreeStatusResponse(ipc.WorktreeStatusReqPayload{Paths: []string{"/w/feat-a"}})

	if asked != 0 {
		t.Errorf("git status ran %d times on a replaced checkout", asked)
	}
	if len(got.Statuses) != 1 || got.Statuses[0].Error == "" || got.Statuses[0].Changes != 0 {
		t.Errorf("statuses = %+v, want one refusal with no count", got.Statuses)
	}
}

// Creating a worktree pane records its identity: the token written into the
// worktree's admin directory is the one the pane keeps.
func TestCreatePaneInWorktree_RecordsTheIdentity(t *testing.T) {
	d, req, checkout := markTestCreate(t)
	var wroteRepo, wrotePath, wroteToken string
	stubMark(t, func(_ context.Context, repo, p, tok string) error {
		wroteRepo, wrotePath, wroteToken = repo, p, tok
		return nil
	})

	resp := d.worktreeAddAndCreate(req)
	if resp.Error != "" {
		t.Fatalf("create failed: %s", resp.Error)
	}
	pane := d.session.Pane(resp.PaneID)
	pane.PluginMu.Lock()
	tok := pane.WorktreeToken
	pane.PluginMu.Unlock()
	if tok == "" || tok != wroteToken || wrotePath != checkout || wroteRepo != req.Worktree.RepoRoot {
		t.Errorf("pane token %q; mark %q written at %q for repo %q", tok, wroteToken, wrotePath, wroteRepo)
	}
}

// A mark the create could not write (the checkout at the path is not the one
// it made, say) leaves the pane with NO token: the close keeps it.
//
// Fails if a refused mark still records a token.
func TestCreatePaneInWorktree_RefusedMarkLeavesNoToken(t *testing.T) {
	d, req, _ := markTestCreate(t)
	stubMark(t, func(context.Context, string, string, string) error { return gitworktree.ErrNotCreatedHere })

	resp := d.worktreeAddAndCreate(req)
	if resp.Error != "" {
		t.Fatalf("create failed: %s", resp.Error)
	}
	pane := d.session.Pane(resp.PaneID)
	pane.PluginMu.Lock()
	tok := pane.WorktreeToken
	pane.PluginMu.Unlock()
	if tok != "" {
		t.Errorf("pane token = %q after a refused mark, want none", tok)
	}
}

// markTestCreate is a worktree create whose git add is stubbed to make the
// checkout folder, so the create runs end to end without git.
func markTestCreate(t *testing.T) (*Daemon, ipc.CreatePanePayload, string) {
	t.Helper()
	t.Setenv("QUIL_HOME", t.TempDir())
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	req := worktreeCreate(tab.ID, t.TempDir(), "feat/id")
	checkout := gitworktree.DerivePath(req.Worktree.RepoRoot, req.Worktree.Branch)
	stubAdd(t, func(context.Context, string, string, string) error { return os.MkdirAll(checkout, 0o700) })
	return d, req, checkout
}

// stubMark swaps the identity-mark seam for the test's duration.
func stubMark(t *testing.T, fn func(ctx context.Context, repo, path, token string) error) {
	t.Helper()
	prev := markNewWorktreeFn
	markNewWorktreeFn = fn
	t.Cleanup(func() { markNewWorktreeFn = prev })
}

// A subagent reported a LINK whose target folder was then renamed: the link
// now dangles and says nothing about where the process went. The close's
// delete gate keeps the worktree.
//
// Fails if a dangling link is treated as an ordinary missing component.
func TestRemoveOwnedWorktrees_DanglingLinkKeepsTheWorktree(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real", "wt")
	sub := filepath.Join(real, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(sub, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	d := daemonWithRecord(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	p := probeFor(pane.ID, link)
	p.Data[hookevents.DataProbeAgent] = "sub-1"
	d.agentWT.probe(p)
	if err := os.Rename(sub, filepath.Join(real, "moved")); err != nil {
		t.Fatal(err)
	}
	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}, {Path: real, Branch: "feat"}}, nil)
	calls := stubRemoveWorktree(t, nil)

	d.removeOwnedWorktreesWith([]string{real}, branchDeleteOpts{Tokens: map[string]string{real: "test-token"}})

	if len(*calls) != 0 {
		t.Errorf("removed %v while a subagent's dangling link may point into it", *calls)
	}
}

// A mark read that never returns cannot hold the caller: the bounded worker
// gives up at the deadline and the answer is a refusal (keep the worktree).
//
// Fails if the identity check runs the file read unbounded.
func TestOwnerMarkRefusal_HungReadRefusesAndReturns(t *testing.T) {
	hang := make(chan struct{})
	prev := ownerMarkMatchesFn
	ownerMarkMatchesFn = func(context.Context, string, string) (bool, error) { <-hang; return true, nil }
	t.Cleanup(func() { close(hang); ownerMarkMatchesFn = prev })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan string, 1)
	go func() { done <- ownerMarkRefusal(ctx, "/w/feat-a", "test-token") }()
	select {
	case reason := <-done:
		if reason == "" {
			t.Error("an unanswered mark read was accepted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the identity check hung on a mark read that never returns")
	}
}

// The folder is replaced during the retry backoff: the identity is checked
// again before EVERY attempt, so the second attempt never runs on the
// replacement.
//
// Fails if the identity is checked only once before the attempts.
func TestRemoveOneWorktree_IdentityRecheckedBeforeEachAttempt(t *testing.T) {
	d := newTestDaemon(t)
	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}, {Path: "/w/feat-a", Branch: "x"}}, nil)
	calls := stubRemoveWorktree(t, func(context.Context, string, string) error { return errors.New("directory busy") })
	checks := 0
	ownerMarkMatchesFn = func(context.Context, string, string) (bool, error) {
		checks++
		return checks <= 2, nil // ours before the facts and the first attempt; replaced after
	}

	d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, identityOpts())

	if len(*calls) != 1 {
		t.Fatalf("removal attempts = %d, want 1: the second ran on a replaced checkout", len(*calls))
	}
	if c := oneKeptCard(t, d); c.Data["reason"] != keptNotOurs {
		t.Errorf("card reason = %q, want %q", c.Data["reason"], keptNotOurs)
	}
}

// On a case-insensitive volume (macOS by default) a probe spelling the
// worktree in another case is the same folder: the delete check keeps it.
//
// Fails if the resolved compare is case-sensitive where the volume is not.
func TestResolvedWithin_CaseAliasKeepsTheWorktree(t *testing.T) {
	prev := foldPathCase
	foldPathCase = true
	t.Cleanup(func() { foldPathCase = prev })
	within := resolvedWithin("/Users/me/WT")
	if !within("/Users/me/WT", "/Users/me/wt/src") {
		t.Error("a case alias of the worktree was not seen as inside it")
	}
	if within("/Users/me/WT", "/Users/me/wt2") {
		t.Error("a sibling folder was seen as inside the worktree")
	}
}

// The identity is written STRAIGHT after git returns: before the pane starts
// (a folder replaced under a running child must not be marked as Quil's) and
// before the post-add checks, so the window between the add and the mark is
// as small as it can be.
//
// Fails if the mark is written after the pane is published, or after the
// post-add re-validation.
func TestCreatePaneInWorktree_MarksStraightAfterTheAdd(t *testing.T) {
	d, req, _ := markTestCreate(t)
	before := len(d.session.AllPanes())
	panesAtWrite := -1
	stubMark(t, func(context.Context, string, string, string) error {
		panesAtWrite = len(d.session.AllPanes())
		return nil
	})

	if resp := d.worktreeAddAndCreate(req); resp.Error != "" {
		t.Fatalf("create failed: %s", resp.Error)
	}
	if panesAtWrite != before {
		t.Errorf("panes when the mark was written = %d, want %d: the pane started first", panesAtWrite, before)
	}

	// The tab closes during the checkout: the post-add check abandons the
	// create, and the mark was already written ahead of it.
	d2, req2, checkout2 := markTestCreate(t)
	marked := false
	stubMark(t, func(context.Context, string, string, string) error { marked = true; return nil })
	stubAdd(t, func(context.Context, string, string, string) error {
		if err := d2.session.DestroyTab(req2.TabID); err != nil {
			return err
		}
		return os.MkdirAll(checkout2, 0o700)
	})
	stubRemove(t, nil)
	if resp := d2.worktreeAddAndCreate(req2); resp.Error == "" {
		t.Fatal("create succeeded in a tab closed during the checkout")
	}
	if !marked {
		t.Error("the mark waited for the post-add checks")
	}
}

// A pane folder that names the worktree through a path the strings cannot
// match (here a link the resolver leaves alone; on macOS a Unicode or case
// alias) is still the same folder on disk: the delete check keeps it.
//
// Fails if the delete check compares path strings only.
func TestResolvedWithin_SameFolderOnDiskKeepsTheWorktree(t *testing.T) {
	base := t.TempDir()
	wt := filepath.Join(base, "wt")
	if err := os.MkdirAll(filepath.Join(wt, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(wt, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	prevEval, prevFold := evalSymlinksFn, foldPathCase
	evalSymlinksFn = func(p string) (string, error) { return p, nil } // the alias survives resolution
	foldPathCase = false
	t.Cleanup(func() { evalSymlinksFn, foldPathCase = prevEval, prevFold })

	within := resolvedWithin(wt)
	if !within(wt, filepath.Join(alias, "src")) {
		t.Error("a folder that is the worktree on disk was not seen as inside it")
	}
	if within(wt, base) {
		t.Error("the worktree's parent was seen as inside it")
	}
}

// The macOS case: one folder, two Unicode spellings (NFC and NFD). Skipped
// where the volume treats the two spellings as different names.
func TestResolvedWithin_UnicodeAliasKeepsTheWorktree(t *testing.T) {
	base := t.TempDir()
	nfc := filepath.Join(base, "caf"+string(rune(0x00e9)))
	nfd := filepath.Join(base, "cafe"+string(rune(0x0301)))
	if err := os.MkdirAll(filepath.Join(nfc, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(nfd); err != nil {
		t.Skipf("this volume is normalization-sensitive (%v): no alias to test", err)
	}
	if !resolvedWithin(nfc)(nfc, filepath.Join(nfd, "src")) {
		t.Error("an NFD spelling of the worktree was not seen as inside it")
	}
}

// The rollback of an abandoned create destroys with --force and branch -D, so
// it keeps the checkout on every doubt the close path has: no identity, not
// the marked checkout, a pane in it, changes, commits, or a check that could
// not answer — and it re-checks the mark last.
//
// Fails if any doubt lets the rollback through.
func TestAbandonRefusal_KeepsOnEveryDoubt(t *testing.T) {
	clean := gitworktree.BranchFacts{Branch: "feat/x", DefaultRef: "refs/heads/master"}
	cases := []struct {
		name    string
		token   string
		marks   []bool // successive mark answers
		changes int
		statErr error
		facts   gitworktree.BranchFacts
		factErr error
		want    string
	}{
		{"clean and ours", "tok", []bool{true, true}, 0, nil, clean, nil, ""},
		{"no identity", "", nil, 0, nil, clean, nil, keptNoIdentity},
		{"not ours", "tok", []bool{false}, 0, nil, clean, nil, keptNotOurs},
		{"replaced before the final check", "tok", []bool{true, false}, 0, nil, clean, nil, keptNotOurs},
		{"changes", "tok", []bool{true, true}, 2, nil, clean, nil, keptHasChanges},
		{"status unanswered", "tok", []bool{true, true}, 0, errors.New("boom"), clean, nil, keptRepoUnreadable},
		{"commits", "tok", []bool{true, true}, 0, nil, gitworktree.BranchFacts{Branch: "feat/x", Unmerged: 1}, nil, keptHasCommits},
		{"facts unanswered", "tok", []bool{true, true}, 0, nil, clean, errors.New("boom"), keptRepoUnreadable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDaemon(t)
			stubWorktreeStatus(t, func(string) (int, error) { return tc.changes, tc.statErr })
			stubBranchFacts(t, func(string) (gitworktree.BranchFacts, error) { return tc.facts, tc.factErr })
			// AFTER stubWorktreeStatus, which installs an always-ours mark.
			calls := 0
			prev := ownerMarkMatchesFn
			ownerMarkMatchesFn = func(context.Context, string, string) (bool, error) {
				ok := calls < len(tc.marks) && tc.marks[calls]
				calls++
				return ok, nil
			}
			t.Cleanup(func() { ownerMarkMatchesFn = prev })

			if got := d.abandonRefusal("/repo", "/w/feat-x", tc.token); got != tc.want {
				t.Errorf("abandonRefusal = %q, want %q", got, tc.want)
			}
		})
	}
}

// The accepted residual meets a rollback: after the add, another operation
// replaces the checkout at the path with one of the SAME repository on the
// same branch, commits there and leaves a change, then the tab closes. The
// replacement carries the mark (the documented residual), yet the rollback
// keeps the checkout, the change and the commit.
//
// Fails if the abandoned create force-removes on the mark alone.
func TestWorktreeAddAndCreate_RealGit_AbandonKeepsAReplacementHoldingWork(t *testing.T) {
	repo := realGitRepoForDaemon(t)
	t.Setenv("QUIL_HOME", t.TempDir())
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	req := worktreeCreate(tab.ID, repo, "feat/x")
	path := gitworktree.DerivePath(repo, "feat/x")
	stubAdd(t, func(ctx context.Context, repo, path, branch string) error {
		if _, err := gitworktree.Add(ctx, repo, path, branch); err != nil {
			return err
		}
		if err := gitworktree.RemoveWorktree(ctx, repo, path); err != nil {
			return err
		}
		runGitForAbandon(t, repo, "worktree", "add", "-q", path, branch)
		if err := os.WriteFile(filepath.Join(path, "work.txt"), []byte("work\n"), 0o644); err != nil {
			return err
		}
		runGitForAbandon(t, path, "add", "work.txt")
		runGitForAbandon(t, path, "commit", "-q", "-m", "work")
		if err := os.WriteFile(filepath.Join(path, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
			return err
		}
		return d.session.DestroyTab(tab.ID)
	})

	resp := d.worktreeAddAndCreate(req)

	if !strings.Contains(resp.Error, "kept") {
		t.Errorf("answer = %q, want it to say the worktree was kept", resp.Error)
	}
	if _, err := os.Stat(filepath.Join(path, "dirty.txt")); err != nil {
		t.Errorf("the uncommitted file is gone: %v", err)
	}
	if out := runGitForAbandon(t, repo, "log", "-1", "--format=%s", "feat/x"); strings.TrimSpace(out) != "work" {
		t.Errorf("branch tip = %q, want the replacement's commit", out)
	}
}

// runGitForAbandon runs git in dir with a hermetic identity and config.
func runGitForAbandon(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
