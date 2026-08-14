package tui

import (
	"log"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/hookevents"
	"github.com/artyomsv/quil/internal/ipc"
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
	workStop          = hookevents.WorkEventStop          // turn completed → mark pane unseen
	workAbort         = hookevents.WorkEventAbort         // process exited → clear working, no mark
	workSubagentStart = hookevents.WorkEventSubagentStart // subagent spawned → spinner on
	workSubagentStop  = hookevents.WorkEventSubagentStop  // subagent finished → spinner off once drained AND turn over
	workStopFinal     = hookevents.WorkEventStopFinal     // terminal stop → also clears the outstanding count
	workPark          = hookevents.WorkEventPark          // agent blocked on the user, UNAMBIGUOUSLY (PermissionRequest/permission.ask) → stamps blockedSince/blockedReason; does NOT clear turnActive (see the workPark case)
	workNotify        = hookevents.WorkEventNotify        // agent blocked on the user, AMBIGUOUSLY (Notification: permission prompt mid-turn OR idle nudge after Stop) → stamps blockedSince only while turnActive is true (see the workNotify case)
)

// workEventKind maps a PaneEvent Type (the daemon encodes hook events as
// "hook.<src>.<event>") to a working-state transition.
func workEventKind(eventType string) workTransition {
	return hookevents.ClassifyWorkEvent(eventType)
}

// findPaneAndTab locates a pane by ID across EVERY project and reports the
// owning project and the tab's index within it.
//
// Cross-project is a correctness requirement, not a convenience: agents in
// background projects keep firing hook events, and scoping this to the
// active project would leave a blocked background agent invisible. Returns
// (nil, nil, -1) if not found.
func (m *Model) findPaneAndTab(paneID string) (*PaneModel, *ProjectModel, int) {
	for _, proj := range m.projects {
		for i, tab := range proj.tabs {
			if tab.Root == nil {
				continue
			}
			if leaf := tab.Root.FindLeaf(paneID); leaf != nil {
				return leaf.Pane, proj, i
			}
		}
	}
	return nil, nil, -1
}

// jumpToPane moves the active project, active tab, and focused pane to reach
// paneID anywhere in the workspace. Returns false when the pane no longer
// exists, so callers can skip recording navigation history for a jump that
// did not happen. Shared by every navigation path that needs to cross a
// project boundary (MCP set_active_pane, sidebar notification navigate,
// pane-history back-navigation, the command palette's goToPane) — one
// implementation instead of four hand-rolled copies of the same sequence.
//
// The returned tea.Cmd reports overlay visibility for both the tab being
// left and the tab being entered, exactly like switchTab — see
// overlayTruthTransitionCmd. It is built AFTER m.activeProject moves, which is
// what makes the answer right for a CROSS-PROJECT jump: overlayOnScreen asks
// about the active project, so a `from` tab left behind in another project
// reports hidden rather than re-stamping its OverlayShownAt.
// This is the same active-tab-changing choke
// point switchTab is, and was missing this report for a while: a tab left
// via this path kept an overlay marked visible forever (never swept), and a
// tab entered here could still be stamped hidden from an earlier hide,
// leaving its overlay one sweep away from being destroyed while on screen.
func (m *Model) jumpToPane(paneID string) (bool, tea.Cmd) {
	pane, proj, tabIdx := m.findPaneAndTab(paneID)
	if pane == nil {
		return false, nil
	}
	// Notes mode is torn down BEFORE the tab moves, which is the contract
	// exitNotesModeInPlace states: it reverts focus mode on the tab that is
	// active when it runs, so calling it afterwards reverts the wrong one.
	//
	// This is the choke point for every cross-tab jump that is not switchTab —
	// MCP set_active_pane, the notification sidebar, pane-history back, the
	// palette and the attention queue all arrive here. They each moved the
	// active tab with the editor still open, bound to a pane in the tab being
	// left, still claiming its share of the width. notesKeyExempt does not
	// cover it: that branch only runs while the EDITOR has focus, and notes
	// open beside a working agent normally has the PANE focused.
	if m.notesMode {
		m.exitNotesModeInPlace()
	}
	// Captured before any mutation below: this is the tab actually on screen
	// right now, whose overlay (if any) is about to go off screen.
	from := m.activeTabModel()
	for i, p := range m.projects {
		if p == proj {
			// Mirrors switchProject's own guard: the sidebar scroll offset is
			// a property of "what am I looking at", so a jump that crosses a
			// project boundary starts the incoming project's PANES body at
			// the top — but a same-project jump (e.g. pane-history back
			// within the project the user is already viewing) must not
			// throw away where they had scrolled to for no reason tied to
			// the jump itself.
			if i != m.activeProject {
				m.sidebarScroll = 0
			}
			m.activeProject = i
			break
		}
	}
	proj.activeTab = tabIdx
	target := proj.tabs[tabIdx]
	target.ActivePane = paneID
	// The Active FLAG is set here, not left to the caller. ActivePane alone
	// routes keystrokes, but Active is what draws the pane's cursor
	// (renderPane) and its focused border — so a pane raised without it looks
	// dead: no cursor anywhere, the pane the user left still wearing the active
	// border. Reported against the desktop toast ("that pane is shown, but
	// focus is not on that pane so I cannot start input"), and every other
	// caller of this choke point had it too — MCP set_active_pane, the
	// notification sidebar and pane-history back. Only the palette's goToPane
	// escaped, by doing the flag dance itself around the call.
	//
	// ActivePaneModel() cannot cover this: it heals a stale ID, never a stale
	// flag, which is the same note ctxmenu.go carries. The outgoing tab is
	// cleared through treeActivePaneModel (no side effects — it must not adopt
	// a leaf in a tab being left), and finalizeTabPanes then sets the flag on
	// the arriving pane and clears every sibling, which also covers a jump
	// WITHIN one tab where `from` and `target` are the same.
	if from != nil && from != target {
		if old := from.treeActivePaneModel(); old != nil {
			old.Active = false
		}
	}
	m.finalizeTabPanes(target)
	// The jump may have crossed a project — and therefore a daemon — boundary,
	// so every later unstamped send has a new right answer.
	m.syncActiveDest()
	m.notifyTabSwitch(target)
	// Runs AFTER the project-boundary reset above: that reset zeroes
	// sidebarScroll outright, so computing the target offset before it would
	// have this call's work thrown away on every cross-project jump.
	m.scrollSidebarToPane(paneID)
	return true, m.overlayTruthTransitionCmd(from, target)
}

