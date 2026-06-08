package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
)

// Force-redraw recovers from accumulated cell-diff drift (width
// disagreements between the renderer and the host terminal scramble
// characters until something forces a full repaint — frequent on Windows).

func TestHandleKey_Redraw_EmitsClearScreen(t *testing.T) {
	m := Model{
		cfg:           config.Default(),
		notifications: NewNotificationCenter(30, 50),
	}
	m.cfg.Keybindings.Redraw = "f9"

	_, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyF9})
	if cmd == nil {
		t.Fatal("redraw key produced no command")
	}

	// The redraw key must BOTH repaint and re-query the window size — a
	// missed WindowSizeMsg (maximize/restore on Windows) leaves the layout
	// model stale, and ClearScreen alone repaints the same wrong frame.
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("redraw key produced %T, want tea.BatchMsg", cmd())
	}
	var haveClear, haveWinSize bool
	for _, c := range batch {
		switch msg := c(); msg {
		case tea.ClearScreen():
			haveClear = true
		case tea.RequestWindowSize():
			haveWinSize = true
		default:
			t.Errorf("unexpected message in redraw batch: %T", msg)
		}
	}
	if !haveClear {
		t.Error("redraw batch missing ClearScreen")
	}
	if !haveWinSize {
		t.Error("redraw batch missing RequestWindowSize")
	}
}

func TestDefaultConfig_RedrawBound(t *testing.T) {
	if config.Default().Keybindings.Redraw == "" {
		t.Error("redraw keybinding must ship with a default")
	}
}
