package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// worktreeFieldCursor returns the setup-dialog field index of the worktree
// field, so these tests survive a field being inserted ahead of it.
func worktreeFieldCursor(t *testing.T, m Model, p *plugin.PanePlugin) int {
	t.Helper()
	for i := 0; i < m.setupFieldCount(p); i++ {
		if kind, _ := m.setupFieldKind(p, i); kind == "worktree" {
			return i
		}
	}
	t.Fatal("the setup dialog has no worktree field")
	return -1
}

// namingViaKeys drives the REAL key path into naming mode with a typed branch:
// focus the worktree field, Enter on the trailing "+ new branch…" row, then
// type. Every key goes through handleCreatePaneSetupKey, never through
// handleSetupWorktreeKey directly — the whole class of bug here is that the
// dispatcher intercepts keys before the field handler sees them, which a test
// calling the field handler cannot observe.
func namingViaKeys(t *testing.T, branch string) (Model, *plugin.PanePlugin) {
	t.Helper()
	m := newBranchModel(t)
	m.client = &fakeSender{}
	// Loaded through the real TOML loader rather than hand-built, so the
	// worktree field appears for the same reason it does in production —
	// prompts_cwd — and not because a struct literal set a flag.
	dir := t.TempDir()
	const def = `
[plugin]
name = "wt-probe"
display_name = "WT Probe"
category = "tools"
schema_version = 2

[command]
cmd = "sh"
prompts_cwd = true
`
	if err := os.WriteFile(filepath.Join(dir, "wt-probe.toml"), []byte(def), 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}
	if err := m.pluginRegistry.LoadFromDir(dir); err != nil {
		t.Fatalf("load plugin: %v", err)
	}
	m.selectedPlugin = "wt-probe"
	p := m.pluginRegistry.Get(m.selectedPlugin)
	if p == nil {
		t.Fatal("the probe plugin is not in the registry")
	}
	if !p.Command.PromptsCWD {
		t.Fatal("the probe plugin lost prompts_cwd — the worktree field would not exist")
	}
	m.dialog = dialogCreatePaneSetup
	m.setupFieldCursor = worktreeFieldCursor(t, m, p)

	m.worktreeCursor = worktreeRowIndex(t, m, worktreeNewRowPath)

	updated, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: 'x', Text: "enter"})
	m = updated.(Model)
	if !m.worktreeNaming {
		t.Fatal("Enter on the new-branch row did not open the name field")
	}
	for _, r := range branch {
		updated, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated.(Model)
	}
	if m.worktreeNewBranch != branch {
		t.Fatalf("typing produced %q, want %q", m.worktreeNewBranch, branch)
	}
	return m, p
}

// Tab is handled by handleCreatePaneSetupKey BEFORE the field dispatch, so it
// never reaches handleWorktreeNameKey — the name field cannot swallow it. This
// pins the precondition for the bug below; if a future change routes Tab
// through the field handler, this test says so rather than the next one going
// quietly vacuous.
func TestWorktreeNaming_TabLeavesTheFieldWithoutAccepting(t *testing.T) {
	m, _ := namingViaKeys(t, "feat/x")

	updated, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Text: "tab"})
	got := updated.(Model)

	if !got.worktreeNaming {
		t.Error("Tab closed the name field — it is expected to bypass the field handler entirely")
	}
	if got.worktreeNewBranch != "feat/x" {
		t.Errorf("worktreeNewBranch = %q after Tab, want it still held", got.worktreeNewBranch)
	}
}

// The reported failure: type a branch, Tab away, press Continue. The dialog
// still SHOWS "new branch feat/x" (the summary renders whenever the name is
// non-empty), and submitting must honour what is on screen.
//
// Discarding it here is what made a worktree pane spawn in the repository root
// with no worktree, no git invocation and no error — the exact silent
// relocation the feature exists to prevent.
func TestSubmitSetup_KeepsABranchTypedBeforeTabbingAway(t *testing.T) {
	m, p := namingViaKeys(t, "feat/x")

	updated, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Text: "tab"})
	m = updated.(Model)

	updated, _ = m.submitSetupDialog(p)
	got := updated.(Model)

	if got.worktreeNewBranch != "feat/x" {
		t.Errorf("worktreeNewBranch = %q after Continue, want the typed branch kept", got.worktreeNewBranch)
	}
}

// handleWorktreeNameKey documents Esc as "abandons the new-branch row entirely
// rather than merely closing the input" — but handleCreatePaneSetupKey takes
// Esc unconditionally, ahead of the field dispatch, so that branch is
// unreachable from the keyboard and Esc backs out of the WHOLE setup dialog
// instead. The existing coverage passes only because it calls the field handler
// directly, which is the one caller production never uses.
//
// It matters more now that tabbing away commits the name: backspacing to empty
// would otherwise be the only way to undo one.
func TestWorktreeNaming_EscAbandonsTheRowNotTheDialog(t *testing.T) {
	m, _ := namingViaKeys(t, "feat/x")

	updated, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Text: "esc"})
	got := updated.(Model)

	if got.dialog != dialogCreatePaneSetup {
		t.Errorf("Esc left the setup dialog (now %v) — it should abandon the name field only", got.dialog)
	}
	if got.worktreeNaming {
		t.Error("Esc left the name field open")
	}
	if got.worktreeNewBranch != "" {
		t.Errorf("worktreeNewBranch = %q after Esc, want it abandoned", got.worktreeNewBranch)
	}
}

// The wire proof. A create whose branch was dropped is indistinguishable on
// screen from an ordinary create, so the assertion has to be the payload: no
// spec means the daemon runs its ordinary path and spawns in the browsed
// directory, which for this fixture is inside the repository.
func TestCreatePane_SendsTheSpecForABranchTypedBeforeTabbingAway(t *testing.T) {
	m, p := namingViaKeys(t, "feat/x")

	updated, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Text: "tab"})
	m = updated.(Model)
	updated, _ = m.submitSetupDialog(p)
	m = updated.(Model)

	m.selectedCWD = "/repo/internal/tui"
	m.dialogCursor = 0 // split horizontally
	sender := &fakeSender{}
	m.client = sender

	updated, cmd := m.handleCreatePaneSplit()
	_ = updated.(Model)
	runCmd(cmd)

	var payload *ipc.CreatePanePayload
	for _, msg := range sender.sent {
		if msg.Type != ipc.MsgCreatePane {
			continue
		}
		var decoded ipc.CreatePanePayload
		if err := msg.DecodePayload(&decoded); err != nil {
			t.Fatalf("decode create_pane: %v", err)
		}
		payload = &decoded
	}
	if payload == nil {
		t.Fatal("no create_pane was sent")
	}
	if payload.Worktree == nil {
		t.Fatal("create_pane carried no worktree spec — the pane would spawn in the repository root")
	}
	if payload.Worktree.Branch != "feat/x" {
		t.Errorf("spec branch = %q, want %q", payload.Worktree.Branch, "feat/x")
	}
	if payload.Worktree.RepoRoot != "/repo" {
		t.Errorf("spec repo root = %q, want the main checkout %q", payload.Worktree.RepoRoot, "/repo")
	}
}
