package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// workSpinnerInterval is the animation cadence for the work-in-progress
// spinner (shared by tab and pane indicators).
const workSpinnerInterval = 100 * time.Millisecond

// workTransition classifies a pane event's effect on a pane's working state.
type workTransition int

const (
	workNone  workTransition = iota // no effect
	workStart                       // a turn began
	workStop                        // turn completed OR parked for user input → mark pane unseen
	workAbort                       // process exited → clear working, no mark
)

// workEventKind maps a PaneEvent Type (the daemon encodes hook events as
// "hook.<src>.<event>") to a working-state transition. This is the single
// source of truth for the work indicator — keep it in sync with the
// producers in internal/claudehook and internal/opencodehook.
func workEventKind(eventType string) workTransition {
	switch eventType {
	case "hook.claude.UserPromptSubmit", "hook.opencode.chat.message":
		return workStart
	// Resume edge: the user answered an interactive-prompt tool (AskUserQuestion
	// / ExitPlanMode) and the agent is working again. The hook registers
	// PostToolUse only for those tools, so this re-arms the spinner after a park
	// without tracking ordinary tool completions.
	case "hook.claude.PostToolUse":
		return workStart
	case "hook.claude.Stop", "hook.claude.SessionEnd",
		"hook.opencode.session.idle", "hook.opencode.session.error":
		return workStop
	// Park-for-input edges: the agent is blocked waiting on the user (permission
	// prompt, option select, idle-input nudge). There is no "resumed after
	// approval" hook, so we treat the park as a turn boundary — stop the spinner
	// and flash the tab green to pull attention. Both Claude (Notification fires
	// for permission + idle-wait; PermissionRequest when available) and opencode
	// (permission.ask) are covered.
	case "hook.claude.Notification", "hook.claude.PermissionRequest",
		"hook.opencode.permission.ask":
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
