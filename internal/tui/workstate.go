package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// tabFlashDuration is how long an inactive tab label stays green after the
// pane it contains finishes a turn.
const tabFlashDuration = 5 * time.Second

// workSpinnerInterval is the animation cadence for the work-in-progress
// spinner (shared by tab and pane indicators).
const workSpinnerInterval = 100 * time.Millisecond

// workTransition classifies a pane event's effect on a pane's working state.
type workTransition int

const (
	workNone  workTransition = iota // no effect
	workStart                       // a turn began
	workStop                        // a turn completed normally → green flash
	workAbort                       // process exited → clear working, no flash
)

// workEventKind maps a PaneEvent Type (the daemon encodes hook events as
// "hook.<src>.<event>") to a working-state transition. This is the single
// source of truth for the work indicator — keep it in sync with the
// producers in internal/claudehook and internal/opencodehook.
func workEventKind(eventType string) workTransition {
	switch eventType {
	case "hook.claude.UserPromptSubmit", "hook.opencode.chat.message":
		return workStart
	case "hook.claude.Stop", "hook.claude.SessionEnd",
		"hook.opencode.session.idle", "hook.opencode.session.error":
		return workStop
	case "process_exit":
		return workAbort
	}
	return workNone
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
// paneID based on the event type. On a normal completion it stamps the
// containing tab's flashUntil and returns a one-shot tick cmd that re-renders
// when the flash expires. All other cases return nil.
func (m *Model) applyWorkTransition(paneID, eventType string) tea.Cmd {
	kind := workEventKind(eventType)
	if kind == workNone {
		return nil
	}
	pane, tabIdx := m.findPaneAndTab(paneID)
	if pane == nil {
		return nil
	}
	switch kind {
	case workStart:
		pane.working = true
		// Seed the pane spinner with the shared frame so the tab and pane
		// glyphs are in sync from the first render (before the next tick).
		pane.workFrame = m.workSpinnerFrame
	case workStop:
		wasWorking := pane.working
		pane.working = false
		if wasWorking {
			m.tabs[tabIdx].flashUntil = time.Now().Add(tabFlashDuration)
			return tea.Tick(tabFlashDuration, func(time.Time) tea.Msg { return flashTickMsg{} })
		}
	case workAbort:
		pane.working = false
	}
	return nil
}

// anyPaneWorking reports whether any pane in any tab is mid-turn.
func (m Model) anyPaneWorking() bool {
	for _, tab := range m.tabs {
		if tab.Root == nil {
			continue
		}
		for _, p := range tab.Root.Leaves() {
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
	for _, p := range m.tabs[idx].Root.Leaves() {
		if p != nil && p.working {
			return true
		}
	}
	return false
}

// tabFlashing reports whether the tab at idx is within its green flash window.
func (m Model) tabFlashing(idx int) bool {
	if idx < 0 || idx >= len(m.tabs) {
		return false
	}
	t := m.tabs[idx].flashUntil
	return !t.IsZero() && time.Now().Before(t)
}

// workSpinnerTick schedules the next shared work-spinner frame.
func (m Model) workSpinnerTick() tea.Cmd {
	return tea.Tick(workSpinnerInterval, func(time.Time) tea.Msg { return workSpinnerTickMsg{} })
}

// syncPaneMeta copies daemon-authoritative metadata from a PaneInfo onto the
// local PaneModel during workspace-state reconciliation.
//
// Muting a pane clears any in-progress work indicator: the daemon drops a
// muted pane's hook events at the source, so the completion edge that would
// normally clear `working` never reaches the TUI. Without this, muting a pane
// mid-turn would strand its spinner — and keep the 100ms animation tick alive
// — indefinitely.
func syncPaneMeta(pane *PaneModel, info *PaneInfo) {
	pane.Name = info.Name
	pane.CWD = info.CWD
	pane.Type = info.Type
	pane.Muted = info.Muted
	pane.Eager = info.Eager
	if info.Muted {
		pane.working = false
	}
}
