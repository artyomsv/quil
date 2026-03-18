# Aethel — Product Requirements Document

**The Persistent Workflow Orchestrator for AI-Native Development**

| Field | Value |
|---|---|
| Author | Artjoms Stukans |
| Date | 2026-03-09 |
| Status | Draft |
| Version | 1.0 |

---

## 1. Problem Statement

Agentic developers run complex multi-tool workflows — AI assistants (Claude Code, Cursor), webhook listeners, build watchers, SSH tunnels — across multiple terminal sessions. Every reboot, crash, or context switch destroys this carefully assembled environment. Re-opening tabs, re-attaching sessions, and re-typing resume commands is a daily tax that breaks flow and wastes time.

**Existing tools fail because:**

- **tmux/screen:** Persist shells but have no concept of "projects" or typed sessions. No automatic resume of AI tools. No session-ID extraction.
- **Terminal emulators (WezTerm, Windows Terminal):** Great rendering, zero persistence across reboots.
- **IDE terminals:** Tied to a single editor. Can't orchestrate standalone CLI tools.

**Success criteria:** Time from boot to "fully productive" drops from 10-15 minutes of manual setup to under 30 seconds.

## 2. User Persona: The Agentic Developer

A developer who runs 5-10 terminal sessions per project: 2-3 Claude Code sessions, a webhook listener, a build watcher, maybe an SSH tunnel. They want to type `aethel` after a reboot and have their entire workspace snap back — AI conversations resumed, webhooks re-connected, builds re-watching.

**Primary audience:** Developers who work with AI coding assistants and run multi-tool terminal workflows daily.

**Secondary audience:** Any developer managing persistent terminal environments (DevOps, SREs, backend engineers with long-running processes).

## 3. Tech Stack

| Component | Technology | Rationale |
|---|---|---|
| Language | Go (Golang) | High concurrency, easy cross-platform binaries, no runtime dependency |
| TUI Framework | Bubble Tea | Rich interactive TUI, active ecosystem, Go-native |
| PTY Management | `creack/pty` (Unix), ConPTY (Windows) | Cross-platform terminal session management |
| Syntax Highlighting | Chroma | Go-native, extensive language support |
| IPC | Unix Domain Sockets (Linux/macOS), Named Pipes (Windows) | Fast local communication, OS-native |
| Config Format | TOML | Human-readable, well-supported in Go |
| State Storage | JSON (config/state), SQLite (buffers/history) | Readable config + queryable historical data |

## 4. Architecture Overview

### 4.1 Client-Daemon Model

```
┌─────────────────────────────────────────────┐
│  aethel (TUI Client)                        │
│  ┌─────────┬─────────┬─────────┐            │
│  │ Tab 1   │ Tab 2   │ Tab 3   │            │
│  ├─────────┴─────────┴─────────┤            │
│  │  Pane Layout (splits)       │            │
│  │  ┌──────────┬──────────┐    │            │
│  │  │ AI Pane  │ Build    │    │            │
│  │  │          │ Pane     │    │            │
│  │  ├──────────┴──────────┤    │            │
│  │  │ Webhook Pane        │    │            │
│  │  └─────────────────────┘    │            │
│  └─────────────────────────────┘            │
│         ▲                                    │
│         │ IPC (Unix Socket / Named Pipe)     │
│         ▼                                    │
│  aetheld (Daemon)                            │
│  ┌─────────────────────────────┐            │
│  │ Session Manager             │            │
│  │  ├─ PTY Pool                │            │
│  │  ├─ State Persistence       │            │
│  │  ├─ Resume Engine           │            │
│  │  ├─ Plugin Registry         │            │
│  │  └─ Ghost Buffer Cache      │            │
│  └─────────────────────────────┘            │
└─────────────────────────────────────────────┘
```

### 4.2 Component Responsibilities

