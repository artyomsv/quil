package tui

import (
	"fmt"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// newTestModelWithTabs builds a Model with a single project holding n tabs,
// each with panesPerTab panes ("pane-<tab>-<pane>") split left-to-right via
// tabWith. Tab 0 is the project's active tab. The package's other fixtures
// (tabWith, Model{...} literals) build a specific tab/pane graph by hand;
// tests that assert against a tab INDEX rather than a specific pane want a
// fixture that hands back N of them directly.
func newTestModelWithTabs(t *testing.T, n, panesPerTab int) Model {
	t.Helper()
	tabs := make([]*TabModel, n)
	for i := 0; i < n; i++ {
		panes := make([]*PaneModel, panesPerTab)
		for j := 0; j < panesPerTab; j++ {
			panes[j] = &PaneModel{ID: fmt.Sprintf("pane-%d-%d", i, j)}
		}
		tabs[i] = tabWith(panes...)
	}
	return Model{
		projects:      []*ProjectModel{{ID: "proj-a", tabs: tabs}},
		activeProject: 0,
	}
}

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

// TestJumpToNextBlockedTellsTheDaemonAboutTheTargetTab: MsgSwitchProject only
// reaches the project's REMEMBERED tab, and the blocked pane is routinely in a
// different one. After a lazy restore those panes are still Pending, so without
// an explicit tab switch the queue lands the user on a restore indicator with
// no process behind it.
func TestJumpToNextBlockedTellsTheDaemonAboutTheTargetTab(t *testing.T) {
	blocked := &PaneModel{ID: "pane-blocked"}
	blocked.blockedSince = time.Now()

	remembered := tabWith(&PaneModel{ID: "pane-other"})
	target := tabWith(blocked)
	target.Dest = "gpu01"
	remembered.Dest = "gpu01"

	fake := newFakeConn()
	m := Model{
		client: fake,
		projects: []*ProjectModel{
			{ID: "proj-a", tabs: []*TabModel{tabWith(&PaneModel{ID: "pane-idle"})}},
			// activeTab 0 is the project's remembered tab; the blocked pane is
			// in tab 1, so a project switch alone never reaches it.
			{ID: "proj-b", Dest: "gpu01", tabs: []*TabModel{remembered, target}},
		},
		activeProject: 0,
	}

	m.jumpToNextBlocked()

	var switched *ipc.Message
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgSwitchTab {
			switched = msg
		}
	}
	if switched == nil {
		t.Fatal("no MsgSwitchTab sent — the blocked pane's tab is never spawned")
	}
	var payload ipc.SwitchTabPayload
	if err := switched.DecodePayload(&payload); err != nil {
		t.Fatalf("decode switch payload: %v", err)
	}
	if payload.TabID != target.ID {
		t.Errorf("MsgSwitchTab TabID = %q, want the blocked pane's tab %q", payload.TabID, target.ID)
	}
	if switched.Origin != "gpu01" {
		t.Errorf("MsgSwitchTab Origin = %q, want gpu01", switched.Origin)
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

// TestAttentionQueueKeyEmptyQueueFlashes: with nothing blocked anywhere the
// key used to be completely silent, which is indistinguishable from a broken
// binding — it was reported as one. Same treatment as the SidebarToggle
// narrow-terminal refusal. The second half pins that the flash arm does not
// hijack the working path: with a blocked pane the jump still happens and no
// flash is shown.
func TestAttentionQueueKeyEmptyQueueFlashes(t *testing.T) {
	newModel := func(panes ...*PaneModel) Model {
		return Model{
			client:        newFakeConn(),
			cfg:           config.Default(),
			width:         100,
			height:        30,
			notifications: NewNotificationCenter(30, 50),
			mcpHighlights: make(map[string]bool),
			projects: []*ProjectModel{
				{ID: "proj-a", tabs: []*TabModel{tabWith(&PaneModel{ID: "pane-idle"})}},
				{ID: "proj-b", tabs: []*TabModel{tabWith(panes[0])}},
			},
			activeProject: 0,
		}
	}
	press := tea.KeyPressMsg{Mod: tea.ModAlt | tea.ModShift, Code: 'a'}

	m := newModel(&PaneModel{ID: "pane-also-idle"})
	updated, cmd := m.handleKey(press)
	got := updated.(Model)
	if got.flashText != "no agent is waiting on you" {
		t.Errorf("flashText = %q, want %q", got.flashText, "no agent is waiting on you")
	}
	if cmd == nil {
		t.Error("the flash must be returned with its expiry timer (flashCmd), or it never clears")
	}
	if got.activeProject != 0 {
		t.Errorf("activeProject = %d, want unchanged 0 — an empty queue must not move focus", got.activeProject)
	}

	blocked := &PaneModel{ID: "pane-blocked"}
	blocked.blockedSince = time.Now()
	updated, _ = newModel(blocked).handleKey(press)
	got = updated.(Model)
	if got.flashText != "" {
		t.Errorf("flashText = %q, want empty — a non-empty queue jumps instead of flashing", got.flashText)
	}
	if got.activeProject != 1 {
		t.Errorf("activeProject = %d, want 1 — the queue must still jump into proj-b", got.activeProject)
	}
}

// TestAttentionQueueKeyFiresWhileNotesEditorFocused pins the notesKeyExempt
// entry for kb.AttentionQueue: Alt+Shift+A must reach jumpToNextBlocked (and
// tear down notes mode first, on the OLD tab) rather than being consumed as
// literal "a" text by the focused notes editor — the asymmetry ProjectPicker
// and ProjectToggle already avoid.
func TestAttentionQueueKeyFiresWhileNotesEditorFocused(t *testing.T) {
	dir := t.TempDir()
	ne, err := NewNotesEditor(dir, "pane-notes", "Notes", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}

	blocked := &PaneModel{ID: "pane-blocked"}
	blocked.blockedSince = time.Now()

	m := Model{
		client:        newFakeConn(),
		cfg:           config.Default(),
		width:         100,
		height:        30,
		notesMode:     true,
		notesEditor:   ne,
		notifications: NewNotificationCenter(30, 50),
		mcpHighlights: make(map[string]bool),
		projects: []*ProjectModel{
			{ID: "proj-a", tabs: []*TabModel{tabWith(&PaneModel{ID: "pane-notes-bound"})}},
			{ID: "proj-b", tabs: []*TabModel{tabWith(blocked)}},
		},
		activeProject: 0,
	}

	if m.cfg.Keybindings.AttentionQueue != "alt+shift+a" {
		t.Fatalf("default attention_queue = %q, want alt+shift+a", m.cfg.Keybindings.AttentionQueue)
	}

	// Text must be empty; Mod carries both so String() → "alt+shift+a"
	// (mirrors TestSidebarToggleKeyFlipsResizesAndPersists).
	updated, _ := m.handleKey(tea.KeyPressMsg{Mod: tea.ModAlt | tea.ModShift, Code: 'a'})
	got := updated.(Model)

	if got.notesMode {
		t.Fatal("alt+shift+a while notes-editor-focused left notesMode true — the key was swallowed as text instead of exiting notes and firing the queue")
	}
	if got.activeProject != 1 {
		t.Fatalf("activeProject = %d, want 1 — the queue must have fired and crossed into proj-b", got.activeProject)
	}
	if got := got.projects[1].tabs[0].ActivePane; got != "pane-blocked" {
		t.Fatalf("ActivePane = %s, want pane-blocked", got)
	}
}

// TestTabBlocked_ReportsParkedPaneOnBackgroundTab pins the tab-level blocked
// mark. Before this existed a parked pane showed ▲ in the sidebar while its
// tab showed nothing at all — the defect in item 6.1.
func TestTabBlocked_ReportsParkedPaneOnBackgroundTab(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		blocked     bool
		activeTab   bool
		focusedPane bool
		want        bool
	}{
		{"parked pane on background tab", true, false, false, true},
		{"parked pane on active tab", true, true, false, true},
		{"parked pane is the focused pane", true, true, true, true},
		{"no parked pane", false, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModelWithTabs(t, 2, 1)
			ti := 1
			if tt.activeTab {
				ti = 0
			}
			tab := m.curTabs()[ti]
			pane := tab.Leaves()[0]
			if tt.blocked {
				pane.blockedSince = time.Now()
			}
			if tt.focusedPane {
				tab.ActivePane = pane.ID
			}
			if got := m.tabBlocked(ti); got != tt.want {
				t.Errorf("tabBlocked(%d) = %v, want %v", ti, got, tt.want)
			}
		})
	}
}

// TestTabStyle_BlockedOutranksUnseen pins the precedence. A pane that is
// blocked AND unseen must read as blocked: "needs you" is the more urgent of
// the two, and it is the one the user can act on.
func TestTabStyle_BlockedOutranksUnseen(t *testing.T) {
	t.Parallel()
	m := newTestModelWithTabs(t, 2, 1)
	pane := m.curTabs()[1].Leaves()[0]
	pane.unseen = true
	pane.blockedSince = time.Now()
	if got := m.tabStyle(1); got.GetBackground() != blockedTabStyle.GetBackground() {
		t.Errorf("tabStyle background = %v, want blockedTabStyle %v",
			got.GetBackground(), blockedTabStyle.GetBackground())
	}
}
