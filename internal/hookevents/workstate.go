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
	WorkEventStop                       // turn completed OR parked for user input → mark pane unseen
	WorkEventAbort                      // process exited → clear working, no mark
	// Background subagents (Claude Code runs them detached by default)
	// outlive the main turn's Stop, so they carry their own edges: the TUI
	// keeps an outstanding count per pane and only lets a Stop end the
	// spinner once that count is drained. A permanently lost SubagentStop
	// (e.g. dropped during an ingester rate-limit storm) keeps the spinner
	// lit until a terminal edge — recovery is deliberately deferred to
	// WorkEventStopFinal / process-exit rather than an age-based drain,
	// because there is no signal that distinguishes a long-running subagent
	// from a lost stop.
	WorkEventSubagentStart // a subagent spawned → spinner on
	WorkEventSubagentStop  // a subagent finished → spinner off once drained AND turn over
	WorkEventStopFinal     // terminal stop (session end) → also clears the outstanding count
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
	// prompt, option select, idle-input nudge). There is no "resumed after
	// approval" hook, so we treat the park as a turn boundary — stop the spinner
	// and mark the pane unseen to pull attention. Both Claude (Notification fires
	// for permission + idle-wait; PermissionRequest when available) and opencode
	// (permission.ask) are covered.
	case "hook.claude.Notification", "hook.claude.PermissionRequest",
		"hook.opencode.permission.ask":
		return WorkEventStop
	case "process_exit":
		return WorkEventAbort
	}
	return WorkEventNone
}
