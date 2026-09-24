package tui

import (
	"log"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/keymap"
)

// Flash texts for a refused arrangement.
const (
	tabBusyFlash        = "Tab is busy — try again in a moment"
	layoutTooSmallFlash = "Not enough room for that layout"
)

// layoutPreset ties one arrangement to its keymap action and its label. One
// table, three front doors: the tab menu's Layout… list, the palette's Tabs
// group and handleKey's tab.layout_* arm all read it, so a label or an id
// cannot drift between them.
type layoutPreset struct {
	kind   layoutKind
	action keymap.ActionID
	label  string
}

var layoutPresets = []layoutPreset{
	{layoutEven, "tab.layout_even", "Even out"},
	{layoutColumns, "tab.layout_columns", "Columns"},
	{layoutRows, "tab.layout_rows", "Rows"},
	{layoutGrid, "tab.layout_grid", "Grid"},
	{layoutMain, "tab.layout_main", "Main + stack"},
	{layoutSpiral, "tab.layout_spiral", "Spiral"},
}

// layoutKindFor resolves a tab.layout_* action id. The palette carries the id
// as its row arg, so this is also what stops an arbitrary arg from running.
func layoutKindFor(id keymap.ActionID) (layoutKind, bool) {
	for _, p := range layoutPresets {
		if p.action == id {
			return p.kind, true
		}
	}
	return layoutNone, false
}

// tabLayoutBusy is tabInFlight plus THIS client's own pendingSplit
// reservation in the tab. tabInFlight deliberately does not grow the
// reservation: it also gates Move to tab…, which has its own rule for them (a
// moved pane never fills one). For an arrangement the reservation is the
// dangerous case — the new tree is a copy, so pendingSplit would be left
// pointing at a placeholder no tree holds.
func (m *Model) tabLayoutBusy(tab *TabModel) bool {
	return m.tabInFlight(tab) || m.pendingSplit[tab.ID] != nil
}

// tabArrangeable is the gate that greys the Layout… row, its list and the
// palette's layout rows: two panes or more, and not busy.
func (m *Model) tabArrangeable(tab *TabModel) bool {
	return tab != nil && len(tab.Leaves()) >= 2 && !m.tabLayoutBusy(tab)
}

// arrangeTab applies arrangement kind to tab — the six menu, palette and key
// actions all come through here. The tab's active pane stays active and is
// main + stack's main pane.
func (m *Model) arrangeTab(tab *TabModel, kind layoutKind) tea.Cmd {
	if tab == nil || tab.Root == nil {
		return nil
	}
	active := tab.treeActivePaneModel()
	return m.applyTabArrangement(tab, arrangeLayout(kind, tab.Root, active), active)
}

// applyTabArrangement installs newRoot as tab's tree — the ONE apply step for
// the six arrangements and both in-tab drag drops.
//
// Refusals leave the tab exactly as it was (newRoot is a separate tree): notes
// mode and fewer than two panes are silent; a busy tab and a tree that would
// put any pane under minPaneW x minPaneH flash. The size check uses this
// client's CANONICAL geometry — paneAreaWidth() x (height - chromeHeight),
// what resizeTabs gives every tab — never the notes-squeezed width, because
// the tab being arranged may not be the one on screen.
//
// On success: the tree, the active pane (and every Active flag in the tab),
// focus mode off, one resize at the canonical geometry, and one layout send
// for THIS tab.
func (m *Model) applyTabArrangement(tab *TabModel, newRoot *LayoutNode, active *PaneModel) tea.Cmd {
	if tab == nil || newRoot == nil || m.notesMode {
		return nil
	}
	if m.tabLayoutBusy(tab) {
		m.setFlash(tabBusyFlash)
		return m.flashCmd()
	}
	if len(tab.Leaves()) < 2 {
		return nil
	}
	w, h := m.paneAreaWidth(), m.height-chromeHeight
	if !fitsMinSize(newRoot, w, h) {
		m.setFlash(layoutTooSmallFlash)
		return m.flashCmd()
	}
	for _, p := range tab.Leaves() {
		p.Active = false
	}
	tab.Root = newRoot
	tab.invalidateLeaves()
	if active != nil && newRoot.FindLeaf(active.ID) != nil {
		tab.ActivePane = active.ID
	}
	if p := tab.treeActivePaneModel(); p != nil {
		tab.ActivePane = p.ID
		p.Active = true
	}
	tab.ExitFocus()
	tab.SetCanvas(w, h)
	tab.SetChrome(m.projectSidebarWidth())
	tab.Resize(w, h)
	return tea.Batch(m.resizeAllPanes(), m.sendTabLayout(tab))
}

// sendTabLayout persists ONE tab's tree. It marshals here, on the Update
// goroutine, and ships through sendDiffedLayouts — sendAllLayouts would re-send
// every tab of every project, reading the trees from a Cmd goroutine.
func (m *Model) sendTabLayout(tab *TabModel) tea.Cmd {
	if tab == nil || tab.Root == nil {
		return nil
	}
	data, err := MarshalLayout(tab.Root)
	if err != nil {
		log.Printf("tab layout: marshal %s: %v", tab.ID, err)
		return nil
	}
	return m.sendDiffedLayouts([]layoutSend{{dest: m.destOfTab(tab.ID), tabID: tab.ID, data: data}})
}
