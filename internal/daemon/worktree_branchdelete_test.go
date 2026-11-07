package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
)

// branchStubs records the branch deletes a removal attempted.
type branchStubs struct {
	local  []string
	remote []string
}

// stubBranchDelete wires a removal that succeeds, facts from facts, and
// recording branch-delete seams. remoteFn may block or fail.
func stubBranchDelete(t *testing.T, facts gitworktree.BranchFacts, remoteFn func(ctx context.Context) error) *branchStubs {
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
	prevFacts, prevDef, prevLocal, prevRemote := branchFactsFn, defaultBranchRefFn, deleteLocalBranchFn, deleteRemoteBranchFn
	branchFactsFn = func(context.Context, string, string, string) (gitworktree.BranchFacts, error) { return facts, nil }
	defaultBranchRefFn = func(context.Context, string) string { return "refs/heads/master" }
	deleteLocalBranchFn = func(_ context.Context, _, branch string) error {
		rec.local = append(rec.local, branch)
		return nil
	}
	deleteRemoteBranchFn = func(ctx context.Context, _, remote, branch string) error {
		rec.remote = append(rec.remote, remote+"/"+branch)
		if remoteFn != nil {
			return remoteFn(ctx)
		}
		return nil
	}
	t.Cleanup(func() {
		branchFactsFn, defaultBranchRefFn, deleteLocalBranchFn, deleteRemoteBranchFn = prevFacts, prevDef, prevLocal, prevRemote
	})
	return rec
}

var testBranchOwner = paneRef{ID: "pane-0000000a", TabID: "tab-0000000a", Name: "claude"}

// optsFor confirms branch for /w/feat-a, and — when remoteName is given —
// the remote target remoteName/branch.
func optsFor(local, remote bool, branch string, remoteName ...string) branchDeleteOpts {
	c := ipc.BranchConfirmation{WorktreePath: "/w/feat-a", Branch: branch}
	if len(remoteName) > 0 {
		c.Remote, c.RemoteBranch = remoteName[0], branch
	}
	return branchDeleteOpts{Local: local, Remote: remote,
		Confirmed: confirmations([]ipc.BranchConfirmation{c}),
		Owners:    map[string]paneRef{"/w/feat-a": testBranchOwner}}
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
	rec := stubBranchDelete(t, gitworktree.BranchFacts{Branch: "feat/a"}, nil)

	d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, optsFor(true, false, "feat/other"))
	if len(rec.local) != 0 {
		t.Fatalf("deleted %v though the dialog showed feat/other", rec.local)
	}
	if kept := eventsOfType(d, "worktree_branch_kept"); len(kept) != 1 || kept[0].PaneID != testBranchOwner.ID {
		t.Errorf("kept events = %+v, want one carrying the pane id", kept)
	}

	d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, optsFor(true, false, "feat/a"))
	if len(rec.local) != 1 || rec.local[0] != "feat/a" {
		t.Errorf("local deletes = %v, want [feat/a]", rec.local)
	}
}

// The default branch is never deleted, whatever the payload says.
func TestRemoveOwnedWorktrees_NeverDeletesDefaultBranch(t *testing.T) {
	d := newTestDaemon(t)
	d.remoteBranchDelete.Store(true)
	rec := stubBranchDelete(t, gitworktree.BranchFacts{Branch: "master", IsDefault: true, Remote: "origin", RemoteBranch: "master"}, nil)

	d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, optsFor(true, true, "master", "origin"))

	if len(rec.local) != 0 || len(rec.remote) != 0 {
		t.Errorf("deleted the default branch: local %v remote %v", rec.local, rec.remote)
	}
}

// The policy is enforced where the push runs: off means no push, and a card
// that says why, carrying the closed pane's id and name.
func TestRemoveOwnedWorktrees_RemoteRefusedWhenPolicyOff(t *testing.T) {
	d := newTestDaemon(t)
	rec := stubBranchDelete(t, gitworktree.BranchFacts{Branch: "feat/a", Remote: "origin", RemoteBranch: "feat/a"}, nil)

	d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, optsFor(false, true, "feat/a", "origin"))

	if len(rec.remote) != 0 {
		t.Fatalf("pushed %v with the policy off", rec.remote)
	}
	kept := eventsOfType(d, "worktree_branch_kept")
	if len(kept) != 1 || kept[0].PaneID != testBranchOwner.ID || kept[0].PaneName != testBranchOwner.Name {
		t.Errorf("kept events = %+v, want one naming pane %s %q", kept, testBranchOwner.ID, testBranchOwner.Name)
	}
}

// With the policy on, the push runs against the remote the facts named.
func TestRemoveOwnedWorktrees_RemoteDeletedWhenPolicyOn(t *testing.T) {
	d := newTestDaemon(t)
	d.remoteBranchDelete.Store(true)
	rec := stubBranchDelete(t, gitworktree.BranchFacts{Branch: "feat/a", Remote: "fork", RemoteBranch: "feat/a"}, nil)

	d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, optsFor(false, true, "feat/a", "fork"))

	if len(rec.remote) != 1 || rec.remote[0] != "fork/feat/a" {
		t.Fatalf("remote deletes = %v, want [fork/feat/a]", rec.remote)
	}
	if got := eventsOfType(d, "worktree_branch_deleted"); len(got) != 1 {
		t.Errorf("deleted events = %+v, want one", got)
	}
}

