package tui

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestFirstNonEmptyLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"single line", "hello", "hello"},
		{"multi line, first non-empty", "alpha\nbeta\n", "alpha"},
		{"leading blanks", "\n\n  \nactual", "actual"},
		{"all blank", "\n\n\n", ""},
		{"empty", "", ""},
		{"whitespace trimmed", "   hi   \n", "hi"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstNonEmptyLine(tt.in); got != tt.want {
				t.Errorf("firstNonEmptyLine(%q): got %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNotificationCenter_View_RendersExcerpt(t *testing.T) {
	t.Parallel()
	nc := NewNotificationCenter(40, 50)
	nc.visible = true
	nc.AddEvent(ipc.PaneEventPayload{
		ID:       "evt-1",
		PaneID:   "pane-1",
		PaneName: "build",
		Type:     "process_exit",
		Title:    "Process failed (code 1)",
		Message:  "Error: missing semicolon at line 42",
		Severity: "error",
	})

	rendered := nc.View(20)
	if !strings.Contains(rendered, "Error: missing semicolon") {
		t.Errorf("View did not render excerpt; output:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Process failed") {
		t.Errorf("View did not render title; output:\n%s", rendered)
	}
}

func TestNotificationCenter_View_NoExcerptStillRendersTitle(t *testing.T) {
	t.Parallel()
	// Events without Message (legacy or empty-excerpt events) must still
	// render the title — the excerpt slot is just left blank.
	nc := NewNotificationCenter(40, 50)
	nc.visible = true
	nc.AddEvent(ipc.PaneEventPayload{
		ID:       "evt-1",
		PaneID:   "pane-1",
		PaneName: "shell",
		Type:     "bell",
		Title:    "Attention",
		Severity: "warning",
	})

	rendered := nc.View(20)
	if !strings.Contains(rendered, "Attention") {
		t.Errorf("View did not render title; output:\n%s", rendered)
	}
}

func TestNotificationCenter_View_ExcerptShowsFirstLineOnly(t *testing.T) {
	t.Parallel()
	// A multi-line Message should collapse to its first non-empty line in
	// the per-event sidebar card. Full text is still available via the
	// daemon's Data["excerpt"] for MCP consumers.
	nc := NewNotificationCenter(60, 50)
	nc.visible = true
	nc.AddEvent(ipc.PaneEventPayload{
		ID:       "evt-1",
		PaneID:   "pane-1",
		PaneName: "ai",
		Type:     "output_idle",
		Title:    "Waiting for input",
		Message:  "first context line\nsecond line\nthird line",
		Severity: "warning",
	})

	rendered := nc.View(20)
	if !strings.Contains(rendered, "first context line") {
		t.Errorf("View should contain first line; output:\n%s", rendered)
	}
	if strings.Contains(rendered, "second line") || strings.Contains(rendered, "third line") {
		t.Errorf("View should not contain later excerpt lines; output:\n%s", rendered)
	}
}
