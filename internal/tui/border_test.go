package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestBuildTopBorder_WorkingShowsLeftSpinner(t *testing.T) {
	t.Parallel()
	c := lipgloss.Color("238")

	// working=false → no spinner glyph anywhere.
	idle := buildTopBorder(40, "/home/user/proj", "claude", c,
		false, false, false, false, 0 /*spinnerFrame*/, false /*working*/, 0 /*workFrame*/)
	if strings.ContainsAny(idle, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Errorf("idle border %q should not contain a spinner", idle)
	}

	// working=true, frame 0 → leading spinner ⠋ present, CWD still shown.
	busy := buildTopBorder(40, "/home/user/proj", "claude", c,
		false, false, false, false, 0, true, 0)
	if !strings.Contains(busy, "⠋") {
		t.Errorf("working border %q should contain frame ⠋", busy)
	}
	if !strings.Contains(busy, "proj") {
		t.Errorf("working border %q should still show the CWD", busy)
	}
}

func TestBuildTopBorder_WorkingNoCWD(t *testing.T) {
	t.Parallel()
	c := lipgloss.Color("238")
	busy := buildTopBorder(40, "", "claude", c,
		false, false, false, false, 0, true, 0)
	if !strings.Contains(busy, "⠋") {
		t.Errorf("working border with empty CWD %q should still show the spinner", busy)
	}
}
