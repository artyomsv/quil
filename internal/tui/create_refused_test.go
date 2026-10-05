package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

var enterKey = tea.KeyPressMsg{Code: tea.KeyEnter}

// submitOrdinaryCreate submits the create dialog's placement step for a
// terminal pane with a toggle the daemon does not know (plugin registry
// drift), cursor choosing the placement (0 split, 2 replace), and returns the
// create_pane it sent.
func submitOrdinaryCreate(t *testing.T, m Model, conn *fakeConn, cursor int) (Model, *ipc.Message) {
	t.Helper()
	m.dialog, m.createPaneStep, m.dialogCursor = dialogCreatePane, 3, cursor
	m.createPaneDest = roDest
	m.selectedPlugin = "terminal"
	m.selectedToggles = []string{"nope"}
	m = roUpdate(t, m, enterKey)
	sent := lastSent(t, conn, ipc.MsgCreatePane)
	if sent.ID == "" {
		t.Fatal("the ordinary create was sent without a request id — its refusal could not name it")
	}
	return m, sent
}

// refusalArrives delivers the daemon's error reply to req through the listen
// loop, as from dest, and then through Update.
func refusalArrives(t *testing.T, m Model, req *ipc.Message, dest, reason string) Model {
	t.Helper()
	src := newFakeConn()
	reply, err := ipc.NewMessage(ipc.MsgError, ipc.ErrorPayload{Code: ipc.ErrCodeBadPayload, Message: reason, Type: req.Type})
	if err != nil {
		t.Fatal(err)
	}
	reply.ID, reply.Origin = req.ID, dest
	src.recv <- reply
	listener := m
	listener.client = src
	arrived := listener.listenForMessages()()
	if _, ok := arrived.(createPaneRefusedMsg); !ok {
		t.Fatalf("the refusal arrived as %T", arrived)
	}
	return roUpdate(t, m, arrived)
}

// A refused split used to leave its placeholder on screen until some later
// broadcast pruned it, with nothing said. The refusal now unwinds it and
// flashes the daemon's reason, sanitized: a remote daemon chooses that text.
func TestCreateRefused_SplitPlaceholderUnwound(t *testing.T) {
	m, conn := rawArgsModel(t, ipc.RightsFull)
	m, sent := submitOrdinaryCreate(t, m, conn, 0)
	tab := m.projects[0].tabs[0]
	if countPlaceholders(tab.Root) != 1 || m.pendingSplit[tab.ID] == nil {
		t.Fatal("setup: the split armed no placeholder")
	}

	esc := string(rune(0x1b))
	m = refusalArrives(t, m, sent, roDest, `unknown toggle "nope" for plugin terminal`+esc+"[2J")

	tab = m.projects[0].tabs[0]
	if n := countPlaceholders(tab.Root); n != 0 {
		t.Errorf("%d placeholders survived the refusal", n)
	}
	if tab.Root == nil || tab.Root.Pane == nil || tab.Root.Pane.ID != "pane-1" {
		t.Error("the pane that was split is not the tab's only pane again")
	}
	if m.pendingSplit[tab.ID] != nil || m.createReqIDs[tab.ID] != "" || tab.reserveSibling != "" {
		t.Error("the refused create is still armed")
	}
	if !strings.Contains(m.flashText, "unknown toggle") {
		t.Errorf("flash = %q, want the daemon's reason", m.flashText)
	}
	if strings.Contains(m.flashText, esc) {
		t.Errorf("flash = %q carries the daemon's escape sequence", m.flashText)
	}
}

// A refused REPLACE used to cost the pane it replaced: disposed at send time,
// gone until the next broadcast rebuilt it blank — although the daemon refuses
// before touching it. The refusal now puts the same model back, alive.
func TestCreateRefused_ReplacedPaneRestored(t *testing.T) {
	m, conn := rawArgsModel(t, ipc.RightsFull)
	old := m.projects[0].tabs[0].Root.Pane
	m, sent := submitOrdinaryCreate(t, m, conn, 2)
	tab := m.projects[0].tabs[0]
	if tab.Root.Pane != nil || m.replaceHeld[tab.ID] != old {
		t.Fatal("setup: the replace did not detach and hold its pane")
	}

	m = refusalArrives(t, m, sent, roDest, `unknown toggle "nope" for plugin terminal`)

	tab = m.projects[0].tabs[0]
	if tab.Root.Pane != old || old.vt == nil {
		t.Error("the replaced pane was not put back alive")
	}
	if m.replaceHeld[tab.ID] != nil || m.pendingSplit[tab.ID] != nil || tab.reserveReplace {
		t.Error("the refused replace is still armed")
	}
	if !strings.Contains(m.flashText, "unknown toggle") {
		t.Errorf("flash = %q, want the daemon's reason", m.flashText)
	}
}

// A refusal from another daemon naming this client's id unwinds nothing: the
// placeholder belongs to the create the tab's own daemon has not answered.
func TestCreateRefused_ForeignDaemonUnwindsNothing(t *testing.T) {
	m, conn := rawArgsModel(t, ipc.RightsFull)
	m, sent := submitOrdinaryCreate(t, m, conn, 0)

	m = refusalArrives(t, m, sent, "gpu01", "x")

	tab := m.projects[0].tabs[0]
	if countPlaceholders(tab.Root) != 1 || m.pendingSplit[tab.ID] == nil || m.createReqIDs[tab.ID] != sent.ID {
		t.Error("another daemon's refusal unwound this daemon's create")
	}
}