| Component | Responsibility |
|---|---|
| `aetheld` (Daemon) | Manages PTY sessions, persists state, runs scrapers, handles process lifecycle. Starts on boot or on first `aethel` invocation. |
| `aethel` (Client) | Bubble Tea TUI. Renders panes, handles input, manages layout. Connects to daemon via IPC. Multiple clients can attach simultaneously. |
| **Session Manager** | Creates/destroys PTY sessions. Assigns pane types. Routes I/O between client and PTYs. |
| **State Persistence** | Snapshots tabs, panes, layout, and metadata to `~/.aethel/`. Runs on a configurable interval + on every structural change. |
| **Resume Engine** | Regex scrapers that watch PTY output. Stores extracted tokens. Executes resume templates on re-hydration. |
| **Plugin Registry** | Loads pane type definitions from `~/.aethel/plugins/`. Each plugin defines: display behavior, scraper patterns, resume templates, status indicators. |
| **Ghost Buffer** | Caches last N lines of each pane's output to SQLite. Renders immediately on reconnect while shells re-initialize. |

### 4.3 IPC Protocol

Length-prefixed JSON messages over Unix domain sockets (Linux/macOS) or Named Pipes (Windows). Simple, debuggable, sufficient for local IPC.

**Message format:**

```
[4 bytes: uint32 big-endian length][JSON payload]
```

**Message categories:**

| Category | Direction | Examples |
|---|---|---|
| Session control | Client → Daemon | `CreatePane`, `DestroyPane`, `ResizePane`, `SetPaneType` |
| Input forwarding | Client → Daemon | `PaneInput` (keystrokes routed to specific PTY) |
| Output streaming | Daemon → Client | `PaneOutput` (PTY output bytes for rendering) |
| State sync | Daemon → Client | `WorkspaceState` (full state on attach), `StateUpdate` (incremental) |
| Lifecycle | Bidirectional | `Attach`, `Detach`, `Shutdown`, `Heartbeat` |

### 4.4 Storage Layout

```
~/.aethel/
├── config.toml              # User configuration
├── state/
│   ├── workspace.json       # Tabs, panes, layout, metadata
│   └── workspace.json.bak   # Previous snapshot (rollback)
├── data/
│   └── aethel.db            # SQLite: ghost buffers, token history, session logs
├── plugins/
│   ├── ai.toml              # Built-in plugin
│   ├── build.toml
│   ├── infrastructure.toml
│   ├── webhook.toml
│   └── stripe-webhook.toml  # User-defined plugin example
├── logs/
│   └── aetheld.log
└── secrets/
    └── tokens.enc           # Encrypted scraped tokens
```

**Storage split rationale:**

| Layer | Format | What | Why |
|---|---|---|---|
| Configuration | TOML | `config.toml`, plugin definitions | Human-readable, hand-editable, git-friendly |
| Workspace state | JSON | `workspace.json` | Source of truth for layout/structure. Human-inspectable for debugging. Atomic writes via temp+rename for crash safety. |
| Volatile data | SQLite | Ghost buffers, token history, session logs | High-write frequency, queryable, rebuildable cache. Not the source of truth — can be deleted and regenerated. |

### 4.5 Data Models

#### Workspace State (`workspace.json`)

```json
{
  "version": 1,
  "last_modified": "2026-03-09T14:30:00Z",
  "active_tab": "tab-001",
  "tabs": [
    {
      "id": "tab-001",
      "name": "Claude Sessions",
      "dock": "top",
      "layout": {
        "type": "hsplit",
        "ratio": 0.5,
        "children": [
          { "type": "pane", "pane_id": "pane-001" },
          { "type": "pane", "pane_id": "pane-002" }
        ]
      }
    }
  ],
  "panes": [
    {
      "id": "pane-001",
      "name": "Claude — feature-auth",
      "plugin": "ai",
      "cwd": "/home/user/projects/myapp",
      "env": {},
      "metadata": {
        "SessionID": "conv-a1b2c3d4"
      }
    }
  ]
}
```