// A push that never answers must end at the deadline with a card, not hang.
func TestRemoveOwnedWorktrees_RemoteTimesOut(t *testing.T) {
	d := newTestDaemon(t)
	d.remoteBranchDelete.Store(true)
	prev := remoteBranchDeleteTimeout
	remoteBranchDeleteTimeout = 50 * time.Millisecond
	t.Cleanup(func() { remoteBranchDeleteTimeout = prev })
	stubBranchDelete(t, gitworktree.BranchFacts{Branch: "feat/a", Remote: "origin", RemoteBranch: "feat/a"},
		func(ctx context.Context) error {
			<-ctx.Done()
			return errors.New("git push origin :feat/a: timed out")
		})

	done := make(chan struct{})
	go func() {
		d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, optsFor(false, true, "feat/a", "origin"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("removal did not return after the push deadline")
	}
	if kept := eventsOfType(d, "worktree_branch_kept"); len(kept) != 1 {
		t.Errorf("kept events = %+v, want one for the timeout", kept)
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
	rec := stubBranchDelete(t, gitworktree.BranchFacts{Branch: "feat/a"}, nil)

	d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, optsFor(true, false, "feat/a"))

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
	pane.WorktreeOwned, pane.WorktreePath = true, "/w/feat-a"
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
		if !opt.Local || opt.Remote || !opt.localConfirmed("/w/feat-a", gitworktree.BranchFacts{Branch: "feat/a"}) || opt.Owners["/w/feat-a"].ID != pane.ID {
			t.Errorf("opts = %+v, want local, confirmed feat/a, testBranchOwner %s", opt, pane.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tab close never reached the removal seam")
	}
}

// The upstream moved to another remote branch after the dialog opened: the
// push would delete a target nobody confirmed. Kept.
//
// Fails if the remote delete checks only the local branch name.
func TestRemoveOwnedWorktrees_RemoteTargetChangedIsKept(t *testing.T) {
	d := newTestDaemon(t)
	d.remoteBranchDelete.Store(true)
	rec := stubBranchDelete(t, gitworktree.BranchFacts{Branch: "feat/a", Remote: "fork", RemoteBranch: "release"}, nil)

	d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, optsFor(false, true, "feat/a", "origin"))

	if len(rec.remote) != 0 {
		t.Errorf("pushed %v to a target the dialog never showed", rec.remote)
	}
}

// One row cannot confirm another: a confirmation is filed under its own
// worktree, so feat/b shown for another worktree does not cover this one.
func TestRemoveOwnedWorktrees_OtherRowDoesNotConfirm(t *testing.T) {
	d := newTestDaemon(t)
	rec := stubBranchDelete(t, gitworktree.BranchFacts{Branch: "feat/b"}, nil)
	opt := branchDeleteOpts{Local: true,
		Confirmed: confirmations([]ipc.BranchConfirmation{{WorktreePath: "/w/other", Branch: "feat/b"}}),
		Owners:    map[string]paneRef{"/w/feat-a": testBranchOwner}}

	d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, opt)

	if len(rec.local) != 0 {
		t.Errorf("deleted %v on another row's confirmation", rec.local)
	}
}

// With no default branch, nothing can count unmerged commits or guard the
// default: no branch is deleted.
func TestRemoveOwnedWorktrees_UnknownDefaultKeepsBranch(t *testing.T) {
	d := newTestDaemon(t)
	rec := stubBranchDelete(t, gitworktree.BranchFacts{Branch: "feat/a"}, nil)
	prev := branchFactsFn
	branchFactsFn = func(context.Context, string, string, string) (gitworktree.BranchFacts, error) {
		return gitworktree.BranchFacts{Branch: "feat/a"}, nil // DefaultRef unknown
	}
	t.Cleanup(func() { branchFactsFn = prev })

	d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, optsFor(true, false, "feat/a"))

	if len(rec.local) != 0 {
		t.Errorf("deleted %v with the default branch unknown", rec.local)
	}
}

// A remote target equal to the default branch is never pushed to.
func TestRemoveOwnedWorktrees_RemoteDefaultNeverDeleted(t *testing.T) {
	d := newTestDaemon(t)
	d.remoteBranchDelete.Store(true)
	rec := stubBranchDelete(t, gitworktree.BranchFacts{Branch: "feat/a", Remote: "origin", RemoteBranch: "master"}, nil)
	opt := branchDeleteOpts{Remote: true,
		Confirmed: confirmations([]ipc.BranchConfirmation{{WorktreePath: "/w/feat-a", Branch: "feat/a", Remote: "origin", RemoteBranch: "master"}}),
		Owners:    map[string]paneRef{"/w/feat-a": testBranchOwner}}

	d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, opt)

	if len(rec.remote) != 0 {
		t.Errorf("pushed a delete of the remote default branch: %v", rec.remote)
	}
}
