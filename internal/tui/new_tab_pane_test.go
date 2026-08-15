package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// Ctrl+T used to create a tab outright, which always came up holding a shell.
// It now opens the create-pane dialog first and names the first pane on the
// create message, so the tab arrives with the pane the user actually wanted.

// newTabModel is a model with a real tab and pane (the create-pane dialog reads
// the active pane for its CWD pre-fill) and a fake sender to observe the wire.
// Every test here goes through handleCreatePaneSplit, whose teardown runs
// SaveRecentCWDs(config.RecentCWDsPath(...)) before the new-tab branch. Without
// QUIL_HOME that resolves to the developer's REAL ~/.quil — and `dev.sh test`
// hides it, because Docker gives the run a throwaway /root, so the write is
// invisible in CI and pollutes production everywhere else. This is also why
// these tests cannot take t.Parallel(): t.Setenv forbids it.
func newTabModel(t *testing.T) Model {
	t.Helper()
	t.Setenv("QUIL_HOME", t.TempDir())
	m := *newSplitDragTestModel(t)
	m.width, m.height = 120, 44
	m.pluginRegistry = plugin.NewRegistry()
	m.client = &fakeSender{}
	// appendTab files its tabs under interimProject(), whose ID is the
	// synthetic placeholder projectActionable refuses — so the fixture would
	// take the unreachable branch and every assertion below would be about the
	// refusal rather than the flow.
	m.projects[0].ID = "proj-1"
	return m
}

func sentMsgTypes(f *fakeSender) []string {
	types := make([]string, 0, len(f.sent))
	for _, msg := range f.sent {
		types = append(types, msg.Type)
	}
	return types
}

// decodeCreateTab returns the payload of the sole create_tab message sent.
func decodeCreateTab(t *testing.T, f *fakeSender) ipc.CreateTabPayload {
	t.Helper()
	var found *ipc.Message
	for _, msg := range f.sent {
		if msg.Type == ipc.MsgCreateTab {
			if found != nil {
				t.Fatalf("sent %d create_tab messages, want exactly 1: %v", len(f.sent), sentMsgTypes(f))
			}
			found = msg
		}
	}
	if found == nil {
		t.Fatalf("no create_tab sent; sent %v", sentMsgTypes(f))
	}
	var p ipc.CreateTabPayload
	if err := found.DecodePayload(&p); err != nil {
		t.Fatalf("decode create_tab: %v", err)
	}
	return p
}

// pressEscape drives the real dialog key handler, so the step-0 / step-N split
// is exercised where it actually lives rather than through a decision function
// the call site could stop reaching.
func pressEscape(t *testing.T, m Model) Model {
	t.Helper()
	out, cmd := m.handleCreatePaneKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	runCmd(cmd)
	return out.(Model)
}

func TestNewTab_OpensTheCreatePaneDialogInsteadOfCreatingATab(t *testing.T) {
	m := newTabModel(t)
	f := m.client.(*fakeSender)

	out, _ := m.handleNewTab()
	got := out.(Model)

	if got.dialog != dialogCreatePane {
		t.Errorf("dialog = %v, want the create-pane dialog", got.dialog)
	}
	if got.createPaneTarget != paneTargetNewTab {
		t.Errorf("target = %v, want paneTargetNewTab", got.createPaneTarget)
	}
	if len(f.sent) != 0 {
		t.Errorf("opening the dialog already sent %v — the tab must not exist until the user picks", sentMsgTypes(f))
	}
}

// Esc on the FIRST step closes the dialog and falls back to a terminal tab, so
// Ctrl+T Esc stays the two-keystroke path to the old behavior. The tab is never
// left un-created and never left empty.
func TestNewTab_EscapeAtTheFirstStepCreatesATerminalTab(t *testing.T) {
	m := newTabModel(t)
	f := m.client.(*fakeSender)
	out, _ := m.handleNewTab()

	got := pressEscape(t, out.(Model))

	if got.dialog != dialogNone {
		t.Errorf("dialog = %v, want it closed", got.dialog)
	}
	p := decodeCreateTab(t, f)
	if p.FirstPane != nil {
		t.Errorf("escape sent a first-pane spec %+v, want the bare terminal default", p.FirstPane)
	}
}

