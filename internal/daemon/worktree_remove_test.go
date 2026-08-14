package daemon

import (
	"context"
	"errors"
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
		{ID: "pane-0000000a", CWD: "/w/feat-a", WorktreeOwned: true},
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
		{ID: "pane-0000000a", CWD: "/w/feat-a", WorktreeOwned: true},
		{ID: "pane-0000000b", CWD: "/w/feat-a", WorktreeOwned: true},
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
	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}}, nil)
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
