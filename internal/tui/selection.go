package tui

import (
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
)

// SelectionAnchor identifies a cell in the combined scrollback+screen space.
type SelectionAnchor struct {
	Col  int // 0-based column within pane content (excludes border)
	Line int // absolute line: 0..sbLen-1 = scrollback, sbLen..sbLen+h-1 = screen
}

// Selection tracks a text selection within a single pane.
type Selection struct {
	PaneID string
	Anchor SelectionAnchor // where selection started (fixed)
	Cursor SelectionAnchor // where selection extends to (moves with keys/mouse)
}

// Normalized returns start/end ordered top-to-bottom, left-to-right.
func (s *Selection) Normalized() (start, end SelectionAnchor) {
	a, c := s.Anchor, s.Cursor
	if a.Line < c.Line || (a.Line == c.Line && a.Col <= c.Col) {
		return a, c
	}
	return c, a
}

// ColRange returns the selected column range for a given absolute line.
// Returns (-1, -1) if the line is not in the selection.
func (s *Selection) ColRange(absLine, width int) (startCol, endCol int) {
	start, end := s.Normalized()
	if absLine < start.Line || absLine > end.Line {
		return -1, -1
	}
	if start.Line == end.Line {
		// Single-line selection
		return start.Col, end.Col
	}
	if absLine == start.Line {
		return start.Col, width - 1
	}
	if absLine == end.Line {
		return 0, end.Col
	}
	// Middle line — fully selected
	return 0, width - 1
}

// extractText extracts the selected text from a pane.
func extractText(pane *PaneModel, sel *Selection) string {
	if sel == nil || pane == nil {
		return ""
	}
	start, end := sel.Normalized()
	w := pane.vt.Width()

	var result strings.Builder
	result.Grow((end.Line - start.Line + 1) * w)
	for line := start.Line; line <= end.Line; line++ {
		colStart := 0
		colEnd := w - 1
		if line == start.Line {
			colStart = start.Col
		}
		if line == end.Line {
			colEnd = end.Col
		}

		getCell := cellAccessor(pane, line)

		var b strings.Builder
		for x := colStart; x <= colEnd && x < w; x++ {
			cell := getCell(x)
			if cell != nil && cell.Content != "" {
				b.WriteString(cell.Content)
			} else {
				b.WriteByte(' ')
			}
		}
		lineText := strings.TrimRight(b.String(), " ")
		result.WriteString(lineText)

		// Add newline only between lines, and only for real line breaks.
		// If the line fills the full terminal width, it's a soft wrap —
		// the text continues on the next row without a real newline.
		if line < end.Line {
			contentEnd := lineContentEnd(pane, line)
			isSoftWrap := false
			if contentEnd >= w-1 {
				// Character-level wrap: content fills to the last column
				isSoftWrap = true
			} else if contentEnd >= 0 && w-1-contentEnd < 3 {
				// Possibly word-wrapped by the shell: content ends near the edge.
				// Check if the next line has content (continuation vs empty line).
				nextEnd := lineContentEnd(pane, line+1)
				isSoftWrap = nextEnd >= 0
			}
			if !isSoftWrap {
				result.WriteByte('\n')
			}
		}
	}
	return result.String()
}

// lineContentEnd returns the column of the last non-space character on a line.
// Returns -1 if the line is empty.
func lineContentEnd(pane *PaneModel, absLine int) int {
	sbLen := pane.vt.ScrollbackLen()
	w := pane.vt.Width()

	var getCell func(x int) *uv.Cell
	if absLine < sbLen {
		srcLine := absLine
		getCell = func(x int) *uv.Cell {
			return pane.vt.ScrollbackCellAt(x, srcLine)
		}
	} else {
		screenLine := absLine - sbLen
		getCell = func(x int) *uv.Cell {
			return pane.vt.CellAt(x, screenLine)
		}
	}

	last := -1
	for x := 0; x < w; x++ {
		cell := getCell(x)
		if cell != nil && cell.Content != "" && cell.Content != " " {
			last = x
		}
	}
	return last
}

// lastContentLine returns the absolute line number of the last line with
// non-space content. Returns 0 if no content is found.
func lastContentLine(pane *PaneModel) int {
	sbLen := pane.vt.ScrollbackLen()
	h := pane.vt.Height()
	maxLine := sbLen + h - 1
	for line := maxLine; line >= 0; line-- {
		if lineContentEnd(pane, line) >= 0 {
			return line
		}
	}
	return 0
}

// cellAccessor returns a function to read cells on the given absolute line.
func cellAccessor(pane *PaneModel, absLine int) func(x int) *uv.Cell {
	sbLen := pane.vt.ScrollbackLen()
	if absLine < sbLen {
		srcLine := absLine
		return func(x int) *uv.Cell {
			return pane.vt.ScrollbackCellAt(x, srcLine)
		}
	}
	screenLine := absLine - sbLen
	return func(x int) *uv.Cell {
		return pane.vt.CellAt(x, screenLine)
	}
}

func cellIsSpace(getCell func(x int) *uv.Cell, x int) bool {
	cell := getCell(x)
	return cell == nil || cell.Content == "" || cell.Content == " "
}

// scanWordBoundary jumps to the next word boundary in a direction.
// Behavior matches standard text editors (Ctrl+Arrow):
//   - Skip any spaces at current position
//   - Then skip the next word (non-spaces)
//   - Stop at the boundary between word and space
func scanWordBoundary(pane *PaneModel, absLine, startCol, dir int) int {
	w := pane.vt.Width()
	getCell := cellAccessor(pane, absLine)

	col := startCol + dir
	if col < 0 {
		return 0
	}
	if col >= w {
		return w - 1
	}

	// Phase 1: skip spaces
	for col >= 0 && col < w && cellIsSpace(getCell, col) {
		col += dir
	}

	// Phase 2: skip word characters (non-spaces)
	for col >= 0 && col < w && !cellIsSpace(getCell, col) {
		col += dir
	}

	// Landed one past the boundary — step back
	if dir > 0 {
		col--
	} else {
		col++
	}

	if col < 0 {
		return 0
	}
	if col >= w {
		return w - 1
	}
	return col
}

// selWordJump performs a word jump for selection, wrapping across lines.
// Jumps n words in the given direction. If no movement on current line,
// wraps to the next/previous line and continues.
func selWordJump(pane *PaneModel, cur SelectionAnchor, dir, n, maxLine int) SelectionAnchor {
	for i := 0; i < n; i++ {
		prev := cur
		next := scanWordBoundary(pane, cur.Line, cur.Col, dir)
		if next != cur.Col {
			cur.Col = next
		} else {
			// No movement — wrap to next/previous line
			if dir > 0 && cur.Line < maxLine {
				cur.Line++
				cur.Col = 0
				// Continue scanning from start of new line
				end := lineContentEnd(pane, cur.Line)
				if end >= 0 {
					next = scanWordBoundary(pane, cur.Line, -1, 1)
					cur.Col = next
				}
			} else if dir < 0 && cur.Line > 0 {
				cur.Line--
				end := lineContentEnd(pane, cur.Line)
				if end >= 0 {
					cur.Col = end
				} else {
					cur.Col = 0
				}
			}
		}
		if cur == prev {
			break // no movement possible
		}
	}
	return cur
}

