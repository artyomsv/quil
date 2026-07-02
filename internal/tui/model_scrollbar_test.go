package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// twoPaneFocusModel builds a Model with a horizontally split two-pane tab
// (left | right), the LEFT pane active, at the given size. Returns the model
// and the two panes.
func twoPaneFocusModel(t *testing.T, width, height int) (Model, *PaneModel, *PaneModel) {
	t.Helper()
	left := NewPaneModel("left", 1024)
	right := NewPaneModel("right", 1024)
	tab := NewTabModel("t1", "Shell")
	tab.Root = NewLeaf(left)
	tab.ActivePane = left.ID
	ph := tab.SplitAtPane(left.ID, SplitHorizontal)
	if ph == nil {
		t.Fatal("SplitAtPane returned nil")
	}
	ph.Pane = right
	tab.invalidateLeaves()

	m := Model{
		width:         width,
		height:        height,
		notifications: NewNotificationCenter(30, 50),
		mcpHighlights: make(map[string]bool),
		tabs:          []*TabModel{tab},
		activeTab:     0,
		cfg:           config.Default(),
	}
	return m, left, right
}

// TestModel_HitTestScrollbar_FocusModeUsesActivePaneFullWidth is the regression
// guard for the focus-mode scrollbar bug: in focus mode the active pane fills
// the whole tab area, so its scrollbar lives at the far-right column. Before
// the fix, hitTestScrollbar walked the (stale) split tree and a click at the
// full-width scrollbar column landed on the RIGHT pane's split-geometry
// scrollbar instead of the active LEFT pane.
func TestModel_HitTestScrollbar_FocusModeUsesActivePaneFullWidth(t *testing.T) {
	t.Parallel()
	m, left, right := twoPaneFocusModel(t, 100, 30)
	m.tabs[0].ToggleFocus()
	if !m.tabs[0].FocusMode() {
		t.Fatal("ToggleFocus did not enter focus mode on a two-pane tab")
	}

	// Full-width scrollbar column = OX + W - 2 = 0 + 100 - 2 = 98; content row 2.
	rect := m.hitTestScrollbar(98, 2)
	if rect == nil {
		t.Fatal("hitTestScrollbar(98, 2) = nil in focus mode, want the active pane")
	}
	if rect.Pane != left {
		t.Errorf("hitTestScrollbar returned pane %q, want active pane %q", rect.Pane.ID, left.ID)
	}
	if rect.Pane == right {
		t.Error("hitTestScrollbar returned the non-active right pane (stale split geometry)")
	}
	if rect.W != 100 {
		t.Errorf("focus-mode rect width = %d, want 100 (full tab width)", rect.W)
	}
}

// TestModel_HitTestScrollbar_NotesModeSinglePaneUsesReducedWidth guards the
// single-pane notes-mode case. ToggleFocus is a no-op on a single-pane tab, so
// notes mode does NOT enter focus mode — yet the pane is still rendered at the
// reduced width (m.width - notesPanelWidth). hitTestScrollbar must locate the
// scrollbar at that reduced right edge, not at the full-width column.
func TestModel_HitTestScrollbar_NotesModeSinglePaneUsesReducedWidth(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	pane := NewPaneModel("solo", 1024)
	pane.Active = true
	tab := NewTabModel("t1", "Shell")
	tab.Root = NewLeaf(pane)
	tab.ActivePane = pane.ID
	m := Model{
		width:         100,
		height:        30,
		notifications: NewNotificationCenter(30, 50),
		mcpHighlights: make(map[string]bool),
		tabs:          []*TabModel{tab},
		activeTab:     0,
		cfg:           cfg,
	}

	updated, _ := m.toggleNotesMode()
	m = updated.(Model)
	if !m.notesMode {
		t.Fatal("notesMode not active after toggle")
	}
	if m.tabs[0].FocusMode() {
		t.Fatal("single-pane tab should NOT be in focus mode in notes mode")
	}
	// Force the same layout pass View() performs so pane.Width is the reduced
	// width before hit-testing.
	_ = m.View()

	notesW, sidebarW := m.notesPanelWidth()
	if notesW == 0 {
		t.Fatal("notes panel width is 0 — terminal too narrow for the test")
	}
	paneW := m.width - sidebarW - notesW
	scrollbarX := paneW - 2 // OX=0

	if rect := m.hitTestScrollbar(scrollbarX, 5); rect == nil {
		t.Errorf("hitTestScrollbar(%d, 5) = nil at the reduced-width scrollbar, want the pane", scrollbarX)
	}
	// The old (buggy) full-width column must NOT register — it's inside the
	// notes editor panel now.
	if rect := m.hitTestScrollbar(m.width-2, 5); rect != nil {
		t.Errorf("hitTestScrollbar(%d, 5) = %q, want nil (that column is the notes editor)", m.width-2, rect.Pane.ID)
	}
}

// TestModel_ActivePaneRect_SplitMode verifies activePaneRect resolves the
// active pane's rect from the layout tree when not in focus mode.
func TestModel_ActivePaneRect_SplitMode(t *testing.T) {
	t.Parallel()
	m, left, _ := twoPaneFocusModel(t, 100, 30)

	rect := m.activePaneRect()
	if rect == nil {
		t.Fatal("activePaneRect() = nil in split mode, want the active pane")
	}
	if rect.Pane != left {
		t.Errorf("activePaneRect returned %q, want active pane %q", rect.Pane.ID, left.ID)
	}
	// Left pane of a horizontal split occupies the left half, anchored at OX=0.
	if rect.OX != 0 {
		t.Errorf("active (left) pane OX = %d, want 0", rect.OX)
	}
	if rect.W >= 100 {
		t.Errorf("split-mode left pane W = %d, want < full width 100", rect.W)
	}
}
