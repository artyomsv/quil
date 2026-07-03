package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/hookevents"
)

// workSpinnerInterval is the animation cadence for the work-in-progress
// spinner (shared by tab and pane indicators).
const workSpinnerInterval = 100 * time.Millisecond

// workTransition classifies a pane event's effect on a pane's working state.
// Alias of hookevents.WorkEventKind — that package is the single source of
// truth (shared with the daemon's mute-bypass logic in emitEvent).
type workTransition = hookevents.WorkEventKind

const (
	workNone  = hookevents.WorkEventNone  // no effect
	workStart = hookevents.WorkEventStart // a turn began
	workStop  = hookevents.WorkEventStop  // turn completed OR parked for user input → mark pane unseen
	workAbort = hookevents.WorkEventAbort // process exited → clear working, no mark
)

// workEventKind maps a PaneEvent Type (the daemon encodes hook events as
// "hook.<src>.<event>") to a working-state transition.
func workEventKind(eventType string) workTransition {
	return hookevents.ClassifyWorkEvent(eventType)
}

// findPaneAndTab locates a pane by ID and the index of its containing tab.
// Returns (nil, -1) if not found.
func (m *Model) findPaneAndTab(paneID string) (*PaneModel, int) {
	for i, tab := range m.tabs {
		if tab.Root == nil {
			continue
		}
		if leaf := tab.Root.FindLeaf(paneID); leaf != nil {
			return leaf.Pane, i
		}
	}
	return nil, -1
}

// applyWorkTransition updates the working state of the pane identified by
// paneID based on the event type. On a normal completion or park, any pane
// that is not the focused pane of the active tab gets a persistent unseen
// mark — green border + derived green tab label — cleared when the user
// focuses the pane (ackFocusedPane at Update entry). There is no timer.
func (m *Model) applyWorkTransition(paneID, eventType string) {
	kind := workEventKind(eventType)
	if kind == workNone {
		return
	}
	pane, tabIdx := m.findPaneAndTab(paneID)
	if pane == nil {
		return
	}
	switch kind {
	case workStart:
		pane.working = true
		// Seed the pane spinner with the shared frame so the tab and pane
		// glyphs are in sync from the first render (before the next tick).
		pane.workFrame = m.workSpinnerFrame
		// A (re)start means the work is no longer "finished/parked" — the
		// spinner supersedes the green unseen mark. Covers both a fresh turn
		// after a previous completion and a resume after the user answers a
		// prompt (PostToolUse arrives while the mark is set).
		pane.unseen = false
	case workStop:
		wasWorking := pane.working
		pane.working = false
		// Mark unless the user is looking straight at the pane: completion
		// in the focused pane of the active tab is seen by definition. An
		// unfocused split sibling IS marked — its green border is the cue.
		focused := tabIdx == m.activeTab && m.tabs[tabIdx].ActivePane == paneID
		if wasWorking && !focused {
			pane.unseen = true
		}
	case workAbort:
		pane.working = false
	}
}

// ackFocusedPane clears the unseen mark on the focused pane of the active
// tab, called once at the top of Update. Correctness does not depend on a
// render having happened between messages (the renderer coalesces frames):
// a focused pane never renders the mark anyway — tabUnseen excludes the
// active tab and the pane border gives the active style precedence — and
// focusing the pane is itself the acknowledgement. This single choke point
// replaces auditing every ActivePane/activeTab assignment (13 call sites);
// a newly focused pane is acknowledged on the next message (the 1 s size
// poll bounds the wait). Unfocused panes keep their mark until focused.
func (m *Model) ackFocusedPane() {
	if m.activeTab < 0 || m.activeTab >= len(m.tabs) {
		return
	}
	tab := m.tabs[m.activeTab]
	if tab == nil || tab.Root == nil || tab.ActivePane == "" {
		return
	}
	// Deliberately not ActivePaneModel(): that helper heals a stale
	// ActivePane and sets Active — side effects we must not run per message.
	for _, p := range tab.Leaves() {
		if p != nil && p.ID == tab.ActivePane {
			p.unseen = false
			return
		}
	}
}

// anyPaneWorking reports whether any pane in any tab is mid-turn.
func (m Model) anyPaneWorking() bool {
	for _, tab := range m.tabs {
		if tab.Root == nil {
			continue
		}
		for _, p := range tab.Leaves() {
			if p != nil && p.working {
				return true
			}
		}
	}
	return false
}

// tabHasWorkingPane reports whether the tab at idx has at least one mid-turn pane.
func (m Model) tabHasWorkingPane(idx int) bool {
	if idx < 0 || idx >= len(m.tabs) || m.tabs[idx].Root == nil {
		return false
	}
	for _, p := range m.tabs[idx].Leaves() {
		if p != nil && p.working {
			return true
		}
	}
	return false
}

// tabUnseen reports whether the background tab at idx contains at least one
// pane with an unacknowledged work-finished mark. Purely derived from pane
// state — the active tab always reports false (the user is on it; the pane
// border carries the cue there).
func (m Model) tabUnseen(idx int) bool {
	if idx < 0 || idx >= len(m.tabs) || idx == m.activeTab || m.tabs[idx].Root == nil {
		return false
	}
	for _, p := range m.tabs[idx].Leaves() {
		if p != nil && p.unseen {
			return true
		}
	}
	return false
}

// workSpinnerTick schedules the next shared work-spinner frame.
func (m Model) workSpinnerTick() tea.Cmd {
	return tea.Tick(workSpinnerInterval, func(time.Time) tea.Msg { return workSpinnerTickMsg{} })
}

// syncPaneMeta copies daemon-authoritative metadata from a PaneInfo onto the
// local PaneModel during workspace-state reconciliation.
//
// Muting a pane does NOT clear `working` here: the daemon still delivers
// work-state hook events (start/stop/abort) live for a muted pane — it only
// suppresses the visible notification card (see emitEvent) — so the normal
// completion edge keeps `working` accurate across the whole mute/unmute
// window instead of going stale the instant the pane is muted.
func syncPaneMeta(pane *PaneModel, info *PaneInfo) {
	pane.Name = info.Name
	pane.CWD = info.CWD
	pane.Type = info.Type
	pane.Muted = info.Muted
	pane.Eager = info.Eager
	pane.Pending = info.Pending
	pane.SessionID = info.SessionID
	pane.HistoryLines = info.HistoryLines
	pane.daemonMouseTracking = info.MouseTracking
	pane.daemonMouseSGR = info.MouseSGR
}
