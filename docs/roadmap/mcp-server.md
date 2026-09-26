# MCP Server — Make Quil the AI's Eyes and Hands

| Field | Value |
|-------|-------|
| Priority | 5 |
| Effort | Medium |
| Impact | Very High (differentiation) |
| Status | Done — expanded to 34 tools in v1.72.0 |
| Depends on | Projects, remote daemon connections and agent hook events |

## Problem

An AI assistant needs to see the test failure in another pane, find the project
that owns it, and ask another agent to work on it. Without a workspace API, the
developer copies output between sessions and polls for completion. Remote hosts
make this harder: the relevant project may be on another machine.

## Implemented solution

`quil mcp` exposes 36 Model Context Protocol tools over stdio. An MCP-capable
client can discover the workspace, create tabs and AI panes using the same
validated options as the TUI, manage projects across configured remote hosts,
and delegate work between panes with completion tracking and notify-back.

This PRD records the capability and its constraints. The [MCP guide](../mcp.md)
is the reference for client configuration, input schemas, responses and examples.

## Tools by purpose (36 total)

### Discovery (6)

Agents discover IDs and capabilities before acting; plugin availability and
session occupancy come from the daemon that will perform the action.

| Tool | Purpose |
|------|---------|
| `list_panes` | Find panes, their projects, hosts and known agent work state |
| `list_tabs` | Find tabs, optionally within a project |
| `list_projects` | Discover project roots and tab membership |
| `list_hosts` | Inspect remote connection status, version and errors |
| `list_plugins` | Discover available pane types, toggles and sandbox support |
| `list_sessions` | Find Claude Code sessions for a directory and their occupancy |

### Inspection (3)

| Tool | Purpose |
|------|---------|
| `read_pane_output` | Read ANSI-stripped recent output |
| `screenshot_pane` | Inspect the terminal screen as VT-emulated text |
| `get_pane_status` | Check process state, exit code and worktree preparation |

### Interaction (2)

| Tool | Purpose |
|------|---------|
| `send_to_pane` | Queue text or a bracketed paste, optionally pressing Enter |
| `send_keys` | Navigate interactive programs with named keys and pacing |

### Pane lifecycle (4)

| Tool | Purpose |
|------|---------|
| `create_pane` | Create a pane with named toggles, session resume, worktree or sandbox options |
| `rename_pane` | Label a pane |
| `restart_pane` | Restart the child with the pane's configuration and dimensions |
| `destroy_pane` | Remove a pane, preserving the last-pane replacement behavior |

### Projects and tabs (9)

Projects let an orchestrator group related workers and keep their working
directories explicit. Creating a tab does not take the user's focus.

| Tool | Purpose |
|------|---------|
| `create_project` | Create a project with a root directory and initial shell tab |
| `update_project` | Rename a project or change its root |
| `switch_project` | Show the project's last active tab |
| `destroy_project` | Remove the project and its tabs and panes |
| `create_from_template` | Create ordered panes and starting prompts from a workspace template (daemon 1.74.0+) |
| `create_tab` | Create a tab with a configurable first pane |
| `rename_tab` | Label a tab |
| `switch_tab` | Show a tab |
| `destroy_tab` | Remove a tab, retaining a shell tab when its project becomes empty |

### Task delegation (4)

| Tool | Purpose |
|------|---------|
| `delegate_task` | Deliver a prompt to a target pane and track its outcome |
| `get_task` | Read one task's status |
| `wait_task` | Wait for completion or a bounded timeout |
| `list_tasks` | List the daemon's retained tasks |

An AI task finishes when the target's hook-derived work state settles idle,
including subagent activity; a raw Stop event alone is insufficient. A terminal
task finishes on shell command completion. Notify-back waits until the requester
can receive input and is limited to panes on the same daemon. Tasks are bounded
and runtime-only. See [task delegation](../mcp.md#delegating-work-to-another-pane).

### TUI cooperation (3)

| Tool | Purpose |
|------|---------|
| `set_active_pane` | Focus a pane, including across tabs, in one attached window |
| `close_tui` | Close one attached window while the daemon and panes remain alive |
| `list_clients` | List attached windows, which one sets pane sizes, and their ids (daemon 1.80.0+) |

### Event observation (3)

| Tool | Purpose |
|------|---------|
| `get_notifications` | Read queued workspace events |
| `watch_notifications` | Wait for an event, including agent-idle and task completion |
| `dismiss_notifications` | Acknowledge events |

### Memory reporting (2)

| Tool | Purpose |
|------|---------|
| `get_memory_report` | Inspect workspace and per-tab memory use |
| `get_pane_memory` | Inspect one pane's memory breakdown |

## Architecture and behavior

The AI client spawns `quil mcp` as a child process. The bridge translates MCP
JSON-RPC on stdio into the daemon's length-prefixed IPC protocol; `Message.ID`
correlates requests and replies. PTYs, buffers, project state and task tracking
remain in the daemon. This keeps the MCP process independent of the TUI's
lifetime and requires no extra installed binary.

The bridge connects to its local daemon and dials configured `[[destinations]]`
over SSH in the background. An explicit host wins, then a cached pane/tab/project
ID determines the host, then the local daemon is the fallback. Unscoped lists
aggregate connected hosts; named-host failures are errors. A failed remote does
not hide other hosts' results, and `list_hosts` exposes its error. Unscoped lists
and status reads schedule retries after the 30-second backoff without waiting
for the dial. Successful reconnection clears stale errors.

The daemon validates create options before mutation: named toggles cannot clash,
worktrees must resolve to a repository, and rejected sandbox options cannot
silently create an unsandboxed pane. New panes start with known dimensions when
available. Restarted PTY output identifies its run so attached TUIs reset the
old terminal state and discard late output from the replaced process.

## Safety and visibility

- Input tools acknowledge queue acceptance or report why input could not be queued.
- Destructive tools document user confirmation before removing or restarting panes.
- Per-pane interaction logs redact explicit secret markers and common secret patterns.
- MCP control highlights the affected pane and emits visible activity events.
- Version checks refuse newer requests against daemons that cannot understand them.
- Per-project MCP authorization/scoping remains deferred; project filters and
  multi-host routing are discovery and addressing features, not access boundaries.

## Acceptance

- MCP clients can connect through stdio and discover all 36 registered tools.
- Agents can manage projects, tabs and panes on local and configured remote daemons.
- Unscoped discovery recovers hosts after the retry backoff without a named call.
- Pane creation honors TUI-equivalent options and reports validation or spawn errors.
- Delegated tasks expose completion/failure and notify the requester when appropriate.
- Event watching, memory reporting, redaction and TUI cooperation remain available.

The [user guide](../mcp.md) provides the detailed tool contract; the runtime
registrations in `cmd/quil/mcp_tools*.go` are the source of truth for tool names.
