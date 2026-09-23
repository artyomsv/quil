package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// newMoveTabTestModel builds the shared move-tab fixture: an ACTIVE source
// project with two tabs, two other local projects it can move into (B, C), a
// project on the SAME Dest marked Offline (exercises the actionable filter
// independently of the Dest filter), two remote projects sharing one host —
// the tab's own remote project (remoteA) and its only live neighbour there
// (remoteB) — and a synthetic placeholder project standing in for a
// project-less host. 100x40, row 0 the tab bar, matching
// newTabCtxMenuTestModel's dimensions; no panes anywhere — none of the
// move-tab paths read a tab's Root.
func newMoveTabTestModel(t *testing.T) *Model {
	t.Helper()
	m := newModelForTest(nil, 0)
	m.width, m.height = 100, 40
	m.notifications = NewNotificationCenter(30, 200)

	source := &ProjectModel{ID: "proj-source", Name: "source", tabs: []*TabModel{
		NewTabModel("tab-src-1", "Shell"),
		NewTabModel("tab-src-2", "Build"),
	}}
	projB := &ProjectModel{ID: "proj-b", Name: "beta", tabs: []*TabModel{NewTabModel("tab-b-1", "Shell")}}
	projC := &ProjectModel{ID: "proj-c", Name: "gamma", tabs: []*TabModel{NewTabModel("tab-c-1", "Shell")}}
	projOffline := &ProjectModel{
		ID: "proj-offline", Name: "offline-proj", Offline: &OfflineState{},
		tabs: []*TabModel{NewTabModel("tab-off-1", "Shell")},
	}
	remoteA := &ProjectModel{
		ID: "proj-remote-a", Name: "remote-a", Dest: "user@TEST-host",
		tabs: []*TabModel{NewTabModel("tab-remote-1", "Shell")},
	}
	remoteB := &ProjectModel{
		ID: "proj-remote-b", Name: "remote-b", Dest: "user@TEST-host",
		tabs: []*TabModel{NewTabModel("tab-remote-2", "Shell")},
	}
	// A project-less host, on a THIRD dest so it stays out of the way of the
	// same-Dest and remote-Dest fixtures above.
	synthDest := "user@no-projects-host"
	synthetic := &ProjectModel{
		ID: interimProjectIDFor(synthDest), Name: interimProjectName, Dest: synthDest,
		tabs: []*TabModel{NewTabModel("tab-synth-1", "Shell")},
	}

	m.projects = []*ProjectModel{source, projB, projC, projOffline, remoteA, remoteB, synthetic}
	m.activeProject = 0
	return &m
}

// newMoveTabSoloTestModel is the smallest legal fixture: one project, so the
// active tab's move-tab candidates are empty and the menu row must be hidden
// rather than merely disabled.
func newMoveTabSoloTestModel(t *testing.T) *Model {
	t.Helper()
	m := newModelForTest(nil, 0)
	m.width, m.height = 100, 40
	m.notifications = NewNotificationCenter(30, 200)
	m.projects = []*ProjectModel{{ID: "proj-source", Name: "source", tabs: []*TabModel{
		NewTabModel("tab-src-1", "Shell"),
		NewTabModel("tab-src-2", "Build"),
	}}}
	m.activeProject = 0
	return &m
}

// moveTabItemIndex scans an open tab menu for the Move to project… row.
func moveTabItemIndex(t *testing.T, items []ctxMenuItem) int {
	t.Helper()
	for i, it := range items {
		if it.id == ctxActMoveTab {
			// buildTabCtxMenuItems appends it LAST, after Rename/Set color —
			// pinned here since every caller of this helper depends on it.
			if i != len(items)-1 {
				t.Fatalf("Move to project… is at index %d of %d, want the LAST row", i, len(items))
			}
			return i
		}
	}
	t.Fatal("Move to project… item not found in the tab menu")
	return -1
}

