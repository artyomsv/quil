package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
)

// stubRemoveWorktree swaps the branch-preserving removal seam for the test's
// duration and records every (repo, path) it is handed.
func stubRemoveWorktree(t *testing.T, fn func(ctx context.Context, repo, path string) error) *[][]string {
	t.Helper()
	var calls [][]string
	prev := removeWorktreeKeepBranchFn
	removeWorktreeKeepBranchFn = func(ctx context.Context, repo, path string) error {
		calls = append(calls, []string{repo, path})
		if fn == nil {
			return nil
		}
		return fn(ctx, repo, path)
	}
	t.Cleanup(func() { removeWorktreeKeepBranchFn = prev })
	return &calls
}

// The listing (stubWorktreeList, worktree_test.go) reports main-checkout-first,
// and that first entry is the repo root a removal runs in.

// Ownership is the whole gate. A pane whose CWD merely happens to sit in a
// linked worktree — one the user made by hand, one they browsed to — is not
// Quil's to delete, and the close dialog must never offer it. Only a pane this
// daemon ran `git worktree add` for carries WorktreeOwned.
func TestOwnedWorktreePaths_KeepsOnlyPanesQuilMadeAWorktreeFor(t *testing.T) {
	got := ownedWorktreePaths([]*Pane{
		{ID: "pane-0000000a", CWD: "/w/feat-a", WorktreeOwned: true, WorktreePath: "/w/feat-a"},
		{ID: "pane-0000000b", CWD: "/w/feat-b"},
		{ID: "pane-0000000c", WorktreeOwned: true},
	})
	want := []string{"/w/feat-a"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("ownedWorktreePaths = %v, want %v", got, want)
	}
}

// Splitting a worktree pane gives two panes one directory, and the second
// removal would fail against a tree the first one already took — reported as an
// error for something that worked.
func TestOwnedWorktreePaths_DeduplicatesTwoPanesInOneWorktree(t *testing.T) {
	got := ownedWorktreePaths([]*Pane{
		{ID: "pane-0000000a", CWD: "/w/feat-a", WorktreeOwned: true, WorktreePath: "/w/feat-a"},
		{ID: "pane-0000000b", CWD: "/w/feat-a", WorktreeOwned: true, WorktreePath: "/w/feat-a"},
	})
	if len(got) != 1 {
		t.Errorf("ownedWorktreePaths = %v, want one entry", got)
	}
}

// The removal runs in the MAIN checkout, which is what the listing's first
// entry reports. Running it in the worktree being deleted is the shape that
// fails on Windows, where a process cannot remove the directory it is sitting
// in.
func TestRemoveOwnedWorktrees_RunsGitInTheMainCheckout(t *testing.T) {
	d := newTestDaemon(t)
	stubWorktreeList(t, []gitworktree.Worktree{
		{Path: "/repo", Branch: "master", Main: true},
		{Path: "/w/feat-a", Branch: "feat/a"},
	}, nil)
	calls := stubRemoveWorktree(t, nil)

	d.removeOwnedWorktrees([]string{"/w/feat-a"})

	if len(*calls) != 1 {
		t.Fatalf("removal calls = %v, want one", *calls)
	}
	if got, want := (*calls)[0], []string{"/repo", "/w/feat-a"}; got[0] != want[0] || got[1] != want[1] {
		t.Errorf("removal call = %v, want %v", got, want)
	}
}

// The guard that keeps this from being a data-loss bug wearing the feature's
// clothes: a worktree can host panes in OTHER tabs, and closing one of them
// must not pull the directory out from under the rest. Those panes are still
// running processes with that directory as their working directory.
func TestRemoveOwnedWorktrees_SkipsAWorktreeAnotherLivePaneIsIn(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("other")
	pane, err := d.session.CreatePane(tab.ID, filepath.Join("/w", "feat-a"))
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	// Deliberately NO recorded worktree path: this pins the CWD arm of the
	// guard, i.e. a pane that merely SITS in the worktree — an ordinary shell
	// the user cd'd there — rather than one created in it.
	pane.WorktreeOwned = true

	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}}, nil)
	calls := stubRemoveWorktree(t, nil)

	d.removeOwnedWorktrees([]string{filepath.Join("/w", "feat-a")})

	if len(*calls) != 0 {
		t.Errorf("removed a worktree another live pane is in: %v", *calls)
	}
}

