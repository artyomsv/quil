package hookevents

import "testing"

// TestClassifyWorkEvent covers the classifier in its own package: it is the
// single source of truth shared by the daemon's mute-bypass gate
// (daemon.emitEvent) and the TUI's spinner (tui.workEventKind delegates
// here), so it must not rely on a cross-package caller for coverage.
func TestClassifyWorkEvent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		eventType string
		want      WorkEventKind
	}{
		{"hook.claude.UserPromptSubmit", WorkEventStart},
		{"hook.opencode.chat.message", WorkEventStart},
		{"hook.claude.PostToolUse", WorkEventStart},
		// A tool call is proof the agent is working, whatever started the
		// turn. It is the ONLY start edge that does not assume a human began
		// it — see the PreToolUse case in ClassifyWorkEvent.
		{"hook.claude.PreToolUse", WorkEventStart},
		{"hook.claude.Stop", WorkEventStop},
		// A turn killed by an API error ends without a Stop. Unmapped, it left
		// turnActive true with nothing but SessionEnd/process_exit to clear it.
		{"hook.claude.StopFailure", WorkEventStop},
		{"hook.claude.SessionEnd", WorkEventStopFinal},
		{"hook.opencode.session.idle", WorkEventStop},
		{"hook.opencode.session.error", WorkEventStop},
		{"hook.claude.Notification", WorkEventNotify},
		{"hook.claude.PermissionRequest", WorkEventPark},
		{"hook.opencode.permission.ask", WorkEventPark},
		{"hook.claude.SubagentStart", WorkEventSubagentStart},
		{"hook.claude.SubagentStop", WorkEventSubagentStop},
		{"process_exit", WorkEventAbort},
		// Synthesised by the TUI from an ESC keypress: the one turn ending
		// upstream emits no event for at all.
		{"internal.user_interrupt", WorkEventStop},
		// Deliberately unmapped: task-list bookkeeping, not execution.
		{"hook.claude.TaskCreated", WorkEventNone},
		{"hook.claude.TaskCompleted", WorkEventNone},
		// Non-hook pane events must never touch the work state.
		{"output_idle", WorkEventNone},
		{"bell", WorkEventNone},
		{"", WorkEventNone},
	}
	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			t.Parallel()
			if got := ClassifyWorkEvent(tt.eventType); got != tt.want {
				t.Errorf("ClassifyWorkEvent(%q) = %v, want %v", tt.eventType, got, tt.want)
			}
		})
	}
}

// TestParkEventsAreDistinctFromStop guards the split that gives "blocked on
// the user" its own WorkEventKind: a permission prompt or idle-wait must not
// collapse into the same value as a turn actually completing, because that
// is the distinction the sidebar's ⚠ marker depends on.
func TestParkEventsAreDistinctFromStop(t *testing.T) {
	t.Parallel()
	for _, evt := range []string{
		"hook.claude.PermissionRequest",
		"hook.opencode.permission.ask",
	} {
		if got := ClassifyWorkEvent(evt); got != WorkEventPark {
			t.Errorf("ClassifyWorkEvent(%q) = %v, want WorkEventPark", evt, got)
		}
	}
	if got := ClassifyWorkEvent("hook.claude.Stop"); got != WorkEventStop {
		t.Errorf("a turn completing is Stop, not Park: got %v", got)
	}

	// Notification is ambiguous — Claude fires it for both a permission
	// prompt (mid-turn) and an idle nudge (after Stop) — so it gets its own
	// kind rather than collapsing into the unambiguous Park signal. This
	// classifier only records that the signal is distinct from both Park and
	// Stop; the producer marks the idle nudge it recognises (DataNotifyKind)
	// and tui.applyWorkTransition parks everything else.
	if got := ClassifyWorkEvent("hook.claude.Notification"); got != WorkEventNotify {
		t.Errorf("Notification must classify as WorkEventNotify, got %v", got)
	}
}
