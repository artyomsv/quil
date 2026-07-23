package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The palette methods are exercised directly (not through the raw key event):
// the exact msg.String() for ctrl+shift+p is terminal-dependent, so the
// keybinding-to-open wiring is covered by the config default test + manual
// verification, while the behavior lives here.

func TestPalette_OpenClose(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	updated, _ := m.openCommandPalette()
	got := updated.(Model)
	if got.dialog != dialogCommandPalette {
		t.Fatalf("dialog = %v, want dialogCommandPalette", got.dialog)
	}
	if len(got.palette.filtered) == 0 {
		t.Error("open should pre-populate the (unfiltered) result list")
	}
	updated, _ = got.handleCommandPaletteKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if updated.(Model).dialog != dialogNone {
		t.Error("esc should close the palette")
	}
}

func TestPalette_NoOpInNotesMode(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.notesMode = true
	updated, _ := m.openCommandPalette()
	if updated.(Model).dialog == dialogCommandPalette {
		t.Error("palette must not open in notes mode")
	}
}

func TestPalette_TypingFiltersAndResetsCursor(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	opened, _ := m.openCommandPalette()
	got := opened.(Model)
	got.palette.cursor = 3

	updated, _ := got.handleCommandPaletteKey(tea.KeyPressMsg{Text: "s"})
	got = updated.(Model)
	if got.palette.query != "s" {
		t.Errorf("query = %q, want s", got.palette.query)
	}
	if got.palette.cursor != 0 {
		t.Error("cursor should reset to 0 on query change")
	}
	full := len(got.palette.commands)
	if len(got.palette.filtered) == 0 || len(got.palette.filtered) > full {
		t.Errorf("filtered = %d, want between 1 and %d", len(got.palette.filtered), full)
	}
	// Backspace clears the query and restores the full list.
	updated, _ = got.handleCommandPaletteKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	got = updated.(Model)
	if got.palette.query != "" {
		t.Errorf("query after backspace = %q, want empty", got.palette.query)
	}
	if len(got.palette.filtered) != full {
		t.Errorf("filtered after clear = %d, want all %d", len(got.palette.filtered), full)
	}
}

func TestPalette_CursorNavigationClamps(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	opened, _ := m.openCommandPalette()
	got := opened.(Model)
	// Up at the top stays at 0.
	updated, _ := got.handleCommandPaletteKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if updated.(Model).palette.cursor != 0 {
		t.Error("up at top should clamp to 0")
	}
	// Down moves.
	updated, _ = got.handleCommandPaletteKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if updated.(Model).palette.cursor != 1 {
		t.Error("down should move the cursor to 1")
	}
}

func TestPalette_GoToPaneFocusesTarget(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t) // ActivePane = p1
	m.dialog = dialogCommandPalette
	m.palette.commands = m.buildPaletteCommands()
	m.palette.filtered = filterPalette("", m.palette.commands)

	updated, _ := m.executePaletteCommand(paletteCommand{action: palActGoToPane, arg: "p2", enabled: true})
	got := updated.(Model)
	if got.dialog != dialogNone {
		t.Error("palette should close on execute")
	}
	if got.tabs[0].ActivePane != "p2" {
		t.Errorf("ActivePane = %q, want p2", got.tabs[0].ActivePane)
	}
	if !got.tabs[0].Root.Right.Pane.Active {
		t.Error("target pane should be marked active")
	}
	if got.tabs[0].Root.Left.Pane.Active {
		t.Error("previously-active pane should be cleared")
	}
}

func TestPalette_ExecuteClosePaneArmsConfirm(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.dialog = dialogCommandPalette
	updated, _ := m.executePaletteCommand(paletteCommand{action: palActClosePane, enabled: true})
	got := updated.(Model)
	if got.dialog != dialogConfirm || got.confirmKind != "pane" {
		t.Errorf("close-pane confirm not armed: dialog=%v kind=%q", got.dialog, got.confirmKind)
	}
}

func TestPalette_DisabledCommandInert(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.dialog = dialogCommandPalette
	updated, _ := m.executePaletteCommand(paletteCommand{action: palActHistory, enabled: false})
	got := updated.(Model)
	// Disabled: palette still closes (execute closes first) but no dialog opens.
	if got.dialog != dialogNone {
		t.Errorf("disabled command should not open a dialog, dialog=%v", got.dialog)
	}
}

func TestPalette_SwitchTabExecutes(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.dialog = dialogCommandPalette
	tabID := m.tabs[0].ID
	updated, _ := m.executePaletteCommand(paletteCommand{action: palActSwitchTab, arg: tabID, enabled: true})
	if updated.(Model).dialog != dialogNone {
		t.Error("palette should close after switch-tab")
	}
}
