package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// newMovePaneTestModel builds the shared move-pane fixture: an ACTIVE source
// project with tab T1 (tab-src-1) holding an H-split p1|p2 and tab T2
// (tab-src-2) holding p3, local project B (beta) with tab T3 (tab-b-1)
// holding p4, a local project marked Offline, a remote project on its own
// host, and a synthetic placeholder project standing in for a project-less
// host. 120x40 — row 0 the tab bar, row 39 the status bar, tabH = 38 — so
// resizeTabs gives every pane a real rect for the right-click tests below.
func newMovePaneTestModel(t *testing.T) *Model {
	t.Helper()
	m := newModelForTest(nil, 0)
	m.width, m.height = 120, 40
	m.notifications = NewNotificationCenter(30, 200)

	p1 := NewPaneModel("p1", 1024)
	p2 := NewPaneModel("p2", 1024)
	t1 := NewTabModel("tab-src-1", "Shell")
	t1.Root = NewLeaf(p1)
	t1.Root.SplitLeaf("p1", SplitHorizontal)
	t1.Root.Right.Pane = p2
	t1.ActivePane = "p1"

	p3 := NewPaneModel("p3", 1024)
	t2 := NewTabModel("tab-src-2", "Build")
	t2.Root = NewLeaf(p3)
	t2.ActivePane = "p3"

	source := &ProjectModel{ID: "proj-source", Name: "source", tabs: []*TabModel{t1, t2}}

	p4 := NewPaneModel("p4", 1024)
	t3 := NewTabModel("tab-b-1", "Shell")
	t3.Root = NewLeaf(p4)
	t3.ActivePane = "p4"
	projB := &ProjectModel{ID: "proj-b", Name: "beta", tabs: []*TabModel{t3}}

	projOffline := &ProjectModel{
		ID: "proj-offline", Name: "offline-proj", Offline: &OfflineState{},
		tabs: []*TabModel{NewTabModel("tab-off-1", "Shell")},
	}

	remote := &ProjectModel{
		ID: "proj-remote", Name: "remote", Dest: "user@TEST-host",
		tabs: []*TabModel{NewTabModel("tab-remote-1", "Shell")},
	}

	synthDest := "user@no-projects-host"
	synthetic := &ProjectModel{
		ID: interimProjectIDFor(synthDest), Name: interimProjectName, Dest: synthDest,
		tabs: []*TabModel{NewTabModel("tab-synth-1", "Shell")},
	}

	m.projects = []*ProjectModel{source, projB, projOffline, remote, synthetic}
	m.activeProject = 0
	// Gives p1/p2 (and every other pane) a real screen rect, so a right-click
	// on p2's half of the H-split actually hits it.
	m.resizeTabs()
	return &m
}

// openMovePanePickerViaMenu drives the path a real user takes: right-click
// paneID at (x,y) to open its context menu, then left-click the "Move to
// tab…" row — both through Update, per the test discipline (no direct
// executeCtxMenuItem/openMovePanePicker call).
func openMovePanePickerViaMenu(t *testing.T, m *Model, paneID string, x, y int) Model {
	t.Helper()
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight})
	got := updated.(Model)
	if !got.ctxMenu.open() || got.ctxMenu.paneID != paneID {
		t.Fatalf("setup: pane menu did not open for %q (paneID=%q)", paneID, got.ctxMenu.paneID)
	}

	idx := -1
	for i, it := range got.ctxMenu.items {
		if it.id == ctxActMovePane {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("Move to tab… item not found in the pane menu")
	}
	clickY := got.ctxMenu.itemScreenY(idx)
	clickX := got.ctxMenu.x + 1
	updated, _ = got.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	got2 := updated.(Model)
	if got2.dialog != dialogTabPick {
		t.Fatalf("setup: clicking Move to tab… did not open the picker (dialog=%v)", got2.dialog)
	}
	return got2
}

// ---------------------------------------------------------------------------
// movePaneCandidates — pure helper, called directly per the plan's own
// naming of it as such.
// ---------------------------------------------------------------------------