#### SQLite Schema

```sql
-- Ghost buffer: last N lines per pane
CREATE TABLE ghost_buffer (
  pane_id   TEXT NOT NULL,
  line_num  INTEGER NOT NULL,
  content   TEXT NOT NULL,
  timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (pane_id, line_num)
);

-- Scraped tokens with history
CREATE TABLE scraped_tokens (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  pane_id   TEXT NOT NULL,
  key       TEXT NOT NULL,       -- e.g., "SessionID"
  value     TEXT NOT NULL,
  scraped_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_tokens_pane ON scraped_tokens(pane_id, key);

-- Session event log for debugging
CREATE TABLE session_log (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  pane_id   TEXT,
  event     TEXT NOT NULL,       -- e.g., "created", "resumed", "crashed", "scraped_token"
  detail    TEXT,
  timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_session_log_pane ON session_log(pane_id);
```

### 4.6 Configuration (`config.toml`)

```toml
[daemon]
snapshot_interval = "30s"          # How often to persist workspace state
auto_start = true                  # Start daemon on first client attach

[ghost_buffer]
max_lines = 500                    # Lines cached per pane
dimmed = true                      # Visual distinction for ghost content

[logging]
level = "info"                     # debug | info | warn | error
max_size_mb = 10
max_files = 3

[security]
encrypt_tokens = true              # Encrypt scraped tokens at rest
redact_secrets = true              # Redact known secret patterns in logs

[ui]
tab_dock = "top"                   # top | bottom | left | right
theme = "default"                  # TUI color theme

[keybindings]
split_horizontal = "ctrl+shift+h"
split_vertical = "ctrl+shift+v"
next_pane = "ctrl+tab"
prev_pane = "ctrl+shift+tab"
new_tab = "ctrl+t"
close_pane = "ctrl+w"
json_transform = "ctrl+j"
quick_actions = "ctrl+a"
```

## 5. Functional Requirements

### FR-1: Session & PTY Management

| ID | Requirement |
|---|---|
| FR-1.1 | Daemon creates and manages PTY sessions (Linux/macOS via `creack/pty`, Windows via ConPTY). |
| FR-1.2 | Multiple clients can attach to the same daemon simultaneously (read-only observers or active controllers). |
| FR-1.3 | Daemon auto-starts on first `aethel` invocation if not already running. |
| FR-1.4 | Daemon runs as a background process; survives client disconnection. |
| FR-1.5 | Graceful shutdown — daemon sends SIGHUP to child processes, waits for clean exit, then persists final state. |
| FR-1.6 | Pane resize events propagate to the underlying PTY (SIGWINCH on Unix, ConPTY resize on Windows). |

### FR-2: State Persistence

| ID | Requirement |
|---|---|
| FR-2.1 | Daemon snapshots full workspace state (tabs, panes, layout, working directories, pane types, metadata) to `~/.aethel/state/workspace.json`. |
| FR-2.2 | Snapshots trigger on: structural changes (tab/pane create/delete/move), configurable interval (default 30s), and clean shutdown. |
| FR-2.3 | On startup, daemon reads last snapshot and re-hydrates the workspace. |
| FR-2.4 | State format is human-readable JSON for debugging and manual recovery. |
| FR-2.5 | Atomic writes — write to `.tmp`, then `os.Rename` for crash safety. Previous snapshot kept as `.bak` for rollback. |

### FR-3: Ghost Buffer

| ID | Requirement |
|---|---|
| FR-3.1 | Daemon continuously caches the last 500 lines (configurable) of each pane's output to SQLite. |
| FR-3.2 | On client attach/re-attach, ghost buffer renders immediately — before the shell has produced any new output. |
| FR-3.3 | Ghost buffer content is visually distinguished (dimmed or labeled) until live output replaces it. |
| FR-3.4 | Ghost buffer survives daemon restart — stored in SQLite on disk, not in-memory only. |

