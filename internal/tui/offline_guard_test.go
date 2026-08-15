package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// countingClient is a minimal tuiClient that counts Send calls — enough to
// prove a guarded caller never reaches the wire, without a Router or a real
// socket.
type countingClient struct {
	onSend func()
}

func (c *countingClient) Send(*ipc.Message) error {
	if c.onSend != nil {
		c.onSend()
	}
	return nil
}

func (c *countingClient) Receive() (*ipc.Message, error) {
	return nil, nil
}

func TestProjectActionable(t *testing.T) {
	live := &ProjectModel{ID: "proj-1a2b3c4d"}
	offline := &ProjectModel{ID: "proj-1a2b3c4d", Dest: "gpu01", Offline: &OfflineState{}}
	synthetic := &ProjectModel{ID: interimProjectIDFor("gpu01"), Dest: "gpu01"}

	m := Model{}
	if !m.projectActionable(live) {
		t.Error("a live project is not actionable")
	}
	if m.projectActionable(offline) {
		t.Error("an offline project is actionable; its messages would be dropped silently")
	}
	if m.projectActionable(synthetic) {
		t.Error("a synthetic project is actionable")
	}
	if m.projectActionable(nil) {
		t.Error("nil is actionable")
	}
}

// switchProject must still WORK for an offline project — switching to it is how
// the user reaches the repair panel — but its MsgSwitchProject would be dropped
// by Router.Send and logged as sent.
func TestSwitchProject_OfflineSwitchesWithoutSending(t *testing.T) {
	sent := 0
	m := Model{client: &countingClient{onSend: func() { sent++ }}}
	m.projects = []*ProjectModel{
		{ID: "proj-1a2b3c4d", Name: "local"},
		{ID: "proj-5e6f7a8b", Name: "cluster", Dest: "gpu01", Offline: &OfflineState{}},
	}

	m.switchProject(1)

	if m.activeProject != 1 {
		t.Fatalf("activeProject = %d, want 1 — the switch itself must work", m.activeProject)
	}
	if sent != 0 {
		t.Errorf("sent %d messages to an offline destination, want 0", sent)
	}
}

// TestOpenProjectCtxMenu_OfflineProjectGreysRenameKeepsDisconnect pins the
// sidebar context menu's call site: an offline project must grey Rename the
// same way a synthetic one does, but Disconnect — entirely client-side — must
// stay enabled, since detaching a host that is not coming back is exactly
// what a user reaches for here.
func TestOpenProjectCtxMenu_OfflineProjectGreysRenameKeepsDisconnect(t *testing.T) {
	t.Parallel()
	p := &ProjectModel{ID: "proj-a", Name: "alpha", Dest: "gpu01", Offline: &OfflineState{}}
	m := &Model{width: 200, height: 40, projects: []*ProjectModel{p}}
	m.openProjectCtxMenu(p, 3, 1)

	var renameEnabled, disconnectEnabled, foundDestroy bool
	for _, it := range m.ctxMenu.items {
		switch it.id {
		case ctxActRenameProject:
			renameEnabled = it.enabled
		case ctxActDisconnectHost:
			disconnectEnabled = it.enabled
		case ctxActDestroyProject:
			foundDestroy = true
		}
	}
	if renameEnabled {
		t.Error("Rename must be greyed for an offline project")
	}
	if !disconnectEnabled {
		t.Error("Disconnect must stay enabled for an offline project — it is client-side only")
	}
	if foundDestroy {
		t.Error("a remote project's menu must offer Disconnect, not Destroy")
	}
}

// TestPalette_OfflineProjectDisablesRenameAndNewTab pins the two palette call
// sites: Rename/Destroy grey the same way they do for a synthetic project
// (projectActionable covers both), Disconnect stays enabled on a remote
// project regardless, and New tab greys out rather than accepting a keypress
// whose message the router would silently drop.
func TestPalette_OfflineProjectDisablesRenameAndNewTab(t *testing.T) {
	t.Parallel()
	m := Model{
		cfg:           config.Default(),
		client:        newFakeConn(),
		width:         120,
		height:        40,
		notifications: NewNotificationCenter(40, 120),
		mcpHighlights: make(map[string]bool),
		// A live sibling project keeps onlyOfflineProjects() false, which is
		// the load-bearing distinction from the pre-first-broadcast launch
		// window (see TestHandleKey_NewTab_NoProjectsYetOpensThePicker):
		// New tab must stay disabled while the ACTIVE project is offline and
		// some other, reachable project exists — only the seeded-but-nothing-
		// heard-from-yet state gets the carve-out.
		projects: []*ProjectModel{
			{ID: "proj-a", Name: "cluster", Dest: "gpu01", Offline: &OfflineState{}},
			{ID: "proj-local", Name: "local"},
		},
		activeProject: 0,
	}

	var renameEnabled, removeEnabled, newTabEnabled bool
	var renameFound, removeFound, newTabFound bool
	for _, c := range m.buildPaletteCommands() {
		switch c.action {
		case palActRenameProject:
			renameEnabled, renameFound = c.enabled, true
		case palActRemoveProject:
			removeEnabled, removeFound = c.enabled, true
		case palActNewTab:
			newTabEnabled, newTabFound = c.enabled, true
		}
	}
	if !renameFound || renameEnabled {
		t.Errorf("Rename project: found=%v enabled=%v, want found and disabled", renameFound, renameEnabled)
	}
	if !removeFound || !removeEnabled {
		t.Errorf("Disconnect host: found=%v enabled=%v, want found and ENABLED (client-side only)", removeFound, removeEnabled)
	}
	if !newTabFound || newTabEnabled {
		t.Errorf("New tab: found=%v enabled=%v, want found and disabled while the active project is offline", newTabFound, newTabEnabled)
	}
}

