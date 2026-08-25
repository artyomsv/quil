package tui

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The dim pass is wired at the frame seam, so these exercise it through
// Update + View rather than by calling dimFrame: a correct dimFrame that
// nothing calls, or that is gated on the wrong flag, passes every test in
// dim_test.go.

func TestView_DimsFrameWhileTerminalUnfocused(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newSplitDragTestModel(t)
	m.cfg.UI.UnfocusedDim = 0.5
	m.termFocused = true

	focused := m.View().Content

	updated, _ := m.Update(tea.BlurMsg{})
	blurred := updated.(Model)
	dimmed := blurred.View().Content

	if dimmed == focused {
		t.Fatal("BlurMsg must change the rendered frame — nothing dims otherwise")
	}
	// The whole promise of this pass: colors change, the picture does not.
	if got, want := stripANSI(dimmed), stripANSI(focused); got != want {
		t.Errorf("dimming altered frame text\n got %q\nwant %q", got, want)
	}
}

func TestView_RestoresUndimmedFrameOnFocus(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newSplitDragTestModel(t)
	m.cfg.UI.UnfocusedDim = 0.5
	m.termFocused = true

	focused := m.View().Content

	updated, _ := m.Update(tea.BlurMsg{})
	updated, _ = updated.(Model).Update(tea.FocusMsg{})

	if got := updated.(Model).View().Content; got != focused {
		t.Error("regaining focus must restore the frame byte-for-byte")
	}
}

func TestView_DoesNotDimWhenUnfocusedDimIsZero(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newSplitDragTestModel(t)
	m.cfg.UI.UnfocusedDim = 0
	m.termFocused = true

	focused := m.View().Content

	updated, _ := m.Update(tea.BlurMsg{})
	if got := updated.(Model).View().Content; got != focused {
		t.Error("unfocused_dim = 0 must leave the frame untouched")
	}
}

func TestView_DimsTowardTheTerminalReportedBackground(t *testing.T) {
	// The terminal answers OSC 10/11 with its real default colors. Blending
	// toward a remembered background rather than an assumed one is the whole
	// reason for asking, so a reported background must actually reach the
	// blend: against a white background, dimming makes colors LIGHTER.
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newSplitDragTestModel(t)
	m.cfg.UI.UnfocusedDim = 0.5
	m.termFocused = true

	updated, _ := m.Update(tea.BackgroundColorMsg{Color: color.White})
	updated, _ = updated.(Model).Update(tea.BlurMsg{})
	onWhite := updated.(Model).View().Content

	updated, _ = m.Update(tea.BlurMsg{})
	onDefault := updated.(Model).View().Content

	if onWhite == onDefault {
		t.Error("a reported background must change the blend result")
	}
	if !strings.Contains(onWhite, "\x1b[38;2;") {
		t.Error("dimmed frame must carry truecolor foregrounds")
	}
}
