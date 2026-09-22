package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

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
	joinLeftTrim := false // this row continues a word-wrapped one
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
			} else if cell != nil && cell.Width == 0 {
				// Trailing half of a wide glyph: the lead cell already wrote
				// it. A space here turned "你好" into "你 好".
				continue
			} else {
				b.WriteByte(' ')
			}
		}
		lineText := strings.TrimRight(b.String(), " ")
		if joinLeftTrim {
			// Drop the continuation indent the wrapping app added.
			lineText = strings.TrimLeft(lineText, " ")
			joinLeftTrim = false
		}
		result.WriteString(lineText)

		// Add newline only between lines, and only for real line breaks.
		// The emulator records no soft-wrap flag, so a wrap is inferred from
		// where the text stops — see lineBreakKind.
		if line < end.Line {
			switch lineBreakKind(pane, line) {
			case breakHard:
				result.WriteByte('\n')
			case breakWordWrap:
				// The app broke the line at a space and indented the
				// continuation: rejoin with exactly one space.
				joinLeftTrim = true
				if lineText != "" {
					result.WriteByte(' ')
				}
			}
		}
	}
	return dedent(result.String(), start.Col)
}

// dedent removes the leading spaces every non-blank line shares. Claude Code
// and similar UIs indent their whole transcript by a margin that is layout,
// not content; relative indentation (nested lists, code) is kept.
//
// firstCol is the screen column the selection starts at. The first line's
// text begins there, so its indent is firstCol plus its own leading spaces —
// a drag started on the first letter of an indented paragraph (the natural
// way to select it) otherwise reads as indent 0 and cancels the dedent for
// every line after it.
//
// A line opening with a list or message marker ("• ", "- ", "1. ") counts at
// the column its TEXT starts, because that is where its hanging continuation
// lines are indented to: Codex opens a reply with "• " and indents the rest
// of it by two, so measuring the marker at 0 kept every later paragraph
// indented. The marker itself is never removed.
func dedent(s string, firstCol int) string {
	lines := strings.Split(s, "\n")
	lead := func(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }
	common := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := lead(l) + markerWidth(l[lead(l):])
		if i == 0 {
			n += firstCol
		}
		if common < 0 || n < common {
			common = n
		}
	}
	if common <= 0 {
		return s
	}
	for i, l := range lines {
		// Only real spaces are removed — never a marker counted as indent.
		cut := min(common, lead(l))
		if i == 0 {
			cut = min(max(common-firstCol, 0), lead(l))
		}
		lines[i] = l[cut:]
	}
	return strings.Join(lines, "\n")
}

// markerWidth returns the columns a leading list/message marker and the
// space after it occupy in text, or 0 when text does not open with one.
func markerWidth(text string) int {
	word, rest, ok := strings.Cut(text, " ")
	if !ok || rest == "" || !isListMarker(word) {
		return 0
	}
	return utf8.RuneCountInString(word) + 1 // markers are single-width runes
}

// Kinds of boundary between two adjacent rows of a selection.
const (
	breakHard     = iota // a real newline
	breakCharWrap        // the terminal wrapped mid-text at the last column
	breakWordWrap        // an app wrapped at a word boundary short of the edge
)

// wordWrapSlack is how far short of the right edge an app may stop when it
// word-wraps. Claude Code and other Ink-based UIs wrap a few columns inside
// the terminal width, so "the next word would not have fit" is measured
// against a slightly narrower line than the emulator's.
const wordWrapSlack = 4

// lineBreakKind classifies the break after absLine. A row whose text reaches
// the last column is a terminal auto-wrap. A row that stops short is still a
// wrap when the first word of the next row would not have fitted in the space
// left — which is exactly the test a word-wrapping app applied when it broke
// the line there. Everything else, including a following blank row or list
// item, is a real line break.
func lineBreakKind(pane *PaneModel, absLine int) int {
	w := pane.vt.Width()
	contentEnd := lineContentEnd(pane, absLine)
	if contentEnd < 0 {
		return breakHard
	}
	if contentEnd >= w-1 {
		// A full row followed by an INDENTED one is an app that wraps right
		// up to the edge (Codex) and indents its continuation, not the
		// terminal splitting a word — the terminal's continuation starts at
		// column 0. One leading space is kept as content: that is the
		// terminal wrapping exactly at a space.
		if leadingBlanks(pane, absLine+1) >= 2 {
			return breakWordWrap
		}
		return breakCharWrap
	}
	word, wordCols := firstWord(pane, absLine+1)
	if word == "" || isListMarker(word) {
		return breakHard
	}
	free := w - 1 - contentEnd // columns left after the last character
	if free < wordCols+1+wordWrapSlack {
		return breakWordWrap
	}
	return breakHard
}

