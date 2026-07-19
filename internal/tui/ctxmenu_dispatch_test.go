package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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
	if !got.tabs[0].Root.Right.Pane.ctxTargetHighlight {
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
	if got.tabs[0].Root.Left.Pane.ctxTargetHighlight {
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
	if got.tabs[0].Root.Left.Pane.ctxTargetHighlight {
		t.Error("old target highlight should be cleared on retarget")
	}
	if !got.tabs[0].Root.Right.Pane.ctxTargetHighlight {
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
	if got.tabs[0].ActivePane != "p2" {
		t.Errorf("ActivePane = %q, want p2 (dispatch focuses the target first)", got.tabs[0].ActivePane)
	}
	if got.dialog != dialogConfirm || got.confirmKind != "pane" || got.confirmID != "p2" {
		t.Errorf("close confirm not armed for p2: dialog=%v kind=%q id=%q", got.dialog, got.confirmKind, got.confirmID)
	}
}

func TestCtxMenu_Execute_SyncsActiveFlagOnBothPanes(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t) // ActivePane = p1
	p1 := m.tabs[0].Root.Left.Pane
	p2 := m.tabs[0].Root.Right.Pane
	p1.Active = true
	p2.Active = false
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model) // targeting p2
	updated, _ = got.executeCtxMenuItem(ctxMenuItem{id: ctxActMute, label: "Mute notifications", enabled: true})
	got = updated.(Model)
	if !got.tabs[0].Root.Right.Pane.Active {
		t.Error("p2.Active should be true after dispatch focuses it")
	}
	if got.tabs[0].Root.Left.Pane.Active {
		t.Error("p1.Active should be false — the old active pane must be cleared")
	}
	if got.tabs[0].ActivePane != "p2" {
		t.Errorf("ActivePane = %q, want p2", got.tabs[0].ActivePane)
	}
}

func TestCtxMenu_ExecuteAttention_TogglesPin(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	updated, _ = got.executeCtxMenuItem(ctxMenuItem{id: ctxActAttention, label: "Mark attention", enabled: true})
	got = updated.(Model)
	if !got.tabs[0].Root.Right.Pane.pinnedAttention {
		t.Error("attention pin should be set on p2")
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

func TestCtxMenu_VanishedTargetClosesOnNextMessage(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	// Simulate daemon reconciliation pruning p2.
	got.tabs[0].Root = NewLeaf(got.tabs[0].Root.Left.Pane)
	got.tabs[0].ActivePane = "p1"
	updated, _ = got.Update(tea.MouseMotionMsg{X: 1, Y: 1})
	if updated.(Model).ctxMenu.open() {
		t.Error("menu must close when its target pane no longer exists")
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
	if got.tabs[0].Root.Left.Pane.ctxTargetHighlight || got.tabs[0].Root.Right.Pane.ctxTargetHighlight {
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
	m.tabs[0].Root.Right.Pane.Name = "Build"
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
	if !got.tabs[0].FocusMode() {
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
	m.tabs[0].ToggleFocus()
	if !m.tabs[0].FocusMode() {
		t.Fatal("fixture: ToggleFocus should enter focus mode on a multi-pane tab")
	}

	updated, _ := m.Update(tea.MouseClickMsg{X: 50, Y: 20, Button: tea.MouseRight})
	got := updated.(Model)
	if !got.ctxMenu.open() || got.ctxMenu.paneID != "p1" {
		t.Errorf("focus-mode right-click inside pane: paneID = %q, open = %v, want p1 open", got.ctxMenu.paneID, got.ctxMenu.open())
	}

	m2 := newSplitDragTestModel(t)
	m2.tabs[0].ToggleFocus()
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
	m.tabs[0].Root.Right.Pane.Name = "builds"
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
