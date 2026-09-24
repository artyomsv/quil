package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// rowHighlight is a whole-row background on a sidebar row: light grey under
// the pointer, light blue while the row is being dragged. The zero value
// paints nothing, and every renderer takes it as a parameter rather than
// reading the Model, so paint and hit test keep sharing one row slice.
type rowHighlight int

const (
	rowHighlightNone rowHighlight = iota
	rowHighlightHover
	rowHighlightDrag
	// rowHighlightDrop marks where a moved project drag would land on release:
	// a group header, or the PROJECTS heading for "no group".
	rowHighlightDrop
	// rowHighlightPaneHover is the hovered PANE (and its git row), a shade
	// darker than the rowHighlightHover its tab's block is painted with.
	rowHighlightPaneHover
)

// glyphDropTarget prefixes the drop-target row's label. U+2192 is one cell
// and has no emoji presentation (the sidebar glyph rule —
// TestSidebarGlyphs_OneCellAndNotEmojiCapable).
const glyphDropTarget = "→"

// Every background is light, so the row's plain text turns dark on them
// (sidebarHighlightFG) — readable on a dark terminal and a light one alike.
// The badge glyphs keep their own colours on top.
var (
	sidebarHoverBG     = lipgloss.Color("252")
	sidebarDragBG      = lipgloss.Color("153")
	sidebarDropBG      = lipgloss.Color("151")
	sidebarPaneHoverBG = lipgloss.Color("249")
	sidebarHighlightFG = lipgloss.Color("235")
)

