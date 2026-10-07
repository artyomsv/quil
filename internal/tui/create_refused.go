package tui

import (
	"log"

	tea "charm.land/bubbletea/v2"
)

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

// createPaneSendFailedMsg is a create_pane whose send failed: the router's conn
// for its destination is dead, or there is none. It is a send result rather
// than an IPC response, so its Update arm does not re-arm the listen loop.
//
// An ordinary create unwinds like a refusal, through its id. A worktree create
// carries no id — its bookkeeping is keyed by tab — so it unwinds the way a
// worktree failure does (unwindWorktreeCreate); otherwise its placeholder and
// held pane waited out the whole worktree timeout.
type createPaneSendFailedMsg struct {
	dest     string
	id       string // the ordinary create's request id; "" for a worktree create
	tabID    string
	worktree bool
	text     string
}

// createSendFailed is what a create_pane send closure returns when the send
// failed. Before it, the error was dropped and a split's placeholder waited
// for an answer the daemon never got.
func createSendFailed(dest, tabID, id string, worktree bool, err error) tea.Msg {
	log.Printf("create pane: send to %s: %v", hostLabel(dest), err)
	return createPaneSendFailedMsg{dest: dest, id: id, tabID: tabID, worktree: worktree, text: "cannot reach " + hostLabel(dest)}
}

// applyCreatePaneSendFailed unwinds a create whose send failed and flashes it.
func (m *Model) applyCreatePaneSendFailed(msg createPaneSendFailedMsg) {
	if msg.worktree {
		m.unwindWorktreeCreate(msg.tabID, msg.text)
		return
	}
	m.applyCreatePaneRefused(createPaneRefusedMsg{dest: msg.dest, id: msg.id, text: msg.text})
}

// createNotDone is the prefix of a flash that says a create was refused.
func createNotDone(target paneTarget) string {
	if target == paneTargetNewTab {
		return "new tab not created: "
	}
	return "pane not created: "
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

// retireOrdinaryCreate forgets tabID's ordinary create before another create
// re-arms the tab's reservation: the new one overwrites pendingSplit, so a late
// refusal of the earlier create would otherwise unwind the NEW placeholder (a
// worktree create's included). A pane that create still held cannot be put
// back any more — its leaf is no longer reserved — so it is disposed.
func (m *Model) retireOrdinaryCreate(tabID string) {
	delete(m.createReqIDs, tabID)
	if held := m.replaceHeld[tabID]; held != nil {
		held.Dispose()
		delete(m.replaceHeld, tabID)
	}
}

// heldReplacedPane is the pane a replace detached and still holds — worktree or
// ordinary — or nil. Both are live on the daemon while held.
func (m *Model) heldReplacedPane(id string) *PaneModel {
	for _, held := range m.worktreeReplaced {
		if held != nil && held.ID == id {
			return held
		}
	}
	for _, held := range m.replaceHeld {
		if held != nil && held.ID == id {
			return held
		}
	}
	return nil
}

// replaceHeldIn is the pane a replace in tabID detached and still holds —
// worktree or ordinary — or nil.
func (m *Model) replaceHeldIn(tabID string) *PaneModel {
	if held := m.worktreeReplaced[tabID]; held != nil {
		return held
	}
	return m.replaceHeld[tabID]
}

// takeOrdinaryHeld is the arrival loop's answer to a broadcast that still
// lists the pane an ordinary replace in tab holds: the broadcast left before
// the daemon read the create (or the create never arrives). The pane is live,
// so the HELD model goes back — a fresh one would show it with its output
// gone, and settling would then dispose the held one, leaving a later refusal
// nothing to restore. It returns to its reserved leaf when that leaf is still
// in the tree, and the create is retired: its leaf is taken, so a refusal has
// nothing to unwind and a success lands where an arrival lands. When the leaf
// is gone it returns the model for the caller to place as an arrival.
func (m *Model) takeOrdinaryHeld(tab *TabModel) (held *PaneModel, placed bool) {
	held = m.replaceHeld[tab.ID]
	delete(m.replaceHeld, tab.ID)
	delete(m.createReqIDs, tab.ID)
	ph := m.pendingSplit[tab.ID]
	if ph == nil || ph.Pane != nil || !treeContains(tab.Root, ph) {
		return held, false
	}
	delete(m.pendingSplit, tab.ID)
	tab.noteReservation("", 0, false)
	ph.fill(held)
	tab.invalidateLeaves()
	return held, true
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
