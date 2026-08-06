package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// A worktree-owned pane whose directory is gone must come up VISIBLY BROKEN,
// not quietly relocated. Blanking the CWD reaches d.defaultCWD(), so the pane
// would return in the MAIN checkout on whatever branch that is — and a claude
// pane resumes its recorded session there, continuing the conversation against
// the wrong tree, with only a daemon-log line to say so.
func TestSpawnRestoredPane_WorktreeGoneComesUpBroken(t *testing.T) {
	d := newTestDaemon(t)
	gone := filepath.Join(t.TempDir(), "vanished")
	pane := &Pane{ID: "pane-0000000a", CWD: gone, WorktreeOwned: true, Type: "terminal"}

	d.spawnRestoredPane(pane)

	if pane.CWD != gone {
		t.Errorf("CWD = %q, want it left at %q — a blanked CWD relocates to the daemon default", pane.CWD, gone)
	}
	if pane.SpawnError == "" {
		t.Error("no SpawnError set — the failure would live only in a log nobody reads")
	}
	if !strings.Contains(pane.SpawnError, gone) {
		t.Errorf("SpawnError = %q, want it to name the missing worktree", pane.SpawnError)
	}
	if pane.PTY != nil {
		t.Error("a PTY was spawned for a pane whose worktree is gone")
	}
}

// A pane whose ORDINARY browsed CWD is gone keeps today's behaviour. The two
// losses are not the same: a stale browsed path costs a convenience, a missing
// worktree costs the isolation the pane exists for.
func TestSpawnRestoredPane_OrdinaryMissingCWDStillFallsBack(t *testing.T) {
	d := newTestDaemon(t)
	pane := &Pane{ID: "pane-0000000b", CWD: filepath.Join(t.TempDir(), "vanished"), Type: "terminal"}

	d.spawnRestoredPane(pane)

	if pane.CWD != "" {
		t.Errorf("CWD = %q, want it blanked so defaultCWD applies", pane.CWD)
	}
	if pane.SpawnError != "" {
		t.Errorf("SpawnError = %q — an ordinary stale CWD is not an error state", pane.SpawnError)
	}
}

// A worktree-owned pane whose directory is STILL THERE restores normally.
func TestSpawnRestoredPane_WorktreePresentRestoresNormally(t *testing.T) {
	d := newTestDaemon(t)
	dir := t.TempDir()
	pane := &Pane{ID: "pane-0000000c", CWD: dir, WorktreeOwned: true, Type: "terminal"}

	d.spawnRestoredPane(pane)

	if pane.SpawnError != "" {
		t.Errorf("SpawnError = %q for a worktree that is present", pane.SpawnError)
	}
	if pane.CWD != dir {
		t.Errorf("CWD = %q, want %q", pane.CWD, dir)
	}
}

// A retry (Alt+R, or a later restore once the worktree is back) must CLEAR a
// previous failure, or a pane that recovers keeps its complaint forever.
func TestSpawnRestoredPane_SuccessClearsAStaleSpawnError(t *testing.T) {
	d := newTestDaemon(t)
	dir := t.TempDir()
	pane := &Pane{
		ID: "pane-0000000d", CWD: dir, WorktreeOwned: true, Type: "terminal",
		SpawnError: "worktree is gone: somewhere",
	}

	d.spawnRestoredPane(pane)

	if pane.SpawnError != "" {
		t.Errorf("SpawnError = %q after a successful respawn, want it cleared", pane.SpawnError)
	}
}

// Ownership must survive the snapshot ROUND TRIP. It is the only thing that
// lets restore tell the two cases apart — the snapshot stores just CWD without
// it, so a missing worktree and a stale browsed path are indistinguishable.
//
// Driven through disk rather than by inspecting the built state: a key the
// writer emits and the reader ignores is the same bug one step later, and only
// the round trip catches that.
func TestSnapshot_WorktreeOwnershipSurvivesTheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	d := newTestDaemonInDir(t, dir)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.WorktreeOwned = true
	d.snapshot()

	d2 := newTestDaemonInDir(t, dir)
	if err := d2.restoreWorkspace(); err != nil {
		t.Fatalf("restoreWorkspace: %v", err)
	}

	got := d2.session.Pane(pane.ID)
	if got == nil {
		t.Fatal("pane did not survive restore")
	}
	if !got.WorktreeOwned {
		t.Error("WorktreeOwned did not survive the snapshot round trip")
	}
}

// The ERROR is NOT persisted: a fresh daemon re-stats and re-derives it, while
// a stored one resurrects a complaint about a worktree the user has since
// restored.
func TestSnapshot_DoesNotPersistSpawnError(t *testing.T) {
	dir := t.TempDir()
	d := newTestDaemonInDir(t, dir)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.SpawnError = "worktree is gone: /wt/feat-x"
	d.snapshot()

	raw, err := os.ReadFile(config.WorkspacePath())
	if err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if strings.Contains(string(raw), "worktree is gone") {
		t.Error("SpawnError was persisted — a restored worktree would carry a stale complaint")
	}
}
