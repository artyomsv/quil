package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
)

// Regression tests for the daemon-side defects a code review found in the
// worktree-replace work. Each one fails against the code as it was reviewed.

// stubRemove swaps the cleanup seam and records what it was asked to remove.
func stubRemove(t *testing.T, fn func(ctx context.Context, repo, path, branch string) error) *[][]string {
	t.Helper()
	var calls [][]string
	prev := removeWorktreeFn
	removeWorktreeFn = func(ctx context.Context, repo, path, branch string) error {
		calls = append(calls, []string{repo, path, branch})
		if fn == nil {
			return nil
		}
		return fn(ctx, repo, path, branch)
	}
	t.Cleanup(func() { removeWorktreeFn = prev })
	return &calls
}

// realRepoDir returns an existing directory to stand in for a repository root.
// The absoluteness guard rejects a bare "/repo" on Windows, so the fixtures
// that must reach git use a real temp path.
func realRepoDir(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "proj", "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return root
}

// An add that succeeds followed by a create that fails used to leave the
// checkout on disk with a branch pointing at it. The next attempt at the same
// name then failed with "already exists" against a directory the user never
// made — indistinguishable from a name genuinely in use.
func TestWorktreeAdd_RemovesTheWorktreeWhenThePaneCannotBeCreated(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	repo := realRepoDir(t)
	// The add "succeeds" without creating the directory, so the stat inside
	// createPaneInWorktree fails — the same shape as a checkout that vanished.
	stubAdd(t, func(context.Context, string, string, string) error { return nil })
	removes := stubRemove(t, nil)

	resp := d.worktreeAddAndCreate(worktreeCreate(tab.ID, repo, "feat/x"))

	if resp.Error == "" {
		t.Fatal("a create that could not place its pane reported success")
	}
	if len(*removes) != 1 {
		t.Fatalf("cleanup ran %d times, want 1 — the worktree is orphaned on disk", len(*removes))
	}
	call := (*removes)[0]
	if call[0] != repo {
		t.Errorf("cleanup ran against %q, want the repository %q", call[0], repo)
	}
	if want := gitworktree.DerivePath(repo, "feat/x"); call[1] != want {
		t.Errorf("cleanup removed %q, want the derived path %q", call[1], want)
	}
	if call[2] != "feat/x" {
		t.Errorf("cleanup deleted branch %q, want feat/x", call[2])
	}
}

// A failed ADD must NOT run the cleanup: there is nothing to remove, and
// `git worktree remove` against a path git never created is noise in the log
// that reads like a second failure.
func TestWorktreeAdd_DoesNotCleanUpWhenTheAddItselfFailed(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	repo := realRepoDir(t)
	stubAdd(t, func(context.Context, string, string, string) error {
		return errors.New("fatal: a branch named 'feat/x' already exists")
	})
	removes := stubRemove(t, nil)

	if resp := d.worktreeAddAndCreate(worktreeCreate(tab.ID, repo, "feat/x")); resp.Error == "" {
		t.Fatal("a failed add reported success")
	}
	if len(*removes) != 0 {
		t.Errorf("cleanup ran %d times for a worktree that was never created", len(*removes))
	}
}

// ReplacePane resolves the pane id GLOBALLY and swaps it inside its OWN tab, so
// a payload pairing tab A with a pane in tab B destroys the pane in B while the
// response echoes A — and the client arms and unwinds its placeholder on the
// echoed tab. The existing p.TabID checks read as tab scoping without providing
// it. Any IPC client can send the pair.
func TestWorktreeAdd_RefusesAReplaceTargetInAnotherTab(t *testing.T) {
	d := newTestDaemon(t)
	tabA := d.session.CreateTab("a")
	tabB := d.session.CreateTab("b")
	repo := realRepoDir(t)
	victim, err := d.session.CreatePane(tabB.ID, repo)
	if err != nil {
		t.Fatalf("seed pane: %v", err)
	}
	calls := stubAdd(t, func(_ context.Context, _, path, _ string) error { return os.MkdirAll(path, 0o755) })

	p := worktreeCreate(tabA.ID, repo, "feat/x")
	p.ReplacePaneID = victim.ID
	resp := d.worktreeAddAndCreate(p)

	if resp.Error == "" {
		t.Fatal("a replace targeting another tab's pane was accepted")
	}
	if d.session.Pane(victim.ID) == nil {
		t.Error("the pane in the OTHER tab was destroyed")
	}
	if len(*calls) != 0 {
		t.Errorf("git ran %d times for a refused replace — the check must precede the checkout", len(*calls))
	}
}