### FR-4: Abstract Resume Engine

| ID | Requirement |
|---|---|
| FR-4.1 | Each pane type defines zero or more regex scraper patterns that watch PTY output for tokens (e.g., session IDs). Named capture groups become template variables. |
| FR-4.2 | Extracted tokens are stored in the pane's metadata within the state snapshot and in SQLite for history. Only the most recently matched value per named group is used for resume. |
| FR-4.3 | Each pane type defines a resume command template (Go `text/template` syntax) that can reference scraped tokens. |
| FR-4.4 | On re-hydration, daemon executes the resume command instead of a bare shell. If no resume command is defined or required tokens are missing, a standard shell opens in the pane's last working directory. |
| FR-4.5 | Scraper runs asynchronously — never blocks PTY I/O. Token matches are batched and persisted periodically. |

### FR-5: Plugin System (Pane Types)

| ID | Requirement |
|---|---|
| FR-5.1 | Pane types are defined as plugin configs in `~/.aethel/plugins/<name>.toml`. |
| FR-5.2 | A plugin definition includes: command config, persistence/resume strategy, form fields for instance creation, error handlers, and display settings. |
| FR-5.3 | Aethel ships with 4 built-in plugins: `terminal` (Go built-in), `claude-code`, `ssh`, `stripe` (embedded TOML defaults). |
| FR-5.4 | Users can create custom plugins without recompiling Aethel. |
| FR-5.5 | Plugin hot-reload via F1 → Plugins → Reload. Active panes using a modified plugin pick up new scraper/error handler rules on next output. |
| FR-5.6 | Plugin validation — daemon validates plugin TOML on load and logs clear errors for malformed definitions. Invalid plugins are skipped, not fatal. |

> **Full plugin configuration reference:** See [`docs/plugin-reference.md`](docs/plugin-reference.md) for complete field-by-field documentation with examples.

#### Plugin Definition Schema (Implemented)

```toml
# ~/.aethel/plugins/<name>.toml

[plugin]
name = "string"              # Required — unique identifier
display_name = "string"      # Optional — defaults to name
category = "string"          # Optional — "terminal"|"ai"|"tools"|"remote" (default: "tools")
description = "string"       # Optional — one-line description

[command]
cmd = "string"               # Required — binary name (resolved via PATH)
args = ["string"]            # Optional — default arguments
env = ["KEY=VALUE"]          # Optional — environment variables
detect = "string"            # Optional — command to check if installed (default: cmd)
arg_template = ["string"]    # Optional — {placeholder} expansion from form fields

[[command.form_fields]]      # Optional — instance creation form
name = "string"              # Required — placeholder key
label = "string"             # Optional — display label
required = true              # Optional — form validation (default: false)
default = "string"           # Optional — pre-filled value

[persistence]
strategy = "string"          # Optional — "none"|"cwd_only"|"rerun"|"preassign_id"|"session_scrape"
ghost_buffer = true          # Optional — save/replay PTY output (default: true)
start_args = ["string"]      # Optional — template args for fresh start
resume_args = ["string"]     # Optional — template args for restoration

[[persistence.scrape]]       # Optional — extract state from PTY output
name = "string"              # Required — state key
pattern = 'regex'            # Required — regex with one capture group

[display]
border_color = "string"      # Optional — Lipgloss color
dialog_width = 60            # Optional — form dialog width (default: 50)

[[instances]]                # Optional — pre-configured variants
name = "string"              # Required — identifier
display_name = "string"      # Optional — label
args = ["string"]            # Optional — instance-specific args

[[error_handlers]]           # Optional — error pattern matching
pattern = 'regex'            # Required — matched against PTY output
title = "string"             # Optional — dialog title
message = "string"           # Optional — dialog body (supports {host}, {user}, {port})
action = "dialog"            # Optional — "dialog"|"log" (default: "log")
```