// A pane one level DOWN inside the worktree counts too — a shell that cd'd into
// a subdirectory reports it via OSC 7, and the removal takes the whole tree.
func TestRemoveOwnedWorktrees_SkipsWhenALivePaneIsBelowTheWorktree(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("other")
	if _, err := d.session.CreatePane(tab.ID, filepath.Join("/w", "feat-a", "internal", "tui")); err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}}, nil)
	calls := stubRemoveWorktree(t, nil)

	d.removeOwnedWorktrees([]string{filepath.Join("/w", "feat-a")})

	if len(*calls) != 0 {
		t.Errorf("removed a worktree a live pane is below: %v", *calls)
	}
}

// Neither platform lets a directory go while a process still holds it, and the
// pane's own child is reaped ASYNCHRONOUSLY — DestroyPane detaches the pane and
// closes its PTY off-lock, so the removal routinely starts while the shell is
// still exiting. One attempt reports a failure for something that succeeds a
// quarter of a second later.
func TestRemoveOwnedWorktrees_RetriesWhileTheChildIsStillBeingReaped(t *testing.T) {
	d := newTestDaemon(t)
	// The worktree is STILL LISTED, which is what makes the failure transient
	// and the retry right. A listing without it would mean git had already
	// deregistered it, and retrying a path git no longer knows can only repeat
	// the same error — see the deregistered case below.
	stubWorktreeList(t, []gitworktree.Worktree{
		{Path: "/repo", Main: true},
		{Path: "/w/feat-a", Branch: "feat/a"},
	}, nil)
	var attempts int
	calls := stubRemoveWorktree(t, func(context.Context, string, string) error {
		attempts++
		if attempts < 2 {
			return errors.New("The process cannot access the file because it is being used by another process.")
		}
		return nil
	})

	d.removeOwnedWorktrees([]string{"/w/feat-a"})

	if len(*calls) < 2 {
		t.Errorf("removal was attempted %d time(s); a held directory must be retried", len(*calls))
	}
}

// The wire carries a BOOL, never a path: the daemon re-derives which directories
// may go from its own WorktreeOwned records. This pins that the flag is read at
// all — the handler is where it enters the daemon.
func TestHandleDestroyPane_RemovesTheWorktreeWhenAsked(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "/w/feat-a")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.WorktreeOwned = true
	pane.WorktreePath = "/w/feat-a"

	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}}, nil)
	done := make(chan string, 1)
	stubRemoveWorktree(t, func(_ context.Context, _, path string) error {
		done <- path
		return nil
	})

	msg, err := ipc.NewMessage(ipc.MsgDestroyPane, ipc.DestroyPanePayload{
		PaneID: pane.ID, RemoveWorktree: true,
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleDestroyPane(msg)

	select {
	case got := <-done:
		if got != "/w/feat-a" {
			t.Errorf("removed %q, want %q", got, "/w/feat-a")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the worktree was never removed")
	}
}

// Off by default, and the default is what every existing producer sends —
// the MCP destroy_pane tool, an older client, the overlay teardown. A close
// that has always been non-destructive must stay that way.
func TestHandleDestroyPane_LeavesTheWorktreeAloneByDefault(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "/w/feat-a")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.WorktreeOwned = true
	pane.WorktreePath = "/w/feat-a"

	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}}, nil)
	removed := make(chan string, 1)
	stubRemoveWorktree(t, func(_ context.Context, _, path string) error {
		removed <- path
		return nil
	})

	msg, err := ipc.NewMessage(ipc.MsgDestroyPane, ipc.DestroyPanePayload{PaneID: pane.ID})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleDestroyPane(msg)

	select {
	case got := <-removed:
		t.Errorf("an ordinary close removed the worktree %q", got)
	case <-time.After(250 * time.Millisecond):
	}
}

