# Features

A capability-by-capability tour of what Quil does. For configuration knobs, see [Configuration](configuration.md). For keystrokes, see [Keybindings](keybindings.md). For AI integration, see [MCP](mcp.md).

## Table of contents

- [Persistence](#persistence)
  - [Reboot-proof sessions](#reboot-proof-sessions)
  - [Lazy restore](#lazy-restore)
  - [Claude Code session-id rotation](#claude-code-session-id-rotation)
  - [OpenCode session-id tracking](#opencode-session-id-tracking)
  - [AI session resume](#ai-session-resume)
- [Layout & navigation](#layout--navigation)
  - [tmux-style pane splits](#tmux-style-pane-splits)
  - [Spatial pane navigation](#spatial-pane-navigation)
  - [Live CWD tracking](#live-cwd-tracking)
  - [Pane focus mode](#pane-focus-mode)
  - [Tab customization](#tab-customization)
- [Input & clipboard](#input--clipboard)
  - [Mouse & keyboard](#mouse--keyboard)
  - [Text selection & clipboard](#text-selection--clipboard)
  - [Image paste from clipboard](#image-paste-from-clipboard)
  - [Input history (AI panes)](#input-history-ai-panes)
- [Typed panes & plugins](#typed-panes--plugins)
  - [Built-in plugins](#built-in-plugins)
  - [Pane setup dialog](#pane-setup-dialog)
  - [Custom plugins via TOML](#custom-plugins-via-toml)
  - [Lazygit integration](#lazygit-integration)
  - [k9s integration](#k9s-integration)
  - [lazysql integration](#lazysql-integration)
- [Observability](#observability)
  - [Notification center](#notification-center)
  - [Memory reporting](#memory-reporting)
  - [Leveled logger + log viewer](#leveled-logger--log-viewer)
- [Pane notes](#pane-notes)
- [Operations](#operations)
  - [Self-healing daemon](#self-healing-daemon)
  - [Client/daemon version handshake](#clientdaemon-version-handshake)
  - [Cross-platform](#cross-platform)

---

## Persistence

### Reboot-proof sessions

Quil continuously snapshots your workspace — tabs, panes, layouts, working directories, and per-plugin state — to `~/.quil/workspace.json`. On restart, everything restores. **Ghost buffers** replay the last 500 lines from a per-pane binary file at `~/.quil/buffers/<pane-id>.buf` so the screen looks familiar instantly while the shell re-initializes underneath.

- Output replay — every pane has a ring buffer that captures PTY output. Reconnecting clients see prior terminal content immediately.
- Layout persistence — the binary split tree is serialized to JSON and stored in the daemon. Reconnect restores the exact split configuration.
- Centralized snapshot queue debounces 500 ms after structural events and runs a safety-net write every 30 s.

### Lazy restore

On daemon restart, only the **active tab's** panes spawn immediately. All other tabs' panes are **deferred** — their workspace model and scrollback history are loaded from disk instantly, but the child process is not started until you first open that tab (or an MCP tool accesses the pane). This makes restart fast even with many tabs open: you see the saved scrollback right away, and live output resumes seamlessly when the tab is opened.

Mark a pane as **eager** with `Alt+Shift+E` (config key `toggle_eager`) to force it to respawn immediately on every restart, regardless of tab order. Eager panes are marked with `●` in the tab bar. The flag is persisted in `workspace.json`.

### Claude Code session-id rotation

`/clear`, `/resume`, and conversation compaction all rotate Claude Code's session id to a new jsonl file. Quil registers a `SessionStart` hook via `claude --settings '<inline JSON>'` at every spawn (it never modifies `~/.claude/settings.json`) and passes `QUIL_PANE_ID=<paneID>` in the PTY env. The hook script — embedded in the binary, written to `$QUIL_HOME/claudehook/`, reused across spawns — atomically writes the live session id to `$QUIL_HOME/sessions/<paneID>.id` on every rotation. On daemon restart, the resume strategy prefers the hook-recorded id over the original preassigned id.

### OpenCode session-id tracking

OpenCode (opencode.ai) mints a new session id on `/new`, fork, or compaction. Quil registers a small JS plugin via `OPENCODE_CONFIG_CONTENT='{"plugin":["<abs path>"]}'` at every spawn and passes `QUIL_PANE_ID` + `QUIL_HOME` in the PTY env. The plugin — embedded in the binary, written to `$QUIL_HOME/opencodehook/` — hooks opencode's `session.created` / `session.updated` / `session.idle` / `session.compacted` / `session.deleted` events and atomically writes `$QUIL_HOME/sessions/opencode-<paneID>.id`. Quil never writes into `~/.config/opencode/` — `OPENCODE_CONFIG_CONTENT` merges with the user's existing config so their plugins, agents, and modes remain active.

### AI session resume

Each AI pane gets a UUID at creation time. On restart Quil runs `claude --resume <session-id>` (or `opencode --session <id>`) automatically. Works for any AI tool that exposes a session id — Claude Code (production), OpenCode (beta), more to come. Tools without a session id can fall back to regex-scraping the last visible state or replaying a stored command.

---

## Layout & navigation

### tmux-style pane splits

Binary split tree enables arbitrarily nested horizontal and vertical splits. Each internal node has its own direction and ratio. Mouse clicks resolve to the correct pane via spatial hit-testing.

| Action | Binding |
|---|---|
| Split side-by-side | `Alt+Shift+H` |
| Split top/bottom | `Alt+Shift+V` |
| Close active pane | `Ctrl+W` |

### Spatial pane navigation

`Alt+Left` / `Right` / `Up` / `Down` focus the closest neighbour in the chosen direction — directional, not linear, matching tmux's `select-pane -L/R/U/D`. Tie-breaks pick the candidate whose perpendicular center is closest to the active pane (vim/iTerm parity).

`Tab` and `Shift+Tab` are deliberately **not** bound — they fall through to the PTY so shell tab-completion and Claude Code's mode-cycling work naturally.

### Live CWD tracking

Pane borders display the shell's current working directory in real-time. Quil auto-injects OSC 7 hooks into bash, zsh, and PowerShell at spawn time — no manual shell configuration required. Fish emits OSC 7 natively.

The CWD also feeds the new-pane setup dialog (pre-filled from the active pane's tracked CWD) and survives daemon restart.

### Pane focus mode

`Ctrl+E` toggles the active pane full-screen. The layout tree stays intact; other panes keep running but aren't rendered. `* FOCUS *` in the pane top border, `[focus]` in the status bar. Pane navigation is disabled in focus mode. Splitting / closing exit focus automatically.

### Tab customization

| Action | Binding |
|---|---|
| New tab | `Ctrl+T` |
| Rename tab | `F2` |
| Rename pane | `Alt+F2` |
| Close tab | `Alt+W` |
| Cycle tab color | `Alt+C` (8 colours) |
| Switch to tab N | `Alt+1` .. `Alt+9` |

---

## Input & clipboard

### Mouse & keyboard

Full mouse support — click tabs to switch, click panes to focus, scroll wheel for terminal history. Drag panes to select text. All keybindings are configurable via `config.toml`.

### Text selection & clipboard

Select text in terminal panes with `Shift+Arrow` (character), `Ctrl+Shift+Arrow` (word jump), `Ctrl+Alt+Shift+Arrow` (3-word jump), or mouse click+drag. Enter copies the selection to the system clipboard. `Ctrl+V` pastes with bracketed-paste sequences so the receiving shell knows the text came from clipboard.

Platform-native clipboard: Win32 `GetClipboardData` / `SetClipboardData` on Windows, `pbpaste` / `pbcopy` on macOS, `xclip` / `xsel` on Linux.

### Image paste from clipboard

Press any paste key on a screenshot. If the clipboard has no text but contains an image (e.g., from `Win+Shift+S`, Snipping Tool, `Cmd+Shift+4`), Quil:

1. Reads the clipboard image data (Win32 `CF_DIBV5` / `CF_DIB`, decodes 24bpp BI_RGB + 32bpp BI_BITFIELDS)
2. Saves it as `~/.quil/paste/quil-paste-<timestamp>-<rand>.png` with `0o600` permissions
3. Types the absolute path into the active pane

AI tools like Claude Code then read the file via their normal file-reading tools — sidesteps the upstream Claude Code Windows clipboard bug ([anthropics/claude-code#32791](https://github.com/anthropics/claude-code/issues/32791)).

Three paste keys: `Ctrl+V`, `Ctrl+Alt+V`, and `F8`. **`F8` is the recommended Windows trigger** because Windows Terminal captures `Ctrl+V` for its own paste action before the TUI sees it.

### Input history (AI panes)

AI panes produce a lot of output, and the prompt you actually typed scrolls far out of view. Quil records each prompt you submit and lets you pull it back up.

- **`Alt+Shift+I`** opens the input-history modal for the active pane: your past prompts as 3-line previews, newest first.
- **`↑`/`↓`** to navigate, **`Enter`** to open the selected prompt's full text in a read-only viewer (scroll and copy supported), **`Esc`** back to the list, **`Esc`** again back to the pane.
- History **persists across daemon restarts** at `~/.quil/history/<pane-id>.jsonl` (one JSON line per prompt, capped at 64 KiB per entry and ring-trimmed to the last 200), and is deleted when the pane is destroyed.

Capture is **opt-in per pane type**. A plugin enables it with `record_history = true` under `[command]` (see [Plugin reference](plugin-reference.md)); the built-in **Claude Code** plugin sets it. The source of truth is the agent's own `UserPromptSubmit` hook — not keystroke scraping — so multiline prompts, pastes, and edits are captured exactly as submitted. Pane types without the opt-in (terminal, lazygit, k9s, lazysql, …) show "No input history for this pane type." OpenCode support is planned.

---

## Typed panes & plugins

### Built-in plugins

Panes aren't just shells. Press `Ctrl+N` to create a typed pane from 5 built-in plugins:

| Plugin | Category | Resume strategy |
|---|---|---|
| **Terminal** | Built-in shell | Restore working directory |
| **Claude Code** | AI Assistant | UUID-based session resume + `SessionStart` hook for rotations |
| **OpenCode** *(beta)* | AI Assistant | JS plugin records `session.*` events; restore via `--session <id>` |
| **SSH** *(POC)* | Remote | Re-run same command |
| **Stripe** *(POC)* | Tools | Re-run same command |

Each plugin defines its own spawn command, default args, resume strategy, idle pattern detection, and error handlers.

### Pane setup dialog

Plugins that opt in via `prompts_cwd = true` or `[[command.toggles]]` get a setup step in the Ctrl+N flow with:

- A **directory browser** pre-loaded with the active pane's CWD (tracked via OSC 7). Tab/arrows navigate, Enter descends, Backspace goes up, `Ctrl+V` jumps to a pasted path.
- One **checkbox per runtime toggle** declared in the plugin TOML. Toggle args are appended to `InstanceArgs`, persist across daemon restarts, and are off by default. Toggles with the same `group` value behave as mutually-exclusive radio buttons.

The shipped `claude-code` plugin uses both: it asks for the working directory (preserving project-specific `.claude/` context that Claude Code ties to the directory) and offers radio-button toggles for permission mode (`--dangerously-skip-permissions` vs `--enable-auto-mode` vs neither).

### Custom plugins via TOML

Create your own pane types as TOML files in `~/.quil/plugins/` without recompiling. Hot reload happens on save. Plugins define commands, error handlers, idle handlers, persistence strategies, runtime toggles, and pre-configured instances.

See the full [plugin reference](plugin-reference.md) for every field.

### Lazygit integration

- **Lazygit plugin** (Ctrl+N → Tools → Lazygit): opens lazygit as a regular
  pane. The directory step lists git repos found near the active pane's
  directory (the enclosing repo plus one-level subfolders, up to 10) with a
  Browse… escape hatch. Only offered when the `lazygit` binary is installed.
- **Overlay (Alt+G)**: toggles a full-tab lazygit view for the repo resolved
  from the active pane's current directory. Hidden overlays keep running —
  re-show is instant with lazygit's UI state intact. One overlay per tab.
  Overlays are ephemeral: they don't survive a daemon restart (one keypress
  recreates them). Quit lazygit (`q`) and the overlay pane is destroyed
  automatically; the next Alt+G starts fresh.

### k9s integration

- **k9s plugin** (Ctrl+N → Tools → k9s): opens [k9s](https://github.com/derailed/k9s)
  as a regular pane — a Kubernetes cluster TUI. Unlike lazygit, k9s is
  cluster-scoped rather than directory-scoped, so there is no working-directory
  prompt. The setup dialog instead offers a **kube-context picker**: "Default
  context" (your kubeconfig current-context) plus the contexts found in
  `KUBECONFIG` / `~/.kube/config`, and pins the pane to the chosen one via
  `--context`. When `k9s` is not on `PATH` the entry is shown greyed with a
  link to its homepage (rather than hidden), so it stays discoverable.
  Cross-platform (Windows, macOS, Linux).
- **Toggles**: a read-only toggle (`--readonly`) lets the pane browse a cluster
  with all mutating commands disabled, and a start-on-Pods toggle opens k9s
  directly on the pods view.
- **Persistence**: on daemon restart the pane re-runs k9s and reconnects
  (`rerun` strategy; no stale-frame replay).

### lazysql integration

- **lazysql plugin** (Ctrl+N → Tools → lazysql): opens
  [lazysql](https://github.com/jorgerojas26/lazysql) as a regular pane — a
  database TUI for MySQL, PostgreSQL, SQLite, and MSSQL. It opens lazysql's own
  connection manager; you select or save connections there.
- **No Quil-side connection picker — by design.** The only argument lazysql
  accepts is a full connection string (DSN) with embedded credentials, which
  would leak through the process arguments. So Quil never reads lazysql's config
  or injects a connection — credential handling stays inside lazysql (which
  supports `${env:VAR}` substitution to keep passwords out of its config).
- **Toggle**: a read-only toggle (`--read-only`) opens the session with data
  modification disabled.
- **Discoverability & persistence**: greyed in Ctrl+N with a homepage link when
  the `lazysql` binary isn't installed; re-runs on daemon restart (`rerun`
  strategy). Cross-platform (Windows, macOS, Linux).

---

## Observability

### Notification center

A non-modal sidebar (drawn as an overlay on the right edge — panes keep their size, so opening it never makes a running TUI re-wrap its output) surfaces:

- Process exits (any pane)
- OSC 133 command-completion events (shell panes)
- Bell characters (30 s cooldown to avoid storming)
- Smart-idle pattern matches (per-plugin `[[idle_handlers]]` regex)
- **"Pane not accepting input"** — the pane's process stopped reading its stdin (e.g. an AI tool wedged after a context compaction), so the daemon drops the keystrokes instead of letting one stuck pane freeze the app. Recover with `Alt+R` (restart the pane in place — AI sessions resume)
- **Hook-driven events from Claude Code and OpenCode** — structured events forwarded directly from the AI tool (permission requests, "reply ready", session errors, file edits, etc.) instead of guessed from the PTY byte stream. See `[notification.hooks]` in [configuration.md](configuration.md#notificationhooks) for the tier knob.

Hook-driven events flow:

```
hook fires (claude .sh / opencode .js)
  → writes one JSONL line to ~/.quil/events/<paneID>.jsonl
  → daemon polls every 200 ms (rate-limited to 100/2s per pane, coalesced 50 ms per event-type)
  → translated to PaneEvent and routed through the same broadcast pipeline
```

Tier values (per source — Claude and OpenCode are configured independently):

- `default` (the v1 set): Claude `SessionEnd`, `UserPromptSubmit`, `Notification`, `PermissionRequest`, `Stop`, `PreCompact`/`PostCompact`, `SubagentStart/Stop`, `TaskCreated/Completed`; OpenCode `permission.ask`, `experimental.session.compacting`, plus filtered bus events (`session.idle/error/compacted`, `session.status` retry-only, `file.edited` batched 1 s).
- `verbose` (currently identical to `default` — placeholder for future tier-2 events like Claude `PreToolUse`/`PostToolUse`).
- `off` disables forwarding entirely; the legacy PTY-byte idle heuristic kicks back in as the fallback notification surface.

| Action | Binding |
|---|---|
| Toggle sidebar | `Alt+N` (3-state: hidden → visible+unfocused → visible+focused → hidden) |
| Focus sidebar | `F3` |
| Pane back-button (browser-style) | `Alt+Backspace` |
| Mute / unmute active pane | `Alt+M` |

External AI agents can subscribe via MCP — `get_notifications` (non-blocking), `watch_notifications` (blocking, up to 5 min) and `dismiss_notifications` (ack from agent side) replace polling. See [MCP](mcp.md#event-observation).

### Memory reporting

`F1 → Memory` opens a collapsible tab / pane tree showing:

- Go-heap (output ring buffer + ghost snapshot + plugin state) per pane
- PTY child resident memory (OS-reported; not comparable across platforms)
- Notes-editor bytes per pane

The status bar gains a `mem <n>` segment refreshed every 5 s by a daemon-side collector. Two MCP tools — `get_memory_report` (per-tab totals) and `get_pane_memory` (single-pane detail) — expose the layers for external agents.

Cross-platform RSS: `/proc/<pid>/status` on Linux, `ps -o rss=` (batched) on Darwin, `GetProcessMemoryInfo` on Windows.

### Leveled logger + log viewer

`internal/logger` wraps Go's stdlib `slog` and bridges all existing `log.Printf` call sites at info level. Set `[logging] level = "debug"` in `config.toml` to trace clipboard pipeline, per-key handlers, and image-paste decoding step-by-step.

The F1 About menu has three log viewers:

- `View client log` — `~/.quil/quil.log`
- `View daemon log` — `~/.quil/quild.log`
- `View MCP logs` — aggregates per-pane files in `~/.quil/mcp-logs/`, most recently modified first

The viewer is a read-only `TextEditor` (typing / save / paste / cut all gated). `Alt+Up` / `Alt+Down` jump the cursor by `[ui] log_viewer_page_lines` (default 40). Reads are symlink-rejecting via `os.Lstat`.

---

## Pane notes

`Alt+E` opens a plain-text editor alongside the active pane (split ~60/40). Notes are stored one file per pane at `~/.quil/notes/<pane-id>.md` with atomic temp+rename and symlink rejection. Three save safety nets: 30 s debounce, `Ctrl+S` explicit save, flush on exit. Notes survive pane destruction — orphans are kept.

Soft-wrap (opt-in via `TextEditor.SoftWrap`): long logical lines wrap onto the next visual row instead of being hard-truncated with `~`. Selections remain contiguous across wrap boundaries.

`Tab` / `Shift+Tab` while in notes mode cycles keyboard focus between editor (default) and the bound pane.

---

## Operations

### Self-healing daemon

A stuck child process can't take Quil down, and a stuck daemon recovers with one command:

- **`quil restart`** — stop the daemon with bounded escalation (graceful IPC shutdown with a final snapshot → SIGTERM → force-kill, each tier with a timeout so even a deadlocked daemon can't stall it), clean up stale pid/socket files, start fresh, and open the TUI. Prints the target environment first (`production (~/.quil)` vs `dev`) so you can never kill the wrong daemon. `quil daemon restart` / `quil daemon stop` use the same escalation. Tabs and panes respawn from the last snapshot; AI panes resume their sessions.
- **Isolated pane input** — every pane's stdin is written by its own goroutine behind a bounded queue. A process that stops reading input (an AI tool wedged mid-turn) costs you a "Pane not accepting input" sidebar warning for that one pane; everything else stays interactive. `Alt+R` restarts the stuck pane in place.
- **Liveness watchdog** — the daemon's snapshot loop doubles as a health canary. If no snapshot completes for 2 minutes, a full goroutine stack dump is written to `~/.quil/quild.log` (`WATCHDOG:` prefix), so a wedge is a diagnosable bug report instead of a silent freeze. Daemon panics and SIGQUIT dumps land in `~/.quil/quild.stderr.log`.

### Client/daemon version handshake

The TUI handshakes with the running daemon before attaching. If the daemon is older it prompts to gracefully stop and auto-spawn the matching daemon from alongside the TUI binary; if the daemon is newer the TUI refuses to attach and points to the releases page. Eliminates the manual "stop daemon → replace both binaries → restart" upgrade dance. Dev/debug builds skip the check.

### Cross-platform

Linux, macOS, and Windows from day one. PTY management via `creack/pty` (Unix) and ConPTY (Windows). IPC over Unix domain sockets or Named Pipes. All persistence paths use atomic temp+rename so a crash during snapshot leaves the previous state on disk.
