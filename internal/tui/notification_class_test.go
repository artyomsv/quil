package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

func TestEventGroup_Table(t *testing.T) {
	cases := []struct {
		eventType string
		want      string
	}{
		// Hook events are matched on the segment AFTER the source, so a fourth
		// agent source needs no table change.
		{"hook.claude.UserPromptSubmit", groupAgentTurn},
		{"hook.codex.Stop", groupAgentTurn},
		{"hook.claude.StopFailure", groupAgentTurn},
		{"hook.opencode.chat.message", groupAgentTurn},
		{"hook.opencode.session.idle", groupAgentTurn},
		{"hook.opencode.session.error", groupAgentTurn},
		{"hook.claude.PermissionRequest", groupAgentBlocked},
		{"hook.claude.Notification", groupAgentBlocked},
		{"hook.opencode.permission.ask", groupAgentBlocked},
		{"bell", groupAgentBlocked},
		{"hook.claude.SubagentStart", groupAgentSubagent},
		{"hook.codex.SubagentStop", groupAgentSubagent},
		{"hook.claude.TaskCreated", groupAgentSubagent},
		{"hook.claude.TaskCompleted", groupAgentSubagent},
		{"hook.claude.SessionEnd", groupAgentSession},
		{"hook.claude.PreCompact", groupAgentSession},
		{"hook.codex.PostCompact", groupAgentSession},
		{"process_exit", groupProcess},
		{"pane_destroyed", groupPane},
		{"pane_pinned", groupPane},
		{"pane_unpinned", groupPane},
		{"pane_marked_deletion", groupPane},
		{"pane_unmarked_deletion", groupPane},
		{"mcp_control", groupMCP},
		{"input_blocked", groupSystem},
		{"worktree_ready", groupSystem},
		{"worktree_failed", groupSystem},
		{"command_complete", groupCommands},
		{"output_idle", groupIdle},
	}
	for _, c := range cases {
		if got := eventGroup(c.eventType); got != c.want {
			t.Errorf("eventGroup(%q) = %q, want %q", c.eventType, got, c.want)
		}
	}
}

// A newer daemon can send a type this build has never heard of. It must land
// in a group that DEFAULTS ON: a wrong extra card is visible and the user can
// silence it, while a wrong hidden card is silent and the user never learns
// the event existed. Same direction the plugin-availability model takes.
func TestEventGroup_UnknownFallsBackToSystem(t *testing.T) {
	for _, typ := range []string{"", "totally_new_thing", "hook.newagent.Whatever", "hook.", "hook.claude."} {
		if got := eventGroup(typ); got != groupSystem {
			t.Errorf("eventGroup(%q) = %q, want %q", typ, got, groupSystem)
		}
	}
}

func TestGroupFilterFrom_DefaultConfig(t *testing.T) {
	f := groupFilterFrom(config.Default().Notification.Events)

	if !f.shows("hook.claude.Stop") {
		t.Error("agent_turn event hidden under the default config")
	}
	if f.shows("output_idle") {
		t.Error("output_idle shown under the default config, want hidden")
	}
	if f.shows("command_complete") {
		t.Error("command_complete shown under the default config, want hidden")
	}
	if !f.shows("brand_new_type") {
		t.Error("unknown type hidden under the default config, want shown")
	}
}

// A nil filter is the zero value on a Model a test built directly and never
// configured. It must not hide everything.
func TestEventGroupFilter_NilShowsEverything(t *testing.T) {
	var f eventGroupFilter
	if !f.shows("output_idle") {
		t.Error("nil filter hid an event; want show-everything")
	}
}

// Every group the config carries must be reachable from the table, or a
// toggle in the settings screen would do nothing.
func TestGroupFilterFrom_CoversEveryGroup(t *testing.T) {
	var all config.EventGroupsConfig // every field false
	f := groupFilterFrom(all)
	for _, g := range []string{
		groupAgentTurn, groupAgentBlocked, groupAgentSubagent, groupAgentSession,
		groupProcess, groupPane, groupMCP, groupSystem, groupCommands, groupIdle,
	} {
		if _, ok := f[g]; !ok {
			t.Errorf("group %q missing from groupFilterFrom's projection", g)
		}
	}
	if len(f) != 10 {
		t.Errorf("filter size: got %d, want 10", len(f))
	}
}
