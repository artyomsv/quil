package tui

import (
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Preview layer for wide-canvas panes: the emulator is window-sized, the
// pane's rect can be much narrower. previewLayout wraps every absolute row
// (scrollback rows, then screen rows) into column windows ("segments") of
// at most innerW cells, respecting wide-glyph boundaries. Rendering and
// scrolling in preview mode operate on these visual rows; in preview mode
// PaneModel.scrollBack counts VISUAL rows, not emulator scrollback lines.
// Cached per pane keyed on (contentGen, innerW) — one rebuild per output
// burst, not per frame.
// See docs/superpowers/specs/2026-07-05-wide-canvas-design.md.

// seg is a half-open cell-column window [start, end) of one absolute row.
type seg struct{ start, end int }

type previewLayout struct {
	innerW     int
	wrap       bool // soft-wrap (all segments) vs left-edge crop (first segment only)
	contentGen uint64
	segs       [][]seg // per absolute row (scrollback + screen)
	prefix     []int   // prefix[i] = visual rows before absolute row i
}

// previewMode reports whether this pane renders the wrapped preview: a
// wide-canvas pane whose viewport is narrower than its emulator.
func (p *PaneModel) previewMode() bool {
	if !p.WideCanvas {
		return false
	}
	innerW := p.Width - 2
	return innerW >= 1 && innerW < p.vt.Width()
}

// previewLayoutFor returns the preview layout for innerW, rebuilding only
// when the emulator content, the width, or the wrap mode changed. Mode:
// p.previewWrap picks soft-wrap (every wide row becomes 1..N visual rows)
// vs the default left-edge crop (exactly 1 visual row per absolute row,
// truncated at innerW — tmux-style).
func (p *PaneModel) previewLayoutFor(innerW int) *previewLayout {
	if p.pvCache != nil && p.pvCache.innerW == innerW &&
		p.pvCache.contentGen == p.contentGen && p.pvCache.wrap == p.previewWrap {
		return p.pvCache
	}
	sbLen := p.vt.ScrollbackLen()
	h := p.vt.Height()
	total := sbLen + h
	l := &previewLayout{
		innerW:     innerW,
		wrap:       p.previewWrap,
		contentGen: p.contentGen,
		segs:       make([][]seg, total),
		prefix:     make([]int, total+1),
	}
	for row := 0; row < total; row++ {
		if p.previewWrap {
			l.segs[row] = wrapRow(cellAccessor(p, row), lineContentEnd(p, row), innerW)
		} else {
			l.segs[row] = cropRow(cellAccessor(p, row), lineContentEnd(p, row), innerW)
		}
		l.prefix[row+1] = l.prefix[row] + len(l.segs[row])
	}
	p.pvCache = l
	return l
}

// cropRow is the no-wrap counterpart of wrapRow: one segment per row,
// truncated at innerW (a wide glyph straddling the cut retreats one
// column, matching wrapRow's boundary rule).
func cropRow(getCell func(x int) *uv.Cell, contentEnd, innerW int) []seg {
	if innerW < 1 {
		innerW = 1
	}
	if contentEnd < 0 {
		return []seg{{0, 0}}
	}
	end := contentEnd + 1
	if end > innerW {
		end = innerW
		if c := getCell(end); c != nil && c.Width == 0 {
			end--
		}
	}
	if end < 1 {
		end = 1
	}
	return []seg{{0, end}}
}

// totalVisual is the number of visual rows across scrollback + screen.
func (l *previewLayout) totalVisual() int { return l.prefix[len(l.prefix)-1] }

// visualIndex maps an absolute (row, col) to its visual-row index.
func (l *previewLayout) visualIndex(absRow, col int) int {
	if absRow < 0 || absRow >= len(l.segs) {
		return 0
	}
	v := l.prefix[absRow]
	segs := l.segs[absRow]
	for i, s := range segs {
		if col < s.end || i == len(segs)-1 {
			return v + i
		}
	}
	return v
}

// locate maps a visual-row index back to (absolute row, segment) via a
// binary search over the prefix sums.
func (l *previewLayout) locate(v int) (absRow int, s seg) {
	lo, hi := 0, len(l.segs)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if l.prefix[mid] <= v {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	segs := l.segs[lo]
	idx := v - l.prefix[lo]
	if idx < 0 || idx >= len(segs) {
		return lo, seg{0, 0}
	}
	return lo, segs[idx]
}

// renderPreview renders the wrapped view of a wide-canvas pane. The live
// view (scrollBack == 0) bottom-anchors on the end of the layout; a
// scrolled view reserves the rightmost column for the scrollbar, with the
// same thumb math as renderScrollback but over visual rows. The caret is
// drawn in reverse video through the selection-capable cell walker
// (selStart == selEnd == caret column).
func (p *PaneModel) renderPreview() string {
	innerW := p.Width - 2
	innerH := p.Height - 2
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 1 {
		innerH = 1
	}
	l := p.previewLayoutFor(innerW)
	total := l.totalVisual()
	viewStart := total - innerH - p.scrollBack
	// Top-anchor when content is shorter than the viewport (rare for a
	// window-sized emulator, but a fresh pane can have total < innerH). A
	// negative viewStart would otherwise pad blank rows at the TOP and pin
	// content to the bottom; terminals conventionally top-anchor.
	if viewStart < 0 {
		viewStart = 0
	}
	scrolled := p.scrollBack > 0

	// Caret position in visual space (live view only).
	cursorVisual, cursorCol := -1, -1
	if !scrolled && p.Active && p.cursorVisible {
		pos := p.vt.CursorPosition()
		absRow := p.vt.ScrollbackLen() + pos.Y
		if absRow >= 0 && absRow < len(l.segs) && len(l.segs[absRow]) > 0 {
			cursorVisual = l.visualIndex(absRow, pos.X)
			s := l.segs[absRow][cursorVisual-l.prefix[absRow]]
			cursorCol = pos.X - s.start
			if cursorCol < 0 {
				cursorCol = 0
			}
			if cursorCol > innerW-1 {
				cursorCol = innerW - 1
			}
		}
	}

	contentW := innerW
	if scrolled {
		contentW = innerW - 1 // reserve the scrollbar column
		if contentW < 1 {
			contentW = 1
		}
	}
	lines := make([]string, innerH)
	for i := 0; i < innerH; i++ {
		v := viewStart + i
		if v < 0 || v >= total {
			lines[i] = ""
			continue
		}
		absRow, s := l.locate(v)
		getCell := cellAccessor(p, absRow)
		window := func(x int) *uv.Cell {
			if s.start+x >= s.end {
				return nil
			}
			return getCell(s.start + x)
		}
		w := s.end - s.start
		if w < 1 {
			w = 1
		}
		if w > contentW {
			w = contentW
		}
		selStart, selEnd := -1, -1
		if v == cursorVisual {
			selStart, selEnd = cursorCol, cursorCol // reverse-video caret
			if w <= cursorCol {
				w = cursorCol + 1 // caret sits on the blank cell after content
			}
		}
		lines[i] = p.styledCellLineWithSelection(window, w, selStart, selEnd)
	}

	if scrolled {
		thumbSize := innerH * innerH / total
		if thumbSize < 1 {
			thumbSize = 1
		}
		scrollRange := total - innerH
		thumbPos := 0
		if scrollRange > 0 {
			thumbPos = viewStart * (innerH - thumbSize) / scrollRange
		}
		if thumbPos < 0 {
			thumbPos = 0
		}
		for i, line := range lines {
			ch := "░"
			if i >= thumbPos && i < thumbPos+thumbSize {
				ch = "█"
			}
			lw := ansi.StringWidth(line)
			if lw > contentW {
				line = ansi.Truncate(line, contentW, "")
			} else if lw < contentW {
				line = line + strings.Repeat(" ", contentW-lw)
			}
			lines[i] = line + "\x1b[90m" + ch + "\x1b[0m"
		}
	}
	return strings.Join(lines, "\n")
}

// previewPosAt maps a pane-local (relX, relY) — already border-adjusted, i.e.
// 0-based within the inner content area — to an emulator (col, absLine) via
// the visual→absolute preview layout. ok is false when the point lands
// outside the rendered content (e.g. below the last line); in that case the
// returned (col, absLine) is still clamped to the nearest rendered row rather
// than zeroed, so callers can use it directly (e.g. a drag that continues
// past the pane edge extends the selection to the boundary instead of
// snapping to buffer position (0,0)). The mapping is the inverse of
// renderPreview's viewStart + locate() walk, so a click lands on the glyph
// under the cursor in both crop and soft-wrap modes.
func (p *PaneModel) previewPosAt(relX, relY int) (col, absLine int, ok bool) {
	innerW := p.Width - 2
	innerH := p.Height - 2
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 1 {
		innerH = 1
	}
	l := p.previewLayoutFor(innerW)
	total := l.totalVisual()
	viewStart := total - innerH - p.scrollBack
	if viewStart < 0 {
		viewStart = 0
	}
	v := viewStart + relY
	inRange := v >= 0 && v < total
	if !inRange {
		if total == 0 {
			return 0, 0, false
		}
		// Clamp to the nearest rendered row so a drag past the pane edge
		// extends the selection to the boundary (like the native path),
		// rather than snapping the endpoint to buffer position (0,0).
		if v < 0 {
			v = 0
		} else {
			v = total - 1
		}
	}
	absRow, s := l.locate(v)
	if relX < 0 {
		relX = 0
	}
	col = s.start + relX
	if col > s.end {
		col = s.end
	}
	// Clamp to the row's real content end so a click in the blank area past
	// text maps to end-of-line rather than an off-grid column.
	if end := lineContentEnd(p, absRow); end >= 0 && col > end+1 {
		col = end + 1
	}
	return col, absRow, inRange
}

// wrapRow splits one wide row into innerW-wide segments over [0, contentEnd].
// A blank row (contentEnd < 0) is a single empty segment. A wide glyph whose
// continuation cell would start a segment (lead straddling the cut) keeps
// lead+continuation together by retreating the boundary one column.
func wrapRow(getCell func(x int) *uv.Cell, contentEnd, innerW int) []seg {
	if innerW < 1 {
		innerW = 1
	}
	if contentEnd < 0 {
		return []seg{{0, 0}}
	}
	var out []seg
	start := 0
	for start <= contentEnd {
		end := start + innerW
		if end > contentEnd+1 {
			end = contentEnd + 1
		} else if c := getCell(end); c != nil && c.Width == 0 {
			end-- // lead glyph would straddle the cut; keep it whole
		}
		if end <= start { // pathological: innerW=1 against a wide glyph
			end = start + 1
		}
		out = append(out, seg{start, end})
		start = end
	}
	return out
}
