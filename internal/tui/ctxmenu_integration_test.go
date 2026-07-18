package tui

import (
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
	w, h := ctxMenuBoxSize(got.ctxMenu.items)
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
