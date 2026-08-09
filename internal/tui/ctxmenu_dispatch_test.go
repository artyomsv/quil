package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// Fixture: newSplitDragTestModel — window 100x40, tab area rows 1..38,
// H-split p1 (cols 0-49) | p2 (cols 50-99), ActivePane p1.

func TestCtxMenu_RightClickOpensForPaneUnderCursor(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	if !got.ctxMenu.open() {
		t.Fatal("menu should open on right-click with no selection")
	}
	if got.ctxMenu.paneID != "p2" {
		t.Errorf("target = %q, want p2 (pane under cursor, not active pane)", got.ctxMenu.paneID)
	}
	if !got.curTabs()[0].Root.Right.Pane.ctxTargetHighlight {
		t.Error("target pane border highlight should be set")
	}
	// Position is clamped inside the content area.
	w, h := got.ctxMenu.boxSize()
	if got.ctxMenu.x+w > 100 || got.ctxMenu.y+h > 39 || got.ctxMenu.y < 1 {
		t.Errorf("menu box (%d,%d,%dx%d) escapes the content area", got.ctxMenu.x, got.ctxMenu.y, w, h)
	}
}

func TestCtxMenu_RightClickWithSelectionCopiesInstead(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.selection = &Selection{PaneID: "p1"}
	updated, _ := m.Update(tea.MouseClickMsg{X: 30, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	if got.ctxMenu.open() {
		t.Error("menu must NOT open while a selection is active (copy wins)")
	}
	if got.selection != nil {
		t.Error("right-click should consume the selection (copy path)")
	}
}

func TestCtxMenu_LeftClickOutsideCloses(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	updated, _ := m.Update(tea.MouseClickMsg{X: 20, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	updated, _ = got.Update(tea.MouseClickMsg{X: 90, Y: 30, Button: tea.MouseLeft})
	got = updated.(Model)
	if got.ctxMenu.open() {
		t.Error("outside left-click should close the menu")
	}
	if got.mouseDown {
		t.Error("the closing click must be swallowed, not arm a selection drag")
	}
	if got.curTabs()[0].Root.Left.Pane.ctxTargetHighlight {
		t.Error("target highlight should clear on close")
	}
}

func TestCtxMenu_RightClickElsewhereRetargets(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	updated, _ := m.Update(tea.MouseClickMsg{X: 20, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	updated, _ = got.Update(tea.MouseClickMsg{X: 70, Y: 20, Button: tea.MouseRight})
	got = updated.(Model)
	if got.ctxMenu.paneID != "p2" {
		t.Errorf("retarget: paneID = %q, want p2", got.ctxMenu.paneID)
	}
	if got.curTabs()[0].Root.Left.Pane.ctxTargetHighlight {
		t.Error("old target highlight should be cleared on retarget")
	}
	if !got.curTabs()[0].Root.Right.Pane.ctxTargetHighlight {
		t.Error("new target highlight should be set")
	}
}

func TestCtxMenu_KeyNavigationAndEsc(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	updated, _ := m.Update(tea.MouseClickMsg{X: 20, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	start := got.ctxMenu.cursor
	updated, _ = got.handleCtxMenuKey("down")
	got = updated.(Model)
	if got.ctxMenu.cursor == start {
		t.Error("down should move the cursor")
	}
	updated, _ = got.handleCtxMenuKey("esc")
	got = updated.(Model)
	if got.ctxMenu.open() {
		t.Error("esc should close the menu")
	}
}

func TestCtxMenu_QuitPassesThrough(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	updated, _ := m.Update(tea.MouseClickMsg{X: 20, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	_, cmd := got.handleCtxMenuKey("ctrl+q")
	if cmd == nil {
		t.Fatal("quit must never be swallowed by the menu")
	}
}

func TestCtxMenu_ExecuteClose_SwitchesTargetAndOpensConfirm(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t) // ActivePane = p1
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model) // targeting p2
	updated, _ = got.executeCtxMenuItem(ctxMenuItem{id: ctxActClose, label: "Close pane…", enabled: true})
	got = updated.(Model)
	if got.ctxMenu.open() {
		t.Error("menu should close on execute")
	}
	if got.curTabs()[0].ActivePane != "p2" {
		t.Errorf("ActivePane = %q, want p2 (dispatch focuses the target first)", got.curTabs()[0].ActivePane)
	}
	if got.dialog != dialogConfirm || got.confirmKind != "pane" || got.confirmID != "p2" {
		t.Errorf("close confirm not armed for p2: dialog=%v kind=%q id=%q", got.dialog, got.confirmKind, got.confirmID)
	}
}

func TestCtxMenu_Execute_SyncsActiveFlagOnBothPanes(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t) // ActivePane = p1
	p1 := m.curTabs()[0].Root.Left.Pane
	p2 := m.curTabs()[0].Root.Right.Pane
	p1.Active = true
	p2.Active = false
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model) // targeting p2
	updated, _ = got.executeCtxMenuItem(ctxMenuItem{id: ctxActMute, label: "Mute notifications", enabled: true})
	got = updated.(Model)
	if !got.curTabs()[0].Root.Right.Pane.Active {
		t.Error("p2.Active should be true after dispatch focuses it")
	}
	if got.curTabs()[0].Root.Left.Pane.Active {
		t.Error("p1.Active should be false — the old active pane must be cleared")
	}
	if got.curTabs()[0].ActivePane != "p2" {
		t.Errorf("ActivePane = %q, want p2", got.curTabs()[0].ActivePane)
	}
}

// TestCtxMenu_ExecuteAttention_SendsThePinToTheDaemon.
//
// The pin is daemon-owned, so the menu SENDS it and syncPaneMeta writes the
// answer back on the next broadcast. Asserting the local field here would pass
// against an implementation that only flips it locally — which is the bug: a
// local write is reverted by the next workspace_state (the git ticker alone
// delivers one every 5 s), so the mark would visibly undo itself and never
// survive a restart.
//
// Driven through executeCtxMenuItem and the returned Cmd rather than by calling
// sendPinnedAttention directly: the send only counts if the menu actually
// produces it.
func TestCtxMenu_ExecuteAttention_SendsThePinToTheDaemon(t *testing.T) {
	t.Parallel()
	fake := &fakeSender{}
	m := newSplitDragTestModel(t)
	m.client = fake
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model) // targeting p2
	updated, cmd := got.executeCtxMenuItem(ctxMenuItem{id: ctxActAttention, label: "Mark attention", enabled: true})
	got = updated.(Model)
	runCmd(cmd)

	if len(fake.sent) == 0 {
		t.Fatal("Mark attention sent nothing — the daemon owns the pin, so a purely local flip is lost on the next broadcast")
	}
	last := fake.sent[len(fake.sent)-1]
	if last.Type != ipc.MsgUpdatePane {
		t.Fatalf("sent %q, want %q", last.Type, ipc.MsgUpdatePane)
	}
	var payload ipc.UpdatePanePayload
	if err := last.DecodePayload(&payload); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if payload.PaneID != "p2" {
		t.Errorf("PaneID = %q, want p2 — the menu targets the right-clicked pane", payload.PaneID)
	}
	if payload.PinnedAttention == nil {
		t.Fatal("PinnedAttention is nil — an absent field means 'leave it alone', so the pin was never set")
	}
	if !*payload.PinnedAttention {
		t.Error("PinnedAttention = false, want true on a Mark")
	}
	// Unmark must send an explicit FALSE rather than omitting the field, which
	// is the whole reason the payload field is a *bool. The menu CLOSES on
	// dispatch, so it has to be reopened — executing against a closed menu
	// resolves no pane and sends nothing, which would have passed a weaker
	// assertion than the one below.
	got.curTabs()[0].Root.Right.Pane.pinnedAttention = true
	updated, _ = got.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got = updated.(Model)
	before := len(fake.sent)
	_, cmd = got.executeCtxMenuItem(ctxMenuItem{id: ctxActAttention, label: "Unmark attention", enabled: true})
	runCmd(cmd)
	if len(fake.sent) == before {
		t.Fatal("Unmark attention sent nothing")
	}
	if err := fake.sent[len(fake.sent)-1].DecodePayload(&payload); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if payload.PinnedAttention == nil || *payload.PinnedAttention {
		t.Error("Unmark attention did not send an explicit false")
	}
}

// Clear attention drops all four marks, and the pin is the one that also has to
// leave the machine — the other three are client-side display state.
func TestCtxMenu_ExecuteClearAttention_SendsThePinClear(t *testing.T) {
	t.Parallel()
	fake := &fakeSender{}
	m := newSplitDragTestModel(t)
	m.client = fake
	m.curTabs()[0].Root.Right.Pane.pinnedAttention = true
	m.curTabs()[0].Root.Right.Pane.unseen = true
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	updated, cmd := got.executeCtxMenuItem(ctxMenuItem{id: ctxActClearAttention, label: "Clear attention", enabled: true})
	got = updated.(Model)
	runCmd(cmd)

	pane := got.curTabs()[0].Root.Right.Pane
	if pane.unseen {
		t.Error("Clear attention left the unseen mark")
	}
	// Cleared locally too, so the row stops showing ◆ on THIS frame rather than
	// on the one after the round trip; the broadcast then confirms the same
	// value.
	if pane.pinnedAttention {
		t.Error("Clear attention left the pin set locally")
	}
	if len(fake.sent) == 0 {
		t.Fatal("Clear attention sent nothing — the pin would come back on the next broadcast")
	}
	var payload ipc.UpdatePanePayload
	if err := fake.sent[len(fake.sent)-1].DecodePayload(&payload); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if payload.PinnedAttention == nil || *payload.PinnedAttention {
		t.Error("Clear attention did not send an explicit pin clear")
	}
}

func TestCtxMenu_QuickActionsOpensForActivePane(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t) // ActivePane = p1
	updated, _ := m.openQuickActionsMenu()
	got := updated.(Model)
	if !got.ctxMenu.open() || got.ctxMenu.paneID != "p1" {
		t.Errorf("quick actions should target the active pane, got %q", got.ctxMenu.paneID)
	}
	// Suppressed in notes mode.
	m2 := newSplitDragTestModel(t)
	m2.notesMode = true
	updated, _ = m2.openQuickActionsMenu()
	if updated.(Model).ctxMenu.open() {
		t.Error("quick actions must be a no-op in notes mode")
	}
}

// The keyboard entry point (quick_actions) paints the menu against the
// FINAL composited frame, where the project sidebar (when open) has already
// shifted the pane area right by its width. The anchor comes from
// activePaneRect, whose OX is screen-absolute (seeded with
// projectSidebarWidth) — this pins that it lands where the pane is actually
// painted, whichever end of the pipeline supplies the offset. The mouse
// right-click path receives genuine screen coordinates and is covered by
// TestCtxMenu_RightClickOpensForPaneUnderCursor.
func TestCtxMenu_QuickActionsAnchorsPastTheProjectSidebar(t *testing.T) {
	t.Parallel()
	without := newSplitDragTestModel(t) // ActivePane = p1, sidebar closed
	updated, _ := without.openQuickActionsMenu()
	xWithout := updated.(Model).ctxMenu.x

	withSidebar := newSplitDragTestModel(t)
	withSidebar.sidebarOpen = true
	withSidebar.sidebarWidth = 22
	updated, _ = withSidebar.openQuickActionsMenu()
	got := updated.(Model)
	if !got.ctxMenu.open() {
		t.Fatal("quick actions should still open with the sidebar open")
	}
	if want := xWithout + 22; got.ctxMenu.x != want {
		t.Errorf("ctxMenu.x = %d, want %d (xWithout=%d + sidebarWidth=22) — the anchor did not "+
			"account for the project sidebar's reserved columns", got.ctxMenu.x, want, xWithout)
	}
}

func TestCtxMenu_VanishedTargetClosesOnNextMessage(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	// Simulate daemon reconciliation pruning p2.
	got.curTabs()[0].Root = NewLeaf(got.curTabs()[0].Root.Left.Pane)
	got.curTabs()[0].ActivePane = "p1"
	updated, _ = got.Update(tea.MouseMotionMsg{X: 1, Y: 1})
	if updated.(Model).ctxMenu.open() {
		t.Error("menu must close when its target pane no longer exists")
	}
}

// TestCtxMenu_ProjectMenuSurvivesUnrelatedMessages is the regression guard for
// the project menu closing itself a frame after it opened: the vanished-target
// guard at the top of Update resolved m.ctxMenu.paneID, which a
// project-targeted menu never sets, so the first message to arrive — any
// spinner tick, PTY chunk or resize — closed a menu the user had not had time
// to touch. The second half pins the equivalent guarantee for the project
// kind: the menu must still close when its PROJECT goes away (MsgDestroyProject
// from another client, or a workspace reconciliation).
func TestCtxMenu_ProjectMenuSurvivesUnrelatedMessages(t *testing.T) {
	t.Parallel()
	m := Model{
		cfg:           config.Default(),
		client:        newFakeConn(),
		width:         200,
		height:        40,
		sidebarOpen:   true,
		sidebarWidth:  22,
		notifications: NewNotificationCenter(40, 200),
		mcpHighlights: make(map[string]bool),
		projects: []*ProjectModel{
			{ID: "proj-a", Name: "alpha", tabs: []*TabModel{tabWith(&PaneModel{ID: "pane-a"})}},
			{ID: "proj-b", Name: "beta", tabs: []*TabModel{tabWith(&PaneModel{ID: "pane-b"})}},
		},
		activeProject: 0,
	}

	// Sidebar row 0 is the PROJECTS heading, so project 0 sits at screen row 1.
	updated, _ := m.Update(tea.MouseClickMsg{X: 3, Y: 1, Button: tea.MouseRight})
	got := updated.(Model)
	if !got.ctxMenu.open() || got.ctxMenu.projectID != "proj-a" {
		t.Fatalf("right-click on the first project row: open=%v projectID=%q, want the menu open on proj-a",
			got.ctxMenu.open(), got.ctxMenu.projectID)
	}

	updated, _ = got.Update(workSpinnerTickMsg{})
	got = updated.(Model)
	if !got.ctxMenu.open() {
		t.Fatal("the project menu closed itself on an unrelated tick — the user never gets to select a row")
	}
	if got.ctxMenu.projectID != "proj-a" {
		t.Errorf("projectID = %q, want proj-a — the target must survive too", got.ctxMenu.projectID)
	}

	// proj-a is gone; the menu targeting it must clean itself up.
	got.projects = got.projects[1:]
	got.activeProject = 0
	updated, _ = got.Update(workSpinnerTickMsg{})
	if updated.(Model).ctxMenu.open() {
		t.Error("menu must close when the project it targets no longer exists")
	}
}

func TestCtxMenu_WheelAndMotionSwallowedWhileOpen(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	updated, _ := m.Update(tea.MouseClickMsg{X: 20, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	before := got.ctxMenu
	updated, _ = got.Update(tea.MouseMotionMsg{X: 90, Y: 30}) // outside box
	got = updated.(Model)
	if !got.ctxMenu.open() {
		t.Error("motion outside must not close the menu")
	}
	if got.mouseDown || got.scrollDragPaneID != "" {
		t.Error("motion while open must not feed any drag")
	}
	_ = before
}

// TestCtxMenu_ClickInsideMenu_BeatsSidebarSwallow guards the input-vs-paint
// ordering bug: the menu is composited over the sidebar (View draws it
// last), so a menu box clamped near the right edge can show rows on top of
// the sidebar strip. The click router must match that paint order — a left
// click on an enabled row must execute the item even when that row's cell
// also lands inside the sidebar's swallow zone. Regression coverage for
// routing the ctxMenu case ahead of sidebarSwallowsMouse in
// tea.MouseClickMsg.
func TestCtxMenu_ClickInsideMenu_BeatsSidebarSwallow(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.notifications.visible = true
	sw := m.sidebarOverlayWidth()
	if sw == 0 {
		t.Fatal("fixture must produce a visible sidebar strip")
	}
	stripX := m.width - sw // first column of the sidebar strip

	// Open the menu on p2, anchored just left of the strip so the clamped
	// box overlaps it.
	anchorX := stripX - 2
	updated, _ := m.Update(tea.MouseClickMsg{X: anchorX, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	if !got.ctxMenu.open() {
		t.Fatal("menu should have opened")
	}
	boxW, _ := got.ctxMenu.boxSize()
	if got.ctxMenu.x+boxW <= stripX {
		t.Fatalf("test setup: box (x=%d w=%d) does not overlap sidebar strip at x=%d — adjust anchor", got.ctxMenu.x, boxW, stripX)
	}

	// Close is always enabled — find its row and compute the screen
	// coordinate that lands inside BOTH the menu box and the sidebar strip.
	closeRow := -1
	for i, it := range got.ctxMenu.items {
		if it.id == ctxActClose {
			closeRow = i
		}
	}
	if closeRow < 0 {
		t.Fatal("close item not found in menu")
	}
	clickY := got.ctxMenu.itemScreenY(closeRow)
	clickX := stripX + 1
	if clickX < got.ctxMenu.x || clickX >= got.ctxMenu.x+boxW {
		t.Fatalf("test setup: clickX=%d not inside box [%d,%d)", clickX, got.ctxMenu.x, got.ctxMenu.x+boxW)
	}
	if clickY < 1 || clickY >= m.height-1 {
		t.Fatalf("test setup: clickY=%d outside the sidebar's vertical range", clickY)
	}

	updated, _ = got.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	got2 := updated.(Model)
	if got2.ctxMenu.open() {
		t.Error("menu should close on execute")
	}
	if got2.dialog != dialogConfirm || got2.confirmKind != "pane" || got2.confirmID != "p2" {
		t.Errorf("close confirm not armed: dialog=%v kind=%q id=%q — click was swallowed by the sidebar instead of executing the topmost (visibly composited) menu row", got2.dialog, got2.confirmKind, got2.confirmID)
	}
}

// TestCtxMenu_NarrowTerminalGuard_NoInvisibleMenu guards against opening a
// menu whose box cannot fit inside the content area. overlayAt silently
// returns its base unchanged when the box would overshoot the right edge, so
// without a fit guard the menu becomes INVISIBLE while still owning all
// keyboard/mouse input (only Esc gets you out). The default 9-item menu box
// is ~23 cols wide; a 20-col terminal cannot fit it.
func TestCtxMenu_NarrowTerminalGuard_NoInvisibleMenu(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.width = 20 // narrower than the ~23-col box
	updated, _ := m.Update(tea.MouseClickMsg{X: 5, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	if got.ctxMenu.open() {
		t.Error("menu must not open when its box cannot fit inside the content area")
	}
	if got.curTabs()[0].Root.Left.Pane.ctxTargetHighlight || got.curTabs()[0].Root.Right.Pane.ctxTargetHighlight {
		t.Error("no pane should get the target highlight when the menu fails to open")
	}
}

// TestCtxMenu_ExecuteRestart_OpensConfirm covers the ctxActRestart dispatch
// branch (previously unexercised).
func TestCtxMenu_ExecuteRestart_OpensConfirm(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t) // ActivePane = p1
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model) // targeting p2
	updated, _ = got.executeCtxMenuItem(ctxMenuItem{id: ctxActRestart, label: "Restart pane…", enabled: true})
	got = updated.(Model)
	if got.dialog != dialogConfirm || got.confirmKind != confirmKindRestartPane || got.confirmID != "p2" {
		t.Errorf("restart confirm not armed for p2: dialog=%v kind=%q id=%q", got.dialog, got.confirmKind, got.confirmID)
	}
}

// TestCtxMenu_ExecuteRename_EntersRenameModeForTarget covers the
// ctxActRename dispatch branch (previously unexercised).
func TestCtxMenu_ExecuteRename_EntersRenameModeForTarget(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t) // ActivePane = p1
	m.curTabs()[0].Root.Right.Pane.Name = "Build"
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model) // targeting p2
	updated, _ = got.executeCtxMenuItem(ctxMenuItem{id: ctxActRename, label: "Rename pane", enabled: true})
	got = updated.(Model)
	if !got.renamingPane {
		t.Error("renamingPane should be true after ctxActRename dispatch")
	}
	if got.paneRenameInput != "Build" {
		t.Errorf("paneRenameInput = %q, want target pane's Name %q", got.paneRenameInput, "Build")
	}
}

// TestCtxMenu_ExecuteFocus_TogglesFocusModeOnActiveTab covers the
// ctxActFocus dispatch branch (previously unexercised).
func TestCtxMenu_ExecuteFocus_TogglesFocusModeOnActiveTab(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t) // ActivePane = p1
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model) // targeting p2
	updated, _ = got.executeCtxMenuItem(ctxMenuItem{id: ctxActFocus, label: "Focus mode", enabled: true})
	got = updated.(Model)
	if !got.curTabs()[0].FocusMode() {
		t.Error("active tab should be in focus mode after ctxActFocus dispatch")
	}
}

// TestCtxMenu_ExecuteNotes_OpensNotesMode covers the ctxActNotes dispatch
// branch (previously unexercised). Not run in parallel: toggleNotesMode
// opens a NotesEditor backed by config.NotesDir(), so QUIL_HOME must be
// redirected to a temp dir before the call — t.Setenv forbids t.Parallel,
// mirroring the isolation idiom used by TestUpdate_PasteMsgEmptyContent_
// FallsBackToImagePaste and TestMaybeShowUpdateNotice.
func TestCtxMenu_ExecuteNotes_OpensNotesMode(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newSplitDragTestModel(t) // ActivePane = p1
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model) // targeting p2
	updated, _ = got.executeCtxMenuItem(ctxMenuItem{id: ctxActNotes, label: "Open notes", enabled: true})
	got = updated.(Model)
	if !got.notesMode {
		t.Error("notesMode should be true after ctxActNotes dispatch")
	}
}

// TestCtxMenu_ExecuteHistory_NilRegistryDoesNotPanic covers the
// ctxActHistory dispatch branch (previously unexercised) and is the
// regression guard for the nil-pluginRegistry crash: buildCtxMenuItems
// nil-guards m.pluginRegistry, but openHistoryForActivePane historically did
// not, so reaching this branch through the menu with the fixture's nil
// registry panicked before that guard was added.
func TestCtxMenu_ExecuteHistory_NilRegistryDoesNotPanic(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t) // ActivePane = p1, m.pluginRegistry == nil
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model) // targeting p2
	updated, _ = got.executeCtxMenuItem(ctxMenuItem{id: ctxActHistory, label: "Input history", enabled: true})
	got = updated.(Model)
	if got.dialog != dialogCommandHistory {
		t.Errorf("dialog = %v, want dialogCommandHistory", got.dialog)
	}
	if got.history.supported {
		t.Error("history.supported should be false with a nil plugin registry")
	}
}

// TestCtxMenu_ExecuteLazygit_NilRegistryDoesNotPanic covers the
// ctxActLazygit dispatch branch (previously unexercised). The fixture panes
// have no CWD, so gitdiscover.Candidates returns no candidates before
// handleToggleLazygit ever touches the (nil) plugin registry — the
// observable outcome is a flash, not a panic either way.
func TestCtxMenu_ExecuteLazygit_NilRegistryDoesNotPanic(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t) // ActivePane = p1, m.pluginRegistry == nil
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model) // targeting p2
	updated, _ = got.executeCtxMenuItem(ctxMenuItem{id: ctxActLazygit, label: "Open lazygit", enabled: true})
	got = updated.(Model)
	if got.ctxMenu.open() {
		t.Error("menu should close on execute")
	}
	if got.flashText != "no git repo here" {
		t.Errorf("flashText = %q, want %q (no CWD on the fixture pane)", got.flashText, "no git repo here")
	}
}