// flattenCmd recursively executes cmd and, for a tea.BatchMsg, every command
// inside it — mirroring runCmd's traversal but COLLECTING every resulting
// message instead of discarding them, so a test can look for one buried
// inside a batch (e.g. tea.ClearScreen alongside m.listenForMessages()).
func flattenCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, flattenCmd(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// openMoveTabPickerViaMenu drives the path a real user takes: right-click the
// tab at curTabs()[tabIdx] to open its context menu, then left-click the
// "Move to project…" row — both through Update, per the plan's test
// discipline (no direct executeCtxMenuItem call). Returns the resulting Model
// and the moved tab's ID.
func openMoveTabPickerViaMenu(t *testing.T, m *Model, tabIdx int) (Model, string) {
	t.Helper()
	tabID := m.curTabs()[tabIdx].ID
	x := tabBarX(t, m, tabIdx, 0)
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseRight})
	got := updated.(Model)
	if !got.ctxMenu.open() || got.ctxMenu.tabID != tabID {
		t.Fatalf("setup: tab menu did not open for %q", tabID)
	}

	idx := moveTabItemIndex(t, got.ctxMenu.items)
	clickY := got.ctxMenu.itemScreenY(idx)
	clickX := got.ctxMenu.x + 1
	updated, _ = got.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	got2 := updated.(Model)
	if got2.dialog != dialogProjectPick {
		t.Fatalf("setup: clicking Move to project… did not open the picker (dialog=%v)", got2.dialog)
	}
	return got2, tabID
}

// ---------------------------------------------------------------------------
// moveTabCandidates — pure helper, called directly per the plan's own
// naming of it as such.
// ---------------------------------------------------------------------------

func TestMoveTabCandidates_SameDestLiveOthersOnly(t *testing.T) {
	t.Parallel()
	m := newMoveTabTestModel(t)
	got := m.moveTabCandidates("tab-src-1")
	if len(got) != 2 || got[0].ID != "proj-b" || got[1].ID != "proj-c" {
		t.Fatalf("moveTabCandidates = %v, want [proj-b proj-c]", got)
	}
}

func TestMoveTabCandidates_RemoteTabSeesOnlyItsHost(t *testing.T) {
	t.Parallel()
	m := newMoveTabTestModel(t)
	got := m.moveTabCandidates("tab-remote-1")
	if len(got) != 1 || got[0].ID != "proj-remote-b" {
		t.Fatalf("moveTabCandidates = %v, want [proj-remote-b] — a remote tab's candidates "+
			"are that host's other live projects only", got)
	}
}

// ---------------------------------------------------------------------------
// Menu item + picker open
// ---------------------------------------------------------------------------

func TestTabCtxMenu_MoveItemHiddenWithoutCandidates(t *testing.T) {
	t.Parallel()
	m := newMoveTabSoloTestModel(t)
	x := tabBarX(t, m, 0, 0)
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseRight})
	got := updated.(Model)
	if !got.ctxMenu.open() {
		t.Fatal("setup: tab menu did not open — the loop below would vacuously see no items at all")
	}

	for _, it := range got.ctxMenu.items {
		if it.id == ctxActMoveTab {
			t.Fatal("Move to project… must be HIDDEN, not merely disabled, with no candidates")
		}
	}
}

func TestTabCtxMenu_MoveOpensPickerScopedToCandidates(t *testing.T) {
	t.Parallel()
	m := newMoveTabTestModel(t)
	got, tabID := openMoveTabPickerViaMenu(t, m, 0)

	if got.projectPick.moveTabID != tabID {
		t.Errorf("moveTabID = %q, want %q", got.projectPick.moveTabID, tabID)
	}
	if len(got.projectPick.filtered) != 2 || got.projectPick.filtered[0].ID != "proj-b" || got.projectPick.filtered[1].ID != "proj-c" {
		t.Fatalf("filtered = %v, want [proj-b proj-c]", got.projectPick.filtered)
	}
}