func (h rowHighlight) background() color.Color {
	switch h {
	case rowHighlightHover:
		return sidebarHoverBG
	case rowHighlightPaneHover:
		return sidebarPaneHoverBG
	case rowHighlightDrag:
		return sidebarDragBG
	case rowHighlightDrop:
		return sidebarDropBG
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

// line is the style for a row drawn in ONE style (a heading, a host or git
// row): its text darkened on the highlight's background. Unhighlighted it is
// s itself.
func (h rowHighlight) line(s lipgloss.Style) lipgloss.Style {
	if h == rowHighlightNone {
		return s
	}
	return h.onBackground(h.text(s))
}

// fill paints unstyled cells — an indent, a pad — on the highlight's
// background. Unhighlighted they stay the bare string they always were.
func (h rowHighlight) fill(s string) string {
	if h == rowHighlightNone || s == "" {
		return s
	}
	return lipgloss.NewStyle().Background(h.background()).Render(s)
}

// sidebarHoverKey names the hovered sidebar row by IDs, never an index: a
// project by (dest, projectID) — the key groups use, since two daemons can
// mint one id — a group header by name, a PANES tab block by (dest, tabID),
// and a pane in it by (dest, tabID, paneID). Stable across a broadcast that
// rebuilds the rows; the zero value is "nothing hovered".
type sidebarHoverKey struct {
	group     string
	dest      string
	projectID string
	tabID     string
	paneID    string
}

// sidebarHoverAt resolves the hover key under a screen cell through
// sidebarRowAt — the row slice the paint uses, PANES scroll window included —
// so the highlighted row is the one the pointer is on. A project row (either
// of a remote's two), a group header, a tab heading, a pane row or a pane's
// git row hovers; anything else, every cell outside the strip included, is
// no hover.
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
	// The PANES rows: every row of a tab's block names that tab (inTab), and a
	// pane row — or the git row under it — its pane too. The blank row between
	// blocks is in neither.
	tabs, p := m.curTabs(), m.cur()
	if !row.inTab || p == nil || row.tabIdx < 0 || row.tabIdx >= len(tabs) {
		return sidebarHoverKey{}
	}
	key := sidebarHoverKey{dest: p.Dest, tabID: tabs[row.tabIdx].ID}
	switch {
	case row.kind == sidebarRowPane:
		key.paneID = row.paneID
	case row.gitOf != "":
		key.paneID = row.gitOf
	}
	return key
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

// projectDrop names where a project drag would land: a group by NAME, or
// ungroup ("no group"). The zero value is "nowhere".
type projectDrop struct {
	group   string
	ungroup bool
}

// projectDropFor is THE drop rule, for project idx released on row (ok false:
// no sidebar row there). finishProjectDrag applies it and trackProjectDrag
// paints it, so the highlight can never promise a drop the release will not
// make, nor the release make one nobody was shown. A group header other than
// the project's own group joins that group; the PROJECTS heading or an
// ungrouped project row takes a GROUPED project out; everything else — its own
// section, its own header, the panes, empty space — is nowhere.
func (m *Model) projectDropFor(idx int, row sidebarRow, ok bool) projectDrop {
	if !ok || idx < 0 || idx >= len(m.projects) {
		return projectDrop{}
	}
	p := m.projects[idx]
	cur := m.groups.groupOf(p.Dest, p.ID)
	switch {
	case row.kind == sidebarRowGroup:
		if row.index >= 0 && row.index < len(m.groups.Groups) && row.index != cur {
			return projectDrop{group: m.groups.Groups[row.index].Name}
		}
	case cur >= 0 && (row.ungroupDrop || (row.kind == sidebarRowProject && !row.inGroup)):
		return projectDrop{ungroup: true}
	}
	return projectDrop{}
}

// activeProjectDrop is the drop target to paint: only for a project drag that
// has left its press row, which is also the only drag a release acts on.
func (m *Model) activeProjectDrop() projectDrop {
	if m.projectDragging && m.projectDragMoved {
		return m.projectDrop
	}
	return projectDrop{}
}

// dragActive reports any armed drag or selection. The hover is frozen during
// one (only held-button motion arrives), so it is not painted either: the
// colours then say where things are going, not where the pointer began.
func (m *Model) dragActive() bool {
	return m.projectDragging || m.groupDragging || m.sidebarTabDragging || m.sidebarDragging ||
		m.tabDragFromIdx >= 0 || m.splitDragNode != nil || m.paneDrag.active() ||
		m.scrollDragPaneID != "" || m.mouseDown || m.notesMouseDown
}

// projectRowHighlight is project i's highlight. A drag that has left its press
// row paints the dragged project, found by its identity (projectDragIndex) so
// it follows the project through a reorder or a rebuild; clearDragState ends
// it. The hover shows only with no drag.
func (m *Model) projectRowHighlight(i int) rowHighlight {
	if m.projectDragMoved && m.projectDragIndex() == i {
		return rowHighlightDrag
	}
	p := m.projects[i]
	if h := m.sidebarHover; !m.dragActive() && h.group == "" && h.projectID != "" && h.projectID == p.ID && h.dest == p.Dest {
		return rowHighlightHover
	}
	return rowHighlightNone
}

// groupRowHighlight is group g's HEADER highlight — never its members'. The
// drag colour needs a header drag that has moved the group (groupDragMoved),
// so a click on the header never flashes it; the drop colour marks the group
// a moved project drag would join.
func (m *Model) groupRowHighlight(g int) rowHighlight {
	if m.groupDragging && m.groupDragMoved && m.groupDragIdx == g {
		return rowHighlightDrag
	}
	name := m.groups.Groups[g].Name
	if d := m.activeProjectDrop(); d.group != "" && d.group == name {
		return rowHighlightDrop
	}
	if h := m.sidebarHover.group; !m.dragActive() && h != "" && h == name {
		return rowHighlightHover
	}
	return rowHighlightNone
}

// tabRowHighlight is the highlight of tab's whole PANES block: the hover grey
// while the pointer is on any row of it, matched by (dest, tabID) — tab ids
// are per daemon. Never during a drag.
func (m *Model) tabRowHighlight(tab *TabModel) rowHighlight {
	h := m.sidebarHover
	if h.tabID == "" || h.tabID != tab.ID || m.dragActive() {
		return rowHighlightNone
	}
	if p := m.cur(); p == nil || p.Dest != h.dest {
		return rowHighlightNone
	}
	return rowHighlightHover
}

// paneRowHighlight is a pane row's (and its git row's): the darker grey for
// the hovered pane, else its tab block's tabHL. The pane id is compared only
// inside a block already matched on (dest, tabID).
func (m *Model) paneRowHighlight(pane *PaneModel, tabHL rowHighlight) rowHighlight {
	if tabHL == rowHighlightHover && m.sidebarHover.paneID != "" && m.sidebarHover.paneID == pane.ID {
		return rowHighlightPaneHover
	}
	return tabHL
}

// projectsHeadingHighlight is the PROJECTS heading's: the drop colour while a
// moved project drag would leave its group there.
func (m *Model) projectsHeadingHighlight() rowHighlight {
	if m.activeProjectDrop().ungroup {
		return rowHighlightDrop
	}
	return rowHighlightNone
}
