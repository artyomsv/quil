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
		{"hook.claude.Stop", WorkEventStop},
		{"hook.claude.SessionEnd", WorkEventStopFinal},
		{"hook.opencode.session.idle", WorkEventStop},
		{"hook.opencode.session.error", WorkEventStop},
		{"hook.claude.Notification", WorkEventStop},
		{"hook.claude.PermissionRequest", WorkEventStop},
		{"hook.opencode.permission.ask", WorkEventStop},
		{"hook.claude.SubagentStart", WorkEventSubagentStart},
		{"hook.claude.SubagentStop", WorkEventSubagentStop},
		{"process_exit", WorkEventAbort},
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
