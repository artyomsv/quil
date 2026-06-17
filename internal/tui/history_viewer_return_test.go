package tui

import (
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
)

// TestReadonlyText_EscReturnsToHistoryList verifies that closing a history
// entry's full-text view returns to the history list — not the About menu,
// which is the log viewer's default parent (the viewer is shared via
// openReadonlyText).
func TestReadonlyText_EscReturnsToHistoryList(t *testing.T) {
	m := Model{cfg: config.Default(), height: 24, width: 80}
	mdl, _ := m.openReadonlyText("Input @ 2026", "the full prompt body")
	m = mdl.(Model)
	if m.dialog != dialogLogViewer {
		t.Fatalf("openReadonlyText should enter dialogLogViewer, got %v", m.dialog)
	}

	out, _ := m.handleLogViewerKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	got := out.(Model)
	if got.dialog != dialogCommandHistory {
		t.Fatalf("Esc from history full-text should return to the history list (dialogCommandHistory), got %v", got.dialog)
	}
}

// TestLogViewer_EscReturnsToAbout is the regression guard: the ordinary log
// viewer (opened from the About menu) still returns to the About menu on Esc.
func TestLogViewer_EscReturnsToAbout(t *testing.T) {
	m := Model{cfg: config.Default(), height: 24, width: 80}
	mdl, _ := m.openLogViewer("daemon log", filepath.Join(t.TempDir(), "missing.log"))
	m = mdl.(Model)
	if m.dialog != dialogLogViewer {
		t.Fatalf("openLogViewer should enter dialogLogViewer, got %v", m.dialog)
	}

	out, _ := m.handleLogViewerKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	got := out.(Model)
	if got.dialog != dialogAbout {
		t.Fatalf("Esc from the log viewer should return to the About menu, got %v", got.dialog)
	}
}
