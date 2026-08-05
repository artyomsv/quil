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
