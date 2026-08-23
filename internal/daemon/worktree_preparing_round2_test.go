package daemon

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	apty "github.com/artyomsv/quil/internal/pty"
)

// failingSession is a fakeSession whose child never starts.
type failingSession struct{ fakeSession }

func (f *failingSession) Start(cmd string, args ...string) error {
	return fmt.Errorf("fork/exec %s: no such file or directory", cmd)
}

func stubFailingSpawn(t *testing.T) {
	t.Helper()
	prev := newSessionFn
	newSessionFn = func(cols, rows int) apty.Session { return &failingSession{} }
	t.Cleanup(func() { newSessionFn = prev })
}

// A replace whose SPAWN fails after the swap must not leave the tab empty.
//
// replacePaneAt destroys the new pane and reports swapped=true, and nothing on
// that path calls ensureTabNotEmpty — its only callers are the destroy and exit
// paths. For an ordinary split-replace that is harmless, because the tab keeps
// its other panes. The new-tab worktree flow is the ONE shape where the replaced
// pane is the tab's only pane, so the empty tab is guaranteed — against
// handleCreateTab's own "the tab must never be pane-less" invariant, and
// reachable whenever the requested plugin's binary is missing.
//
// createFirstPaneWorktree correctly SKIPS failPreparingPane there (Swapped), so
// without this the user's only notice is a three-second flash over a blank tab.
func TestReplacePaneAt_SpawnFailureLeavesAPaneCarryingTheReason(t *testing.T) {
	d := newTestDaemon(t)
	stubFailingSpawn(t)
	tab := d.session.CreateTab("t")
	old, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	_, swapped, err := d.replacePaneAt(ipc.CreatePanePayload{
		TabID:         tab.ID,
		ReplacePaneID: old.ID,
	}, t.TempDir(), "terminal")
	if err == nil {
		t.Fatal("the stubbed spawn did not fail")
	}
	if !swapped {
		t.Fatal("swapped = false — the old pane went with the swap and cannot come back")
	}

	panes := d.session.Panes(tab.ID)
	if len(panes) != 1 {
		t.Fatalf("tab has %d panes after a failed replace, want 1 — an empty tab has nothing to click and nothing to close", len(panes))
	}
	panes[0].PluginMu.Lock()
	reason, pty := panes[0].SpawnError, panes[0].PTY
	panes[0].PluginMu.Unlock()
	if reason == "" {
		t.Error("the recovery pane carries no reason — the failure would live only in the log")
	}
	if pty != nil {
		t.Error("the recovery pane spawned a shell; renderSpawnError would then cover a running child permanently")
	}
}

// A replace on a tab that still has SIBLINGS must not gain an extra pane: the
// recovery is for the empty case only.
func TestReplacePaneAt_SpawnFailureLeavesASiblingTabAlone(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	sibling, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	old, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	stubFailingSpawn(t)

	if _, _, err := d.replacePaneAt(ipc.CreatePanePayload{
		TabID:         tab.ID,
		ReplacePaneID: old.ID,
	}, t.TempDir(), "terminal"); err == nil {
		t.Fatal("the stubbed spawn did not fail")
	}

	panes := d.session.Panes(tab.ID)
	if len(panes) != 1 || panes[0].ID != sibling.ID {
		t.Errorf("tab has %d panes, want just the surviving sibling", len(panes))
	}
}

// A snapshot landing inside the checkout window persists the placeholder as an
// ordinary terminal in the REPOSITORY ROOT — handleCreateTab requests one
// immediately, so the pane is on disk for the whole checkout, not for a narrow
// window. Restored, it lazy-spawns a live shell there: exactly the state this
// feature exists to remove, brought back from disk.
//
// PreparingWorktree itself must stay unpersisted (a restored pane would spin for
// an add nobody is running), so the marker is a SEPARATE persisted fact —
// "this pane's worktree never finished" — which restore turns into a visible
// refusal rather than a spinner.
func TestSnapshot_PersistsThatAWorktreeWasInterrupted(t *testing.T) {
	dir := t.TempDir()
	d := newTestDaemonInDir(t, dir)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.PreparingWorktree = "feat/x"
	d.snapshot()

	raw, err := os.ReadFile(config.WorkspacePath())
	if err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if strings.Contains(string(raw), "feat/x") {
		t.Error("the branch was persisted — a restored pane would spin for an add nobody is running")
	}
	if !strings.Contains(string(raw), "worktree_interrupted") {
		t.Error("nothing records that the worktree never finished — the pane restores as a shell in the repository root")
	}
}