#### Built-in Plugin: Claude Code (AI)

```toml
[plugin]
name = "claude-code"
display_name = "Claude Code"
category = "ai"
description = "AI coding assistant"

[command]
cmd = "claude"
detect = "claude --version"

[persistence]
strategy = "preassign_id"
ghost_buffer = false
start_args = ["--session-id", "{session_id}"]
resume_args = ["--resume", "{session_id}"]

[[error_handlers]]
pattern = '(?i)error.*API key not found|ANTHROPIC_API_KEY.*not set'
title = "API Key Missing"
message = "Set ANTHROPIC_API_KEY in your environment or run 'claude auth'."
action = "dialog"
```

#### Built-in Plugin: SSH (Remote)

```toml
[plugin]
name = "ssh"
display_name = "SSH"
category = "remote"
description = "Remote SSH connection"

[command]
cmd = "ssh"
detect = "ssh -V"
arg_template = ["-p", "{port}", "{user}@{host}"]

[[command.form_fields]]
name = "name"
label = "Name"
required = true

[[command.form_fields]]
name = "host"
label = "Host"
required = true

[[command.form_fields]]
name = "user"
label = "Username"
required = true

[[command.form_fields]]
name = "port"
label = "Port"
default = "22"

[display]
dialog_width = 60

[persistence]
strategy = "rerun"
ghost_buffer = true

[[error_handlers]]
pattern = 'Permission denied \(publickey'
title = "SSH Authentication Failed"
message = "SSH key not configured. Run: ssh-copy-id {user}@{host}"
action = "dialog"

[[error_handlers]]
pattern = "Connection refused|No route to host"
title = "Connection Failed"
message = "Cannot reach {host}. Check that the server is running."
action = "dialog"
```

#### Built-in Plugin: Stripe (Webhook Listener)

```toml
[plugin]
name = "stripe"
display_name = "Stripe"
category = "tools"
description = "Stripe webhook listener"

[command]
cmd = "stripe"
args = ["listen"]
detect = "stripe --version"
arg_template = ["listen", "--forward-to", "{url}"]

[[command.form_fields]]
name = "name"
label = "Name"
required = true

[[command.form_fields]]
name = "url"
label = "Forward URL"
required = true
default = "http://localhost:8080/webhook"

[persistence]
strategy = "rerun"
ghost_buffer = true

[[error_handlers]]
pattern = "not logged in|login required"
title = "Stripe Authentication Required"
message = "Run 'stripe login' in a terminal pane first."
action = "dialog"
```

### FR-6: Layout & UI

| ID | Requirement |
|---|---|
| FR-6.1 | Tabs with configurable dock position (top, bottom, left, right). |
| FR-6.2 | Panes support vertical and horizontal splits, infinitely nested. Split ratios are adjustable via keyboard or mouse. |
| FR-6.3 | Panes can be manually named or auto-named from the running process or plugin config. |
| FR-6.4 | JSON transformer hotkey (`Ctrl+J`) toggles raw/minified/pretty-printed with syntax highlighting (via Chroma). |
| FR-6.5 | Keyboard-driven navigation with configurable keybindings (defined in `config.toml`). |
| FR-6.6 | Active pane visually highlighted with distinct border color. Inactive panes dimmed. |
| FR-6.7 | Pane status line shows plugin-defined format string (e.g., session ID, build status, context). |
| FR-6.8 | Quick actions menu (`Ctrl+A`) shows plugin-defined actions for the active pane. |

### FR-7: CLI Commands

| Command | Description |
|---|---|
| `aethel` | Launch TUI client (auto-starts daemon if needed). |
| `aethel daemon start` | Explicitly start the daemon. |
| `aethel daemon stop` | Gracefully stop the daemon (persists state first). |
| `aethel status` | Show daemon status, active sessions, memory usage. |
| `aethel debug <pane-id>` | Dump pane metadata, scraper state, recent IPC messages. |
| `aethel config init` | Generate default `config.toml` and built-in plugins. |
| `aethel plugin list` | List loaded plugins with status. |
| `aethel plugin validate <file>` | Validate a plugin TOML file. |

