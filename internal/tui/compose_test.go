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
