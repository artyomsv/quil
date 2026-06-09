package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestBuildTopBorder_Focus_LabelVisibilityByWidth(t *testing.T) {
	t.Parallel()
	color := lipgloss.Color("57")

	tests := []struct {
		name      string
		width     int
		focus     bool
		wantLabel bool
	}{
		{"focus wide", 60, true, true},
		{"focus narrow fits", 20, true, true},
		{"focus too narrow", 12, true, false},
		{"no focus wide", 60, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildTopBorder(tt.width, "", "", color, false, false, false, tt.focus, 0, false, 0)
			hasLabel := strings.Contains(result, "* FOCUS *")
			if hasLabel != tt.wantLabel {
				t.Errorf("width=%d focus=%v: hasLabel=%v, want %v", tt.width, tt.focus, hasLabel, tt.wantLabel)
			}
		})
	}
}

func TestBuildTopBorder_Focus_CentersLabel(t *testing.T) {
	t.Parallel()
	color := lipgloss.Color("57")
	result := buildTopBorder(40, "", "", color, false, false, false, true, 0, false, 0)

	// Find the focus label position — dashes should be roughly equal on both sides
	idx := strings.Index(result, "* FOCUS *")
	if idx < 0 {
		t.Fatal("focus label not found")
	}

	// The label should be roughly centered (within 1 char tolerance due to odd widths)
	before := result[:idx]
	after := result[idx+len("* FOCUS *"):]
	// Count only border characters (─), not corner chars
	leftDashes := strings.Count(before, "─")
	rightDashes := strings.Count(after, "─")
	diff := leftDashes - rightDashes
	if diff < -1 || diff > 1 {
		t.Errorf("label not centered: left dashes=%d, right dashes=%d", leftDashes, rightDashes)
	}
}
