# MCP Server — Make Aethel the AI's Eyes and Hands

| Field | Value |
|-------|-------|
| Priority | 5 |
| Effort | Medium |
| Impact | Very High (differentiation) |
| Status | Proposed |
| Depends on | — |

## Problem

**Layer 5: Cross-tool blindness** — AI assistants can't see the build error in the next pane. Claude Code, VS Code Copilot, Cursor — they're all blind to what's happening in other terminal sessions. The AI fixes code but can't see that the build is still failing in another pane. The developer becomes a copy-paste bridge between tools.

**No other terminal multiplexer offers this.** Aethel becomes the **bridge between AI and the dev environment** — not just a container for AI sessions but an active collaborator.

## Proposed Solution

Expose Aethel as a [Model Context Protocol](https://modelcontextprotocol.io/) server so AI assistants can interact with the terminal environment:

```
AI: "Check the test output in the build pane and fix the failing test"
→ MCP call: aethel.read_pane_output(pane="build", last_lines=50)
→ AI sees: "FAIL src/auth.test.ts - Expected 200, got 401"
→ AI fixes the code
→ MCP call: aethel.send_to_pane(pane="build", input="npm test")
```

### MCP Tools

| Tool | Description |
|------|-------------|
| `list_panes` | Enumerate all panes with types, names, CWDs |
| `read_pane_output` | Read the last N lines from any pane's ring buffer |
| `send_to_pane` | Send keystrokes/commands to a pane |
| `get_pane_status` | Process running, exit code, error state |
| `create_pane` | Spin up a new pane with a plugin type |

## Architecture: `aethel mcp` Subcommand (Not a Separate Process)

The MCP server should be a **new subcommand** (`aethel mcp`) that acts as a thin bridge — exactly like the TUI client is a bridge between Bubble Tea and the daemon:

```
┌──────────────┐    ┌───────────────┐    ┌──────────────┐
│ AI Tool       │    │ aethel mcp    │    │ aetheld      │
│ (Claude, etc) │←──→│ (MCP↔IPC      │←──→│ (daemon)     │
│               │stdio│  bridge)     │sock │              │
│ JSON-RPC      │    │ Translates    │    │ Ring buffers  │
│               │    │ MCP ↔ IPC msgs│    │ PTY sessions  │
│               │    │               │    │ Plugins       │
└──────────────┘    └───────────────┘    └──────────────┘
```

### Why not directly in the daemon?

MCP servers are invoked by the AI tool as a child process via stdio. Claude Desktop, VS Code, Cursor — they all spawn `aethel mcp` and talk JSON-RPC over stdin/stdout. The daemon is a long-running background service over sockets — fundamentally different lifecycle.

### Why not a separate binary?

All data lives in the daemon — ring buffers, session state, PTY handles, plugin registry. A separate binary would need its own IPC connection, which is exactly what `aethel mcp` already is. A third binary fragments the project for no gain.

### Why `aethel mcp` is the right design

| Concern | How it's handled |
|---------|-----------------|
| Data access | Connects to daemon via existing IPC socket — same as TUI client |
| Lifecycle | AI tool spawns/kills it — no new daemon management needed |
| Protocol | Translates MCP JSON-RPC (stdio) ↔ length-prefixed JSON (IPC) |
| New daemon messages | 2-3 new IPC message types: `ReadPaneOutput`, `ListPanesDetailed`, `PaneStatus` |
| Deployment | Already in the `aethel` binary — zero extra install steps |
| Existing state | Ring buffers already have `Bytes()` for output replay — MCP just reads them |

## Technical Approach

### 1. New Daemon Messages (small additions)

| Message | Purpose |
|---------|---------|
| `MsgReadPaneOutput` | Request last N lines from a pane's ring buffer (data already exists from ghost buffer support) |
| `MsgListPanesDetailed` | Return pane names, types, CWDs, process status |
| `MsgPaneExitStatus` | Field on the existing pane struct |

The core daemon logic stays untouched. The MCP subcommand is ~300-500 lines — a thin translation layer.

### 2. AI Tool Configuration

```json
// claude_desktop_config.json or VS Code MCP settings
{
  "mcpServers": {
    "aethel": {
      "command": "aethel",
      "args": ["mcp"]
    }
  }
}
```

The daemon doesn't even need to know MCP exists — it just sees another client connecting to the socket.

### 3. Files

| File | Change |
|------|--------|
| `cmd/aethel/mcp.go` | New — MCP server main loop, JSON-RPC handling |
| `cmd/aethel/mcp_tools.go` | New — tool implementations (list, read, send, status, create) |
| `internal/ipc/messages.go` | Add 2-3 new message types |
| `internal/daemon/handler.go` | Handle new message types |
| `cmd/aethel/main.go` | Add `mcp` subcommand routing |

## Success Criteria

- [ ] `aethel mcp` starts and speaks MCP JSON-RPC over stdio
- [ ] Claude Desktop can connect with the config above
- [ ] `list_panes` returns all panes with metadata
- [ ] `read_pane_output` returns last N lines from any pane
- [ ] `send_to_pane` sends input to a pane's PTY
- [ ] `get_pane_status` returns process state
- [ ] AI can autonomously read build output and act on it

## Open Questions

- Should `read_pane_output` return raw terminal output or stripped ANSI?
- Rate limiting on `send_to_pane` to prevent AI from flooding a terminal?
- Should `create_pane` support workspace-file-style definitions?
- Resource endpoints (MCP resources) for continuous pane monitoring?
