package tui

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/BurntSushi/toml"
	"github.com/charmbracelet/x/ansi"

	"github.com/artyomsv/quil/internal/clipboard"
	"github.com/artyomsv/quil/internal/config"
)

// editorPasteMsg delivers clipboard content to the active editor.
type editorPasteMsg string

// HighlightMode selects which syntax highlighter the editor renders with.
type HighlightMode int

const (
	// HighlightTOML applies TOML keyword/comment colouring (default — used by
	// the TOML editor accessible via F1 → Plugins).
	HighlightTOML HighlightMode = iota
	// HighlightPlain disables syntax colouring. Used by pane notes.
	HighlightPlain
)

// TextEditor is a minimal multi-line text editor with optional syntax highlighting.
type TextEditor struct {
	Lines      []string
	CursorRow  int // rune-based row
	CursorCol  int // rune-based column
	ScrollTop  int
	ViewHeight int
	ViewWidth  int
	FilePath   string
	Dirty      bool
	SaveErr    string
	Sel        *EditorSel // active selection (nil = none)
	// Highlight selects the syntax highlighter. Defaults to HighlightTOML.
	Highlight HighlightMode
	// ReadOnly disables every key path that would mutate the document
	// (typing, paste, cut, save, enter/backspace/delete, tab/space). Cursor
	// movement, selection, and clipboard COPY (Enter on a selection,
	// right-click) still work. Used by the F1 → log viewers so users can
	// scroll and copy log content without accidentally overwriting the
	// underlying file with Ctrl+S.
	ReadOnly bool
	// PageSize is the cursor jump distance for Alt+Up / Alt+Down. 0 falls
	// back to a built-in default (see editorDefaultPageSize). Used by the
	// log viewer to navigate large files quickly without holding Down.
	PageSize int
}

// editorDefaultPageSize is used when TextEditor.PageSize is 0 (unset).
const editorDefaultPageSize = 40

// highlight applies the configured highlighter to a line. Returns the line
// unchanged when in plain mode.
func (e *TextEditor) highlight(line string) string {
	if e.Highlight == HighlightPlain {
		return line
	}
	return highlightTOML(line)
}

// ApproxBytes returns a lower-bound estimate of the editor's in-memory size.
// Sums UTF-8 byte lengths of all lines plus one newline byte per line
// boundary. Does not account for Go slice overhead or unused capacity.
// Used by the Memory dialog for ranking; precision is not important.
func (e *TextEditor) ApproxBytes() uint64 {
	if e == nil {
		return 0
	}
	var n uint64
	for _, line := range e.Lines {
		n += uint64(len(line))
	}
	if len(e.Lines) > 1 {
		n += uint64(len(e.Lines) - 1) // newlines between lines
	}
	return n
}

// NewTextEditor creates an editor from file content.
func NewTextEditor(content, filePath string, viewW, viewH int) *TextEditor {
	content = strings.ReplaceAll(content, "\r", "")
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}
	return &TextEditor{
		Lines:      lines,
		FilePath:   filePath,
		ViewHeight: viewH,
		ViewWidth:  viewW,
	}
}

// --- Rune helpers ---

func runeLen(s string) int {
	return utf8.RuneCountInString(s)
}

// runeOffset returns the byte index where rune at position runePos starts.
func runeOffset(s string, runePos int) int {
	i := 0
	for j := range s {
		if i == runePos {
			return j
		}
		i++
	}
	return len(s)
}

// --- Key handling ---

