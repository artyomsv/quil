package hookevents

// WorkEventKind classifies how a composed PaneEvent Type (the daemon encodes
// hook events as "hook.<src>.<event>") affects a pane's mid-turn "working"
// state. Shared by the daemon — which must keep delivering these specific
// event types to live TUI clients even for a muted pane, so the spinner
// doesn't go stale for the duration of the mute and never resync on unmute —
// and the TUI, which drives the work-in-progress spinner from them. This is
// the single source of truth for the work indicator — keep it in sync with
// the producers in internal/claudehook and internal/opencodehook.
type WorkEventKind int

const (
	WorkEventNone  WorkEventKind = iota // no effect
	WorkEventStart                      // a turn began
	WorkEventStop                       // turn completed → mark pane unseen
	WorkEventAbort                      // process exited → clear working, no mark
	// Background subagents (Claude Code runs them detached by default)
	// outlive the main turn's Stop, so they carry their own edges: the TUI
	// keeps a per-pane ledger of outstanding agents KEYED BY agent_type and
	// only lets a Stop end the spinner once that ledger is empty. The key
	// is what makes a stop cancel only the start it names — Claude Code
	// emits an unpaired SubagentStop with an EMPTY agent_type at the end of
	// every main turn (the root turn's own completion), and a fungible
	// count spends it on a live background agent instead. A permanently
	// lost SubagentStop (e.g. dropped during an ingester rate-limit storm)
	// keeps the spinner lit until a terminal edge — recovery is
	// deliberately deferred to WorkEventStopFinal / process-exit rather
	// than an age-based drain, because there is no signal that
	// distinguishes a long-running subagent from a lost stop.
	WorkEventSubagentStart // a subagent spawned → spinner on
	WorkEventSubagentStop  // a subagent finished → spinner off once drained AND turn over
	WorkEventStopFinal     // terminal stop (session end) → also clears the outstanding count
	// WorkEventPark: the agent is blocked waiting on the USER, UNAMBIGUOUSLY —
	// a permission prompt (hook.claude.PermissionRequest) or opencode's
	// permission.ask. Distinct from WorkEventStop, which means the turn
	// finished. Unlike Stop, this does NOT clear the spinner: the consumer
	// (internal/tui/workstate.go) sets blockedSince without touching
	// turnActive, because a permission prompt arrives mid-turn and approving
	// it fires no hook of its own. Park means "this needs you", which is what
	// the sidebar's ▲ renders.
	WorkEventPark
	// WorkEventNotify: the agent MAY be blocked waiting on the user. Claude
	// reuses hook.claude.Notification for a permission prompt (mid-turn,
	// while turnActive is still true — behaviourally identical to
	// WorkEventPark) AND for its own idle nudge, "Claude is waiting for your
	// input" (fired AFTER the turn's own Stop already cleared turnActive,
	// often while background subagents are still draining). Collapsing both
	// into WorkEventPark painted a tab's amber "blocked on you" marker while
	// its agent was demonstrably still working (production sequence: Stop,
	// then Notification, with 3 SubagentStops still to come).
	//
	// ClassifyWorkEvent cannot tell them apart — it is handed the event TYPE
	// and nothing else — so the split is resolved by the two ends instead:
	// the PRODUCER recognises the idle nudge from the hook's message text and
	// says so in Data[DataNotifyKind], and the consumer parks unless it was
	// told this is the idle case AND the turn is already over. See
	// DataNotifyKind for why the mark is positive rather than negative.
	WorkEventNotify
)

// DataNotifyKind is the Payload Data key a Notification producer uses to say
// WHICH of Claude's two notifications a spool line carries, and NotifyKindIdle
// is the only value it ever holds: the idle "waiting for your input" nudge.
// Producer: internal/claudehook (notifyKindData). Consumer:
// internal/tui/workstate.go's workNotify case.
//
// The mark is POSITIVE — "this one is the idle nudge" — and its absence means
// "park", deliberately. Recognising upstream English prose is fragile, so the
// direction of the match decides which way a reworded, unknown, or unmarked
// message fails: toward the amber tab the next Stop clears (visible, and what
// shipped for months), never toward a permission prompt that never surfaces at
// all. It also makes an OLD hook binary beside a new TUI safe by construction —
// it marks nothing, so every Notification parks, exactly as it used to.
const (
	DataNotifyKind = "notify_kind"
	NotifyKindIdle = "idle"
)

