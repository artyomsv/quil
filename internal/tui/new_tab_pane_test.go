package tui

import (
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
func newTabModel(t *testing.T) Model {
	t.Helper()
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

	if got := m.createPaneItemCount(); m.createPaneStep == 3 && got != 0 {
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
}