func (e *TextEditor) HandleKey(key string) (saved, closed bool, cmd tea.Cmd) {
	e.SaveErr = ""

	// 1. Selection-extending keys (shift combos)
	if e.isSelectionKey(key) {
		e.handleSelectionKey(key)
		e.ensureCursorVisible()
		return false, false, nil
	}

	// 2. Clipboard / selection operations
	switch key {
	case "ctrl+a":
		e.selectAll()
		e.ensureCursorVisible()
		return false, false, nil

	case "ctrl+c":
		// Copy selection to clipboard (without deleting).
		if e.Sel != nil && !e.Sel.IsEmpty() {
			text := editorExtractText(e.Lines, e.Sel)
			e.Sel = nil
			cmd = func() tea.Msg {
				if err := clipboard.Write(text); err != nil {
					log.Printf("editor clipboard write: %v", err)
				}
				return nil
			}
		}
		e.ensureCursorVisible()
		return false, false, cmd

	case "ctrl+x":
		if e.ReadOnly {
			e.ensureCursorVisible()
			return false, false, nil
		}
		if e.Sel != nil && !e.Sel.IsEmpty() {
			text := editorExtractText(e.Lines, e.Sel)
			e.deleteSelection()
			e.Dirty = true
			cmd = func() tea.Msg {
				if err := clipboard.Write(text); err != nil {
					log.Printf("editor clipboard write: %v", err)
				}
				return nil
			}
		}
		e.ensureCursorVisible()
		return false, false, cmd

	case "ctrl+v":
		if e.ReadOnly {
			e.ensureCursorVisible()
			return false, false, nil
		}
		e.ensureCursorVisible()
		return false, false, e.pasteCmd()
	}

	// 3. Context-sensitive keys
	switch key {
	case "esc":
		if e.Sel != nil {
			e.Sel = nil
			return false, false, nil
		}
		return false, true, nil

	case "ctrl+s":
		if e.ReadOnly {
			return false, false, nil
		}
		if err := e.Save(); err != nil {
			e.SaveErr = err.Error()
			return false, false, nil
		}
		return true, false, nil

	case "enter":
		if e.Sel != nil && !e.Sel.IsEmpty() {
			text := editorExtractText(e.Lines, e.Sel)
			e.Sel = nil
			cmd = func() tea.Msg {
				if err := clipboard.Write(text); err != nil {
					log.Printf("editor clipboard write: %v", err)
				}
				return nil
			}
			e.ensureCursorVisible()
			return false, false, cmd
		}
		if e.ReadOnly {
			return false, false, nil
		}
		e.insertNewline()
		e.Dirty = true

	case "backspace":
		if e.ReadOnly {
			return false, false, nil
		}
		if !e.clearSel() {
			e.backspace()
		}
		e.Dirty = true

	case "delete":
		if e.ReadOnly {
			return false, false, nil
		}
		if !e.clearSel() {
			e.deleteChar()
		}
		e.Dirty = true

	case "ctrl+y":
		if e.ReadOnly {
			return false, false, nil
		}
		e.Sel = nil
		if len(e.Lines) == 1 {
			if e.Lines[0] != "" {
				e.Lines[0] = ""
				e.CursorCol = 0
				e.Dirty = true
			}
		} else {
			e.Lines = append(e.Lines[:e.CursorRow], e.Lines[e.CursorRow+1:]...)
			if e.CursorRow >= len(e.Lines) {
				e.CursorRow = len(e.Lines) - 1
			}
			e.clampCol()
			e.Dirty = true
		}

	// 4. Movement keys — clear selection
	case "up":
		e.Sel = nil
		if e.CursorRow > 0 {
			e.CursorRow--
			e.clampCol()
		}
	case "down":
		e.Sel = nil
		if e.CursorRow < len(e.Lines)-1 {
			e.CursorRow++
			e.clampCol()
		}
	case "left":
		e.Sel = nil
		if e.CursorCol > 0 {
			e.CursorCol--
		} else if e.CursorRow > 0 {
			e.CursorRow--
			e.CursorCol = runeLen(e.Lines[e.CursorRow])
		}
	case "right":
		e.Sel = nil
		if e.CursorCol < runeLen(e.Lines[e.CursorRow]) {
			e.CursorCol++
		} else if e.CursorRow < len(e.Lines)-1 {
			e.CursorRow++
			e.CursorCol = 0
		}
	case "home":
		e.Sel = nil
		e.CursorCol = 0
	case "end":
		e.Sel = nil
		e.CursorCol = runeLen(e.Lines[e.CursorRow])

	// Navigation keys (word jump without selection)
	case "ctrl+right":
		e.Sel = nil
		pos := editorWordJump(e.Lines, EditorPos{Row: e.CursorRow, Col: e.CursorCol}, 1, 1)
		e.CursorRow = pos.Row
		e.CursorCol = pos.Col
	case "ctrl+left":
		e.Sel = nil
		pos := editorWordJump(e.Lines, EditorPos{Row: e.CursorRow, Col: e.CursorCol}, -1, 1)
		e.CursorRow = pos.Row
		e.CursorCol = pos.Col
	case "ctrl+alt+right":
		e.Sel = nil
		pos := editorWordJump(e.Lines, EditorPos{Row: e.CursorRow, Col: e.CursorCol}, 1, 3)
		e.CursorRow = pos.Row
		e.CursorCol = pos.Col
	case "ctrl+alt+left":
		e.Sel = nil
		pos := editorWordJump(e.Lines, EditorPos{Row: e.CursorRow, Col: e.CursorCol}, -1, 3)
		e.CursorRow = pos.Row
		e.CursorCol = pos.Col
	case "ctrl+up":
		e.Sel = nil
		e.CursorRow = editorParagraphJump(e.Lines, e.CursorRow, -1)
		e.CursorCol = 0
	case "ctrl+down":
		e.Sel = nil
		e.CursorRow = editorParagraphJump(e.Lines, e.CursorRow, 1)
		e.CursorCol = 0

	// Alt+Up / Alt+Down jump the cursor by PageSize lines (default 40).
	// Used by the F1 → log viewers to flip through long files quickly
	// without holding the arrow key. Configurable via [ui]
	// log_viewer_page_lines in config.toml.
	case "alt+up":
		e.Sel = nil
		step := e.PageSize
		if step <= 0 {
			step = editorDefaultPageSize
		}
		e.CursorRow -= step
		if e.CursorRow < 0 {
			e.CursorRow = 0
		}
		e.clampCol()
	case "alt+down":
		e.Sel = nil
		step := e.PageSize
		if step <= 0 {
			step = editorDefaultPageSize
		}
		e.CursorRow += step
		if e.CursorRow > len(e.Lines)-1 {
			e.CursorRow = len(e.Lines) - 1
		}
		e.clampCol()

	case "tab":
		if e.ReadOnly {
			break
		}
		e.clearSel()
		e.insertText("  ")
		e.Dirty = true
	case "space":
		if e.ReadOnly {
			break
		}
		e.clearSel()
		e.insertText(" ")
		e.Dirty = true

	default:
		if e.ReadOnly {
			break
		}
		if utf8.RuneCountInString(key) == 1 {
			r, _ := utf8.DecodeRuneInString(key)
			if r >= 32 {
				e.clearSel()
				e.insertText(key)
				e.Dirty = true
			}
		}
	}

	e.ensureCursorVisible()
	return false, false, nil
}

