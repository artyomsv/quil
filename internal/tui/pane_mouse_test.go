package tui

import (
	"bytes"
	"testing"
)

// TestPaneModel_MouseTracking_ReflectsModeSequences feeds the DEC mouse-mode
// set/reset escape sequences through the real VT emulator (the path used by
// live PTY output) and verifies MouseTracking() tracks them. This is the
// signal the wheel handler uses to decide whether to forward the event to the
// child app (opencode, claude-code, vim, …) instead of scrolling Quil's own
// scrollback.
func TestPaneModel_MouseTracking_ReflectsModeSequences(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		seq  string
	}{
		{"x10 ?9", "\x1b[?9h"},
		{"normal ?1000", "\x1b[?1000h"},
		{"button-event ?1002", "\x1b[?1002h"},
		{"any-event ?1003", "\x1b[?1003h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := NewPaneModel("mt-"+tt.name, testRingBufSize)
			if p.MouseTracking() {
				t.Fatal("MouseTracking() = true before any mode set, want false")
			}
			p.AppendOutput([]byte(tt.seq))
			if !p.MouseTracking() {
				t.Errorf("MouseTracking() = false after %q, want true", tt.seq)
			}
		})
	}
}

// TestPaneModel_MouseTracking_DisableOfUnsetModeKeepsTracking guards the
// per-mode bool design: an app may enable ?1002 then later reset ?1000 (a mode
// it never set). A single shared bool would wrongly clear tracking; per-mode
// bools keep ?1002 active.
func TestPaneModel_MouseTracking_DisableOfUnsetModeKeepsTracking(t *testing.T) {
	t.Parallel()
	p := NewPaneModel("mt-disable", testRingBufSize)
	p.AppendOutput([]byte("\x1b[?1002h")) // enable button-event tracking
	p.AppendOutput([]byte("\x1b[?1000l")) // reset normal tracking (never set)
	if !p.MouseTracking() {
		t.Error("MouseTracking() = false after resetting an unset mode, want true (?1002 still active)")
	}
	p.AppendOutput([]byte("\x1b[?1002l")) // reset the mode that was set
	if p.MouseTracking() {
		t.Error("MouseTracking() = true after resetting ?1002, want false")
	}
}

// TestPaneModel_WheelForwardSeq_NoTrackingReturnsNil ensures a plain terminal
// pane (no mouse tracking) never forwards — the wheel handler must fall through
// to Quil's local scrollback for those.
func TestPaneModel_WheelForwardSeq_NoTrackingReturnsNil(t *testing.T) {
	t.Parallel()
	p := NewPaneModel("wf-none", testRingBufSize)
	if seq := p.wheelForwardSeq(true, 0, 0); seq != nil {
		t.Errorf("wheelForwardSeq with no tracking = %q, want nil", seq)
	}
}

// TestPaneModel_WheelForwardSeq_SGRAndX10 verifies the encoded sequence picks
// SGR when ?1006 is set and legacy X10 otherwise, with wheel-up/down button
// codes and 1-based clamped coordinates.
func TestPaneModel_WheelForwardSeq_SGRAndX10(t *testing.T) {
	t.Parallel()

	t.Run("sgr wheel up", func(t *testing.T) {
		t.Parallel()
		p := NewPaneModel("wf-sgr", testRingBufSize)
		p.ResizeVT(80, 24)
		p.AppendOutput([]byte("\x1b[?1000h\x1b[?1006h")) // normal tracking + SGR
		got := p.wheelForwardSeq(true, 4, 9)
		// Wheel up button = 64; SGR is 1-based: col 5, row 10; press = 'M'.
		want := []byte("\x1b[<64;5;10M")
		if !bytes.Equal(got, want) {
			t.Errorf("wheelForwardSeq = %q, want %q", got, want)
		}
	})

	t.Run("sgr wheel down", func(t *testing.T) {
		t.Parallel()
		p := NewPaneModel("wf-sgr-down", testRingBufSize)
		p.ResizeVT(80, 24)
		p.AppendOutput([]byte("\x1b[?1000h\x1b[?1006h"))
		got := p.wheelForwardSeq(false, 0, 0)
		want := []byte("\x1b[<65;1;1M") // wheel down = 65
		if !bytes.Equal(got, want) {
			t.Errorf("wheelForwardSeq = %q, want %q", got, want)
		}
	})

	t.Run("x10 fallback when no sgr", func(t *testing.T) {
		t.Parallel()
		p := NewPaneModel("wf-x10", testRingBufSize)
		p.ResizeVT(80, 24)
		p.AppendOutput([]byte("\x1b[?1000h")) // tracking but no SGR
		got := p.wheelForwardSeq(true, 4, 9)
		// X10 encodes button 64 -> 32+64 = 96 = ' '+'@' ... assert it is a valid
		// CSI M sequence and does NOT use the SGR '<' form.
		if !bytes.HasPrefix(got, []byte("\x1b[M")) {
			t.Errorf("wheelForwardSeq = %q, want X10 CSI M prefix", got)
		}
	})
}

// TestPaneModel_WheelForwardSeq_ClampsToGrid verifies out-of-range coordinates
// (cursor outside the pane content) clamp into the emulator grid rather than
// producing negative or oversized SGR coordinates.
func TestPaneModel_WheelForwardSeq_ClampsToGrid(t *testing.T) {
	t.Parallel()
	p := NewPaneModel("wf-clamp", testRingBufSize)
	p.ResizeVT(80, 24)
	p.AppendOutput([]byte("\x1b[?1000h\x1b[?1006h"))

	// Negative coords clamp to (0,0) -> SGR 1;1.
	if got, want := p.wheelForwardSeq(true, -5, -3), []byte("\x1b[<64;1;1M"); !bytes.Equal(got, want) {
		t.Errorf("negative clamp = %q, want %q", got, want)
	}
	// Past the grid clamps to width-1/height-1 -> SGR 80;24.
	if got, want := p.wheelForwardSeq(true, 999, 999), []byte("\x1b[<64;80;24M"); !bytes.Equal(got, want) {
		t.Errorf("oversize clamp = %q, want %q", got, want)
	}
}