// Esc DEEPER in the flow is back-navigation, which it has always been. Making
// every Esc create a tab would remove the only way back a step and make one key
// mean two things depending on how far in you are.
func TestNewTab_EscapeAfterTheFirstStepGoesBackAndCreatesNothing(t *testing.T) {
	m := newTabModel(t)
	f := m.client.(*fakeSender)
	out, _ := m.handleNewTab()
	deeper := out.(Model)
	deeper.createPaneStep = 1

	got := pressEscape(t, deeper)

	if got.createPaneStep != 0 {
		t.Errorf("createPaneStep = %d, want 0 (stepped back)", got.createPaneStep)
	}
	if got.dialog != dialogCreatePane {
		t.Errorf("dialog = %v, want the dialog still open", got.dialog)
	}
	if len(f.sent) != 0 {
		t.Errorf("stepping back sent %v, want nothing", sentMsgTypes(f))
	}
}

// The submit sends ONE create_tab carrying the choices — never a create_pane,
// which names a tab id that does not exist yet.
func TestNewTab_SubmitSendsTheChoicesOnTheCreateTab(t *testing.T) {
	m := newTabModel(t)
	f := m.client.(*fakeSender)
	m.createPaneTarget = paneTargetNewTab
	m.dialog = dialogCreatePane
	m.selectedPlugin = "claude-code"
	m.selectedCWD = "/work/repo"
	m.selectedInstanceName = "inst"
	m.selectedInstanceArgs = []string{"--flag"}

	out, cmd := m.handleCreatePaneSplit()
	runCmd(cmd)
	got := out.(Model)

	p := decodeCreateTab(t, f)
	if p.FirstPane == nil {
		t.Fatal("the create carried no first-pane spec")
	}
	if p.FirstPane.Type != "claude-code" {
		t.Errorf("type = %q, want the chosen plugin", p.FirstPane.Type)
	}
	if p.FirstPane.CWD != "/work/repo" {
		t.Errorf("cwd = %q, want the chosen directory", p.FirstPane.CWD)
	}
	if p.FirstPane.InstanceName != "inst" || len(p.FirstPane.InstanceArgs) != 1 {
		t.Errorf("instance = %q args = %v, want both carried", p.FirstPane.InstanceName, p.FirstPane.InstanceArgs)
	}
	for _, msg := range f.sent {
		if msg.Type == ipc.MsgCreatePane {
			t.Error("a new-tab submit sent create_pane, which names a tab that does not exist yet")
		}
	}
	// Nothing was split, so nothing may be armed to unwind.
	if len(got.pendingSplit) != 0 {
		t.Errorf("a new-tab create armed %d pending splits, want none", len(got.pendingSplit))
	}
}

// The placement step asks where to put the pane RELATIVE to an existing one.
// A new tab has none, so the step is skipped rather than rendered with three
// rows that cannot mean anything.
func TestNewTab_SkipsThePlacementStep(t *testing.T) {
	m := newTabModel(t)
	m.createPaneTarget = paneTargetNewTab

	// createPaneStep must be SET to 3 — the fixture leaves it at 0, so a guard
	// of the form `if step == 3 && ...` never runs and asserts nothing.
	m.createPaneStep = 3
	if got := m.createPaneItemCount(); got != 0 {
		t.Errorf("placement rows = %d in new-tab mode, want none", got)
	}

	// enterSetupOrSplit is the branch a plugin with no setup dialog takes.
	m.createPaneStep = 1
	if cmd := m.enterSetupOrSplit(nil); cmd != nil {
		runCmd(cmd)
	}
	if m.createPaneStep == 3 {
		t.Error("a no-setup plugin landed on the placement step in new-tab mode")
	}
}

