package tui

import (
	"strings"
	"unicode"
)

// EditorPos identifies a position in the editor's rune-indexed line space.
type EditorPos struct {
	Row int // 0-based line index in Lines[]
	Col int // 0-based rune column
}

// EditorSel tracks a text selection within the TextEditor.
type EditorSel struct {
	Anchor EditorPos // where selection started (fixed)
	Cursor EditorPos // where selection extends to (moves with keys)
}

// Normalized returns start/end ordered top-to-bottom, left-to-right.
func (s *EditorSel) Normalized() (start, end EditorPos) {
	a, c := s.Anchor, s.Cursor
	if a.Row < c.Row || (a.Row == c.Row && a.Col <= c.Col) {
		return a, c
	}
	return c, a
}

// IsEmpty returns true if the selection has zero width (anchor == cursor).
func (s *EditorSel) IsEmpty() bool {
	return s.Anchor == s.Cursor
}

// ColRange returns the selected column range [startCol, endCol) for a given row.
// endCol is exclusive (one past the last selected rune) for easy slicing.
// Returns (-1, -1) if the row is not in the selection.
func (s *EditorSel) ColRange(row, lineRuneLen int) (startCol, endCol int) {
	start, end := s.Normalized()
	if row < start.Row || row > end.Row {
		return -1, -1
	}
	if start.Row == end.Row {
		return start.Col, end.Col
	}
	if row == start.Row {
		return start.Col, lineRuneLen
	}
	if row == end.Row {
		return 0, end.Col
	}
	// Middle line — fully selected
	return 0, lineRuneLen
}

// editorExtractText extracts the selected text from the editor's lines.
func editorExtractText(lines []string, sel *EditorSel) string {
	if sel == nil || sel.IsEmpty() {
		return ""
	}
	start, end := sel.Normalized()

	// Clamp to valid bounds
	if start.Row < 0 {
		start.Row = 0
		start.Col = 0
	}
	if end.Row >= len(lines) {
		end.Row = len(lines) - 1
		end.Col = runeLen(lines[end.Row])
	}

	if start.Row == end.Row {
		line := lines[start.Row]
		runes := []rune(line)
		sc := clampInt(start.Col, 0, len(runes))
		ec := clampInt(end.Col, 0, len(runes))
		return string(runes[sc:ec])
	}

	var b strings.Builder
	// First line: from startCol to end
	firstRunes := []rune(lines[start.Row])
	sc := clampInt(start.Col, 0, len(firstRunes))
	b.WriteString(string(firstRunes[sc:]))

	// Middle lines: full lines
	for row := start.Row + 1; row < end.Row; row++ {
		b.WriteByte('\n')
		b.WriteString(lines[row])
	}

	// Last line: from start to endCol
	b.WriteByte('\n')
	lastRunes := []rune(lines[end.Row])
	ec := clampInt(end.Col, 0, len(lastRunes))
	b.WriteString(string(lastRunes[:ec]))

	return b.String()
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// isWordChar returns true for characters considered part of a "word".
func isWordChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// editorWordBoundary finds the next word boundary in a line.
// Two-phase scan: skip non-word chars (spaces/punctuation), then skip word chars.
// Returns the new column position.
func editorWordBoundary(line string, startCol, dir int) int {
	runes := []rune(line)
	rl := len(runes)
	if rl == 0 {
		return 0
	}

	col := startCol + dir
	if col < 0 {
		return 0
	}
	if col >= rl {
		return rl
	}

	// Phase 1: skip non-word characters (spaces, punctuation)
	for col >= 0 && col < rl && !isWordChar(runes[col]) {
		col += dir
	}

	// Phase 2: skip word characters
	for col >= 0 && col < rl && isWordChar(runes[col]) {
		col += dir
	}

	// For backward: landed one before the word start — step forward
	if dir < 0 {
		col++
	}

	if col < 0 {
		return 0
	}
	if col > rl {
		return rl
	}
	return col
}

// editorWordJump performs a multi-word jump with line wrapping.
func editorWordJump(lines []string, pos EditorPos, dir, count int) EditorPos {
	cur := pos
	for i := 0; i < count; i++ {
		prev := cur
		if cur.Row < 0 || cur.Row >= len(lines) {
			break
		}

		next := editorWordBoundary(lines[cur.Row], cur.Col, dir)
		if next != cur.Col {
			cur.Col = next
		} else {
			// No movement on current line — wrap to next/previous line
			if dir > 0 && cur.Row < len(lines)-1 {
				cur.Row++
				cur.Col = 0
				// Continue scanning from start of new line
				if runeLen(lines[cur.Row]) > 0 {
					next = editorWordBoundary(lines[cur.Row], -1, 1)
					cur.Col = next
				}
			} else if dir < 0 && cur.Row > 0 {
				cur.Row--
				rl := runeLen(lines[cur.Row])
				if rl > 0 {
					cur.Col = rl
					next = editorWordBoundary(lines[cur.Row], rl, -1)
					cur.Col = next
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

// isEmptyOrWhitespace returns true if the line is empty or contains only spaces/tabs.
func isEmptyOrWhitespace(line string) bool {
	for _, r := range line {
		if r != ' ' && r != '\t' {
			return false
		}
	}
	return true
}

// editorParagraphJump finds the next paragraph boundary in a direction.
// A paragraph boundary is an empty/whitespace-only line.
// Skips any current blank lines first, then scans until the next blank line
// (or document edge).
func editorParagraphJump(lines []string, fromRow, dir int) int {
	row := fromRow

	// Phase 1: skip current blank lines
	for row >= 0 && row < len(lines) && isEmptyOrWhitespace(lines[row]) {
		row += dir
	}

	// Phase 2: scan until next blank line
	for row >= 0 && row < len(lines) && !isEmptyOrWhitespace(lines[row]) {
		row += dir
	}

	// Clamp to document bounds
	if row < 0 {
		return 0
	}
	if row >= len(lines) {
		return len(lines) - 1
	}
	return row
}
