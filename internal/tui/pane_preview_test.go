package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/artyomsv/quil/internal/config"
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
	p.previewWrap = true
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
	p.previewWrap = true
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
	// Flipping the wrap mode must also invalidate the cache.
	before := p.previewLayoutFor(30)
	p.previewWrap = true
	after := p.previewLayoutFor(30)
	if after == before {
		t.Error("wrap-mode flip must rebuild the preview layout")
	}
	if !after.wrap {
		t.Error("rebuilt layout must carry the new wrap mode")
	}
}

// Crop is the DEFAULT preview mode (soft wrap is opt-in via toggle_wrap):
// exactly one visual row per absolute row, truncated at innerW.
func TestPreviewLayout_CropDefault_SingleSegmentPerRow(t *testing.T) {
	p := canvasPane(t, 100, 8, strings.Repeat("a", 95)+"\r\nshort\r\n")
	defer p.Dispose()
	if p.previewWrap {
		t.Fatal("previewWrap must default to false (crop)")
	}
	l := p.previewLayoutFor(40)
	for row, segs := range l.segs {
		if len(segs) != 1 {
			t.Fatalf("crop mode: row %d has %d segments, want 1", row, len(segs))
		}
	}
	if got := l.segs[0][0]; got.start != 0 || got.end != 40 {
		t.Errorf("95-wide row cropped to [%d,%d), want [0,40)", got.start, got.end)
	}
	if got := l.segs[1][0]; got.end != 5 {
		t.Errorf("short row segment end = %d, want 5", got.end)
	}
	// One visual row per absolute row — scroll space matches emulator lines.
	if l.totalVisual() != len(l.segs) {
		t.Errorf("crop totalVisual = %d, want %d (1:1 with rows)", l.totalVisual(), len(l.segs))
	}
}

func TestRenderPreview_CropTruncatesLines(t *testing.T) {
	p := canvasPane(t, 100, 6, strings.Repeat("c", 95)+"\r\ntail> ")
	defer p.Dispose()
	p.Width, p.Height = 42, 8 // innerW 40, crop default
	out := ansi.Strip(p.renderPreview())
	if strings.Contains(out, strings.Repeat("c", 41)) {
		t.Error("crop mode must not render more than innerW columns of a row")
	}
	if !strings.Contains(out, "tail>") {
		t.Errorf("crop preview must show the screen tail, got:\n%s", out)
	}
	for i, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w > 40 {
			t.Errorf("line %d width %d exceeds innerW 40", i, w)
		}
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
	p.previewWrap = true
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
	p.previewWrap = true
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

// Cursor beyond the first wrap segment: with soft-wrap on, a caret at
// column 45 (innerW 40) must map to visual row 1, column 5 — verifying
// cursorVisual/cursorCol, not just "an escape is present".
func TestRenderPreview_CursorPastFirstSegment(t *testing.T) {
	// 50 chars then the cursor parks at col 50 (end of content) on row 0.
	p := canvasPane(t, 100, 6, strings.Repeat("a", 50))
	defer p.Dispose()
	p.previewWrap = true
	p.Width, p.Height = 42, 12 // innerW 40, innerH 10; row 0 wraps into 2 visual rows
	p.Active = true

	l := p.previewLayoutFor(40)
	pos := p.vt.CursorPosition()
	absRow := p.vt.ScrollbackLen() + pos.Y
	wantVisual := l.visualIndex(absRow, pos.X)
	if wantVisual == l.prefix[absRow] {
		t.Fatalf("setup: cursor at col %d should be on a wrapped continuation row, not the first segment", pos.X)
	}

	out := p.renderPreview()
	lines := strings.Split(out, "\n")
	// The reverse-video caret must appear on the continuation visual row,
	// not row 0. Locate which rendered line carries the \x1b[7m caret.
	caretLine := -1
	for i, ln := range lines {
		if strings.Contains(ln, "\x1b[7m") {
			caretLine = i
			break
		}
	}
	if caretLine < 0 {
		t.Fatal("no reverse-video caret rendered for an active cursor")
	}
	// total visual rows = 2 (content) ... blanks; bottom-anchored at innerH 10
	// means the two content rows sit at the top. The caret is on the 2nd.
	if caretLine != 1 {
		t.Errorf("caret rendered on visual line %d, want 1 (past the first wrap segment)", caretLine)
	}
}

// locate is the inverse of the prefix-sum walk; exercise it directly across
// multiple wrapped rows including the boundary v values.
func TestPreviewLayout_LocateRoundTrip(t *testing.T) {
	p := canvasPane(t, 100, 6, strings.Repeat("a", 95)+"\r\n"+strings.Repeat("b", 45)+"\r\nc\r\n")
	defer p.Dispose()
	p.previewWrap = true
	l := p.previewLayoutFor(40)
	total := l.totalVisual()
	for v := 0; v < total; v++ {
		absRow, s := l.locate(v)
		// The located segment must be one of absRow's segments, and its
		// visual index must round-trip back to v.
		found := false
		for i, seg := range l.segs[absRow] {
			if seg == s && l.prefix[absRow]+i == v {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("locate(%d) = row %d seg %+v does not round-trip", v, absRow, s)
		}
	}
}

// The toggle_wrap keybinding flips previewWrap on the active WIDE-CANVAS
// pane and no-ops elsewhere — the UI entry point, distinct from the layout
// math tested above.
func TestHandleKey_ToggleWrap(t *testing.T) {
	cfg := config.Default()
	cfg.Keybindings.ToggleWrap = "f9" // simple key, avoids alt+shift encoding

	canvas := NewPaneModel("wc", 1024)
	canvas.WideCanvas = true
	tab := NewTabModel("t", "T")
	tab.Root = NewLeaf(canvas)
	tab.ActivePane = "wc"
	m := Model{
		cfg:           cfg,
		client:        &fakeSender{},
		tabs:          []*TabModel{tab},
		activeTab:     0,
		notifications: NewNotificationCenter(30, 50),
	}

	if canvas.previewWrap {
		t.Fatal("setup: previewWrap must start false")
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF9})
	if !canvas.previewWrap {
		t.Error("toggle_wrap did not enable previewWrap on the active wide-canvas pane")
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF9})
	if canvas.previewWrap {
		t.Error("toggle_wrap did not flip previewWrap back off")
	}

	// Non-canvas active pane: the toggle must be a no-op (no spurious flag).
	plain := NewPaneModel("plain", 1024)
	tab2 := NewTabModel("t2", "T2")
	tab2.Root = NewLeaf(plain)
	tab2.ActivePane = "plain"
	m.tabs = []*TabModel{tab2}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF9})
	if plain.previewWrap {
		t.Error("toggle_wrap must not set previewWrap on a non-canvas pane")
	}
}