## 6. Non-Functional Requirements

### NFR-1: Performance

| Metric | Target |
|---|---|
| Daemon startup | < 500ms cold start |
| Client attach | < 200ms to render ghost buffer |
| Pane switching latency | < 50ms |
| Memory per PTY session | < 10MB (excluding ghost buffer) |
| Ghost buffer disk per pane | < 1MB (500 lines) |
| Max concurrent sessions | 50+ panes without degradation |
| State snapshot write | < 100ms (non-blocking to I/O) |

### NFR-2: Security

| ID | Requirement |
|---|---|
| NFR-2.1 | Daemon IPC socket uses filesystem permissions (owner-only: `0700` for socket directory). Named Pipes on Windows use ACLs restricted to current user. |
| NFR-2.2 | Ghost buffer files (SQLite DB) are stored with `0600` permissions — pane output may contain secrets. |
| NFR-2.3 | Plugin definitions are config only — they cannot execute arbitrary code outside of defined command templates. No shell expansion in scraper patterns. |
| NFR-2.4 | State snapshots must not store scraped tokens in plaintext — use OS keyring integration (or at minimum, file-level encryption) for sensitive tokens. |
| NFR-2.5 | Daemon logs must redact patterns matching known secret formats (API keys, tokens, passwords). |

### NFR-3: Observability

| ID | Requirement |
|---|---|
| NFR-3.1 | Daemon logs to `~/.aethel/logs/aetheld.log` with structured JSON logging. |
| NFR-3.2 | Log levels: `debug`, `info`, `warn`, `error`. Default: `info`. Configurable via `config.toml`. |
| NFR-3.3 | Log rotation: max 10MB per file, 3 files retained. |
| NFR-3.4 | `aethel status` command shows: daemon uptime, active sessions, memory usage, last snapshot time. |
| NFR-3.5 | `aethel debug <pane-id>` dumps pane metadata, scraper state, and last 50 IPC messages for troubleshooting. |

### NFR-4: Platform Support

| Platform | PTY Method | IPC Method |
|---|---|---|
| Linux | `creack/pty` | Unix Domain Socket |
| macOS | `creack/pty` | Unix Domain Socket |
| Windows | ConPTY | Named Pipes |

All three platforms supported from day one. Go's cross-compilation makes this feasible with platform-specific build tags for PTY and IPC layers.

## 7. Milestones

### M1: Foundation — Daemon + Shell + TUI (Done)

> **Goal:** `aethel` launches a daemon, opens a Bubble Tea TUI with one tab and one pane running a shell. Basic split support.

**Deliverables:**

- `aetheld` daemon with PTY management (cross-platform)
- `aethel` client with Bubble Tea UI
- IPC via Unix sockets / Named Pipes
- Tab creation/switching
- Vertical/horizontal pane splits
- Keyboard navigation between panes
- Basic `config.toml` loading
- `aethel daemon start/stop` commands

**Exit criteria:** User can launch `aethel`, create tabs, split panes, and run shell commands on all three platforms.

### M2: Persistence — Reboot-Proof Sessions (Done)

> **Goal:** Close `aethel`, reboot, run `aethel` again — tabs, panes, and layout are restored. Ghost buffers show previous output instantly.

**Deliverables:**

- State snapshotting (JSON) on interval + structural changes
- Atomic writes with `.bak` rollback
- Workspace re-hydration on startup
- Ghost buffer system (file-backed binary snapshots)
- Visual distinction for ghost buffer content (dimmed)
- `aetheld` auto-start on first client invocation
- Daemon graceful shutdown with state persist

**Exit criteria:** Full reboot cycle — close everything, restart OS, run `aethel` — workspace layout and ghost buffer content are restored within 30 seconds.

