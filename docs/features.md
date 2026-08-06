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
  - [Pane context menu](#pane-context-menu)
  - [Text selection & clipboard](#text-selection--clipboard)
  - [Image paste from clipboard](#image-paste-from-clipboard)
  - [Input history (AI panes)](#input-history-ai-panes)
- [Typed panes & plugins](#typed-panes--plugins)
  - [Built-in plugins](#built-in-plugins)
  - [Pane setup dialog](#pane-setup-dialog)
  - [Spawn a pane in a worktree](#spawn-a-pane-in-a-worktree)
  - [Resume a past session at pane creation](#resume-a-past-session-at-pane-creation)
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
  - [Remote daemon over SSH](#remote-daemon-over-ssh)
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

### Wide canvas (no-resize AI panes)

AI transcripts are immutable hard-wrapped text: whatever width the pane had while the reply streamed is the width that text keeps forever, and every PTY resize makes the tool re-render its tail — the classic source of mixed-width, duplicated-looking transcripts in small panes. Wide-canvas panes (`[display] wide_canvas = true`; claude-code and opencode ship with it) sidestep the whole problem: the tool always renders at full window width, small grid panes show a preview of that wide buffer, and zoom (`Ctrl+E`) switches to the native render instantly — no resize, no repaint, no artifacts. Splits, the notification sidebar, notes mode, and zoom never touch the PTY; only a real window resize does. The preview crops to the left edge by default (clean lines, tmux-style); `Alt+Shift+W` (`toggle_wrap`) switches the active pane to soft-wrap when you want every character visible. In the preview you can type, scroll, and see the cursor; text selection needs the zoomed view (v1).

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

**Drag-resize splits** — click and drag any border between panes to resize; every pane keeps a 10×4 minimum (nested splits included), affected panes highlight while dragging, and child processes see a single resize on release. Note: when a *terminal* pane gets narrower, line content that no longer fits is cut by the console host and is not restored on growing back — the same thing happens when shrinking the whole window (no reflow-on-resize; see `techdebt/3-5-terminal-vt-resize-reflow.md`). AI panes are unaffected (window-sized canvas, apps repaint themselves). If content survival matters more than formatting for a given pane (log tails, watch loops), pick the built-in **Terminal (keeps content on squeeze)** pane type instead — it runs the same shell on the AI-pane-style window-sized canvas, so squeezes never cut content, at the cost of output being formatted for the window width (previewed cropped/soft-wrapped) while the pane is narrow.

### Command palette

Press `Alt+Shift+P` to open a modal, keyboard-first launcher for **everything**: split/close/rename/focus a pane, new/close/rename a tab, jump to any pane or tab across the whole workspace, create a pane, and open Settings, Plugins, Memory, or the log viewers. Type a fragment of the intent (`split`, `restart`, `backend`) and the list filters live by fuzzy score; `↑`/`↓` (or `Ctrl+P`/`Ctrl+N`) move the selection, `Enter` runs it, `Esc` closes.

Entries are grouped under dim section headers — **Go to pane**, **Tabs**, **Projects**, **Pane**, **System** — with navigation first (jumping to a pane or tab is the most common reason to open it), so the organization is obvious at a glance; headers disappear once you start typing. Panes are listed by `tab.pane` index and plugin type so same-name or same-directory panes are easy to tell apart. Every command dispatches into the same handler the keybinding uses — the palette is a launcher, not a second implementation — and each row shows its shortcut, so it teaches the bindings as you go. Rows that don't apply grey out (Input history without `record_history`, Open lazygit without the binary).

The **Projects** section covers the whole feature: a *Switch to* row per project (matched on the name or the host, so `gpu` finds `build@gpu01`), New project, Rename, Destroy — or **Disconnect host** on a remote one, never both — Previous project, *Go to the agent waiting longest*, and the sidebar toggle. Since every row carries its binding, this is also the fastest way to learn the project keymap without opening `F1`.

- **Content search** — as you type, the palette also searches every pane's
  scrollback and lists matching panes in a **Found in panes** section beneath the
  filtered commands (match count + a preview line), so one query narrows commands
  and finds content at once — no separate mode or prefix. Enter on a pane match
  jumps to it. Literal, case-insensitive; covers background and muted panes too.
  It reads each pane's **loaded** output buffer and never wakes a dormant pane, so
  it stays fast across many tabs — but because Quil restores panes lazily, a pane
  you haven't opened yet this session may not appear in results until you visit it.

The default is `Alt+Shift+P` because `Ctrl+Shift+P` (the VS Code key) is intercepted by many terminals' own command palette — Windows Terminal, VS Code's integrated terminal — before Quil sees it. Add it back via `command_palette = "ctrl+shift+p,alt+shift+p"` if your terminal leaves it free. (Phase 2 will add per-plugin/instance quick-create.)

### Pane context menu

Right-click a pane (with no text selection active — a selection still copies, unchanged) or press `Alt+A` (`quick_actions`, active pane) to open a popup with 10 actions: Input history, Enter/Exit focus mode, Open notes, Open lazygit, Rename pane, Mute/Unmute notifications, Mark/Unmark attention, Clear attention, Restart pane… (confirm), Close pane… (confirm). The menu shows the target pane's name as a header, and the target pane gets a blue highlight border while the menu is open. Hovering the mouse highlights the row under the cursor; `↑`/`↓`/`k`/`j` also navigate (disabled rows are skipped), `Enter` or a click executes, `Esc` or a click outside closes, and right-clicking another pane re-targets the menu. Action groups (view actions / pane settings / destructive) are separated by a blank line, keeping Restart/Close visually isolated (the menu falls back to a compact layout on short terminals).

Three rows grey out when unavailable: **Input history** unless the pane's plugin sets `record_history` (Claude Code), **Open lazygit** when the `lazygit` binary isn't installed, and **Clear attention** when the pane carries no mark to clear.

**Mark attention** pins a green border on the pane — the same colour as the "work finished, unseen" mark, but it survives focusing the pane and clears only via **Unmark attention**. It's session-only (not persisted across daemon restarts) and also colours the tab label, including on the active tab when the pinned pane isn't the one currently focused.

**Clear attention** drops every mark the pane carries — the amber "needs you" mark in the sidebar, the green unseen mark, and the pin. The blocked mark is set and cleared by the agent's own hook events, so when a clearing event never arrives (the hook stream stopped, or the prompt was answered somewhere the hooks don't observe) the pane stays flagged, and the project row summarising it stays flagged too. This is the way to dismiss that. It changes display state only — nothing is sent to the daemon, and the pane's next hook event re-derives whatever is actually true, so a pane that really is still parked will mark itself again.

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

- **`Alt+Shift+I`** opens the input-history modal for the active pane: one row per past prompt, newest first. A multi-line prompt is flattened to a single line and truncated to the box, so the list stays scannable however long the prompts were.
- **`↑`/`↓`** to navigate, **`PgUp`/`PgDn`/`Home`/`End`** to move faster; the list scrolls to keep the cursor in view and shows its position (`12-31/200`) whenever there is more than one screenful.
- **`Enter`** opens the selected prompt's full text in a read-only viewer, **`Esc`** back to the list, **`Esc`** again back to the pane. The viewer **soft-wraps** (a pasted paragraph or stack trace is one very long logical line — without wrapping, most of it would be unreachable) and opens at the top. Drag to select, right-click or `Enter` to copy, `Ctrl+A` to select all, mouse wheel to scroll.
- History **persists across daemon restarts** at `~/.quil/history/<pane-id>.jsonl` (one JSON line per prompt, capped at 64 KiB per entry and ring-trimmed to the last 200), and is deleted when the pane is destroyed.

Capture is **opt-in per pane type**. A plugin enables it with `record_history = true` under `[command]` (see [Plugin reference](plugin-reference.md)); the built-in **Claude Code** plugin sets it. The source of truth is the agent's own `UserPromptSubmit` hook — not keystroke scraping — so multiline prompts, pastes, and edits are captured exactly as submitted. Pane types without the opt-in (terminal, lazygit, k9s, lazysql, …) show "No input history for this pane type." OpenCode support is planned.

Turns the harness submits on your behalf are filtered out on write, on read, and on compaction — background-task notifications (`<task-notification>`) and subagent reports (`<agent-message>`) are things the agent said to itself, not prompts you typed. A prompt that merely *mentions* one of those tags is kept: a turn is only dropped when it both starts and ends with those markers and holds nothing else.

Prompt text is sanitized before display, on both sides of the connection — control characters and Unicode format characters (bidi overrides, zero-width spaces) are stripped from the list rows and from the detail view. A prompt is free text you may have pasted into, and neither surface passes through the terminal emulator that makes ordinary pane output safe.

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

- A **recent-locations quick pick** — the last 5 working directories you used are offered as selectable rows (Enter opens there instantly), with a **Browse…** row to drop into the full browser. The list persists across restarts (`~/.quil/recent-cwds.json`) and skips folders that no longer exist, so hopping between projects is one keystroke instead of a re-navigation.
- A **directory browser** pre-loaded with the active pane's CWD (tracked via OSC 7). Tab/arrows navigate, Enter descends, Backspace goes up, `Ctrl+V` jumps to a pasted path.
- One **checkbox per runtime toggle** declared in the plugin TOML. Toggle args are appended to `InstanceArgs`, persist across daemon restarts, and are off by default. Toggles with the same `group` value behave as mutually-exclusive radio buttons.

The shipped `claude-code` plugin uses both: it asks for the working directory (preserving project-specific `.claude/` context that Claude Code ties to the directory) and offers radio-button toggles for permission mode (`--dangerously-skip-permissions` vs `--enable-auto-mode` vs neither).

### Spawn a pane in a worktree

Any plugin that asks for a directory (`prompts_cwd = true`) also gets a **Worktree** field in the setup dialog, scoped to whichever directory you've picked above it. It lists the git worktrees belonging to that directory's repository; picking one spawns the pane there instead of in the directory field's own checkout. This is how an agent, a shell, and lazygit end up parked in the same worktree — and how the sidebar's git row (see [Projects](#projects)) shows that pane's real branch instead of repeating the main checkout's for every pane in the tab.

**Quil does not create worktrees yet.** The field offers only the ones `git worktree list` already reports for that repository — there's no "new branch" flow in this release.

The main checkout never appears as a row: it's the directory the field above already selected, not a worktree to attach *to*. Locked worktrees, and ones whose directory is gone from disk while git still tracks them (labeled `(directory is gone)`), are shown rather than hidden — the cursor can still reach them and the row explains why picking it is refused, instead of a worktree quietly vanishing from the list. Point the directory field at something that isn't a git repository at all, and the Worktree field goes inert — `not a git repository` — rather than disappearing, so a missing field is never mistaken for a bug.

### Resume a past session at pane creation

Claude Code panes can start **inside an earlier conversation** instead of a fresh one. The setup dialog's **Session** field lists the sessions recorded for the folder you selected — newest first, each row showing a relative age and the first prompt you typed in it:

```
> Session:
  > New session
      2h ago   Add resume option to claude pane setup dialog
      1d ago   fix(update): release only our own apply lock
      3d ago   I would like to add more mouse controls. For e…
    2/22  ↑↓ PgUp/PgDn move  Enter select  i details
```

The field stays collapsed to one line until you `Tab` onto it, so creating a normal fresh pane looks and costs exactly what it did before — the listing is only fetched when you actually go looking for it.

When an age and a title are not enough to tell two sessions apart, press **`i`** for details: when the session started, when it was last touched, how many prompts you typed, and — the useful part — **the last prompt you left it on**, which appears nowhere else. `↑`/`↓` move to another session and re-read for it, so you can compare candidates without toggling the panel per row; `i` or `Esc` goes back to the list.

Sessions **already open in another pane** are shown greyed with an `[open in 2.Claude]` marker and cannot be selected: two `claude` processes attached to one transcript would overwrite each other's history. Changing the working directory clears the choice and rescans, since a session from another project is not a meaningful resume target.

Picking a session spawns `claude --resume <id>`; permission-mode and `--chrome` toggles still apply. From then on the pane behaves like any other — including surviving a daemon restart back into the same conversation.

Beats `--resume` inside the pane on two counts: you see richer rows (real titles, ages) without waiting for Claude to boot its own picker, and Quil knows which session the pane is on from the first instant, so restore, input history, and the model/context status segment are all correct immediately.

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
  context" (the kubeconfig's current-context) plus the contexts found in
  `KUBECONFIG` / `~/.kube/config` **on the machine the daemon runs on**, and
  pins the pane to the chosen one via
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

## Projects

A project groups tabs, owns a root directory, and belongs to exactly one daemon. The left sidebar (`Alt+Shift+S`) lists every project with a roll-up of its panes — `⚠` needs you, `◐` running, `✓` finished while you were away — so an agent that finished or got stuck in a project you are not looking at is visible from the one place you are.

Under the active project, each tab's panes carry the same glyphs plus the checkout they sit in: branch, the linked worktree's name, and `↑N`/`↓N` against upstream. Git state is refreshed on a background ticker, cached per checkout so N panes in one repository cost one invocation, and marked stale rather than guessed when a probe does not answer.

A worktree named after its branch — `feat-x` for `feat/x`, the usual convention — is shown as a plain `wt` marker instead, since repeating the branch would cost most of the row. You see the name when it differs from the branch, which is exactly when it tells you something: an agent working in `wt-1` on branch `feat/refactor-sidebar`. Naming the worktree costs no extra git call — git already stores a linked checkout's metadata under that name.

| Key | Action |
|---|---|
| `Alt+Shift+S` | Toggle the sidebar |
| `Alt+Shift+N` | New project |
| `Alt+P` | Fuzzy project picker |
| `Alt+O` | Bounce between the two most recent |
| `Alt+Shift+←/→` | Cycle projects |
| `Alt+Shift+A` | Jump to the oldest pane waiting on you, across every project |
| `Alt+Shift+X` | Remove the active project (destroy locally, disconnect a remote host) |

Right-click a project row for Rename, and either Destroy (local) or Disconnect host (remote).

### Projects on another machine

A project's root directory lives on one machine, so a project belongs to the daemon that holds it. Tick **Remote (ssh)** in the New Project dialog, give a user and host, and press Enter on the Host row: quil dials it and then browses *that* machine's filesystem for the root directory. The host is remembered in `[[destinations]]` and attached at every launch until you disconnect it.

**One host holds one project.** A daemon must have at least one tab and a tab must belong to a project, so a host always arrives already holding one — called `Default`, either created when you attach or migrated from tabs that predate projects. Naming a project on such a host **renames that one** rather than adding a second, so whatever was already running there ends up under the name you chose. The local daemon is unaffected — it holds as many projects as you like.

**A host that already has projects folds them into the one you name.** Press Enter and the form tells you what it is about to do — how many projects the host has, what the result will be called, how many tabs move, and that nothing is closed — and a second Enter does it. Every tab moves onto the surviving project and the emptied records are dropped; no tab, pane or running command is touched. Editing the name in between re-describes rather than acting on what you moved away from. The surviving project keeps its **root directory**: the dialog fills that field in by itself once the directory listing arrives, so it usually holds wherever the daemon starts rather than anywhere you chose — use **Rename** to move one. This is the way to tidy a host connected before v1.48.0, where reconnecting and re-creating "the project that disappeared" left another row behind each time — disconnecting is client-side only, so the remote daemon kept every project and replayed them all on the next connect.

A host that cannot be attached to is **provisioned from the dialog** rather than sending you to a shell. No Quil there at all installs it; a daemon older than your client is upgraded, which stops that daemon and respawns its panes from the saved workspace — commands running in its shells are killed, and the status line says so while it runs. Both are attempted at most once per host per session: a dial that fails the same way straight after is something the install cannot fix, so it is reported instead of retried.

The one case Quil will not fix for you is a remote daemon **newer** than your client. Provisioning pushes your own build, so acting there would downgrade a machine other people may be sharing — the message names the client upgrade instead.

Disconnecting removes the machine from your sidebar and stops nothing on it — the remote daemon keeps every pane alive, and reconnecting restores the same workspace.

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

### Auto-update

The daemon checks GitHub daily for new releases,
downloads and verifies them (sha256) in the background, and stages them
under `~/.quil/update/`. The next `quil` launch applies the update with
one confirmation and restarts the daemon; tabs, layouts, CWDs, notes,
and Claude sessions are preserved via the workspace snapshot. Configure
via `[update]` in `config.toml`; About (F1) has a manual "Update now".

### Remote daemon over SSH

> **BETA.** Phases 1, 2, and most of 3 of [Remote Daemon Attach](roadmap/remote-daemon.md). Usable for real work, with the limits at the end of this section — chiefly that plugin *definitions* still come from your local machine, and that `quil status` and the update controls are blocked in remote mode rather than targeting the wrong host.

`quil --remote gpu01` attaches the TUI to a daemon running on another machine. The panes, tabs, and AI sessions live on that host and keep running there when you close the laptop — the TUI is only a viewer.

```
   your laptop                                  gpu01
┌────────────────┐                     ┌──────────────────────┐
│  quil (TUI)    │   ssh -T            │  quild (daemon)      │
│                │═══════════════════▶ │   ├── pane: claude   │
│  a viewer.     │  "quil --stdio"     │   ├── pane: shell    │
│  holds no      │                     │   └── pane: lazygit  │
│  state.        │  one channel,       │                      │
└────────────────┘  no open port       │  the work lives here │
        ╎                              └──────────────────────┘
        ╎ lid closes, wifi drops, you change network
        ╎
        ▼
  link dies → banner, input frozen, redial with backoff
            → panes never stopped; reattach and carry on
```

**No network port is opened on the remote host.** Quil runs `ssh -T gpu01 "quil --stdio"` and speaks its normal length-prefixed protocol over that single channel, so anything SSH can reach works: a bastion behind `ProxyJump`, a Tailscale or WireGuard address, a box on the public internet. The remote daemon is started on demand if it isn't already running.

The destination string is passed to `ssh` verbatim, so your `~/.ssh/config` keeps working unchanged — `Host` aliases, `ProxyJump`, `ControlMaster` multiplexing, per-host `IdentityFile`, hardware tokens (FIDO2/PKCS#11), and SSH certificates all apply. Quil layers on only two things: bounded timeouts, and a set of options it forces **off** for this connection regardless of your config — agent forwarding, X11 forwarding, port forwarding, and local-command execution. The remote side never needs them, and the daemon protocol is powerful enough (it spawns processes) that reducing what a compromised remote can reach back through is worth the loss of flexibility.

Both ends of the connection's life are bounded, because an unbounded one has nowhere to report to — the dial happens before the TUI starts, so there is no interface to press Ctrl+C in:

| Option | Value | Bounds |
|---|---|---|
| `ConnectTimeout` | 15s | The TCP handshake. Without it, a silently-dropped SYN inherits the OS connect timeout — minutes. |
| `ServerAliveInterval` / `ServerAliveCountMax` | 15s / 3 | An established link going dead. Detected in ~45s. This is the only liveness check; there is no application-layer heartbeat. |

#### When the link drops

A dropped link is a pause, not an ending. Close the lid, lose wifi, switch from
ethernet to a hotspot — the session holds.

An amber bar takes the top row, names the host, counts the attempts, and shows
what `ssh` actually said. Retries back off from half a second to at most thirty.
When the host answers again the panes are reattached with their contents intact;
nothing respawns, because nothing ever stopped.

**Retrying stops when it cannot possibly help.** A key the server rejects, a host
key that changed, an agent that went away — none of these improve by being tried
again, so the banner says so and waits, and `r` retries once you have fixed the
cause. This is worth more than tidiness: every attempt is a full SSH login, and a
laptop left retrying a rejected key overnight can get its own address banned by
the server's brute-force protection. Anything Quil cannot confidently identify as
permanent keeps retrying, because stopping a session that would have recovered is
the worse mistake.

**Keystrokes are dropped while the link is down, not queued.** A key typed at a
dead connection would otherwise be delivered minutes later, into a live agent
session, answering a question that had already moved on. A visible stall is the
lesser failure. `ctrl+q` stays live throughout — it is the only way out of a host
that is not coming back.

An attempt is only reported as restored once the far side has actually answered.
That distinction matters more than it sounds: `ssh` reports success the moment
its own binary starts, long before it has resolved the host or authenticated, so
"the dial worked" is not evidence that anything is there.

Detection rests on `ssh`'s keepalive above (~45s for a link that dies silently);
a link that dies loudly — the host rebooting, the process being killed — is
noticed at once.

These are set on the command line, which OpenSSH resolves before any config file ("first obtained value wins"), so they override a `ConnectTimeout` in your own `ssh_config`. That is deliberate: a bounded, diagnosable failure beats an unbounded hang.

When the connection fails, Quil reports it as a connection failure and prints the exact command to reproduce it by hand (`ssh <host> quil --stdio`, which should print nothing and stay open). It does **not** report it as a version mismatch — an unreachable host and an out-of-date daemon look identical from the client's side unless the transport is asked directly, and conflating them sent users off upgrading binaries that were fine.

#### Installing Quil on the remote

```bash
quil remote setup gpu01
```

Quil downloads the release for the **remote's** platform onto your machine, verifies its checksum there, and pushes it over the SSH connection. The server needs no route to GitHub — which matters, because cluster nodes frequently have none. The version installed matches your TUI by construction, so the two cannot disagree afterwards.

You rarely need to run it yourself. `quil --remote <host>` on a machine that has no Quil offers to install it, and **attaches once it has** — the command you typed asked to attach, so that is what it finishes doing. A version mismatch offers an upgrade the same way. Nothing is installed without an explicit `y`, and the prompt names the host, the exact path, the version, and — for an upgrade — that the remote daemon will be stopped.

Connecting a host from the New Project dialog does the same work without the prompt: Bubble Tea owns the screen and stdin by then, so a `[y/N]` there would land on top of the dialog with no way to answer it. Naming the host in the form is the consent, and the status line reports what is happening — including the daemon restart on an upgrade. It is skipped entirely on a development build, which has no matching release to install; use `quil remote setup <host> --from-dir <path>` for those.

This also solves a problem that is otherwise easy to hit and hard to diagnose. `ssh host quil --stdio` runs a *non-interactive* shell, and on Debian and Ubuntu `~/.bashrc` returns before it reaches any `PATH` line — so a binary in `~/.local/bin` is invisible, and the failure looks exactly like an unreachable host. Setup records the absolute path per destination and uses it as the remote command, so `PATH` never participates. Installs go to `~/.local/bin` and **never use `sudo`**; an upgrade replaces an existing binary in place only where that directory is already writable.

| Remote platform | Supported |
|---|---|
| `linux/amd64`, `linux/arm64` | Yes |
| `darwin/amd64`, `darwin/arm64` | Yes |
| `windows/amd64` | No — see below |

Any local platform can provision any supported remote; a Windows laptop setting up a Linux ARM server is not a special case. The far side needs only `sh`, `uname`, `tar`, and either `sha256sum` or `shasum`. Alpine and other musl distributions work, because releases are built with `CGO_ENABLED=0` and are statically linked.

Windows remotes are excluded for a concrete reason rather than a lack of interest: a running `.exe` cannot be overwritten. Renaming over a running ELF binary works — the process keeps its inode — which is what makes upgrading a live daemon safe on Unix. Windows locks the image file instead. A fresh install would be straightforward; the upgrade path is the hard half, and shipping one without the other would strand you the second time you used it.

`--from-dir <path>` pushes locally built binaries instead of a release. Development builds have no matching release to download, so this is the only path available to them.

Commands that manage a daemon's lifecycle refuse under `--remote` instead of silently acting on the wrong machine:

| Command | Behavior with `--remote` |
|---|---|
| `quil restart` | Refuses — manage the remote daemon over a normal SSH session |
| `quil daemon start\|stop\|restart` | Refuses |
| Upgrade-restart prompt | Reports the version mismatch and exits |
| `quil --remote <host> mcp` | Refuses — the MCP bridge is local-only |

Two setup requirements are worth stating, because both fail in confusing ways. **`quil` must be on the remote's non-interactive `PATH`** — `ssh host quil --stdio` runs a non-interactive shell, which on Debian/Ubuntu returns from `~/.bashrc` before any `PATH` line, so `~/.local/bin` is usually invisible; install to `/usr/local/bin` and check with `ssh <host> command -v quil`. And **`ssh <host> quil --stdio` must print nothing** — its stdout *is* the IPC channel, so a shell banner or MOTD on stdout corrupts the first frame. That command is the fastest way to tell a transport problem from a Quil problem.

#### Current limits (beta)

Phase 1 is the transport, Phase 2 is reconnect, Phase 3 moves the filesystem
dialogs to the server. These are known and scoped, not bugs:

| Limit | Effect |
|---|---|
| Plugin availability can be stale on the server | `Ctrl+N` now greys out what the *server* lacks, but the daemon checks which tools are installed at startup and on plugin reload only — and it is built to run for weeks. Install something on the server mid-session and it stays greyed until the daemon restarts. |
| Plugin *definitions* still come from your machine | Only availability crosses the link. A plugin defined on the server but not locally cannot be offered at all, and the F1 → Plugins editor reads and writes your own `~/.quil/plugins/`. |
| `quil status` refuses under `--remote` | It would report on the local daemon. Use `ssh <host> quil status`. |
| Update controls hidden in remote mode | The banner describes the remote daemon while every apply path writes to local disk, so it is suppressed rather than offered wrongly. |
| Clipboard image paste is local-only | The PNG is written locally and a local path is typed into a remote pane, where it does not resolve. |
| Notes and the log viewer are local | By design — the daemon's own logs are reachable over SSH. |

What Phase 3 already fixed: the working-directory browser, `~` expansion,
relative paths, drive and root listings, and git-repository discovery — both
`Alt+G` and the setup dialog's candidate list — now ask the daemon, so they
describe the machine that actually holds the files. The Claude session list was
always daemon-side. See the [PRD](roadmap/remote-daemon.md) for the remaining
work and the reasoning behind the transport choices.

Because those names, paths and error messages now arrive from a host you may not
control, they are stripped of terminal control sequences before being drawn, and
of the invisible characters that reverse text direction — a folder name cannot
scramble the dialog around it, or read as something other than what it is on the
list you pick a working directory from. The real name is always what gets opened.

### Cross-platform

Linux, macOS, and Windows from day one. PTY management via `creack/pty` (Unix) and ConPTY (Windows). IPC over Unix domain sockets or Named Pipes. All persistence paths use atomic temp+rename so a crash during snapshot leaves the previous state on disk.
