package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestOverlayRight_PlainLines(t *testing.T) {
	base := "aaaaaaaaaa\nbbbbbbbbbb" // 10 wide
	overlay := "XXX\nYYY"            // 3 wide
	got := overlayRight(base, overlay, 10, 3)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want 2", len(lines))
	}
	if w := ansi.StringWidth(lines[0]); w != 10 {
		t.Errorf("line 0 width = %d, want 10", w)
	}
	if stripped := ansi.Strip(lines[0]); stripped != "aaaaaaaXXX" {
		t.Errorf("line 0 stripped = %q, want aaaaaaaXXX", stripped)
	}
	if stripped := ansi.Strip(lines[1]); stripped != "bbbbbbbYYY" {
		t.Errorf("line 1 stripped = %q, want bbbbbbbYYY", stripped)
	}
}

func TestOverlayRight_ANSIStyledBase_NoBleed(t *testing.T) {
	// Red-styled base line: the style must not bleed into the overlay text.
	base := "\x1b[31m" + strings.Repeat("r", 10) + "\x1b[0m"
	got := overlayRight(base, "XX", 10, 2)
	if !strings.Contains(got, "\x1b[0mXX") {
		t.Errorf("overlay not preceded by SGR reset: %q", got)
	}
	if stripped := ansi.Strip(got); stripped != "rrrrrrrrXX" {
		t.Errorf("stripped = %q, want rrrrrrrrXX", stripped)
	}
}

func TestOverlayRight_WideGlyphAtCut_PadsToWidth(t *testing.T) {
	// 4 CJK glyphs = 8 cells + 2 ASCII = 10 cells; cut at 7 leaves 3 glyphs
	// (6 cells) + 1 pad space.
	base := "你好世界xx"
	got := overlayRight(base, "ZZZ", 10, 3)
	if w := ansi.StringWidth(got); w != 10 {
		t.Errorf("width = %d, want 10", w)
	}
	if stripped := ansi.Strip(got); stripped != "你好世 ZZZ" {
		t.Errorf("stripped = %q, want %q", stripped, "你好世 ZZZ")
	}
}

func TestOverlayRight_ShortBaseLine_PaddedBeforeOverlay(t *testing.T) {
	got := overlayRight("ab", "XX", 10, 2)
	if stripped := ansi.Strip(got); stripped != "ab      XX" {
		t.Errorf("stripped = %q, want %q", stripped, "ab      XX")
	}
}

func TestOverlayRight_OverlayShorterThanBase_BlankFill(t *testing.T) {
	base := "aaaaaaaaaa\nbbbbbbbbbb\ncccccccccc"
	got := overlayRight(base, "XX", 10, 2)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("line count = %d, want 3", len(lines))
	}
	if stripped := ansi.Strip(lines[2]); stripped != "cccccccc  " {
		t.Errorf("line 2 stripped = %q, want blank-covered tail", stripped)
	}
}

func TestOverlayRight_DegenerateWidths_ReturnsBase(t *testing.T) {
	base := "aaaa"
	if got := overlayRight(base, "X", 4, 0); got != base {
		t.Errorf("overlayW=0: got %q, want base", got)
	}
	if got := overlayRight(base, "X", 4, 4); got != base {
		t.Errorf("overlayW=totalW: got %q, want base", got)
	}
}

func TestOverlayAt_MiddleOverwrite_PreservesTail(t *testing.T) {
	base := "aaaaaaaaaa\nbbbbbbbbbb\ncccccccccc"
	got := overlayAt(base, "XX\nYY", 3, 1, 10)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("line count = %d, want 3", len(lines))
	}
	if s := ansi.Strip(lines[0]); s != "aaaaaaaaaa" {
		t.Errorf("row 0 = %q, want untouched", s)
	}
	if s := ansi.Strip(lines[1]); s != "bbbXXbbbbb" {
		t.Errorf("row 1 = %q, want bbbXXbbbbb", s)
	}
	if s := ansi.Strip(lines[2]); s != "cccYYccccc" {
		t.Errorf("row 2 = %q, want cccYYccccc", s)
	}
}

func TestOverlayAt_SGRResetGuards(t *testing.T) {
	base := "\x1b[31mrrrrrrrrrr\x1b[0m"
	got := overlayAt(base, "XX", 4, 0, 10)
	// Reset must close the left segment before the box so red never bleeds in.
	if !strings.Contains(got, "\x1b[0mXX") {
		t.Errorf("box not preceded by SGR reset: %q", got)
	}
	if s := ansi.Strip(got); s != "rrrrXXrrrr" {
		t.Errorf("stripped = %q, want rrrrXXrrrr", s)
	}
}

func TestOverlayAt_WideGlyphAtLeftCut_KeepsTotalWidth(t *testing.T) {
	// 你(0-1)好(2-3)世(4-5)界(6-7)xx(8-9). Cut at x=3 straddles 好.
	base := "你好世界xx"
	got := overlayAt(base, "ZZ", 3, 0, 10)
	if w := ansi.StringWidth(got); w != 10 {
		t.Errorf("width = %d, want 10", w)
	}
	if s := ansi.Strip(got); !strings.HasPrefix(s, "你") || !strings.Contains(s, "ZZ") {
		t.Errorf("stripped = %q, want 你-prefix with ZZ box", s)
	}
}

func TestOverlayAt_BoxRowsBeyondBase_Dropped(t *testing.T) {
	got := overlayAt("aaaaaaaaaa", "XX\nYY\nZZ", 2, 0, 10)
	if n := len(strings.Split(got, "\n")); n != 1 {
		t.Errorf("line count = %d, want 1 (extra box rows dropped)", n)
	}
}

func TestOverlayAt_Degenerate_ReturnsBase(t *testing.T) {
	base := "aaaa\nbbbb"
	for name, tc := range map[string]struct{ x, y, w int }{
		"negative x":       {-1, 0, 4},
		"negative y":       {0, -1, 4},
		"box exceeds base": {3, 0, 4}, // 2-wide box at x=3 needs 5 cells
	} {
		if got := overlayAt(base, "XX", tc.x, tc.y, tc.w); got != base {
			t.Errorf("%s: got %q, want base unchanged", name, got)
		}
	}
}

func TestOverlayAt_WideGlyphAtRightCut_KeepsTotalWidth(t *testing.T) {
	// All-double-width base; the box's right edge (x=1+2=3) lands mid-好.
	base := "你好你好你"
	got := overlayAt(base, "ZZ", 1, 0, 10)
	if w := ansi.StringWidth(got); w != 10 {
		t.Errorf("width = %d, want 10", w)
	}
	got = overlayAt(base, "Z", 2, 0, 10)
	if w := ansi.StringWidth(got); w != 10 {
		t.Errorf("single-cell box: width = %d, want 10", w)
	}
}