// A tab is closed as a unit, and so are its worktrees: one toggle covers every
// owned checkout the tab holds. Panes must be read BEFORE DestroyTab, which is
// what removes them from the session maps.
func TestHandleDestroyTab_RemovesEveryOwnedWorktreeInTheTab(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	for _, cwd := range []string{"/w/feat-a", "/w/feat-b"} {
		pane, err := d.session.CreatePane(tab.ID, cwd)
		if err != nil {
			t.Fatalf("CreatePane: %v", err)
		}
		pane.WorktreeOwned = true
		pane.WorktreePath = cwd
	}

	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}}, nil)
	removed := make(chan string, 4)
	stubRemoveWorktree(t, func(_ context.Context, _, path string) error {
		removed <- path
		return nil
	})

	msg, err := ipc.NewMessage(ipc.MsgDestroyTab, ipc.DestroyTabPayload{
		TabID: tab.ID, RemoveWorktree: true,
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleDestroyTab(msg)

	got := map[string]bool{}
	for range 2 {
		select {
		case p := <-removed:
			got[p] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of 2 worktrees were removed: %v", len(got), got)
		}
	}
	if !got["/w/feat-a"] || !got["/w/feat-b"] {
		t.Errorf("removed %v, want both worktrees", got)
	}
}

func TestPathWithin(t *testing.T) {
	tests := []struct {
		name          string
		parent, child string
		want          bool
	}{
		{"the directory itself", "/w/feat-a", "/w/feat-a", true},
		{"a subdirectory", "/w/feat-a", "/w/feat-a/internal", true},
		{"a sibling", "/w/feat-a", "/w/feat-b", false},
		// The failure a plain strings.HasPrefix produces: a sibling whose name
		// merely starts with the same characters would be treated as inside,
		// and closing one pane would delete the other's checkout.
		{"a sibling with a shared prefix", "/w/feat-a", "/w/feat-a2", false},
		{"a parent", "/w/feat-a", "/w", false},
		{"trailing separator", "/w/feat-a/", "/w/feat-a", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pathWithin(tt.parent, tt.child); got != tt.want {
				t.Errorf("pathWithin(%q, %q) = %v, want %v", tt.parent, tt.child, got, tt.want)
			}
		})
	}
}

// The delete target is the path the worktree was CREATED at, never the pane's
// current CWD — and the difference is a force-delete of somebody else's
// checkout.
//
// `WorktreeOwned` is set once, at creation. `CWD` is a live cursor that OSC 7
// rewrites on every `cd` (gitcache.go says so in as many words, and
// handleUpdatePane is what writes it). So a pane created in feat-a whose shell
// walks into feat-b — an ordinary thing to do, and something a compromised or
// prompt-injected agent can do with one escape sequence — carries
// WorktreeOwned=true with CWD pointing at a worktree this daemon never made.
// Closing it with the toggle armed then removed feat-b, uncommitted work and
// all, having priced feat-a in the dialog.
func TestOwnedWorktreePaths_UsesTheRecordedPathNotTheShellsCWD(t *testing.T) {
	got := ownedWorktreePaths([]*Pane{{
		ID:            "pane-0000000a",
		WorktreeOwned: true,
		WorktreePath:  "/w/feat-a",
		CWD:           "/w/feat-b", // the shell wandered
	}})

	if len(got) != 1 || got[0] != "/w/feat-a" {
		t.Errorf("ownedWorktreePaths = %v, want the recorded /w/feat-a", got)
	}
}

// A pane from a snapshot written BEFORE the path was recorded has ownership and
// no path. It is not offered: the only directory such a pane can name is its
// CWD, which is exactly the value that cannot be trusted for this. Losing the
// offer on an old pane is a convenience; deleting the wrong checkout is not.
func TestOwnedWorktreePaths_SkipsAPaneWithNoRecordedPath(t *testing.T) {
	got := ownedWorktreePaths([]*Pane{{
		ID:            "pane-0000000a",
		WorktreeOwned: true,
		CWD:           "/w/feat-a",
	}})

	if len(got) != 0 {
		t.Errorf("ownedWorktreePaths = %v, want none — the pane records no worktree path", got)
	}
}

// The in-use guard has the same CWD problem in reverse: a pane whose shell has
// walked out of its own worktree still LIVES there — a build running in it, an
// agent that cd'd into a subrepo — so asking only where the shell last said it
// was would delete the checkout out from under it.
func TestRemoveOwnedWorktrees_SkipsAWorktreeAPaneWasCreatedInEvenIfItsShellMoved(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("other")
	pane, err := d.session.CreatePane(tab.ID, "/elsewhere")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.WorktreeOwned = true
	pane.WorktreePath = "/w/feat-a"

	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}}, nil)
	calls := stubRemoveWorktree(t, nil)

	d.removeOwnedWorktrees([]string{"/w/feat-a"})

	if len(*calls) != 0 {
		t.Errorf("removed a worktree a live pane was created in: %v", *calls)
	}
}

