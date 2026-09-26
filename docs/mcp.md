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
- [The 36 tools](#the-36-tools)
  - [Discovery](#discovery)
  - [Reading pane output](#reading-pane-output)
  - [Interacting with panes](#interacting-with-panes)
  - [Pane lifecycle](#pane-lifecycle)
  - [Projects and tabs](#projects-and-tabs)
  - [Remote hosts](#remote-hosts)
  - [Delegating work to another pane](#delegating-work-to-another-pane)
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

You need `quil` on the AI client's `PATH`. If `quil version` works in your shell, you're set. If your AI client doesn't inherit your shell PATH (common on macOS for GUI apps), use the absolute path — e.g., `~/.local/bin/quil`.

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

Restart Claude Desktop. The 🔌 icon in the input bar should show Quil with 36 tools.

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

## The 36 tools

Tools are grouped below by purpose. Every tool returns a `text` content block; many return JSON-formatted payloads.

### Discovery

Start here. Other tools need pane IDs and tab IDs as inputs.

| Tool | Input | Returns |
|---|---|---|
| `list_panes` | `host` (optional) | JSON array of all panes on every connected host: `{id, type, name, cwd, tab_id, project_id, running, preparing_worktree, agent_state, blocked_reason, last_idle_at, host, self}` |
| `list_tabs` | `project_id`, `host` (both optional) | JSON array of all tabs: `{id, name, project_id, pane_count, active, host}` |
| `list_projects` | `host` (optional) | JSON array of projects: `{id, name, root_dir, active, bootstrap, tab_ids, active_tab, host}` |
| `list_hosts` | — | The remote daemons this bridge reaches (`[[destinations]]` of the machine running `quil mcp`) with `connected` and any dial error |
| `list_plugins` | `host` (optional) | Every plugin with `available`, `prompts_cwd`, `sessions` and its `toggles[]` `{name, label, group, default}`, plus `sandbox_available`. Call before `create_pane` with an AI type |
| `list_sessions` | `cwd` (required), `host` | The Claude Code sessions recorded for a directory: `{id, title, modified_ms, in_use_pane_id}` |

**`agent_state`** is the daemon's own replay of an AI pane's hook events: `working`, `blocked` (with `blocked_reason` naming the tool a permission prompt waits on), or `idle`. **Empty means unknown, not idle** — a terminal pane, or an AI pane whose hooks never loaded. **`self`** marks the pane the bridge is running inside (the daemon sets `QUIL_PANE_ID` on every AI pane's child, and the bridge is that child's child), so an orchestrator can tell itself apart from its workers.

### Reading pane output

Two ways to see what's in a pane — pick the right one for the kind of program running there.

| Tool | Input | Returns | Use when |
|---|---|---|---|
| `read_pane_output` | `pane_id` (required), `last_lines` (default 50, max 1000) | ANSI-stripped scrollback text | Shell command output, build logs, test results — anything line-oriented |
| `screenshot_pane` | `pane_id` (required), `width` (default 80), `height` (default 24) | VT-emulated screen text + cursor position | Interactive TUIs (vim, htop, Claude Code) — `read_pane_output` would show raw escape sequences |
| `get_pane_status` | `pane_id` (required) | JSON: `{alive, exit_code, type, cwd, pid, preparing_worktree}` | Check if a process is still running, or what its exit code was |

**`preparing_worktree`** is present when a pane is waiting for `git worktree add` to finish. Such a pane has **no process on purpose** — `alive` is false and `exit_code` is null, which otherwise looks exactly like a pane that crashed. It is a placeholder: the pane you asked for replaces it when the checkout completes, so there is nothing to restart and nothing to read. `restart_pane` refuses it.

### Interacting with panes

| Tool | Input | Returns | Notes |
|---|---|---|---|
| `send_to_pane` | `pane_id` (required), `input` (text), `press_enter` (default true), `paste` (default false) | "Sent N bytes to <pane>", or an ERROR — see below | Use for typing commands. Newline is appended by default so the command executes. `paste=true` delivers the text as a bracketed paste and presses Enter 100 ms later, so a multi-line prompt to an AI pane is not submitted at its first newline. For a prompt to another AI pane prefer [`delegate_task`](#delegating-work-to-another-pane). **Wrap secrets in `<<REDACT>>...<</REDACT>>` markers** — see [Security](#security-redaction-model) |
| `send_keys` | `pane_id` (required), `keys` (array of names or literal text, max 1000) | "Sent N keys to <pane>", or an ERROR — see below | Use for navigating TUIs. Each entry is either a key name (see table below) or literal text. The bridge inserts 50 ms between escape sequences so a TUI app processes each key before the next arrives. |

**Both tools now FAIL rather than reporting a send that went nowhere.** They wait for the daemon to confirm the bytes reached a pane's input queue, and return an error naming the reason when it could not:

- `pane is still waiting for its worktree (<branch>) and has no process yet` — the target is a placeholder (see `preparing_worktree` above).
- `pane has no process: <reason>` — its spawn failed; the pane shows the same reason and offers `Alt+R`.
- `pane input queue is full — its child has stopped reading stdin` — the program is wedged and not reading input.
- `no such pane` — the id is unknown.

Previously these calls answered `"Sent N bytes"` regardless, so input aimed at a pane with no process disappeared silently and an agent waited for output from a command that never ran. Delivery means QUEUED for the pane's writer — that is as much as can be confirmed synchronously, because a child that has stopped reading its stdin must never be allowed to block the daemon.

**Recognised key names** (case-insensitive):

`enter`, `tab`, `escape` / `esc`, `up`, `down`, `left`, `right`, `home`, `end`, `page_up`, `page_down`, `backspace`, `delete`, `space`, `f1` through `f12`, `ctrl+a` through `ctrl+z`. Anything else is treated as literal text.

### Pane lifecycle

New panes start at a sibling pane's dimensions, or the last attached terminal's
size when no sibling has a known size (80×24 only when neither is available).
This also applies to MCP-created panes in hidden tabs. Restarting resets the
TUI's terminal state before it displays the replacement child's output.

| Tool | Input | Returns | Notes |
|---|---|---|---|
| `create_pane` | `tab_id` (optional, default = active), `cwd`, `type` (default `terminal`; also `claude-code`, `opencode`, `codex`, `ssh`, `stripe`), `name`, `toggles[]`, `resume_session_id`, `worktree_branch`, `sandbox_image`, `sandbox_auth`, `host` | JSON: `{pane_id, tab_id, host, error?}` | Spawns a new pane with the same options the Ctrl+N dialog collects — see below. An ERROR (no pane) names the refusal; `error` WITH a `pane_id` means the pane exists but its process failed to start. |
| `rename_pane` | `pane_id`, `name` | `{ok}` | What Alt+F2 does. |
| `restart_pane` | `pane_id` (required) | JSON: `{success, message}` | Kills + respawns with the same plugin, CWD, and instance config. Useful for stuck or crashed panes. AI clients should confirm with the user before calling. |
| `destroy_pane` | `pane_id` (required) | "Destroyed pane <id>" or "Failed to destroy" | If it was the last pane in a tab, a replacement terminal pane is auto-created. AI clients should confirm before calling. |

**`create_pane` options, and where each one goes:**

| Input | Meaning | Validated by |
|---|---|---|
| `toggles` | Plugin toggle NAMES from `list_plugins` — `dangerously_skip_permissions`, `enable_auto_mode`, `chrome` (claude-code); `bypass_approvals_and_sandbox`, `auto_workspace_write`, `search` (codex); `print_logs` (opencode). Resolved to the plugin's own flags by the daemon. An unknown name, or two names from one group (the permission modes), is refused — never silently dropped, because an unattended pane without the permission mode it asked for is a pane stuck on a prompt nobody will answer. | daemon |
| `resume_session_id` | A Claude session from `list_sessions`. Must be a UUID and not held by a live pane; otherwise the pane starts fresh. | daemon |
| `worktree_branch` | Creates a NEW linked worktree on this branch off the repository that contains `cwd` and opens the pane inside it. The repo root is resolved by the daemon from `cwd` — a client-built path is how a worktree ends up nested inside a checkout. Not a repository, a bad branch name, or a branch already checked out elsewhere → ERROR and no pane. The bridge waits up to 130 s for the checkout. | daemon |
| `sandbox_image` / `sandbox_auth` | Runs the pane in a Docker container from the image (`token` or `browser` sign-in for claude-code; empty follows `[sandbox] auth`). Only when `list_plugins` reports `sandbox_available`. A rejected image destroys the pane rather than running it on the host. See [Sandbox panes](sandbox-panes.md). | daemon |
| `instance_name` / `instance_args` | For plugins with saved instances (`ssh`, `stripe`). `instance_args` REPLACE the plugin's own arguments, so the daemon REFUSES them for an AI plugin (`category = "ai"`) — use `toggles` there, which are checked by name. | daemon |

The four "deliberately not exposed to MCP" notes in `internal/ipc/protocol.go` (resume, worktree, sandbox, overlay) are now three: overlay panes stay TUI-only. The other three ride the same validated payload the TUI sends, so an agent gets exactly the refusals the dialog gets.

### Projects and tabs

A project groups tabs and owns a root directory (new tabs open there). Every tab and pane reports its `project_id`.

| Tool | Input | Returns | Notes |
|---|---|---|---|
| `list_projects` | `host` (optional) | `[{id, name, root_dir, active, bootstrap, tab_ids, active_tab, host}]` | `bootstrap` marks the "Default" project the daemon invented because a tab needed a home. |
| `create_project` | `name` (required), `root_dir`, `host` | `{project_id, name, host}` | Opens with one shell tab, like a project made from the TUI. The name is made unique on the daemon if taken. |
| `update_project` | `project_id`, `name` (required), `root_dir` (optional) | `{ok}` | Rename and/or relocate. Renaming a bootstrap project adopts it. |
| `switch_project` | `project_id` | `{ok}` | Brings the project's last active tab into view in the TUI. |
| `destroy_project` | `project_id` | `{ok}` | Destroys every tab and pane under it. **Confirm with the user first.** |
| `create_tab` | `name`, `project_id` (default: active project), `first_pane` (any `create_pane` option), `host` | `{tab_id, pane_id, preparing_worktree?, error?, host}` | Does NOT steal the TUI's focus — an orchestrator opening tabs for workers must not yank the user around; call `switch_tab` when you want it. With `worktree_branch` the returned `pane_id` is a placeholder (no process, `preparing_worktree` set); the `worktree_ready` event names the pane that replaces it. The first pane is validated BEFORE the tab is made, so a refusal (unknown plugin, clashing toggles, unresolvable worktree root) is an ERROR with no tab and no pane. |
| `create_from_template` | `template`, `task`, `cwd`, `branch`, `project_id` (default: active project), `host` | `{tab_id, pane_ids, preparing_worktree?, error?, host}` | **Requires daemon 1.74.0+.** Creates the template's panes in listed order with frozen toggle/model arguments and optional starting prompts. Refuses unknown templates, invalid pane settings, and unusable directories; a first-pane subdirectory is checked against the new checkout and an invalid one rolls back the provisional tab. Does not switch focus. A branch request returns a visible placeholder immediately; use `list_panes` for the completed tab's pane IDs. |
| `rename_tab` | `tab_id`, `name` | `{ok}` | |
| `destroy_tab` | `tab_id` | `{ok}` | Every pane in it. If it was the project's last tab a shell tab is auto-created. **Confirm first.** |

Every mutation answers `{id, ok, error}` — the daemon reports whether it applied, instead of the agent inferring it from the next listing. (The TUI's own sends stay fire-and-forget; only an ID-bearing request gets the answer.) Moving a single tab between projects is not offered — the daemon has no such primitive; `MergeProjects` moves all of a project's tabs.

Templates are documented in [Workspace templates](workspace-templates.md). The template creation tool has its own 1.74.0 version floor; the released project/tab/task tools still accept 1.72.0 daemons.

### Remote hosts

Unscoped lists and `list_hosts` retry down hosts in the background after the
30-second backoff. The current call skips a host still connecting; later calls
include it once recovered, without requiring a named-host request. Successful
reconnection clears stale connection and request errors.

The bridge dials every `[[destinations]]` host of the machine running `quil mcp` — the same hosts the TUI shows in its sidebar — in the background, with the same ssh transport, version gate and hello. The local daemon is always there and needs no host.

| Tool | Input | Returns |
|---|---|---|
| `list_hosts` | — | `{local: true, hosts: [{host, label, connected, daemon_version, error}]}` |

**The remote daemon must be new enough.** The project, tab, plugin-catalog and task tools, and `create_pane` with any dialog option, send request types a daemon older than **1.72.0** does not know — and an old daemon drops an unknown request silently, which would look like a 10 s timeout. The bridge asks each daemon its version when it connects (`daemon_version` in `list_hosts`) and refuses such a call against an older release by name: `list_projects needs quil 1.72.0 or newer on the daemon, and this one runs 1.71.0`. Upgrade the host with `quil remote setup <host>`. The tools every daemon has always answered (`list_panes`, `read_pane_output`, `send_to_pane`, a bare `create_pane`, `watch_notifications`, …) keep working against it. A dev daemon reports no release number and is never refused.

**One failing host does not empty the workspace.** An unscoped list (`list_panes`, `list_tabs`, `list_projects`, `list_tasks`, `get_notifications` with no `host`) skips a remote whose request failed — too old, or a wedged link — returns every other host's entries, and records the failure as `error` on that host in `list_hosts` (`last request failed: …`) until a later request to it succeeds. A call that NAMES a host still fails loudly, and the local daemon's failure always fails the call.

Addressing: every tool that takes an id also takes an optional `host`. The bridge resolves it in this order — an explicit `host` (`"local"` or empty names the local daemon); else the host the id was **discovered on** (every `list_*` and every create files its ids); else local. So `list_panes` once, then `read_pane_output pane_id=…` just works for a remote pane. To CREATE something on a remote (a project, a tab in a project you have not listed yet), pass `host`.

The list tools (`list_panes`, `list_tabs`, `list_projects`, `list_tasks`, `get_notifications`) aggregate across every connected host and stamp each entry with `host`; `watch_notifications` fans out one watcher per host and returns the first event. A host that is down is skipped when NO host was named — that call asked for whatever is reachable — and is re-dialled at most every 30 s. Naming a host that is unknown or unreachable is an ERROR from those same tools, never an empty array: an unavailable workspace must not look like an empty one. `list_hosts` reports a dial still in flight as `error: "connecting"`, and never waits for it. `quil --remote <host>` sessions still refuse to run the bridge — that mode is "drive that one machine", and the bridge would have to run there.

### Delegating work to another pane

Before: pane A "asked" pane B for work by simulating keystrokes and then polling. Nothing tied the request to the `Stop` that eventually answered it, and `hook.claude.Stop` fires while background subagents are still running, so a watcher woke early.

| Tool | Input | Returns | Notes |
|---|---|---|---|
| `delegate_task` | `pane_id` (target), `prompt`, `notify` (default true), `timeout` (seconds, 0 = none), `host` | `{id, from_pane, to_pane, to_pane_name, state, prompt, created_at, host}` | Pastes the prompt (bracketed paste, then Enter 100 ms later — a multi-line prompt is one block) into an AI pane, or types `prompt⏎` into a terminal. `from_pane` is the bridge's own pane. Refuses a placeholder, a pane with no process, an empty prompt, self, or **a pane that already has a live task** (the error names it). |
| `wait_task` | `task_id`, `timeout` (default 60, max 300) | `{…task, timeout}` | Blocks until the task ends. |
| `get_task` | `task_id` | `{…task, result, error, started_at, ended_at, notified}` | `result` is the target's last 30 output lines at completion, ANSI-stripped. |
| `list_tasks` | `pane_id` (requester OR target), `host` | `[…tasks]` | Oldest first. |

**Task states** — `sent` (prompt queued) → `working` (the target's first hook edge) → one of:

- `done` — the target's agent state fell to idle **and stayed there for 2 s**. Not the raw `Stop`: Claude Code resumes on its own when a teammate reports back, and background subagents outlive the main turn. The daemon's per-pane work ledger (the same edges the TUI's spinner uses, subagent ledger included) is what decides.
- `failed` — the target's process exited, or the target pane was destroyed (`error` says which). A crash always beats a completion: an exit ends the task as `failed`, never as `done`.
- `timeout` — you set one and it passed.

**One live task per target pane.** A second `delegate_task` aimed at a pane that already has a task in flight is refused, and the error names the live task. Completion is read off the TARGET's work ledger, and that ledger says nothing about WHICH prompt finished — so two queued prompts would both be marked done by the first settled idle, including the one the agent had not looked at yet. Wait (`wait_task`), or use another pane.

A terminal target is `done` when its shell reports the command finished (OSC 133 `D`, which Quil's shell integration emits only after a command actually ran — never for the startup prompt or a bare Enter). The command is sent with a trailing CR, the byte Enter produces; LF is echoed but not executed by PowerShell under ConPTY.

**Hearing about it.** Three ways, pick by what the requester is doing meanwhile:

1. `wait_task` — blocking; fine when you have nothing else to do.
2. `watch_notifications` / `get_notifications` — a `task_done` event is queued on the TARGET pane with `data.task_id`, `data.state`, `data.from_pane` and the result excerpt.
3. **The notify-back** (`notify`, default on) — when the task ends, the daemon types one line into the REQUESTER's own prompt:

   ```
   [quil task task-3f9a1c2e] pane pane-77d2 (worker) done. Last output: ✓ 42 tests passed. Call get_task with task_id=task-3f9a1c2e for the full result, or read_pane_output on pane-77d2.
   ```

   It is delivered only while the requester is **confirmed idle** (text typed into a working agent is at best queued behind its current work); otherwise it waits for the requester's next settled idle. So an orchestrator can hand out several tasks, carry on, finish its turn, and be woken by each result as it lands. `notified` in `get_task` says whether the line reached the requester. The notify-back needs requester and target on the SAME host — a remote daemon cannot type into a local pane; `wait_task` and `task_done` still work across hosts.

   **It also needs the requester's hooks.** A pane whose `agent_state` is empty is UNKNOWN, not idle — no hook edge has ever been seen for it, and such a pane may be mid-turn — so the notice waits for a real idle edge and is never delivered if none comes. `wait_task` and the `task_done` event need no hooks and always work.

The daemon does not parse the target's reply. The excerpt is raw output; the requester decides what to do with it (read more with `read_pane_output`, follow up with another `delegate_task`).

### TUI cooperation

These steer the live TUI window(s) attached to the daemon — see
[Multi-client sync](features.md#multi-client-sync) for what it means to have
more than one.

| Tool | Input | Returns | Notes |
|---|---|---|---|
| `switch_tab` | `tab_id` (required) | "Switched to tab <id>" | Brings a different tab into view — in every attached window, since the active tab is shared. |
| `set_active_pane` | `pane_id` (required), `client` (optional) | "Set active pane to <id>" | Switches the shared active tab if needed, then focuses the pane in ONE window: the one named by `client`, or, when omitted, whichever window you last typed in. |
| `close_tui` | `client` (optional) | "TUI close signal sent. Daemon continues running." | Closes ONE window — named by `client`, or the one you last typed in — not every attached window. Daemon and all pane processes keep running regardless; reattach by running `quil` again. With no window attached at all, nothing is sent. |
| `list_clients` | `host` (optional) | JSON array: `{client, attached_at, cols, rows, master, last_input_at, role, pid, exe}` | Every attached window (and any other attached client), oldest first. `client` is the id to pass to `set_active_pane`/`close_tui`; `master` marks which one currently sets pane sizes. `attached_at`/`last_input_at` are RFC 3339, and `last_input_at` is absent for a window that has only watched. **Requires daemon 1.80.0+.** |

`client` picks an EXACT window and never falls back to another one — but both tools are fire-and-forget, so a `client` that names no attached window still returns success; the daemon simply has nothing to send it to (logged daemon-side). Use `list_clients` right before to confirm the id is current. Omit `client` to get today's single-window behaviour unchanged — with one window attached, there is only ever one to choose.

### Event observation

Replace polling-with-sleep + screenshot with the blocking watcher.

| Tool | Input | Returns | Notes |
|---|---|---|---|
| `get_notifications` | — | JSON array of pending events: `{pane_id, kind, title, body, timestamp, severity, data}` | Non-blocking. Returns the daemon's event queue without removing entries — use `dismiss_notifications` to ack. Each event's `data.excerpt` carries the last lines of pane output that triggered the event. |
| `watch_notifications` | `pane_ids` (optional, empty = all), `timeout` (seconds, default 60, max 300), `since_timestamp` (Unix ms, optional) | JSON: the first event that fires, or `{timed_out: true}` | **Blocks** up to `timeout` seconds. Use after kicking off a long-running task ("watch the build pane until it finishes"). Replaces sleep+poll patterns. Pass `since_timestamp` (the timestamp of the last event you handled) to catch events fired between your previous action and this call — the daemon returns the oldest queued event newer than the marker without ever registering a watcher. |
| `dismiss_notifications` | `event_id` (optional, empty = dismiss all) | Confirmation string | Acks events the agent has already handled so they don't show up again on the next `get_notifications` call. |

The events fired by the daemon include: process exits (any pane), OSC 133 command completion (shell panes), bell characters (with 30 s cooldown to avoid storming), smart-idle pattern matches based on per-plugin `[[idle_handlers]]` in TOML, the hook events of AI panes (`hook.claude.Stop`, `hook.claude.PermissionRequest`, …), **`agent_idle`** ("Turn finished" — an AI pane's work state fell to idle and stayed there for 2 s, subagents included; the reliable "it is done" signal, unlike the raw `Stop`) and **`task_done`** (a delegated task ended; see above). Each event carries a `data.excerpt` field with the last few stripped lines that triggered it, so an agent can act on the context without a follow-up `read_pane_output` call. Every event also carries `host` when it came from a remote daemon; pass it back to `dismiss_notifications`.

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

**Spin up a worker and hand it a job** —
> Open a Claude Code pane in a fresh worktree `feat/login-form`, skip permissions, and have it implement the login form. Tell me when it's done.

The AI calls `list_plugins` (to confirm the toggle name), `create_pane` with `type="claude-code"`, `worktree_branch="feat/login-form"`, `toggles=["dangerously_skip_permissions"]`, then `delegate_task` on the new pane. It carries on; when the worker's turn settles, the `[quil task …] done` line lands in its own prompt and it reports back.

**Fan out across a remote box** —
> On the gpu host, make a project for ~/work/train, open a codex pane there and start the benchmark.

The AI calls `list_hosts`, `create_project host="gpu"`, `create_tab project_id=… first_pane={type:"codex", toggles:["auto_workspace_write"]}`, then `delegate_task`; it waits with `wait_task` because the notify-back cannot cross hosts.

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
The bridge's request timeout is 10 s (130 s for a `create_pane` / `create_tab` with `worktree_branch`, which waits for the checkout). Long-running operations should use `watch_notifications` or `wait_task` (blocking, configurable timeout up to 300 s) instead of a synchronous tool. If a normal tool consistently times out, check `~/.quil/quild.log` for daemon errors.

**A remote host shows `connected: false`** —
`list_hosts` carries the dial error. The bridge dials with batch ssh (no prompts), so a first-time host key or a passphrase must be accepted once by hand: `quil remote setup <host>` or one manual `ssh`. It retries at most every 30 s; a tool call naming the host retries on the spot after that.

**`delegate_task` says done too early / never** —
`done` is the target's work ledger settling idle for 2 s. Too early means the target's hooks never reported a resume (check `agent_state` in `list_panes`); never means the pane's hooks are not loaded at all (`agent_state` stays empty) — `[notification.hooks]` set to `off` drops every edge. A terminal target needs Quil's shell integration for `command_complete`.

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
- Host router (one connection per `[[destinations]]` host, id→host cache): [`cmd/quil/mcp_hosts.go`](../cmd/quil/mcp_hosts.go)
- Tool implementations: [`cmd/quil/mcp_tools.go`](../cmd/quil/mcp_tools.go), [`mcp_tools_projects.go`](../cmd/quil/mcp_tools_projects.go), [`mcp_tools_tasks.go`](../cmd/quil/mcp_tools_tasks.go)
- Daemon side: [`internal/daemon/create_req.go`](../internal/daemon/create_req.go) (create with dialog options), [`project_req.go`](../internal/daemon/project_req.go), [`workstate.go`](../internal/daemon/workstate.go) (per-pane work ledger, `agent_idle`), [`task.go`](../internal/daemon/task.go) (delegation), [`internal/hookevents/ledger.go`](../internal/hookevents/ledger.go)
- Key name mapping: [`cmd/quil/mcp_keys.go`](../cmd/quil/mcp_keys.go)
- Redaction + logging: [`cmd/quil/mcp_log.go`](../cmd/quil/mcp_log.go)
- Architecture rationale: [Architecture / ADR-?? MCP](architecture.md)