// isSelectionKey returns true for shift-modified keys that extend selection.
func (e *TextEditor) isSelectionKey(key string) bool {
	switch key {
	case "shift+right", "shift+left", "shift+up", "shift+down",
		"ctrl+shift+right", "ctrl+shift+left",
		"ctrl+alt+shift+right", "ctrl+alt+shift+left",
		"ctrl+shift+up", "ctrl+shift+down",
		"shift+home", "shift+end":
		return true
	}
	return false
}

// handleSelectionKey initializes or extends the selection.
func (e *TextEditor) handleSelectionKey(key string) {
	// Initialize selection at current cursor if none exists
	if e.Sel == nil {
		pos := EditorPos{Row: e.CursorRow, Col: e.CursorCol}
		e.Sel = &EditorSel{Anchor: pos, Cursor: pos}
	}

	cur := e.Sel.Cursor

	switch key {
	case "shift+right":
		if cur.Col < runeLen(e.Lines[cur.Row]) {
			cur.Col++
		} else if cur.Row < len(e.Lines)-1 {
			cur.Row++
			cur.Col = 0
		}

	case "shift+left":
		if cur.Col > 0 {
			cur.Col--
		} else if cur.Row > 0 {
			cur.Row--
			cur.Col = runeLen(e.Lines[cur.Row])
		}

	case "shift+up":
		if cur.Row > 0 {
			cur.Row--
			rl := runeLen(e.Lines[cur.Row])
			if cur.Col > rl {
				cur.Col = rl
			}
		}

	case "shift+down":
		if cur.Row < len(e.Lines)-1 {
			cur.Row++
			rl := runeLen(e.Lines[cur.Row])
			if cur.Col > rl {
				cur.Col = rl
			}
		}

	case "ctrl+shift+right":
		cur = editorWordJump(e.Lines, cur, 1, 1)

	case "ctrl+shift+left":
		cur = editorWordJump(e.Lines, cur, -1, 1)

	case "ctrl+alt+shift+right":
		cur = editorWordJump(e.Lines, cur, 1, 3)

	case "ctrl+alt+shift+left":
		cur = editorWordJump(e.Lines, cur, -1, 3)

	case "ctrl+shift+up":
		cur.Row = editorParagraphJump(e.Lines, cur.Row, -1)
		cur.Col = 0

	case "ctrl+shift+down":
		cur.Row = editorParagraphJump(e.Lines, cur.Row, 1)
		cur.Col = 0

	case "shift+home":
		cur.Col = 0

	case "shift+end":
		cur.Col = runeLen(e.Lines[cur.Row])
	}

	e.Sel.Cursor = cur
	e.CursorRow = cur.Row
	e.CursorCol = cur.Col
}

