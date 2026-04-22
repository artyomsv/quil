package tui

import (
	"errors"
	"image/color"
	"io"
	"log"
	"net/url"
	"strings"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/artyomsv/quil/internal/ringbuf"
)

// drainEmulatorReplies reads and discards the emulator's reply stream.
// The vt.Emulator replies to DECRQM / DSR / cursor-position queries by
// writing to an internal unbuffered io.Pipe. If nothing reads that pipe the
// writer (inside Emulator.Write, which runs on Update's goroutine under the
// SafeEmulator mutex) blocks forever — freezing the entire TUI. We are a
// renderer, not a real terminal; the real ConPTY already handled the query.
//
// Shutdown contract: the goroutine has no context.Context by design — its
// lifecycle is bound to the emulator. Callers MUST call em.Close() before
// dropping the emulator pointer; Close closes the pipe's write side, Read
// returns io.EOF (or io.ErrClosedPipe), and the goroutine exits. See
// ResetVT for the caller-side invariant. Any other error is logged once
// and the goroutine exits — the pipe is gone either way, and if a future
// library change exits this drain for a transient reason we want a
// breadcrumb instead of a silent re-wedge.
func drainEmulatorReplies(em *vt.SafeEmulator) {
	buf := make([]byte, 4096)
	for {
		if _, err := em.Read(buf); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
				return
			}
			log.Printf("pane: emulator drain exited: %v", err)
			return
		}
	}
}

// spinnerFrames are braille characters cycled for the resuming indicator.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type PaneModel struct {
	ID            string
	Type          string // plugin type ("terminal", "claude-code", etc.)
	Name          string // user-given name (empty if not set)
	CWD           string // current working directory from daemon
	vt            *vt.SafeEmulator
	Width         int
	Height        int
	Active        bool
	scrollBack    int
	rawBuf        *ringbuf.RingBuffer // raw PTY bytes for resize replay
	cursorVisible bool                // tracks shell's DECTCEM state
	ghost         bool                // true while showing restored content
	resuming      bool                // true while waiting for first live output after restore
	preparing     bool                // true for newly created panes (not restored)
	resumeStart   time.Time           // when resuming/preparing started (minimum display duration)
	spinnerFrame  int                 // current frame index in spinnerFrames
	activeSel     *Selection          // set by Model before View() for selection rendering
	focusMode     bool                // set by Model before View() when in focus mode
	mcpHighlight  bool                // set by Model before View() when MCP is interacting
}

func NewPaneModel(id string, bufSize int) *PaneModel {
	p := &PaneModel{
		ID:            id,
		Name:          "",
		rawBuf:        ringbuf.NewRingBuffer(bufSize),
		cursorVisible: true, // visible by default (matches terminal default)
	}
	em := vt.NewSafeEmulator(80, 24)
	em.SetScrollbackSize(10000)
	em.SetCallbacks(vt.Callbacks{
		CursorVisibility: func(visible bool) {
			p.cursorVisible = visible
		},
		WorkingDirectory: func(dir string) {
			p.CWD = parseOSC7Path(dir)
		},
	})
	p.vt = em
	go drainEmulatorReplies(em)
	return p
}

func (p *PaneModel) AppendOutput(data []byte) {
	p.rawBuf.Write(data)
	p.vt.Write(data)
}

// ResetVT creates a fresh VT emulator at the current dimensions, clearing
// ghost buffer state so live output starts with a clean cursor position.
func (p *PaneModel) ResetVT() {
	// NewPaneModel is the sole constructor and always sets p.vt, so no nil
	// guard is needed here. Closing the old emulator before dropping the
	// pointer is load-bearing: Close closes the pipe's write side, which
	// signals the existing drain goroutine to exit via EOF. Skipping it
	// would leak one goroutine per ResetVT.
	w, h := p.vt.Width(), p.vt.Height()
	_ = p.vt.Close()
	em := vt.NewSafeEmulator(w, h)
	em.SetScrollbackSize(10000)
	em.SetCallbacks(vt.Callbacks{
		CursorVisibility: func(visible bool) {
			p.cursorVisible = visible
		},
		WorkingDirectory: func(dir string) {
			p.CWD = parseOSC7Path(dir)
		},
	})
	p.vt = em
	go drainEmulatorReplies(em)
	p.rawBuf.Reset()
	p.cursorVisible = true
}

