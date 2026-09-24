package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// rowHighlight is a whole-row background on a PROJECTS row: light grey under
// the pointer, light blue while the row is being dragged. The zero value
// paints nothing, and every renderer takes it as a parameter rather than
// reading the Model, so paint and hit test keep sharing one row slice.
type rowHighlight int

const (
	rowHighlightNone rowHighlight = iota
	rowHighlightHover
	rowHighlightDrag
)

// Both backgrounds are light, so the row's plain text turns dark on them
// (sidebarHighlightFG) — readable on a dark terminal and a light one alike.
// The badge glyphs keep their own colours on top.
var (
	sidebarHoverBG     = lipgloss.Color("252")
	sidebarDragBG      = lipgloss.Color("153")
	sidebarHighlightFG = lipgloss.Color("235")
)

func (h rowHighlight) background() color.Color {
	switch h {
	case rowHighlightHover:
		return sidebarHoverBG
	case rowHighlightDrag:
		return sidebarDragBG
	}
	return nil
}

// onBackground puts s on the highlight's background, keeping its foreground.
func (h rowHighlight) onBackground(s lipgloss.Style) lipgloss.Style {
	if h == rowHighlightNone {
		return s
	}
	return s.Background(h.background())
}

// text is the style for a highlighted row's PLAIN text: the dark foreground,
// every other attribute (bold on the active project) kept.
func (h rowHighlight) text(s lipgloss.Style) lipgloss.Style {
	if h == rowHighlightNone {
		return s
	}
	return s.Foreground(sidebarHighlightFG)
}

// fill paints unstyled cells — an indent, a pad — on the highlight's
// background. Unhighlighted they stay the bare string they always were.
func (h rowHighlight) fill(s string) string {
	if h == rowHighlightNone || s == "" {
		return s
	}
	return lipgloss.NewStyle().Background(h.background()).Render(s)
}

// sidebarHoverKey names the hovered PROJECTS row: a project by (dest, id) —
// the key groups use, since two daemons can mint one id — or a group header by
// name. Stable across a broadcast that rebuilds the rows; the zero value is
// "nothing hovered".
type sidebarHoverKey struct {
	group     string
	dest      string
	projectID string
}

// sidebarHoverAt resolves the hover key under a screen cell through
// sidebarRowAt — the row slice the paint uses — so the highlighted row is the
// one the pointer is on. Anything but a project row (either of a remote's two)
// or a group header, including every cell outside the strip, is no hover.
//
// Building the rows styles every one of them, and buttonless motion arrives
// per pointer move, so a move along the same row reuses the last answer while
// no frame has been rebuilt since (viewCacheBox.hoverBuilds). Without a view
// cache every move resolves.
func (m *Model) sidebarHoverAt(x, y int) sidebarHoverKey {
	if w := m.projectSidebarWidth(); w <= 0 || x < 0 || x >= w || y < 0 || y >= m.height-1 {
		return sidebarHoverKey{}
	}
	c := m.viewCache
	if c != nil && c.valid && c.hoverValid && c.hoverY == y && c.hoverBuilds == c.builds {
		return c.hoverKey
	}
	key := m.resolveSidebarHover(x, y)
	if c != nil {
		c.hoverValid, c.hoverY, c.hoverBuilds, c.hoverKey = true, y, c.builds, key
		c.hoverResolves++
	}
	return key
}

// resolveSidebarHover is sidebarHoverAt's slow path: the row slice itself.
func (m *Model) resolveSidebarHover(x, y int) sidebarHoverKey {
	row, ok := m.sidebarRowAt(x, y)
	if !ok {
		return sidebarHoverKey{}
	}
	switch row.kind {
	case sidebarRowProject:
		if row.index >= 0 && row.index < len(m.projects) {
			p := m.projects[row.index]
			return sidebarHoverKey{dest: p.Dest, projectID: p.ID}
		}
	case sidebarRowGroup:
		if row.index >= 0 && row.index < len(m.groups.Groups) {
			return sidebarHoverKey{group: m.groups.Groups[row.index].Name}
		}
	}
	return sidebarHoverKey{}
}

// setSidebarHover stores k and reports whether it changed — an unchanged
// hover is what lets the motion arm serve the cached frame.
func (m *Model) setSidebarHover(k sidebarHoverKey) bool {
	if k == m.sidebarHover {
		return false
	}
	m.sidebarHover = k
	return true
}

// projectRowHighlight is project i's highlight. A drag that has left its press
// row wins over the hover: the dragged row is the one being acted on, and it
// follows projectDragIdx as the project reorders. clearDragState ends it.
func (m *Model) projectRowHighlight(i int) rowHighlight {
	if m.projectDragging && m.projectDragMoved && m.projectDragIdx == i {
		return rowHighlightDrag
	}
	p := m.projects[i]
	if h := m.sidebarHover; h.group == "" && h.projectID != "" && h.projectID == p.ID && h.dest == p.Dest {
		return rowHighlightHover
	}
	return rowHighlightNone
}

// groupRowHighlight is group g's HEADER highlight — never its members'. The
// drag colour needs a header drag that has moved the group (groupDragMoved),
// so a click on the header never flashes it.
func (m *Model) groupRowHighlight(g int) rowHighlight {
	if m.groupDragging && m.groupDragMoved && m.groupDragIdx == g {
		return rowHighlightDrag
	}
	if h := m.sidebarHover.group; h != "" && h == m.groups.Groups[g].Name {
		return rowHighlightHover
	}
	return rowHighlightNone
}