// IsWorkStateOnly reports whether an event exists PURELY to drive the work
// indicator and must never be treated as a notification — neither queued by the
// daemon nor carded by the TUI sidebar. It lives here, beside
// ClassifyWorkEvent, so the two consumers share one list: the TUI held a
// private copy first, and the daemon could not see it, which is precisely how
// the heartbeat ended up in the notification queue.
//
// Being a work-state edge is NOT the test. Most of them — Stop, StopFailure,
// PermissionRequest, a named SubagentStop — are exactly what the sidebar and
// the attach replay exist for. The test is whether the event says anything a
// user can act on, and these two say only "still running": PostToolUse fires
// when the user answers a prompt they are already looking at, and PreToolUse is
// a heartbeat that repeats for as long as an agent keeps working.
//
// The consequence for the DAEMON is why this matters beyond tidiness. The event
// queue is bounded and aggregates by (PaneID, Title) before re-prepending the
// entry, so a heartbeat carrying a constant title holds one slot per working
// pane and jumps ahead of every older event each time it fires — displacing
// genuine notifications out of the attach-replay window, and waking every
// watch_notifications watcher on that pane. Callers must still BROADCAST these
// events: suppressing the queue leaves the live broadcast as the only route by
// which a client learns the pane is working.
func IsWorkStateOnly(eventType string) bool {
	switch eventType {
	case "hook.claude.PostToolUse", "hook.claude.PreToolUse", "hook.codex.PreToolUse":
		return true
	}
	return false
}

// IsWorkHeartbeat reports whether a start edge came from the agent carrying on
// by itself rather than from a human acting on the pane.
//
// The distinction exists because the consumer's rising edge clears the pane's
// unseen mark, and that was only ever sound while every start implied a human:
// UserPromptSubmit is a typed prompt and PostToolUse is matched to a prompt the
// user has just answered, so in both cases the person had demonstrably just
// looked at the pane. PreToolUse carries no such implication — nobody
// acknowledged anything, the agent simply resumed — so clearing there lets an
// agent delete the completion mark, and withdraw the desktop toast raised with
// it, for a turn the user never saw.
//
// It is deliberately NOT the same predicate as IsWorkStateOnly, which also
// holds for PostToolUse: that one asks "should this become a card", this one
// asks "did a human cause this". Collapsing them would re-introduce the bug for
// the answered-prompt case, where clearing the mark is exactly right.
func IsWorkHeartbeat(eventType string) bool {
	return eventType == "hook.claude.PreToolUse" || eventType == "hook.codex.PreToolUse"
}