func (p *PaneModel) ResizeVT(cols, rows int) {
	if cols <= 0 || rows <= 0 || (cols == p.vt.Width() && rows == p.vt.Height()) {
		return
	}
	// Resize the emulator in place instead of rebuilding it from the raw PTY
	// ring buffer. Historical bytes from TUI apps (Claude Code, vim, htop,
	// fzf) contain CUP / scroll-region sequences laid out for the previous
	// width; replaying them into a freshly-sized emulator stamps narrow-
	// column ghost rows into the new screen. The x/vt library's Resize
	// preserves the current screen state, and the PTY child will redraw via
	// SIGWINCH (triggered separately by MsgResizePane) into the new size.
	p.vt.Resize(cols, rows)
}

func (p *PaneModel) ScrollUp(lines int) {
	p.scrollBack += lines
	if max := p.vt.ScrollbackLen(); p.scrollBack > max {
		p.scrollBack = max
	}
}

func (p *PaneModel) ScrollDown(lines int) {
	p.scrollBack -= lines
	if p.scrollBack < 0 {
		p.scrollBack = 0
	}
}

func (p *PaneModel) ResetScroll() {
	p.scrollBack = 0
}

func (p *PaneModel) View() string {
	borderColor := lipgloss.Color("238")
	if p.Active {
		borderColor = lipgloss.Color("57")
	}
	if p.ghost || p.resuming || p.preparing {
		borderColor = lipgloss.Color("95") // muted purple — distinct but not jarring
	}
	if p.mcpHighlight {
		borderColor = lipgloss.Color("208") // orange — MCP interaction
	}

	innerW := p.Width - 2
	innerH := p.Height - 2
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 1 {
		innerH = 1
	}

	content := p.renderContent(p.activeSel)

	// Render content with left, right, bottom borders (no top).
	// Lipgloss v2: Width/Height include borders in the budget (v1 was additive).
	// +2 width for left+right borders, +1 height for bottom border (top removed).
	bodyStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderTop(false).
		BorderForeground(borderColor).
		Width(innerW + 2).
		Height(innerH + 1)

	body := bodyStyle.Render(content)

	// Manual top border: CWD on the left, pane name on the right.
	topLine := buildTopBorder(p.Width, p.CWD, p.Name, borderColor, p.ghost, p.resuming, p.preparing, p.focusMode, p.spinnerFrame)

	return topLine + "\n" + body
}

func buildTopBorder(width int, cwd, name string, color color.Color, ghost, resuming, preparing, focus bool, spinnerFrame int) string {
	if ghost {
		if name == "" {
			name = "restored"
		} else {
			name = name + " · restored"
		}
	}

	// Spinner overrides the right label temporarily
	if resuming || preparing {
		frame := spinnerFrames[spinnerFrame%len(spinnerFrames)]
		label := "resuming..."
		if preparing {
			label = "preparing..."
		}
		name = frame + " " + label
	}

	style := lipgloss.NewStyle().Foreground(color)
	b := lipgloss.RoundedBorder()
	innerW := width - 2
	if innerW < 1 {
		return style.Render(b.TopLeft + b.TopRight)
	}

	// Right label: pane name or spinner (only if it fits with padding).
	rightLabel := ""
	rightLen := 0
	if name != "" && len([]rune(name))+4 <= innerW {
		rightLabel = " " + name + " "
		rightLen = len([]rune(rightLabel))
	}

	// Left label: CWD, truncated with ellipsis if needed.
	leftLabel := ""
	leftLen := 0
	if cwd != "" {
		available := innerW - rightLen - 1 // reserve at least 1 dash
		cwdLabel := " " + cwd + " "
		cwdLabelLen := len([]rune(cwdLabel))

		if cwdLabelLen <= available {
			leftLabel = cwdLabel
			leftLen = cwdLabelLen
		} else if available >= 6 {
			// Truncate CWD from the left: " …tail "
			maxCwd := available - 4 // 4 = len(" …") + len(" ")
			cwdRunes := []rune(cwd)
			leftLabel = " …" + string(cwdRunes[len(cwdRunes)-maxCwd:]) + " "
			leftLen = len([]rune(leftLabel))
		}
	}

	dashes := innerW - leftLen - rightLen
	if dashes < 0 {
		dashes = 0
	}

	// Focus mode: center "* FOCUS *" relative to the full border width
	if focus {
		focusLabel := "* FOCUS *"
		focusLen := len([]rune(focusLabel))
		if dashes >= focusLen+2 {
			// Center position relative to full innerW, then subtract left/right label offsets
			centerPos := (innerW - focusLen) / 2
			leftDash := centerPos - leftLen
			if leftDash < 1 {
				leftDash = 1
			}
			rightDash := dashes - focusLen - leftDash
			if rightDash < 0 {
				rightDash = 0
			}
			return style.Render(b.TopLeft + leftLabel +
				strings.Repeat(b.Top, leftDash) + focusLabel + strings.Repeat(b.Top, rightDash) +
				rightLabel + b.TopRight)
		}
	}

	return style.Render(b.TopLeft + leftLabel + strings.Repeat(b.Top, dashes) + rightLabel + b.TopRight)
}