func TestPreviewPosAt_CropRoundTrip(t *testing.T) {
	// 100-wide emulator, one 95-char row + a short row. Crop mode (default):
	// one visual row per absolute row, viewport bottom-anchored. The emulator
	// is 4 rows tall (matching innerH) so the bottom-anchored viewport starts
	// exactly at absolute row 0 — a taller emulator (e.g. 6 rows, matching
	// only the pane's outer Height) leaves 2 unused blank screen rows below
	// the fed content, and bottom-anchoring would show those blank rows
	// instead of the "a" row, which is not what this test is exercising.
	p := canvasPane(t, 100, 4, strings.Repeat("a", 95)+"\r\nshort\r\n")
	defer p.Dispose()
	p.Width = 42  // innerW 40
	p.Height = 6  // innerH 4
	if !p.previewMode() {
		t.Fatalf("setup: want preview mode (innerW 40 < vt %d)", p.vt.Width())
	}
	// Column within the crop window maps 1:1 (seg.start == 0).
	col, _, ok := p.previewPosAt(10, 0)
	if !ok || col != 10 {
		t.Errorf("relX 10 -> col %d ok=%v, want col 10", col, ok)
	}
	// relY past the rendered content is out of range.
	if _, _, ok := p.previewPosAt(0, 99); ok {
		t.Errorf("relY 99 should be out of range")
	}
	// A click past the row's content clamps to content end (95), not off-grid.
	// (innerW is 40, so col can never even reach 95 — this only guards against
	// an unclamped or wildly-wrong column, not the exact clamp value; see the
	// "short" row assertion below for a tight, meaningful check of the clamp.)
	col, _, ok = p.previewPosAt(39, 0)
	if !ok || col > 95 {
		t.Errorf("relX 39 -> col %d ok=%v, want <=95", col, ok)
	}

	// A click past the "short" row's content (5 chars, contentEnd 4) clamps
	// to that row's real content end rather than the crop window width.
	// Derive relY the same way renderPreview/previewPosAt do — via the
	// layout's prefix sums and the bottom-anchored viewStart — instead of
	// guessing a magic number, so the coordinate is a real, reproducible
	// pane-local click.
	l := p.previewLayoutFor(40)
	total := l.totalVisual()
	viewStart := total - 4 // innerH
	if viewStart < 0 {
		viewStart = 0
	}
	// Absolute row 1 ("short") has exactly one visual row in crop mode; its
	// visual index is l.prefix[1].
	relYShort := l.prefix[1] - viewStart
	if relYShort < 0 {
		t.Fatalf("setup: relYShort %d must be a real non-negative click coordinate", relYShort)
	}
	wantCol := lineContentEnd(p, 1) + 1
	col, absLine, ok := p.previewPosAt(39, relYShort)
	if !ok || col != wantCol || absLine != 1 {
		t.Errorf("relX 39 on short row -> col %d absLine %d ok=%v, want col %d absLine 1",
			col, absLine, ok, wantCol)
	}
}

func TestPreviewPosAt_WrapSegments(t *testing.T) {
	// Soft-wrap: a 95-wide row at innerW 40 becomes 3 visual rows
	// [0,40),[40,80),[80,95). Across the 6-row emulator that's 3 wrapped +
	// 5 blank screen rows = 8 total visual rows. p.Height = 10 (innerH 8)
	// makes the whole layout fit with no scroll (viewStart = 0), so relY
	// below is a real, non-negative pane-local click coordinate landing on
	// the row's 2nd wrapped segment — not a value previewPosAt merely
	// happens to recompute internally.
	p := canvasPane(t, 100, 6, strings.Repeat("b", 95)+"\r\n")
	defer p.Dispose()
	p.previewWrap = true
	p.Width = 42  // innerW 40
	p.Height = 10 // innerH 8 == total visual rows (8) -> viewStart 0, no scroll
	l := p.previewLayoutFor(40)
	// Find the visual row index of the row's 2nd segment (absolute row 0).
	vSecond := l.prefix[0] + 1
	total := l.totalVisual()
	viewStart := total - 8 // innerH
	if viewStart < 0 {
		viewStart = 0
	}
	relY := vSecond - viewStart
	if relY < 0 {
		t.Fatalf("setup: relY %d must be a real non-negative click coordinate", relY)
	}
	col, absLine, ok := p.previewPosAt(5, relY)
	if !ok || col != 45 || absLine != 0 {
		t.Errorf("wrap seg2 relX 5 -> col %d line %d ok=%v, want col 45 line 0", col, absLine, ok)
	}
}