### M3: Resume Engine — AI Sessions Survive Reboots (Done)

> **Goal:** Claude Code session IDs are automatically scraped and sessions resume on re-hydration.

**Deliverables:**

- Regex scraper framework watching PTY output
- Token extraction and storage (workspace JSON `plugin_state`)
- Resume command template execution on re-hydration
- `preassign_id` strategy for Claude Code (UUID pre-assigned via `--session-id`)
- `session_scrape` strategy for tools that emit session IDs in output
- `rerun` strategy for SSH/Stripe (re-execute same command)
- Fallback to plain shell when resume args can't be resolved

**Exit criteria:** Start a Claude Code session, reboot, run `aethel` — Claude session resumes automatically with the previous conversation context.

### M4: Plugin System + Typed Panes (Done)

> **Goal:** Users can define custom pane types. Ship with 4 built-in plugins.

**Deliverables:**

- Plugin loader from `~/.aethel/plugins/*.toml`
- Plugin validation with clear error messages (strategy, cmd, action, regex)
- Plugin registry with auto-detection (`exec.LookPath`)
- Built-in plugins: Terminal (production), Claude Code (production), SSH (POC), Stripe (POC)
- Error handler pattern matching on PTY output with help dialogs
- Pane creation dialog (`Ctrl+N`) — category, plugin, split direction
- Atomic pane replacement (`ReplacePane`)
- `preassign_id` persistence strategy for UUID-based resume
- Resuming/preparing spinner indicator on pane border
- Window size persistence across restarts (Win32 API / xterm)

**Exit criteria:** User creates a custom plugin TOML, assigns it to a pane, and the plugin launches with correct persistence strategy. Claude Code sessions resume after hard restart.

### M5: Polish — Advanced UI + Developer Experience

> **Goal:** Production-quality UX. JSON transformer, tab docking, observability commands.

**Deliverables:**

- JSON transformer (`Ctrl+J`) with Chroma highlighting
- Tab dock positions (top/bottom/left/right)
- Configurable keybindings
- `aethel status` and `aethel debug` commands
- Structured logging with rotation
- Secret redaction in logs
- Encrypted token storage
- `aethel config init` command

**Exit criteria:** All FR and NFR items implemented. Clean UX across all three platforms. Observability commands provide actionable debugging information.

## 8. Success Metrics

| Metric | Target |
|---|---|
| Time to full workspace after reboot | < 30 seconds |
| AI session resume success rate | > 90% (when tool exposes session IDs) |
| Plugin creation time (new pane type) | < 5 minutes for a simple plugin |
| Crash recovery (daemon restart) | Full workspace restored from last snapshot |
| Cross-platform parity | All FR/NFR items work on Linux, macOS, and Windows |

## 9. Out of Scope (v1)

- Remote/networked session sharing (multi-machine)
- GUI/electron-based client
- Built-in file editor or IDE features
- Package manager / plugin marketplace
- Cloud sync of workspace state
- Mouse-based pane resizing (keyboard only in v1)
- Scripting/macro system beyond plugin TOML definitions

## 10. Glossary

| Term | Definition |
|---|---|
| **Agentic developer** | A developer who works with AI coding assistants (Claude Code, Cursor, Copilot) as a core part of their workflow. |
| **Ghost buffer** | Cached terminal output that renders immediately on reconnect, providing visual context while shells re-initialize. |
| **Pane type / Plugin** | A TOML-based configuration that defines specialized behavior for a pane: scraper patterns, resume templates, display rules, and quick actions. |
| **Resume engine** | The system that watches terminal output for session tokens and uses them to resume CLI tools after a reboot. |
| **Scraper** | A background regex listener that extracts named tokens (e.g., session IDs) from PTY output. |
| **Re-hydration** | The process of restoring a full workspace from a persisted state snapshot, including launching shells and executing resume commands. |