// TestCtxMenu_FocusModeRightClick exercises paneRectAt's activePaneRectFocus
// branch: in focus mode the active pane fills the content area, so a
// right-click anywhere inside it targets the active pane, and a click
// outside the content area (e.g. the status bar row) must not open a menu.
func TestCtxMenu_FocusModeRightClick(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.curTabs()[0].ToggleFocus()
	if !m.curTabs()[0].FocusMode() {
		t.Fatal("fixture: ToggleFocus should enter focus mode on a multi-pane tab")
	}

	updated, _ := m.Update(tea.MouseClickMsg{X: 50, Y: 20, Button: tea.MouseRight})
	got := updated.(Model)
	if !got.ctxMenu.open() || got.ctxMenu.paneID != "p1" {
		t.Errorf("focus-mode right-click inside pane: paneID = %q, open = %v, want p1 open", got.ctxMenu.paneID, got.ctxMenu.open())
	}

	m2 := newSplitDragTestModel(t)
	m2.curTabs()[0].ToggleFocus()
	updated, _ = m2.Update(tea.MouseClickMsg{X: 50, Y: m2.height - 1, Button: tea.MouseRight})
	got2 := updated.(Model)
	if got2.ctxMenu.open() {
		t.Error("right-click on the status bar row must not open the menu in focus mode")
	}
}

