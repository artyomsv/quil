package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Alt+drag of a whole pane: drop it beside another pane (one of four sides),
// on another pane's centre (swap), or on a tab in the tab bar (move).
//
// Mid-drag only the preview is drawn; the tree, the VT emulators and the PTYs
// change once, on release — the split-border drag's rule, for its reason.

// paneDragState is an armed pane drag. IDs, never pointers: a broadcast can
// rebuild the tree under an armed drag (the tabPickRow precedent). Zero value
// = no drag.
type paneDragState struct {
	srcPaneID    string   // the dragged pane
	srcTabID     string   // the tab it was dragged from
	targetPaneID string   // the pane under the pointer, "" when none
	zone         dropZone // where on targetPaneID it would land
	overTabID    string   // the tab-bar tab under the pointer, "" when none
}

func (d paneDragState) active() bool { return d.srcPaneID != "" }

var (
	paneDragOutlineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	paneDragTabStyle     = lipgloss.NewStyle().Reverse(true).Bold(true)
)

// paneDragModifier is THE drag chord: Alt without Ctrl. Windows Terminal was
// verified to deliver Alt with a press while Quil tracks the mouse (Task 3
// Step 0), so it does not box-select. Ctrl stays excluded because Update
// swallows every Ctrl-modified click before any other mouse handling.
func paneDragModifier(mod tea.KeyMod) bool {
	return mod.Contains(tea.ModAlt) && !mod.Contains(tea.ModCtrl)
}

// dropEdgeFraction is how far in from each side the edge zones reach.
const dropEdgeFraction = 0.25

// dropZoneAt names where (x, y) falls in r: the outer quarter of the width or
// height on each side is that side, the rest is the centre. Distances are
// measured from the cell's centre as a fraction of that dimension, so a
// corner resolves to the NEARER edge, and a tie goes to Left/Right.
func dropZoneAt(r PaneRect, x, y int) dropZone {
	if r.W <= 0 || r.H <= 0 || x < r.OX || x >= r.OX+r.W || y < r.OY || y >= r.OY+r.H {
		return zoneNone
	}
	fx := (float64(x-r.OX) + 0.5) / float64(r.W)
	fy := (float64(y-r.OY) + 0.5) / float64(r.H)
	horiz, horizZone := fx, zoneLeft
	if 1-fx < horiz {
		horiz, horizZone = 1-fx, zoneRight
	}
	vert, vertZone := fy, zoneTop
	if 1-fy < vert {
		vert, vertZone = 1-fy, zoneBottom
	}
	switch {
	case horiz < dropEdgeFraction && horiz <= vert:
		return horizZone
	case vert < dropEdgeFraction:
		return vertZone
	}
	return zoneCenter
}

// dropPreviewRect is the part of r the dragged pane takes: the half on the
// zone's side (the int(n*0.5) halves CollectRects gives a 0.5 split), or all
// of r for a swap.
func dropPreviewRect(r PaneRect, zone dropZone) PaneRect {
	switch zone {
	case zoneLeft:
		r.W = int(float64(r.W) * 0.5)
	case zoneRight:
		half := int(float64(r.W) * 0.5)
		r.OX, r.W = r.OX+half, r.W-half
	case zoneTop:
		r.H = int(float64(r.H) * 0.5)
	case zoneBottom:
		half := int(float64(r.H) * 0.5)
		r.OY, r.H = r.OY+half, r.H-half
	}
	return r
}

// overlayOutline draws a heavy-line box around r onto base as four overlayAt
// strips, so the pane content inside stays visible — overlayAt has no
// transparency, and a filled band would hide what the user is aiming at.
// Heavy glyphs because no pane border uses them.
func overlayOutline(base string, r PaneRect, totalW int) string {
	if r.W < 2 || r.H < 2 {
		return base
	}
	s := paneDragOutlineStyle
	base = overlayAt(base, s.Render("┏"+strings.Repeat("━", r.W-2)+"┓"), r.OX, r.OY, totalW)
	base = overlayAt(base, s.Render("┗"+strings.Repeat("━", r.W-2)+"┛"), r.OX, r.OY+r.H-1, totalW)
	if r.H > 2 {
		side := strings.TrimSuffix(strings.Repeat(s.Render("┃")+"\n", r.H-2), "\n")
		base = overlayAt(base, side, r.OX, r.OY+1, totalW)
		base = overlayAt(base, side, r.OX+r.W-1, r.OY+1, totalW)
	}
	return base
}

// beginPaneDrag arms a drag of the pane under (x, y) in the active tab. The
// caller has cleared every other drag. A busy tab arms nothing, since its
// tree is about to change under the drag, and flashes tabBusyFlash like the
// menu, palette and key paths do.
func (m *Model) beginPaneDrag(x, y int) tea.Cmd {
	tab := m.activeTabModel()
	if tab == nil || tab.Root == nil {
		return nil
	}
	if m.tabLayoutBusy(tab) {
		m.setFlash(tabBusyFlash)
		return m.flashCmd()
	}
	r := m.paneRectAt(x, y)
	if r == nil || r.Pane == nil {
		return nil
	}
	m.paneDrag = paneDragState{srcPaneID: r.Pane.ID, srcTabID: tab.ID}
	return nil
}