// ---------------------------------------------------------------------------
// Picker interaction
// ---------------------------------------------------------------------------

func TestMoveTabPicker_EnterSendsMoveTabAndStaysInProject(t *testing.T) {
	t.Parallel()
	m := newMoveTabTestModel(t)
	fake := newFakeConn()
	m.client = fake

	got, tabID := openMoveTabPickerViaMenu(t, m, 0)
	wantActiveProject := got.activeProject

	updated, cmd := got.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got2 := updated.(Model)
	if cmd == nil {
		t.Fatal("Enter in move mode returned a nil cmd, want tea.ClearScreen + the move send")
	}
	runCmd(cmd)

	if got2.dialog != dialogNone {
		t.Errorf("dialog = %v, want dialogNone after Enter", got2.dialog)
	}
	if got2.activeProject != wantActiveProject {
		t.Errorf("activeProject changed to %d, want unchanged %d — the user stays in the source project",
			got2.activeProject, wantActiveProject)
	}

	var moveMsg, switchMsg *ipc.Message
	for _, msg := range fake.sent {
		switch msg.Type {
		case ipc.MsgMoveTab:
			moveMsg = msg
		case ipc.MsgSwitchProject:
			switchMsg = msg
		}
	}
	if switchMsg != nil {
		t.Error("Enter in move mode must never send MsgSwitchProject")
	}
	if moveMsg == nil {
		t.Fatal("no MsgMoveTab sent")
	}
	var payload ipc.MoveTabPayload
	if err := moveMsg.DecodePayload(&payload); err != nil {
		t.Fatalf("decode move payload: %v", err)
	}
	if payload.TabID != tabID || payload.ProjectID != "proj-b" {
		t.Errorf("MsgMoveTab = %+v, want TabID=%q ProjectID=proj-b", payload, tabID)
	}
}

func TestMoveTabPicker_QueryCannotReachOutOfScopeProjects(t *testing.T) {
	t.Parallel()
	m := newMoveTabTestModel(t)
	got, _ := openMoveTabPickerViaMenu(t, m, 0)

	for _, query := range []string{"remote", "offline"} {
		updated, _ := got.Update(tea.KeyPressMsg{Text: query})
		next := updated.(Model)
		if len(next.projectPick.filtered) != 0 {
			t.Errorf("query %q reached an out-of-scope project: filtered = %v", query, next.projectPick.filtered)
		}
	}
}

func TestMoveTabPicker_BroadcastRefreshKeepsTheScope(t *testing.T) {
	t.Parallel()
	m := newMoveTabTestModel(t)
	m.client = newFakeConn()
	got, tabID := openMoveTabPickerViaMenu(t, m, 0)
	if len(got.projectPick.filtered) != 2 {
		t.Fatalf("setup: filtered = %v, want [proj-b proj-c]", got.projectPick.filtered)
	}

	// An ordinary broadcast for the tab's own destination: source and its
	// candidates restated unchanged. proj-offline is simply not mentioned
	// (an ordinary broadcast dropping a project it no longer reports) — the
	// point of this test is that the SCOPE recomputation survives the round
	// trip, which model.go's broadcast-refresh comment calls out explicitly:
	// filterProjects reruns from scratch on every workspace_state, and a scope
	// applied only at open time would let the first broadcast widen it back to
	// every project.
	updated, _ := got.Update(WorkspaceStateMsg{
		Dest: "",
		Projects: []ProjectInfo{
			{ID: "proj-source", Name: "source", TabIDs: []string{tabID, "tab-src-2"}, ActiveTab: tabID},
			{ID: "proj-b", Name: "beta", TabIDs: []string{"tab-b-1"}, ActiveTab: "tab-b-1"},
			{ID: "proj-c", Name: "gamma", TabIDs: []string{"tab-c-1"}, ActiveTab: "tab-c-1"},
		},
		Tabs: []TabInfo{
			{ID: tabID, Name: "Shell", ProjectID: "proj-source"},
			{ID: "tab-src-2", Name: "Build", ProjectID: "proj-source"},
			{ID: "tab-b-1", Name: "Shell", ProjectID: "proj-b"},
			{ID: "tab-c-1", Name: "Shell", ProjectID: "proj-c"},
		},
	})
	next := updated.(Model)

	if next.dialog != dialogProjectPick {
		t.Fatalf("dialog = %v, want dialogProjectPick — the picker's own tab is still present", next.dialog)
	}
	if len(next.projectPick.filtered) != 2 || next.projectPick.filtered[0].ID != "proj-b" || next.projectPick.filtered[1].ID != "proj-c" {
		t.Fatalf("filtered = %v, want [proj-b proj-c] — a broadcast must not widen a move-mode picker's scope",
			next.projectPick.filtered)
	}
}