// clearSel deletes the active selection text if present, returning true if it did.
func (e *TextEditor) clearSel() bool {
	if e.Sel != nil && !e.Sel.IsEmpty() {
		e.deleteSelection()
		return true
	}
	return false
}

// selectAll selects the entire document.
func (e *TextEditor) selectAll() {
	lastRow := len(e.Lines) - 1
	lastCol := runeLen(e.Lines[lastRow])
	e.Sel = &EditorSel{
		Anchor: EditorPos{Row: 0, Col: 0},
		Cursor: EditorPos{Row: lastRow, Col: lastCol},
	}
	e.CursorRow = lastRow
	e.CursorCol = lastCol
}

// deleteSelection removes the selected text and places cursor at selection start.
func (e *TextEditor) deleteSelection() {
	if e.Sel == nil || e.Sel.IsEmpty() {
		e.Sel = nil
		return
	}
	start, end := e.Sel.Normalized()
	e.Sel = nil

	if start.Row == end.Row {
		// Same-line deletion
		line := e.Lines[start.Row]
		runes := []rune(line)
		sc := clampInt(start.Col, 0, len(runes))
		ec := clampInt(end.Col, 0, len(runes))
		e.Lines[start.Row] = string(runes[:sc]) + string(runes[ec:])
	} else {
		// Multi-line deletion: merge first line prefix + last line suffix
		firstRunes := []rune(e.Lines[start.Row])
		lastRunes := []rune(e.Lines[end.Row])
		sc := clampInt(start.Col, 0, len(firstRunes))
		ec := clampInt(end.Col, 0, len(lastRunes))

		merged := string(firstRunes[:sc]) + string(lastRunes[ec:])
		newLines := make([]string, 0, len(e.Lines)-(end.Row-start.Row))
		newLines = append(newLines, e.Lines[:start.Row]...)
		newLines = append(newLines, merged)
		newLines = append(newLines, e.Lines[end.Row+1:]...)
		e.Lines = newLines
	}

	e.CursorRow = start.Row
	e.CursorCol = start.Col
	e.clampCol()
}

// InsertMultiLine inserts text that may contain newlines at the cursor position.
// If a selection is active, it is deleted first. No-op when ReadOnly.
func (e *TextEditor) InsertMultiLine(text string) {
	if e.ReadOnly {
		return
	}
	e.clearSel()

	text = strings.ReplaceAll(text, "\r", "")
	parts := strings.Split(text, "\n")

	if len(parts) == 1 {
		// Single-line insert
		e.insertText(parts[0])
		return
	}

	// Multi-line insert: split current line at cursor
	line := e.Lines[e.CursorRow]
	col := e.CursorCol
	rl := runeLen(line)
	if col > rl {
		col = rl
	}
	byteOff := runeOffset(line, col)
	before := line[:byteOff]
	after := line[byteOff:]

	// Build new lines
	newLines := make([]string, 0, len(e.Lines)+len(parts)-1)
	newLines = append(newLines, e.Lines[:e.CursorRow]...)
	newLines = append(newLines, before+parts[0]) // first fragment merges with before
	for _, mid := range parts[1 : len(parts)-1] {
		newLines = append(newLines, mid)
	}
	lastPart := parts[len(parts)-1]
	newLines = append(newLines, lastPart+after) // last fragment gets after appended
	newLines = append(newLines, e.Lines[e.CursorRow+1:]...)

	e.Lines = newLines
	e.CursorRow = e.CursorRow + len(parts) - 1
	e.CursorCol = runeLen(lastPart)
}

// pasteCmd returns an async command that reads the clipboard.
func (e *TextEditor) pasteCmd() tea.Cmd {
	return func() tea.Msg {
		text, err := clipboard.Read()
		if err != nil {
			log.Printf("editor clipboard read: %v", err)
			return nil
		}
		if text == "" {
			return nil
		}
		return editorPasteMsg(text)
	}
}

// --- Text operations (rune-aware) ---

