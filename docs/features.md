# Features

A capability-by-capability tour of what Quil does. For configuration knobs, see [Configuration](configuration.md). For keystrokes, see [Keybindings](keybindings.md). For AI integration, see [MCP](mcp.md).

Quil exposes **35 MCP tools**: agents can manage [projects and tabs](mcp.md#projects-and-tabs), discover and route work across [remote hosts](mcp.md#remote-hosts), and create AI panes with the TUI dialog's options. [`delegate_task`](mcp.md#delegating-work-to-another-pane) tracks pane-to-pane work and can notify the requester after completion, when it is ready to receive input.

## Table of contents

- [Persistence](#persistence)
  - [Reboot-proof sessions](#reboot-proof-sessions)
  - [Lazy restore](#lazy-restore)
  - [Claude Code session-id rotation](#claude-code-session-id-rotation)
  - [OpenCode session-id tracking](#opencode-session-id-tracking)
  - [Codex session-id tracking](#codex-session-id-tracking)
  - [AI session resume](#ai-session-resume)
  - [Wide canvas (no-resize AI panes)](#wide-canvas-no-resize-ai-panes)
- [Layout & navigation](#layout--navigation)
  - [tmux-style pane splits](#tmux-style-pane-splits)
  - [Spatial pane navigation](#spatial-pane-navigation)
  - [Live CWD tracking](#live-cwd-tracking)
  - [Pane focus mode](#pane-focus-mode)
  - [Rearranging a tab's panes](#rearranging-a-tabs-panes)
  - [Tab customization](#tab-customization)
  - [New tab, with the pane you actually want](#new-tab-with-the-pane-you-actually-want)
- [Input & clipboard](#input--clipboard)
  - [Mouse & keyboard](#mouse--keyboard)
  - [Command palette](#command-palette)
  - [Pane context menu](#pane-context-menu)
  - [Text selection & clipboard](#text-selection--clipboard)
  - [Image paste from clipboard](#image-paste-from-clipboard)
  - [Input history (AI panes)](#input-history-ai-panes)
- [Typed panes & plugins](#typed-panes--plugins)
  - [Built-in plugins](#built-in-plugins)
  - [Pane setup dialog](#pane-setup-dialog)
  - [Spawn a pane in a worktree](#spawn-a-pane-in-a-worktree)
  - [Run an AI pane in a Docker sandbox](#run-an-ai-pane-in-a-docker-sandbox)
  - [Resume a past session at pane creation](#resume-a-past-session-at-pane-creation)
  - [Custom plugins via TOML](#custom-plugins-via-toml)
  - [Lazygit integration](#lazygit-integration)
  - [Hunk integration](#hunk-integration)
  - [k9s integration](#k9s-integration)
  - [lazysql integration](#lazysql-integration)
- [Observability](#observability)
  - [Notification center](#notification-center)
  - [Desktop notifications](#desktop-notifications)
  - [Processes and memory](#processes-and-memory)
  - [Leveled logger + log viewer](#leveled-logger--log-viewer)
- [Projects](#projects)
  - [Project groups](#project-groups)
  - [Projects on another machine](#projects-on-another-machine)
- [Pane notes](#pane-notes)
- [Operations](#operations)
  - [Self-healing daemon](#self-healing-daemon)
  - [Client/daemon version handshake](#clientdaemon-version-handshake)
  - [Auto-update](#auto-update)
  - [What's New after an upgrade](#whats-new-after-an-upgrade)
  - [Remote daemon over SSH](#remote-daemon-over-ssh)
  - [Cross-platform](#cross-platform)
- [Workspace templates](#workspace-templates)

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

`/clear`, `/resume`, and conversation compaction all rotate Claude Code's session id to a new jsonl file. Quil registers a `SessionStart` hook at every spawn (it never modifies `~/.claude/settings.json`) and passes `QUIL_PANE_ID=<paneID>` in the PTY env. The hook is the `quild claude-hook` subcommand, not a script: the daemon writes a per-pane settings file `$QUIL_HOME/sessions/<paneID>.settings.json` naming `"<quild>" claude-hook`, and passes `claude --settings <that file>`. The subcommand atomically writes the live session id to `$QUIL_HOME/sessions/<paneID>.id` on every rotation. `$QUIL_HOME/claudehook/` holds only `hook.log`, created lazily on first write. On daemon restart, the resume strategy prefers the hook-recorded id over the original preassigned id.

### Hand-started agents

Start `claude`, `codex` or `opencode` from a terminal pane's shell and Quil
opens the pane as that agent instead, carrying the arguments you typed. The
pane then gets everything a pane created with Ctrl+N gets: hook registration,
session-id tracking through `/clear` and compaction, work-in-progress
indicators, notifications, input history, and resume after a restart.

Nothing is killed to do this. Quil's shell integration defines a function that
shadows the binary, so the daemon learns the command line *before* the binary
starts; the pane is reopened rather than interrupted. When the agent exits
cleanly the pane goes back to being a terminal.

It works at an interactive **bash (4.1+), zsh or PowerShell** prompt. The
command runs exactly as typed — unchanged from before the feature existed — in
every other case:

| Case | Why |
|---|---|
| **fish** | Quil injects no init script for fish, so nothing is armed at all. Fish emits OSC 7 by itself; that is the whole of its integration. |
| **bash 3.2** (the macOS system bash) | `read -N` arrived in 4.1 |
| You already define your own `claude` function | Such wrappers exist to set environment a respawn would drop, so yours wins |
| `command claude`, `env claude`, an absolute path, `npx`, `sudo`, `xargs`, a Makefile, a script | The function is bypassed. `command claude` is the deliberate one-off override |
| `claude &`, `$(claude …)`, `( claude )` | A background job reading the tty would stop on SIGTTIN |
| `claude -p`, `--version`, `--help`, `mcp`, `setup-token`, `codex exec`, `opencode serve` | Not an interactive session |
| A typed `--settings` | It would contend with Quil's own hook settings |
| An agent variable (`CLAUDE_*`, `ANTHROPIC_*`, `CODEX_*`, `OPENAI_*`, `OPENCODE_*`) set in the shell but not in the daemon | The respawn would behave differently — `CLAUDE_CONFIG_DIR` naming another session store is the case that matters |

When conversion is declined for a **Claude** launch, Quil still records which
session the running agent is in, so the pane resumes that conversation after a
restart. It does not attach to the process, and it never can: hooks are
registered at launch, so a running agent produces no work-state or notification
events. Codex and opencode keep no session store a third party can read, so for
those Quil reports the situation and does nothing else.

Configure with `[agents] hand_started` — see
[Configuration](configuration.md).

### OpenCode session-id tracking

OpenCode (opencode.ai) mints a new session id on `/new`, fork, or compaction. Quil registers a small JS plugin via `OPENCODE_CONFIG_CONTENT='{"plugin":["<abs path>"]}'` at every spawn and passes `QUIL_PANE_ID` + `QUIL_HOME` in the PTY env. The plugin — embedded in the binary, written to `$QUIL_HOME/opencodehook/` — hooks opencode's `session.created` / `session.updated` / `session.idle` / `session.compacted` / `session.deleted` events and atomically writes `$QUIL_HOME/sessions/opencode-<paneID>.id`. Quil never writes into `~/.config/opencode/` — `OPENCODE_CONFIG_CONTENT` merges with the user's existing config so their plugins, agents, and modes remain active.

### Codex session-id tracking

Codex (OpenAI's coding agent CLI) mints a new session id on `/new` and reports it through its Claude-compatible `SessionStart` hook. Quil registers `quild codex-hook` per pane with a `-c hooks=…` override that also carries the hook's trust hash — codex runs only trusted hooks, and the hash is computed by Quil, so nothing under `~/.codex` is read or written and no trust prompt appears. The record lands at `$QUIL_HOME/sessions/codex-<paneID>.id`; on restart Quil runs `codex resume <id>`. A pane with no recorded session starts fresh — `resume --last` (codex's most-recent-session lookup) is never used, because on restore it finds the sibling pane that respawned a second earlier. The same hook drives the notification sidebar, the work indicator, the model/context status segment and input history for Codex panes.

### AI session resume

Each AI pane gets a UUID at creation time. On restart Quil runs `claude --resume <session-id>` (or `opencode --session <id>`, or `codex resume <id>`) automatically. Works for any AI tool that exposes a session id — Claude Code (production), OpenCode (beta), Codex, more to come. Tools without a session id can fall back to regex-scraping the last visible state or replaying a stored command.

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

### Rearranging a tab's panes

Panes moved in from other tabs can leave a tab lopsided. Right-click the tab (in the tab bar or the sidebar) and choose **Layout…**:

| Row | Result |
|---|---|
| Even out | Keeps the arrangement, gives every pane the same area |
| Columns / Rows | All panes side by side / stacked, equal sizes |
| Grid | Rows of equal panes, as square as possible (5 panes → 3 + 2) |
| Main + stack | The tab's active pane on the left at half the width, the rest stacked on the right |
| Spiral | Each pane splits the last one, alternating direction, all equal |

The same six are in the command palette (acting on the active tab) and can be bound to keys as `tab.layout_even`, `tab.layout_columns`, `tab.layout_rows`, `tab.layout_grid`, `tab.layout_main` and `tab.layout_spiral` in `bindings.toml` — they ship unbound. Hold `Alt` and drag a pane onto another to place it on the side you drop it on, onto the middle to swap the two, or onto a tab to move it there. Nothing restarts. A layout that would make a pane smaller than 10×4 cells is refused, and the new layout is saved at once. Another Quil window attached to the same daemon keeps its own arrangement of that tab, and may overwrite the saved layout with it; whichever window sent its layout last is the one a restart brings back.

### Tab customization

| Action | Binding |
|---|---|
| New tab | `Ctrl+T` — see below |
| Rename tab | `F2` |
| Rename pane | `Alt+F2` |
| Close tab | `Alt+W` |
| Cycle tab color | `Alt+C` (8 colours) |
| Switch to tab N | `Alt+1` .. `Alt+9` |

### New tab, with the pane you actually want

`Ctrl+T` opens the same picker as `Ctrl+N` and creates the tab around whatever
you choose — Claude Code, OpenCode, lazysql, a terminal — including its setup
step, so a new tab can start in a chosen directory, on a chosen kube context, or
resuming a Claude session. If the plugin offers worktrees, a new tab can open
straight onto a fresh branch.

`Esc` on the first screen cancels: no tab is created. Terminal is the first
category, so `Ctrl+T` `Enter` `Enter` is the quick path to a plain shell tab.

The tab and its pane are created together, so a tab never flickers through a
shell you did not ask for. The one exception is a new branch: the tab opens with
a terminal while `git worktree add` runs, and the pane you asked for replaces it
when the checkout finishes — an agent must never spend those seconds in the main
checkout.

---

## Input & clipboard

### Mouse & keyboard

Full mouse support — click tabs to switch, click panes to focus, scroll wheel for terminal history. Drag panes to select text. Drag a tab (in the tab bar or by its name in the project sidebar) or a project row to reorder it; the item moves once the pointer passes the middle of its neighbour, so the drag never flips back and forth. `Alt+Shift+PgUp`/`PgDn` and `Alt+Shift+Up`/`Down` do the same from the keyboard for tabs and projects. Right-click a tab (in the bar or the sidebar) for a context menu to rename it, pick its color, tidy its layout, or move it to another project on the same machine — see [Mouse: tab context menu](keybindings.md#mouse-tab-context-menu). Hold `Alt` and drag a pane to move it beside another pane, swap the two, or drop it on a tab — see [Rearranging a tab's panes](#rearranging-a-tabs-panes). All keybindings are configurable via `config.toml`.

When there are more tabs than fit, the mouse wheel over the tab bar scrolls the strip left/right instead of switching tabs or reaching the pane beneath it; markers (`«N` / `N»`) show how many tabs are hidden on each side. Clicking a tab keeps the current scroll position; switching some other way (keyboard, the palette, an MCP tool) snaps the bar back to centering on whichever tab is now active.

**Drag-resize splits** — click and drag any border between panes to resize; every pane keeps a 10×4 minimum (nested splits included), affected panes highlight while dragging, and child processes see a single resize on release. Note: when a *terminal* pane gets narrower, line content that no longer fits is cut by the console host and is not restored on growing back — the same thing happens when shrinking the whole window (no reflow-on-resize; see `techdebt/3-5-terminal-vt-resize-reflow.md`). AI panes are unaffected (window-sized canvas, apps repaint themselves). If content survival matters more than formatting for a given pane (log tails, watch loops), pick the built-in **Terminal (keeps content on squeeze)** pane type instead — it runs the same shell on the AI-pane-style window-sized canvas, so squeezes never cut content, at the cost of output being formatted for the window width (previewed cropped/soft-wrapped) while the pane is narrow.

### Command palette

Press `Alt+Shift+P` to open a modal, keyboard-first launcher for **everything**: split/close/rename/focus a pane, new/close/rename a tab, jump to any pane or tab across the whole workspace, create a pane, and open Settings, Plugins, Memory, or the log viewers. Type a fragment of the intent (`split`, `restart`, `backend`) and the list filters live by fuzzy score; `↑`/`↓` (or `Ctrl+P`/`Ctrl+N`) move the selection, `Enter` runs it, `Esc` closes.

Entries are grouped under dim section headers — **Go to pane**, **Tabs**, **Projects**, **Pane**, **System**, **Appearance** — with navigation first (jumping to a pane or tab is the most common reason to open it), so the organization is obvious at a glance; headers disappear once you start typing. Panes are listed by `tab.pane` index and plugin type so same-name or same-directory panes are easy to tell apart. Every command dispatches into the same handler the keybinding uses — the palette is a launcher, not a second implementation — and each row shows its shortcut, so it teaches the bindings as you go. Rows that don't apply grey out (Input history without `record_history`, Open lazygit without the binary).

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

Right-click a pane (with no text selection active — a selection still copies, unchanged) or press `Alt+A` (`quick_actions`, active pane) to open a popup with 13 actions: Input history, Enter/Exit focus mode, Open notes, Open lazygit, Open hunk, Rename pane, Move to tab…, Mute/Unmute notifications, Mark/Unmark for deletion, Mark/Unmark attention, Clear attention, Restart pane… (confirm), Close pane… (confirm). The menu shows the target pane's name as a header, and the target pane gets a blue highlight border while the menu is open. Hovering the mouse highlights the row under the cursor; `↑`/`↓`/`k`/`j` also navigate (disabled rows are skipped), `Enter` or a click executes, `Esc` or a click outside closes, and right-clicking another pane re-targets the menu. Action groups (view actions / pane settings / destructive) are separated by a blank line, keeping Restart/Close visually isolated (the menu falls back to a compact layout on short terminals).

**Move to tab…** sends the pane, still running, to any other tab on the same machine — its process, output history and CWD are untouched. It lists every other tab as "project / tab" in a small picker; picking one moves the pane there and splits the last pane in that tab to make room, alternating direction — a lone pane is split side by side, the right-hand one of a side-by-side pair top and bottom, and so on — so moved panes spiral in instead of forming thin columns. You stay on the tab you were on, and a tab left with no panes by the move is closed. The row is greyed rather than hidden when there is nowhere to move to (a single-tab workspace, or the pane or its tab mid worktree checkout).

Five rows grey out when unavailable: **Input history** unless the pane's plugin sets `record_history` (Claude Code), **Open lazygit** and **Open hunk** when their binaries aren't installed (each gated on its own), **Move to tab…** when there is nowhere to move the pane to, and **Clear attention** when the pane carries no mark to clear.

**Mark attention** pins a purple `◆` on the pane — deliberately not the green of the automatic "work finished, unseen" mark, because only one of the two clears itself. The pin survives focusing the pane and goes away only via **Unmark attention** or **Clear attention**.

It is **persisted**: the mark lives on the daemon alongside the pane's mute setting, so it survives a TUI restart, a daemon restart and a reboot, and reads the same in every client attached to that daemon. A pin you set on Friday is still there on Monday.

It shows in four places at once, all using the same `◆` and the same purple:

| Where | What you see |
|---|---|
| Pane border | Purple instead of the unseen green |
| Tab bar | `◆` before the label, purple background |
| Sidebar pane row | `◆` as the row's glyph, or as a suffix when a more urgent state outranks it |
| Sidebar project row | `◆N` counting the pinned panes in that project |

A pinned tab keeps the `◆` even when something more urgent has claimed its colour — a tab that is both blocked and pinned is amber *and* marked, because those are two facts and a colour can only carry one. In the project row the count is independent of the working / blocked / finished counts beside it, since a pinned pane is usually also doing something.

**Mark for deletion** is the opposite note: a red `⌫` recording that you are *finished* with the pane and it is safe to close. It exists for the pane you deliberately keep alive — a deployment still running, a server you want to test against — so that when you come back to it you can close it on sight instead of reading its scrollback to work out whether it still matters.

It is persisted on the daemon on exactly the same terms as the pin, and shows in the same four places:

| Where | What you see |
|---|---|
| Pane border | Red |
| Tab bar | `⌫` before the label — glyph only, **no colour** |
| Sidebar pane row | `⌫` as the row's glyph, or as a suffix when a live state outranks it |
| Sidebar project row | `⌫N` counting the marked panes in that project |

The tab bar is the one deliberate difference from the pin. Tab colour ranks by urgency — blocked, then pinned, then unseen — and a pane you have already decided to throw away is the least urgent thing in the workspace, so it gets the glyph and nothing else rather than competing with three states that want you to act.

The suffix behaviour matters more here than for the pin: the marked pane is usually the one still *doing* something, so `working` claims the row's glyph for exactly as long as the reason to keep the pane alive lasts. The mark moves to the end of the row in its own red instead of vanishing for that window.

**Mark for deletion and Mark attention are mutually exclusive** — they are opposite claims about the same pane, so setting either clears the other. The daemon enforces it, so every attached client and the snapshot on disk agree. *Unmarking* one leaves the other alone: an unmark is not a claim about the opposite mark, and since the pair can never both be set, a clear that also cleared its opposite could only destroy state you set.

**Clear attention** drops every mark the pane carries — the amber "needs you" mark in the sidebar, the green unseen mark, and the pin. The blocked mark is set and cleared by the agent's own hook events, so when a clearing event never arrives (the hook stream stopped, or the prompt was answered somewhere the hooks don't observe) the pane stays flagged, and the project row summarising it stays flagged too. This is the way to dismiss that. The first two are display state only — nothing about them is sent to the daemon, and the pane's next hook event re-derives whatever is actually true, so a pane that really is still parked will mark itself again. The pin is the exception: it is stored on the daemon, so clearing it is sent there too and stays cleared. The deletion mark is deliberately **not** in this row's scope — it is a different vocabulary with its own **Unmark for deletion**, and the two can never be set at once anyway.

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

Capture is **opt-in per pane type**. A plugin enables it with `record_history = true` under `[command]` (see [Plugin reference](plugin-reference.md)); the built-in **Claude Code** and **Codex** plugins set it. The source of truth is the agent's own `UserPromptSubmit` hook — not keystroke scraping — so multiline prompts, pastes, and edits are captured exactly as submitted. Pane types without the opt-in (terminal, lazygit, k9s, lazysql, …) show "No input history for this pane type." OpenCode support is planned.

Turns the harness submits on your behalf are filtered out on write, on read, and on compaction — background-task notifications (`<task-notification>`) and subagent reports (`<agent-message>`) are things the agent said to itself, not prompts you typed. A prompt that merely *mentions* one of those tags is kept: a turn is only dropped when it both starts and ends with those markers and holds nothing else.

Prompt text is sanitized before display, on both sides of the connection — control characters and Unicode format characters (bidi overrides, zero-width spaces) are stripped from the list rows and from the detail view. A prompt is free text you may have pasted into, and neither surface passes through the terminal emulator that makes ordinary pane output safe.

---

## Typed panes & plugins

### Built-in plugins

Panes aren't just shells. Press `Ctrl+N` to create a typed pane from 11 built-in plugins — two compiled into the binary, nine written to `~/.quil/plugins/` as editable TOML on first run:

| Plugin | Category | Resume strategy |
|---|---|---|
| **Terminal** | Built-in shell | Restore working directory |
| **Terminal (keeps content on squeeze)** | Built-in shell | Restore working directory; window-sized canvas, so a pane squeeze never cuts content |
| **Claude Code** | AI Assistant | UUID-based session resume + `SessionStart` hook for rotations |
| **OpenCode** *(beta)* | AI Assistant | JS plugin records `session.*` events; restore via `--session <id>` |
| **Codex** | AI Assistant | Claude-compatible hooks registered through a trusted `-c hooks=…` override; restore via `resume <id>` |
| **lazygit** | Tools | Re-run same command — also a per-tab `Alt+G` overlay |
| **hunk** | Tools | Re-run same command — also a per-tab `Alt+D` overlay, sharing lazygit's slot |
| **k9s** | Tools | Re-run same command; kube context picked from your kubeconfig |
| **lazysql** | Tools | Re-run same command; connections stay in lazysql's own manager |
| **SSH** *(POC)* | Remote | Re-run same command |
| **Stripe** *(POC)* | Tools | Re-run same command |

Each plugin defines its own spawn command, default args, resume strategy, idle pattern detection, and error handlers. The ones that wrap an external binary are offered only when that binary is on `PATH`, and greyed with an install link when it is not — checked per machine, so a remote project reports what *that* host has.

### Pane setup dialog

Plugins that opt in via `prompts_cwd = true` or `[[command.toggles]]` get a setup step in the Ctrl+N flow with:

- A **recent-locations quick pick** — the last 5 working directories you used are offered as selectable rows (Enter opens there instantly), with a **Browse…** row to drop into the full browser. The list persists across restarts (`~/.quil/recent-cwds.json`) and skips folders that no longer exist, so hopping between projects is one keystroke instead of a re-navigation.
- A **directory browser** pre-loaded with the active pane's CWD (tracked via OSC 7). Tab/arrows navigate, Enter descends, Backspace goes up, `Ctrl+V` jumps to a pasted path.
- One **checkbox per runtime toggle** declared in the plugin TOML. Toggle args are appended to `InstanceArgs`, persist across daemon restarts, and are off by default. Toggles with the same `group` value behave as mutually-exclusive radio buttons.

The shipped `claude-code` plugin uses both: it asks for the working directory (preserving project-specific `.claude/` context that Claude Code ties to the directory) and offers radio-button toggles for permission mode (`--dangerously-skip-permissions` vs `--enable-auto-mode` vs neither).

### Spawn a pane in a worktree

Any plugin that asks for a directory (`prompts_cwd = true`) also gets a **Worktree** field in the setup dialog, scoped to whichever directory you've picked above it. It lists the git worktrees belonging to that directory's repository; picking one spawns the pane there instead of in the directory field's own checkout. This is how an agent, a shell, and lazygit end up parked in the same worktree — and how the sidebar's git row (see [Projects](#projects)) shows that pane's real branch instead of repeating the main checkout's for every pane in the tab.

**Finding one in a long list:** the worktree the pane you are splitting sits in comes first, tagged `(current)` — a second agent beside the first, on the same checkout, is the usual reason to open this field. The rest follow by their branch's last commit, newest first, so the checkouts you worked in this week sit at the top rather than wherever their folder name falls alphabetically. With the field focused, **type to search**: the list narrows to the worktrees whose branch *or folder* contains the text (case-insensitive — the folder is often the name you know a worktree by, and it can share no word with the branch). Backspace edits, Esc clears the search, and Enter picks the highlighted match and brings the whole list back with your choice marked. While the field is focused `j`/`k` are letters, not cursor keys; Up/Down still move. A row wider than the dialog is cut at the *start* of its path, so the folder name at the end stays readable.

**Creating one:** the row directly under `off` is `+ new branch…` (it is hidden while a search is typed — clear the search first). Enter opens a name field, type the branch, Enter again. Quil runs `git worktree add -b <branch>` and spawns the pane inside the result. The new branch starts at the repository's **default branch** — `origin/HEAD` where it's set and still resolves, otherwise `origin/main`, `origin/master`, `main` or `master`, and failing all of those your current HEAD — not at whatever the main checkout happens to be on, so a worktree created while you're parked on a feature branch doesn't inherit that feature's commits. The branch is created with no upstream, so `git push -u origin <branch>` works the way it does for any new branch. The worktree lands in a **sibling** directory — `<parent>/<repo>-worktrees/<branch>`, with `/` flattened to `-` — so no tool that walks your repo finds a second checkout nested inside it, and no `.gitignore` entry is needed.

Only new branches are offered. Checking out a branch that's already live in another worktree fails at the git level; attaching to *that worktree* is what you actually want, and it's a row above.

If the add fails — the branch exists, the path is occupied, the repo has no commits — **no pane is created**, and git's own message is shown. Quil never falls back to the repository root: a pane on `master` that you believe is isolated is worse than no pane. For the same reason the field is refused when you choose **Replace** rather than a split, since that path closes the old pane before the new one is known to be possible.

**Removing one when you close the pane.** Worktrees are never removed automatically — closing a pane leaves its worktree, and any uncommitted work in it, exactly where it was. The close confirm (`Ctrl+W` for a pane, `Alt+W` for a tab) *offers* to delete it: an unticked `[ ] Also delete its worktree` row, armed with `space`, off every time the dialog opens. Closing a tab lists every worktree in it under one toggle, since a tab closes as a unit.

The row appears only for a worktree **Quil created**, and it names the directory Quil created — not wherever the pane's shell happens to be now. `cd` a worktree pane into a sibling checkout and the offer still refers to its own worktree, because a pane you opened in a worktree you made by hand never gets the offer at all: Quil deletes what Quil created. (A pane restored from a workspace saved before Quil started recording that directory does not get the offer either — there is no way to be sure which checkout it made.)

While the dialog is open it asks the daemon what the worktree holds and shows `clean` or `⚠ 3 uncommitted or ignored files will be lost` under each row. The count includes **ignored** files, not just modified and untracked ones — a `.env` or a `build/` is exactly what a forced removal destroys with no branch to recover it from, so a worktree holding one is never reported as clean. The removal is forced, so all of that goes with the directory; **the branch stays**, along with any commits on it, so nothing you committed is lost and `git branch` still lists it. A worktree that still hosts a pane in another tab is kept and the reason logged — closing one pane never pulls the directory out from under another. If the check can't answer, the row says so rather than reporting `clean`.

**If a worktree goes missing** (you ran `git worktree remove`, or its drive is unmounted), the pane restores *unspawned*, showing which worktree is gone, with `Alt+R` to retry. It does not quietly reopen in the main checkout — for an AI pane that would resume the recorded conversation against the wrong tree.

The main checkout never appears as a row: it's the directory the field above already selected, not a worktree to attach *to*. Locked worktrees, and ones whose directory is gone from disk while git still tracks them (labeled `(directory is gone)`), are shown rather than hidden — the cursor can still reach them and the row explains why picking it is refused, instead of a worktree quietly vanishing from the list. Point the directory field at something that isn't a git repository at all, and the Worktree field goes inert — `not a git repository` — rather than disappearing, so a missing field is never mistaken for a bug.

### Run an AI pane in a Docker sandbox

`--dangerously-skip-permissions` is how an agent gets useful, and it is also how an agent reaches every file you can. Turn on **Run in a Docker container** in the setup dialog and the pane runs inside a container instead: its checkout is bind-mounted in, so its edits and its commits are real, but its filesystem reach stops at that checkout. Available for **Claude Code**, **Codex** and **OpenCode**, and only when the daemon's machine has Docker running linux containers.

**The mount set is the boundary.** Quil passes no `--privileged`, no `--cap-add` and no `--network` flag. The checkout is read-write. The repository's `.git` is mounted *as a mountpoint* — a mountpoint answers `EBUSY` to rename and remove — with `objects`, `hooks`, `config`, `config.worktree`, `modules` and `worktrees` pinned read-only on top. Those files are values host git *executes*: without the pin an agent rewrites `.git/config` with a `core.fsmonitor` and Quil's own git ticker runs it on your machine.

**Your history cannot be destroyed from inside.** New objects go to a store belonging to the pane, with your repository's object store mounted read-only as an alternate, so no commit on any branch can be deleted from the container. Quil copies the new objects across every 30 seconds and again when the pane closes. Branch pointers are deliberately *not* read-only — that would make committing impossible — and are recoverable with `git reflog`.

**Everything else about the pane still works.** A Linux `quild` is mounted read-only into the container and the agent's hooks call it, so a sandboxed pane shows the same spinner while it works, the same green mark when it needs you, the same `Alt+Shift+I` input history, and resumes its session across a daemon restart. Each pane gets its own hook spool and its own agent config directory, so one sandboxed pane cannot read or write another's.

**Signing in differs per agent**, because each vendor offers a different mechanism for containers. Claude Code gets a **Sign in** row in the same dialog: *Token* forwards `CLAUDE_CODE_OAUTH_TOKEN` by name (Docker reads the value, so it never enters a command line or a log) and needs no sign-in at all — a pane with no token runs `claude setup-token` for you, once per machine, behind a single-flight so two panes never open two browser tabs; *Browser* signs in inside the container, once per pane, and is what a pane needs for Fable, Remote Control and claude.ai connectors. Codex needs nothing — Quil copies your host `~/.codex/auth.json` into the pane. OpenCode signs in per container. **Quil never reads, copies, stores or refreshes a Claude credential in any mode.**

**You supply the image; Quil publishes none and pulls none.** `scripts/sandbox-image.sh` builds one locally and then *verifies* it provides a non-root user, the agent binary and `git` — rather than reporting success from a clean build log. `--with codex,opencode` adds the other agents. There is deliberately no default registry name: the official-looking `anthropics/claude-code` on Docker Hub is a security researcher's honeypot containing no Claude Code at all.

Closing the pane removes its container. Full guide, including the prerequisites, both auth flows and a walk through the image recipe: [Sandbox panes](sandbox-panes.md).

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

### Hunk integration

[hunk](https://github.com/modem-dev/hunk) is a review-first diff viewer — built
for reading changes an agent just wrote, which is most of what a Quil workspace
produces.

- **Hunk plugin** (Ctrl+N → Tools → Hunk): opens `hunk diff` as a regular pane,
  with the same git-repo directory step lazygit uses. Only offered when the
  `hunk` binary is installed (`npm i -g hunkdiff`, `brew install hunk`, or
  `mise use -g hunk`).
- **Overlay (Alt+D)**: toggles a full-tab review of the working tree for the
  repo resolved from the active pane's directory — same lifecycle as the
  lazygit overlay above.

**Alt+G and Alt+D share one overlay slot per tab.** Pressing the other tool's
key while an overlay is on screen *swaps* tools rather than opening a second
one, so the outgoing tool's process ends and its UI state is lost. Each key
still hides its own overlay, and each is offered only when its own binary is
installed.

Hunk takes no repository flag — it reviews whatever repository its working
directory sits in — so the directory the pane spawns in is what scopes it.

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

A non-modal sidebar (drawn as an overlay on the right edge — panes keep their size, so opening it never makes a running TUI re-wrap its output) shows a **timeline of work**, newest first, across every project and every destination:

- **Hook-driven events from Claude Code, OpenCode and Codex** — structured events forwarded directly from the AI tool (`Working on: …`, `Reply ready`, permission requests, session errors) instead of guessed from the PTY byte stream. See `[notification.hooks]` in [configuration.md](configuration.md#notificationhooks) for the tier knob.
- Process exits (any pane)
- **"Pane not accepting input"** — the pane's process stopped reading its stdin (e.g. an AI tool wedged after a context compaction), so the daemon drops the keystrokes instead of letting one stuck pane freeze the app. Recover with `Alt+R` (restart the pane in place — AI sessions resume)
- Pane lifecycle: closed, pinned for attention, marked for deletion
- **An MCP agent taking a pane** — one card per pane per 30 s, so you can see which panes an agent is driving
- Worktree ready, when a `git worktree` checkout finishes
- Bell characters (30 s cooldown to avoid storming)
- Off by default: OSC 133 command completions, and smart-idle pattern matches (per-plugin `[[idle_handlers]]` regex)

**Working with the list:**

- **Click a card** to jump to its pane. Each card names the project and tab it will take you to, and a card whose pane has since closed reads `(closed)` and does not jump.
- **Scroll with the mouse wheel**, or `↑`/`↓` once the sidebar is focused (`F3`).
- **Right-click a card** to dismiss it; `d` dismisses the selected one, `D` dismisses all.
- **`a`** reveals every event for a moment, ignoring your filter — for when you are debugging a pane rather than working in it.
- Each card is titled by its **tab** when that tab holds a single pane — the name you gave the work — and by the pane otherwise. Choose which kinds of event appear in **F1 → Settings → Notifications**, or via [`[notification.events]`](configuration.md#notificationevents). Hiding a group never hides it from MCP agents.

Hook-driven events flow:

```
hook fires (quild claude-hook / codex-hook, opencode .js)
  → writes one JSONL line to ~/.quil/events/<paneID>.jsonl
  → daemon polls every 200 ms (rate-limited to 100/2s per pane, coalesced 50 ms per event-type)
  → translated to PaneEvent and routed through the same broadcast pipeline
```

Tier values (per source — Claude, OpenCode and Codex are configured independently):

- `default` (the v1 set): Claude `SessionEnd`, `UserPromptSubmit`, `Notification`, `PermissionRequest`, `Stop`, `StopFailure`, `PreCompact`/`PostCompact`, `SubagentStart/Stop`, `TaskCreated/Completed`, plus a throttled `PreToolUse` heartbeat (see below); OpenCode `permission.ask`, `experimental.session.compacting`, plus filtered bus events (`session.idle/error/compacted`, `session.status` retry-only, `file.edited` batched 1 s); Codex `SessionEnd`, `UserPromptSubmit`, `PermissionRequest`, `Stop`, `PreCompact`/`PostCompact`, `SubagentStart/Stop`, plus the same throttled `PreToolUse` heartbeat.
- `verbose` (currently identical to `default` — placeholder for future tier-2 events such as the full per-tool-call `PreToolUse`/`PostToolUse` stream).

The `default` tier's `PreToolUse` registration is **not** the per-tool-call stream `verbose` is reserved for. It is the work-indicator's only evidence that a turn is running when no user prompt started it — an agent resuming after a teammate reports back — so it must fire for every tool, but it is throttled to at most one spooled event per pane per 15 seconds of silence and never becomes a notification card. `verbose` remains the switch for seeing every tool call.
- `off` disables forwarding entirely; the legacy PTY-byte idle heuristic kicks back in as the fallback notification surface.

| Action | Binding |
|---|---|
| Toggle sidebar | `Alt+N` (3-state: hidden → visible+unfocused → visible+focused → hidden) |
| Focus sidebar | `F3` |
| Pane back-button (browser-style) | `Alt+Backspace` |
| Mute / unmute active pane | `Alt+M` |

External AI agents can subscribe via MCP — `get_notifications` (non-blocking), `watch_notifications` (blocking, up to 5 min) and `dismiss_notifications` (ack from agent side) replace polling. See [MCP](mcp.md#event-observation).

These events also support [pane-to-pane delegation](mcp.md#delegating-work-to-another-pane): settled agent-idle and task-completion events let a requester follow work across its project's tabs. The bridge aggregates events from connected [remote hosts](mcp.md#remote-hosts).

### Desktop notifications

**Windows only.** The sidebar above is a log; this is the alert. When an agent needs you and you are in another window, Windows raises a toast — and clicking it puts Quil on that exact pane.

It fires on the same two states the project sidebar already marks, and no others:

| State | Sidebar | Toast |
|---|---|---|
| Parked waiting on you | `▲` | "▲ Waiting for your input", with the tool name when the hook reported one |
| Turn finished while you were away | `✓` | "✓ Turn finished" |
| Turn in progress | `◐` | — (noise) |
| Attention pinned by hand | `◆` | — (you set it while looking at the screen) |

Behaviour worth knowing:

- **Only for a pane you are not looking at.** Another tab, another project, or another application all count. The single pane that never toasts is the one on screen — the active pane of the active tab of the active project while the terminal has focus. That is the same test the sidebar uses to decide whether a finished turn counts as unseen, so the two never disagree.
- **One toast per pane**, with a short per-pane floor (5 s) against a runaway agent. Six agents finishing at once give six independently clickable toasts, not a storm of duplicates; Windows collapses the overflow into Action Center on its own.
- **Clicking routes precisely** — project, tab and pane, the same jump `Alt+Shift+A` performs.
- **Answering withdraws the toast.** Typing your answer clears it from Action Center, so the surface never keeps claiming attention you already gave. That covers approving a Bash/Edit/Write prompt, which fires no hook at all.
- **Muted panes stay muted.** `Alt+M` suppresses toasts as well as sidebar rows.

Setup is explicit and reversible — nothing is written as a side effect of enabling the config flag:

```bash
quil notify setup           # writes a Start Menu shortcut + a quil:// handler, and prints both
quil notify status          # what is registered, and what the config says
quil notify test            # send one self-labelled canary toast
quil notify setup --remove  # a true inverse
```

Toggle it live at **F1 → Settings → Notifications** (which also holds the `blocked` and `done` switches), or via [`[notification.desktop]`](configuration.md#notificationdesktop). The `Enabled` row reports registration *state* rather than the flag — it reads `on (run notify setup)` when the flag is on but nothing is registered, which is the default on a fresh install.

`quil notify setup` shows a verification toast and reports whether it actually appeared, so you find out immediately rather than the next time an agent blocks.

Clicking a toast can only move your cursor. The `quil://` handler validates a pane id and forwards it to the running TUI over a per-PID named pipe — there is deliberately no path from a registered URI to spawning a pane, sending input, or running a command, since a registered scheme is invokable by any local process. Inline toast buttons ("Approve" / "Deny") are refused on that basis rather than merely deferred.

### Processes and memory

`F1 → Processes` answers two questions on one screen: what is running under your panes and eating the machine, and which quil processes are alive right now.

**Under each pane**, expand a tab and then a pane to see the actual OS process tree — the shell or agent quil started, and everything it went on to spawn — with memory and CPU per process. A build that will not stop, a language server that has doubled in size, an agent's child that outlived it: the thing you would otherwise open a process explorer for.

Press `K` on any process **below** a pane's own shell to stop it, along with everything it started. The pane's own shell or agent is not offered here — that is `Restart pane` — and quil's own processes are never offered at all.

**Quil's own processes** are listed with their version, uptime and PID: the TUI, the daemon, and every MCP bridge. A bridge still running an older binary is flagged, which is how you catch one pinned to a version an in-place upgrade renamed aside.

Also shown per pane: Go-heap (output ring buffer + ghost snapshot + plugin state), PTY child resident memory, and notes-editor bytes.

The status bar keeps its `mem <n>` segment, refreshed every 5 s. Two MCP tools — `get_memory_report` (per-tab totals) and `get_pane_memory` (single-pane detail) — are unchanged.

Everything about processes is read on the **daemon's** machine, which is the one running them — so this works unchanged when the daemon is remote.

Cross-platform RSS: `/proc/<pid>/status` on Linux, `ps -o rss=` (batched) on Darwin, `GetProcessMemoryInfo` on Windows. Process enumeration: `/proc`, `ps`, and `CreateToolhelp32Snapshot` respectively.

**CPU on macOS means something different**, and the dialog says so. Linux and Windows expose a cumulative CPU counter, so the percentage is usage measured across quil's own sample window. macOS has no equivalent reachable without CGo, so the figure there is the kernel's own decaying average. The numbers are not comparable between a Mac and a Linux host.

Percentages are per-core: a process saturating two cores reads 200%, matching `top`. A process quil has not yet sampled twice shows `—` rather than `0%` — an unknown is not an idle.

**Scrollback depth scales to the workspace.** Every pane holds its own terminal emulator whether or not it is visible, so depth multiplies by pane count. With `[ui] scrollback_lines` unset, quil spends a workspace-wide budget instead of a fixed per-pane depth: ten panes or fewer are unchanged, larger workspaces get proportionally less, and a floor keeps every pane usable. A depth you set yourself always wins and is never adjusted; a depth chosen for you is written to the log. Editable at **F1 → Settings → Scrollback lines**, applied on next launch — the terminal emulator cannot release scrollback it has already retained, so depth is fixed when a pane is created.

### Leveled logger + log viewer

`internal/logger` wraps Go's stdlib `slog` and bridges all existing `log.Printf` call sites at info level. Set `[logging] level = "debug"` in `config.toml` to trace clipboard pipeline, per-key handlers, and image-paste decoding step-by-step.

The F1 About menu has three log viewers:

- `View client log` — `~/.quil/quil.log`
- `View daemon log` — `~/.quil/quild.log`
- `View MCP logs` — aggregates per-pane files in `~/.quil/mcp-logs/`, most recently modified first

The viewer is a read-only `TextEditor` (typing / save / paste / cut all gated). `Alt+Up` / `Alt+Down` jump the cursor by `[ui] log_viewer_page_lines` (default 40). Reads are symlink-rejecting via `os.Lstat`.

---

## Projects

A project groups tabs, owns a root directory, and belongs to exactly one daemon. The left sidebar (`Alt+Shift+S`) lists every project with a roll-up of its panes — `▲` needs you, `⠹` running (a spinner, the same one the tab bar and the pane border cycle), `✓` finished while you were away, `◆` pinned by hand, `⌫` marked for deletion by hand — so an agent that finished or got stuck in a project you are not looking at is visible from the one place you are, and so is a project quietly accumulating panes you meant to close. Each count is painted in the same colour its pane rows use, so the roll-up reads as a summary of them rather than as a second notation. The first three rank against each other (a pane parked for input has also finished its turn, and "needs you" outranks "is ready", so it counts once); `◆` and `⌫` are independent of them, because a marked pane is usually also doing something. A tab holding a pane parked on you turns amber in the tab bar, including the tab you are on: the pane waiting may be the one you are not looking at in a split.

Under the active project, each tab gets a numbered heading (`1:name`, matching `Alt+1..9`) and its panes carry the same glyphs plus the checkout they sit in: branch, the linked worktree's name, and `↑N`/`↓N` against upstream. A pane you have pinned with **Mark attention** shows `◆` in purple, which stays until you unmark it and survives a restart — if a more urgent state is showing, the pin moves to the end of the row rather than disappearing, and keeps its own colour there so it never reads as part of the state that outranked it. A pane you marked with **Mark for deletion** shows `⌫` in red on exactly the same terms. Git state is refreshed on a background ticker, cached per checkout so N panes in one repository cost one invocation, and marked stale rather than guessed when a probe does not answer.

The `▲` is hidden on the pane you are currently in — you are looking straight at the prompt — while the tab, the project roll-up and `Alt+Shift+A` all keep counting it. **Answering** the prompt is what drops it everywhere: typing or pasting into the pane clears the mark, because approving a permission prompt tells quil nothing by itself. A glance is not an answer, and neither is a mouse gesture — scroll the pane or drag a selection across it and the mark stays. Switch away without replying and the `▲` is back on the row.

The PANES section scrolls when there are more panes than rows: the wheel moves it, the project list above stays pinned, and `⋯ N above` / `⋯ N below` mark what is off-screen. Reaching a pane by clicking it in the sidebar, by `Alt+Shift+A`, or from the command palette scrolls it into view.

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

Right-click a project row for Rename, Move to group… (see [Project groups](#project-groups)), and either Destroy (local) or Disconnect host (remote). Right-click a **pane** row for the same menu you get on the pane itself (see [Mouse: pane context menu](keybindings.md#mouse-pane-context-menu)) — note that this focuses the pane first, switching tabs if it lives on another one, so the menu's actions all land on the pane you clicked. Right-click a **tab** heading for the tab menu (see [Mouse: tab context menu](keybindings.md#mouse-tab-context-menu)) — this one does not focus or switch first, since none of its actions need the active tab.

### Project groups

With many projects — especially remote ones, which take two rows each — the sidebar's project list crowds out the PANES section below it. Put projects into named groups and collapse the ones you are not using:

- **Right-click a project → Move to group…** lists your groups (the current one marked `✓`), **New group…** — a small dialog asks for the name, `Enter` to create, `Esc` to cancel — and **No group**. A group can mix local and remote projects.
- **Click a group header** to collapse or expand it. A collapsed group is one row, `▸ name (N)`, carrying the summed badges of its projects — `▲`, the working spinner, `✓`, `◆`, `⌫`, and the link marker when one of its hosts is parked or retrying its connection. If the project you are in belongs to a collapsed group, its row still shows under the header.
- **Drag a header** to reorder the groups. **Drag a project onto a header** to put it in that group, or onto the **PROJECTS** heading (or an ungrouped project) to take it out. Dragging a project up or down inside its group, or inside the ungrouped list, reorders it as before; `Alt+Shift+Up`/`Down` do the same and never move a project into or out of a group.
- The project row or group header under the mouse pointer is shaded light grey, and the project or header you are dragging light blue. While you drag a project, the place it would land if you let go — another group's header, or the **PROJECTS** heading for "no group" — turns light green with a `→`.
- **Right-click a header** for Rename group (the same name dialog), Collapse / Expand, Move up / down and Delete group. Deleting a group only ungroups its projects — nothing is closed.
- Two keymap actions ship unbound: `project.group_toggle` (the active project's group) and `project.groups_collapse_all` (collapse every group, or expand them all when all are collapsed). Bind them in `bindings.toml`.

Group names are unique (ignoring case) and at most 32 characters. Groups live on this machine, in `project-groups.json` beside `config.toml`, and survive a restart; a project whose host is offline stays in its group, and one its daemon reports as gone leaves it. Two Quil windows on one machine share the file: the last change wins, and the other window picks it up on its next start. The project picker, `Alt+Shift+←/→` and `Alt+Shift+A` still reach every project, including those in collapsed groups.

### Projects on another machine

A project's root directory lives on one machine, so a project belongs to the daemon that holds it. Tick **Remote (ssh)** in the New Project dialog, give a user and host, and press Enter on the Host row: quil dials it and then browses *that* machine's filesystem for the root directory. The host is remembered in `[[destinations]]` and attached at every launch until you disconnect it.

**One host holds one project.** A daemon must have at least one tab and a tab must belong to a project, so a host always arrives already holding one — called `Default`, either created when you attach or migrated from tabs that predate projects. Naming a project on such a host **renames that one** rather than adding a second, so whatever was already running there ends up under the name you chose. The local daemon is unaffected — it holds as many projects as you like.

**A host that already has projects folds them into the one you name.** Press Enter and the form tells you what it is about to do — how many projects the host has, what the result will be called, how many tabs move, and that nothing is closed — and a second Enter does it. Every tab moves onto the surviving project and the emptied records are dropped; no tab, pane or running command is touched. Editing the name in between re-describes rather than acting on what you moved away from. The surviving project keeps its **root directory**: the dialog fills that field in by itself once the directory listing arrives, so it usually holds wherever the daemon starts rather than anywhere you chose — use **Rename** to move one. This is the way to tidy a host connected before v1.48.0, where reconnecting and re-creating "the project that disappeared" left another row behind each time — disconnecting is client-side only, so the remote daemon kept every project and replayed them all on the next connect.

A host that cannot be attached to is **provisioned from the dialog** rather than sending you to a shell. No Quil there at all installs it; a daemon older than your client is upgraded, which stops that daemon and respawns its panes from the saved workspace — commands running in its shells are killed, and the status line says so while it runs. Both are attempted at most once per host per session: a dial that fails the same way straight after is something the install cannot fix, so it is reported instead of retried.

The one case Quil will not fix for you is a remote daemon **newer** than your client. Provisioning pushes your own build, so acting there would downgrade a machine other people may be sharing — the message names the client upgrade instead.

Disconnecting removes the machine from your sidebar and stops nothing on it — the remote daemon keeps every pane alive, and reconnecting restores the same workspace.

A configured remote host that's unreachable when you launch no longer drops out of the sidebar: its projects stay in place, shown in orange, while Quil retries the connection in the background. If the host needs a fresh install or an upgrade rather than a retry, `quil remote setup <host>` repairs it from a terminal — no need to wait for the next launch. There's also an in-session route: open the New Project dialog and enter the same host — it dials in, detects the version mismatch, upgrades the remote daemon, reconnects, and the offline row goes live again with no relaunch or separate terminal needed.

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

The manual row always confirms with GitHub before it acts, so it installs
the release that is newest *now* rather than the one the last daily check
found — if something newer than the staged version has shipped, that is
what gets fetched and offered. Opening F1 also refreshes the row's label.
The update row is local-only: with a remote project active it says so
instead of acting, because applying swaps this machine's binaries.

### What's New after an upgrade

The first launch on a new version opens a summary of the releases you skipped,
so an update is not a silent binary swap. Features and changes are shown in
full; fixes are collapsed to a count that `→` expands. `Esc` closes it, and it
is reachable at any time from **F1 → What's New**.

The text is not the changelog. Each PR writes a one-line `headline:` beside its
changelog fragment, and the release pipeline appends those to a file the binary
embeds — so the dialog stays in the register you can read in half a minute
rather than repeating a full entry. See
[`changelog.d/README.md`](../changelog.d/README.md) for the fragment format.

The marker it compares against is the last version you actually *ran*, not the
last one you were told about, so dismissing an update offer never suppresses the
summary for a version you never installed. Nothing is backfilled: a version is
either present with complete data or absent, so the file starts empty on an
install that predates the feature.

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

## Workspace templates

A template describes one tab: its panes, their layout, and an optional starting
prompt for each. Quil recreates that setup on demand and then gets out of the
way — nothing survives creation, and nothing supervises what the panes go on to
do. The tab that results is an ordinary tab.

The command palette's **New from template** has four rows: the template, an
optional free-text task, a directory, and an optional new branch. The directory
row is the same daemon-side browser the Ctrl+N pane dialog uses — Up/Down moves,
Enter descends, Left goes up, Ctrl+V jumps to a pasted path — so the directory
is chosen by navigating rather than typed. Creating is refused while a listing
is still loading, so the tab cannot land somewhere other than what is on screen.
Ctrl+S creates from any row; Enter creates from every row except the task editor
(where it adds a line) and the browser (where it descends).

Each pane may name a `model`, plugin `toggles` by name, a subdirectory, a mute,
and a `prompt`. Model and toggle arguments are **frozen into the pane at
creation**, so editing the file later cannot change what an existing pane
restarts with. Prompts substitute `{{task}}`, `{{dir}}`, `{{branch}}` and
`{{panes}}` in a single pass — placeholder-like text inside your own task stays
literal. `{{panes}}` lists every pane in the tab with its name, id and type,
which is what lets one pane's prompt drive the others without you pasting ids.

Five layouts: `rows`, `columns`, `main-left`, `main-top`, `grid`. The pane
marked `main` is the anchor for the two main-\* shapes, chosen independently of
list order — so a pane that should be briefed last can still hold the large
region. Panes are created and prompted in listed order, after every pane exists.

Naming a branch opens the tab in a fresh git worktree instead: a placeholder
pane spins while git runs, then the real panes replace it.

Three templates ship — **agent-team**, **pair** and **review** — and
`templates.toml` is editable at F1 → Settings → Templates in the TOML editor.
A save validates the whole document before replacing it atomically; an invalid
file keeps the editor open with the reason and leaves the previous file intact.
Comments and prompt formatting survive a round trip.

MCP exposes the same creation through `create_from_template`. Two limits are
worth knowing before writing a team prompt: a Codex pane's output cannot be read
back as its answer (scrollback and screen capture are both spinner animation, so
ask for a file), and the per-pane MCP server is a guard rail rather than a
security boundary against an agent that has a shell. See
[Workspace templates](workspace-templates.md).
