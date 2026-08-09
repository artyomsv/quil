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
	// WorkEventNotify: the agent MAY be blocked waiting on the user — the
	// classifier cannot tell, because Claude reuses hook.claude.Notification
	// for two different situations and only the consumer's turn state
	// distinguishes them. It fires for a permission prompt (mid-turn, while
	// turnActive is still true — behaviourally identical to WorkEventPark)
	// AND for Claude's own idle nudge, "Claude is waiting for your input"
	// (fired AFTER the turn's own Stop already cleared turnActive, often
	// while background subagents are still draining). Collapsing both into
	// WorkEventPark painted a tab's amber "blocked on you" marker while its
	// agent was demonstrably still working (production sequence: Stop, then
	// Notification, with 3 SubagentStops still to come). The consumer gates
	// on turnActive: true behaves exactly like WorkEventPark; false leaves
	// the pane's state exactly as Stop left it.
	WorkEventNotify
)

// ClassifyWorkEvent maps a composed PaneEvent Type to a work-state transition.
func ClassifyWorkEvent(eventType string) WorkEventKind {
	switch eventType {
	case "hook.claude.UserPromptSubmit", "hook.opencode.chat.message":
		return WorkEventStart
	// Resume edge: the user answered an interactive-prompt tool (AskUserQuestion
	// / ExitPlanMode) and the agent is working again. The hook registers
	// PostToolUse only for those tools, so this re-arms the spinner after a park
	// without tracking ordinary tool completions.
	case "hook.claude.PostToolUse":
		return WorkEventStart
	case "hook.claude.Stop",
		"hook.opencode.session.idle", "hook.opencode.session.error":
		return WorkEventStop
	// SessionEnd is terminal for the whole Claude session (/clear, /logout,
	// exit): no subagent of it can still be running, so the TUI also drops
	// any outstanding-subagent count instead of letting a lost SubagentStop
	// wedge the spinner forever.
	case "hook.claude.SessionEnd":
		return WorkEventStopFinal
	// Background subagents outlive the main turn's Stop (Claude Code runs
	// them detached by default), so they carry their own start/stop edges.
	// TaskCreated/TaskCompleted stay unmapped on purpose: the task list is
	// bookkeeping, not an execution signal.
	case "hook.claude.SubagentStart":
		return WorkEventSubagentStart
	case "hook.claude.SubagentStop":
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
	// turn is already over, and nothing in the payload tells them apart — only
	// turnActive does, which is TUI-side state the classifier here does not
	// have. It gets its own kind, WorkEventNotify, so the consumer can make
	// that call instead of the classifier guessing wrong in one direction or
	// the other. Tagged distinctly from WorkEventStop either way, so the
	// sidebar can tell "blocked on you" apart from "turn finished" — a
	// genuine park no longer sets unseen; tabBlocked + blockedTabStyle carry
	// it to the tab bar instead.
	case "hook.claude.PermissionRequest", "hook.opencode.permission.ask":
		return WorkEventPark
	case "hook.claude.Notification":
		return WorkEventNotify
	case "process_exit":
		return WorkEventAbort
	}
	return WorkEventNone
}
