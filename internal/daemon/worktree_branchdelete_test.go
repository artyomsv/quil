package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
)

// branchStubs records the branch deletes a removal attempted.
type branchStubs struct {
	local []string
}

// stubBranchDelete wires a removal that succeeds, facts from facts, and a
// recording local-delete seam.
func stubBranchDelete(t *testing.T, facts gitworktree.BranchFacts) *branchStubs {
	t.Helper()
	stubWorktreeList(t, []gitworktree.Worktree{
		{Path: "/repo", Branch: "master", Main: true},
		{Path: "/w/feat-a", Branch: facts.Branch},
	}, nil)
	stubRemoveWorktree(t, nil)
	if facts.DefaultRef == "" {
		// A known default branch, as in nearly every repository; the unknown
		// case has its own test.
		facts.DefaultRef = "refs/heads/master"
	}
	rec := &branchStubs{}
	prevFacts, prevDef, prevLocal := branchFactsFn, defaultBranchRefFn, deleteLocalBranchFn
	branchFactsFn = func(context.Context, string, string, string) (gitworktree.BranchFacts, error) { return facts, nil }
	defaultBranchRefFn = func(context.Context, string) string { return "refs/heads/master" }
	deleteLocalBranchFn = func(_ context.Context, _, branch, _ string) error {
		rec.local = append(rec.local, branch)
		return nil
	}
	t.Cleanup(func() {
		branchFactsFn, defaultBranchRefFn, deleteLocalBranchFn = prevFacts, prevDef, prevLocal
	})
	return rec
}

var testBranchOwner = paneRef{ID: "pane-0000000a", TabID: "tab-0000000a", Name: "claude"}

// optsFor confirms branch for /w/feat-a, a Quil-made worktree (its identity
// token is recorded; stubRemoveWorktree makes the mark match).
func optsFor(local bool, branch string) branchDeleteOpts {
	return branchDeleteOpts{Local: local,
		Confirmed: confirmations([]ipc.BranchConfirmation{{WorktreePath: "/w/feat-a", Branch: branch}}),
		Owners:    map[string]paneRef{"/w/feat-a": testBranchOwner},
		Tokens:    map[string]string{"/w/feat-a": "test-token"}}
}

// eventsOfType returns the queued events of one type.
func eventsOfType(d *Daemon, typ string) []PaneEvent {
	var out []PaneEvent
	for _, e := range d.events.Events() {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// The branch the daemon reads is deleted only when the dialog SHOWED it: a
// worktree switched to another branch after the dialog opened keeps it.
//
// Fails if confirmed() is skipped, or compares against anything but the
// branch read from git.
func TestRemoveOwnedWorktrees_DeletesLocalBranchOnlyWhenConfirmed(t *testing.T) {
	d := newTestDaemon(t)
	rec := stubBranchDelete(t, gitworktree.BranchFacts{Branch: "feat/a"})

	d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, optsFor(true, "feat/other"))
	if len(rec.local) != 0 {
		t.Fatalf("deleted %v though the dialog showed feat/other", rec.local)
	}
	if kept := eventsOfType(d, "worktree_branch_kept"); len(kept) != 1 || kept[0].PaneID != testBranchOwner.ID {
		t.Errorf("kept events = %+v, want one carrying the pane id", kept)
	}

	d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, optsFor(true, "feat/a"))
	if len(rec.local) != 1 || rec.local[0] != "feat/a" {
		t.Errorf("local deletes = %v, want [feat/a]", rec.local)
	}
}

// The default branch is never deleted, whatever the payload says.
func TestRemoveOwnedWorktrees_NeverDeletesDefaultBranch(t *testing.T) {
	d := newTestDaemon(t)
	rec := stubBranchDelete(t, gitworktree.BranchFacts{Branch: "master", IsDefault: true})

	d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, optsFor(true, "master"))

	if len(rec.local) != 0 {
		t.Errorf("deleted the default branch: %v", rec.local)
	}
}

// A worktree still in use is kept, so its branch is too — git would refuse,
// and a branch call there would only produce a misleading error.
func TestRemoveOwnedWorktrees_NoBranchCallWhenWorktreeKept(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("other")
	if _, err := d.session.CreatePane(tab.ID, "/w/feat-a"); err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	rec := stubBranchDelete(t, gitworktree.BranchFacts{Branch: "feat/a"})

	d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, optsFor(true, "feat/a"))

	if len(rec.local) != 0 {
		t.Errorf("deleted %v while a pane is in the worktree", rec.local)
	}
}

// The tab close path builds the options from its payload and hands them to
// the seam — it used to bypass the seam entirely.
func TestHandleDestroyTab_PassesBranchOpts(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("wt")
	pane, err := d.session.CreatePane(tab.ID, "/w/feat-a")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.WorktreeOwned, pane.WorktreePath, pane.WorktreeToken = true, "/w/feat-a", "test-token"
	got := make(chan branchDeleteOpts, 1)
	prev := removeOwnedWorktreesFn
	removeOwnedWorktreesFn = func(_ *Daemon, _ []string, opt branchDeleteOpts) { got <- opt }
	t.Cleanup(func() { removeOwnedWorktreesFn = prev })

	msg, err := ipc.NewMessage(ipc.MsgDestroyTab, ipc.DestroyTabPayload{
		TabID: tab.ID, RemoveWorktree: true, DeleteBranch: true, Confirmations: []ipc.BranchConfirmation{{WorktreePath: "/w/feat-a", Branch: "feat/a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	d.handleDestroyTab(nil, msg)

	select {
	case opt := <-got:
		if !opt.Local || !opt.localConfirmed("/w/feat-a", gitworktree.BranchFacts{Branch: "feat/a"}) || opt.Owners["/w/feat-a"].ID != pane.ID {
			t.Errorf("opts = %+v, want local, confirmed feat/a, testBranchOwner %s", opt, pane.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tab close never reached the removal seam")
	}
}

// One row cannot confirm another: a confirmation is filed under its own
// worktree, so feat/b shown for another worktree does not cover this one.
func TestRemoveOwnedWorktrees_OtherRowDoesNotConfirm(t *testing.T) {
	d := newTestDaemon(t)
	rec := stubBranchDelete(t, gitworktree.BranchFacts{Branch: "feat/b"})
	opt := branchDeleteOpts{Local: true,
		Confirmed: confirmations([]ipc.BranchConfirmation{{WorktreePath: "/w/other", Branch: "feat/b"}}),
		Owners:    map[string]paneRef{"/w/feat-a": testBranchOwner},
		Tokens:    map[string]string{"/w/feat-a": "test-token"}}

	d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, opt)

	if len(rec.local) != 0 {
		t.Errorf("deleted %v on another row's confirmation", rec.local)
	}
}

// With no default branch, nothing can count unmerged commits or guard the
// default: no branch is deleted.
func TestRemoveOwnedWorktrees_UnknownDefaultKeepsBranch(t *testing.T) {
	d := newTestDaemon(t)
	rec := stubBranchDelete(t, gitworktree.BranchFacts{Branch: "feat/a"})
	prev := branchFactsFn
	branchFactsFn = func(context.Context, string, string, string) (gitworktree.BranchFacts, error) {
		return gitworktree.BranchFacts{Branch: "feat/a"}, nil // DefaultRef unknown
	}
	t.Cleanup(func() { branchFactsFn = prev })

	d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, optsFor(true, "feat/a"))

	if len(rec.local) != 0 {
		t.Errorf("deleted %v with the default branch unknown", rec.local)
	}
}
