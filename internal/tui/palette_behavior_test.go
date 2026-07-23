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
	// ctrl+n / ctrl+p are aliases for down / up.
	updated, _ = got.handleCommandPaletteKey(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if updated.(Model).palette.cursor != 1 {
		t.Errorf("ctrl+n should move down, cursor = %d", updated.(Model).palette.cursor)
	}
	// Down at the bottom clamps to len-1.
	got.palette.cursor = len(got.palette.filtered) - 1
	updated, _ = got.handleCommandPaletteKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if c := updated.(Model).palette.cursor; c != len(got.palette.filtered)-1 {
		t.Errorf("down at bottom should clamp, cursor = %d, want %d", c, len(got.palette.filtered)-1)
	}
}

func TestPalette_SpaceExtendsQuery(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	opened, _ := m.openCommandPalette()
	got := opened.(Model)
	updated, _ := got.handleCommandPaletteKey(tea.KeyPressMsg{Code: tea.KeySpace})
	if q := updated.(Model).palette.query; q != " " {
		t.Errorf("space should append a space, query = %q", q)
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

// TestPalette_DispatchOpensExpectedDialog exercises every dialog-opening action
// so a mis-mapped case (e.g. palActPlugins landing on dialogSettings) is caught.
func TestPalette_DispatchOpensExpectedDialog(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		action      paletteAction
		wantDialog  dialogScreen
		confirmKind string
	}{
		{"settings", palActSettings, dialogSettings, ""},
		{"shortcuts", palActShortcuts, dialogShortcuts, ""},
		{"plugins", palActPlugins, dialogPlugins, ""},
		{"about", palActAbout, dialogAbout, ""},
		{"memory", palActMemory, dialogMemory, ""},
		{"new pane", palActNewPane, dialogCreatePane, ""},
		{"close pane", palActClosePane, dialogConfirm, "pane"},
		{"close tab", palActCloseTab, dialogConfirm, "tab"},
		{"restart pane", palActRestartPane, dialogConfirm, "restart-pane"},
		{"client log", palActClientLog, dialogLogViewer, ""},
		{"daemon log", palActDaemonLog, dialogLogViewer, ""},
		{"mcp log", palActMCPLog, dialogLogViewer, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newSplitDragTestModel(t)
			m.dialog = dialogCommandPalette
			updated, _ := m.executePaletteCommand(paletteCommand{action: tc.action, enabled: true})
			got := updated.(Model)
			if got.dialog != tc.wantDialog {
				t.Errorf("action %v: dialog = %v, want %v", tc.action, got.dialog, tc.wantDialog)
			}
			if tc.confirmKind != "" && got.confirmKind != tc.confirmKind {
				t.Errorf("action %v: confirmKind = %q, want %q", tc.action, got.confirmKind, tc.confirmKind)
			}
		})
	}
}

// TestPalette_DispatchNonDialogActions covers the remaining actions: each must
// close the palette (dialog != dialogCommandPalette) and not panic. The ones
// with observable non-dialog state are asserted specifically.
func TestPalette_DispatchNonDialogActions(t *testing.T) {
	t.Parallel()
	cmdOnly := []paletteAction{
		palActSplitH, palActSplitV, palActMute, palActEager, palActNewTab,
		palActCycleTabColor, palActSwitchTab, palActLazygit, palActRedraw,
	}
	for _, a := range cmdOnly {
		m := newSplitDragTestModel(t)
		m.dialog = dialogCommandPalette
		updated, _ := m.executePaletteCommand(paletteCommand{action: a, enabled: true})
		if updated.(Model).dialog == dialogCommandPalette {
			t.Errorf("action %v should close the palette", a)
		}
	}

	// Rename tab → inline rename state.
	m := newSplitDragTestModel(t)
	m.dialog = dialogCommandPalette
	if got := mustExec(t, m, palActRenameTab); !got.renaming {
		t.Error("rename tab should set m.renaming")
	}
	// Rename pane → inline pane-rename state.
	m = newSplitDragTestModel(t)
	m.dialog = dialogCommandPalette
	if got := mustExec(t, m, palActRenamePane); !got.renamingPane {
		t.Error("rename pane should set m.renamingPane")
	}
	// Focus → active tab enters focus mode.
	m = newSplitDragTestModel(t)
	m.dialog = dialogCommandPalette
	got := mustExec(t, m, palActFocus)
	if tab := got.activeTabModel(); tab == nil || !tab.FocusMode() {
		t.Error("focus action should toggle focus mode on the active tab")
	}
}

func mustExec(t *testing.T, m *Model, a paletteAction) Model {
	t.Helper()
	updated, _ := m.executePaletteCommand(paletteCommand{action: a, enabled: true})
	return updated.(Model)
}

// TestPalette_GoToPaneCrossTab verifies the load-bearing focus ordering when the
// target pane is on a DIFFERENT tab — the single-tab fixture cannot exercise it.
func TestPalette_GoToPaneCrossTab(t *testing.T) {
	t.Parallel()
	m := newModelForTest([]string{"A", "B"}, 0)
	m.notifications = NewNotificationCenter(30, 200)
	p1 := NewPaneModel("p1", 1024)
	p1.Active = true
	m.tabs[0].Root = NewLeaf(p1)
	m.tabs[0].ActivePane = "p1"
	p3 := NewPaneModel("p3", 1024)
	m.tabs[1].Root = NewLeaf(p3)
	m.tabs[1].ActivePane = "p3"
	m.width, m.height = 100, 40
	m.tabs[0].Resize(100, 38)
	m.tabs[1].Resize(100, 38)
	m.dialog = dialogCommandPalette

	updated, _ := m.executePaletteCommand(paletteCommand{action: palActGoToPane, arg: "p3", enabled: true})
	got := updated.(Model)
	if got.activeTab != 1 {
		t.Errorf("activeTab = %d, want 1 (target tab)", got.activeTab)
	}
	if got.tabs[1].ActivePane != "p3" {
		t.Errorf("target tab ActivePane = %q, want p3", got.tabs[1].ActivePane)
	}
	if !p3.Active {
		t.Error("target pane should be marked active")
	}
	if p1.Active {
		t.Error("previously-active pane on the old tab should be cleared")
	}
}

func TestPalette_ControlTextNotInjected(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	opened, _ := m.openCommandPalette()
	got := opened.(Model)
	// A key we do not handle whose Text carries a control char (e.g. tab → \t)
	// must not extend the query.
	updated, _ := got.handleCommandPaletteKey(tea.KeyPressMsg{Text: "\t"})
	if q := updated.(Model).palette.query; q != "" {
		t.Errorf("query = %q, want empty (control text rejected)", q)
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