// notifyTabSwitch tells a tab's OWNING daemon that this tab is now active.
//
// Not bookkeeping: the daemon spawns a tab's lazily-restored panes on the
// switch, so an activeTab write that skips it lands the user on panes that are
// still Pending — the restore indicator with no process behind it, which no
// resize can rescue because there is no PTY to resize. It also keeps
// Project.ActiveTab in step, which is what the next restore warms up.
//
// Sent synchronously rather than as a tea.Cmd, like switchProject's own
// MsgSwitchProject: the dest is resolved here, on the Update goroutine, rather
// than inside a closure racing a workspace rebuild of m.projects.
func (m *Model) notifyTabSwitch(tab *TabModel) {
	// The nil client is the ~46 Models tests build directly, and the window
	// before a connection exists; sendForDest dereferences it unconditionally.
	if tab == nil || m.client == nil {
		return
	}
	msg, err := ipc.NewMessage(ipc.MsgSwitchTab, ipc.SwitchTabPayload{TabID: tab.ID})
	if err != nil {
		log.Printf("switch tab %s: encode: %v", tab.ID, err)
		return
	}
	if err := m.sendForDest(tab.Dest, msg); err != nil {
		log.Printf("switch tab %s: send: %v", tab.ID, err)
	}
}

// applyWorkTransition updates the working state of the pane identified by
// paneID based on the event type. On a normal completion, any pane that is not
// the focused pane of the active tab gets a persistent unseen mark — green
// border + derived green tab label — cleared when the user focuses the pane
// (ackFocusedPane at Update entry). There is no timer.
//
// A PARK is deliberately not one of those completions. workPark keeps
// turnActive, so `working` does not fall and the falling edge below never
// fires — no unseen mark is set for a parked pane, whether it is focused or
// not. tabBlocked (read by tabStyle, model.go) is what carries a parked
// BACKGROUND pane to the tab bar instead, deriving from blockedSince rather
// than from unseen; the two must not be separated.
//
// workNotify is the ambiguous twin (see hookevents.WorkEventNotify): Claude's
// Notification fires for a mid-turn permission prompt AND for its own idle
// nudge once the turn is already over. Only ONE case is exempted from the
// park — the nudge the producer positively recognised (Data[DataNotifyKind])
// arriving with the turn already closed — and it changes NOTHING here: no
// blockedSince, no touch to turnActive, so `working` recomputes to the same
// value it already had and neither edge below fires. Everything else parks,
// including an unmarked Notification from an older hook binary. Parking the
// idle nudge too painted a tab amber and hid a still-working pane's ◐/⋯N
// behind ▲ while its background subagents were still draining (production
// sequence: Stop, then Notification, with 3 SubagentStops still to come).
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
// empty. Oldest-first eviction drops START edges, never the stops that follow
// them, so for the ledger it can only ever orphan a SubagentStop — and a stop
// naming no live agent is ignored below. The turn has the same exposure with
// a different owner: an evicted UserPromptSubmit leaves turnActive false under
// events that assumed it true, which is why workNotify may not rest on
// turnActive alone. It parks on an ABSENT idle mark whatever the turn state
// says, so a truncated replay can cost a spinner frame but never a permission
// prompt.
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
	// The tab index is deliberately dropped: deciding whether the user saw this
	// completion is userIsWatching's job alone, and re-deriving half of it here
	// from a local is how the two definitions drifted apart in the first place.
	pane, proj, _ := m.findPaneAndTab(paneID)
	if pane == nil {
		return
	}
	wasWorking := pane.working
	// Captured beside wasWorking for the same reason: the edge actions below
	// key off a before/after pair so they fire exactly once per transition.
	// blockedSince is written at two points in the switch and unseen at one, so
	// hooking each write would silently miss a fourth added later.
	wasBlocked := !pane.blockedSince.IsZero()
	wasUnseen := pane.unseen
	abort := false
	switch kind {
	case workStart:
		pane.turnActive = true
		pane.blockedSince = time.Time{}
		pane.blockedReason = ""
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
		// A completed turn is by definition not blocked — clears on a PLAIN
		// workStop too, not just workStopFinal. Approving a permission
		// prompt fires no hook of its own: the pane's next event is the
		// turn's Stop, so this is the only edge that un-blocks it.
		pane.blockedSince = time.Time{}
		pane.blockedReason = ""
		if kind == workStopFinal {
			// Terminal stop (session end): no subagent of the session can
			// still be alive — drop the ledger so a lost SubagentStop can't
			// wedge the spinner forever. This is also the only sound point to
			// clear an overflow, for the same reason.
			clear(pane.subagents)
			pane.subagentsOverflow = false
		}
	case workPark:
		// Blocked waiting on the user. This deliberately does NOT clear
		// turnActive. Notification covers two situations: a permission prompt
		// (turn still running) and an idle-wait nudge (Stop already cleared
		// turnActive), so clearing it here was a no-op exactly when it was
		// right and wrong exactly when it was not — approving a Bash/Edit/Write
		// prompt fires no hook of its own, so the pane read as blocked-not-
		// working until the turn's Stop, sometimes for minutes.
		//
		// Consequence: `working` no longer falls here, so the falling edge
		// below does not set `unseen`. tabBlocked (model.go tabStyle) is what
		// carries a parked background pane to the tab bar instead — the two
		// must not be separated.
		pane.blockedSince = time.Now()
		// Data["tool"] is set by the claude hook only for PermissionRequest
		// and PostToolUse. Notification and opencode's permission.ask may
		// carry no tool, so the reason is genuinely optional — render a bare
		// marker rather than inventing one. Guarded rather than assigned:
		// a second park for the SAME prompt that carries no tool must not
		// erase the name the first one gave the sidebar (▲ Bash → bare ▲).
		if tool := data["tool"]; tool != "" {
			pane.blockedReason = tool
		}
	case workNotify:
		// Notification is Claude's idle nudge as well as its permission
		// prompt. The classifier is handed the event type alone, so the
		// PRODUCER (internal/claudehook) marks the idle case it can recognise
		// from the message text, and this is where the two facts meet:
		//
		//   - not marked idle → park. An unrecognised, reworded or unmarked
		//     message (an older hook binary marks nothing) may be a permission
		//     prompt, and turnActive is a LOSSY discriminator for that — it is
		//     false for a background subagent's prompt after the main turn's
		//     Stop, after a reattach zeroed it, and after a replay truncated
		//     past its UserPromptSubmit. Wrong-on is an amber tab the next
		//     Stop clears; wrong-off is a prompt that never surfaces and no
		//     later hook recovers, since a parked agent emits nothing.
		//   - marked idle AND turnActive → park anyway. Only Stop-then-nudge
		//     is unambiguous, and keeping the gate is what makes the
		//     production sequence resolve correctly against a hook binary
		//     that predates the mark.
		//   - marked idle, turn already over → change NOTHING. Leave every
		//     field as Stop left it (unseen if nothing was outstanding, still
		//     working if subagents are). Stamping blockedSince here was the
		//     bug: it painted a tab amber and hid the ◐ ⋯N a pane with live
		//     subagents should show.
		if data[hookevents.DataNotifyKind] != hookevents.NotifyKindIdle || pane.turnActive {
			pane.blockedSince = time.Now()
			if tool := data["tool"]; tool != "" {
				pane.blockedReason = tool
			}
		}
	case workAbort:
		pane.turnActive = false
		clear(pane.subagents)
		pane.subagentsOverflow = false
		pane.blockedSince = time.Time{}
		pane.blockedReason = ""
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
		//
		// userIsWatching, not the three-part on-screen test this line used to
		// inline: a pane on screen in a terminal that is BEHIND ANOTHER WINDOW
		// was being counted as seen, which is the state a user is in every time
		// they ask an agent something and alt-tab away. See the comment there —
		// it also cost the desktop toast, which fires on this mark's rising
		// edge. Nothing changes visually for a pane marked this way: tabUnseen
		// excludes the active tab and the active pane border outranks the green
		// one, so the mark is invisible until it matters and is cleared by
		// ackFocusedPane the moment the user comes back.
		if !m.userIsWatching(paneID) {
			pane.unseen = true
		}
	}

	// Desktop toast on the RISING edge of either attention state. One call
	// site, deriving both from the pair captured at the top — see
	// raiseAttentionToast. Withdrawal is NOT here: the falling edge has no
	// choke point (the user typing an answer clears blockedSince without any
	// hook firing at all), so it is a sweep in Update instead.
	m.raiseAttentionToast(pane, proj, wasBlocked, wasUnseen)
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

