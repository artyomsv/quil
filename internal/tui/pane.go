package tui

import (
	"net/url"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/artyomsv/aethel/internal/ringbuf"
)

type PaneModel struct {
	ID            string
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
	return p
}

func (p *PaneModel) AppendOutput(data []byte) {
	p.rawBuf.Write(data)
	p.vt.Write(data)
}

func (p *PaneModel) ResizeVT(cols, rows int) {
	if cols <= 0 || rows <= 0 || (cols == p.vt.Width() && rows == p.vt.Height()) {
		return
	}
	// Create fresh emulator at new dimensions and replay buffered output
	em := vt.NewSafeEmulator(cols, rows)
	em.SetScrollbackSize(10000)
	em.SetCallbacks(vt.Callbacks{
		CursorVisibility: func(visible bool) {
			p.cursorVisible = visible
		},
		WorkingDirectory: func(dir string) {
			p.CWD = parseOSC7Path(dir)
		},
	})
	if buf := p.rawBuf.Bytes(); len(buf) > 0 {
		em.Write(buf)
	}
	p.vt = em
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
	if p.ghost {
		borderColor = lipgloss.Color("95") // muted purple — distinct but not jarring
	}

	innerW := p.Width - 2
	innerH := p.Height - 2
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 1 {
		innerH = 1
	}

	content := p.renderContent()

	// Render content with left, right, bottom borders (no top).
	bodyStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderTop(false).
		BorderForeground(borderColor).
		Width(innerW).
		Height(innerH)

	body := bodyStyle.Render(content)

	// Manual top border: CWD on the left, pane name on the right.
	topLine := buildTopBorder(p.Width, p.CWD, p.Name, borderColor, p.ghost)

	return topLine + "\n" + body
}

func buildTopBorder(width int, cwd, name string, color lipgloss.TerminalColor, ghost bool) string {
	if ghost {
		if name == "" {
			name = "restored"
		} else {
			name = name + " · restored"
		}
	}

	style := lipgloss.NewStyle().Foreground(color)
	b := lipgloss.RoundedBorder()
	innerW := width - 2
	if innerW < 1 {
		return style.Render(b.TopLeft + b.TopRight)
	}

	// Right label: pane name (only if it fits with padding).
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

func (p *PaneModel) renderContent() string {
	if p.scrollBack == 0 {
		// Live view — use Render() for full color support
		content := p.vt.Render()
		if p.Active && p.cursorVisible {
			content = p.insertCursor(content)
		}
		return content
	}

	// Scrollback view — render from scrollback + screen cells
	return p.renderScrollback()
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