// TestHandleKey_NewTab_OfflineProjectRefusesWithFlash pins the Ctrl+T
// keypress path, which has no `enabled:` field to grey out — a silent no-op
// there reads as a broken keybinding, so it must flash a message naming the
// host instead of calling createTab.
//
// This does NOT assert on Send count: the returned cmd is the flash's expiry
// timer, not createTab's — invoking it produces a flashExpireMsg, never a
// Send call, so a Send-count assertion here would pass whether or not the
// guard actually fires. flashText and cmd being the refusal path (not nil)
// are what actually pin the behavior.
func TestHandleKey_NewTab_OfflineProjectRefusesWithFlash(t *testing.T) {
	t.Parallel()
	m := Model{
		cfg:           config.Default(),
		client:        newFakeConn(),
		width:         100,
		height:        30,
		notifications: NewNotificationCenter(30, 100),
		mcpHighlights: make(map[string]bool),
		// A live sibling project keeps onlyOfflineProjects() false — see the
		// matching comment in TestPalette_OfflineProjectDisablesRenameAndNewTab.
		// Without it this fixture is indistinguishable from the seeded-at-
		// launch, nothing-broadcast-yet window, which is deliberately NOT
		// refused (TestHandleKey_NewTab_NoProjectsYetOpensThePicker's sibling
		// carve-out).
		projects: []*ProjectModel{
			{ID: "proj-a", Name: "cluster", Dest: "gpu01", Offline: &OfflineState{}},
			{ID: "proj-local", Name: "local"},
		},
		activeProject: 0,
	}
	// handleKey resolves keys through the action registry, so a directly-built
	// Model needs the keymap NewModel would have given it. Without this,
	// MatchTier answers "" for every press, the "tab.new" case never runs, and
	// this test fails on a guard that is working.
	m.initKeymap()

	updated, cmd := m.handleKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	got := updated.(Model)

	if got.flashText == "" {
		t.Error("Ctrl+T on an offline project must flash a message, not no-op silently")
	}
	if cmd == nil {
		t.Fatal("a flash needs its expiry command, or the message never clears")
	}
	if _, ok := cmd().(flashExpireMsg); !ok {
		t.Errorf("cmd() = %T, want flashExpireMsg — createTab must not have run", cmd())
	}
}

// TestHandleKey_NewTab_NoProjectsYetOpensThePicker pins the fix for a review
// regression: m.projects is nil from NewModel until the first workspace_state
// broadcast, which is a real window every session passes through — NOT the
// same thing as "unreachable". The create's send resolves correctly in this
// window via Router.Send's sole-conn startup fallback (routeDest's
// currentDest() is "" before any project is known, which is the key the local
// daemon's own connection is registered under). Ctrl+T must not flash a
// refusal here.
//
// What the carve-out gates is now the DIALOG rather than the send — Ctrl+T
// asks which pane the tab should open with — so that is what is asserted. The
// property is unchanged: this window must not be treated as unreachable.
func TestHandleKey_NewTab_NoProjectsYetOpensThePicker(t *testing.T) {
	t.Parallel()
	sent := 0
	m := Model{
		cfg:           config.Default(),
		client:        &countingClient{onSend: func() { sent++ }},
		width:         100,
		height:        30,
		notifications: NewNotificationCenter(30, 100),
		mcpHighlights: make(map[string]bool),
		// projects is deliberately nil — the pre-first-broadcast state.
	}
	m.initKeymap() // see TestHandleKey_NewTab_OfflineProjectRefusesWithFlash

	updated, _ := m.handleKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	got := updated.(Model)

	if got.flashText != "" {
		t.Errorf("flashText = %q, want empty — no project known yet is not the same as unreachable", got.flashText)
	}
	if got.dialog != dialogCreatePane {
		t.Errorf("dialog = %v, want the first-pane picker open", got.dialog)
	}
	if got.createPaneTarget != paneTargetNewTab {
		t.Errorf("target = %v, want paneTargetNewTab", got.createPaneTarget)
	}
	if sent != 0 {
		t.Errorf("sent %d messages, want 0 — nothing is created until the user picks", sent)
	}
}

