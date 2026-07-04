package tui

import (
	uv "github.com/charmbracelet/ultraviolet"
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

// previewLayoutFor returns the wrap layout for innerW, rebuilding only when
// the emulator content or the width changed.
func (p *PaneModel) previewLayoutFor(innerW int) *previewLayout {
	if p.pvCache != nil && p.pvCache.innerW == innerW && p.pvCache.contentGen == p.contentGen {
		return p.pvCache
	}
	sbLen := p.vt.ScrollbackLen()
	h := p.vt.Height()
	total := sbLen + h
	l := &previewLayout{
		innerW:     innerW,
		contentGen: p.contentGen,
		segs:       make([][]seg, total),
		prefix:     make([]int, total+1),
	}
	for row := 0; row < total; row++ {
		l.segs[row] = wrapRow(cellAccessor(p, row), lineContentEnd(p, row), innerW)
		l.prefix[row+1] = l.prefix[row] + len(l.segs[row])
	}
	p.pvCache = l
	return l
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