// A new tab arms no placeholder, but its refused first pane was just as
// silent: the create_tab now carries an id, and the refusal is flashed.
func TestCreateRefused_NewTabFlashes(t *testing.T) {
	m, conn := rawArgsModel(t, ipc.RightsFull)
	m.createPaneTarget = paneTargetNewTab
	m.dialog, m.createPaneStep = dialogCreatePane, 3
	m.createPaneDest = roDest
	m.selectedPlugin = "terminal"
	m.selectedToggles = []string{"nope"}
	m = roUpdate(t, m, enterKey)
	sent := lastSent(t, conn, ipc.MsgCreateTab)
	if sent.ID == "" {
		t.Fatal("the create_tab was sent without a request id")
	}

	m = refusalArrives(t, m, sent, roDest, `unknown toggle "nope" for plugin terminal`)

	if !strings.Contains(m.flashText, "unknown toggle") {
		t.Errorf("flash = %q, want the daemon's reason", m.flashText)
	}
}

// Before the first broadcast there is no project, so the dialog pins no
// destination — and both raw-arguments gates read that as "" (local, full):
// a standard --connect token was offered the instance form and its submit.
// They read the destination this client started against instead.
func TestNoRawArgs_InstanceRefusedBeforeTheFirstBroadcast(t *testing.T) {
	for _, rights := range []string{ipc.RightsStandard, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			full := rights == ipc.RightsFull
			m, conn := rawArgsModel(t, rights)
			m.projects = nil
			m.SetHomeDest(roDest)

			cats := m.createPaneCategories()
			ci, pi := -1, -1
			for i, c := range cats {
				if c.key == "remote" {
					ci = i
					for j, p := range c.plugins {
						if p.Name == "remotex" {
							pi = j
						}
					}
				}
			}
			if ci < 0 || pi < 0 {
				t.Fatal("setup: no remotex plugin")
			}
			m.dialog, m.createPaneStep, m.dialogCursor = dialogCreatePane, 0, ci
			m.createPaneDest = ""
			m = roUpdate(t, m, enterKey)
			m.dialogCursor = pi
			m = roUpdate(t, m, enterKey)
			if onForm := m.dialog == dialogInstanceForm; onForm != full {
				t.Fatalf("instance form opened = %v on rights %q (dialog %v)", onForm, rights, m.dialog)
			}
			if refused := m.flashText == noRawArgsFlash; refused == full {
				t.Fatalf("plugin pick: raw-arguments flash = %v on rights %q", refused, rights)
			}

			m.flashText = ""
			m.dialog, m.createPaneStep, m.dialogCursor = dialogCreatePane, 3, 0
			m.selectedPlugin, m.selectedInstanceName, m.selectedInstanceArgs = "remotex", "box", []string{"u@h"}
			m = roUpdate(t, m, enterKey)
			if refused := m.flashText == noRawArgsFlash; refused == full {
				t.Fatalf("submit: raw-arguments flash = %v on rights %q (flash %q)", refused, rights, m.flashText)
			}
			if !full && sentType(conn, ipc.MsgCreatePane) {
				t.Fatal("a refused instance was sent")
			}
		})
	}
}

// Two daemons can each have a branch of one name. One daemon's answer for its
// own branch must neither report nor consume the other's new-tab worktree
// create.
func TestNewTabWorktree_KeyedByDestination(t *testing.T) {
	m := newBranchModel(t)
	t.Setenv("QUIL_HOME", t.TempDir())
	m.client = &fakeSender{}
	m.createPaneTarget = paneTargetNewTab
	m.createPaneDest = "hostA"
	m.selectedPlugin = "terminal"
	m.selectedCWD = "/repo"
	m.worktreeNewBranch = "feat/x"
	out, cmd := m.handleCreatePaneSplit()
	runCmd(cmd)
	m = out.(Model)
	keyA := newTabWorktreeKey("hostA", "feat/x")
	if !m.newTabWorktrees[keyA] {
		t.Fatalf("setup: the new-tab worktree create was not armed: %v", m.newTabWorktrees)
	}

	fail := func(dest string) createPaneRespMsg {
		return createPaneRespMsg{Dest: dest, Resp: ipc.CreatePaneRespPayload{
			TabID: "tab-minted", Error: "fatal: boom",
			Worktree: &ipc.WorktreeSpec{RepoRoot: "/repo", Branch: "feat/x"},
		}}
	}
	updated, _ := m.Update(fail("hostB"))
	m = updated.(Model)
	if m.flashText != "" || !m.newTabWorktrees[keyA] {
		t.Fatalf("hostB's answer for its own feat/x reported (%q) or consumed this create", m.flashText)
	}
	updated, _ = m.Update(fail("hostA"))
	m = updated.(Model)
	if !strings.Contains(m.flashText, "boom") || m.newTabWorktrees[keyA] {
		t.Fatalf("hostA's answer: flash %q, still armed %v", m.flashText, m.newTabWorktrees[keyA])
	}
}
