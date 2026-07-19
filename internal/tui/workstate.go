package tui

import (
	"strconv"
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
	workNone          = hookevents.WorkEventNone          // no effect
	workStart         = hookevents.WorkEventStart         // a turn began
	workStop          = hookevents.WorkEventStop          // turn completed OR parked for user input → mark pane unseen
	workAbort         = hookevents.WorkEventAbort         // process exited → clear working, no mark
	workSubagentStart = hookevents.WorkEventSubagentStart // subagent spawned → spinner on
	workSubagentStop  = hookevents.WorkEventSubagentStop  // subagent finished → spinner off once drained AND turn over
	workStopFinal     = hookevents.WorkEventStopFinal     // terminal stop → also clears the outstanding count
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
//
// `working` is DERIVED — recomputed at a single point below as
// turnActive || subagents > 0 — never assigned by hand in a branch, so no
// future edge can desync the spinner from its inputs. The main turn
// (turnActive) and the outstanding background-subagent count (subagents)
// are tracked separately: Claude Code runs subagents detached by default,
// so the main turn's Stop routinely fires while they are still grinding —
// the spinner must survive that edge and the unseen mark is deferred until
// the LAST subagent drains (which then becomes the completion edge).
//
// data carries the ingester's coalesced burst count: N same-type events
// inside the 50 ms debounce window arrive as ONE PaneEvent with
// data["coalesced"] = "N".
//
// Replay safety: the daemon replays the queued event history on attach,
// and attach happens exactly once per TUI process (Model.attached guard) —
// counters always start from zero and the ordered replay reconstructs the
// live state. The ring's oldest-first eviction can only ever orphan a
// SubagentStop (never strand a start behind its stop), and orphan stops
// are ignored below.
func (m *Model) applyWorkTransition(paneID, eventType string, data map[string]string) {
	kind := workEventKind(eventType)
	if kind == workNone {
		return
	}
	pane, tabIdx := m.findPaneAndTab(paneID)
	if pane == nil {
		return
	}
	wasWorking := pane.working
	abort := false
	switch kind {
	case workStart:
		pane.turnActive = true
	case workSubagentStart:
		pane.subagents += coalescedCount(data)
	case workSubagentStop:
		if pane.subagents == 0 {
			// Orphan stop (replay truncated by ring eviction, lost start) —
			// ignore rather than underflow, or the next start/stop pair
			// stops balancing.
			return
		}
		pane.subagents -= coalescedCount(data)
		if pane.subagents < 0 {
			pane.subagents = 0
		}
	case workStop, workStopFinal:
		pane.turnActive = false
		if kind == workStopFinal {
			// Terminal stop (session end): no subagent of the session can
			// still be alive — drop the count so a lost SubagentStop can't
			// wedge the spinner forever.
			pane.subagents = 0
		}
	case workAbort:
		pane.turnActive = false
		pane.subagents = 0
		abort = true
	}

	// Single derivation point for the spinner; the edge actions below key
	// off the before/after pair so they fire exactly once per transition.
	pane.working = pane.turnActive || pane.subagents > 0
	switch {
	case pane.working && !wasWorking:
		// Rising edge: seed the pane spinner with the shared frame so the
		// tab and pane glyphs are in sync from the first render, and clear
		// any stale mark — the spinner supersedes the green unseen cue.
		// (working ⇒ !unseen is an invariant: the mark is set only on the
		// falling edge below, so a start on an already-working pane has
		// nothing to clear.)
		pane.workFrame = m.workSpinnerFrame
		pane.unseen = false
	case !pane.working && wasWorking && !abort:
		// Falling edge on a genuine completion — turn over AND subagents
		// drained, whichever edge landed last. Mark unless the user is
		// looking straight at the pane: completion in the focused pane of
		// the active tab is seen by definition; an unfocused split sibling
		// IS marked — its green border is the cue. An abort (process exit)
		// clears the spinner without marking: a crash is not a completed
		// turn.
		focused := tabIdx == m.activeTab && m.tabs[tabIdx].ActivePane == paneID
		if !focused {
			pane.unseen = true
		}
	}
}

// coalescedCount extracts the ingester's burst count from an event's Data
// ("coalesced" = total events merged into this one), defaulting to 1 for a
// plain uncoalesced event, absent data, or a malformed value.
func coalescedCount(data map[string]string) int {
	n, err := strconv.Atoi(data["coalesced"])
	if err != nil || n < 1 {
		return 1
	}
	return n
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

// tabPinnedAttention reports whether the tab at idx contains a pane with a
// manually pinned attention mark. Unlike tabUnseen, the ACTIVE tab also
// reports true — a pin is an explicit "don't let me forget", not a
// seen/unseen state — except when the pinned pane is the focused pane of
// the active tab (the user is looking straight at it).
func (m Model) tabPinnedAttention(idx int) bool {
	if idx < 0 || idx >= len(m.tabs) || m.tabs[idx].Root == nil {
		return false
	}
	for _, p := range m.tabs[idx].Leaves() {
		if p == nil || !p.pinnedAttention {
			continue
		}
		if idx == m.activeTab && p.ID == m.tabs[idx].ActivePane {
			continue
		}
		return true
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
// wideCanvas is passed explicitly (resolved by the caller via
// Model.pluginWideCanvas) so EVERY reconciliation path re-evaluates it
// against the live plugin registry — a plugin migration mid-session
// reloads the registry, and a flag captured only at pane creation would
// stay stale forever (2026-07-05 dev-smoke bug: panes stayed rect-sized
// after the wide_canvas migration because only the cold-attach path set
// the flag).
//
// Muting a pane does NOT clear `working` here: the daemon still delivers
// work-state hook events (start/stop/abort) live for a muted pane — it only
// suppresses the visible notification card (see emitEvent) — so the normal
// completion edge keeps `working` accurate across the whole mute/unmute
// window instead of going stale the instant the pane is muted.
func syncPaneMeta(pane *PaneModel, info *PaneInfo, wideCanvas bool, minNativeCols int) {
	pane.Name = info.Name
	pane.CWD = info.CWD
	pane.Type = info.Type
	pane.WideCanvas = wideCanvas
	pane.MinNativeCols = minNativeCols
	pane.Muted = info.Muted
	pane.Eager = info.Eager
	pane.Pending = info.Pending
	pane.SessionID = info.SessionID
	pane.HistoryLines = info.HistoryLines
	pane.daemonMouseTracking = info.MouseTracking
	pane.daemonMouseSGR = info.MouseSGR
	// Unconditional copy, like the other daemon-authoritative fields: the
	// daemon writes LastModel BEFORE broadcasting the hook event and IPC
	// delivery is ordered per connection, so a snapshot can never lag behind
	// a live paneEventMsg value — and an empty snapshot value is meaningful
	// (pane restart cleared the daemon-side state; the status bar must not
	// keep showing the pre-restart model until the next turn).
	pane.Model = info.Model
	pane.ContextTokens = info.ContextTokens
}