// TestHandleKey_NewTab_OnlyOfflineProjectsOpensThePicker pins the review fix
// this carve-out exists for: at launch, before the local daemon's first
// broadcast, m.projects can hold ONLY seeded offline stand-ins for remote
// hosts that failed to dial — a state m.cur() cannot tell apart from
// "genuinely offline forever". Ctrl+T there must still open the picker,
// because the send resolves to the LOCAL daemon via Router.Send's sole-conn
// startup fallback — a machine this state says nothing bad about. Without
// onlyOfflineProjects(), this flashed "cannot reach gpu01" about a host that
// had nothing to do with the tab being created.
func TestHandleKey_NewTab_OnlyOfflineProjectsOpensThePicker(t *testing.T) {
	t.Parallel()
	sent := 0
	m := Model{
		cfg:           config.Default(),
		client:        &countingClient{onSend: func() { sent++ }},
		width:         100,
		height:        30,
		notifications: NewNotificationCenter(30, 100),
		mcpHighlights: make(map[string]bool),
		projects: []*ProjectModel{
			{ID: "proj-offline@gpu01", Name: "gpu01", Dest: "gpu01", Offline: &OfflineState{}},
		},
		activeProject: 0,
	}
	m.initKeymap() // see TestHandleKey_NewTab_OfflineProjectRefusesWithFlash

	updated, _ := m.handleKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	got := updated.(Model)

	if got.flashText != "" {
		t.Errorf("flashText = %q, want empty — every known project being an offline stand-in says nothing about the local daemon", got.flashText)
	}
	if got.dialog != dialogCreatePane {
		t.Errorf("dialog = %v, want the first-pane picker open", got.dialog)
	}
	if sent != 0 {
		t.Errorf("sent %d messages, want 0 — nothing is created until the user picks", sent)
	}
}

// TestPalette_OnlyOfflineProjectsKeepsNewTabEnabled is the palette's half of
// TestHandleKey_NewTab_OnlyOfflineProjectsOpensThePicker.
func TestPalette_OnlyOfflineProjectsKeepsNewTabEnabled(t *testing.T) {
	t.Parallel()
	m := Model{
		cfg:           config.Default(),
		client:        newFakeConn(),
		width:         120,
		height:        40,
		notifications: NewNotificationCenter(40, 120),
		mcpHighlights: make(map[string]bool),
		projects: []*ProjectModel{
			{ID: "proj-offline@gpu01", Name: "gpu01", Dest: "gpu01", Offline: &OfflineState{}},
		},
		activeProject: 0,
	}

	var newTabEnabled, newTabFound bool
	for _, c := range m.buildPaletteCommands() {
		if c.action == palActNewTab {
			newTabEnabled, newTabFound = c.enabled, true
		}
	}
	if !newTabFound || !newTabEnabled {
		t.Errorf("New tab: found=%v enabled=%v, want found and ENABLED while every known project is an offline stand-in", newTabFound, newTabEnabled)
	}
}

// TestSendUpdateProject_RefusesOfflineProject pins the rename-dialog submit
// path: an offline project's rename must never reach Router.Send, which would
// drop it silently and leave the dialog appearing to have worked.
//
// "gpu01" is registered as a LIVE conn in the router — not just an unknown
// dest — so this test actually pins the guard rather than the router's
// unrelated unknown-dest drop: with "gpu01" unregistered, Router.Send would
// drop the message with or without the projectActionable guard, and this test
// would pass on unguarded code. With a live "gpu01" conn, removing the guard
// makes the send actually succeed and gpu.sent becomes 1, which is what
// distinguishes "the guard refused it" from "the router had nowhere to send
// it anyway". Verified by temporarily deleting the guard and confirming this
// test fails — see the fix report.
func TestSendUpdateProject_RefusesOfflineProject(t *testing.T) {
	t.Parallel()
	local := newFakeConn()
	gpu := newFakeConn()
	m := Model{
		client: NewRouter(map[string]Client{"": local, "gpu01": gpu}),
		projects: []*ProjectModel{
			{ID: "proj-cluster", Name: "cluster", Dest: "gpu01", Offline: &OfflineState{}},
		},
		dialog: dialogProjectRename,
	}

	m.sendUpdateProject("proj-cluster", "renamed", "/srv/infra", false)

	if len(gpu.sent) != 0 {
		t.Errorf("sent %d messages to gpu01 for a rename of an offline project, want 0", len(gpu.sent))
	}
	if len(local.sent) != 0 {
		t.Errorf("sent %d messages to the wrong dest", len(local.sent))
	}
}

// TestHandleConfirmKey_DestroyProject_OfflineRefusesSend pins the destroy
// confirm dialog: accepting it on an offline project must not build or send
// MsgDestroyProject, since Router.Send would drop it and log it as sent.
func TestHandleConfirmKey_DestroyProject_OfflineRefusesSend(t *testing.T) {
	t.Parallel()
	fake := &fakeSender{}
	m := Model{
		client:      fake,
		dialog:      dialogConfirm,
		confirmKind: confirmKindDestroyProject,
		confirmID:   "proj-a",
		projects: []*ProjectModel{
			{ID: "proj-a", Name: "alpha", Dest: "gpu01", Offline: &OfflineState{}},
		},
	}

	m.handleConfirmKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	if len(fake.sent) != 0 {
		t.Errorf("sent %d messages destroying an offline project, want 0", len(fake.sent))
	}
}