func TestMovePaneCandidates_SameDestAcrossProjects(t *testing.T) {
	t.Parallel()
	m := newMovePaneTestModel(t)
	got := m.movePaneCandidates("p2")
	if len(got) != 2 || got[0].ID != "tab-src-2" || got[1].ID != "tab-b-1" {
		t.Fatalf("movePaneCandidates = %v, want [tab-src-2 tab-b-1]", got)
	}
}

func TestMovePaneCandidates_ExcludesInFlightTabs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(m *Model, source *ProjectModel)
	}{
		{"worktree create", func(m *Model, source *ProjectModel) {
			m.worktreeCreates = map[string]string{"tab-src-2": "feat-x"}
		}},
		{"worktree replace", func(m *Model, source *ProjectModel) {
			m.worktreeReplaced = map[string]*PaneModel{"tab-src-2": NewPaneModel("held", 1024)}
		}},
		{"template layout pending", func(m *Model, source *ProjectModel) {
			source.tabs[1].templateLayoutPending = true
		}},
		{"preparing worktree leaf", func(m *Model, source *ProjectModel) {
			source.tabs[1].Leaves()[0].PreparingWorktree = "feat-y"
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newMovePaneTestModel(t)
			source := m.projectByID("proj-source")
			tc.setup(m, source)

			got := m.movePaneCandidates("p2")
			for _, tab := range got {
				if tab.ID == "tab-src-2" {
					t.Fatalf("tab-src-2 must be excluded while in flight: %v", got)
				}
			}
		})
	}
}