// parseOSC7Path extracts a filesystem path from an OSC 7 URI (file://host/path).
func parseOSC7Path(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" {
		if raw != "" {
			return raw // treat as plain path
		}
		return ""
	}
	path := u.Path
	// Windows: url.Parse("file:///C:/foo") gives Path="/C:/foo"; strip leading /.
	if len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	return path
}

func (p *PaneModel) renderContent(sel *Selection) string {
	// If selection is active on this pane, use cell-by-cell rendering
	if sel != nil && sel.PaneID == p.ID {
		return p.renderWithSelection(sel)
	}

	if p.scrollBack == 0 {
		// Live view — use Render() for full color support
		content := p.vt.Render()
		// Only overlay cursor for terminal panes — TUI apps (Claude Code etc.)
		// render their own cursor.
		isTerminal := p.Type == "" || p.Type == "terminal" || p.Type == "ssh"
		if p.Active && p.cursorVisible && isTerminal {
			content = p.insertCursor(content)
		}
		return content
	}

	// Scrollback view — render from scrollback + screen cells
	return p.renderScrollback()
}

// renderWithSelection renders content cell-by-cell with selection highlighting.
func (p *PaneModel) renderWithSelection(sel *Selection) string {
	w := p.vt.Width()
	h := p.vt.Height()
	sbLen := p.vt.ScrollbackLen()

	viewStart := sbLen - p.scrollBack

	lines := make([]string, h)
	for i := 0; i < h; i++ {
		absLine := viewStart + i

		var getCell func(x int) *uv.Cell
		if absLine < 0 {
			getCell = func(x int) *uv.Cell { return nil }
		} else if absLine < sbLen {
			srcLine := absLine
			getCell = func(x int) *uv.Cell {
				return p.vt.ScrollbackCellAt(x, srcLine)
			}
		} else {
			screenLine := absLine - sbLen
			getCell = func(x int) *uv.Cell {
				return p.vt.CellAt(x, screenLine)
			}
		}

		selStart, selEnd := sel.ColRange(absLine, w)
		lines[i] = p.styledCellLineWithSelection(getCell, w, selStart, selEnd)
	}

	return strings.Join(lines, "\n")
}

func (p *PaneModel) renderScrollback() string {
	w := p.vt.Width()
	h := p.vt.Height()
	sbLen := p.vt.ScrollbackLen()

	// viewStart is the first line to show (in combined scrollback+screen space)
	viewStart := sbLen - p.scrollBack

	lines := make([]string, h)
	for i := 0; i < h; i++ {
		srcLine := viewStart + i

		if srcLine < 0 {
			lines[i] = ""
		} else if srcLine < sbLen {
			lines[i] = p.styledCellLine(func(x int) *uv.Cell {
				return p.vt.ScrollbackCellAt(x, srcLine)
			}, w)
		} else {
			screenLine := srcLine - sbLen
			lines[i] = p.styledCellLine(func(x int) *uv.Cell {
				return p.vt.CellAt(x, screenLine)
			}, w)
		}
	}

	// Add scrollbar on the right side
	totalLines := sbLen + h
	thumbSize := max(1, h*h/totalLines)
	scrollRange := totalLines - h
	thumbPos := 0
	if scrollRange > 0 {
		thumbPos = viewStart * (h - thumbSize) / scrollRange
	}
	if thumbPos < 0 {
		thumbPos = 0
	}

	for i, line := range lines {
		ch := "░"
		if i >= thumbPos && i < thumbPos+thumbSize {
			ch = "█"
		}
		// Ensure line is exactly w-1 columns, then append scrollbar character
		lineW := ansi.StringWidth(line)
		if lineW > w-1 {
			line = ansi.Truncate(line, w-1, "")
		} else if lineW < w-1 {
			line = line + strings.Repeat(" ", w-1-lineW)
		}
		lines[i] = line + "\x1b[90m" + ch + "\x1b[0m"
	}

	return strings.Join(lines, "\n")
}

