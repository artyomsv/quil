// Package notifyclass files a pane event into the notification group the
// user toggles in F1 → Settings → Notifications. The TUI and quil web both
// use it, so the browser's sidebar shows exactly what the TUI's does.
package notifyclass

import (
	"strings"

	"github.com/artyomsv/quil/internal/config"
)

// Notification groups. A group is the unit the user toggles in
// F1 -> Settings -> Notifications; an event TYPE is an internal string that
// changes whenever an upstream tool adds a hook, so a setting keyed on types
// would rot while a setting keyed on groups survives.
const (
	AgentTurn     = "agent_turn"
	AgentBlocked  = "agent_blocked"
	AgentSubagent = "agent_subagent"
	AgentSession  = "agent_session"
	Process       = "process"
	Pane          = "pane"
	MCP           = "mcp"
	System        = "system"
	Commands      = "commands"
	Idle          = "idle"
)

// DefaultGroup is where an unrecognised type goes (see Group).
const DefaultGroup = System

// hookEventGroups maps a hook event's TRAILING segment — everything after
// "hook.<source>." — to its group.
//
// Keyed on the trailing part rather than the full type so a fourth agent
// source is classified correctly with no table change. That is deliberately
// different from the daemon's own ClassifyWorkEvent, which spells out a case
// per source and pays for it every time one is added: this table answers
// "should a human see it", which does not depend on which agent produced it.
var hookEventGroups = map[string]string{
	"UserPromptSubmit":  AgentTurn,
	"Stop":              AgentTurn,
	"StopFailure":       AgentTurn,
	"chat.message":      AgentTurn,
	"session.idle":      AgentTurn,
	"session.error":     AgentTurn,
	"PermissionRequest": AgentBlocked,
	"Notification":      AgentBlocked,
	"permission.ask":    AgentBlocked,
	"SubagentStart":     AgentSubagent,
	"SubagentStop":      AgentSubagent,
	"TaskCreated":       AgentSubagent,
	"TaskCompleted":     AgentSubagent,
	"SessionEnd":        AgentSession,
	"PreCompact":        AgentSession,
	"PostCompact":       AgentSession,
}

// plainEventGroups maps a non-hook event type, matched whole.
var plainEventGroups = map[string]string{
	"bell":                   AgentBlocked,
	"process_exit":           Process,
	"pane_destroyed":         Pane,
	"pane_pinned":            Pane,
	"pane_unpinned":          Pane,
	"pane_marked_deletion":   Pane,
	"pane_unmarked_deletion": Pane,
	"mcp_control":            MCP,
	"input_blocked":          System,
	// Hand-started agents. System rather than process: these report what Quil
	// did about a launch, not what a program did.
	"agent_adopted":    System,
	"agent_untracked":  System,
	"worktree_ready":   System,
	"worktree_failed":  System,
	"command_complete": Commands,
	"output_idle":      Idle,
}

// Group classifies one PaneEvent Type.
//
// An UNRECOGNISED type returns DefaultGroup (System), which defaults ON. That
// direction is deliberate: a wrong extra card is visible and the user can
// silence it, while a wrong hidden card is silent and the user never learns
// the event existed. It also makes a NEWER daemon paired with an OLDER client
// degrade safely — new event types render as ordinary cards instead of
// vanishing, which matters because a client attaches to remote daemons it does
// not control the version of.
func Group(eventType string) string {
	if rest, ok := strings.CutPrefix(eventType, "hook."); ok {
		// rest is "<source>.<event>", and the event may itself contain dots
		// (opencode's "chat.message"), so only the source segment is cut.
		if _, event, ok := strings.Cut(rest, "."); ok {
			if g, known := hookEventGroups[event]; known {
				return g
			}
		}
		return DefaultGroup
	}
	if g, ok := plainEventGroups[eventType]; ok {
		return g
	}
	return DefaultGroup
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// HookGroups and PlainGroups are copies of the tables, for quil web, which
// files events in the page with the same rule as Group.
func HookGroups() map[string]string  { return copyMap(hookEventGroups) }
func PlainGroups() map[string]string { return copyMap(plainEventGroups) }

// ShownGroups projects the config onto group → shown. The struct is the file
// format; the map is the hot path.
func ShownGroups(c config.EventGroupsConfig) map[string]bool {
	return map[string]bool{
		AgentTurn:     c.AgentTurn,
		AgentBlocked:  c.AgentBlocked,
		AgentSubagent: c.AgentSubagent,
		AgentSession:  c.AgentSession,
		Process:       c.Process,
		Pane:          c.Pane,
		MCP:           c.MCP,
		System:        c.System,
		Commands:      c.Commands,
		Idle:          c.Idle,
	}
}