// leadingBlanks returns how many blank cells open absLine before its first
// content, or 0 when the line is blank or out of range (a blank row is a
// paragraph break, never a continuation).
func leadingBlanks(pane *PaneModel, absLine int) int {
	if absLine < 0 || absLine >= pane.vt.ScrollbackLen()+pane.vt.Height() {
		return 0
	}
	w := pane.vt.Width()
	getCell := cellAccessor(pane, absLine)
	for x := 0; x < w; x++ {
		if !cellIsSpace(getCell, x) {
			return x
		}
	}
	return 0
}

// firstWord returns the first run of non-space cells on absLine and the
// columns it occupies, or ("", 0) when the line is blank or out of range.
// Columns, not runes, because a CJK word is twice as wide as it is long.
func firstWord(pane *PaneModel, absLine int) (string, int) {
	if absLine < 0 || absLine >= pane.vt.ScrollbackLen()+pane.vt.Height() {
		return "", 0
	}
	w := pane.vt.Width()
	getCell := cellAccessor(pane, absLine)
	var b strings.Builder
	cols := 0
	for x := 0; x < w; x++ {
		if cell := getCell(x); cell != nil && cell.Content == "" && cell.Width == 0 {
			if b.Len() > 0 {
				cols++ // trailing half of a wide glyph inside the word
			}
			continue
		}
		if cellIsSpace(getCell, x) {
			if b.Len() > 0 {
				break
			}
			continue
		}
		b.WriteString(getCell(x).Content)
		cols++
	}
	return b.String(), cols
}

// isListMarker reports whether word opens a list item or a new block, which
// starts a new line even when the row above it is full.
func isListMarker(word string) bool {
	switch word {
	case "-", "*", "+", "•", "●", "⏺", "⎿", ">", "│", "#", "##", "###":
		return true
	}
	// "1." / "12)" — a numbered item.
	r := []rune(word)
	if len(r) >= 2 && (r[len(r)-1] == '.' || r[len(r)-1] == ')') {
		for _, c := range r[:len(r)-1] {
			if !unicode.IsDigit(c) {
				return false
			}
		}
		return true
	}
	return false
}

// isSelectWordRune reports whether r belongs to a word for double-click
// selection. Paths and URLs stay whole (`/`, `.`, `-`, `:` are word runes);
// brackets, quotes, separators and box-drawing glyphs end a word.
func isSelectWordRune(r rune) bool {
	if unicode.IsSpace(r) || (r >= 0x2500 && r <= 0x257F) {
		return false
	}
	return !strings.ContainsRune("()[]{}<>\"'`,;|", r)
}

// cellWordRune reports whether the cell at x is part of a word. The trailing
// half of a wide character (empty content, zero width) belongs to the word
// its leading half is in.
func cellWordRune(getCell func(x int) *uv.Cell, x int) bool {
	cell := getCell(x)
	if cell == nil {
		return false
	}
	if cell.Content == "" {
		return cell.Width == 0 && x > 0 && cellWordRune(getCell, x-1)
	}
	r := []rune(cell.Content)
	return len(r) > 0 && isSelectWordRune(r[0])
}

// wordBoundsAt returns the inclusive column range of the word under col on
// absLine. ok is false when col is not on a word. Trailing sentence
// punctuation is dropped so "done." selects "done".
func wordBoundsAt(pane *PaneModel, absLine, col int) (start, end int, ok bool) {
	w := pane.vt.Width()
	if col < 0 || col >= w {
		return 0, 0, false
	}
	getCell := cellAccessor(pane, absLine)
	if !cellWordRune(getCell, col) {
		return 0, 0, false
	}
	start, end = col, col
	for start > 0 && cellWordRune(getCell, start-1) {
		start--
	}
	for end < w-1 && cellWordRune(getCell, end+1) {
		end++
	}
	for end > col {
		c := getCell(end)
		if c == nil || !strings.ContainsAny(c.Content, ".,:!?") || len(c.Content) != 1 {
			break
		}
		end--
	}
	return start, end, true
}