func (e *TextEditor) insertText(s string) {
	line := e.Lines[e.CursorRow]
	col := e.CursorCol
	rl := runeLen(line)
	if col > rl {
		col = rl
	}
	byteOff := runeOffset(line, col)
	e.Lines[e.CursorRow] = line[:byteOff] + s + line[byteOff:]
	e.CursorCol = col + runeLen(s)
}

func (e *TextEditor) insertNewline() {
	line := e.Lines[e.CursorRow]
	col := e.CursorCol
	rl := runeLen(line)
	if col > rl {
		col = rl
	}
	byteOff := runeOffset(line, col)
	before := line[:byteOff]
	after := line[byteOff:]
	e.Lines[e.CursorRow] = before
	newLines := make([]string, 0, len(e.Lines)+1)
	newLines = append(newLines, e.Lines[:e.CursorRow+1]...)
	newLines = append(newLines, after)
	newLines = append(newLines, e.Lines[e.CursorRow+1:]...)
	e.Lines = newLines
	e.CursorRow++
	e.CursorCol = 0
}

func (e *TextEditor) backspace() {
	if e.CursorCol > 0 {
		line := e.Lines[e.CursorRow]
		col := e.CursorCol
		rl := runeLen(line)
		if col > rl {
			col = rl
		}
		prevOff := runeOffset(line, col-1)
		curOff := runeOffset(line, col)
		e.Lines[e.CursorRow] = line[:prevOff] + line[curOff:]
		e.CursorCol = col - 1
	} else if e.CursorRow > 0 {
		prevLine := e.Lines[e.CursorRow-1]
		curLine := e.Lines[e.CursorRow]
		e.CursorCol = runeLen(prevLine)
		e.Lines[e.CursorRow-1] = prevLine + curLine
		e.Lines = append(e.Lines[:e.CursorRow], e.Lines[e.CursorRow+1:]...)
		e.CursorRow--
	}
}

func (e *TextEditor) deleteChar() {
	line := e.Lines[e.CursorRow]
	rl := runeLen(line)
	if e.CursorCol < rl {
		curOff := runeOffset(line, e.CursorCol)
		nextOff := runeOffset(line, e.CursorCol+1)
		e.Lines[e.CursorRow] = line[:curOff] + line[nextOff:]
	} else if e.CursorRow < len(e.Lines)-1 {
		e.Lines[e.CursorRow] = line + e.Lines[e.CursorRow+1]
		e.Lines = append(e.Lines[:e.CursorRow+1], e.Lines[e.CursorRow+2:]...)
	}
}

func (e *TextEditor) clampCol() {
	rl := runeLen(e.Lines[e.CursorRow])
	if e.CursorCol > rl {
		e.CursorCol = rl
	}
}

func (e *TextEditor) ensureCursorVisible() {
	if e.CursorRow < e.ScrollTop {
		e.ScrollTop = e.CursorRow
	}
	if e.CursorRow >= e.ScrollTop+e.ViewHeight {
		e.ScrollTop = e.CursorRow - e.ViewHeight + 1
	}
}

// Content returns raw text (no ANSI codes) for saving.
func (e *TextEditor) Content() string {
	return strings.Join(e.Lines, "\n")
}