// ClassifyWorkEvent maps a composed PaneEvent Type to a work-state transition.
//
// Codex emits Claude's event NAMES (its hook system is Claude-compatible; see
// internal/codexhook), so each hook.codex.* case sits beside its Claude twin.
// The events codex never emits — Notification, StopFailure, the Task pair —
// simply have no codex arm.
func ClassifyWorkEvent(eventType string) WorkEventKind {
	switch eventType {
	case "hook.claude.UserPromptSubmit", "hook.opencode.chat.message", "hook.codex.UserPromptSubmit":
		return WorkEventStart
	// Resume edge: the user answered an interactive-prompt tool (AskUserQuestion
	// / ExitPlanMode) and the agent is working again. The hook registers
	// PostToolUse only for those tools, so this re-arms the spinner after a park
	// without tracking ordinary tool completions.
	case "hook.claude.PostToolUse":
		return WorkEventStart
	// The only start edge that does not assume a HUMAN began the turn, and the
	// reason it had to exist: UserPromptSubmit is a typed prompt and the
	// PostToolUse above is matched to the prompt tools a user has just
	// answered, so a turn Claude starts by itself has neither. When a teammate
	// reports back, its result arrives as a user-ROLE transcript entry and the
	// agent resumes — measured on one orchestrator pane as 3 Stops against 1
	// UserPromptSubmit, with a 14m41s stretch of ~60 tool calls showing no
	// indicator at all. A tool call is proof of work whatever started it.
	//
	// The producer throttles these to roughly one per quiet interval
	// (claudehook.spoolIsFresh), so the ledger sees a heartbeat rather than a
	// per-tool-call stream. Dropping one is free — it is a level, not an edge:
	// any later tool call in the same turn re-arms the identical state.
	case "hook.claude.PreToolUse", "hook.codex.PreToolUse":
		return WorkEventStart
	case "hook.claude.Stop", "hook.codex.Stop",
		"hook.opencode.session.idle", "hook.opencode.session.error":
		return WorkEventStop
	// A turn killed by an API error ends with StopFailure and NEVER a Stop, so
	// leaving it unmapped left turnActive true with only SessionEnd or
	// process_exit able to clear it — a spinner claiming work that had already
	// stopped, for as long as the pane stayed open. Same missing-edge class as
	// the PreToolUse case below, opposite direction: that one loses the
	// indicator, this one strands it.
	//
	// Plain WorkEventStop, not StopFinal: an API error ends the TURN, and says
	// nothing about background subagents, which are separate processes the
	// ledger tracks on their own edges. Clearing the ledger here would drop
	// agents that are still running.
	case "hook.claude.StopFailure":
		return WorkEventStop
	// SessionEnd is terminal for the whole Claude session (/clear, /logout,
	// exit): no subagent of it can still be running, so the TUI also drops
	// any outstanding-subagent count instead of letting a lost SubagentStop
	// wedge the spinner forever.
	case "hook.claude.SessionEnd", "hook.codex.SessionEnd":
		return WorkEventStopFinal
	// Background subagents outlive the main turn's Stop (Claude Code runs
	// them detached by default), so they carry their own start/stop edges.
	// TaskCreated/TaskCompleted stay unmapped on purpose: the task list is
	// bookkeeping, not an execution signal.
	case "hook.claude.SubagentStart", "hook.codex.SubagentStart":
		return WorkEventSubagentStart
	case "hook.claude.SubagentStop", "hook.codex.SubagentStop":
		return WorkEventSubagentStop
	// Park-for-input edges: the agent is blocked waiting on the user (permission
	// prompt, option select, idle-input nudge). internal/tui/workstate.go's
	// workPark case sets blockedSince WITHOUT clearing turnActive, so a
	// permission prompt that arrives mid-turn keeps the spinner running across
	// it (approving a Bash/Edit/Write prompt fires no hook of its own, so the
	// pane used to read as blocked-not-working until the eventual Stop).
	// PermissionRequest (Claude, when available) and permission.ask (opencode)
	// are unambiguous — always a real block — and stay WorkEventPark.
	//
	// hook.claude.Notification is NOT unambiguous: Claude reuses it for both
	// that mid-turn permission prompt AND an idle-wait nudge fired once the
	// turn is already over. THIS function cannot tell them apart — it is
	// handed the event type and nothing else, neither the hook's message text
	// (which the producer reads) nor turnActive (which the consumer holds).
	// So it reports the ambiguity as its own kind, WorkEventNotify, and the
	// two ends resolve it: the producer marks the idle nudge in
	// Data[DataNotifyKind], the consumer parks whenever that mark is absent.
	// Tagged distinctly from WorkEventStop either way, so the
	// sidebar can tell "blocked on you" apart from "turn finished" — a
	// genuine park no longer sets unseen; tabBlocked + blockedTabStyle carry
	// it to the tab bar instead.
	case "hook.claude.PermissionRequest", "hook.opencode.permission.ask", "hook.codex.PermissionRequest":
		return WorkEventPark
	case "hook.claude.Notification":
		return WorkEventNotify
	// The user pressed ESC, which is Claude's interrupt. This is the ONLY
	// turn-ending edge with no upstream event behind it: measured against
	// Claude Code 2.1.233 by interrupting a streaming response on a real pane,
	// an ESC spools nothing at all — not Stop, not StopFailure, not
	// Notification — so `turnActive` stayed true until SessionEnd and a
	// stopped pane went on reporting work indefinitely (observed: 43 minutes).
	// The TUI synthesises it from the keystroke instead (see
	// tui.userInterruptEvent); it is not a hook type and never crosses IPC.
	//
	// A plain Stop, so it ends the TURN and leaves the subagent ledger alone.
	case "internal.user_interrupt":
		return WorkEventStop
	case "process_exit":
		return WorkEventAbort
	}
	return WorkEventNone
}
