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

// maxTrackedSubagents caps the DISTINCT agent_type keys one pane's ledger may
// hold. The ledger is keyed by a producer-controlled string in a TUI process
// that runs for weeks, so its cardinality needs a ceiling that does not depend
// on the child behaving; the ceiling sits far above any real fan-out (observed
// sessions peak in the low tens of distinct agent names, and entries are
// deleted as they drain) so a healthy pane never reaches it.
const maxTrackedSubagents = 64

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
// turnActive || len(subagents) > 0 || subagentsOverflow — never assigned by
// hand in a branch, so no future edge can desync the spinner. The main turn
// (turnActive) and the outstanding background subagents (subagents) are
// tracked separately: Claude Code runs subagents detached by default, so the
// main turn's Stop routinely fires while they are still grinding — the
// spinner must survive that edge and the unseen mark is deferred until the
// LAST subagent drains (which then becomes the completion edge).
//
// The ledger is keyed by agent_type rather than being a bare count, because
// a SubagentStop must only cancel a SubagentStart it can be MATCHED to.
// Claude Code emits one unpaired stop with an empty agent_type at the end of
// every main turn; with fungible stops that phantom drains an unrelated live
// agent and the spinner dies mid-work. See the workSubagentStop branch.
//
// data carries the ingester's coalesced burst count: N events with the same
// (pane, hook_event, agent_type) inside the 50 ms debounce window arrive as
// ONE PaneEvent with data["coalesced"] = "N". agent_type is part of that key
// (internal/hookevents/ingest.go) precisely so this ledger can rely on the
// identity surviving coalescing.
//
// Replay safety: the daemon replays the queued event history on attach, and
// the ordered replay reconstructs the live state PROVIDED the ledger starts
// empty. The ring's oldest-first eviction can only ever orphan a
// SubagentStop (never strand a start behind its stop), and a stop naming no
// live agent is ignored below.
//
// That zero-start premise used to be free: attach happened exactly once per
// TUI process, guarded by Model.attached. Remote reconnect (RD-011) broke
// that — a restored link attaches again into a Model whose counters already
// reflect every event about to be replayed, and this function has no dedup,
// so a replayed SubagentStart stacks on top of the one it is repeating and
// wedges the spinner until SessionEnd. resetWorkStateForReattach
// (reconnect.go) re-establishes the premise before each reattach. Any future
// path that attaches a second time owes the same reset.
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
		agentType := data["agent_type"]
		if agentType == "" {
			// A start must NAME the agent it starts, and every observed one
			// does. Enforcing it is what turns "the empty key is never live"
			// from a measurement into an invariant: the empty key is exactly
			// the one the unpaired end-of-turn stop carries, so admitting it
			// here would let that phantom drain real work again — silently,
			// if the producer ever renames or drops the field.
			break
		}
		_, live := pane.subagents[agentType]
		if !live && len(pane.subagents) >= maxTrackedSubagents {
			// agent_type is producer-controlled, so key cardinality is too,
			// in a process that runs for weeks. Past the ceiling we stop
			// name-tracking, which makes that agent invisible to the ledger —
			// so record THAT rather than dropping it silently. Without this
			// flag, draining the tracked agents would take len() to zero and
			// turn the spinner off while a refused agent was still running:
			// the exact bug this ledger exists to prevent, reappearing at the
			// cap boundary.
			//
			// Sticky until a terminal edge, deliberately. We never learn that
			// an untracked agent finished (its stop names a key we do not
			// hold), so there is no sound moment to clear it early; SessionEnd
			// and process_exit are the only points where nothing can still be
			// live. Wrong-on is the safe direction — a spinner that lingers on
			// a pathological pane costs a glyph, wrong-off costs the user the
			// one cue that work is happening.
			pane.subagentsOverflow = true
			break
		}
		if pane.subagents == nil {
			pane.subagents = make(map[string]int, 1)
		}
		pane.subagents[agentType] += coalescedCount(data)
	case workSubagentStop:
		agentType := data["agent_type"]
		outstanding, live := pane.subagents[agentType]
		if !live {
			// The stop names no agent this pane has running, so there is
			// nothing for it to cancel. Two ways to get here, both real:
			//
			//   - Claude Code emits ONE unpaired SubagentStop carrying an
			//     EMPTY agent_type at the end of every main turn (measured
			//     1:1 against Stop on every AI pane; a SubagentStart with an
			//     empty agent_type never occurs). It is the root turn's own
			//     completion — its start edge is UserPromptSubmit, not a
			//     SubagentStart — so it can never have a partner here.
			//   - A replay truncated by ring eviction, or a lost start.
			//
			// Ignoring it is what makes the ledger self-correcting. A bare
			// counter cannot: stops are fungible there, so the phantom is
			// spent on whichever background agent happens to be outstanding
			// and the spinner goes dark while that agent is still working
			// (2026-08-02: a 27-minute agent ran with no indicator). The
			// old zero-guard could not catch it either — it only fires when
			// the count is already zero, which is precisely when no agent is
			// at risk.
			//
			// break, not return: the derivation below is the single point
			// that owns `working`, and leaving the function around it would
			// make that property depend on this branch never mattering.
			// Recomputing an unchanged state is free and fires no edge.
			break
		}
		outstanding -= coalescedCount(data)
		if outstanding <= 0 {
			delete(pane.subagents, agentType)
		} else {
			pane.subagents[agentType] = outstanding
		}
	case workStop, workStopFinal:
		pane.turnActive = false
		if kind == workStopFinal {
			// Terminal stop (session end): no subagent of the session can
			// still be alive — drop the ledger so a lost SubagentStop can't
			// wedge the spinner forever. This is also the only sound point to
			// clear an overflow, for the same reason.
			clear(pane.subagents)
			pane.subagentsOverflow = false
		}
	case workAbort:
		pane.turnActive = false
		clear(pane.subagents)
		pane.subagentsOverflow = false
		abort = true
	}

	// Single derivation point for the spinner; the edge actions below key
	// off the before/after pair so they fire exactly once per transition.
	pane.working = pane.turnActive || len(pane.subagents) > 0 || pane.subagentsOverflow
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
	pane.daemonBracketedPaste = info.BracketedPaste
	// Unconditional copy, like the other daemon-authoritative fields: the
	// daemon writes LastModel BEFORE broadcasting the hook event and IPC
	// delivery is ordered per connection, so a snapshot can never lag behind
	// a live paneEventMsg value — and an empty snapshot value is meaningful
	// (pane restart cleared the daemon-side state; the status bar must not
	// keep showing the pre-restart model until the next turn).
	pane.Model = info.Model
	pane.ContextTokens = info.ContextTokens
}
