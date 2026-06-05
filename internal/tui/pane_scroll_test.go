package tui

import (
	"bytes"
	"testing"
)

// populateScrollback writes `lines` newline-terminated rows so that the
// VT emulator pushes (lines - innerH) of them into scrollback. Returns
// the actual ScrollbackLen() so tests don't have to second-guess the VT
// emulator's exact behavior.
func populateScrollback(t *testing.T, p *PaneModel, lines int) int {
	t.Helper()
	data := bytes.Repeat([]byte("line\r\n"), lines)
	p.AppendOutput(data)
	return p.vt.ScrollbackLen()
}

func TestPaneModel_ScrollToRelY_NoopBranches(t *testing.T) {
	t.Parallel()
	t.Run("empty scrollback is noop", func(t *testing.T) {
		t.Parallel()
		p := NewPaneModel("noop-sb", testRingBufSize)
		p.ResizeVT(80, 24)
		p.scrollBack = 0
		p.ScrollToRelY(5, 20)
		if p.scrollBack != 0 {
			t.Errorf("scrollBack = %d, want 0 (no scrollback to scroll into)", p.scrollBack)
		}
	})

	t.Run("innerH <= 0 is noop", func(t *testing.T) {
		t.Parallel()
		p := NewPaneModel("noop-h", testRingBufSize)
		p.ResizeVT(80, 24)
		populateScrollback(t, p, 100)
		before := p.scrollBack
		p.ScrollToRelY(0, 0)
		p.ScrollToRelY(5, -3)
		if p.scrollBack != before {
			t.Errorf("scrollBack changed from %d to %d on zero/negative innerH", before, p.scrollBack)
		}
	})
}

func TestPaneModel_ScrollToRelY_EndpointsMatchRenderInverse(t *testing.T) {
	t.Parallel()
	// Each case uses fresh pane + populated scrollback. The test asserts
	// the algebraic inverse of renderScrollback's thumb-position formula
	// holds at the endpoints — see CONTRACT in pane.go:ScrollToRelY doc.
	cases := []struct {
		name      string
		cols      int
		innerH    int
		feedLines int
	}{
		{"24x80 standard", 80, 24, 200},
		{"40x132 wide", 132, 40, 500},
		{"6x80 short pane", 80, 6, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := NewPaneModel("endpoints-"+tc.name, testRingBufSize)
			p.ResizeVT(tc.cols, tc.innerH)
			sbLen := populateScrollback(t, p, tc.feedLines)
			if sbLen <= 0 {
				t.Fatalf("test precondition: expected non-empty scrollback, got %d", sbLen)
			}

			// relY = 0 → thumb at the top of the track → viewing the
			// oldest scrollback row → scrollBack == sbLen.
			p.ScrollToRelY(0, tc.innerH)
			if p.scrollBack != sbLen {
				t.Errorf("relY=0: scrollBack = %d, want sbLen=%d", p.scrollBack, sbLen)
			}

			// Compute maxThumbPos and check the bottom endpoint maps to
			// scrollBack == 0 (live tail). Mirror the same thumbSize
			// math the implementation uses.
			totalLines := sbLen + tc.innerH
			thumbSize := tc.innerH * tc.innerH / totalLines
			if thumbSize < 1 {
				thumbSize = 1
			}
			maxThumbPos := tc.innerH - thumbSize
			if maxThumbPos <= 0 {
				t.Skip("no scrollable range in this layout")
			}
			p.ScrollToRelY(maxThumbPos, tc.innerH)
			if p.scrollBack != 0 {
				t.Errorf("relY=maxThumbPos (%d): scrollBack = %d, want 0 (live tail)", maxThumbPos, p.scrollBack)
			}
		})
	}
}

func TestPaneModel_ScrollToRelY_Clamping(t *testing.T) {
	t.Parallel()
	p := NewPaneModel("clamp", testRingBufSize)
	p.ResizeVT(80, 24)
	sbLen := populateScrollback(t, p, 200)
	if sbLen <= 0 {
		t.Fatalf("test precondition: expected non-empty scrollback, got %d", sbLen)
	}

	// Negative relY should behave like relY=0 (thumb at top → scrollBack=sbLen).
	p.ScrollToRelY(-100, 24)
	if p.scrollBack != sbLen {
		t.Errorf("negative relY: scrollBack = %d, want sbLen=%d", p.scrollBack, sbLen)
	}

	// Massive relY should behave like relY=maxThumbPos (thumb at bottom → scrollBack=0).
	p.ScrollToRelY(10_000, 24)
	if p.scrollBack != 0 {
		t.Errorf("excessive relY: scrollBack = %d, want 0 (live tail)", p.scrollBack)
	}
}

func TestPaneModel_ScrollToRelY_NoScrollableRangeIsNoop(t *testing.T) {
	t.Parallel()
	// When the visible area dominates the total content, maxThumbPos is
	// zero (or negative) and the function should leave scrollBack alone.
	p := NewPaneModel("no-range", testRingBufSize)
	p.ResizeVT(80, 24)
	// Write just a couple of lines — way less than innerH so scrollback
	// barely exists if at all. Set scrollBack to a sentinel and verify
	// it isn't touched.
	p.AppendOutput([]byte("a\r\nb\r\n"))
	p.scrollBack = 42
	p.ScrollToRelY(0, 24)
	if p.scrollBack != 42 {
		t.Errorf("scrollBack mutated to %d when no scrollable range; sentinel was 42", p.scrollBack)
	}
}
