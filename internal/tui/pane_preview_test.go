package tui

import (
	"strings"
	"testing"
)

// canvasPane builds a wide-canvas pane whose emulator is cols×rows with
// feed already written (feed goes straight to the emulator — no child
// process, so rows are exactly the fed lines).
func canvasPane(t *testing.T, cols, rows int, feed string) *PaneModel {
	t.Helper()
	p := NewPaneModel("pv", 4096)
	p.Type = "claude-code"
	p.WideCanvas = true
	p.ResizeVT(cols, rows)
	if feed != "" {
		p.AppendOutput([]byte(feed))
	}
	return p
}

func TestPreviewLayout_WrapCounts(t *testing.T) {
	p := canvasPane(t, 100, 8, strings.Repeat("a", 95)+"\r\nshort\r\n\r\n")
	defer p.Dispose()
	p.Width = 42 // innerW 40
	l := p.previewLayoutFor(40)
	if got := len(l.segs[0]); got != 3 {
		t.Errorf("95-wide row at innerW 40: %d segs, want 3", got)
	}
	if got := len(l.segs[1]); got != 1 {
		t.Errorf("short row: %d segs, want 1", got)
	}
	if got := len(l.segs[2]); got != 1 {
		t.Errorf("blank row: %d segs, want 1", got)
	}
	if l.segs[0][2].start != 80 || l.segs[0][2].end != 95 {
		t.Errorf("third seg = [%d,%d), want [80,95)", l.segs[0][2].start, l.segs[0][2].end)
	}
	// prefix sums must be monotone and consistent with segment counts.
	for i := 1; i < len(l.prefix); i++ {
		if l.prefix[i] != l.prefix[i-1]+len(l.segs[i-1]) {
			t.Fatalf("prefix[%d]=%d inconsistent with segs", i, l.prefix[i])
		}
	}
}

func TestPreviewLayout_WideGlyphBoundary(t *testing.T) {
	// "x" + 20 CJK glyphs: lead of glyph 20 sits at col 39, its continuation
	// at col 40 — the innerW-40 boundary must retreat to 39 so the glyph
	// stays whole.
	p := canvasPane(t, 100, 6, "x"+strings.Repeat("你", 20)+"\r\n")
	defer p.Dispose()
	l := p.previewLayoutFor(40)
	first := l.segs[0][0]
	if first.end != 39 {
		t.Errorf("segment boundary %d, want 39 (wide glyph must not straddle)", first.end)
	}
	if len(l.segs[0]) < 2 || l.segs[0][1].start != 39 {
		t.Errorf("second segment must start at 39, got %+v", l.segs[0])
	}
}

func TestPreviewLayout_CacheInvalidation(t *testing.T) {
	p := canvasPane(t, 100, 6, "hello\r\n")
	defer p.Dispose()
	l1 := p.previewLayoutFor(40)
	if p.previewLayoutFor(40) != l1 {
		t.Error("unchanged (contentGen, innerW) must reuse the cached layout")
	}
	p.AppendOutput([]byte("more\r\n"))
	if p.previewLayoutFor(40) == l1 {
		t.Error("AppendOutput must invalidate the preview layout cache")
	}
	l30 := p.previewLayoutFor(30)
	if l30.innerW != 30 {
		t.Errorf("layout innerW = %d, want 30", l30.innerW)
	}
}

func TestPreviewMode_Predicate(t *testing.T) {
	p := canvasPane(t, 100, 6, "")
	defer p.Dispose()
	p.Width = 42
	if !p.previewMode() {
		t.Error("narrow rect on canvas pane must be preview mode")
	}
	p.Width = 102 // innerW 100 == vt width → native
	if p.previewMode() {
		t.Error("rect matching canvas must render natively")
	}
	p.WideCanvas = false
	p.Width = 42
	if p.previewMode() {
		t.Error("non-canvas pane must never be preview mode")
	}
}