// ackFocusedPane clears the unseen mark on the focused pane of the active tab,
// called once at the top of Update. Correctness does not depend on a render
// having happened between messages (the renderer coalesces frames): a focused
// pane never renders the mark anyway — tabUnseen excludes the active tab and
// the pane border gives the active style precedence — and focusing the pane is
// itself the acknowledgement. This single choke point replaces auditing every
// ActivePane/activeTab assignment (13 call sites); a newly focused pane is
// acknowledged on the next message (the 1 s size poll bounds the wait).
// Unfocused panes keep their mark until focused.
//
// `unseen` is the ONLY mark this clears, and the two it leaves alone are left
// alone for the same reason. blockedSince/blockedReason are a fact about the
// AGENT — it is still waiting whether or not anyone is looking — while `unseen`
// is a "you missed something" flag that looking genuinely answers. Clearing the
// blocked mark here was tried and reversed: this runs for EVERY message,
// including the shared 100 ms workSpinnerTickMsg that is guaranteed to be
// ticking because the pane is working, so a pane parked while it held focus had
// its mark set and dropped ~100 ms later — the ▲, the amber tab, the project
// badge's blocked count and the attention-queue entry were none of them ever
// observable, in the commonest park there is (the agent asks for permission
// while you are sitting in its pane), and with workPark keeping turnActive the
// pane went on claiming to be working the whole time it waited. paneRow
// suppresses the blocked PRESENTATION for the focused pane instead, so leaving
// the pane restores every signal with no hook edge required. pinnedAttention is
// untouched for a stronger reason than the others: it is DAEMON-owned, and
// syncPaneMeta (this file) is the only thing in the client that writes it. The
// context menu's two attention rows send MsgUpdatePane; focus is not a route to
// a clear at all.
func (m *Model) ackFocusedPane() {
	// A window the user is not looking at cannot acknowledge anything, and this
	// half was missing: with it absent the mark set by a turn that finished
	// while the terminal was in the background was cleared again on the very
	// next message — the 1 s size poll guarantees one — so the desktop toast
	// raised on that mark's rising edge was withdrawn by the sweep before the
	// user could act on it. Same shape as the blockedSince clear this function
	// used to do and no longer does: a signal removed ~one tick after it was
	// set is indistinguishable from one that was never set at all.
	if !m.termFocused {
		return
	}
	tab := m.activeTabModel()
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

// answerBlockedByInput clears a pane's parked-on-the-user mark because real
// user input just reached it. A glance is not an answer; a keystroke is.
//
// This is the other half of ackFocusedPane's rule, and it exists because
// approving a Bash/Edit/Write permission prompt fires NO hook of its own — the
// pane's next event is the turn's Stop, which can be minutes away. With focus
// deliberately not clearing the mark, an ANSWERED prompt would otherwise keep
// its tab amber, keep counting as blocked rather than working in the project
// badge, keep being offered by Alt+Shift+A, and put the ▲ back the moment the
// user switched away. Input reaching the pane is the one signal that separates
// answering the prompt from looking at it.
//
// Callers are the producers that represent a HUMAN acting on the pane: the two
// handleKey forward paths and both paste paths. Deliberately NOT enqueueInput,
// which is the ordering choke point for every producer including forwarded
// wheel notches, and NOT forwardInputBytes, which the selection handler also
// uses to walk the shell cursor during a mouse DRAG — those emit arrow-key
// escapes a permission prompt would consume as a choice. Scrolling or dragging
// across a parked pane is a glance with a mouse.
//
// It lives here rather than beside its callers so workstate.go remains the
// owner of every write to these fields but the context menu's explicit one.
func (p *PaneModel) answerBlockedByInput() {
	if p == nil {
		return
	}
	p.blockedSince = time.Time{}
	p.blockedReason = ""
}

// anyPaneWorking reports whether any pane in any tab is mid-turn.
func (m Model) anyPaneWorking() bool {
	for _, tab := range m.allTabs() {
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
	tabs := m.curTabs()
	if idx < 0 || idx >= len(tabs) || tabs[idx].Root == nil {
		return false
	}
	for _, p := range tabs[idx].Leaves() {
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
	tabs := m.curTabs()
	if idx < 0 || idx >= len(tabs) || idx == m.activeTabIdx() || tabs[idx].Root == nil {
		return false
	}
	for _, p := range tabs[idx].Leaves() {
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
	tabs := m.curTabs()
	if idx < 0 || idx >= len(tabs) || tabs[idx].Root == nil {
		return false
	}
	for _, p := range tabs[idx].Leaves() {
		if p == nil || !p.pinnedAttention {
			continue
		}
		if idx == m.activeTabIdx() && p.ID == tabs[idx].ActivePane {
			continue
		}
		return true
	}
	return false
}

// tabBlocked reports whether the tab at idx holds a pane parked on the user.
// Unlike tabUnseen the ACTIVE tab also reports true: a permission prompt is
// not a seen/unseen state, it is an outstanding question, and the tab bar is
// the only place a user scanning several projects will notice it.
//
// It stays true for the FOCUSED pane too, and that is the point rather than an
// edge: nothing clears blockedSince but the agent (or the context menu's Clear
// attention row), so a pane the user is sitting in without answering keeps
// marking its tab. paneRow suppresses the pane row's own glyph while it is
// focused — the level the user is looking straight at — and every derived
// level reads this same flag, so they cannot disagree about whether the agent
// is still waiting.
func (m Model) tabBlocked(idx int) bool {
	tabs := m.curTabs()
	if idx < 0 || idx >= len(tabs) || tabs[idx].Root == nil {
		return false
	}
	for _, p := range tabs[idx].Leaves() {
		if p != nil && !p.blockedSince.IsZero() {
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
	// Unconditional, which makes the DAEMON the sole writer of this mark. The
	// context menu used to flip the local bool in place; it sends
	// MsgUpdatePane now and lets the answer come back here, exactly as the mute
	// toggle does. A surviving local write would fight this copy on the next
	// broadcast — and broadcasts are frequent (the git ticker alone fires every
	// 5 s), so the mark would appear to set and then undo itself.
	pane.pinnedAttention = info.PinnedAttention
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
	// Git state, copied unconditionally for the same reason as Model: an
	// ABSENT key is meaningful. A pane that leaves a repository, or a daemon
	// restart that has not re-probed yet, must clear the branch rather than
	// keep showing the last one it had.
	// Unconditional, like the git fields below: an ABSENT key is meaningful.
	// A pane whose worktree comes back must lose its complaint, and the daemon
	// clears the field on every successful spawn.
	pane.SpawnError = info.SpawnError
	pane.GitBranch = info.GitBranch
	pane.GitDetached = info.GitDetached
	pane.GitWorktree = info.GitWorktree
	pane.GitWorktreeName = info.GitWorktreeName
	// Unconditional like the rest: a pane whose worktree Quil no longer owns —
	// a restore that could not confirm it, a daemon that dropped the record —
	// must stop being offered for deletion, and the absent key is what says so.
	pane.WorktreeOwned = info.WorktreeOwned
	pane.GitUpstream = info.GitUpstream
	pane.GitAhead = info.GitAhead
	pane.GitBehind = info.GitBehind
	pane.GitStale = info.GitStale
}