// maxSentenceRows bounds how far a triple-click follows a wrapped paragraph
// up or down, so a click on a huge unbroken block stays cheap.
const maxSentenceRows = 200

// sentenceAt returns the start and end cells (inclusive) of the sentence
// under at, following the paragraph across wrapped rows the same way copy
// joins them (lineBreakKind). ok is false on a blank row.
func sentenceAt(pane *PaneModel, at SelectionAnchor) (from, to SelectionAnchor, ok bool) {
	if lineContentEnd(pane, at.Line) < 0 {
		return from, to, false
	}
	maxLine := pane.vt.ScrollbackLen() + pane.vt.Height() - 1
	top := at.Line
	for top > 0 && at.Line-top < maxSentenceRows && lineBreakKind(pane, top-1) != breakHard {
		top--
	}
	bot := at.Line
	for bot < maxLine && bot-at.Line < maxSentenceRows && lineBreakKind(pane, bot) != breakHard {
		bot++
	}

	// One unit per glyph, with the cell it came from. Rows are simply
	// concatenated: a wrapped row's trailing blanks and the next row's
	// indent are whitespace, which is all the sentence scan needs.
	w := pane.vt.Width()
	var units []string
	var refs []SelectionAnchor
	idx := 0
	for line := top; line <= bot; line++ {
		getCell := cellAccessor(pane, line)
		for x := 0; x < w; x++ {
			cell := getCell(x)
			if cell != nil && cell.Content == "" && cell.Width == 0 {
				continue // trailing half of a wide glyph
			}
			if line == at.Line && x <= at.Col {
				idx = len(units)
			}
			content := " "
			if cell != nil && cell.Content != "" {
				content = cell.Content
			}
			units = append(units, content)
			refs = append(refs, SelectionAnchor{Col: x, Line: line})
		}
	}
	start, end, ok := sentenceBounds(units, idx)
	if !ok {
		return from, to, false
	}
	return refs[start], refs[end-1], true
}

// sentenceBounds returns the sentence of units containing idx as
// [start, end). A sentence ends at a run of . ! ? … (plus any closing quotes
// or brackets) followed by whitespace or the end of the text, so "3.14" and
// "v1.75.0" do not end one. A click in the gap between two sentences picks
// the next one. A list or message marker opening the text ("• ", "- ") is
// not part of the first sentence. ok is false when units is all whitespace.
func sentenceBounds(units []string, idx int) (start, end int, ok bool) {
	n := len(units)
	space := func(i int) bool { return strings.TrimSpace(units[i]) == "" }
	skipSpace := func(i int) int {
		for i < n && space(i) {
			i++
		}
		return i
	}
	s := skipSpace(0)
	if s == n {
		return 0, 0, false
	}
	// Drop a leading marker word, keeping it only when nothing follows it.
	k := s
	var word strings.Builder
	for k < n && !space(k) {
		word.WriteString(units[k])
		k++
	}
	if isListMarker(word.String()) && skipSpace(k) < n {
		s = skipSpace(k)
	}

	isTerm := func(i int) bool { return isOneOf(units[i], ".!?…") }
	for j := s; j < n; {
		if !isTerm(j) {
			j++
			continue
		}
		k := j
		for k < n && isTerm(k) {
			k++
		}
		for k < n && isOneOf(units[k], `"')]»”’`) {
			k++
		}
		if k < n && !space(k) {
			j = k // "3.14", "v1.2": punctuation inside a token
			continue
		}
		next := skipSpace(k)
		if idx < k || next == n {
			// Also the answer for a click in the whitespace after the last
			// sentence: nothing follows it to select instead.
			return s, k, true
		}
		s, j = next, next
	}
	// The last sentence has no terminator: it runs to the last glyph.
	e := n
	for e > s && space(e-1) {
		e--
	}
	return s, e, true
}

// isOneOf reports whether glyph is exactly one of the runes in set.
func isOneOf(glyph, set string) bool {
	r, size := utf8.DecodeRuneInString(glyph)
	return size > 0 && size == len(glyph) && strings.ContainsRune(set, r)
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

