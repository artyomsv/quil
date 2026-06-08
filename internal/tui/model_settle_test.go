package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// A child process reflows its UI right after the daemon's spawn-time resize
// kick lands. When the host terminal disagrees with the renderer about glyph
// widths (Claude Code's logo on Windows fonts), that redraw leaves stale
// cells behind that only a full repaint clears. The TUI schedules settle
// repaints automatically on a pane's FIRST live output — the same recovery
// as the manual redraw key, without the keypress.

func TestHandlePaneOutput_FirstLiveOutput_SchedulesSettleRepaint(t *testing.T) {
	m, _ := cursorTestModel("claude-code")

	cmd := m.handlePaneOutput(PaneOutputMsg{PaneID: "p1", Data: []byte("hello")})
	if cmd == nil {
		t.Fatal("first live output must schedule settle repaints")
	}

	// Subsequent output must NOT reschedule — one settle window per pane.
	cmd = m.handlePaneOutput(PaneOutputMsg{PaneID: "p1", Data: []byte("more")})
	if cmd != nil {
		t.Error("second live output scheduled another settle repaint")
	}
}

func TestHandlePaneOutput_GhostOutput_DoesNotScheduleRepaint(t *testing.T) {
	m, _ := cursorTestModel("claude-code")
	m.cfg.GhostBuffer.Dimmed = true

	cmd := m.handlePaneOutput(PaneOutputMsg{PaneID: "p1", Data: []byte("replay"), Ghost: true})
	if cmd != nil {
		t.Error("ghost replay scheduled a settle repaint — only live output should")
	}
}

func TestUpdate_PaneSettleRepaint_EmitsClearScreen(t *testing.T) {
	m, _ := cursorTestModel("claude-code")

	_, cmd := m.Update(paneSettleRepaintMsg{})
	if cmd == nil {
		t.Fatal("paneSettleRepaintMsg produced no command")
	}
	if got, want := cmd(), tea.ClearScreen(); got != want {
		t.Errorf("got %T, want tea.ClearScreen message", got)
	}
}