func TestMoveTabPicker_ClosesWhenTheTabVanishes(t *testing.T) {
	t.Parallel()
	m := newMoveTabTestModel(t)
	fake := newFakeConn()
	// Closed up front so the batch's own m.listenForMessages() sub-cmd
	// returns immediately (io.EOF → linkLostMsg) instead of blocking on
	// Receive() forever when flattenCmd executes it below — same technique
	// tinyterm_test.go uses for the same reason.
	close(fake.recv)
	m.client = fake
	got, _ := openMoveTabPickerViaMenu(t, m, 0)

	// The tab the picker is about is destroyed elsewhere: source's TabIDs no
	// longer lists it, and no other project claims it either.
	updated, cmd := got.Update(WorkspaceStateMsg{
		Dest: "",
		Projects: []ProjectInfo{
			{ID: "proj-source", Name: "source", TabIDs: []string{"tab-src-2"}, ActiveTab: "tab-src-2"},
			{ID: "proj-b", Name: "beta", TabIDs: []string{"tab-b-1"}, ActiveTab: "tab-b-1"},
			{ID: "proj-c", Name: "gamma", TabIDs: []string{"tab-c-1"}, ActiveTab: "tab-c-1"},
		},
		Tabs: []TabInfo{
			{ID: "tab-src-2", Name: "Build", ProjectID: "proj-source"},
			{ID: "tab-b-1", Name: "Shell", ProjectID: "proj-b"},
			{ID: "tab-c-1", Name: "Shell", ProjectID: "proj-c"},
		},
	})
	next := updated.(Model)

	if next.dialog != dialogNone {
		t.Errorf("dialog = %v, want dialogNone — the tab being moved no longer exists", next.dialog)
	}
	if next.projectPick.moveTabID != "" {
		t.Errorf("projectPick = %+v, want the zero value after close", next.projectPick)
	}

	// Every other picker-close path (Enter, Esc) returns tea.ClearScreen; this
	// one must too, or the picker's stale border survives on screen until
	// something else forces a full redraw.
	haveClear := false
	for _, msg := range flattenCmd(cmd) {
		if msg == tea.ClearScreen() {
			haveClear = true
		}
	}
	if !haveClear {
		t.Error("the vanish-close did not return tea.ClearScreen")
	}
}