// Save validates TOML syntax and writes to disk atomically.
func (e *TextEditor) Save() error {
	content := e.Content()

	var test map[string]any
	if err := toml.Unmarshal([]byte(content), &test); err != nil {
		return fmt.Errorf("TOML syntax error: %w", err)
	}

	// Path containment check
	absPath, err := filepath.Abs(e.FilePath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	pluginsDir, err := filepath.Abs(config.PluginsDir())
	if err != nil {
		return fmt.Errorf("resolve plugins dir: %w", err)
	}
	if !strings.HasPrefix(absPath, pluginsDir+string(filepath.Separator)) {
		return fmt.Errorf("path outside plugins directory")
	}

	tmpPath := e.FilePath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(content), 0600); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := os.Rename(tmpPath, e.FilePath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	e.Dirty = false
	return nil
}

// GutterWidth returns the visible width (in columns) of the line-number
// gutter for the current document. It is `max(3, digits(len(Lines))) + 1`
// — three digits minimum plus one trailing space. Both Render() and the
// mouse-to-document coordinate helper (notesEditorPosAt) use this so
// the body content's left edge stays in sync with what the user sees.
func (e *TextEditor) GutterWidth() int {
	n := len(e.Lines)
	if n < 1 {
		n = 1
	}
	digits := 1
	for n >= 10 {
		n /= 10
		digits++
	}
	if digits < 3 {
		digits = 3
	}
	return digits + 1 // +1 for the trailing space
}

// --- Rendering ---

func (e *TextEditor) Render() string {
	var b strings.Builder

	gutter := e.GutterWidth()
	contentW := e.ViewWidth - gutter - 1 // -1 defensive pad for cursor overflow
	if contentW < 10 {
		contentW = 10
	}

	end := e.ScrollTop + e.ViewHeight
	if end > len(e.Lines) {
		end = len(e.Lines)
	}

	hasSel := e.Sel != nil && !e.Sel.IsEmpty()

	// Build the line-number format string once per render so long
	// documents get a wider gutter without re-formatting per line.
	lineNumFmt := fmt.Sprintf("\x1b[90m%%%dd \x1b[0m", gutter-1)

	for i := e.ScrollTop; i < end; i++ {
		rawLine := e.Lines[i]

		// Truncate by rune count
		runes := []rune(rawLine)
		truncated := false
		if len(runes) > contentW {
			runes = runes[:contentW-1]
			truncated = true
		}
		displayRaw := string(runes)
		if truncated {
			displayRaw += "~"
		}

		lineNum := fmt.Sprintf(lineNumFmt, i+1)

		// Check if this line has selection
		selStart, selEnd := -1, -1
		if hasSel {
			selStart, selEnd = e.Sel.ColRange(i, runeLen(rawLine))
			// Clamp to display width
			if selStart >= 0 {
				displayRL := len([]rune(displayRaw))
				if selStart > displayRL {
					selStart = -1
					selEnd = -1
				} else if selEnd > displayRL {
					selEnd = displayRL
				}
			}
		}

		if selStart >= 0 {
			// Line has selection — render with selection highlight
			b.WriteString(lineNum)
			e.renderLineWithSelection(&b, i, displayRaw, contentW, selStart, selEnd)
			b.WriteByte('\n')
		} else if i == e.CursorRow {
			// Cursor line: render with cursor highlight
			b.WriteString(lineNum)
			e.renderCursorLine(&b, displayRaw, contentW)
			b.WriteByte('\n')
		} else {
			// Non-cursor line: apply syntax highlighting then pad
			highlighted := e.highlight(displayRaw)
			visW := ansi.StringWidth(displayRaw)
			pad := ""
			if visW < contentW {
				pad = strings.Repeat(" ", contentW-visW)
			}
			b.WriteString(lineNum + highlighted + pad + "\n")
		}
	}

	for i := end; i < e.ScrollTop+e.ViewHeight; i++ {
		b.WriteString("\x1b[90m  ~ \x1b[0m\n")
	}

	return b.String()
}

// renderCursorLine renders the current line with cursor highlight and syntax colors.
func (e *TextEditor) renderCursorLine(b *strings.Builder, displayRaw string, contentW int) {
	runes := []rune(displayRaw)
	col := e.CursorCol
	if col > len(runes) {
		col = len(runes)
	}

	// Split into before-cursor, cursor char, after-cursor (all as raw strings)
	beforeRunes := runes[:col]
	var cursorRune rune = ' '
	var afterRunes []rune
	if col < len(runes) {
		cursorRune = runes[col]
		afterRunes = runes[col+1:]
	}

	// Apply highlighting to the parts
	beforeStr := string(beforeRunes)
	afterStr := string(afterRunes)

	b.WriteString("\x1b[97m") // bright white for cursor line
	b.WriteString(beforeStr)
	b.WriteString("\x1b[7m") // reverse video (cursor)
	b.WriteRune(cursorRune)
	b.WriteString("\x1b[27m") // end reverse
	b.WriteString(afterStr)
	b.WriteString("\x1b[0m") // reset

	// Pad remaining width
	visW := ansi.StringWidth(displayRaw)
	if col >= len(runes) {
		visW++ // cursor on empty space adds 1
	}
	if visW < contentW {
		b.WriteString(strings.Repeat(" ", contentW-visW))
	}
}

// renderLineWithSelection renders a line with selection highlight.
// selStart/selEnd are rune-based column indices (endCol is exclusive).
func (e *TextEditor) renderLineWithSelection(b *strings.Builder, lineIdx int, displayRaw string, contentW, selStart, selEnd int) {
	runes := []rune(displayRaw)
	rl := len(runes)
	isCursorLine := lineIdx == e.CursorRow
	cursorCol := e.CursorCol
	if cursorCol > rl {
		cursorCol = rl
	}

	// Clamp selection bounds to display
	if selStart < 0 {
		selStart = 0
	}
	if selEnd > rl {
		selEnd = rl
	}

	// Before selection
	if selStart > 0 {
		before := string(runes[:selStart])
		if isCursorLine {
			b.WriteString("\x1b[97m") // bright white
			b.WriteString(before)
			b.WriteString("\x1b[0m")
		} else {
			b.WriteString(e.highlight(before))
		}
	}

	// Selected region
	for i := selStart; i < selEnd && i < rl; i++ {
		if isCursorLine && i == cursorCol {
			// Cursor within selection: reverse + underline
			b.WriteString("\x1b[7;4m")
			b.WriteRune(runes[i])
			b.WriteString("\x1b[0m")
		} else {
			b.WriteString("\x1b[7m")
			b.WriteRune(runes[i])
			b.WriteString("\x1b[0m")
		}
	}

	// Cursor at end of selection (past last char)
	if isCursorLine && cursorCol == selEnd && cursorCol >= rl {
		b.WriteString("\x1b[7;4m \x1b[0m")
	}

	// After selection
	if selEnd < rl {
		after := string(runes[selEnd:])
		if isCursorLine {
			// Render cursor if it's in the after portion
			if cursorCol >= selEnd {
				afterRunes := runes[selEnd:]
				cursorInAfter := cursorCol - selEnd
				b.WriteString("\x1b[97m")
				if cursorInAfter < len(afterRunes) {
					b.WriteString(string(afterRunes[:cursorInAfter]))
					b.WriteString("\x1b[7m")
					b.WriteRune(afterRunes[cursorInAfter])
					b.WriteString("\x1b[27m")
					if cursorInAfter+1 < len(afterRunes) {
						b.WriteString(string(afterRunes[cursorInAfter+1:]))
					}
				} else {
					b.WriteString(string(afterRunes))
				}
				b.WriteString("\x1b[0m")
			} else {
				b.WriteString("\x1b[97m")
				b.WriteString(after)
				b.WriteString("\x1b[0m")
			}
		} else {
			b.WriteString(e.highlight(after))
		}
	}

	// Pad remaining width
	visW := ansi.StringWidth(displayRaw)
	extraCursor := 0
	if isCursorLine && cursorCol >= rl {
		extraCursor = 1
	}
	if visW+extraCursor < contentW {
		b.WriteString(strings.Repeat(" ", contentW-visW-extraCursor))
	}
}

// --- TOML Syntax Highlighting ---

func highlightTOML(line string) string {
	trimmed := strings.TrimSpace(line)

	// Comment line
	if strings.HasPrefix(trimmed, "#") {
		return "\x1b[90m" + line + "\x1b[0m"
	}

	// Section header [section] or [[array]]
	if strings.HasPrefix(trimmed, "[") {
		return "\x1b[38;5;208m" + line + "\x1b[0m"
	}

	// Key = value
	eqIdx := strings.Index(line, "=")
	if eqIdx > 0 {
		key := line[:eqIdx]
		rest := line[eqIdx:]
		return "\x1b[34m" + key + "\x1b[0m" + highlightStrings(rest)
	}

	return line
}

// highlightStrings colors double-quoted strings light green and single-quoted dark green.
func highlightStrings(s string) string {
	var b strings.Builder
	i := 0
	runes := []rune(s)

	for i < len(runes) {
		ch := runes[i]

		if ch == '"' {
			// Double-quoted string
			b.WriteString("\x1b[92m") // light green
			b.WriteRune(ch)
			i++
			for i < len(runes) {
				b.WriteRune(runes[i])
				if runes[i] == '"' && (i == 0 || runes[i-1] != '\\') {
					i++
					break
				}
				i++
			}
			b.WriteString("\x1b[0m")
		} else if ch == '\'' {
			// Single-quoted string
			b.WriteString("\x1b[32m") // dark green
			b.WriteRune(ch)
			i++
			for i < len(runes) {
				b.WriteRune(runes[i])
				if runes[i] == '\'' {
					i++
					break
				}
				i++
			}
			b.WriteString("\x1b[0m")
		} else {
			b.WriteRune(ch)
			i++
		}
	}

	return b.String()
}