// And restore must REFUSE to spawn it, with the reason on screen. Alt+R is the
// way out, exactly as it is for a missing worktree.
func TestSpawnRestoredPane_RefusesAnInterruptedWorktree(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.Type = "terminal"
	pane.WorktreeInterrupted = true

	d.spawnRestoredPane(pane)

	pane.PluginMu.Lock()
	pty, reason := pane.PTY, pane.SpawnError
	pane.PluginMu.Unlock()
	if pty != nil {
		t.Error("restore spawned a shell in the repository root — the bug this feature removes, restored from disk")
	}
	if !strings.Contains(reason, "worktree") {
		t.Errorf("SpawnError = %q, want it to name the interrupted worktree", reason)
	}
}

// The snapshot test above greps for a string, which also passes if the pane
// vanished from the snapshot entirely. Pin that the pane IS persisted.
func TestSnapshot_StillPersistsAnInterruptedPlaceholderPane(t *testing.T) {
	dir := t.TempDir()
	d := newTestDaemonInDir(t, dir)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.PreparingWorktree = "feat/x"
	d.snapshot()

	raw, err := os.ReadFile(config.WorkspacePath())
	if err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if !strings.Contains(string(raw), pane.ID) {
		t.Error("the placeholder pane is absent from the snapshot — the tab would restore pane-less")
	}
}

// A FAILED add must leave the same persisted marker an interrupted one does.
//
// failPreparingPane clears PreparingWorktree and writes SpawnError — and
// SpawnError is deliberately never persisted. So the next snapshot recorded the
// pane with no marker and no error, at its recorded CWD, which is the repository
// ROOT: a daemon restart then lazy-spawned a live shell there. Same bug the
// interrupted marker exists to fix, on the failure branch instead.
func TestFailPreparingPane_LeavesTheInterruptedMarkerForRestore(t *testing.T) {
	dir := t.TempDir()
	d := newTestDaemonInDir(t, dir)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.PreparingWorktree = "feat/x"

	d.failPreparingPane(pane.ID, "worktree not created: fatal: a branch named 'feat/x' already exists")

	pane.PluginMu.Lock()
	interrupted, preparing := pane.WorktreeInterrupted, pane.PreparingWorktree
	pane.PluginMu.Unlock()
	if preparing != "" {
		t.Errorf("PreparingWorktree = %q after the failure, want it cleared", preparing)
	}
	if !interrupted {
		t.Fatal("no interrupted marker after a FAILED add — a restart spawns a shell in the repository root")
	}

	d.snapshot()
	raw, err := os.ReadFile(config.WorkspacePath())
	if err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if !strings.Contains(string(raw), "worktree_interrupted") {
		t.Error("the marker did not survive to the snapshot")
	}
}

// Alt+R is the escape from BOTH refusals, so spawnPane has to clear the flag —
// otherwise the pane refuses again on its next lazy spawn, after the user has
// already retried it. The sibling SpawnError clear has its own test for exactly
// this reason.
func TestSpawnPane_ClearsAStaleWorktreeInterrupted(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.Type = "terminal"
	pane.WorktreeInterrupted = true

	if err := d.spawnPane(pane, &fakeSession{}, false); err != nil {
		t.Fatalf("spawnPane: %v", err)
	}

	pane.PluginMu.Lock()
	interrupted := pane.WorktreeInterrupted
	pane.PluginMu.Unlock()
	if interrupted {
		t.Error("WorktreeInterrupted survived a successful spawn — the pane would refuse again on its next lazy spawn")
	}
}
