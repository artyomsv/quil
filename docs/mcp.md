# MCP Server — Make Quil the AI's Eyes and Hands

Quil ships with a built-in [Model Context Protocol](https://modelcontextprotocol.io/) server that lets AI assistants (Claude Desktop, Claude Code, Cursor, VS Code Copilot, any MCP-capable client) read pane output, send commands, watch for events, and orchestrate your terminal workspace.

The result: your AI can **see what's in your build pane and react**, instead of you copy-pasting error messages into chat.

## Table of contents

- [How it works](#how-it-works)
- [Wiring Quil into your AI client](#wiring-quil-into-your-ai-client)
  - [Claude Desktop](#claude-desktop)
  - [Claude Code (CLI)](#claude-code-cli)
  - [Cursor](#cursor)
  - [VS Code (GitHub Copilot Chat)](#vs-code-github-copilot-chat)
  - [Any MCP-capable client](#any-mcp-capable-client)
- [Verify the connection](#verify-the-connection)
- [The 17 tools](#the-17-tools)
  - [Discovery](#discovery)
  - [Reading pane output](#reading-pane-output)
  - [Interacting with panes](#interacting-with-panes)
  - [Pane lifecycle](#pane-lifecycle)
  - [TUI cooperation](#tui-cooperation)
  - [Event observation](#event-observation)
  - [Memory reporting](#memory-reporting)
- [Example AI prompts](#example-ai-prompts)
- [Security: redaction model](#security-redaction-model)
- [Visual MCP-activity indicator](#visual-mcp-activity-indicator)
- [Per-pane logging](#per-pane-logging)
- [Troubleshooting](#troubleshooting)

## How it works

`quil mcp` is a thin bridge process that AI clients spawn over stdio. It speaks MCP JSON-RPC on stdin/stdout and forwards requests to the running `quild` daemon over its Unix socket — the same socket the TUI uses.

```
┌──────────────┐  stdio   ┌───────────────┐  Unix sock  ┌──────────────┐
│ AI client    │ ←──────→ │ quil mcp      │ ←─────────→ │ quild        │
│ (Claude,     │ JSON-RPC │ (MCP ↔ IPC    │ length-     │ (daemon —    │
│  Cursor,…)   │          │  bridge)      │ prefixed    │  ring buffers │
│              │          │               │ JSON        │  + PTYs +    │
│              │          │               │             │  plugins)    │
└──────────────┘          └───────────────┘             └──────────────┘
```

- **`quil mcp` is one process per AI client**. Each time the AI client starts, it spawns a fresh `quil mcp` over stdio. The bridge auto-starts the daemon if it isn't already running.
- **No second config file** — all state lives in the daemon. The MCP bridge has no state of its own.
- **No network** — the bridge is filesystem-local. The daemon socket lives at `~/.quil/quild.sock` (mode `0600`). Only your user account can connect.

## Wiring Quil into your AI client

Each client has its own config file. Add the snippet below, restart the client, and Quil shows up in the tool picker.

You need `quil` on the AI client's `PATH`. If `quil --version` works in your shell, you're set. If your AI client doesn't inherit your shell PATH (common on macOS for GUI apps), use the absolute path — e.g., `~/.local/bin/quil`.

### Claude Desktop

Edit `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS) or `%APPDATA%\Claude\claude_desktop_config.json` (Windows):

```json
{
  "mcpServers": {
    "quil": {
      "command": "quil",
      "args": ["mcp"]
    }
  }
}
```

Restart Claude Desktop. The 🔌 icon in the input bar should show Quil with 17 tools.

### Claude Code (CLI)

```bash
claude mcp add quil quil mcp
```

Or edit `~/.claude/settings.json` manually:

```json
{
  "mcpServers": {
    "quil": {
      "command": "quil",
      "args": ["mcp"]
    }
  }
}
```

### Cursor

Open Cursor settings → MCP → Add new server:

```json
{
  "mcpServers": {
    "quil": {
      "command": "quil",
      "args": ["mcp"]
    }
  }
}
```

### VS Code (GitHub Copilot Chat)

Add to `settings.json`:

```jsonc
{
  "github.copilot.chat.mcp.servers": {
    "quil": {
      "command": "quil",
      "args": ["mcp"]
    }
  }
}
```

### Any MCP-capable client

The general contract is: **the client spawns `quil mcp` as a subprocess and talks MCP over its stdio**. Most clients accept a config object with `command` + `args`. Use:

| Field | Value |
|---|---|
| `command` | `quil` (or absolute path) |
| `args` | `["mcp"]` |
| `env` | not required — bridge inherits the client's env |

## Verify the connection

In your AI client, ask:

> List all my Quil panes.

The AI should call `list_panes` and return a JSON array with each pane's `id`, `type`, `tab_id`, `cwd`, etc. If you see "no Quil panes" or an error, check [Troubleshooting](#troubleshooting).

## The 17 tools

Tools are grouped below by purpose. Every tool returns a `text` content block; many return JSON-formatted payloads.

### Discovery

Start here. Other tools need pane IDs and tab IDs as inputs.

| Tool | Input | Returns |
|---|---|---|
| `list_panes` | — | JSON array of all panes: `{id, type, name, cwd, tab_id, alive, pid}` |
| `list_tabs` | — | JSON array of all tabs: `{id, name, pane_count, active}` |

### Reading pane output

Two ways to see what's in a pane — pick the right one for the kind of program running there.

| Tool | Input | Returns | Use when |
|---|---|---|---|
| `read_pane_output` | `pane_id` (required), `last_lines` (default 50, max 1000) | ANSI-stripped scrollback text | Shell command output, build logs, test results — anything line-oriented |
| `screenshot_pane` | `pane_id` (required), `width` (default 80), `height` (default 24) | VT-emulated screen text + cursor position | Interactive TUIs (vim, htop, Claude Code) — `read_pane_output` would show raw escape sequences |
| `get_pane_status` | `pane_id` (required) | JSON: `{alive, exit_code, type, cwd, pid}` | Check if a process is still running, or what its exit code was |

### Interacting with panes

| Tool | Input | Returns | Notes |
|---|---|---|---|
| `send_to_pane` | `pane_id` (required), `input` (text), `press_enter` (default true) | "Sent N bytes to <pane>" | Use for typing commands. Newline is appended by default so the command executes. **Wrap secrets in `<<REDACT>>...<</REDACT>>` markers** — see [Security](#security-redaction-model) |
| `send_keys` | `pane_id` (required), `keys` (array of names or literal text, max 1000) | "Sent N keys to <pane>" | Use for navigating TUIs. Each entry is either a key name (see table below) or literal text. The bridge inserts 50 ms between escape sequences so a TUI app processes each key before the next arrives. |

**Recognised key names** (case-insensitive):

`enter`, `tab`, `escape` / `esc`, `up`, `down`, `left`, `right`, `home`, `end`, `page_up`, `page_down`, `backspace`, `delete`, `space`, `f1` through `f12`, `ctrl+a` through `ctrl+z`. Anything else is treated as literal text.

### Pane lifecycle

| Tool | Input | Returns | Notes |
|---|---|---|---|
| `create_pane` | `tab_id` (optional, default = active), `cwd` (optional), `type` (default `terminal`; also `claude-code`, `opencode`, `ssh`, `stripe`) | JSON: `{pane_id, tab_id}` | Spawns a new pane in the given tab. |
| `restart_pane` | `pane_id` (required) | JSON: `{success, message}` | Kills + respawns with the same plugin, CWD, and instance config. Useful for stuck or crashed panes. AI clients should confirm with the user before calling. |
| `destroy_pane` | `pane_id` (required) | "Destroyed pane <id>" or "Failed to destroy" | If it was the last pane in a tab, a replacement terminal pane is auto-created. AI clients should confirm before calling. |

### TUI cooperation

These steer the live TUI window (if one is attached).

| Tool | Input | Returns | Notes |
|---|---|---|---|
| `switch_tab` | `tab_id` (required) | "Switched to tab <id>" | Brings a different tab into view in the TUI. |
| `set_active_pane` | `pane_id` (required) | "Set active pane to <id>" | Focuses the pane. Auto-switches tab if needed. |
| `close_tui` | — | "TUI close signal sent. Daemon continues running." | Closes the TUI window. Daemon and all pane processes keep running — reattach by running `quil` again. |

### Event observation

Replace polling-with-sleep + screenshot with the blocking watcher.

| Tool | Input | Returns | Notes |
|---|---|---|---|
| `get_notifications` | — | JSON array of pending events: `{pane_id, kind, title, body, timestamp, severity}` | Non-blocking. Drains the daemon's event queue. |
| `watch_notifications` | `pane_ids` (optional, empty = all), `timeout` (seconds, default 60, max 300) | JSON: the first event that fires, or `{timed_out: true}` | **Blocks** up to `timeout` seconds. Use after kicking off a long-running task ("watch the build pane until it finishes"). Replaces sleep+poll patterns. |

The events fired by the daemon include: process exits (any pane), OSC 133 command completion (shell panes), bell characters (with 30 s cooldown to avoid storming), and smart-idle pattern matches based on per-plugin `[[notification_handlers]]` in TOML.

### Memory reporting

| Tool | Input | Returns | Notes |
|---|---|---|---|
| `get_memory_report` | — | JSON: `{snapshot_at, total_bytes, total_human, go_heap_bytes, pty_rss_bytes, tabs[]}` | Per-tab aggregation. Go-heap includes ring buffers + ghost snapshots + plugin state; PTY RSS is OS-reported (not comparable cross-platform). |
| `get_pane_memory` | `pane_id` (required) | JSON: per-pane detail with each layer broken out | Drill into a specific pane. |

## Example AI prompts

These are the kinds of asks where the MCP integration earns its keep.

**Build monitoring** —
> Watch the pane running `cargo build` and tell me when it finishes. If it failed, read the last 100 lines and propose a fix.

The AI calls `list_panes` to find the build pane, `watch_notifications` (with that pane_id) to block until exit, then `read_pane_output` if the exit code was non-zero.

**Cross-pane context** —
> The Claude Code pane is asking about my schema. Read the last screen of the `psql` pane and paste the table definitions into Claude.

The AI calls `screenshot_pane` on the psql pane, then `send_to_pane` on the Claude pane (since Claude Code is a TUI, `screenshot_pane` is the right read).

**Workspace setup** —
> Open a new terminal pane in `~/work/quil`, run `./scripts/dev.sh test`, and switch focus to it.

The AI calls `create_pane` (with `cwd="~/work/quil"` and `type="terminal"`), `send_to_pane` to type the command, then `set_active_pane` to focus it.

**Triage a stuck pane** —
> The third pane in the build tab is unresponsive. What's the status, and can you restart it?

The AI calls `list_panes` + `get_pane_status` to inspect, then asks for confirmation before calling `restart_pane`.

## Security: redaction model

When the AI sends a command that contains secrets (API keys, passwords, tokens, mnemonic phrases), it can wrap the value in **redaction markers** so the secret reaches the terminal cleanly but never lands in the MCP interaction log:

```
send_to_pane(input="export OPENAI_API_KEY=<<REDACT>>sk-abc123def…<</REDACT>>")
```

- **The markers are stripped before the data reaches the PTY** — the shell sees `export OPENAI_API_KEY=sk-abc123def…`.
- **The log shows the marker count, not the value** — e.g., `[%d redacted]` appears in `~/.quil/mcp-logs/<pane-id>.log`.

Even if the AI forgets to wrap, **Layer 2 redaction** catches common secret patterns via regex in the log:

| Pattern | Caught |
|---|---|
| `sk-[a-zA-Z0-9]{20,}` | OpenAI keys |
| `ghp_[a-zA-Z0-9]{36,}` | GitHub personal access tokens |
| `ghs_[a-zA-Z0-9]{36,}` | GitHub app tokens |
| `eyJ…\.eyJ…` | JWT tokens |
| `(password\|secret\|token\|api_key)\s*[=:]\s*\S+` | Common `key=value` form |
| 64+ hex chars | Private keys (min 64 avoids git SHA-1 false positives) |
| `xprv…` / `xpub…` | BIP-32 extended keys |

Both layers are best-effort defense — the authoritative protection is the marker wrapping. Quil also writes a one-time **server instructions** message to the AI client on connect that tells the model when and how to use the markers. Most modern MCP-capable clients respect it.

## Visual MCP-activity indicator

When the AI interacts with a pane, its border flashes **orange** for a configurable duration so you can see which pane the AI is touching from across the screen.

Tune via `config.toml`:

```toml
[mcp]
highlight_duration = "10s"  # default
```

## Per-pane logging

Every MCP tool call that targets a specific pane is logged to:

```
~/.quil/mcp-logs/<pane-id>.log
```

…with timestamp, tool name, pane id, and a sanitized detail string. View them from inside the TUI:

> `F1 → View MCP logs`

The log viewer aggregates all per-pane files in order of most-recent-modification and renders read-only (no edits possible).

The log file is created with `0600` permissions and only your user account can read it.

## Troubleshooting

**"cannot connect to daemon" error on first MCP call** —
The bridge tried to auto-start `quild` but couldn't. Check that `quild` is on `PATH` (same directory as `quil` works) and that `~/.quil/` is writable. Then start it manually: `quil daemon start`.

**AI client says "no MCP server named quil"** —
The client doesn't see your config. Restart the client. If that doesn't work, check that `quil` is on the client's `PATH` (not just your shell's). On macOS, GUI-launched apps don't inherit terminal `PATH` — use the absolute path in the config, e.g., `"command": "/Users/you/.local/bin/quil"`.

**Tool calls hang for ~10 s and time out** —
The bridge's request timeout is 10 s. Long-running operations should use `watch_notifications` (blocking, configurable timeout up to 300 s) instead of a synchronous tool. If a normal tool consistently times out, check `~/.quil/quild.log` for daemon errors.

**Border doesn't flash orange when the AI calls a tool** —
The flash is configurable; check `[mcp] highlight_duration` in `~/.quil/config.toml`. If you set it to `0s`, no flash. The flash also requires an attached TUI — MCP calls land in the daemon, and the daemon broadcasts the highlight event for the TUI to render.

**Where do I see what the AI did?** —
Three places:
- `~/.quil/mcp-logs/<pane-id>.log` — per-pane interaction log (timestamps + tool names + sanitized detail)
- TUI status bar — current MCP activity (pane border flash)
- `~/.quil/quild.log` — daemon log; grep for `ipc recv:` to see every IPC message the daemon processed

**`send_keys` sends `down down down enter` but the TUI only goes down once** —
This is exactly the case the bridge handles automatically: each escape-sequence key gets a 50 ms gap before the next so TUI apps process them one at a time. If you're still seeing the issue, the target TUI may be discarding keys faster than 20 Hz — increase the gap in `cmd/quil/mcp_tools.go` (`50*time.Millisecond` in `registerSendKeysTool`) or split the call.

**Daemon socket not found / permission denied** —
The bridge talks to `~/.quil/quild.sock` (mode `0600`). If that path doesn't exist, run `quil daemon start`. If permissions are wrong, delete it and restart the daemon.

## Reference

- Bridge entry point: [`cmd/quil/mcp.go`](../cmd/quil/mcp.go)
- Tool implementations: [`cmd/quil/mcp_tools.go`](../cmd/quil/mcp_tools.go)
- Key name mapping: [`cmd/quil/mcp_keys.go`](../cmd/quil/mcp_keys.go)
- Redaction + logging: [`cmd/quil/mcp_log.go`](../cmd/quil/mcp_log.go)
- Architecture rationale: [Architecture / ADR-?? MCP](architecture.md)