// TestCtxMenu_HoverMovesCursorOnEnabledRow_NotOnDisabled covers the
// MouseMotionMsg hover-cursor path: motion inside the box on an enabled row
// moves the cursor there; motion on a disabled row (row 0, Input history,
// disabled by the fixture's nil plugin registry) leaves it unchanged.
func TestCtxMenu_HoverMovesCursorOnEnabledRow_NotOnDisabled(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)

	if got.ctxMenu.items[0].enabled {
		t.Fatal("fixture assumption broken: row 0 (Input history) should be disabled without a plugin registry")
	}
	const enabledRow = 1 // Focus mode — always enabled
	if !got.ctxMenu.items[enabledRow].enabled {
		t.Fatalf("fixture assumption broken: row %d should be enabled", enabledRow)
	}

	hoverX := got.ctxMenu.x + 1
	hoverY := got.ctxMenu.itemScreenY(enabledRow)
	updated, _ = got.Update(tea.MouseMotionMsg{X: hoverX, Y: hoverY})
	got2 := updated.(Model)
	if got2.ctxMenu.cursor != enabledRow {
		t.Errorf("hover on enabled row %d: cursor = %d, want %d", enabledRow, got2.ctxMenu.cursor, enabledRow)
	}

	before := got2.ctxMenu.cursor
	disabledY := got2.ctxMenu.itemScreenY(0) // Input history — disabled
	updated, _ = got2.Update(tea.MouseMotionMsg{X: hoverX, Y: disabledY})
	got3 := updated.(Model)
	if got3.ctxMenu.cursor != before {
		t.Errorf("hover on disabled row must not move cursor: got %d, want %d", got3.ctxMenu.cursor, before)
	}
}

