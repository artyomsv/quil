package daemon

import (
	"context"
	"errors"
	"os"
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
	t.Setenv("QUIL_HOME", t.TempDir())
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	req := worktreeCreate(tab.ID, t.TempDir(), "feat/id")
	checkout := gitworktree.DerivePath(req.Worktree.RepoRoot, req.Worktree.Branch)
	if err := os.MkdirAll(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	var wrotePath, wroteToken string
	prev := writeOwnerMarkFn
	writeOwnerMarkFn = func(_ context.Context, p, tok string) error { wrotePath, wroteToken = p, tok; return nil }
	t.Cleanup(func() { writeOwnerMarkFn = prev })

	pane, _, err := d.createPaneInWorktree(req, checkout)
	if err != nil {
		t.Fatalf("createPaneInWorktree: %v", err)
	}
	pane.PluginMu.Lock()
	tok, path := pane.WorktreeToken, pane.WorktreePath
	pane.PluginMu.Unlock()
	if tok == "" || tok != wroteToken || wrotePath != path {
		t.Errorf("pane token %q path %q; mark written %q at %q", tok, path, wroteToken, wrotePath)
	}
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
