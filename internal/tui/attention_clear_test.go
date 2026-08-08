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
	// All three marks, or the row half-works: clearing only the blocked mark
	// leaves the pane rendering green and the project row still counting it.
	if target.unseen {
		t.Error("unseen should be cleared too")
	}
	if target.pinnedAttention {
		t.Error("pinnedAttention should be cleared too")
	}
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
	if _, blocked, _ := m.projects[0].counts(); blocked != 2 {
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