// TestCtxMenu_ViewSwitchesToAllMotionWhileOpen: cell-motion never delivers
// buttonless hover, so View must request all-motion exactly while the menu
// is open — that is what makes the hover highlight work.
func TestCtxMenu_ViewSwitchesToAllMotionWhileOpen(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	if v := m.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("closed menu: MouseMode = %v, want CellMotion", v.MouseMode)
	}
	updated, _ := m.Update(tea.MouseClickMsg{X: 20, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	if !got.ctxMenu.open() {
		t.Fatal("menu should have opened")
	}
	if v := got.View(); v.MouseMode != tea.MouseModeAllMotion {
		t.Errorf("open menu: MouseMode = %v, want AllMotion", v.MouseMode)
	}
}

// TestCtxMenu_TitleShowsPaneDisplayName: the header row carries the target
// pane's display name so the user can see which pane the actions will hit.
func TestCtxMenu_TitleShowsPaneDisplayName(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.curTabs()[0].Root.Right.Pane.Name = "builds"
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	if got.ctxMenu.title != "builds" {
		t.Errorf("title = %q, want the target pane's display name", got.ctxMenu.title)
	}
	if !strings.Contains(renderCtxMenu(got.ctxMenu), "builds") {
		t.Error("rendered menu should contain the pane display name header")
	}
}

// TestCtxMenu_CompactFallbackOnShortTerminal: when the spaced box is taller
// than the content area but the compact one fits, the menu opens compact
// instead of not at all.
func TestCtxMenu_CompactFallbackOnShortTerminal(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.height = 16 // content area 14: spaced box (15) can't fit, compact (13) can
	updated, _ := m.Update(tea.MouseClickMsg{X: 20, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	if !got.ctxMenu.open() {
		t.Fatal("menu should open in compact layout on a short terminal")
	}
	if got.ctxMenu.spaced {
		t.Error("menu should have fallen back to the compact layout")
	}
	if _, h := got.ctxMenu.boxSize(); h > m.height-2 {
		t.Errorf("compact box h=%d still exceeds content area %d", h, m.height-2)
	}
}

// ---------------------------------------------------------------------------
// Sidebar pane row right-click (item 4)
// ---------------------------------------------------------------------------

// newTestModelWithSidebar builds a Model with the project sidebar open and
// one project holding two tabs: tab 0 (active, pane-1) and tab 1
// (background, pane-2) — so a sidebar right-click has a background-tab pane
// available to target. Dimensions match newSplitDragTestModel's so the
// pane context menu box has room to open.
func newTestModelWithSidebar(t *testing.T) Model {
	t.Helper()
	tab0 := tabWithPane("tab-0", "pane-1")
	tab1 := tabWithPane("tab-1", "pane-2")
	proj := &ProjectModel{ID: "proj-a", Name: "alpha", tabs: []*TabModel{tab0, tab1}}
	return Model{
		cfg:           config.Default(),
		width:         100,
		height:        40,
		sidebarOpen:   true,
		sidebarWidth:  22,
		notifications: NewNotificationCenter(40, 200),
		projects:      []*ProjectModel{proj},
		activeProject: 0,
	}
}

// sidebarPaneRowCoords resolves the screen coordinate of the sidebar pane
// row at flat ordinal `ordinal` (numbered across the active project's tabs,
// as sidebarHit numbers them) by scanning the same row list sidebarRowAt
// indexes — mirroring activateSidebarRow rather than hardcoding row
// geometry that shifts whenever sidebar_test.go's fixtures gain a row.
func sidebarPaneRowCoords(t *testing.T, m *Model, ordinal int) (int, int) {
	t.Helper()
	for y, row := range m.sidebarVisibleRows(m.projectSidebarWidth(), m.sidebarContentHeight()) {
		if row.kind == sidebarRowPane && row.index == ordinal {
			return 3, y // column 3: same arbitrary in-strip column sidebar_test.go uses
		}
	}
	t.Fatalf("no sidebar pane row for ordinal %d", ordinal)
	return 0, 0
}

// backgroundTabPaneID returns the id of a pane belonging to a tab other than
// the active one, for tests exercising executeCtxMenuItem's background-tab
// resolution.
func backgroundTabPaneID(t *testing.T, m *Model) string {
	t.Helper()
	activeIdx := m.activeTabIdx()
	for ti, tab := range m.curTabs() {
		if ti == activeIdx {
			continue
		}
		for _, pane := range tab.Leaves() {
			return pane.ID
		}
	}
	t.Fatal("fixture has no pane on a background tab")
	return ""
}

// TestSidebarRightClick_OpensPaneCtxMenu pins item 4. The MouseRight branch
// inside the sidebar swallow tested only sidebarRowProject, so a right-click on
// a pane row fell through to `return m, nil` — no menu, no feedback.
//
// Driven through Update, not by calling the handler: the bug IS a branch the
// call site never enters, and a direct-call test would pass against it.
func TestSidebarRightClick_OpensPaneCtxMenu(t *testing.T) {
	t.Parallel()
	m := newTestModelWithSidebar(t)
	x, y := sidebarPaneRowCoords(t, &m, 0)

	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight})
	got := updated.(Model)

	if !got.ctxMenu.open() {
		t.Fatal("right-click on a sidebar pane row should open the pane context menu")
	}
}

// TestSidebarRightClick_BackgroundTabPaneMenuActs pins the round-1 fix: a
// right-click on a sidebar pane row FOCUSES the pane first, exactly like
// left-click — reversing the earlier "does not move focus" decision. Opening
// the menu was not enough on its own: eight of the ten dispatched items
// (Rename among them) resolve their target through the ACTIVE tab's ACTIVE
// pane internally (shared with the keybinding and command-palette paths), so
// without focus-first a menu opened on a BACKGROUND-tab pane would act on
// whatever pane was on screen instead of the one the menu was titled after.
func TestSidebarRightClick_BackgroundTabPaneMenuActs(t *testing.T) {
	t.Parallel()
	m := newTestModelWithSidebar(t)
	// A pane row belonging to a tab that is not the active one. Given a
	// name distinct from the active tab's pane so Rename's seeded value
	// below proves WHICH pane it read, not just that it read something.
	pane, _, tabIdx := m.findPaneAndTab(backgroundTabPaneID(t, &m))
	if pane == nil || tabIdx == m.activeTabIdx() {
		t.Fatal("fixture must provide a pane on a background tab")
	}
	pane.Name = "wt-build"
	x, y := sidebarPaneRowCoords(t, &m, 1) // ordinal 1: the background pane

	updated, cmd := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight})
	got := updated.(Model)

	if !got.ctxMenu.open() || got.ctxMenu.paneID != pane.ID {
		t.Fatalf("right-click should open the menu on the clicked pane: open=%v paneID=%q, want %q",
			got.ctxMenu.open(), got.ctxMenu.paneID, pane.ID)
	}
	if got.activeTabIdx() != tabIdx {
		t.Errorf("right-click should focus the clicked pane's tab: activeTabIdx=%d, want %d", got.activeTabIdx(), tabIdx)
	}
	if !pane.Active {
		t.Error("right-click should focus the clicked pane")
	}
	if cmd == nil {
		t.Error("focusing a pane on a different tab must return switchTab's IPC cmd, not drop it")
	}

	updated2, _ := got.executeCtxMenuItem(ctxMenuItem{id: ctxActRename, enabled: true})
	got2 := updated2.(Model)

	if !got2.renamingPane || got2.paneRenameInput != "wt-build" {
		t.Errorf("Rename should act on the clicked pane: renamingPane=%v paneRenameInput=%q, want %q",
			got2.renamingPane, got2.paneRenameInput, "wt-build")
	}
}

