package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

type PaneModel struct {
	ID     string
	Name   string
	vt     *vt.SafeEmulator
	Width  int
	Height int
	Active bool
}

func NewPaneModel(id string) *PaneModel {
	return &PaneModel{
		ID:   id,
		Name: id,
		vt:   vt.NewSafeEmulator(80, 24),
	}
}

func (p *PaneModel) AppendOutput(data []byte) {
	p.vt.Write(data)
}

func (p *PaneModel) ResizeVT(cols, rows int) {
	if cols > 0 && rows > 0 && (cols != p.vt.Width() || rows != p.vt.Height()) {
		p.vt.Resize(cols, rows)
	}
}

func (p *PaneModel) View() string {
	style := inactivePaneBorder
	if p.Active {
		style = activePaneBorder
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

	return style.
		Width(innerW).
		Height(innerH).
		Render(content)
}

func (p *PaneModel) renderContent() string {
	content := p.vt.Render()

	if !p.Active {
		return content
	}

	// Insert visible block cursor at cursor position
	pos := p.vt.CursorPosition()
	lines := strings.Split(content, "\n")

	if pos.Y < 0 || pos.Y >= len(lines) {
		return content
	}

	line := lines[pos.Y]
	lineWidth := ansi.StringWidth(line)

	if pos.X >= lineWidth {
		// Cursor beyond line content — pad and add cursor block
		padding := pos.X - lineWidth
		lines[pos.Y] = line + strings.Repeat(" ", padding) + "\x1b[7m \x1b[27m"
	} else {
		// Split line at cursor position, wrap cursor char in reverse video
		before := ansi.Truncate(line, pos.X, "")
		cursorChar := ansi.Cut(line, pos.X, pos.X+1)
		after := ansi.TruncateLeft(line, pos.X+1, "")
		if cursorChar == "" {
			cursorChar = " "
		}
		lines[pos.Y] = before + "\x1b[7m" + cursorChar + "\x1b[27m" + after
	}

	return strings.Join(lines, "\n")
}
