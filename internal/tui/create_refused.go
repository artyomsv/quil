package tui

// createPaneRefusedMsg is the daemon refusing an ordinary create_pane or
// create_tab this client sent with an id: an `error` frame naming the request.
// It is the only answer such a create gets — success is the next broadcast —
// so without it a refusal (a toggle or kube context the daemon's plugin
// registry no longer knows) left a split placeholder on screen and a replaced
// pane gone until the next broadcast, with nothing said.
type createPaneRefusedMsg struct {
	dest string // Message.Origin: the daemon that refused
	id   string
	text string // the daemon's reason, unsanitized
}

// armOrdinaryCreate records the id an ordinary dialog create is sent with, so
// a refusal can find the tab whose reservation it has to unwind. Keyed by tab
// like pendingSplit, which the create also overwrites.
func (m *Model) armOrdinaryCreate(tabID string) string {
	id := "create-" + m.nextReqGen()
	if m.createReqIDs == nil {
		m.createReqIDs = make(map[string]string)
	}
	m.createReqIDs[tabID] = id
	return id
}

// holdReplacedPane keeps the pane an ordinary replace detached until the
// create settles. A pane still held from an earlier create in the same tab is
// disposed: that create's reservation was overwritten, so nothing can put it
// back.
func (m *Model) holdReplacedPane(tabID string, p *PaneModel) {
	if m.replaceHeld == nil {
		m.replaceHeld = make(map[string]*PaneModel)
	}
	if prev := m.replaceHeld[tabID]; prev != nil && prev != p {
		prev.Dispose()
	}
	m.replaceHeld[tabID] = p
}

// applyCreatePaneRefused flashes the daemon's reason and unwinds the create it
// names: the split placeholder is pruned, a replaced pane goes back into its
// leaf. Keep it to that — the daemon created nothing, so there is nothing else
// to undo.
//
// The flash is shown for any refusal on this conn, even one whose create has
// already been settled by a broadcast: only this client's own creates carry
// ids on it, so the refusal is still news to this user. The unwind needs the
// id to match a tab this client armed, on the daemon that refused.
func (m *Model) applyCreatePaneRefused(msg createPaneRefusedMsg) {
	m.setFlash("pane not created: " + truncateCells(sanitizeRemoteText(msg.text), createErrFlashCap))
	for tabID, id := range m.createReqIDs {
		if id != msg.id {
			continue
		}
		tab := m.tabByID(tabID)
		if tab != nil && tab.Dest != msg.dest {
			return // another daemon naming our id: not its create to unwind
		}
		delete(m.createReqIDs, tabID)
		held := m.replaceHeld[tabID]
		delete(m.replaceHeld, tabID)
		leaf := m.pendingSplit[tabID]
		delete(m.pendingSplit, tabID)
		if tab != nil {
			tab.noteReservation("", 0, false)
		}
		if tab == nil || tab.Root == nil {
			if held != nil {
				held.Dispose()
			}
			return
		}
		if held != nil {
			// Not when the tree already shows the pane again: a broadcast that
			// left before the daemon read this create rebuilt it from scratch,
			// and a second model for one id would be two views of one PTY.
			if leaf != nil && leaf.Pane == nil && treeContains(tab.Root, leaf) && tab.Root.FindLeaf(held.ID) == nil {
				leaf.fill(held)
			} else {
				held.Dispose()
			}
		}
		tab.Root.PrunePlaceholders()
		tab.invalidateLeaves()
		return
	}
}

// settleOrdinaryCreates retires the bookkeeping of every ordinary create whose
// reservation is gone — filled by the pane that create asked for, or pruned by
// a broadcast. Either way no refusal can unwind it any more, so a replaced
// pane still held is disposed: a filled leaf means the daemon swapped it out,
// and a pruned one leaves it nowhere to go (the next broadcast brings the pane
// back if the daemon still has it).
func (m *Model) settleOrdinaryCreates() {
	for tabID := range m.createReqIDs {
		if m.ordinaryCreateOver(tabID) {
			delete(m.createReqIDs, tabID)
		}
	}
	for tabID, held := range m.replaceHeld {
		if m.ordinaryCreateOver(tabID) {
			held.Dispose()
			delete(m.replaceHeld, tabID)
		}
	}
}

// ordinaryCreateOver reports whether tabID holds no reservation any more: none
// is armed, or the tab itself is gone.
func (m *Model) ordinaryCreateOver(tabID string) bool {
	return m.pendingSplit[tabID] == nil || m.tabByID(tabID) == nil
}