// TestCtxMenu_ExecuteRefusesWhenTheActiveTabMovedAway pins the execute-time half
// of the property the focus-first entry points only establish at OPEN time.
//
// Eight of the ten items resolve their target through
// activeTabModel().ActivePaneModel(), so they are correct only while the menu's
// target sits in the ACTIVE tab. Every entry point focuses first, which makes
// that true when the menu opens — and nothing keeps it true afterwards: MCP
// set_active_pane (setActivePaneMsg → jumpToPane) moves the active project AND
// tab, and the Update-entry guard only closes a menu whose target has VANISHED.
// Keyboard and mouse cannot reach this; MCP is the one producer that can.
//
// Before the guard, Rename seeded the ON-SCREEN pane's name, Mute toggled the
// on-screen pane, and Restart/Close armed a confirm for it — each of them acting
// on a pane the menu was not titled after. The refusal is uniform across all ten
// items, INCLUDING the two attention rows that resolve paneID directly and could
// still have acted correctly: one rule for one surface, it matches what the code
// did before this branch, and the remedy is a second right-click.
func TestCtxMenu_ExecuteRefusesWhenTheActiveTabMovedAway(t *testing.T) {
	t.Parallel()
	m := newTestModelWithSidebar(t)
	m.client = newFakeConn()

	target, _, targetTabIdx := m.findPaneAndTab(backgroundTabPaneID(t, &m))
	if target == nil || targetTabIdx == m.activeTabIdx() {
		t.Fatal("fixture must provide a pane on a background tab")
	}
	target.Name = "wt-build"
	onScreen := m.curTabs()[m.activeTabIdx()].Leaves()[0]
	onScreen.Name = "on-screen"

	// Right-click the background pane's sidebar row: this focuses it, so its
	// tab becomes active and the menu's assumption holds at open time.
	x, y := sidebarPaneRowCoords(t, &m, 1)
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight})
	got := updated.(Model)
	if !got.ctxMenu.open() || got.ctxMenu.paneID != target.ID {
		t.Fatalf("right-click should open the menu on %q: open=%v paneID=%q",
			target.ID, got.ctxMenu.open(), got.ctxMenu.paneID)
	}

	// MCP set_active_pane moves the active tab out from under the open menu.
	updated, _ = got.Update(setActivePaneMsg{PaneID: onScreen.ID})
	got = updated.(Model)
	if !got.ctxMenu.open() {
		t.Fatal("precondition: the vanished-target guard must NOT close this menu — " +
			"the target pane still exists, only the active tab moved")
	}
	if got.activeTabIdx() == targetTabIdx {
		t.Fatal("fixture: set_active_pane did not move the active tab, so this test cannot fail")
	}

	// Rename resolves through the ACTIVE tab, so acting would seed "on-screen".
	updated, _ = got.executeCtxMenuItem(ctxMenuItem{id: ctxActRename, enabled: true})
	if r := updated.(Model); r.renamingPane {
		t.Errorf("Rename acted with paneRenameInput=%q — the menu targets %q, which the "+
			"active-tab dispatch cannot reach; the item must refuse", r.paneRenameInput, target.Name)
	}

	// Close arms a confirm dialog for whatever the active-tab dispatch resolves.
	updated, _ = got.executeCtxMenuItem(ctxMenuItem{id: ctxActClose, enabled: true})
	if c := updated.(Model); c.dialog == dialogConfirm {
		t.Errorf("Close armed a confirm for %q — the menu targets %q", c.confirmID, target.ID)
	}

	// The two paneID-direct attention items refuse under the same rule.
	updated, _ = got.executeCtxMenuItem(ctxMenuItem{id: ctxActAttention, enabled: true})
	a := updated.(Model)
	if target.pinnedAttention || onScreen.pinnedAttention {
		t.Errorf("Mark attention pinned a pane (target=%v on-screen=%v) — the menu refuses "+
			"as a whole once its target is off the active tab",
			target.pinnedAttention, onScreen.pinnedAttention)
	}
	if a.ctxMenu.open() {
		t.Error("a refused item must still close the menu")
	}
}
