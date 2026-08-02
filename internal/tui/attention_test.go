package tui

import (
	"testing"
	"time"
)

func TestBlockedPanesOrderedOldestFirstAcrossProjects(t *testing.T) {
	now := time.Now()
	recent := &PaneModel{ID: "pane-recent"}
	recent.blockedSince = now.Add(-1 * time.Minute)
	oldest := &PaneModel{ID: "pane-oldest"}
	oldest.blockedSince = now.Add(-9 * time.Minute)
	idle := &PaneModel{ID: "pane-idle"}

	m := Model{projects: []*ProjectModel{
		{ID: "proj-a", tabs: []*TabModel{tabWith(recent), tabWith(idle)}},
		{ID: "proj-b", tabs: []*TabModel{tabWith(oldest)}},
	}}

	got := m.blockedPanes()
	if len(got) != 2 {
		t.Fatalf("blocked = %d, want 2 (the idle pane must not appear)", len(got))
	}
	if got[0].Pane.ID != "pane-oldest" {
		t.Fatalf("first = %s, want pane-oldest — order is blocked-longest-first",
			got[0].Pane.ID)
	}
}

func TestJumpToNextBlockedSwitchesProject(t *testing.T) {
	blocked := &PaneModel{ID: "pane-blocked"}
	blocked.blockedSince = time.Now()

	m := Model{
		client: newFakeConn(),
		projects: []*ProjectModel{
			{ID: "proj-a", tabs: []*TabModel{tabWith(&PaneModel{ID: "pane-idle"})}},
			{ID: "proj-b", tabs: []*TabModel{tabWith(blocked)}},
		},
		activeProject: 0,
	}

	m.jumpToNextBlocked()

	if m.activeProject != 1 {
		t.Fatalf("activeProject = %d, want 1 — the queue must cross project boundaries",
			m.activeProject)
	}
	if got := m.projects[1].tabs[0].ActivePane; got != "pane-blocked" {
		t.Fatalf("ActivePane = %s, want pane-blocked", got)
	}
}

// TestJumpToNextBlockedNoBlockedPanesIsNoop pins the empty-queue edge case: no
// pane anywhere is parked, so the key must not move focus or hand back a live
// command.
func TestJumpToNextBlockedNoBlockedPanesIsNoop(t *testing.T) {
	m := Model{
		client: newFakeConn(),
		projects: []*ProjectModel{
			{ID: "proj-a", tabs: []*TabModel{tabWith(&PaneModel{ID: "pane-idle"})}},
		},
		activeProject: 0,
	}

	if cmd := m.jumpToNextBlocked(); cmd != nil {
		t.Fatal("jumpToNextBlocked() with no blocked panes returned a live command, want nil")
	}
	if m.activeProject != 0 {
		t.Fatalf("activeProject = %d, want unchanged 0", m.activeProject)
	}
}

// TestJumpToNextBlockedOnlyBlockedIsFocusedCyclesToItself covers the
// single-entry queue where the user is already looking at the one blocked
// pane: the cycle-past-current logic must wrap to the same pane rather than
// panic or lose focus.
func TestJumpToNextBlockedOnlyBlockedIsFocusedCyclesToItself(t *testing.T) {
	blocked := &PaneModel{ID: "pane-blocked"}
	blocked.blockedSince = time.Now()
	tab := tabWith(blocked) // tabWith focuses panes[0], i.e. blocked, by default

	m := Model{
		client:        newFakeConn(),
		projects:      []*ProjectModel{{ID: "proj-a", tabs: []*TabModel{tab}}},
		activeProject: 0,
	}

	m.jumpToNextBlocked()

	if m.activeProject != 0 {
		t.Fatalf("activeProject = %d, want unchanged 0", m.activeProject)
	}
	if tab.ActivePane != "pane-blocked" {
		t.Fatalf("ActivePane = %s, want pane-blocked (cycled back to itself)", tab.ActivePane)
	}
}

// TestJumpToNextBlockedCyclesOldestFirstAcrossProjects drives three
// consecutive presses across three blocked panes in two projects and checks
// that each press advances to the NEXT-oldest entry rather than re-selecting
// the pane the user is already on, wrapping back to the start on the fourth
// step's equivalent (third press here, since there are three panes).
func TestJumpToNextBlockedCyclesOldestFirstAcrossProjects(t *testing.T) {
	now := time.Now()
	oldest := &PaneModel{ID: "pane-oldest"}
	oldest.blockedSince = now.Add(-9 * time.Minute)
	middle := &PaneModel{ID: "pane-middle"}
	middle.blockedSince = now.Add(-5 * time.Minute)
	newest := &PaneModel{ID: "pane-newest"}
	newest.blockedSince = now.Add(-1 * time.Minute)

	m := Model{
		client: newFakeConn(),
		projects: []*ProjectModel{
			{ID: "proj-a", tabs: []*TabModel{tabWith(oldest), tabWith(middle)}},
			{ID: "proj-b", tabs: []*TabModel{tabWith(newest)}},
		},
		activeProject: 0,
	}

	// Starts on pane-oldest (tabWith focuses panes[0] of tab 0).
	m.jumpToNextBlocked()
	if m.activeProject != 0 || m.projects[0].activeTab != 1 {
		t.Fatalf("after 1st press: activeProject=%d proj-a.activeTab=%d, want 0/1",
			m.activeProject, m.projects[0].activeTab)
	}
	if got := m.projects[0].tabs[1].ActivePane; got != "pane-middle" {
		t.Fatalf("after 1st press: ActivePane = %s, want pane-middle", got)
	}

	m.jumpToNextBlocked()
	if m.activeProject != 1 {
		t.Fatalf("after 2nd press: activeProject = %d, want 1 (proj-b)", m.activeProject)
	}
	if got := m.projects[1].tabs[0].ActivePane; got != "pane-newest" {
		t.Fatalf("after 2nd press: ActivePane = %s, want pane-newest", got)
	}

	m.jumpToNextBlocked()
	if m.activeProject != 0 || m.projects[0].activeTab != 0 {
		t.Fatalf("after 3rd press: activeProject=%d proj-a.activeTab=%d, want 0/0 (wrapped)",
			m.activeProject, m.projects[0].activeTab)
	}
	if got := m.projects[0].tabs[0].ActivePane; got != "pane-oldest" {
		t.Fatalf("after 3rd press: ActivePane = %s, want pane-oldest (wrapped to start)", got)
	}
}
