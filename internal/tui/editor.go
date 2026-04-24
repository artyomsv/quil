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
	// SoftWrap makes long logical lines wrap onto the next visual row
	// instead of being hard-truncated with a trailing "~". When enabled,
	// ScrollTop is a visual-row index and cursor Up/Down/Home/End work on
	// visual rows. Paragraph jumps (ctrl+up/down) and PageSize jumps
	// (alt+up/down) remain logical-line based. Only NotesEditor opts in
	// — the TOML plugin editor and F1 log viewer keep truncation.
	SoftWrap bool
}

// visualRow maps a visual row (what the user sees on screen) back to a
// slice [Start, End) of runes within a logical line. With SoftWrap off
// there is exactly one visualRow per logical line (Start=0, End=runeLen);
// with SoftWrap on, each logical line produces ceil(runeLen/contentW)
// visualRows (minimum 1 for empty lines).
type visualRow struct {
	Logical int // index into Lines
	Start   int // first rune in the logical line (inclusive)
	End     int // last rune (exclusive)
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
		e.verticalMove(-1)
	case "down":
		e.Sel = nil
		e.verticalMove(1)
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
		if e.SoftWrap {
			layout := e.visualLayout(e.contentWForLayout())
			vi := e.cursorVisualRow(layout)
			e.CursorCol = layout[vi].Start
		} else {
			e.CursorCol = 0
		}
	case "end":
		e.Sel = nil
		if e.SoftWrap {
			layout := e.visualLayout(e.contentWForLayout())
			vi := e.cursorVisualRow(layout)
			e.CursorCol = layout[vi].End
		} else {
			e.CursorCol = runeLen(e.Lines[e.CursorRow])
		}

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
	if !e.SoftWrap {
		if e.CursorRow < e.ScrollTop {
			e.ScrollTop = e.CursorRow
		}
		if e.CursorRow >= e.ScrollTop+e.ViewHeight {
			e.ScrollTop = e.CursorRow - e.ViewHeight + 1
		}
		if e.ScrollTop < 0 {
			e.ScrollTop = 0
		}
		return
	}
	layout := e.visualLayout(e.contentWForLayout())
	vi := e.cursorVisualRow(layout)
	if vi < e.ScrollTop {
		e.ScrollTop = vi
	}
	if vi >= e.ScrollTop+e.ViewHeight {
		e.ScrollTop = vi - e.ViewHeight + 1
	}
	if e.ScrollTop < 0 {
		e.ScrollTop = 0
	}
}

