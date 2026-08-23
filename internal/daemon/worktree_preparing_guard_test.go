package daemon

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// Alt+R against a placeholder must do NOTHING, and the consequences of it not
// doing nothing are why this is a guard rather than a nicety.
//
// A placeholder has no child and is about to be replaced by the pane the
// checkout is creating. Restarting it spawns a live shell into the same pane
// object while that goroutine is still running, and nothing clears
// PreparingWorktree — so the shell renders hidden behind the "creating
// worktree" block. Whichever outcome lands next then clobbers it: success
// destroys the pane in replacePaneAt, discarding the shell the user just
// started, and failure writes SpawnError over a pane that now holds a live PTY
// child nobody will ever close.
func TestHandleRestartPaneReq_RefusesAPaneStillCreatingItsWorktree(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.Type = "terminal"
	pane.PreparingWorktree = "feat/x"

	msg, err := ipc.NewMessage(ipc.MsgRestartPaneReq, ipc.RestartPaneReqPayload{PaneID: pane.ID})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleRestartPaneReq(nil, msg)

	pane.PluginMu.Lock()
	pty, preparing := pane.PTY, pane.PreparingWorktree
	pane.PluginMu.Unlock()
	if pty != nil {
		t.Error("restart spawned a child into a placeholder — it would render behind the preparing block and be destroyed by the swap")
	}
	if preparing != "feat/x" {
		t.Errorf("PreparingWorktree = %q, want the refusal to leave it alone", preparing)
	}
}

// The pane a FAILED add leaves behind must still restart: Alt+R is exactly what
// its error screen offers. The guard therefore keys on PreparingWorktree, never
// on SpawnError.
func TestHandleRestartPaneReq_StillRestartsAFailedWorktreePane(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.Type = "terminal"
	pane.SpawnError = "worktree not created: fatal: a branch named 'feat/x' already exists"

	msg, err := ipc.NewMessage(ipc.MsgRestartPaneReq, ipc.RestartPaneReqPayload{PaneID: pane.ID})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleRestartPaneReq(nil, msg)

	pane.PluginMu.Lock()
	pty := pane.PTY
	pane.PluginMu.Unlock()
	if pty == nil {
		t.Error("a failed worktree pane did not restart — Alt+R is what its error screen advertises")
	}
}

// The branch reaches Pane.PreparingWorktree and a full workspace_state
// broadcast BEFORE worktreeAddAndCreate's own validation runs, because that
// validation lives inside the goroutine createFirstPaneWorktree spawns.
//
// worktree_add.go states the invariant this breaks: "Validated BEFORE any
// repository write, so a bad name costs no git invocation, no permit and no
// slot — and never reaches argv." A placeholder is not a repository write, but
// it IS a broadcast to every attached client, and any IPC client can send
// create_tab.
func TestHandleCreateTab_RefusesAnInvalidBranchBeforeBroadcastingIt(t *testing.T) {
	d := newTestDaemon(t)
	msg, err := ipc.NewMessage(ipc.MsgCreateTab, ipc.CreateTabPayload{
		Name: "t",
		FirstPane: &ipc.FirstPaneSpec{
			Type:     "terminal-wide",
			Worktree: &ipc.WorktreeSpec{RepoRoot: t.TempDir(), Branch: strings.Repeat("x", 4096)},
		},
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleCreateTab(nil, msg)

	for _, p := range d.session.Panes(d.session.ActiveTabID()) {
		p.PluginMu.Lock()
		got := p.PreparingWorktree
		p.PluginMu.Unlock()
		if got != "" {
			t.Errorf("PreparingWorktree = %d bytes of unvalidated branch name — it was stored and broadcast before ValidateBranch ran", len(got))
		}
	}
}

// A tab whose worktree was refused still gets a usable pane: the tab must never
// be pane-less, and the requester learns why through the create_pane_resp the
// worktree path would have sent.
//
// And that pane is a PLAIN TERMINAL carrying NONE of the requested plugin's
// fields. Dropping the spec to recover the tab must not promote the request:
// firstPaneType only downgrades while Worktree is non-nil, so computing the type
// after the drop hands back the REQUESTED type — and firstPanePayload then
// attaches InstanceArgs and ResumeSessionID, and the pane spawns in the
// repository ROOT. That is an agent running with its own arguments in the main
// checkout, which is precisely the isolation failure the whole path exists to
// prevent, reached by nothing more than a branch name git would refuse. Any IPC
// client can send create_tab.
func TestHandleCreateTab_AnInvalidBranchStillLeavesAHarmlessPane(t *testing.T) {
	d := newTestDaemon(t)
	msg, err := ipc.NewMessage(ipc.MsgCreateTab, ipc.CreateTabPayload{
		Name: "t",
		FirstPane: &ipc.FirstPaneSpec{
			Type:            "terminal-wide",
			InstanceName:    "inst",
			InstanceArgs:    []string{"--dangerously-skip-permissions"},
			ResumeSessionID: "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
			Worktree:        &ipc.WorktreeSpec{RepoRoot: t.TempDir(), Branch: "-flag-shaped"},
		},
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleCreateTab(nil, msg)

	panes := d.session.Panes(d.session.ActiveTabID())
	if len(panes) != 1 {
		t.Fatalf("new tab has %d panes after a refused worktree, want exactly 1", len(panes))
	}
	pane := panes[0]
	pane.PluginMu.Lock()
	typ, args, name := pane.Type, pane.InstanceArgs, pane.InstanceName
	resume := pane.PluginState["resume_session_id"]
	pane.PluginMu.Unlock()

	if typ != "terminal" {
		t.Errorf("refused worktree spawned type %q in the repository root, want a plain terminal", typ)
	}
	if len(args) != 0 {
		t.Errorf("refused worktree carried InstanceArgs %v — these replace the shell's own args", args)
	}
	if name != "" {
		t.Errorf("refused worktree carried InstanceName %q", name)
	}
	if resume != "" {
		t.Errorf("refused worktree carried resume_session_id %q", resume)
	}
}
