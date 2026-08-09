package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// clearAttentionIndex finds the Clear attention row in an open menu.
func clearAttentionIndex(t *testing.T, m Model) int {
	t.Helper()
	for i, it := range m.ctxMenu.items {
		if it.id == ctxActClearAttention {
			return i
		}
	}
	t.Fatal("no Clear attention row in the pane context menu")
	return -1
}

// blockedSince is written only by hook edges (workstate.go), and cleared only
// by other hook edges — so a pane whose clearing event never arrives stays
// marked for the life of the TUI process, and the project row it rolls up into
// stays marked with it.
//
// Driven through Update rather than by calling executeCtxMenuItem directly: a
// direct-call test passes against a row the menu never offers, which is exactly
// how a disabled row's dispatch can look correct while being unreachable.
func TestCtxMenu_ClearAttentionDropsAStuckBlockedMark(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	pane := m.curTabs()[0].Root.Left.Pane
	pane.blockedSince = time.Now().Add(-time.Hour)
	pane.blockedReason = "Bash"
	pane.unseen = true
	pane.pinnedAttention = true

	updated, _ := m.Update(tea.MouseClickMsg{X: 20, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	if !got.ctxMenu.open() {
		t.Fatal("right-click should open the menu")
	}
	idx := clearAttentionIndex(t, got)
	if !got.ctxMenu.items[idx].enabled {
		t.Fatal("Clear attention must be enabled while the pane is blocked")
	}
	got.ctxMenu.cursor = idx
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got = updated.(Model)

	target := got.curTabs()[0].Root.Left.Pane
	if !target.blockedSince.IsZero() {
		t.Error("blockedSince should be cleared")
	}
	if target.blockedReason != "" {
		t.Errorf("blockedReason = %q, want empty", target.blockedReason)
	}
	// All four marks, or the row half-works: clearing only the blocked mark
	// leaves the pane rendering green and the project row still counting it.
	if target.unseen {
		t.Error("unseen should be cleared too")
	}
	// The PIN is the exception, and not clearing it here is the point: it is
	// daemon-owned, so the row SENDS the clear and the broadcast that answers is
	// what drops the ◆. Asserting a local clear here would be asserting the bug
	// — a local write blinks against a broadcast already in flight, and leaves a
	// visible lie when the link is parked and the send is dropped. The send
	// itself is pinned by TestCtxMenu_ExecuteClearAttention_SendsThePinClear.
	if got.ctxMenu.open() {
		t.Error("menu should close after the action runs")
	}
}

// TestAckFocusedPane_KeepsTheBlockedMark pins the REVISED item 6.2. The
// original rule had focus clear blockedSince alongside unseen; that was
// reversed after review, because ackFocusedPane runs at the top of EVERY
// Update — including the shared 100 ms workSpinnerTickMsg, which is guaranteed
// to be ticking while a pane is working. So a pane that is the focused pane of
// the active tab when it parks had its mark set and cleared ~100 ms later, and
// the ▲ was never observable at all in the commonest park of all (the agent
// asks for permission while you are sitting in its pane).
//
// unseen is a "you missed something" flag genuinely answered by looking.
// blockedSince is a fact about the AGENT, so clearing it on a spinner tick
// destroys information rather than acknowledging a notification. The glyph is
// suppressed at render for the focused pane instead — see
// TestPaneRow_BlockedFocusedSuppressesTheGlyph — which keeps counts(),
// tabBlocked and the attention queue truthful and restores every signal the
// moment the user leaves the pane, with no hook edge required.
//
// pinnedAttention still deliberately SURVIVES: it is the explicit "don't let me
// forget" mark, and auto-clearing it would leave no way to express that.
func TestAckFocusedPane_KeepsTheBlockedMark(t *testing.T) {
	t.Parallel()
	m := newTestModelWithTabs(t, 1, 1)
	tab := m.curTabs()[0]
	pane := tab.Leaves()[0]
	tab.ActivePane = pane.ID
	pane.unseen = true
	pane.blockedSince = time.Now()
	pane.blockedReason = "Bash"
	pane.pinnedAttention = true

	m.ackFocusedPane()

	if pane.unseen {
		t.Error("unseen should be cleared")
	}
	if pane.blockedSince.IsZero() {
		t.Error("blockedSince must SURVIVE focus — a spinner tick is not an answer")
	}
	if pane.blockedReason != "Bash" {
		t.Errorf("blockedReason = %q, want %q — it must survive with the mark", pane.blockedReason, "Bash")
	}
	if !pane.pinnedAttention {
		t.Error("pinnedAttention must survive focus")
	}
}

// TestAckFocusedPane_KeepsEveryDerivedBlockedSignal is the half of the ruling
// that a per-pane assertion cannot see: the state is kept precisely so the
// levels ABOVE the pane row stay truthful while the user sits in the pane
// without answering. The tab mark, the project badge's blocked count and the
// attention queue all derive from blockedSince, and before the revision each of
// them went dark within one spinner tick of the park.
func TestAckFocusedPane_KeepsEveryDerivedBlockedSignal(t *testing.T) {
	t.Parallel()
	m := newTestModelWithTabs(t, 1, 2)
	tab := m.curTabs()[0]
	panes := tab.Leaves()
	if len(panes) < 2 {
		t.Skip("fixture needs a two-pane tab")
	}
	tab.ActivePane = panes[0].ID
	panes[0].blockedSince = time.Now()
	panes[1].blockedSince = time.Now()

	m.ackFocusedPane()

	if panes[0].blockedSince.IsZero() {
		t.Error("the focused pane's mark must survive")
	}
	if panes[1].blockedSince.IsZero() {
		t.Error("unfocused sibling must keep its mark")
	}
	if !m.tabBlocked(0) {
		t.Error("tab must stay marked")
	}
	if blocked := m.projects[0].counts().blocked; blocked != 2 {
		t.Errorf("project badge counts %d blocked, want 2 — focus must not shrink the roll-up", blocked)
	}
	if got := len(m.blockedPanes()); got != 2 {
		t.Errorf("attention queue holds %d panes, want 2 — a focused pane must not drain out of it", got)
	}
}

// TestAckFocusedPane_ClearsOnlyTheUnseenMark bounds the ack to the one flag it
// still owns, at the level the ruling is stated: everything else on the focused
// pane is left exactly as it was found.
func TestAckFocusedPane_ClearsOnlyTheUnseenMark(t *testing.T) {
	t.Parallel()
	m := newTestModelWithTabs(t, 1, 1)
	tab := m.curTabs()[0]
	pane := tab.Leaves()[0]
	tab.ActivePane = pane.ID

	since := time.Now().Add(-time.Hour)
	pane.blockedSince = since
	pane.blockedReason = "AskUserQuestion"
	pane.unseen = true
	pane.working = true
	pane.turnActive = true

	m.ackFocusedPane()

	if pane.unseen {
		t.Error("unseen should be cleared")
	}
	if !pane.blockedSince.Equal(since) {
		t.Errorf("blockedSince = %v, want it untouched at %v", pane.blockedSince, since)
	}
	if !pane.working || !pane.turnActive {
		t.Error("the ack must not touch the working state")
	}
}

// parkedInputTestModel builds a Model whose panes can receive real PTY input:
// a wired client and inputCh, so every MsgPaneInput producer runs its true path
// instead of the nil-channel fallback. panes[0] is the active one (tabWith).
func parkedInputTestModel(t *testing.T, paneIDs ...string) *Model {
	t.Helper()
	panes := make([]*PaneModel, len(paneIDs))
	for i, id := range paneIDs {
		panes[i] = NewPaneModel(id, 1024)
	}
	return &Model{
		projects: oneProject(tabWith(panes...)),
		client:   &fakeSender{},
		inputCh:  make(chan paneInput, inputForwardBuffer),
	}
}

// TestUserInput_ClearsTheParkedMark pins the rule that closes the gap the
// revised item 6.2 left open: a glance is not an answer, but a keystroke is.
//
// Approving a Bash/Edit/Write permission prompt fires NO hook of its own — the
// pane's next event is the turn's Stop, which can be minutes away — so with
// focus no longer clearing the mark, an ANSWERED prompt otherwise left the tab
// amber, the project badge reporting blocked rather than working, Alt+Shift+A
// still offering the pane, and the ▲ returning the instant the user switched
// away. Real input routed to the pane is the one signal that distinguishes
// answering the prompt from looking at it.
//
// Driven through handleKey — the real keystroke entry point — rather than the
// clear itself, so the test fails if the branch is ever placed somewhere a
// typed key does not reach.
func TestUserInput_ClearsTheParkedMark(t *testing.T) {
	t.Parallel()
	m := parkedInputTestModel(t, "p1")
	pane := m.curTabs()[0].Leaves()[0]
	pane.blockedSince = time.Now()
	pane.blockedReason = "Bash"

	m.handleKey(tea.KeyPressMsg{Text: "y"})

	if !pane.blockedSince.IsZero() {
		t.Error("blockedSince should be cleared by real user input")
	}
	if pane.blockedReason != "" {
		t.Errorf("blockedReason = %q, want empty", pane.blockedReason)
	}
	// Every derived level has to go with it, or the pane reads answered while
	// the tab bar and the queue still say it is waiting.
	if m.tabBlocked(0) {
		t.Error("the tab must stop reading blocked once the pane was answered")
	}
	if blocked := m.projects[0].counts().blocked; blocked != 0 {
		t.Errorf("project badge counts %d blocked, want 0", blocked)
	}
	if got := len(m.blockedPanes()); got != 0 {
		t.Errorf("attention queue holds %d panes, want 0", got)
	}
}

// TestPasteToUnfocusedPane_ClearsTheParkedMark: the clear is about input
// REACHING a pane, not about which pane holds focus. The asynchronous paste
// path binds its target when the user asks and delivers after the clipboard
// read, so it can legitimately land on a pane that is not the active one.
func TestPasteToUnfocusedPane_ClearsTheParkedMark(t *testing.T) {
	t.Parallel()
	m := parkedInputTestModel(t, "p1", "p2")
	panes := m.curTabs()[0].Leaves()
	target := panes[1] // NOT the active pane
	if target.ID == m.curTabs()[0].ActivePane {
		t.Fatal("fixture must target an unfocused pane")
	}
	target.blockedSince = time.Now()
	target.blockedReason = "Edit"

	m.sendClipboardToPaneID(target.ID, "approved")

	if !target.blockedSince.IsZero() {
		t.Error("a paste into the pane should clear its parked mark")
	}
	if target.blockedReason != "" {
		t.Errorf("blockedReason = %q, want empty", target.blockedReason)
	}

	// The synchronous paste path answers the ACTIVE pane the same way.
	panes[0].blockedSince = time.Now()
	m.sendClipboardToPane("approved")
	if !panes[0].blockedSince.IsZero() {
		t.Error("a paste into the active pane should clear its parked mark too")
	}
}

// TestMouseDerivedInput_DoesNotClearTheParkedMark is the other half of the
// rule, and the reason the clear does NOT sit on enqueueInput or on
// forwardInputBytes even though both look like the single choke point.
//
// Both carry traffic the USER did not type. enqueueInput takes forwarded wheel
// notches (sendInputToPane), and forwardInputBytes is also called by the
// selection handler to walk the shell cursor with a mouse DRAG — arrow-key
// escapes that a permission prompt would happily consume as a choice.
// Scrolling or dragging across a parked pane is a glance with a mouse, so
// neither may count as an answer.
func TestMouseDerivedInput_DoesNotClearTheParkedMark(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		send func(m *Model)
	}{
		{"forwarded wheel notch", func(m *Model) {
			m.sendInputToPane("p1", []byte("\x1b[<64;1;1M"))
		}},
		{"selection drag walking the shell cursor", func(m *Model) {
			m.forwardInputBytes([]byte("\x1b[C"))
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := parkedInputTestModel(t, "p1")
			pane := m.curTabs()[0].Leaves()[0]
			since := time.Now().Add(-time.Minute)
			pane.blockedSince = since
			pane.blockedReason = "Bash"

			tt.send(m)

			if !pane.blockedSince.Equal(since) {
				t.Errorf("blockedSince = %v, want it untouched at %v — a mouse gesture is not an answer",
					pane.blockedSince, since)
			}
			if pane.blockedReason != "Bash" {
				t.Errorf("blockedReason = %q, want it untouched", pane.blockedReason)
			}
			if !m.tabBlocked(0) {
				t.Error("the tab must still read blocked")
			}
		})
	}
}

// The row answers "is this pane actually still flagged" as well as clearing
// it, so on a pane with nothing to clear it must be inert rather than a no-op
// that looks like it did something.
func TestCtxMenu_ClearAttentionDisabledOnACleanPane(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	pane := m.curTabs()[0].Root.Left.Pane

	items := m.buildCtxMenuItems(pane)
	for _, it := range items {
		if it.id == ctxActClearAttention && it.enabled {
			t.Fatal("Clear attention must be disabled when the pane carries no mark")
		}
	}

	// The row's label and its group separator are both unverified elsewhere.
	// gapAfter moved from ctxActAttention to this row so the blank line still
	// falls between the pane-settings group and the destructive one — a wrong
	// position is invisible to every other assertion, because nothing else
	// depends on where the separator sits.
	var idx = -1
	for i, it := range items {
		if it.id == ctxActClearAttention {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("no Clear attention row")
	}
	if items[idx].label != "Clear attention" {
		t.Errorf("label = %q, want %q", items[idx].label, "Clear attention")
	}
	if !items[idx].gapAfter {
		t.Error("Clear attention must carry gapAfter — it is the last row of the pane-settings group")
	}
	if items[idx-1].id != ctxActAttention || items[idx-1].gapAfter {
		t.Error("the separator must have MOVED off ctxActAttention, not been added beside it")
	}

	// Each mark independently enables it — a pane can be unseen without ever
	// having been blocked, and pinned without either.
	for name, set := range map[string]func(){
		"blocked": func() { pane.blockedSince = time.Now() },
		"unseen":  func() { pane.unseen = true },
		"pinned":  func() { pane.pinnedAttention = true },
	} {
		pane.blockedSince = time.Time{}
		pane.unseen = false
		pane.pinnedAttention = false
		set()
		enabled := false
		for _, it := range m.buildCtxMenuItems(pane) {
			if it.id == ctxActClearAttention {
				enabled = it.enabled
			}
		}
		if !enabled {
			t.Errorf("Clear attention should be enabled when the pane is %s", name)
		}
	}
}