// A NEW tab's context is the PROJECT, not whichever pane happens to be focused
// in the tab you pressed Ctrl+T from. The daemon roots a new tab at projectCWD
// for the same reason; this is the client half of that agreement, and without
// it the git-repo candidates are discovered from wherever the last shell had
// cd'd to.
func TestNewTab_DiscoveryBaseIsTheProjectRootNotTheActivePane(t *testing.T) {
	m := newTabModel(t)
	m.cur().RootDir = "/work/project"
	if tab := m.activeTabModel(); tab != nil {
		if pane := tab.ActivePaneModel(); pane != nil {
			pane.CWD = "/somewhere/else"
		}
	}

	m.createPaneTarget = paneTargetNewTab
	if got := m.setupDiscoveryBase(); got != "/work/project" {
		t.Errorf("new-tab discovery base = %q, want the project root", got)
	}

	// A split still follows the pane it is splitting — that pane IS the context.
	m.createPaneTarget = paneTargetSplit
	if got := m.setupDiscoveryBase(); got != "/somewhere/else" {
		t.Errorf("split discovery base = %q, want the active pane's CWD", got)
	}
}

// createPaneTarget must be reset where the dialog OPENS, not on each close
// path: the step-0 escape, the instance-delete detour into the confirm dialog
// and handleCreatePaneSplit's three early refusals all leave without reaching
// its teardown block. A target that outlived one of those would make the next
// plain Ctrl+N create a TAB instead of splitting the current one.
func TestOpenCreatePaneDialog_ResetsAStaleNewTabTarget(t *testing.T) {
	m := newTabModel(t)
	m.createPaneTarget = paneTargetNewTab

	out, _ := m.openCreatePaneDialog()

	if got := out.(Model).createPaneTarget; got != paneTargetSplit {
		t.Errorf("target = %v after opening Ctrl+N, want paneTargetSplit", got)
	}
}

// The PALETTE's "New tab" row must dispatch the same way the keybinding does.
//
// Without this the palette path is only covered by a bucket assertion that the
// palette closed — which stays true if the row is wired to openCreatePaneDialog
// instead of handleNewTab, so a dispatch that splits the current tab rather
// than creating a new one passes silently.
func TestPalette_NewTabOpensThePickerInNewTabMode(t *testing.T) {
	m := newTabModel(t)
	f := m.client.(*fakeSender)

	// enabled:true is required — executePaletteCommand returns early on a row
	// that is not selectable(), so a zero-value literal never dispatches at all
	// and the assertions below would be about nothing.
	out, _ := m.executePaletteCommand(paletteCommand{action: palActNewTab, enabled: true})
	got := out.(Model)

	if got.dialog != dialogCreatePane {
		t.Errorf("dialog = %v, want the create-pane dialog", got.dialog)
	}
	if got.createPaneTarget != paneTargetNewTab {
		t.Errorf("target = %v, want paneTargetNewTab — the palette row is wired to the split opener", got.createPaneTarget)
	}
	if len(f.sent) != 0 {
		t.Errorf("the palette row already sent %v", sentMsgTypes(f))
	}
}

// The submit targets the daemon the dialog was OPENED against, not whichever
// project is active by the time the user presses Enter.
//
// This is the scenario createPaneDest exists for and it is not reachable from
// the keyboard: MCP set_active_pane moves the active project with no keystroke
// involved, and the dialog stays open for as long as the user takes. Every
// value in the form describes the open-time daemon's disk, so a submit routed
// elsewhere hands one machine's paths to another.
func TestNewTab_SubmitTargetsTheDialogsOwnDestination(t *testing.T) {
	m := newTabModel(t)
	f := m.client.(*fakeSender)
	m.cur().Dest = "user@buildhost"

	out, _ := m.handleNewTab()
	opened := out.(Model)
	if opened.createPaneDest != "user@buildhost" {
		t.Fatalf("createPaneDest = %q, want the dest captured at open", opened.createPaneDest)
	}

	// The active project moves under the open dialog — what MCP set_active_pane
	// does — and the submit must ignore it.
	opened.projects = append(opened.projects, &ProjectModel{ID: "proj-2", Name: "elsewhere", Dest: "user@other"})
	opened.activeProject = len(opened.projects) - 1
	opened.selectedPlugin = "terminal"

	_, cmd := opened.handleCreatePaneSplit()
	runCmd(cmd)

	// decodeCreateTab also asserts exactly one create_tab was sent, so the
	// Origin read below cannot be picking up some other message.
	decodeCreateTab(t, f)
	if got := f.sent[len(f.sent)-1].Origin; got != "user@buildhost" {
		t.Errorf("create_tab routed to %q, want the dialog's own destination", got)
	}
}

