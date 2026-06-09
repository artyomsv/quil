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

// TestBuildTopBorder_SmallWidths exercises the degenerate-width paths: the
// innerW<1 early return and a too-narrow pane where the CWD cannot fit. These
// must not panic or produce negative-width repeats.
func TestBuildTopBorder_SmallWidths(t *testing.T) {
	t.Parallel()
	c := lipgloss.Color("238")
	cases := []struct {
		name    string
		width   int
		cwd     string
		working bool
	}{
		{"width 2 — innerW<1 early return", 2, "/home/user/proj", true},
		{"width 1", 1, "", false},
		{"width 0", 0, "", true},
		{"narrow + long cwd + working", 10, "/home/user/very/long/path", true},
		{"narrow + long cwd no work", 8, "/home/user/very/long/path", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Must not panic; lipgloss.Width must stay within the requested
			// width (a negative dash count would blow this up).
			got := buildTopBorder(tc.width, tc.cwd, "claude", c,
				false, false, false, false, 0, tc.working, 0)
			if got == "" && tc.width > 0 {
				t.Errorf("width=%d produced empty border", tc.width)
			}
		})
	}
}