// styledCellLineWithSelection renders a row with optional selection highlighting.
// selStart/selEnd define the selected column range (-1 = no selection on this row).
func (p *PaneModel) styledCellLineWithSelection(getCell func(x int) *uv.Cell, width, selStart, selEnd int) string {
	var b strings.Builder
	var lastSGR string
	var pending int

	for x := 0; x < width; x++ {
		cell := getCell(x)
		ch := " "
		styled := false
		var sgr string

		if cell != nil {
			if cell.Content != "" {
				ch = cell.Content
			}
			if !cell.Style.IsZero() {
				styled = true
				sgr = cell.Style.String()
			}
		}

		// Check if this cell is selected
		inSelection := selStart >= 0 && x >= selStart && x <= selEnd

		if inSelection {
			// Flush pending spaces before selection
			if pending > 0 {
				b.WriteString(strings.Repeat(" ", pending))
				pending = 0
			}
			if lastSGR != "" {
				b.WriteString("\x1b[m")
				lastSGR = ""
			}
			// Render with reverse video
			b.WriteString("\x1b[7m")
			b.WriteString(ch)
			b.WriteString("\x1b[m")
			continue
		}

		// Normal rendering (same as styledCellLine)
		if ch == " " && !styled {
			if lastSGR != "" {
				b.WriteString("\x1b[m")
				lastSGR = ""
			}
			pending++
			continue
		}

		if pending > 0 {
			b.WriteString(strings.Repeat(" ", pending))
			pending = 0
		}
		if sgr != lastSGR {
			if !styled && lastSGR != "" {
				b.WriteString("\x1b[m")
			} else if styled {
				b.WriteString(sgr)
			}
			lastSGR = sgr
		}
		b.WriteString(ch)
	}

	if lastSGR != "" {
		b.WriteString("\x1b[m")
	}
	return b.String()
}

// styledCellLine renders a row of cells with ANSI styles preserved.
// Trailing unstyled spaces are buffered and only flushed when followed by
// visible content, so the result is naturally right-trimmed.
func (p *PaneModel) styledCellLine(getCell func(x int) *uv.Cell, width int) string {
	var b strings.Builder
	var lastSGR string
	var pending int // buffered trailing unstyled spaces

	for x := 0; x < width; x++ {
		cell := getCell(x)
		ch := " "
		styled := false
		var sgr string

		if cell != nil {
			if cell.Content != "" {
				ch = cell.Content
			}
			if !cell.Style.IsZero() {
				styled = true
				sgr = cell.Style.String()
			}
		}

		// Unstyled space — buffer (may be trailing)
		if ch == " " && !styled {
			if lastSGR != "" {
				b.WriteString("\x1b[m")
				lastSGR = ""
			}
			pending++
			continue
		}

		// Non-trivial cell: flush buffered spaces, then render
		if pending > 0 {
			b.WriteString(strings.Repeat(" ", pending))
			pending = 0
		}
		if sgr != lastSGR {
			if !styled && lastSGR != "" {
				b.WriteString("\x1b[m")
			} else if styled {
				b.WriteString(sgr)
			}
			lastSGR = sgr
		}
		b.WriteString(ch)
	}

	// Reset at end if style was active (trailing spaces already dropped)
	if lastSGR != "" {
		b.WriteString("\x1b[m")
	}
	return b.String()
}

func (p *PaneModel) insertCursor(content string) string {
	pos := p.vt.CursorPosition()
	lines := strings.Split(content, "\n")

	if pos.Y < 0 || pos.Y >= len(lines) {
		return content
	}

	// Rebuild cursor line from cell data to avoid ANSI string splitting issues.
	w := p.vt.Width()
	var b strings.Builder

	for x := 0; x < w; x++ {
		cell := p.vt.CellAt(x, pos.Y)
		ch := " "
		if cell != nil && cell.Content != "" {
			ch = cell.Content
		}

		if x == pos.X {
			// Cursor: reset style, render in reverse video
			b.WriteString("\x1b[0m\x1b[7m")
			b.WriteString(ch)
			b.WriteString("\x1b[27m")
		} else {
			// Non-cursor: render with cell's original style
			if cell != nil {
				if sgr := cell.Style.String(); sgr != "" {
					b.WriteString(sgr)
				}
			}
			b.WriteString(ch)
		}
	}
	b.WriteString("\x1b[0m")

	lines[pos.Y] = b.String()
	return strings.Join(lines, "\n")
}
