package tui

import (
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/notifyclass"
)

// Notification groups. The tables and the classification live in
// internal/notifyclass, which quil web shares, so the browser's sidebar files
// every event exactly as this one does. These names keep the TUI's own.
const (
	groupAgentTurn     = notifyclass.AgentTurn
	groupAgentBlocked  = notifyclass.AgentBlocked
	groupAgentSubagent = notifyclass.AgentSubagent
	groupAgentSession  = notifyclass.AgentSession
	groupProcess       = notifyclass.Process
	groupPane          = notifyclass.Pane
	groupMCP           = notifyclass.MCP
	groupSystem        = notifyclass.System
	groupCommands      = notifyclass.Commands
	groupIdle          = notifyclass.Idle
)

// eventGroup classifies one PaneEvent Type (see notifyclass.Group).
func eventGroup(eventType string) string { return notifyclass.Group(eventType) }

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
	return eventGroupFilter(notifyclass.ShownGroups(c))
}

// shows reports whether an event of this type belongs on the sidebar.
func (f eventGroupFilter) shows(eventType string) bool {
	if f == nil {
		return true
	}
	return f[eventGroup(eventType)]
}
