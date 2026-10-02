package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// One code path for every transport: the new-tab create carries the names,
// and no instance_args for a non-instance plugin.
func TestNewTab_SubmitSendsToggleNames(t *testing.T) {
	m := newTabModel(t)
	f := m.client.(*fakeSender)
	m.createPaneTarget = paneTargetNewTab
	m.dialog = dialogCreatePane
	m.selectedPlugin = "k9s"
	m.selectedToggles = []string{"readonly"}
	m.selectedKubeContext = "prod"

	out, cmd := m.handleCreatePaneSplit()
	runCmd(cmd)
	got := out.(Model)

	p := decodeCreateTab(t, f)
	if p.FirstPane == nil || !reflect.DeepEqual(p.FirstPane.Toggles, []string{"readonly"}) || p.FirstPane.KubeContext != "prod" {
		t.Fatalf("first pane = %+v", p.FirstPane)
	}
	if len(p.FirstPane.InstanceArgs) != 0 {
		t.Fatalf("instance args sent: %v", p.FirstPane.InstanceArgs)
	}
	if got.selectedToggles != nil || got.selectedKubeContext != "" {
		t.Fatal("the teardown left the named choices for the next Ctrl+N")
	}
}

// The split and the replace forms carry the same names on create_pane.
func TestSplitAndReplace_SubmitSendToggleNames(t *testing.T) {
	for name, cursor := range map[string]int{"split": 0, "replace": 2} {
		t.Run(name, func(t *testing.T) {
			m := newBranchModel(t)
			f := &fakeSender{}
			m.client = f
			m.selectedPlugin = "k9s"
			m.selectedCWD = "/repo"
			m.selectedToggles = []string{"readonly"}
			m.selectedKubeContext = "prod"
			m.dialogCursor = cursor
			oldPaneID := m.curTabs()[0].ActivePaneModel().ID

			out, cmd := m.handleCreatePaneSplit()
			runCmd(cmd)
			got := out.(Model)

			var payload *ipc.CreatePanePayload
			for _, msg := range f.sent {
				if msg.Type != ipc.MsgCreatePane {
					continue
				}
				var decoded ipc.CreatePanePayload
				if err := msg.DecodePayload(&decoded); err != nil {
					t.Fatalf("decode: %v", err)
				}
				payload = &decoded
			}
			if payload == nil {
				t.Fatal("no create_pane was sent")
			}
			if (payload.ReplacePaneID == oldPaneID) != (name == "replace") {
				t.Fatalf("ReplacePaneID = %q (old pane %q) for %s", payload.ReplacePaneID, oldPaneID, name)
			}
			if !reflect.DeepEqual(payload.Toggles, []string{"readonly"}) || payload.KubeContext != "prod" || len(payload.InstanceArgs) != 0 {
				t.Fatalf("payload toggles=%v kube=%q args=%v", payload.Toggles, payload.KubeContext, payload.InstanceArgs)
			}
			if got.selectedToggles != nil || got.selectedKubeContext != "" {
				t.Fatal("the teardown left the named choices for the next Ctrl+N")
			}
		})
	}
}

// refusalFor builds the answer the daemon sends when it refuses a worktree
// create's named selections: the request's tab (none for a new tab) and its
// spec echoed back, with the error.
func refusalFor(tabID string, spec *ipc.WorktreeSpec) createPaneRespMsg {
	return createPaneRespMsg{Resp: ipc.CreatePaneRespPayload{
		TabID: tabID, Error: `unknown toggle "nope" for plugin k9s (see list_plugins)`, Worktree: spec,
	}}
}

// A worktree split whose toggle the daemon refuses is unwound by that answer
// at once, not by the give-up tick 150 s later.
func TestWorktreeSplit_ToggleRefusalUnwinds(t *testing.T) {
	m := newBranchModel(t)
	f := &fakeSender{}
	m.client = f
	m.selectedPlugin = "k9s"
	m.selectedCWD = "/repo"
	m.worktreeNewBranch = "feat/x"
	m.selectedToggles = []string{"nope"}
	m.dialogCursor = 0
	tabID := m.curTabs()[0].ID

	out, cmd := m.handleCreatePaneSplit()
	runCmd(cmd)
	got := out.(Model)
	var sent ipc.CreatePanePayload
	for _, msg := range f.sent {
		if msg.Type == ipc.MsgCreatePane {
			if err := msg.DecodePayload(&sent); err != nil {
				t.Fatal(err)
			}
		}
	}
	if sent.Worktree == nil || sent.TabID != tabID || len(sent.Toggles) != 1 {
		t.Fatalf("sent %+v", sent)
	}

	updated, _ := got.Update(refusalFor(sent.TabID, sent.Worktree))
	after := updated.(Model)
	if _, ok := after.pendingSplit[tabID]; ok || after.worktreeCreates[tabID] != "" {
		t.Error("the refused create is still armed")
	}
	if tab := after.tabByID(tabID); tab != nil && countPlaceholders(tab.Root) != 0 {
		t.Error("a placeholder leaf survived the refusal")
	}
	if !strings.Contains(after.flashText, "unknown toggle") {
		t.Errorf("flash = %q", after.flashText)
	}
}

// The new-tab form has no tab to name; the answer is matched by its branch,
// and that entry is consumed.
func TestNewTabWorktree_ToggleRefusalUnwinds(t *testing.T) {
	m := newBranchModel(t)
	t.Setenv("QUIL_HOME", t.TempDir())
	f := &fakeSender{}
	m.client = f
	m.projects[0].ID = "proj-1"
	m.createPaneTarget = paneTargetNewTab
	m.selectedPlugin = "k9s"
	m.selectedCWD = "/repo"
	m.worktreeNewBranch = "feat/x"
	m.selectedToggles = []string{"nope"}

	out, cmd := m.handleCreatePaneSplit()
	runCmd(cmd)
	got := out.(Model)
	p := decodeCreateTab(t, f)
	if p.FirstPane == nil || p.FirstPane.Worktree == nil || !got.newTabWorktrees["feat/x"] {
		t.Fatalf("first pane = %+v armed=%v", p.FirstPane, got.newTabWorktrees)
	}

	updated, _ := got.Update(refusalFor("", p.FirstPane.Worktree))
	after := updated.(Model)
	if after.newTabWorktrees["feat/x"] {
		t.Error("the branch entry was not consumed")
	}
	if !strings.Contains(after.flashText, "unknown toggle") {
		t.Errorf("flash = %q", after.flashText)
	}
}

// Picking another plugin drops the previous dialog's named choices, so a
// cancelled k9s setup cannot hand its context to the next pane.
func TestPluginPick_ClearsNamedChoices(t *testing.T) {
	m := &Model{pluginRegistry: toolsRegistry(t)} // lazygit available locally
	m.selectedToggles = []string{"readonly"}
	m.selectedKubeContext = "prod"
	m.createPaneStep = 1
	m.selectedCategory = toolsIdxIn(t, m)
	m.dialogCursor = 0 // lazygit

	out, _ := m.handleCreatePaneSelect()
	got := out.(Model)
	if got.selectedPlugin != "lazygit" {
		t.Fatalf("selectedPlugin = %q, want lazygit — the pick never ran", got.selectedPlugin)
	}
	if got.selectedToggles != nil || got.selectedKubeContext != "" {
		t.Fatalf("toggles=%v kube=%q survived a plugin pick", got.selectedToggles, got.selectedKubeContext)
	}
}
