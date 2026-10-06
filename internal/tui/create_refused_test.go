package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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

// The refusal was set as the flash but never DRAWN: with the daemon's real
// reason the right side no longer fit the bar, and the fit dropped all of it,
// flash included. The user saw no message at all, twice (manual test, PR #256).
// The dialog's own session scan answers around the refusal, as it did then.
func TestCreateRefused_LongReasonIsVisibleInTheStatusBar(t *testing.T) {
	m, conn := rawArgsModel(t, ipc.RightsFull)
	m, sent := submitOrdinaryCreate(t, m, conn, 0)
	m = roUpdate(t, m, sessionScanTimeoutMsg{cwd: "/repo"})
	m = refusalArrives(t, m, sent, roDest, `unknown toggle "driftx" for plugin claude-code (see list_plugins)`)
	m = roUpdate(t, m, sessionScanTimeoutMsg{cwd: "/repo"})
	// This binary's flashDuration is 10 ms (main_test.go); what is asserted
	// here is the layout, not the expiry.
	m.flashUntil = time.Now().Add(time.Minute)

	for _, width := range []int{172, 100, 60} {
		m.width = width
		bar := m.renderStatusBar()
		if !strings.Contains(bar, "pane not created") {
			t.Errorf("width %d: status bar %q does not show the refusal", width, bar)
		}
		if strings.Contains(bar, "\n") {
			t.Errorf("width %d: status bar wrapped to two rows", width)
		}
	}
	m.width = 172
	if bar := m.renderStatusBar(); !strings.Contains(bar, "driftx") {
		t.Errorf("status bar %q cut the reason at a width it fits in", bar)
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

// mixedSession is a local project (active, full) beside a project on roDest
// whose token is standard — the shape where "" is a real destination.
func mixedSession(t *testing.T) (Model, *fakeConn) {
	t.Helper()
	m, _ := rawArgsModel(t, ipc.RightsStandard)
	local := newFakeConn()
	t.Cleanup(func() { close(local.recv) })
	remote := newFakeConn()
	t.Cleanup(func() { close(remote.recv) })
	m.client = NewRouter(map[string]Client{"": local, roDest: remote})
	m.SetDestRights(roDest, ipc.RightsStandard)
	pane := NewPaneModel("pane-local", testRingBufSize)
	t.Cleanup(pane.Dispose)
	tab := NewTabModel("tab-local", "Local")
	tab.Root = NewLeaf(pane)
	tab.ActivePane = pane.ID
	m.projects = append(m.projects, &ProjectModel{ID: "proj-local", Name: "Local", tabs: []*TabModel{tab}})
	m.activeProject = len(m.projects) - 1
	return m, local
}

// A dialog opened on the LOCAL project pins "" — and "" used to read as "not
// pinned". After the active project moved to a remote, the raw-arguments gate
// read the remote's (standard) rights, and a new-tab worktree create was
// armed under the remote's key while the send went elsewhere, so its failure
// was never reported. The pin now holds for the gate, the key and the send.
func TestCreatePane_LocalPinSurvivesAMoveToARemote(t *testing.T) {
	m, local := mixedSession(t)
	m = roUpdate(t, m, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if m.dialog != dialogCreatePane {
		t.Fatalf("setup: Ctrl+N left dialog = %v", m.dialog)
	}
	if dest, pinned := m.createPanePin(); dest != "" || !pinned {
		t.Fatalf("pin = (%q, %v), want the local daemon pinned", dest, pinned)
	}
	m.activeProject = 0 // MCP set_active_pane moves to the remote project

	cats := m.createPaneCategories()
	for i, c := range cats {
		if c.key == "remote" {
			m.dialogCursor = i
			m = roUpdate(t, m, enterKey)
			for j, p := range c.plugins {
				if p.Name == "remotex" {
					m.dialogCursor = j
				}
			}
		}
	}
	m = roUpdate(t, m, enterKey)
	if m.dialog != dialogInstanceForm || m.flashText == noRawArgsFlash {
		t.Fatalf("the local dialog read the remote's rights: dialog %v, flash %q", m.dialog, m.flashText)
	}

	m.dialog, m.createPaneStep = dialogCreatePane, 3
	m.createPaneTarget = paneTargetNewTab
	m.selectedPlugin, m.selectedInstanceName, m.selectedInstanceArgs = "terminal", "", nil
	m.worktreeNewBranch = "feat/x"
	m.worktrees = worktreeState{loaded: true, repo: true, root: "/repo"}
	m = roUpdate(t, m, enterKey)
	if !sentType(local, ipc.MsgCreateTab) {
		t.Fatal("the create_tab did not go to the local daemon the dialog was opened on")
	}
	if !m.newTabWorktrees[newTabWorktreeKey("", "feat/x")] {
		t.Fatalf("the worktree create was armed under %v, not the local key", m.newTabWorktrees)
	}
	updated, _ := m.Update(createPaneRespMsg{Dest: "", Resp: ipc.CreatePaneRespPayload{
		TabID: "tab-minted", Error: "fatal: boom",
		Worktree: &ipc.WorktreeSpec{RepoRoot: "/repo", Branch: "feat/x"},
	}})
	m = updated.(Model)
	if !strings.Contains(m.flashText, "boom") {
		t.Errorf("the local failure was not reported: flash %q", m.flashText)
	}
}

// A worktree create re-arms the tab's reservation over an ordinary create
// still waiting for its answer. A late refusal of the ORDINARY create must not
// unwind the worktree create's placeholder.
func TestCreateRefused_LateRefusalSparesALaterWorktreeCreate(t *testing.T) {
	m := newBranchModel(t)
	m.client = &fakeSender{}
	m.selectedPlugin, m.selectedCWD, m.worktreeNewBranch, m.dialogCursor = "terminal", "/repo", "", 0
	out, _ := m.handleCreatePaneSplit()
	m = out.(Model)
	tab := m.curTabs()[0]
	ordinaryID := m.createReqIDs[tab.ID]
	if ordinaryID == "" {
		t.Fatal("setup: the ordinary create armed no id")
	}

	m.selectedPlugin, m.selectedCWD, m.worktreeNewBranch, m.dialogCursor = "terminal", "/repo", "feat/x", 0
	m.worktrees = worktreeState{loaded: true, repo: true, root: "/repo"}
	out, _ = m.handleCreatePaneSplit()
	m = out.(Model)
	worktreePH := m.pendingSplit[tab.ID]
	if worktreePH == nil || m.worktreeCreates[tab.ID] != "feat/x" {
		t.Fatal("setup: the worktree create armed no placeholder")
	}
	if m.createReqIDs[tab.ID] != "" {
		t.Error("the ordinary create's id survived the worktree create that re-armed the tab")
	}

	updated, _ := m.Update(createPaneRefusedMsg{dest: tab.Dest, id: ordinaryID, text: "unknown toggle"})
	m = updated.(Model)
	if m.pendingSplit[tab.ID] != worktreePH || !treeContains(tab.Root, worktreePH) || m.worktreeCreates[tab.ID] != "feat/x" {
		t.Error("the ordinary create's late refusal unwound the worktree create")
	}
}

// A pane held by a replace is still live on the daemon; output it produces
// while held is kept, so the pane a refusal puts back has no gap.
func TestCreateRefused_HeldPaneKeepsItsOutput(t *testing.T) {
	m, conn := rawArgsModel(t, ipc.RightsFull)
	old := m.projects[0].tabs[0].Root.Pane
	m, sent := submitOrdinaryCreate(t, m, conn, 2)
	m = roUpdate(t, m, PaneOutputMsg{PaneID: old.ID, Data: []byte("while-held")})
	m = refusalArrives(t, m, sent, roDest, "unknown toggle")
	if m.projects[0].tabs[0].Root.Pane != old {
		t.Fatal("setup: the pane was not put back")
	}
	if !strings.Contains(string(old.rawBuf.Bytes()), "while-held") {
		t.Error("output that arrived while the pane was held was dropped")
	}
}

// A create that reserves nothing must not retire an earlier one. With the
// overlay shown, the active pane is the overlay, which is not in the layout
// tree: the split is refused (SplitAtPane finds no leaf) and a replace finds
// no leaf to reserve. Either way the earlier create keeps its id — and, for
// a replace, its held pane — so its late refusal can still unwind it.
func TestCreateRefused_UnreservedCreateKeepsTheEarlierOne(t *testing.T) {
	for _, tc := range []struct {
		name          string
		first, second int // dialog cursor: 0 split, 2 replace
	}{
		{"split refused", 0, 0},
		{"replace without a leaf", 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newBranchModel(t)
			m.client = &fakeSender{}
			m.selectedPlugin, m.selectedCWD, m.worktreeNewBranch, m.dialogCursor = "terminal", "/repo", "", tc.first
			out, _ := m.handleCreatePaneSplit()
			m = out.(Model)
			tab := m.curTabs()[0]
			firstID, firstPH, held := m.createReqIDs[tab.ID], m.pendingSplit[tab.ID], m.replaceHeld[tab.ID]
			if firstID == "" || firstPH == nil {
				t.Fatal("setup: the first create armed nothing")
			}
			if held != nil {
				t.Cleanup(held.Dispose)
			}

			ov := NewPaneModel("ov-1", testRingBufSize)
			t.Cleanup(ov.Dispose)
			tab.overlayPane, tab.overlayVisible = ov, true
			m.selectedPlugin, m.selectedCWD, m.dialogCursor = "terminal", "/repo", tc.second
			out, _ = m.handleCreatePaneSplit()
			m = out.(Model)

			if m.createReqIDs[tab.ID] != firstID || m.pendingSplit[tab.ID] != firstPH {
				t.Error("a create that reserved nothing retired the earlier create")
			}
			if held != nil && (m.replaceHeld[tab.ID] != held || held.vt == nil) {
				t.Error("a create that reserved nothing disposed the earlier replace's held pane")
			}
		})
	}
}

// parkRevoked drops roDest's link and parks its ladder the way a revoked
// token does: the link is lost, and the re-login is refused for good.
func parkRevoked(t *testing.T, m Model) Model {
	t.Helper()
	m.SetRedialFunc(roDest, func(Client) (Client, error) { return nil, errors.New("unused") })
	m = roUpdate(t, m, linkLostMsg{dest: roDest, err: errLinkLost})
	link := m.linkOf(roDest)
	m = roUpdate(t, m, redialResultMsg{gen: link.gen, dest: roDest,
		err: fmt.Errorf("token refused (wrong, expired or revoked): %w", ErrLinkPermanent)})
	if !m.linkOf(roDest).parked {
		t.Fatal("setup: the refused re-login did not park the link")
	}
	return m
}

// A host whose token was revoked stays on screen, parked. Ctrl+N there opened
// the form, and its create was sent into the dead conn and never answered
// (manual test, PR #256). The opener now refuses and names the host and why.
func TestCreateRefused_ParkedHostRefusesTheDialog(t *testing.T) {
	m, conn := rawArgsModel(t, ipc.RightsFull)
	m = parkRevoked(t, m)
	clearSent(conn)

	m = roUpdate(t, m, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})

	if m.dialog != dialogNone {
		t.Fatalf("Ctrl+N on a parked host opened dialog %v", m.dialog)
	}
	if !strings.Contains(m.flashText, roDest) || !strings.Contains(m.flashText, "token refused") {
		t.Errorf("flash = %q, want the host and the reason", m.flashText)
	}
	if n := countSent(conn, ipc.MsgCreatePane); n != 0 {
		t.Errorf("%d create_pane sent to a parked host", n)
	}
}

// The dialog stays open across a link loss (it may hold the user's input), so
// the submit refuses too — before it arms a placeholder that nothing answers.
func TestCreateRefused_ParkedHostRefusesTheSubmit(t *testing.T) {
	m, conn := rawArgsModel(t, ipc.RightsFull)
	m.dialog, m.createPaneStep, m.dialogCursor = dialogCreatePane, 3, 0
	m.createPaneDest = roDest
	m.selectedPlugin = "terminal"
	m = parkRevoked(t, m)
	clearSent(conn)

	m = roUpdate(t, m, enterKey)

	tab := m.projects[0].tabs[0]
	if n := countSent(conn, ipc.MsgCreatePane); n != 0 {
		t.Errorf("%d create_pane sent to a parked host", n)
	}
	if n := countPlaceholders(tab.Root); n != 0 || m.pendingSplit[tab.ID] != nil || m.createReqIDs[tab.ID] != "" {
		t.Errorf("the refused create armed a reservation (placeholders %d)", n)
	}
	if m.dialog != dialogNone {
		t.Errorf("the dialog stayed open over the flash (dialog %v)", m.dialog)
	}
	if !strings.Contains(m.flashText, "pane not created: "+roDest) {
		t.Errorf("flash = %q, want the refusal naming the host", m.flashText)
	}
}

// A create whose send failed returned nil from its closure, so its split
// placeholder waited for an answer that the daemon never got. The failure now
// comes back as a message and unwinds it like a refusal.
func TestCreateRefused_FailedSendUnwindsThePlaceholder(t *testing.T) {
	m, _ := rawArgsModel(t, ipc.RightsFull)
	r := NewRouter(map[string]Client{roDest: &failingConn{fakeConn: newFakeConn()}}) // project_merge_test.go
	r.SetActiveDest(roDest)
	m.client = r
	m.dialog, m.createPaneStep, m.dialogCursor = dialogCreatePane, 3, 0
	m.createPaneDest = roDest
	m.selectedPlugin = "terminal"

	next, cmd := m.Update(enterKey)
	m = next.(Model)
	tab := m.projects[0].tabs[0]
	if countPlaceholders(tab.Root) != 1 {
		t.Fatal("setup: the split armed no placeholder")
	}
	var failed tea.Msg
	var collect func(tea.Cmd)
	collect = func(c tea.Cmd) {
		if c == nil {
			return
		}
		done := make(chan tea.Msg, 1)
		go func() { done <- c() }()
		var got tea.Msg
		select {
		case got = <-done:
		case <-time.After(time.Second):
			return // a timer or a listener: not the send
		}
		switch msg := got.(type) {
		case tea.BatchMsg:
			for _, sub := range msg {
				collect(sub)
			}
		case createPaneSendFailedMsg:
			failed = msg
		}
	}
	collect(cmd)
	if failed == nil {
		t.Fatal("the failed send reported nothing")
	}
	m = roUpdate(t, m, failed)

	tab = m.projects[0].tabs[0]
	if n := countPlaceholders(tab.Root); n != 0 || m.pendingSplit[tab.ID] != nil {
		t.Errorf("the placeholder survived the failed send (%d)", n)
	}
	if !strings.Contains(m.flashText, "pane not created: cannot reach "+roDest) {
		t.Errorf("flash = %q, want the failed send reported", m.flashText)
	}
}
