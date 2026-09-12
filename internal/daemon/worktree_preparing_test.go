package daemon

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// preparingBranch reads the field under the lock that protects it, like paneType
// and paneCWD beside it.
func preparingBranch(p *Pane) string {
	p.PluginMu.Lock()
	defer p.PluginMu.Unlock()
	return p.PreparingWorktree
}

func paneSpawnError(p *Pane) string {
	p.PluginMu.Lock()
	defer p.PluginMu.Unlock()
	return p.SpawnError
}

// The placeholder has NO child process, and that is the point.
//
// It used to be a live shell in the repository root, which is indistinguishable
// from a finished create that put the user in the wrong tree — so a checkout
// that takes minutes on a large monorepo looked exactly like success, and the
// only thing that said otherwise was a three-second status-bar flash if it
// failed. A pane with nothing running in it cannot be mistaken for one that is
// ready.
func TestHandleCreateTab_WorktreePlaceholderHasNoChildProcess(t *testing.T) {
	d, sock := overlayServerDaemonWithConfig(t, config.Default())
	release := make(chan struct{})
	done := joinableAdd(t, func(context.Context, string, string, string) error {
		<-release // hold the checkout open so the placeholder is observable
		return errors.New("released")
	})
	defer func() {
		close(release)
		joinAdd(t, done)
	}()

	client := attachTestClient(t, sock)
	defer client.Close()
	sendCreateTab(t, client, &ipc.FirstPaneSpec{
		Type:     "terminal-wide",
		Worktree: &ipc.WorktreeSpec{RepoRoot: t.TempDir(), Branch: "feat/x"},
	})

	waitUntil(t, "the placeholder to be published", func() bool {
		panes := d.session.Panes(d.session.ActiveTabID())
		return len(panes) == 1 && preparingBranch(panes[0]) != ""
	})

	pane := newTabPane(t, d)
	if got := preparingBranch(pane); got != "feat/x" {
		t.Errorf("PreparingWorktree = %q, want the branch being checked out", got)
	}
	pane.PluginMu.Lock()
	pty := pane.PTY
	pane.PluginMu.Unlock()
	if pty != nil {
		t.Error("the placeholder spawned a child — a live shell in the repository root reads as a finished create")
	}
}

// A failed add leaves the REASON on the placeholder, where the user is looking.
//
// The client's flash lasts three seconds in the right half of the status bar and
// is the only notice this path had; the tab meanwhile held a working shell, so
// "the agent I asked for never came" had no explanation anywhere but quild.log.
func TestHandleCreateTab_WorktreeFailureLeavesTheReasonOnThePane(t *testing.T) {
	d, sock := overlayServerDaemonWithConfig(t, config.Default())
	done := joinableAdd(t, func(context.Context, string, string, string) error {
		return errors.New("fatal: a branch named 'feat/x' already exists")
	})
	defer func() { joinAdd(t, done) }()

	client := attachTestClient(t, sock)
	defer client.Close()
	sendCreateTab(t, client, &ipc.FirstPaneSpec{
		Type:     "terminal-wide",
		Worktree: &ipc.WorktreeSpec{RepoRoot: t.TempDir(), Branch: "feat/x"},
	})

	waitUntil(t, "the failure to land on the pane", func() bool {
		panes := d.session.Panes(d.session.ActiveTabID())
		return len(panes) == 1 && paneSpawnError(panes[0]) != ""
	})

	pane := newTabPane(t, d)
	if got := paneSpawnError(pane); !strings.Contains(got, "already exists") {
		t.Errorf("SpawnError = %q, want git's own reason", got)
	}
	// Cleared with the failure, or the pane renders a spinner for a checkout
	// that is over — the confidently-wrong answer in the other direction.
	if got := preparingBranch(pane); got != "" {
		t.Errorf("PreparingWorktree = %q after the add failed, want it cleared", got)
	}
}

// Runtime-only, like SpawnError beside it. A snapshot landing inside the
// checkout window would otherwise persist "preparing", and the restored pane
// would spin forever for an add no daemon is running.
func TestSnapshot_DoesNotPersistPreparingWorktree(t *testing.T) {
	dir := t.TempDir()
	d := newTestDaemonInDir(t, dir)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.PreparingWorktree = "feat/never-finishes"
	d.snapshot()

	raw, err := os.ReadFile(config.WorkspacePath())
	if err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if strings.Contains(string(raw), "feat/never-finishes") {
		t.Error("PreparingWorktree was persisted — a restored pane would wait for an add nobody is running")
	}
}

// It DOES reach the client, or the pane it describes renders as an ordinary
// blank terminal.
func TestWorkspaceState_BroadcastsPreparingWorktree(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.PreparingWorktree = "feat/x"

	state := d.workspaceStateFromSnapshot(tab.ID, []*Tab{tab},
		map[string][]*Pane{tab.ID: {pane}}, nil, "", true)
	panes, _ := state["panes"].([]map[string]any)
	for _, p := range panes {
		if p["id"] != pane.ID {
			continue
		}
		if p["preparing_worktree"] != "feat/x" {
			t.Errorf("preparing_worktree = %v, want feat/x", p["preparing_worktree"])
		}
		return
	}
	t.Fatal("the pane was not in the broadcast state")
}
