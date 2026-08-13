package tui

import (
	"testing"
	"time"
)

// twoTabProject builds one project with two tabs, one pane each, active on the
// first — the smallest shape a cross-tab jump can be observed in.
func twoTabProject(t *testing.T) Model {
	t.Helper()
	proj := &ProjectModel{ID: "proj-1", Name: "P"}
	proj.tabs = []*TabModel{
		tabWithPane("tab-1", "pane-1"),
		tabWithPane("tab-2", "pane-2"),
	}
	return Model{
		width: 120, height: 40,
		client:        newFakeConn(),
		projects:      []*ProjectModel{proj},
		activeProject: 0,
	}
}

// TestJumpToPane_TearsDownNotesMode: every cross-tab jump that is not switchTab
// funnels through jumpToPane — MCP set_active_pane, the notification sidebar,
// pane-history back, and the palette's Go to pane. All of them moved the active
// tab with the notes editor still open.
//
// The editor stays bound to a pane in the tab being left, still claiming its
// share of the width, and applyWorkspaceState's reconciliation does not rescue
// it: the bound pane is alive in a background tab whose ActivePane still names
// it. notesKeyExempt does not cover it either — that branch only runs while the
// EDITOR holds focus, and notes open beside a working agent normally has the
// PANE focused.
func TestJumpToPane_TearsDownNotesMode(t *testing.T) {
	m := twoTabProject(t)
	m.notesMode = true
	m.notesPaneFocused = true // the ordinary state notesKeyExempt does not cover

	if ok, _ := m.jumpToPane("pane-2"); !ok {
		t.Fatal("jumpToPane = false for a pane that exists")
	}

	if m.notesMode {
		t.Error("notes mode survived a jump to another tab: the editor is now " +
			"bound to a pane in the tab that was left, and still takes width " +
			"from the one on screen")
	}
	if m.notesEditor != nil {
		t.Error("notesEditor survived the jump — pending edits are unflushed")
	}
	if m.notesPaneFocused {
		t.Error("notesPaneFocused survived the jump")
	}
}

// TestJumpToPane_RevertsFocusOnTheTabItLeaves pins the ordering half. The
// teardown reverts focus mode on whichever tab is active WHEN IT RUNS, so
// running it after the tab has moved un-focuses the incoming tab and strands
// the outgoing one in focus mode — which is what goToPane did by calling
// switchTab only after jumpToPane had already moved things.
func TestJumpToPane_RevertsFocusOnTheTabItLeaves(t *testing.T) {
	m := twoTabProject(t)
	m.notesMode = true
	m.notesEnteredFocus = true // notes mode owns the focus toggle
	from := m.projects[0].tabs[0]
	to := m.projects[0].tabs[1]
	// Set directly rather than via ToggleFocus, which is a no-op on a
	// single-pane tab. How the tab got into focus mode is not what is under
	// test; that the teardown reverts the RIGHT tab is.
	from.focusMode = true

	m.jumpToPane("pane-2")

	if from.FocusMode() {
		t.Error("the tab being LEFT is still in focus mode — the teardown ran " +
			"against the wrong tab, so returning to it shows one pane full-screen " +
			"with no notes editor and no way to tell why")
	}
	if to.FocusMode() {
		t.Error("the tab being ENTERED was put into focus mode by a teardown " +
			"that belongs to the outgoing one")
	}
}

// TestJumpToNextBlocked_TearsDownNotesModeWithinOneProject: switchProject tears
// notes down only when the project actually CHANGES, and the attention queue
// routinely lands on another tab of the same project.
func TestJumpToNextBlocked_TearsDownNotesModeWithinOneProject(t *testing.T) {
	m := twoTabProject(t)
	// A pane in the OTHER tab of the same project, blocked and waiting.
	blocked := m.projects[0].tabs[1].ActivePaneModel()
	if blocked == nil {
		t.Fatal("fixture has no pane in tab 2")
	}
	blocked.blockedSince = time.Now()

	m.notesMode = true
	m.notesPaneFocused = true

	m.jumpToNextBlocked()

	if m.notesMode {
		t.Error("notes mode survived an attention-queue jump inside one " +
			"project: switchProject only tears down on a project change, and " +
			"this jump crossed only a tab boundary")
	}
}
