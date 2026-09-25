package tui

import (
	"encoding/json"
	"log"
	"reflect"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Layout sync between several TUIs on one daemon (spec §7.2).
//
// The daemon numbers every stored write of a tab's tree (layout_rev) and
// refuses a write whose base_rev is not the current one. A client therefore
// sends only when ITS user changed the tree (markLayoutChanged), and adopts
// any broadcast carrying a higher revision than the tree it holds. It never
// sends because the stored tree merely disagrees with its own — two clients
// doing that re-send each other's trees forever.
//
// A change nobody on this client asked for (an MCP create, another client's
// split or close, a moved pane) is placed or pruned locally and remembered in
// awaitingPanes/awaitingGone. The requester's write normally arrives with the
// next broadcast and is adopted; only when that broadcast's stored tree still
// lacks the change does every client send, and the first write wins.

// markLayoutChanged records a tree change this client's user made and returns
// the command that stores it on dest. It marshals HERE, on the Update
// goroutine: the command holds only the bytes, never the tab.
//
// A write already in flight for the tab defers this one (layoutResend): both
// would carry the same base, so the second would be refused behind the first
// and then lost to the first one's echo. The echo sends it instead
// (syncTabLayout).
func (m *Model) markLayoutChanged(dest string, tab *TabModel) tea.Cmd {
	if tab == nil || tab.Root == nil || tab.templateLayoutPending {
		return nil
	}
	if tab.layoutDirty {
		tab.layoutResend = true
		return nil
	}
	s := m.layoutForSend(tab)
	if s == nil {
		return nil
	}
	data, err := json.Marshal(s)
	if err != nil {
		log.Printf("tab layout: marshal %s: %v", tab.ID, err)
		return nil
	}
	tab.layoutDirty, tab.layoutSent, tab.layoutResend = true, s, false
	tab.clearAwaiting()
	return m.sendDiffedLayouts([]layoutSend{{dest: dest, tabID: tab.ID, data: data, baseRev: tab.layoutRev}})
}

// layoutForSend is the tab's tree as it may be stored: no placeholders, since
// a reservation is this client's runtime state and means nothing to another.
// The one exception is a worktree REPLACE, whose placeholder stands in for a
// pane that is still live on the daemon — it is written under that pane's id,
// so another client adopting the tree keeps the pane where it is.
func (m *Model) layoutForSend(tab *TabModel) *SerializedNode {
	var keep *LayoutNode
	var keepID string
	if held := m.worktreeReplaced[tab.ID]; held != nil {
		keep, keepID = m.pendingSplit[tab.ID], held.ID
	}
	return serializeForSend(tab.Root, keep, keepID)
}

func serializeForSend(n, keep *LayoutNode, keepID string) *SerializedNode {
	if n == nil {
		return nil
	}
	if n.IsLeaf() {
		return &SerializedNode{PaneID: n.Pane.ID}
	}
	if n.Left == nil && n.Right == nil {
		if n == keep && keepID != "" {
			return &SerializedNode{PaneID: keepID}
		}
		return nil
	}
	l, r := serializeForSend(n.Left, keep, keepID), serializeForSend(n.Right, keep, keepID)
	if l == nil {
		return r
	}
	if r == nil {
		return l
	}
	split := n.Split
	return &SerializedNode{Split: &split, Ratio: n.Ratio, Left: l, Right: r}
}

// serializedIDs is the set of pane ids a stored tree names.
func serializedIDs(s *SerializedNode) map[string]bool {
	ids := make(map[string]bool)
	var walk func(*SerializedNode)
	walk = func(n *SerializedNode) {
		if n == nil {
			return
		}
		if n.PaneID != "" {
			ids[n.PaneID] = true
			return
		}
		walk(n.Left)
		walk(n.Right)
	}
	walk(s)
	return ids
}

// storedLayout parses a broadcast's layout, or nil when there is none or it
// does not parse — both mean the daemon holds nothing this client can use.
func storedLayout(raw json.RawMessage) *SerializedNode {
	s, err := UnmarshalLayout(raw)
	if err != nil {
		return nil
	}
	return s
}

// layoutPass is what syncTabLayout decided for one tab of one broadcast.
type layoutPass struct {
	// send: this pass must end in markLayoutChanged.
	send bool
	// oldTree holds the ids the tab's tree had BEFORE adoption, which is what
	// tells a moved pane (another tab's) from one this tab already held.
	oldTree map[string]bool
	// created: pane ids adoption built a new PaneModel for.
	created []string
	// reseated: adoption put this client's reservation back into the new
	// tree. The caller must spare it from this pass's placeholder prune, or
	// pendingSplit is left pointing at a detached node and the requested pane
	// lands in it invisibly.
	reseated bool
}

// closeKey scopes a closeRequested entry to its destination, as sizedKey does
// for sizedOnce: pane ids are per daemon.
func closeKey(dest, paneID string) string { return sizedKey(dest, paneID) }

// takeCloseRequest reports whether THIS client's user asked to close paneID
// on dest, consuming the request.
func (m *Model) takeCloseRequest(dest, paneID string) bool {
	k := closeKey(dest, paneID)
	if !m.closeRequested[k] {
		return false
	}
	delete(m.closeRequested, k)
	return true
}

// syncTabLayout runs for an EXISTING tab before its panes are reconciled, and
// decides what the broadcast's revision means for the tree it holds.
func (m *Model) syncTabLayout(tab *TabModel, ti TabInfo, paneSet map[string]bool, paneMap map[string]*PaneInfo, existingPanes map[string]*PaneModel, dest string) layoutPass {
	lp := layoutPass{oldTree: map[string]bool{}}
	if tab.Root != nil {
		lp.oldTree = tab.Root.PaneIDs()
	}
	stored := storedLayout(ti.Layout)

	// Every tree comparison here is STRUCTURAL (parsed SerializedNode), never
	// bytes: the daemon stores the struct-ordered bytes a client sent, but
	// parseWorkspaceState re-marshals the layout from map[string]any, which
	// sorts keys — so the same split tree arrives as different bytes.
	switch {
	case ti.LayoutRev > tab.layoutRev || tab.adoptNext:
		// Higher revision, or the first broadcast after a reattach, whose
		// revision can be anything — even the 0 of a restored workspace that
		// no client has written since (resetLayoutSync).
		//
		// Our own write coming back: the tree we hold already contains it,
		// plus anything done since, which the deferred resend now carries.
		echo := tab.layoutDirty && stored != nil && reflect.DeepEqual(stored, tab.layoutSent)
		resend := echo && tab.layoutResend
		tab.layoutRev = ti.LayoutRev
		tab.adoptNext = false
		tab.layoutDirty, tab.layoutSent, tab.layoutResend = false, nil, false
		if stored != nil && !echo && !reflect.DeepEqual(stored, m.layoutForSend(tab)) {
			lp.created, lp.send, lp.reseated = m.adoptTabLayout(tab, stored, paneSet, paneMap, existingPanes, dest)
			return lp
		}
		lp.send = resend
	case ti.LayoutRev < tab.layoutRev || tab.layoutDirty:
		// A dirty tab keeps its tree: our write is in flight, and whichever
		// write the daemon stores next arrives with a higher revision.
		return lp
	}

	// The tab holds the stored revision and nothing of ours is in flight: the
	// last broadcast's local placements are settled now. Anything the stored
	// tree still has not caught up with was not written by the client that
	// asked for it, so this one sends.
	if len(tab.awaitingPanes) > 0 || len(tab.awaitingGone) > 0 {
		ids := serializedIDs(stored)
		for id := range tab.awaitingPanes {
			if paneSet[id] && !ids[id] {
				lp.send = true
			}
		}
		for id := range tab.awaitingGone {
			if !paneSet[id] && ids[id] {
				lp.send = true
			}
		}
		tab.clearAwaiting()
	}
	return lp
}

// adoptTabLayout replaces tab's tree with the stored one. Pane models are
// reused by id, so no emulator or scrollback is lost; ids the broadcast no
// longer lists are dropped, and panes the stored tree lacks are left for the
// caller's arrival loop to place. Returns the ids it built new models for,
// whether a pane THIS client closed was still in the stored tree (a user
// change the requester must store), and whether it re-seated this client's
// reservation (which the caller must spare from this pass's prune).
func (m *Model) adoptTabLayout(tab *TabModel, stored *SerializedNode, paneSet map[string]bool, paneMap map[string]*PaneInfo, existingPanes map[string]*PaneModel, dest string) (created []string, send, reseated bool) {
	// A drag armed on this tab describes a tree that is about to go.
	if (m.splitDragNode != nil && treeContains(tab.Root, m.splitDragNode)) ||
		(m.paneDrag.active() && m.paneDrag.srcTabID == tab.ID) {
		m.clearDragState()
	}

	held := m.worktreeReplaced[tab.ID]
	panes := make(map[string]*PaneModel, len(paneSet))
	gone := make([]string, 0)
	for id := range serializedIDs(stored) {
		if !paneSet[id] {
			// Gone from the daemon but still in the stored tree.
			if m.takeCloseRequest(dest, id) {
				send = true
			} else {
				gone = append(gone, id)
			}
			continue
		}
		if p, ok := existingPanes[id]; ok {
			panes[id] = p
			continue
		}
		if held != nil && held.ID == id {
			continue // the worktree replace's pane: its leaf is the reservation
		}
		p := NewPaneModel(id, m.replayBufSize())
		p.resumeStart = time.Now()
		if info := paneMap[id]; info == nil || !info.Pending {
			p.preparing = true
		}
		panes[id] = p
		created = append(created, id)
	}

	// The stored tree names the pane a REPLACE reservation stands in for;
	// a sentinel keeps that leaf through the prune so the reservation can
	// take its place.
	ph := m.pendingSplit[tab.ID]
	var sentinel *PaneModel
	if ph != nil && tab.reserveReplace && tab.reserveSibling != "" && panes[tab.reserveSibling] == nil {
		sentinel = &PaneModel{ID: tab.reserveSibling}
		panes[tab.reserveSibling] = sentinel
	}

	root := DeserializeLayout(stored, panes)
	if root != nil {
		root.PrunePlaceholders()
		if root.Pane == nil && root.Left == nil && root.Right == nil {
			root = nil
		}
	}
	tab.Root = root
	tab.invalidateLeaves()
	tab.clearAwaiting()
	for _, id := range gone {
		tab.awaitGone(id)
	}

	if ph != nil {
		m.reseatReservation(tab, ph, sentinel)
		reseated = true
	}
	return created, send, reseated
}

// reseatReservation puts this client's pendingSplit placeholder back into an
// adopted tree: beside its original sibling in the original direction (the
// right/bottom half, where SplitLeaf put it), or in place of the pane a
// replace stands in for; failing both, where the spiral would place an
// arrival.
func (m *Model) reseatReservation(tab *TabModel, old *LayoutNode, sentinel *PaneModel) {
	var ph *LayoutNode
	switch {
	case sentinel != nil:
		if leaf := tab.Root.FindLeaf(sentinel.ID); leaf != nil && leaf.Pane == sentinel {
			leaf.Pane = nil
			ph = leaf
		}
	case !tab.reserveReplace && tab.reserveSibling != "" && tab.Root.FindLeaf(tab.reserveSibling) != nil:
		ph = tab.SplitAtPane(tab.reserveSibling, tab.reserveDir)
	}
	if ph == nil {
		ph = tab.spiralSlot(m.paneAreaWidth(), m.height-chromeHeight)
	}
	if ph == nil {
		// Nothing left to sit beside: the reservation is the whole tab.
		ph = &LayoutNode{Ratio: 0.5}
		tab.Root = ph
	}
	ph.phType = old.phType
	tab.invalidateLeaves()
	m.pendingSplit[tab.ID] = ph
}

// resetLayoutSync forgets every tab's revision on dest and marks the first
// broadcast after a reattach for adoption whatever its revision: the daemon's
// stored tree is the authority, and after a daemon restart its revision can be
// LOWER than ours (the snapshot is debounced) — as low as the 0 of a tree no
// client has written since restore, which even a zeroed revision would not
// adopt. Pending close requests for dest are dropped too: the daemon that
// answers may not be the one they were sent to.
func (m *Model) resetLayoutSync(dest string) {
	for _, proj := range m.projects {
		if proj.Dest != dest {
			continue
		}
		for _, tab := range proj.tabs {
			tab.layoutRev = 0
			tab.adoptNext = true
			tab.layoutDirty, tab.layoutSent, tab.layoutResend = false, nil, false
			tab.clearAwaiting()
		}
	}
	prefix := dest + "\x00"
	for k := range m.closeRequested {
		if strings.HasPrefix(k, prefix) {
			delete(m.closeRequested, k)
		}
	}
}