// TestMoveTabPicker_VanishCloseDoesNotDismissAReplacingDialog pins the guard
// on m.dialog == dialogProjectPick: moveTabID is cleared only by
// closeProjectPicker, so a dialog that REPLACES the open picker without going
// through it (PluginErrorMsg sets m.dialog directly and never touches
// m.projectPick) leaves moveTabID stale. An ungated vanish-close would then
// dismiss that OTHER dialog on the next broadcast, mistaking it for the
// picker it no longer is.
func TestMoveTabPicker_VanishCloseDoesNotDismissAReplacingDialog(t *testing.T) {
	t.Parallel()
	m := newMoveTabTestModel(t)
	fake := newFakeConn()
	close(fake.recv) // see TestMoveTabPicker_ClosesWhenTheTabVanishes
	m.client = fake
	got, _ := openMoveTabPickerViaMenu(t, m, 0)

	updated, _ := got.Update(PluginErrorMsg{Title: "boom", Message: "it broke"})
	next := updated.(Model)
	if next.dialog != dialogPluginError {
		t.Fatalf("setup: PluginErrorMsg did not open the plugin-error dialog: dialog=%v", next.dialog)
	}
	if next.projectPick.moveTabID == "" {
		t.Fatal("setup: PluginErrorMsg must not clear the stale moveTabID — that IS the hazard under test")
	}

	// A broadcast now reports the picker's tab gone — the vanish-close's own
	// trigger — while a DIFFERENT dialog is on screen.
	updated, _ = next.Update(WorkspaceStateMsg{
		Dest: "",
		Projects: []ProjectInfo{
			{ID: "proj-source", Name: "source", TabIDs: []string{"tab-src-2"}, ActiveTab: "tab-src-2"},
			{ID: "proj-b", Name: "beta", TabIDs: []string{"tab-b-1"}, ActiveTab: "tab-b-1"},
			{ID: "proj-c", Name: "gamma", TabIDs: []string{"tab-c-1"}, ActiveTab: "tab-c-1"},
		},
		Tabs: []TabInfo{
			{ID: "tab-src-2", Name: "Build", ProjectID: "proj-source"},
			{ID: "tab-b-1", Name: "Shell", ProjectID: "proj-b"},
			{ID: "tab-c-1", Name: "Shell", ProjectID: "proj-c"},
		},
	})
	final := updated.(Model)

	if final.dialog != dialogPluginError {
		t.Errorf("dialog = %v, want dialogPluginError — the vanish-close must not dismiss a "+
			"dialog that replaced the picker", final.dialog)
	}
}

func TestMoveTabPicker_EnterOnTargetThatWentOfflineSendsNothing(t *testing.T) {
	t.Parallel()
	m := newMoveTabTestModel(t)
	fake := newFakeConn()
	m.client = fake
	got, _ := openMoveTabPickerViaMenu(t, m, 0)
	if len(got.projectPick.filtered) < 1 || got.projectPick.filtered[0].ID != "proj-b" {
		t.Fatalf("setup: filtered = %v, want proj-b first", got.projectPick.filtered)
	}

	// The target goes offline between open and Enter. Mutated directly on the
	// live *ProjectModel the picker's snapshot already points at — a runtime
	// host disconnect is client-side only (projects.md), not necessarily a
	// broadcast.
	got.projectByID("proj-b").Offline = &OfflineState{}

	updated, cmd := got.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	next := updated.(Model)
	// The re-check's whole effect is which Cmd the Enter branch RETURNS — the
	// in-scope path batches sendMoveTab into it, the refused path returns bare
	// tea.ClearScreen — so the send (and the bug this test exists to catch)
	// only happens once this runs.
	runCmd(cmd)

	if next.dialog != dialogNone {
		t.Errorf("dialog = %v, want dialogNone", next.dialog)
	}
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgMoveTab {
			t.Error("a target that went offline between open and Enter must not receive a move")
		}
	}
}

