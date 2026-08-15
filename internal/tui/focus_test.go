package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/plugin"
)

func TestFocusMsg_TracksTerminalFocus(t *testing.T) {
	m := newTestModelWithTabs(t, 1, 1)
	m.termFocused = true

	updated, _ := m.Update(tea.BlurMsg{})
	m = updated.(Model)
	if m.termFocused {
		t.Error("BlurMsg must clear termFocused")
	}
	if !m.focusEverReported {
		t.Error("BlurMsg must record that focus reporting works")
	}

	updated, _ = m.Update(tea.FocusMsg{})
	m = updated.(Model)
	if !m.termFocused {
		t.Error("FocusMsg must set termFocused")
	}
}

// A terminal without DEC 1004 support never sends either message, so
// termFocused keeps its initial value forever. Initialising it TRUE means the
// feature silently does nothing there rather than storming the user with
// toasts while they look straight at the screen. focusEverReported is what
// makes that state diagnosable instead of mysterious.
// Asserted against the real constructor rather than a package fixture: the
// fixtures build Model literals, so they would report the zero value and prove
// nothing about what a launched TUI actually starts with.
func TestNewModel_AssumesFocusedUntilProvenOtherwise(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := NewModel(newFakeConn(), config.Default(), "test", plugin.NewRegistry(), nil)
	if !m.termFocused {
		t.Error("termFocused must start true so an unsupporting terminal fails quiet, not loud")
	}
	if m.focusEverReported {
		t.Error("focusEverReported must start false — nothing has reported yet")
	}
}

func TestView_EnablesFocusReporting(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newSplitDragTestModel(t)
	if !m.View().ReportFocus {
		t.Error("View must set ReportFocus or no focus event ever arrives")
	}
}