// A missing replace target is refused before the checkout too, so a bogus id
// cannot spend a full `git worktree add` per request.
func TestWorktreeAdd_RefusesAMissingReplaceTargetBeforeRunningGit(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	repo := realRepoDir(t)
	calls := stubAdd(t, func(context.Context, string, string, string) error { return nil })

	p := worktreeCreate(tab.ID, repo, "feat/x")
	p.ReplacePaneID = "pane-doesnotexist"
	resp := d.worktreeAddAndCreate(p)

	if resp.Error == "" {
		t.Fatal("a replace naming no pane was accepted")
	}
	if len(*calls) != 0 {
		t.Errorf("git ran %d times for a replace target that does not exist", len(*calls))
	}
}

// The replace target can be closed DURING the checkout — the window is seconds
// wide. Without the post-add re-check ReplacePane returns "pane not found", and
// the worktree git just made is orphaned.
func TestWorktreeAdd_ReplaceTargetClosedDuringTheAddCleansUp(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	repo := realRepoDir(t)
	victim, err := d.session.CreatePane(tab.ID, repo)
	if err != nil {
		t.Fatalf("seed pane: %v", err)
	}
	// The user closes that pane while the checkout runs.
	stubAdd(t, func(_ context.Context, _, path, _ string) error {
		d.session.DestroyPane(victim.ID)
		return os.MkdirAll(path, 0o755)
	})
	removes := stubRemove(t, nil)

	p := worktreeCreate(tab.ID, repo, "feat/x")
	p.ReplacePaneID = victim.ID
	resp := d.worktreeAddAndCreate(p)

	if resp.Error == "" {
		t.Fatal("a replace whose target vanished mid-checkout reported success")
	}
	if len(*removes) != 1 {
		t.Errorf("cleanup ran %d times, want 1 — the worktree is orphaned", len(*removes))
	}
	if resp.Swapped {
		t.Error("Swapped is set although no swap happened — the client would refuse to restore a live pane")
	}
}

// Swapped is what stops the client restoring a pane the daemon destroyed. It
// must be true whenever the swap ran, INCLUDING on the error paths that follow
// it, which is exactly why it cannot be inferred from Error.
func TestWorktreeAdd_ReplaceSuccessReportsSwapped(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	repo := realRepoDir(t)
	old, err := d.session.CreatePane(tab.ID, repo)
	if err != nil {
		t.Fatalf("seed pane: %v", err)
	}
	stubAdd(t, func(_ context.Context, _, path, _ string) error { return os.MkdirAll(path, 0o755) })

	p := worktreeCreate(tab.ID, repo, "feat/x")
	p.ReplacePaneID = old.ID
	resp := d.worktreeAddAndCreate(p)

	if resp.Error != "" {
		t.Fatalf("replace failed: %s", resp.Error)
	}
	if !resp.Swapped {
		t.Error("a successful replace did not report Swapped — the client would hold the old pane forever")
	}
}

// A plain create never swaps anything, so Swapped must stay false or the client
// would dispose a pane it should have kept.
func TestWorktreeAdd_PlainCreateDoesNotReportSwapped(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	repo := realRepoDir(t)
	stubAdd(t, func(_ context.Context, _, path, _ string) error { return os.MkdirAll(path, 0o755) })

	resp := d.worktreeAddAndCreate(worktreeCreate(tab.ID, repo, "feat/x"))

	if resp.Error != "" {
		t.Fatalf("create failed: %s", resp.Error)
	}
	if resp.Swapped {
		t.Error("a plain create reported Swapped")
	}
}

// The tab closing mid-checkout was already refused; it must clean up too.
func TestWorktreeAdd_TabClosedDuringTheAddCleansUp(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	repo := realRepoDir(t)
	stubAdd(t, func(_ context.Context, _, path, _ string) error {
		d.session.DestroyTab(tab.ID)
		return os.MkdirAll(path, 0o755)
	})
	removes := stubRemove(t, nil)

	if resp := d.worktreeAddAndCreate(worktreeCreate(tab.ID, repo, "feat/x")); resp.Error == "" {
		t.Fatal("a create into a closed tab reported success")
	}
	if len(*removes) != 1 {
		t.Errorf("cleanup ran %d times, want 1", len(*removes))
	}
}

// ipc.CreatePaneRespPayload.Swapped must survive the wire — it is a new field
// and a client that never sees it would restore destroyed panes.
func TestCreatePaneRespPayload_SwappedRoundTrips(t *testing.T) {
	msg, err := ipc.NewMessage(ipc.MsgCreatePaneResp, ipc.CreatePaneRespPayload{
		TabID: "tab-1", Error: "boom", Swapped: true,
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var got ipc.CreatePaneRespPayload
	if err := msg.DecodePayload(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Swapped {
		t.Error("Swapped did not survive the round trip")
	}
}