func TestMoveTabPicker_EscSendsNothing(t *testing.T) {
	t.Parallel()
	m := newMoveTabTestModel(t)
	fake := newFakeConn()
	m.client = fake
	got, _ := openMoveTabPickerViaMenu(t, m, 0)

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

// TestProjectPicker_AltPAfterMoveModeIsPlainSwitch: opening the picker in move
// mode must not leave it stuck there — Esc closes it outright, and the next
// Alt+P is an ordinary, unscoped picker whose Enter switches project rather
// than moving anything.
func TestProjectPicker_AltPAfterMoveModeIsPlainSwitch(t *testing.T) {
	t.Parallel()
	m := newMoveTabTestModel(t)
	fake := newFakeConn()
	m.client = fake
	got, _ := openMoveTabPickerViaMenu(t, m, 0)

	updated, cmd := got.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	next := updated.(Model)
	runCmd(cmd)
	if next.dialog != dialogNone {
		t.Fatal("setup: Esc did not close the move-mode picker")
	}

	updated, cmd = next.Update(tea.KeyPressMsg{Mod: tea.ModAlt, Code: 'p'})
	next = updated.(Model)
	runCmd(cmd)
	if next.dialog != dialogProjectPick || next.projectPick.moveTabID != "" {
		t.Fatalf("Alt+P did not open a plain (non-move) picker: dialog=%v moveTabID=%q",
			next.dialog, next.projectPick.moveTabID)
	}
	if len(next.projectPick.filtered) != len(next.projects) {
		t.Fatalf("filtered = %d, want every project (%d) — a plain picker is unscoped",
			len(next.projectPick.filtered), len(next.projects))
	}

	updated, cmd = next.Update(tea.KeyPressMsg{Text: "beta"})
	next = updated.(Model)
	runCmd(cmd)
	if len(next.projectPick.filtered) != 1 || next.projectPick.filtered[0].ID != "proj-b" {
		t.Fatalf("filtered = %v, want [proj-b]", next.projectPick.filtered)
	}

	updated, cmd = next.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	next = updated.(Model)
	// This is the assertion that matters for "no MsgMoveTab": the move-mode
	// send only happens inside the closure sendMoveTab returns, so without
	// running the Cmd a stale moveTabID surviving Esc would pass silently.
	runCmd(cmd)

	if next.dialog != dialogNone {
		t.Errorf("dialog = %v, want dialogNone after Enter", next.dialog)
	}
	if got := next.projects[next.activeProject].ID; got != "proj-b" {
		t.Errorf("activeProject = %q, want proj-b — Alt+P after Esc must be a plain switch", got)
	}

	var moveMsg, switchMsg *ipc.Message
	for _, msg := range fake.sent {
		switch msg.Type {
		case ipc.MsgMoveTab:
			moveMsg = msg
		case ipc.MsgSwitchProject:
			switchMsg = msg
		}
	}
	if moveMsg != nil {
		t.Error("no MsgMoveTab should ever be sent on this path")
	}
	if switchMsg == nil {
		t.Error("no MsgSwitchProject sent — the plain picker must still switch")
	}
}

// ---------------------------------------------------------------------------
// Send failure
// ---------------------------------------------------------------------------

// TestMoveTabFailed_FlashesAndDoesNotRelisten follows
// TestCreateTabFailed_FlashesAndDoesNotRelisten (new_tab_pane_test.go).
func TestMoveTabFailed_FlashesAndDoesNotRelisten(t *testing.T) {
	// newTabModel calls t.Setenv, which panics under t.Parallel — matches
	// TestCreateTabFailed_FlashesAndDoesNotRelisten, which is not parallel
	// either.
	m := newTabModel(t)

	updated, cmd := m.Update(moveTabFailedMsg{dest: "user@buildhost"})
	got := updated.(Model)

	if got.flashText == "" {
		t.Fatal("a move_tab that never left flashed nothing")
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
// Broadcast reconciliation (the actual move)
// ---------------------------------------------------------------------------

// TestMoveTab_BroadcastRelocatesTabAndKeepsUserInSource pins the "no code
// needed" claim in the plan: applyWorkspaceState's existing existingTabs
// index (keyed by ID across every project) is what makes a moved tab keep its
// identity, and resolveActiveProjectIndex is what keeps the user's own
// project selection put.
func TestMoveTab_BroadcastRelocatesTabAndKeepsUserInSource(t *testing.T) {
	t.Parallel()
	source := &ProjectModel{ID: "proj-source", Name: "source", tabs: []*TabModel{
		NewTabModel("tab-src-1", "Shell"),
		NewTabModel("tab-src-2", "Build"),
	}}
	projB := &ProjectModel{ID: "proj-b", Name: "beta", tabs: []*TabModel{NewTabModel("tab-b-1", "Shell")}}
	m := Model{
		client:        newFakeConn(),
		projects:      []*ProjectModel{source, projB},
		activeProject: 0,
	}
	movedTab := source.tabs[0] // tab-src-1, captured before the move

	updated, _ := m.Update(WorkspaceStateMsg{
		Dest: "",
		Projects: []ProjectInfo{
			{ID: "proj-source", Name: "source", TabIDs: []string{"tab-src-2"}, ActiveTab: "tab-src-2"},
			{ID: "proj-b", Name: "beta", TabIDs: []string{"tab-b-1", "tab-src-1"}, ActiveTab: "tab-src-1"},
		},
		Tabs: []TabInfo{
			{ID: "tab-src-2", Name: "Build", ProjectID: "proj-source"},
			{ID: "tab-b-1", Name: "Shell", ProjectID: "proj-b"},
			{ID: "tab-src-1", Name: "Shell", ProjectID: "proj-b"},
		},
	})
	got := updated.(Model)

	gotSource := got.cur()
	if gotSource == nil || gotSource.ID != "proj-source" {
		t.Fatalf("m.cur() = %v, want still proj-source", gotSource)
	}
	if len(gotSource.tabs) != 1 || gotSource.tabs[0].ID != "tab-src-2" {
		t.Fatalf("source tabs = %v, want only [tab-src-2] — the moved tab must be gone from the source",
			gotSource.tabs)
	}
	if idx := gotSource.activeTab; idx < 0 || idx >= len(gotSource.tabs) || gotSource.tabs[idx].ID != "tab-src-2" {
		t.Fatalf("source activeTab = %d, want it to resolve to the successor tab-src-2", idx)
	}

	target := got.projectByID("proj-b")
	if target == nil {
		t.Fatal("proj-b vanished")
	}
	var found *TabModel
	for _, tb := range target.tabs {
		if tb.ID == "tab-src-1" {
			found = tb
		}
	}
	if found == nil {
		t.Fatal("tab-src-1 did not land in proj-b")
	}
	if found != movedTab {
		t.Error("the moved tab's *TabModel pointer changed — identity (layout, VT, scrollback) was lost")
	}
}

// TestRenderProjectPickDialog_MoveModeHostileTabName_NoRawESCByte follows
// TestRenderProjectPickDialog_HostileCandidate_NoRawESCByte: the tab named in
// the move-mode title is daemon-sourced and must not inject a raw ESC byte
// into the rendered dialog.
func TestRenderProjectPickDialog_MoveModeHostileTabName_NoRawESCByte(t *testing.T) {
	hostileName := "shell\x1b[31m;rm -rf\x1b[0m"
	fixture := func(tabName string) Model {
		return Model{
			projects: []*ProjectModel{{ID: "proj-source", tabs: []*TabModel{{ID: "tab-1", Name: tabName}}}},
			projectPick: projectPickState{
				moveTabID: "tab-1",
				filtered:  []*ProjectModel{{ID: "proj-b", Name: "beta"}},
			},
		}
	}
	clean := fixture("shell")
	hostile := fixture(hostileName)

	cleanOut := clean.renderProjectPickDialog()
	hostileOut := hostile.renderProjectPickDialog()

	wantESC := strings.Count(cleanOut, "\x1b")
	if got := strings.Count(hostileOut, "\x1b"); got != wantESC {
		t.Errorf("hostile tab name changed the raw ESC byte count: got %d, want %d (dialog-chrome baseline)\n%s",
			got, wantESC, hostileOut)
	}

	wantContent := sanitizeRemoteText(hostileName)
	if !strings.Contains(stripANSI(hostileOut), wantContent) {
		t.Errorf("sanitized tab name %q missing from render — row may have been dropped rather than cleaned\n%s",
			wantContent, stripANSI(hostileOut))
	}
}