// A failed `git worktree add` on the NEW-TAB path must reach the user.
//
// That path arms no worktreeCreates entry — it has no placeholder to unwind —
// and applyCreatePaneResp's guard on that map is what makes it ignore the
// daemon's answer. The user asked for an agent on a fresh branch and got a
// plain shell in the project root; without this the only record is quild.log.
func TestNewTab_WorktreeFailureIsReported(t *testing.T) {
	m := newTabModel(t)
	m.createPaneTarget = paneTargetNewTab
	m.dialog = dialogCreatePane
	m.selectedPlugin = "claude-code"
	m.worktreeNewBranch = "feat/x"
	m.worktrees = worktreeState{loaded: true, repo: true, root: "/repo"}

	// Submit for real, so what arms the branch is the production path rather
	// than the test reaching into Model — a submit that stopped arming it would
	// otherwise still pass here.
	out, cmd := m.handleCreatePaneSplit()
	runCmd(cmd)
	submitted := out.(Model)
	if len(submitted.worktreeCreates) != 0 {
		t.Fatal("the new-tab path armed tab-keyed bookkeeping; it owns no tab id and must not")
	}

	updated, _ := submitted.Update(createPaneRespMsg{Resp: ipc.CreatePaneRespPayload{
		TabID:    "tab-the-daemon-minted",
		Error:    "fatal: 'feat/x' is already used by worktree at '/x/feat-y'",
		Worktree: &ipc.WorktreeSpec{RepoRoot: "/repo", Branch: "feat/x"},
	}})
	got := updated.(Model)

	if got.flashText == "" {
		t.Fatal("a failed worktree add on the new-tab path told the user nothing")
	}
	if !strings.Contains(got.flashText, "/x/feat-y") {
		t.Errorf("flash = %q, want git's own message — no text quil invents names the pane to look at", got.flashText)
	}
}

// A branch with no known repository root is REFUSED, never sent.
//
// The split path refuses the same pair for the same reason: falling back to the
// browsed directory is what puts a second checkout inside the first, and the
// client is explicitly not allowed to compute that path (protocol.go). The
// new-tab branch duplicates the refusal because it returns before the split
// path's copy of it.
func TestNewTab_BranchWithoutARepositoryRootIsRefused(t *testing.T) {
	m := newTabModel(t)
	f := m.client.(*fakeSender)
	m.createPaneTarget = paneTargetNewTab
	m.dialog = dialogCreatePane
	m.selectedPlugin = "claude-code"
	m.worktreeNewBranch = "feat/x"
	m.worktrees = worktreeState{} // never loaded, so root is unknown

	out, _ := m.handleCreatePaneSplit()
	got := out.(Model)

	if len(f.sent) != 0 {
		t.Errorf("sent %v — a branch with no known root must never reach the daemon", sentMsgTypes(f))
	}
	if got.flashText == "" {
		t.Error("the refusal said nothing")
	}
}

// A create that never reached its daemon has to say so. Router.Send drops an
// unroutable message and returns nil, so without this arm the user is told a
// tab was created that never was.
func TestCreateTabFailed_FlashesAndDoesNotRelisten(t *testing.T) {
	m := newTabModel(t)

	updated, cmd := m.Update(createTabFailedMsg{dest: "user@buildhost"})
	got := updated.(Model)

	if got.flashText == "" {
		t.Fatal("a create_tab that never left flashed nothing")
	}
	if !strings.Contains(got.flashText, "buildhost") {
		t.Errorf("flash = %q, want it to name the host", got.flashText)
	}
	// It is a SEND result, not an IPC response, so it must not re-arm the listen
	// loop — doing so would install a second reader of the router's channel.
	if _, isListen := cmd().(flashExpireMsg); !isListen {
		t.Errorf("cmd() = %T, want the flash expiry — this arm must not relisten", cmd())
	}
}