// A worktree whose repository cannot be resolved is KEPT, and the removal seam
// is never reached. The listing is how the main checkout — the directory git
// runs in — is recovered, so an error or an empty answer there means the daemon
// does not know enough to delete anything safely.
func TestRemoveOwnedWorktrees_KeepsTheWorktreeWhenItsRepositoryCannotBeResolved(t *testing.T) {
	tests := []struct {
		name string
		list []gitworktree.Worktree
		err  error
	}{
		{"the listing failed", nil, errors.New("not a git repository")},
		{"the listing is empty", nil, nil},
		{"the main checkout has no path", []gitworktree.Worktree{{Main: true}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newTestDaemon(t)
			stubWorktreeList(t, tt.list, tt.err)
			calls := stubRemoveWorktree(t, nil)

			d.removeOwnedWorktrees([]string{"/w/feat-a"})

			if len(*calls) != 0 {
				t.Errorf("removal ran without a resolved repository: %v", *calls)
			}
		})
	}
}

// A FAILED attempt may already have done most of the work, and treating the
// failure as total is what leaves a mess behind.
//
// Measured on Windows: `git worktree remove --force` deletes the tree and the
// admin entry, then fails on the now-empty directory itself because the pane's
// shell still has it as its working directory. git exits non-zero, so the retry
// runs — and hits "is not a working tree", because the previous attempt already
// deregistered it. The removal is then reported as failed after three attempts
// while the user is left with an orphaned empty directory that `git worktree
// list` no longer mentions.
//
// Once git no longer knows the path, finishing the job is ours: the directory
// is the only residue.
func TestRemoveOwnedWorktrees_FinishesADeregisteredWorktreeLeftOnDisk(t *testing.T) {
	d := newTestDaemon(t)
	// A real directory, empty — exactly what a partially-completed removal
	// leaves behind.
	path := filepath.Join(t.TempDir(), "feat-a")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// The listing no longer contains it: git has deregistered it.
	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}}, nil)
	calls := stubRemoveWorktree(t, func(context.Context, string, string) error {
		return errors.New("fatal: '" + path + "' is not a working tree")
	})

	d.removeOwnedWorktrees([]string{path})

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the deregistered worktree directory survived: %v", err)
	}
	// One attempt is enough once git has disowned it — retrying a path git no
	// longer knows can only produce the same error.
	if len(*calls) != 1 {
		t.Errorf("git was called %d times; a deregistered path must not be retried", len(*calls))
	}
}

// The inverse, and the reason the check is against the LISTING rather than the
// error text: while git still knows the worktree, the failure is transient (the
// child is being reaped) and the retry is what makes it work. Nothing may be
// deleted from under it.
func TestRemoveOwnedWorktrees_DoesNotTouchTheDirectoryWhileGitStillOwnsIt(t *testing.T) {
	d := newTestDaemon(t)
	path := filepath.Join(t.TempDir(), "feat-a")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// STILL registered.
	stubWorktreeList(t, []gitworktree.Worktree{
		{Path: "/repo", Main: true},
		{Path: path, Branch: "feat/a"},
	}, nil)
	stubRemoveWorktree(t, func(context.Context, string, string) error {
		return errors.New("The process cannot access the file because it is being used by another process.")
	})

	d.removeOwnedWorktrees([]string{path})

	if _, err := os.Stat(path); err != nil {
		t.Errorf("the directory was removed while git still owned the worktree: %v", err)
	}
}

// The directory cleanup needs the SAME retry the git call has, and shipping it
// without one left the empty directory behind on every real close.
//
// Measured against a live daemon: git deregisters the worktree on the first
// attempt and fails to delete the folder, we take over — and the pane's shell
// is STILL holding that folder as its working directory, because DestroyPane
// closes the PTY asynchronously. One os.Remove there fails for the same reason
// git's did, a second or two before it would have succeeded.
func TestFinishRemovedWorktree_RetriesWhileTheDirectoryIsHeld(t *testing.T) {
	var attempts int
	prev := removeDirFn
	removeDirFn = func(path string) error {
		attempts++
		if attempts < 3 {
			return errors.New("The process cannot access the file because it is being used by another process.")
		}
		return nil
	}
	t.Cleanup(func() { removeDirFn = prev })

	finishRemovedWorktree("/w/feat-a")

	if attempts < 3 {
		t.Errorf("the directory removal was attempted %d time(s); a held directory must be retried", attempts)
	}
}

// A directory that is not there is the SUCCESS case, not a failure: git managed
// the whole removal and only its exit code misled us.
func TestFinishRemovedWorktree_TreatsAnAlreadyGoneDirectoryAsDone(t *testing.T) {
	var attempts int
	prev := removeDirFn
	removeDirFn = func(string) error {
		attempts++
		return os.ErrNotExist
	}
	t.Cleanup(func() { removeDirFn = prev })

	finishRemovedWorktree("/w/feat-a")

	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 — a missing directory is the outcome we wanted", attempts)
	}
}