func TestMovePaneCandidates_NilForAnImmovablePane(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(m *Model)
	}{
		{"pane is preparing", func(m *Model) {
			m.projectByID("proj-source").tabs[0].Leaves()[1].PreparingWorktree = "feat-z" // p2 itself
		}},
		{"source has a worktree create in flight", func(m *Model) {
			m.worktreeCreates = map[string]string{"tab-src-1": "feat-z"}
		}},
		{"source template is pending", func(m *Model) {
			m.projectByID("proj-source").tabs[0].templateLayoutPending = true
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newMovePaneTestModel(t)
			tc.setup(m)
			if got := m.movePaneCandidates("p2"); got != nil {
				t.Fatalf("movePaneCandidates = %v, want nil", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Menu item + picker open
// ---------------------------------------------------------------------------

func TestPaneCtxMenu_MoveRowSitsAfterRenameAndIsGreyedWithoutCandidates(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	// Non-synthetic id, so the row's only possible reason to be disabled is
	// "nowhere to move to" rather than "not actionable".
	m.projects[0].ID = "proj-solo"

	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	if !got.ctxMenu.open() || got.ctxMenu.paneID != "p2" {
		t.Fatalf("setup: pane menu did not open for p2 (paneID=%q)", got.ctxMenu.paneID)
	}

	renameIdx, moveIdx := -1, -1
	for i, it := range got.ctxMenu.items {
		switch it.id {
		case ctxActRename:
			renameIdx = i
		case ctxActMovePane:
			moveIdx = i
		}
	}
	if renameIdx < 0 || moveIdx < 0 {
		t.Fatalf("setup: Rename pane (%d) or Move to tab… (%d) missing from the menu", renameIdx, moveIdx)
	}
	if moveIdx != renameIdx+1 {
		t.Fatalf("Move to tab… is at index %d, want directly after Rename pane (%d)", moveIdx, renameIdx)
	}
	if got.ctxMenu.items[moveIdx].enabled {
		t.Error("Move to tab… must be greyed with no other tab on the host")
	}
}

func TestPaneCtxMenu_MoveOpensTabPicker(t *testing.T) {
	t.Parallel()
	m := newMovePaneTestModel(t)
	got := openMovePanePickerViaMenu(t, m, "p2", 90, 10)

	if got.tabPick.paneID != "p2" || got.tabPick.srcTabID != "tab-src-1" {
		t.Fatalf("tabPick = %+v, want paneID=p2 srcTabID=tab-src-1", got.tabPick)
	}
	wantLabels := []string{"source / Build", "beta / Shell"}
	if len(got.tabPick.filtered) != len(wantLabels) {
		t.Fatalf("filtered = %v, want %v", got.tabPick.filtered, wantLabels)
	}
	for i, row := range got.tabPick.filtered {
		if row.label != wantLabels[i] {
			t.Errorf("filtered[%d].label = %q, want %q", i, row.label, wantLabels[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Picker interaction
// ---------------------------------------------------------------------------

func TestTabPicker_EnterSendsMovePaneAndStaysPut(t *testing.T) {
	t.Parallel()
	m := newMovePaneTestModel(t)
	fake := newFakeConn()
	m.client = fake

	got := openMovePanePickerViaMenu(t, m, "p2", 90, 10)
	wantActiveProject := got.activeProject
	wantActiveTab := got.activeTabIdx()

	// Move the cursor onto "beta / Shell" (tab-b-1), the second candidate.
	updated, _ := got.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got = updated.(Model)
	if row := got.tabPick.filtered[got.tabPick.cursor]; row.tabID != "tab-b-1" {
		t.Fatalf("setup: cursor row = %+v, want tab-b-1", row)
	}

	updated, cmd := got.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got2 := updated.(Model)
	if cmd == nil {
		t.Fatal("Enter returned a nil cmd, want tea.ClearScreen + the move send")
	}
	runCmd(cmd)

	if got2.dialog != dialogNone {
		t.Errorf("dialog = %v, want dialogNone after Enter", got2.dialog)
	}
	if got2.activeProject != wantActiveProject {
		t.Errorf("activeProject changed to %d, want unchanged %d", got2.activeProject, wantActiveProject)
	}
	if got2.activeTabIdx() != wantActiveTab {
		t.Errorf("activeTabIdx changed to %d, want unchanged %d", got2.activeTabIdx(), wantActiveTab)
	}

	var moveMsg, switchMsg *ipc.Message
	for _, msg := range fake.sent {
		switch msg.Type {
		case ipc.MsgMovePane:
			moveMsg = msg
		case ipc.MsgSwitchTab:
			switchMsg = msg
		}
	}
	if switchMsg != nil {
		t.Error("Enter must never send MsgSwitchTab")
	}
	if moveMsg == nil {
		t.Fatal("no MsgMovePane sent")
	}
	var payload ipc.MovePanePayload
	if err := moveMsg.DecodePayload(&payload); err != nil {
		t.Fatalf("decode move payload: %v", err)
	}
	if payload.PaneID != "p2" || payload.TabID != "tab-b-1" {
		t.Errorf("MsgMovePane = %+v, want PaneID=p2 TabID=tab-b-1", payload)
	}

	// No optimistic move: T1's tree still holds p2.
	source := got2.projectByID("proj-source")
	if source == nil || source.tabs[0].Root.FindLeaf("p2") == nil {
		t.Fatal("p2 must still be in T1's tree — no local mutation on send")
	}
}

func TestTabPicker_QueryMatchesProjectOrTabName(t *testing.T) {
	t.Parallel()
	m := newMovePaneTestModel(t)
	got := openMovePanePickerViaMenu(t, m, "p2", 90, 10)

	updated, _ := got.Update(tea.KeyPressMsg{Text: "beta"})
	next := updated.(Model)
	if len(next.tabPick.filtered) != 1 || next.tabPick.filtered[0].tabID != "tab-b-1" {
		t.Fatalf(`query "beta" = %v, want [tab-b-1]`, next.tabPick.filtered)
	}

	updated, _ = got.Update(tea.KeyPressMsg{Text: "Build"})
	next = updated.(Model)
	if len(next.tabPick.filtered) != 1 || next.tabPick.filtered[0].tabID != "tab-src-2" {
		t.Fatalf(`query "Build" = %v, want [tab-src-2]`, next.tabPick.filtered)
	}
}

func TestTabPicker_QueryCannotReachOutOfScopeTabs(t *testing.T) {
	t.Parallel()
	m := newMovePaneTestModel(t)
	got := openMovePanePickerViaMenu(t, m, "p2", 90, 10)

	updated, _ := got.Update(tea.KeyPressMsg{Text: "remote"})
	next := updated.(Model)
	if len(next.tabPick.filtered) != 0 {
		t.Errorf(`query "remote" reached an out-of-scope tab: filtered = %v`, next.tabPick.filtered)
	}
}

func TestTabPicker_BroadcastRefreshKeepsTheScope(t *testing.T) {
	t.Parallel()
	m := newMovePaneTestModel(t)
	m.client = newFakeConn()
	got := openMovePanePickerViaMenu(t, m, "p2", 90, 10)
	if len(got.tabPick.filtered) != 2 {
		t.Fatalf("setup: filtered = %v, want 2 rows", got.tabPick.filtered)
	}

	// An ordinary broadcast for the pane's own destination: source and its
	// candidates restated unchanged, p2 still in T1. The point is that the
	// SCOPE recomputation survives the round trip — filterTabPick reruns from
	// scratch on every workspace_state, and a scope captured only at open
	// would be widened back by the very next broadcast.
	updated, _ := got.Update(WorkspaceStateMsg{
		Dest: "",
		Projects: []ProjectInfo{
			{ID: "proj-source", Name: "source", TabIDs: []string{"tab-src-1", "tab-src-2"}, ActiveTab: "tab-src-1"},
			{ID: "proj-b", Name: "beta", TabIDs: []string{"tab-b-1"}, ActiveTab: "tab-b-1"},
		},
		Tabs: []TabInfo{
			{ID: "tab-src-1", Name: "Shell", ProjectID: "proj-source", Panes: []string{"p1", "p2"}},
			{ID: "tab-src-2", Name: "Build", ProjectID: "proj-source", Panes: []string{"p3"}},
			{ID: "tab-b-1", Name: "Shell", ProjectID: "proj-b", Panes: []string{"p4"}},
		},
		Panes: []PaneInfo{
			{ID: "p1", TabID: "tab-src-1", Type: "terminal"},
			{ID: "p2", TabID: "tab-src-1", Type: "terminal"},
			{ID: "p3", TabID: "tab-src-2", Type: "terminal"},
			{ID: "p4", TabID: "tab-b-1", Type: "terminal"},
		},
	})
	next := updated.(Model)

	if next.dialog != dialogTabPick {
		t.Fatalf("dialog = %v, want dialogTabPick — the pane is still in its source tab", next.dialog)
	}
	if len(next.tabPick.filtered) != 2 || next.tabPick.filtered[0].tabID != "tab-src-2" || next.tabPick.filtered[1].tabID != "tab-b-1" {
		t.Fatalf("filtered = %v, want [tab-src-2 tab-b-1] — a broadcast must not widen a tab-pick scope",
			next.tabPick.filtered)
	}
}

func TestTabPicker_ClosesWhenThePaneLeavesItsTab(t *testing.T) {
	t.Parallel()
	m := newMovePaneTestModel(t)
	m.client = newFakeConn()
	got := openMovePanePickerViaMenu(t, m, "p2", 90, 10)

	// p2 now lists under tab-b-1 — moved by another client while the picker
	// sat open.
	updated, _ := got.Update(WorkspaceStateMsg{
		Dest: "",
		Projects: []ProjectInfo{
			{ID: "proj-source", Name: "source", TabIDs: []string{"tab-src-1", "tab-src-2"}, ActiveTab: "tab-src-1"},
			{ID: "proj-b", Name: "beta", TabIDs: []string{"tab-b-1"}, ActiveTab: "tab-b-1"},
		},
		Tabs: []TabInfo{
			{ID: "tab-src-1", Name: "Shell", ProjectID: "proj-source", Panes: []string{"p1"}},
			{ID: "tab-src-2", Name: "Build", ProjectID: "proj-source", Panes: []string{"p3"}},
			{ID: "tab-b-1", Name: "Shell", ProjectID: "proj-b", Panes: []string{"p4", "p2"}},
		},
		Panes: []PaneInfo{
			{ID: "p1", TabID: "tab-src-1", Type: "terminal"},
			{ID: "p3", TabID: "tab-src-2", Type: "terminal"},
			{ID: "p4", TabID: "tab-b-1", Type: "terminal"},
			{ID: "p2", TabID: "tab-b-1", Type: "terminal"},
		},
	})
	next := updated.(Model)

	if next.dialog != dialogNone {
		t.Errorf("dialog = %v, want dialogNone — the pane moved out of its source tab", next.dialog)
	}
	if next.tabPick.paneID != "" {
		t.Errorf("tabPick = %+v, want the zero value after close", next.tabPick)
	}
}

func TestTabPicker_ClosesWhenThePaneVanishes(t *testing.T) {
	t.Parallel()
	m := newMovePaneTestModel(t)
	m.client = newFakeConn()
	got := openMovePanePickerViaMenu(t, m, "p2", 90, 10)

	// p2 is gone entirely — destroyed elsewhere while the picker sat open.
	updated, _ := got.Update(WorkspaceStateMsg{
		Dest: "",
		Projects: []ProjectInfo{
			{ID: "proj-source", Name: "source", TabIDs: []string{"tab-src-1", "tab-src-2"}, ActiveTab: "tab-src-1"},
			{ID: "proj-b", Name: "beta", TabIDs: []string{"tab-b-1"}, ActiveTab: "tab-b-1"},
		},
		Tabs: []TabInfo{
			{ID: "tab-src-1", Name: "Shell", ProjectID: "proj-source", Panes: []string{"p1"}},
			{ID: "tab-src-2", Name: "Build", ProjectID: "proj-source", Panes: []string{"p3"}},
			{ID: "tab-b-1", Name: "Shell", ProjectID: "proj-b", Panes: []string{"p4"}},
		},
		Panes: []PaneInfo{
			{ID: "p1", TabID: "tab-src-1", Type: "terminal"},
			{ID: "p3", TabID: "tab-src-2", Type: "terminal"},
			{ID: "p4", TabID: "tab-b-1", Type: "terminal"},
		},
	})
	next := updated.(Model)

	if next.dialog != dialogNone {
		t.Errorf("dialog = %v, want dialogNone — the pane no longer exists", next.dialog)
	}
}

// TestTabPicker_VanishCloseDoesNotDismissAReplacingDialog follows
// TestMoveTabPicker_VanishCloseDoesNotDismissAReplacingDialog: tabPick.paneID
// is cleared only by closeTabPicker, so a dialog that REPLACES the open
// picker without going through it (PluginErrorMsg sets m.dialog directly and
// never touches m.tabPick) leaves it stale. An ungated vanish-close would
// then dismiss that OTHER dialog on the next broadcast, mistaking it for the
// picker it no longer is.
func TestTabPicker_VanishCloseDoesNotDismissAReplacingDialog(t *testing.T) {
	t.Parallel()
	m := newMovePaneTestModel(t)
	m.client = newFakeConn()
	got := openMovePanePickerViaMenu(t, m, "p2", 90, 10)

	updated, _ := got.Update(PluginErrorMsg{Title: "boom", Message: "it broke"})
	next := updated.(Model)
	if next.dialog != dialogPluginError {
		t.Fatalf("setup: PluginErrorMsg did not open the plugin-error dialog: dialog=%v", next.dialog)
	}
	if next.tabPick.paneID == "" {
		t.Fatal("setup: PluginErrorMsg must not clear the stale tabPick — that IS the hazard under test")
	}

	// The vanish-close's own trigger — p2 leaves its source tab — while a
	// DIFFERENT dialog is on screen.
	updated, _ = next.Update(WorkspaceStateMsg{
		Dest: "",
		Projects: []ProjectInfo{
			{ID: "proj-source", Name: "source", TabIDs: []string{"tab-src-1", "tab-src-2"}, ActiveTab: "tab-src-1"},
			{ID: "proj-b", Name: "beta", TabIDs: []string{"tab-b-1"}, ActiveTab: "tab-b-1"},
		},
		Tabs: []TabInfo{
			{ID: "tab-src-1", Name: "Shell", ProjectID: "proj-source", Panes: []string{"p1"}},
			{ID: "tab-src-2", Name: "Build", ProjectID: "proj-source", Panes: []string{"p3"}},
			{ID: "tab-b-1", Name: "Shell", ProjectID: "proj-b", Panes: []string{"p4", "p2"}},
		},
		Panes: []PaneInfo{
			{ID: "p1", TabID: "tab-src-1", Type: "terminal"},
			{ID: "p3", TabID: "tab-src-2", Type: "terminal"},
			{ID: "p4", TabID: "tab-b-1", Type: "terminal"},
			{ID: "p2", TabID: "tab-b-1", Type: "terminal"},
		},
	})
	final := updated.(Model)

	if final.dialog != dialogPluginError {
		t.Errorf("dialog = %v, want dialogPluginError — the vanish-close must not dismiss a "+
			"dialog that replaced the picker", final.dialog)
	}
}

func TestTabPicker_EnterOnTargetThatBecameIneligibleSendsNothing(t *testing.T) {
	t.Parallel()
	m := newMovePaneTestModel(t)
	fake := newFakeConn()
	m.client = fake
	got := openMovePanePickerViaMenu(t, m, "p2", 90, 10)

	updated, _ := got.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got = updated.(Model)
	if row := got.tabPick.filtered[got.tabPick.cursor]; row.tabID != "tab-b-1" {
		t.Fatalf("setup: cursor row = %+v, want tab-b-1", row)
	}

	// The target starts a worktree create between open and Enter — another
	// client's action this client only learns about through its own state,
	// since no broadcast is required to arm worktreeCreates for THIS client's
	// own in-flight tracking. Mutated directly on the live Model, mirroring
	// TestMoveTabPicker_EnterOnTargetThatWentOfflineSendsNothing's approach.
	got.worktreeCreates = map[string]string{"tab-b-1": "feat-x"}

	updated, cmd := got.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	next := updated.(Model)
	// The re-check's whole effect is which Cmd the Enter branch RETURNS — the
	// in-scope path batches sendMovePane into it, the refused path returns
	// bare tea.ClearScreen — so the send only happens once this runs.
	runCmd(cmd)

	if next.dialog != dialogNone {
		t.Errorf("dialog = %v, want dialogNone", next.dialog)
	}
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgMovePane {
			t.Error("a target that became ineligible between open and Enter must not receive a move")
		}
	}
}

func TestTabPicker_EscSendsNothing(t *testing.T) {
	t.Parallel()
	m := newMovePaneTestModel(t)
	fake := newFakeConn()
	m.client = fake
	got := openMovePanePickerViaMenu(t, m, "p2", 90, 10)

	updated, cmd := got.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	next := updated.(Model)
	runCmd(cmd)

	if next.dialog != dialogNone {
		t.Errorf("dialog = %v, want dialogNone", next.dialog)
	}
	if len(fake.sent) != 0 {
		t.Errorf("Esc sent %d message(s), want 0", len(fake.sent))
	}
}

// ---------------------------------------------------------------------------
// Send failure
// ---------------------------------------------------------------------------

// TestMovePaneFailed_FlashesAndDoesNotRelisten follows
// TestMoveTabFailed_FlashesAndDoesNotRelisten (move_tab_test.go).
func TestMovePaneFailed_FlashesAndDoesNotRelisten(t *testing.T) {
	// newTabModel calls t.Setenv, which panics under t.Parallel().
	m := newTabModel(t)

	updated, cmd := m.Update(movePaneFailedMsg{dest: "user@buildhost"})
	got := updated.(Model)

	if got.flashText == "" {
		t.Fatal("a move_pane that never left flashed nothing")
	}
	if !strings.Contains(got.flashText, "buildhost") {
		t.Errorf("flash = %q, want it to name the host", got.flashText)
	}
	// It is a SEND result, not an IPC response, so it must not re-arm the
	// listen loop.
	if _, isFlash := cmd().(flashExpireMsg); !isFlash {
		t.Errorf("cmd() = %T, want the flash expiry — this arm must not relisten", cmd())
	}
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

func TestRenderTabPickDialog_HostileNames_NoRawESCByte(t *testing.T) {
	hostile := "shell\x1b[31m;rm -rf\x1b[0m"
	fixture := func(paneName, tabName, label string) Model {
		pane := NewPaneModel("p1", 1024)
		pane.Name = paneName
		tab := NewTabModel("tab-1", tabName)
		tab.Root = NewLeaf(pane)
		return Model{
			projects: []*ProjectModel{{ID: "proj-1", Name: "irrelevant", tabs: []*TabModel{tab}}},
			tabPick: tabPickState{
				paneID:   "p1",
				srcTabID: "tab-1",
				filtered: []tabPickRow{{tabID: "tab-2", label: label}},
			},
		}
	}
	clean := fixture("shell", "shell", "proj / shell")
	hostileM := fixture(hostile, hostile, hostile+" / "+hostile)

	cleanOut := clean.renderTabPickDialog()
	hostileOut := hostileM.renderTabPickDialog()

	wantESC := strings.Count(cleanOut, "\x1b")
	if got := strings.Count(hostileOut, "\x1b"); got != wantESC {
		t.Errorf("hostile names changed the raw ESC byte count: got %d, want %d (dialog-chrome baseline)\n%s",
			got, wantESC, hostileOut)
	}

	wantTitle := sanitizeRemoteText(hostile)
	stripped := stripANSI(hostileOut)
	if !strings.Contains(stripped, wantTitle) {
		t.Errorf("sanitized pane name %q missing from render\n%s", wantTitle, stripped)
	}
}

// ---------------------------------------------------------------------------
// End to end
// ---------------------------------------------------------------------------

// TestMovePane_MenuToBroadcastEndToEnd drives the whole feature through
// Update: right-click, the tab picker, Enter, then the daemon's own post-move
// broadcast. p2 must land in T3's right half as its active pane, and the user
// must still be on T1 with p1 active.
func TestMovePane_MenuToBroadcastEndToEnd(t *testing.T) {
	t.Parallel()
	m := newMovePaneTestModel(t)
	fake := newFakeConn()
	m.client = fake

	got := openMovePanePickerViaMenu(t, m, "p2", 90, 10)
	updated, _ := got.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // cursor onto tab-b-1
	got = updated.(Model)
	updated, cmd := got.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got2 := updated.(Model)
	runCmd(cmd)

	if got2.dialog != dialogNone {
		t.Fatalf("dialog = %v, want dialogNone after Enter", got2.dialog)
	}
	moveSent := false
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgMovePane {
			moveSent = true
		}
	}
	if !moveSent {
		t.Fatal("no MsgMovePane sent — nothing for the daemon to have acted on")
	}

	// The daemon's post-move broadcast: p2 now lists under tab-b-1.
	updated, _ = got2.Update(WorkspaceStateMsg{
		Dest: "",
		Projects: []ProjectInfo{
			{ID: "proj-source", Name: "source", TabIDs: []string{"tab-src-1", "tab-src-2"}, ActiveTab: "tab-src-1"},
			{ID: "proj-b", Name: "beta", TabIDs: []string{"tab-b-1"}, ActiveTab: "tab-b-1"},
		},
		Tabs: []TabInfo{
			{ID: "tab-src-1", Name: "Shell", ProjectID: "proj-source", Panes: []string{"p1"}},
			{ID: "tab-src-2", Name: "Build", ProjectID: "proj-source", Panes: []string{"p3"}},
			{ID: "tab-b-1", Name: "Shell", ProjectID: "proj-b", Panes: []string{"p4", "p2"}},
		},
		Panes: []PaneInfo{
			{ID: "p1", TabID: "tab-src-1", Type: "terminal"},
			{ID: "p3", TabID: "tab-src-2", Type: "terminal"},
			{ID: "p4", TabID: "tab-b-1", Type: "terminal"},
			{ID: "p2", TabID: "tab-b-1", Type: "terminal"},
		},
	})
	final := updated.(Model)

	target := final.projectByID("proj-b")
	if target == nil || len(target.tabs) != 1 {
		t.Fatalf("proj-b = %v", target)
	}
	tgtTab := target.tabs[0]
	if tgtTab.Root == nil || tgtTab.Root.IsLeaf() {
		t.Fatalf("target root = %+v, want a split", tgtTab.Root)
	}
	if tgtTab.Root.Right == nil || !tgtTab.Root.Right.IsLeaf() || tgtTab.Root.Right.Pane.ID != "p2" {
		t.Errorf("target Right = %+v, want leaf p2", tgtTab.Root.Right)
	}
	if tgtTab.ActivePane != "p2" {
		t.Errorf("target ActivePane = %q, want p2", tgtTab.ActivePane)
	}

	source := final.projectByID("proj-source")
	if final.cur() != source {
		t.Errorf("active project = %v, want source", final.cur())
	}
	activeTab := final.activeTabModel()
	if activeTab == nil || activeTab.ID != "tab-src-1" {
		t.Errorf("active tab = %v, want tab-src-1", activeTab)
	} else if activeTab.ActivePane != "p1" {
		t.Errorf("T1 ActivePane = %q, want p1", activeTab.ActivePane)
	}
}