// Both startup carve-outs must survive the submit, not just the keypress.
//
// These drive a REAL Router, which is the whole point: fakeSender is not a
// *Router, so sendForDestStrict's refusal branch is structurally unreachable
// with it and every other test in this file would stay green through a total
// routing regression.
//
// The window: before the first workspace_state broadcast there is no project,
// so the dialog has no destination to pin. Under --remote the router is keyed
// by the REMOTE host and holds no "" conn at all, so pinning "" and stamping it
// refuses a send that must reach the only daemon there is. Router.Send's
// sole-conn fallback exists for exactly this — and it is gated on the message
// being UNSTAMPED (router.go), so a stamped send can never reach it.
func TestNewTab_BeforeTheFirstBroadcastReachesTheSoleDaemon(t *testing.T) {
	remote := newFakeConn()
	m := newTabModel(t)
	// The --remote shape: one conn, keyed by the host, and no "" entry.
	m.client = NewRouter(map[string]Client{"gpu01": remote})
	m.projects = nil // pre-first-broadcast
	m.activeProject = 0

	out, _ := m.handleNewTab()
	opened := out.(Model)
	if opened.dialog != dialogCreatePane {
		t.Fatalf("dialog = %v, want the picker to open in the startup window", opened.dialog)
	}

	_, cmd := opened.handleCreatePaneKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	runCmd(cmd)

	remote.mu.Lock()
	defer remote.mu.Unlock()
	if len(remote.sent) != 1 {
		t.Fatalf("the sole daemon received %d messages, want the create_tab", len(remote.sent))
	}
	if remote.sent[0].Type != ipc.MsgCreateTab {
		t.Errorf("sent %q, want create_tab", remote.sent[0].Type)
	}
}

// The same window once offline stand-ins have been seeded: every known project
// is a placeholder for some OTHER host that failed to dial, so m.cur() names an
// unreachable machine while the local daemon — the one the send resolves to —
// is fine. Flashing "cannot reach gpu01" here is verbatim the bug the carve-out
// was added to remove; deferring it from the keypress to the submit is the same
// bug one step later.
func TestNewTab_OnlyOfflineProjectsStillReachesTheLocalDaemon(t *testing.T) {
	local := newFakeConn()
	m := newTabModel(t)
	m.client = NewRouter(map[string]Client{"": local})
	m.projects = []*ProjectModel{
		{ID: "proj-offline@gpu01", Name: "gpu01", Dest: "gpu01", Offline: &OfflineState{}},
	}
	m.activeProject = 0

	out, _ := m.handleNewTab()
	opened := out.(Model)
	if opened.dialog != dialogCreatePane {
		t.Fatalf("dialog = %v, want the picker (the carve-out must not refuse here)", opened.dialog)
	}

	_, cmd := opened.handleCreatePaneKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	runCmd(cmd)

	local.mu.Lock()
	defer local.mu.Unlock()
	if len(local.sent) != 1 {
		t.Fatalf("the local daemon received %d messages, want the create_tab", len(local.sent))
	}
}

// The reachability refusal moves to dialog OPEN — filling in a form for a host
// that cannot be reached, only to have the send dropped silently, is worse than
// refusing the keystroke.
func TestNewTab_UnreachableProjectRefusesBeforeTheDialogOpens(t *testing.T) {
	m := newTabModel(t)
	f := m.client.(*fakeSender)
	p := m.cur()
	if p == nil {
		t.Fatal("fixture has no active project")
	}
	p.Dest = "user@host"
	p.Offline = &OfflineState{}
	// A SECOND, online project, or onlyOfflineProjects() answers true and the
	// startup carve-out correctly lets this through — that state means "the
	// first broadcast has not landed", not "this host is unreachable forever".
	m.projects = append(m.projects, &ProjectModel{ID: "proj-2", Name: "local"})

	out, _ := m.handleNewTab()
	got := out.(Model)

	if got.dialog == dialogCreatePane {
		t.Error("the dialog opened against a host that cannot be reached")
	}
	if len(f.sent) != 0 {
		t.Errorf("sent %v to an unreachable host", sentMsgTypes(f))
	}
	// Asserted explicitly: without it a handleNewTab that silently no-opped
	// would pass, and a key that appears to do nothing is indistinguishable
	// from a broken one — the reason this path flashes at all.
	if got.flashText == "" {
		t.Error("the refusal said nothing; Ctrl+T must name the host it cannot reach")
	}
}
