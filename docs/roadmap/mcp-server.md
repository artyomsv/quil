# MCP Server — Make Quil the AI's Eyes and Hands

| Field | Value |
|-------|-------|
| Priority | 5 |
| Effort | Medium |
| Impact | Very High (differentiation) |
| Status | Done |
| Depends on | — |

## Problem

**Layer 5: Cross-tool blindness** — AI assistants can't see the build error in the next pane. Claude Code, VS Code Copilot, Cursor — they're all blind to what's happening in other terminal sessions. The AI fixes code but can't see that the build is still failing in another pane. The developer becomes a copy-paste bridge between tools.

**No other terminal multiplexer offers this.** Quil becomes the **bridge between AI and the dev environment** — not just a container for AI sessions but an active collaborator.

## Implemented Solution

Expose Quil as a [Model Context Protocol](https://modelcontextprotocol.io/) server so AI assistants can interact with the terminal environment:

```
AI: "Check the test output in the build pane and fix the failing test"
→ MCP call: quil.read_pane_output(pane="build", last_lines=50)
→ AI sees: "FAIL src/auth.test.ts - Expected 200, got 401"
→ AI fixes the code
→ MCP call: quil.send_to_pane(pane="build", input="npm test")
```

### MCP Tools (13 total)

**Phase A — Core Interaction:**

| Tool | Description |
|------|-------------|
| `list_panes` | Enumerate all panes with types, names, CWDs |
| `read_pane_output` | Read last N lines from ring buffer (ANSI-stripped) |
| `send_to_pane` | Send text/commands to a pane (appends newline by default) |
| `get_pane_status` | Process running/exited, exit code, type, CWD |
| `create_pane` | Create new pane with plugin type |

**Phase B — Navigation & Lifecycle:**

| Tool | Description |
|------|-------------|
| `send_keys` | Named key sequences (arrows, F-keys, ctrl+a-z) with pacing |
| `restart_pane` | Kill + respawn with same plugin/CWD/args |
| `screenshot_pane` | VT-emulated text screenshot of actual screen state |
| `switch_tab` | Switch active tab |
| `list_tabs` | List tabs with pane counts |
| `destroy_pane` | Remove pane (auto-creates replacement if last) |
| `set_active_pane` | Focus pane in TUI (cross-tab) |
| `close_tui` | Exit TUI, daemon persists |

## Architecture: `quil mcp` Subcommand (Not a Separate Process)

The MCP server should be a **new subcommand** (`quil mcp`) that acts as a thin bridge — exactly like the TUI client is a bridge between Bubble Tea and the daemon:

```
┌──────────────┐    ┌───────────────┐    ┌──────────────┐
│ AI Tool       │    │ quil mcp    │    │ quild      │
│ (Claude, etc) │←──→│ (MCP↔IPC      │←──→│ (daemon)     │
│               │stdio│  bridge)     │sock │              │
│ JSON-RPC      │    │ Translates    │    │ Ring buffers  │
│               │    │ MCP ↔ IPC msgs│    │ PTY sessions  │
│               │    │               │    │ Plugins       │
└──────────────┘    └───────────────┘    └──────────────┘
```

### Why not directly in the daemon?

MCP servers are invoked by the AI tool as a child process via stdio. Claude Desktop, VS Code, Cursor — they all spawn `quil mcp` and talk JSON-RPC over stdin/stdout. The daemon is a long-running background service over sockets — fundamentally different lifecycle.

### Why not a separate binary?

All data lives in the daemon — ring buffers, session state, PTY handles, plugin registry. A separate binary would need its own IPC connection, which is exactly what `quil mcp` already is. A third binary fragments the project for no gain.

### Why `quil mcp` is the right design

| Concern | How it's handled |
|---------|-----------------|
| Data access | Connects to daemon via existing IPC socket — same as TUI client |
| Lifecycle | AI tool spawns/kills it — no new daemon management needed |
| Protocol | Translates MCP JSON-RPC (stdio) ↔ length-prefixed JSON (IPC) |
| New daemon messages | 2-3 new IPC message types: `ReadPaneOutput`, `ListPanesDetailed`, `PaneStatus` |
| Deployment | Already in the `quil` binary — zero extra install steps |
| Existing state | Ring buffers already have `Bytes()` for output replay — MCP just reads them |

## Technical Implementation

### 1. IPC Protocol Extension

Added `ID` field to `ipc.Message` (omitempty, backward compatible) for request-response correlation.

4 new request-response message pairs:

| Request | Response | Purpose |
|---------|----------|---------|
| `list_panes_req` | `list_panes_resp` | Enumerate panes with metadata |
| `read_pane_output_req` | `read_pane_output_resp` | Read ANSI-stripped text from ring buffer |
| `pane_status_req` | `pane_status_resp` | Process running/exited state, exit code |
| `create_pane_req` | `create_pane_resp` | Create pane, return new ID |

### 2. Process Exit Tracking

Added `WaitExit() int` to `pty.Session` interface. `Pane.ExitCode` and `Pane.ExitedAt` captured at end of `streamPTYOutput()`. Unix: `cmd.Wait()` + `ProcessState.ExitCode()`. Windows: `WaitForSingleObject` + `GetExitCodeProcess`.

### 3. MCP SDK

Official `github.com/modelcontextprotocol/go-sdk` (v1.4+). Typed tool handlers with struct-based input schemas (`jsonschema` tags). `StdioTransport` for JSON-RPC 2.0 over stdin/stdout.

### 4. AI Tool Configuration

```json
// claude_desktop_config.json or VS Code MCP settings
{
  "mcpServers": {
    "quil": {
      "command": "quil",
      "args": ["mcp"]
    }
  }
}
```

The daemon doesn't even need to know MCP exists — it just sees another client connecting to the socket.

### 5. Phase B: Navigation & Lifecycle Tools (8 additional tools)

| Tool | Description |
|------|-------------|
| `send_keys` | Named key sequences (arrows, F-keys, ctrl+a-z) with 50ms pacing between escape sequences |
| `restart_pane` | Kill PTY + respawn with same plugin/CWD/args; uses pane's last known dimensions |
| `screenshot_pane` | VT-emulated text screenshot via `charmbracelet/x/vt`; shows actual screen state |
| `switch_tab` | Switch active tab by ID |
| `list_tabs` | List all tabs with pane counts and active status |
| `destroy_pane` | Remove pane; auto-creates replacement if last in tab |
| `set_active_pane` | TUI cooperation: broadcasts to TUI to switch focus (cross-tab) |
| `close_tui` | TUI cooperation: broadcasts quit signal; daemon stays running |

### 6. MCP Interaction Logging & Redaction

Per-pane log files in `~/.quil/mcp-logs/`. Two-layer redaction:
- **Layer 1 (AI markers):** `<<REDACT>>value<</REDACT>>` — stripped before PTY, counted in log
- **Layer 2 (regex fallback):** Common patterns (OpenAI keys, GitHub PATs, JWTs, passwords, BIP-32 keys) caught automatically

MCP server `Instructions` field guides AI on tool usage and redaction marker protocol.

### 7. Visual Feedback

Orange pane border highlight (ANSI color 208) when MCP interacts with a pane. Duration configurable via `[mcp] highlight_duration` (default 10s, max 60s). Timer resets on rapid interactions.

### 8. Files

| File | Change |
|------|--------|
| `cmd/quil/mcp.go` | New — MCP bridge: daemon connection, request-response, server instructions |
| `cmd/quil/mcp_tools.go` | New — 13 tool implementations with typed input structs |
| `cmd/quil/mcp_keys.go` | New — Key name → escape sequence map (50+ keys) |
| `cmd/quil/mcp_log.go` | New — Per-pane logging, redaction markers, regex fallback |
| `internal/ipc/protocol.go` | Add `ID` field, 27 new message types + payload structs |
| `internal/daemon/daemon.go` | 10 new handlers, `respondTo()`, `highlightPane()`, exit code capture |
| `internal/daemon/session.go` | Add `ExitCode`, `ExitedAt`, `Cols`, `Rows` to Pane struct |
| `internal/config/config.go` | Add `MCPConfig` with `HighlightDuration`, `LogDir` |
| `internal/pty/session.go` | Add `WaitExit() int` to Session interface |
| `internal/pty/session_unix.go` | Implement `WaitExit()` with `sync.Once` |
| `internal/pty/session_windows.go` | Implement `WaitExit()` with `sync.Once`, `windows.Handle` |
| `internal/tui/model.go` | Handle `MsgHighlightPane`, `MsgSetActivePane`, `MsgCloseTUI` |
| `internal/tui/pane.go` | Orange border when `mcpHighlight` flag set |
| `internal/tui/styles.go` | `mcpHighlightBorder` style (color 208) |
| `cmd/quil/main.go` | Add `mcp` subcommand routing, extract daemon retry constants |

## Success Criteria

- [x] `quil mcp` starts and speaks MCP JSON-RPC over stdio
- [x] Claude Desktop / Claude Code can connect via `.mcp.json` config
- [x] `list_panes` returns all panes with metadata
- [x] `read_pane_output` returns last N lines from any pane (ANSI-stripped)
- [x] `send_to_pane` sends input to a pane's PTY
- [x] `get_pane_status` returns process state and exit code
- [x] `create_pane` creates new panes with plugin type support
- [x] `send_keys` navigates interactive TUI menus (paced escape sequences)
- [x] `screenshot_pane` shows VT-emulated screen state
- [x] `restart_pane` kills and respawns with same config
- [x] `set_active_pane` switches TUI focus (cross-tab)
- [x] `close_tui` exits TUI while daemon persists
- [x] Orange border highlights pane during MCP interaction
- [x] Per-pane MCP logs with redaction (no secrets on disk)
- [x] AI can communicate with Claude Code in another pane (tested end-to-end)

## Resolved Questions

- **ANSI stripping** — `read_pane_output` returns stripped text via `charmbracelet/x/ansi.Strip()`. Raw output deferred.
- **Rate limiting** — Deferred. AI tools self-regulate via conversation flow. `send_keys` capped at 1000 keys.
- **Workspace definitions** — Deferred to M9 (workspace files).
- **MCP resources** — Deferred. Tools sufficient for v1. Event subscriptions planned for M12 integration.
- **Key pacing** — Escape sequences sent individually with 50ms delays. Plain text batched. Solved interactive TUI menu navigation.
- **Screenshot dimensions** — Uses pane's last known `Cols`/`Rows` from resize events. Capped at 500x200.
- **Sensitive data** — Two-layer redaction (AI markers + regex). Logs only metadata (byte counts, line counts). Verified no secrets in daemon/TUI/MCP logs.