// verticalMove moves the cursor one visual row in the given direction
// (-1 up, +1 down). Preserves the visual column where possible so
// wrapped-line navigation feels natural. Falls back to logical-row
// stepping when SoftWrap is off.
func (e *TextEditor) verticalMove(dir int) {
	if !e.SoftWrap {
		target := e.CursorRow + dir
		if target < 0 || target >= len(e.Lines) {
			return
		}
		e.CursorRow = target
		e.clampCol()
		return
	}
	layout := e.visualLayout(e.contentWForLayout())
	if len(layout) == 0 {
		return
	}
	vi := e.cursorVisualRow(layout)
	nv := vi + dir
	if nv < 0 || nv >= len(layout) {
		return
	}
	vcol := e.CursorCol - layout[vi].Start
	row, col := e.visualToLogical(layout, nv, vcol)
	e.CursorRow = row
	e.CursorCol = col
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

// contentWForLayout returns the usable content width (columns) for
// rendering and wrap calculations, matching what Render() uses.
func (e *TextEditor) contentWForLayout() int {
	cw := e.ViewWidth - e.GutterWidth() - 1
	if cw < 10 {
		cw = 10
	}
	return cw
}

// visualLayout expands Lines into visual rows. When SoftWrap is off the
// result is 1:1 (kept so callers have a single code path). When on, long
// lines are split at contentW rune boundaries.
func (e *TextEditor) visualLayout(contentW int) []visualRow {
	if contentW < 1 {
		contentW = 1
	}
	out := make([]visualRow, 0, len(e.Lines))
	if !e.SoftWrap {
		for i, line := range e.Lines {
			out = append(out, visualRow{Logical: i, Start: 0, End: runeLen(line)})
		}
		return out
	}
	for i, line := range e.Lines {
		rl := runeLen(line)
		if rl == 0 {
			out = append(out, visualRow{Logical: i, Start: 0, End: 0})
			continue
		}
		for start := 0; start < rl; start += contentW {
			end := start + contentW
			if end > rl {
				end = rl
			}
			out = append(out, visualRow{Logical: i, Start: start, End: end})
		}
	}
	return out
}

// cursorVisualRow returns the visual-row index in layout that owns the
// current cursor position. A cursor sitting exactly at End of a visual
// row is attributed to that row only if it's the last visual row for
// the logical line (end-of-line); otherwise it belongs to the next row.
// Defensive: if CursorRow is out of range or no matching row is found,
// returns the clamped index closest to the cursor's logical row so the
// caller never derefs layout[0] for an unrelated line.
func (e *TextEditor) cursorVisualRow(layout []visualRow) int {
	if len(layout) == 0 {
		return 0
	}
	for vi, vr := range layout {
		if vr.Logical != e.CursorRow {
			continue
		}
		if e.CursorCol >= vr.Start && e.CursorCol < vr.End {
			return vi
		}
		if e.CursorCol == vr.End {
			// cursor sits on the boundary — last visual row of this
			// logical line gets it (end-of-line cursor).
			if vi == len(layout)-1 || layout[vi+1].Logical != vr.Logical {
				return vi
			}
		}
	}
	// Fallback: find the first visual row whose Logical >= CursorRow,
	// or the last row if CursorRow is past the document.
	for vi, vr := range layout {
		if vr.Logical >= e.CursorRow {
			return vi
		}
	}
	return len(layout) - 1
}

// visualToLogical converts a visual (row, col) screen position back into
// a logical (row, col) position in Lines. Clamps vrow to the valid range
// and vcol to the visual row's width.
func (e *TextEditor) visualToLogical(layout []visualRow, vrow, vcol int) (row, col int) {
	if len(layout) == 0 {
		return 0, 0
	}
	if vrow < 0 {
		vrow = 0
	}
	if vrow >= len(layout) {
		vrow = len(layout) - 1
	}
	vr := layout[vrow]
	if vcol < 0 {
		vcol = 0
	}
	width := vr.End - vr.Start
	if vcol > width {
		vcol = width
	}
	return vr.Logical, vr.Start + vcol
}

// --- Rendering ---

func (e *TextEditor) Render() string {
	var b strings.Builder

	gutter := e.GutterWidth()
	contentW := e.contentWForLayout()
	layout := e.visualLayout(contentW)

	end := e.ScrollTop + e.ViewHeight
	if end > len(layout) {
		end = len(layout)
	}
	hasSel := e.Sel != nil && !e.Sel.IsEmpty()

	// Build the gutter strings once: the line-number format picks up
	// the widest digit count; wrapped continuations share a single
	// blank-spaces buffer.
	lineNumFmt := fmt.Sprintf("\x1b[90m%%%dd \x1b[0m", gutter-1)
	blankGutter := strings.Repeat(" ", gutter)

	for vi := e.ScrollTop; vi < end; vi++ {
		e.renderVisualRow(&b, layout, vi, contentW, lineNumFmt, blankGutter, hasSel)
	}

	for i := end; i < e.ScrollTop+e.ViewHeight; i++ {
		b.WriteString("\x1b[90m  ~ \x1b[0m\n")
	}
	return b.String()
}

// renderVisualRow renders a single visual row into b. Factored out of
// Render so the loop body fits on one screen: slice the logical line
// to the visual window, resolve cursor/selection attribution for this
// row, then dispatch to the selection/cursor/plain renderer.
func (e *TextEditor) renderVisualRow(b *strings.Builder, layout []visualRow, vi, contentW int, lineNumFmt, blankGutter string, hasSel bool) {
	vr := layout[vi]
	rawLine := e.Lines[vr.Logical]
	rawRunes := []rune(rawLine)
	sliceRunes := rawRunes[vr.Start:vr.End]

	// Non-softwrap: hard-truncate oversize rows with "~" (legacy
	// TOML-editor and log-viewer behavior). With SoftWrap on, the
	// layout already splits at contentW so no truncation fires.
	truncated := false
	if !e.SoftWrap && len(sliceRunes) > contentW {
		sliceRunes = sliceRunes[:contentW-1]
		truncated = true
	}
	displayRaw := string(sliceRunes)
	if truncated {
		displayRaw += "~"
	}
	displayRL := len([]rune(displayRaw))
	rowWidth := vr.End - vr.Start

	// Gutter: line number on the first visual row of each logical line,
	// blank spaces on wrapped continuations.
	if vr.Start == 0 {
		b.WriteString(fmt.Sprintf(lineNumFmt, vr.Logical+1))
	} else {
		b.WriteString(blankGutter)
	}

	onCursorLine := false
	if vr.Logical == e.CursorRow {
		if e.CursorCol >= vr.Start && e.CursorCol < vr.End {
			onCursorLine = true
		} else if e.CursorCol == vr.End {
			// End-of-row position belongs to this row only if no
			// continuation row for the same logical line follows.
			if vi == len(layout)-1 || layout[vi+1].Logical != vr.Logical {
				onCursorLine = true
			}
		}
	}
	localCursorCol := e.CursorCol - vr.Start
	if localCursorCol < 0 {
		localCursorCol = 0
	}
	if localCursorCol > displayRL {
		localCursorCol = displayRL
	}

	selStart, selEnd := -1, -1
	if hasSel {
		gs, ge := e.Sel.ColRange(vr.Logical, len(rawRunes))
		if gs >= 0 && ge > vr.Start && gs < vr.End {
			selStart = gs - vr.Start
			if selStart < 0 {
				selStart = 0
			}
			selEnd = ge - vr.Start
			if selEnd > rowWidth {
				selEnd = rowWidth
			}
			if selStart > displayRL {
				selStart = -1
				selEnd = -1
			} else if selEnd > displayRL {
				selEnd = displayRL
			}
		}
	}

	switch {
	case selStart >= 0:
		e.renderLineWithSelection(b, displayRaw, contentW, selStart, selEnd, onCursorLine, localCursorCol)
	case onCursorLine:
		e.renderCursorLine(b, displayRaw, contentW, localCursorCol)
	default:
		highlighted := e.highlight(displayRaw)
		visW := ansi.StringWidth(displayRaw)
		b.WriteString(highlighted)
		if visW < contentW {
			b.WriteString(strings.Repeat(" ", contentW-visW))
		}
	}
	b.WriteByte('\n')
}

// renderCursorLine renders the current line with cursor highlight and syntax colors.
// cursorCol is the rune column within displayRaw where the cursor sits
// (already translated from the logical CursorCol by the caller).
func (e *TextEditor) renderCursorLine(b *strings.Builder, displayRaw string, contentW, cursorCol int) {
	runes := []rune(displayRaw)
	col := cursorCol
	if col < 0 {
		col = 0
	}
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
// selStart/selEnd are rune-based column indices within displayRaw (endCol
// is exclusive). isCursorLine and cursorCol are pre-translated by the
// caller (which knows whether the cursor lives on this visual row and
// where inside the sliced displayRaw it sits).
func (e *TextEditor) renderLineWithSelection(b *strings.Builder, displayRaw string, contentW, selStart, selEnd int, isCursorLine bool, cursorCol int) {
	runes := []rune(displayRaw)
	rl := len(runes)
	if cursorCol < 0 {
		cursorCol = 0
	}
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
					b.WriteString("\x1b[0m")
				} else {
					// Cursor sits past the last rune of the after-run.
					// Close the 97m run, then paint a reverse-video
					// space as the cursor glyph. Without this the
					// padding below reserves the slot (extraCursor=1)
					// but nothing visible lands on it.
					b.WriteString(string(afterRunes))
					b.WriteString("\x1b[0m\x1b[7m \x1b[27m")
				}
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
