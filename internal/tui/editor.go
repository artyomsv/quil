package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
	"github.com/charmbracelet/x/ansi"

	"github.com/artyomsv/aethel/internal/config"
)

// TextEditor is a minimal multi-line text editor with TOML syntax highlighting.
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

func (e *TextEditor) HandleKey(key string) (saved, closed bool) {
	e.SaveErr = ""

	switch key {
	case "esc":
		return false, true
	case "ctrl+s":
		if err := e.Save(); err != nil {
			e.SaveErr = err.Error()
			return false, false
		}
		return true, false

	case "up":
		if e.CursorRow > 0 {
			e.CursorRow--
			e.clampCol()
		}
	case "down":
		if e.CursorRow < len(e.Lines)-1 {
			e.CursorRow++
			e.clampCol()
		}
	case "left":
		if e.CursorCol > 0 {
			e.CursorCol--
		} else if e.CursorRow > 0 {
			e.CursorRow--
			e.CursorCol = runeLen(e.Lines[e.CursorRow])
		}
	case "right":
		if e.CursorCol < runeLen(e.Lines[e.CursorRow]) {
			e.CursorCol++
		} else if e.CursorRow < len(e.Lines)-1 {
			e.CursorRow++
			e.CursorCol = 0
		}
	case "home":
		e.CursorCol = 0
	case "end":
		e.CursorCol = runeLen(e.Lines[e.CursorRow])

	case "enter":
		e.insertNewline()
		e.Dirty = true
	case "backspace":
		e.backspace()
		e.Dirty = true
	case "delete":
		e.deleteChar()
		e.Dirty = true
	case "tab":
		e.insertText("  ")
		e.Dirty = true
	case "space":
		e.insertText(" ")
		e.Dirty = true
	default:
		if utf8.RuneCountInString(key) == 1 {
			r, _ := utf8.DecodeRuneInString(key)
			if r >= 32 {
				e.insertText(key)
				e.Dirty = true
			}
		}
	}

	e.ensureCursorVisible()
	return false, false
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

// --- Rendering ---

func (e *TextEditor) Render() string {
	var b strings.Builder

	contentW := e.ViewWidth - 5 // "NNN " prefix
	if contentW < 10 {
		contentW = 10
	}

	end := e.ScrollTop + e.ViewHeight
	if end > len(e.Lines) {
		end = len(e.Lines)
	}

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

		lineNum := fmt.Sprintf("\x1b[90m%3d \x1b[0m", i+1)

		if i == e.CursorRow {
			// Cursor line: render with cursor highlight
			b.WriteString(lineNum)
			e.renderCursorLine(&b, displayRaw, contentW)
			b.WriteByte('\n')
		} else {
			// Non-cursor line: apply syntax highlighting then pad
			highlighted := highlightTOML(displayRaw)
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
