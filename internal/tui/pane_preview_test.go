package tui

import (
	"github.com/charmbracelet/x/ansi"
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

func TestRenderPreview_BottomAnchoredAndWrapped(t *testing.T) {
	p := canvasPane(t, 100, 6, strings.Repeat("w", 95)+"\r\nprompt> ")
	defer p.Dispose()
	// innerH 10 > 8 visual rows (3 wrapped + prompt + 4 blank screen rows),
	// so the whole wrapped content is in view; bottom-anchoring itself is
	// covered by TestRenderPreview_ScrolledShowsScrollbarAndHistory.
	p.Width, p.Height = 42, 12 // innerW 40, innerH 10
	out := ansi.Strip(p.renderPreview())
	lines := strings.Split(out, "\n")
	if len(lines) != 10 {
		t.Fatalf("preview height %d, want 10", len(lines))
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "prompt>") {
		t.Errorf("live preview must show the screen tail, got:\n%s", joined)
	}
	if !strings.Contains(joined, strings.Repeat("w", 40)) {
		t.Errorf("wide row must appear wrapped at 40 cols, got:\n%s", joined)
	}
}

func TestRenderPreview_LinesFitInnerWidth(t *testing.T) {
	p := canvasPane(t, 120, 6, strings.Repeat("你x", 30)+"\r\n")
	defer p.Dispose()
	p.Width, p.Height = 42, 8
	for i, line := range strings.Split(p.renderPreview(), "\n") {
		if w := ansi.StringWidth(line); w > 40 {
			t.Errorf("line %d width %d exceeds innerW 40", i, w)
		}
	}
}

func TestRenderPreview_CursorReverseVideo(t *testing.T) {
	p := canvasPane(t, 100, 6, "prompt> ")
	defer p.Dispose()
	p.Width, p.Height = 42, 8
	p.Active = true
	out := p.renderPreview()
	if !strings.Contains(out, "\x1b[7m") {
		t.Error("active pane preview must render the caret in reverse video")
	}
	p.Active = false
	if strings.Contains(p.renderPreview(), "\x1b[7m") {
		t.Error("inactive pane preview must not render a caret")
	}
}

func TestRenderPreview_ScrolledShowsScrollbarAndHistory(t *testing.T) {
	var feed strings.Builder
	for i := 0; i < 30; i++ {
		feed.WriteString(strings.Repeat("h", 95) + "\r\n")
	}
	p := canvasPane(t, 100, 6, feed.String())
	defer p.Dispose()
	p.Width, p.Height = 42, 8
	l := p.previewLayoutFor(40)
	p.scrollBack = l.totalVisual() - 6 // scroll to the very top
	out := p.renderPreview()
	if !strings.Contains(out, "█") {
		t.Error("scrolled preview must render the scrollbar thumb")
	}
	for i, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w > 40 {
			t.Errorf("scrolled line %d width %d exceeds innerW 40", i, w)
		}
	}
}

func TestPreviewScroll_ClampsToVisualRows(t *testing.T) {
	var feed strings.Builder
	for i := 0; i < 20; i++ {
		feed.WriteString(strings.Repeat("z", 95) + "\r\n")
	}
	p := canvasPane(t, 100, 6, feed.String())
	defer p.Dispose()
	p.Width, p.Height = 42, 8 // innerW 40 → each content row = 3 visual rows
	l := p.previewLayoutFor(40)
	maxScroll := l.totalVisual() - 6
	p.ScrollUp(1000000)
	if p.scrollBack != maxScroll {
		t.Errorf("scrollBack clamped to %d, want %d (visual rows; emulator scrollback is %d)",
			p.scrollBack, maxScroll, p.vt.ScrollbackLen())
	}
	p.ScrollDown(1000000)
	if p.scrollBack != 0 {
		t.Errorf("ScrollDown floor: %d, want 0", p.scrollBack)
	}
}

func TestPreviewScrollToRelY_TopAndBottom(t *testing.T) {
	var feed strings.Builder
	for i := 0; i < 30; i++ {
		feed.WriteString(strings.Repeat("y", 95) + "\r\n")
	}
	p := canvasPane(t, 100, 6, feed.String())
	defer p.Dispose()
	p.Width, p.Height = 42, 8
	innerH := 6
	p.ScrollToRelY(0, innerH)
	if p.scrollBack != p.maxScroll() {
		t.Errorf("thumb at top: scrollBack %d, want max %d", p.scrollBack, p.maxScroll())
	}
	l := p.previewLayoutFor(40)
	thumbSize := max(1, innerH*innerH/l.totalVisual())
	p.ScrollToRelY(innerH-thumbSize, innerH)
	if p.scrollBack != 0 {
		t.Errorf("thumb at bottom: scrollBack %d, want 0", p.scrollBack)
	}
}
