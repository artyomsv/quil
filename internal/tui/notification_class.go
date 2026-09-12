package tui

import (
	"strings"

	"github.com/artyomsv/quil/internal/config"
)

// Notification groups. A group is the unit the user toggles in
// F1 -> Settings -> Notifications; an event TYPE is an internal string that
// changes whenever an upstream tool adds a hook, so a setting keyed on types
// would rot while a setting keyed on groups survives.
const (
	groupAgentTurn     = "agent_turn"
	groupAgentBlocked  = "agent_blocked"
	groupAgentSubagent = "agent_subagent"
	groupAgentSession  = "agent_session"
	groupProcess       = "process"
	groupPane          = "pane"
	groupMCP           = "mcp"
	groupSystem        = "system"
	groupCommands      = "commands"
	groupIdle          = "idle"
)

// hookEventGroups maps a hook event's TRAILING segment — everything after
// "hook.<source>." — to its group.
//
// Keyed on the trailing part rather than the full type so a fourth agent
// source is classified correctly with no table change. That is deliberately
// different from the daemon's own ClassifyWorkEvent, which spells out a case
// per source and pays for it every time one is added: this table answers
// "should a human see it", which does not depend on which agent produced it.
var hookEventGroups = map[string]string{
	"UserPromptSubmit":  groupAgentTurn,
	"Stop":              groupAgentTurn,
	"StopFailure":       groupAgentTurn,
	"chat.message":      groupAgentTurn,
	"session.idle":      groupAgentTurn,
	"session.error":     groupAgentTurn,
	"PermissionRequest": groupAgentBlocked,
	"Notification":      groupAgentBlocked,
	"permission.ask":    groupAgentBlocked,
	"SubagentStart":     groupAgentSubagent,
	"SubagentStop":      groupAgentSubagent,
	"TaskCreated":       groupAgentSubagent,
	"TaskCompleted":     groupAgentSubagent,
	"SessionEnd":        groupAgentSession,
	"PreCompact":        groupAgentSession,
	"PostCompact":       groupAgentSession,
}

// plainEventGroups maps a non-hook event type, matched whole.
var plainEventGroups = map[string]string{
	"bell":                   groupAgentBlocked,
	"process_exit":           groupProcess,
	"pane_destroyed":         groupPane,
	"pane_pinned":            groupPane,
	"pane_unpinned":          groupPane,
	"pane_marked_deletion":   groupPane,
	"pane_unmarked_deletion": groupPane,
	"mcp_control":            groupMCP,
	"input_blocked":          groupSystem,
	"worktree_ready":         groupSystem,
	"command_complete":       groupCommands,
	"output_idle":            groupIdle,
}

// eventGroup classifies one PaneEvent Type.
//
// An UNRECOGNISED type returns groupSystem, which defaults ON. That direction
// is deliberate: a wrong extra card is visible and the user can silence it,
// while a wrong hidden card is silent and the user never learns the event
// existed. It also makes a NEWER daemon paired with an OLDER client degrade
// safely — new event types render as ordinary cards instead of vanishing,
// which matters because a client attaches to remote daemons it does not
// control the version of.
func eventGroup(eventType string) string {
	if rest, ok := strings.CutPrefix(eventType, "hook."); ok {
		// rest is "<source>.<event>", and the event may itself contain dots
		// (opencode's "chat.message"), so only the source segment is cut.
		if _, event, ok := strings.Cut(rest, "."); ok {
			if g, known := hookEventGroups[event]; known {
				return g
			}
		}
		return groupSystem
	}
	if g, ok := plainEventGroups[eventType]; ok {
		return g
	}
	return groupSystem
}

// eventGroupFilter answers "does the sidebar show this group".
//
// A NIL filter shows everything. That is load-bearing rather than defensive:
// roughly 46 tests build a Model directly and never set config, four of them
// assert on output_idle and bell events, and a zero value that hid every event
// would break all of them while claiming the feature worked.
type eventGroupFilter map[string]bool

// groupFilterFrom projects the config struct onto the map the renderer reads.
// The struct is the file format; the map is the hot path.
func groupFilterFrom(c config.EventGroupsConfig) eventGroupFilter {
	return eventGroupFilter{
		groupAgentTurn:     c.AgentTurn,
		groupAgentBlocked:  c.AgentBlocked,
		groupAgentSubagent: c.AgentSubagent,
		groupAgentSession:  c.AgentSession,
		groupProcess:       c.Process,
		groupPane:          c.Pane,
		groupMCP:           c.MCP,
		groupSystem:        c.System,
		groupCommands:      c.Commands,
		groupIdle:          c.Idle,
	}
}

// shows reports whether an event of this type belongs on the sidebar.
func (f eventGroupFilter) shows(eventType string) bool {
	if f == nil {
		return true
	}
	return f[eventGroup(eventType)]
}