// paneDragIntact reports whether the armed drag still describes the screen:
// its pane is still a leaf of the tab it started in, that tab is still the
// active one, and no overlay or notes editor has taken it over. A broadcast
// that moved or destroyed the pane breaks it.
func (m *Model) paneDragIntact() bool {
	d := m.paneDrag
	if !d.active() || m.notesMode {
		return false
	}
	tab := m.activeTabModel()
	return tab != nil && tab.ID == d.srcTabID && !tab.overlayVisible &&
		tab.Root != nil && tab.Root.FindLeaf(d.srcPaneID) != nil
}

// isMovePaneTarget reports whether tabID is one of movePaneCandidates(paneID)
// — the same scope Move to tab… offers.
func (m *Model) isMovePaneTarget(paneID, tabID string) bool {
	for _, t := range m.movePaneCandidates(paneID) {
		if t.ID == tabID {
			return true
		}
	}
	return false
}

// trackPaneDrag moves the drag's target to what is under (x, y): a candidate
// tab on the tab-bar row, or another pane of the active tab and the zone on
// it. Anything else clears the target. A drag that is no longer intact is
// cancelled.
func (m *Model) trackPaneDrag(x, y int) {
	if !m.paneDragIntact() {
		m.clearDragState()
		return
	}
	d := &m.paneDrag
	d.targetPaneID, d.zone, d.overTabID = "", zoneNone, ""
	if y == 0 {
		tabs := m.curTabs()
		if idx := m.hitTestTab(x); idx >= 0 && idx < len(tabs) &&
			tabs[idx].ID != d.srcTabID && m.isMovePaneTarget(d.srcPaneID, tabs[idx].ID) {
			d.overTabID = tabs[idx].ID
		}
		return
	}
	if m.sidebarSwallowsMouse(x, y) {
		return
	}
	if r := m.paneRectAt(x, y); r != nil && r.Pane != nil && r.Pane.ID != d.srcPaneID {
		d.targetPaneID = r.Pane.ID
		d.zone = dropZoneAt(*r, x, y)
	}
}

// finishPaneDrag ends the drag at (x, y). It re-tracks at the release cell
// first, so a release with no motion still resolves and a target that went
// ineligible since the last motion is dropped. A tab drop is Move to tab…
// (sendMovePane; the daemon moves it and every client places it); a pane
// drop goes through the one apply step, the dragged pane becoming active.
func (m *Model) finishPaneDrag(x, y int) tea.Cmd {
	m.trackPaneDrag(x, y)
	d := m.paneDrag
	m.clearDragState()
	switch {
	case !d.active():
		return nil
	case d.overTabID != "":
		return m.sendMovePane(d.srcPaneID, d.overTabID)
	case d.targetPaneID == "" || d.zone == zoneNone:
		return nil
	}
	tab := m.activeTabModel()
	src, dst := tab.Root.FindLeaf(d.srcPaneID), tab.Root.FindLeaf(d.targetPaneID)
	if src == nil || dst == nil {
		return nil
	}
	var root *LayoutNode
	if d.zone == zoneCenter {
		root = swapLeaves(tab.Root, src.Pane, dst.Pane)
	} else {
		root = moveLeafBeside(tab.Root, src.Pane, dst.Pane, d.zone)
	}
	return m.applyTabArrangement(tab, root, src.Pane)
}

// paneRectByID is the screen rect of pane id in the active tab's split
// layout (paneRectAt's geometry), or nil — including in focus mode, where no
// other pane is on screen to drop onto.
func (m *Model) paneRectByID(id string) *PaneRect {
	tab := m.activeTabModel()
	if tab == nil || tab.Root == nil || tab.FocusMode() {
		return nil
	}
	var rects []PaneRect
	tab.Root.CollectRects(m.projectSidebarWidth(), 1, m.paneAreaWidth()-m.notesPanelWidth(), m.height-chromeHeight, &rects)
	for i := range rects {
		if rects[i].Pane != nil && rects[i].Pane.ID == id {
			return &rects[i]
		}
	}
	return nil
}

// paneDragOverlay composites the drop preview onto paneArea, whose first line
// is screen row 0 (pane rects are screen-absolute): the hovered tab restyled
// in place, or an outline around the part of the target pane the dragged pane
// would take.
func (m *Model) paneDragOverlay(paneArea string) string {
	if !m.paneDragIntact() {
		return paneArea
	}
	d := m.paneDrag
	if d.overTabID != "" {
		tabs := m.curTabs()
		for _, s := range m.tabSpans() {
			if s.index < len(tabs) && tabs[s.index].ID == d.overTabID {
				return overlayAt(paneArea, paneDragTabStyle.Render(ansi.Strip(s.text)), m.projectSidebarWidth()+s.start, 0, m.width)
			}
		}
		return paneArea
	}
	if d.targetPaneID == "" || d.zone == zoneNone {
		return paneArea
	}
	r := m.paneRectByID(d.targetPaneID)
	if r == nil {
		return paneArea
	}
	return overlayOutline(paneArea, dropPreviewRect(*r, d.zone), m.width)
}
