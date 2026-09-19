# Quil Roadmap

Detailed progress tracker and future plans for Quil.

**Reflects shipped state as of v1.69.0.** Completed entries are ordered roughly
chronologically and tagged with the release that shipped them; anything below the
`In Progress` divider is unimplemented unless it says otherwise. When a feature
ships, update this file in the same PR — the `Completed` section is the answer to
"what does Quil actually do today", and it drifts fast when features ship straight
from a plan in `docs/superpowers/plans/` with no PRD in `docs/roadmap/`.

---

## Completed

### M1: Foundation
> Daemon, TUI, IPC, PTY, tabs, splits, shell integration, mouse, scrollback, daemon lifecycle.

All core infrastructure is in place. The client-daemon architecture works across Linux, macOS, and Windows. Shell integration auto-injects OSC 7 hooks for CWD tracking. Binary split tree enables arbitrarily nested pane layouts.

### M2: Persistence
> Workspace snapshots, ghost buffer persistence, shell respawn, reboot-proof sessions.

Workspace state (tabs, panes, layout, CWD) persists to `~/.quil/workspace.json` with atomic writes and `.bak` rollback. Ghost buffers capture PTY output to binary files. On daemon restart, shells respawn with saved CWD and ghost buffers replay instantly.

### M3: Resume Engine
> Regex scrapers, token extraction, AI session resume.

Session resume infrastructure is complete. The `preassign_id` strategy generates a UUID at pane creation, passes it via `--session-id`, and resumes with `--resume` after daemon restart. The `session_scrape` strategy extracts tokens from PTY output via regex for tools that don't support pre-assigned IDs. The `rerun` strategy re-executes the same command + args. Fallback to shell when resume args can't be resolved.

### M4: Plugin System
> Typed panes with TOML plugins, plugin registry, pane creation dialog.

The plugin system is fully operational. 11 built-in plugins ship with Quil — two Go
built-ins plus nine embedded TOML defaults written to `~/.quil/plugins/` on first
run (user copies override them):

| Plugin | Status | Persistence | Shipped |
|--------|--------|-------------|---------|
| **Terminal** | Production | `cwd_only` — restore working directory | M4 |
| **Terminal (wide canvas)** | Production | `cwd_only` — keeps content on squeeze | v1.34.0 |
| **Claude Code** | Production | `preassign_id` / `--resume` — session resume | M4 |
| **OpenCode** | Production | `session_scrape` via JS plugin hook | v1.12.0 |
| **Codex** | Production | `preassign_id` — hook-recorded id, `codex resume <id>` | v1.67.0 |
| **SSH** | POC | `rerun` — reconnect with same args | M4 |
| **Stripe** | POC | `rerun` — re-listen with same webhook URL | M4 |
| **lazygit** | Tool | `rerun` — plus per-tab `Alt+G` overlay | v1.22.0 |
| **hunk** | Tool | `rerun` — plus per-tab `Alt+D` overlay, sharing lazygit's slot | v1.57.0 |
| **k9s** | Tool | `rerun` — kube-context pick via `discover = "kube"` | v1.27.0 |
| **lazysql** | Tool | `rerun` — connections stay in lazysql's manager | v1.28.0 |

Key capabilities:
- **TOML plugin format** — user-created plugins in `~/.quil/plugins/*.toml`
- **Plugin registry** with auto-detection (`exec.LookPath`)
- **Pane creation dialog** (`Ctrl+N`) — three-step: category, plugin, split direction
- **Error handlers** — regex patterns match PTY output and show help dialogs
- **Atomic pane replacement** — swap pane type in-place
- **Resuming/preparing spinner** — animated border indicator during pane startup
- **Window size persistence** — save/restore terminal dimensions across restarts

### M6: Pane Focus Mode
> Full-window focus for single pane (Ctrl+E toggle).

Ctrl+E toggles the active pane to fill the entire tab content area. Other panes keep running in the background, receiving PTY output. The layout tree stays intact — focus mode is a pure rendering toggle on `TabModel.focusMode`.

Key behaviors:
- **Ctrl+E** toggles focus on/off (rebindable as the `pane.focus_toggle` action)
- Active pane resized to full tab dimensions; VT emulator + daemon PTY updated
- `[focus]` indicator in status bar
- Pane navigation (`Alt+Arrow`) disabled in focus mode
- Split (`Alt+Shift+H` / `Alt+Shift+V`) and close (`Ctrl+W`) auto-exit focus mode
- Focus state is NOT persisted — restarting Quil returns to normal layout

### M8: Bubble Tea v2 Migration + Text Selection
> BT v2 + Lipgloss v2 migration, text selection, platform-native clipboard, editor enhancements.

Migrated from Bubble Tea v1.3.10 to v2.0.2 and Lipgloss v1.1.0 to v2.0.2. Added text selection, clipboard, editor selection/navigation, and beta disclaimer dialog.

Key changes:
- **Bubble Tea v2** — declarative View (`tea.View`), typed mouse events, `KeyPressMsg`
- **Lipgloss v2** — border-inclusive Width/Height semantics
- **Terminal text selection** — Shift+Arrow (char), Ctrl+Shift+Arrow (word), mouse click+drag
- **Editor text selection** — full selection/clipboard in TOML editor (Shift+Arrow, Ctrl+X/V/A, Enter to copy)
- **Editor navigation** — Ctrl+Arrow word jump, Ctrl+Alt+Arrow 3-word, Ctrl+Up/Down paragraph
- **Clipboard** — platform-native Read/Write: Win32 API, pbcopy/xclip
- **Bracketed paste** — Ctrl+V wraps content in `ESC[200~...ESC[201~`
- **Beta disclaimer** — startup dialog with random tips, "Don't show again" persists to config
- **Config persistence** — `config.Save()` for atomic config write-back
- **Go 1.25** — required by Lipgloss v2

### Pre-Built Binaries & One-Line Install — [PRD](roadmap/pre-built-binaries.md)
> GoReleaser cross-compilation, GitHub Releases, install script.

GoReleaser produces archives for 5 platforms (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64) with SHA256 checksums. Single `release.yml` workflow with two jobs: version bump + tag, then GoReleaser build + publish. Install script (`scripts/install.sh`) for Linux/macOS with checksum verification. Homebrew tap, Scoop, Winget deferred (need external repos).

Key capabilities:
- **GoReleaser config** — `.goreleaser.yml` (v2), two builds (quil + quild), `.tar.gz`/`.zip` archives
- **Automated releases** — conventional commit analysis → version bump → tag → build → publish
- **Install script** — POSIX shell, OS/arch detection, SHA256 verification, `QUIL_VERSION` pinning
- **Version injection** — consistent `-ldflags` across GoReleaser, dev.sh, dev.ps1, rebuild.ps1, Makefile
- **CI security** — actions pinned to SHA, per-job permissions, version format validation

### M10: MCP Server — [PRD](roadmap/mcp-server.md)
> Make Quil the AI's eyes and hands via Model Context Protocol.

`quil mcp` subcommand bridges MCP JSON-RPC (stdio) to daemon IPC (socket). AI assistants can read pane output, send commands, take screenshots, navigate tabs, restart panes, and control the TUI. No other terminal multiplexer offers this.

Key capabilities:
- **35 MCP tools** — Phase A (workspace control): `list_panes`, `read_pane_output`, `send_to_pane`, `get_pane_status`, `create_pane`. Phase B (interaction + introspection): `send_keys`, `restart_pane`, `screenshot_pane`, `switch_tab`, `list_tabs`, `destroy_pane`, `set_active_pane`, `close_tui`. Notification Center (M12): `get_notifications`, `watch_notifications`, `dismiss_notifications`. Memory Reporting (M13): `get_memory_report`, `get_pane_memory`. Projects, hosts and tasks (2026-09): `list_projects`, `create_project`, `update_project`, `switch_project`, `destroy_project`, `create_tab`, `create_from_template`, `rename_tab`, `destroy_tab`, `rename_pane`, `list_plugins`, `list_sessions`, `list_hosts`, `delegate_task`, `get_task`, `wait_task`, `list_tasks`. Workspace templates add ordered pane creation with frozen arguments, prompts and layouts
- **Official MCP SDK** — `modelcontextprotocol/go-sdk` v1.4+, typed tool handlers with struct-based input schemas
- **Request-response IPC** — backward-compatible `Message.ID` field for correlation; daemon responds to specific connection
- **VT-emulated screenshots** — `charmbracelet/x/vt` renders ring buffer into text grid showing actual screen state
- **Orange highlight** — pane border flashes orange during MCP interaction (configurable `[mcp] highlight_duration`)
- **Per-pane logging** — interaction metadata in `~/.quil/mcp-logs/`; two-layer redaction (AI markers + regex fallback)
- **TUI cooperation** — `set_active_pane` and `close_tui` use daemon broadcast → TUI handler pattern
- **Process exit tracking** — `Pane.ExitCode` and `WaitExit()` with `sync.Once` on PTY sessions (Unix + Windows)

### M12: Notification Center — [PRD](roadmap/notification-center.md)
> Centralized event sidebar with pane navigation and history stack.

Replaces manual pane polling with push notifications. A non-modal sidebar shows pending events; select an event to jump to the linked pane; `Alt+Backspace` returns to where you were (browser-back pattern).

Key capabilities:
- **Daemon event queue** — bounded, mutex-protected, survives TUI disconnects, replays on attach. Watcher pub/sub for MCP blocking tool
- **Event sources** — process exit detection, OSC 133 command completion with exit code, bell character detection (30s cooldown), smart idle analysis
- **Smart idle analysis** — when a pane goes idle (5s no output), last 4KB stripped of ANSI and matched against plugin `[[idle_handlers]]` patterns. SSH `[Y/n]` → "Waiting for confirmation", Claude Code prompt → "Waiting for input", password prompts detected
- **TUI sidebar** — toggled via Alt+N (visibility), F3 (focus+navigate). Severity-colored pane names (red/orange/blue). Status bar `[N events]` badge. 10s timestamp refresh
- **Pane history stack** — `Alt+Backspace` navigates back through previously visited panes
- **OSC 133 shell hooks** — bash, zsh, PowerShell emit command start/end markers. Zsh captures `$?` immediately via `local ec=$?`. PowerShell uses `[char]0x1b` for 5.1 compat
- **Plugin `[[idle_handlers]]`** — TOML section for context-aware patterns, parallel to `[[error_handlers]]`. Default patterns for terminal, claude-code, and ssh plugins
- **Plugin `path` field** — explicit binary location bypasses PATH lookup. 3-tier detection: path → LookPath → searchBinary fallback (fixes Explorer-launched apps on Windows)
- **MCP tools** — `get_notifications` (non-blocking) and `watch_notifications` (blocking up to 5 min, replaces polling). `requestWithTimeout` with `time.NewTimer` + `defer timer.Stop()`

### M13: Memory Reporting
> Per-pane memory accounting with status-bar segment, F1 dialog, and MCP tools.

A daemon-side 5 s collector (`internal/memreport/`) snapshots per-pane Go-heap (output ring buffer + ghost snapshot + plugin state) and PTY child resident memory. Surfaces it three ways: a `mem <n>` segment in the status bar refreshed every 5 s, an F1 → Memory tab/pane tree with expand/collapse and per-pane notes-editor byte accounting, and two MCP tools for external agents. *(The dialog was replaced by F1 → Processes in v1.63.0 — see below. The collector, the status-bar segment and both MCP tools are unchanged.)*

Key capabilities:
- **Cross-platform PTY RSS** — `/proc/<pid>/status` on Linux, `ps -o rss=` batched on Darwin, `GetProcessMemoryInfo` on Windows. No-op stub elsewhere
- **New IPC pair** — `MsgMemoryReportReq`/`MsgMemoryReportResp` with the daemon as the source of truth
- **Two MCP tools** — `get_memory_report` (per-tab totals + grand total) and `get_pane_memory` (single-pane detail). Spec: `docs/superpowers/specs/2026-04-20-memory-reporting-design.md`, plan: `docs/superpowers/plans/2026-04-20-memory-reporting.md`
- **VT grid TUI memory deferred** — no stable public emulator accessor in `charmbracelet/x/vt` yet

### Overlay Retention — idle eviction and a live cap
> Closing the `Alt+G` lazygit overlay reclaims it, instead of leaking one process per tab.

`Alt+G` only ever HID the overlay. The pane and its lazygit process kept running for the life of the tab, because every existing destroy path was conditioned on something else happening — opening a different repo, the process exiting, or the tab losing its last normal pane. Measured in a real session: **7 live lazygit processes, ~116 MB each**, with a ceiling near 3.8 GB across 33 tabs. It surfaced as a memory report showing more panes in a tab than were open.

Two independent bounds, both daemon-side, both configurable in `[overlay]` and F1 → Settings, `0` disabling either:
- **Idle eviction** — an overlay hidden past `idle_timeout_minutes` (default 5) is destroyed, swept from the existing 1 s `idleChecker` tick
- **Live cap** — at most `max_live` (default 5), enforced at CREATION so the sixth is instant and deterministic, evicting **least recently shown** rather than oldest-created

Key decisions:
- **Visibility is reported, never inferred** — an idle lazygit emits nothing whether on screen or not, so an activity-based rule would kill the pane being read. The TUI reports it through `UpdatePanePayload.OverlayVisible *bool`, and `overlayOnScreen` (active tab of the active project) is the single definition of "on screen" shared by all five reporting paths
- **A report is one client's CLAIM, not a global fact** — two TUIs share a daemon, so a single flag let the second client's tab switch destroy an overlay the first had open, mid-rebase. Claims are per connection; an overlay is hidden only when no attached client claims it
- **ATTACHED clients, not connections** — every MCP bridge holds an IPC conn for its lifetime, so `ConnCount() == 0` essentially never happens; the first version was inert in exactly the case it was written for
- **Verified at runtime** — eviction fired within one tick of the 5-minute deadline, the cap evicted least-recently-shown where FIFO would have chosen differently, and five idle overlays reclaimed 191 MB → 0

Spec: `docs/superpowers/specs/2026-08-12-overlay-lifecycle-design.md`, plan: `docs/superpowers/plans/2026-08-12-overlay-lifecycle.md`. Three deferred findings are tracked in `techdebt/`.

### v1.8.0: Client/Daemon Version Handshake
> Eliminates the manual "stop daemon → replace both binaries → restart" upgrade dance.

The TUI handshakes with the running daemon before attaching. Older daemon → prompts the user, gracefully stops it, auto-spawns the matching daemon from alongside the TUI binary. Newer daemon → TUI refuses to attach and points to the releases page. Dev/debug builds skip the check.

Key capabilities:
- **New IPC pair** — `MsgVersionReq`/`MsgVersionResp` added to the protocol
- **Shared `internal/version/` package** — proper semver comparison so `1.10.0 > 1.9.0` (the previous lexical comparison had the opposite ordering)
- **Auto-spawn from binary directory** — finds `quild` next to the TUI binary, falls back to PATH
- **Unstamped local builds skip the check** — empty version string short-circuits the comparison

### v1.9.1: VT Drain + Update Watchdog
> Fixes a TUI freeze on claude-code pane creation; ships a stuck-Update detector for future hangs.

`charmbracelet/x/vt`'s `Emulator.handleRequestMode` writes DECRQM replies to an unbuffered `io.Pipe`. Quil uses the emulator as a renderer only (ConPTY is the real terminal), so nobody drained the pipe. When claude-code sent a mode query, `SafeEmulator.Write` blocked forever inside Update under its own mutex — single-keystroke wedge requiring a client kill.

Fix: per-pane goroutine in `internal/tui/pane.go` reads and discards emulator replies; shutdown via `em.Close()` → `io.EOF`, wired into `ResetVT` so no goroutine leaks on VT reset.

Watchdog: `internal/tui/watchdog.go` ticks every 2 s and, if a Bubble Tea Update has been in flight for more than 10 s, writes `runtime.Stack(buf, true)` to the log. Memoised per start-ns so one wedge produces exactly one dump; `sync.Pool` reuses the 1 MiB buffer. Eight new `apply: ...` breadcrumb log lines bracket each step of `applyWorkspaceState`. Seven white-box tests cover the logic via injected clock/stack/logger.

### v1.9.2: Claude Code SessionStart Hook
> Track session-id rotation across `/clear`, `/resume`, and conversation compaction.

Before this, daemon kept resuming the preassigned jsonl after a restart, silently restoring pre-rotation conversation and discarding the user's post-rotation work.

Quil registers a `SessionStart` hook at every spawn (never modifies `~/.claude/settings.json`) and passes `QUIL_PANE_ID=<paneID>` in the PTY env. The hook is the `quild claude-hook` subcommand (the embedded sh/ps1 scripts it replaced are described in [ADR-23](architecture.md)): the daemon writes `$QUIL_HOME/sessions/<paneID>.settings.json` naming that command and passes `claude --settings <that file>`. The subcommand reads Claude's stdin JSON, extracts `session_id`, and atomically writes `$QUIL_HOME/sessions/<paneID>.id`. On daemon restore, `resumeTemplateFor` consults this file and prefers the hook-recorded id over the original preassigned id.

Hardening:
- **`pathIsArgvSafe`** rejects a settings path holding a character a shell would
  re-interpret in the `--settings` argument, before the file is written
- **`ReadPersistedSessionID`** rejects pane ids containing path separators and caps reads at 256 bytes
- **The hook validates** the extracted id against a uuid regex before persisting; failures land in `$QUIL_HOME/claudehook/hook.log` (created lazily on first write)
- **Registration failure is not fatal** at spawn time (`claudeHookSpawnPrep`) — if the
  quild executable cannot be resolved, the settings JSON cannot be built, or the settings
  file cannot be written, the pane spawns with pre-feature behaviour rather than a dead
  hook, and the reason is logged

### Notes Soft-Wrap
> Long prose in the pane-notes editor wraps onto the next visual row instead of being hard-truncated.

Pane notes (M7) historically truncated long lines at the panel edge with a trailing `~`. The notes panel is only ~40% of window width — every normal paragraph disappeared off the right edge.

Character wrap (not word wrap), opt-in per editor via a new `TextEditor.SoftWrap` flag — the TOML plugin editor and F1 log viewer keep their existing truncation. Cursor Up/Down walks visual rows with column preservation; Home/End snap to the visual row; shift-arrow selections stay contiguous across wrap boundaries; mouse clicks on a continuation row resolve to the correct logical column via a new `visualToLogical` helper. `ScrollTop` is reinterpreted as a visual-row index when wrap is on. Paragraph (Ctrl+Up/Down) and PageSize (Alt+Up/Down) jumps stay logical. Also fixes a pre-existing render bug exposed by the new path: cursor at end-of-line past a shorter selection was invisible.

### M7: Pane Notes — [PRD](roadmap/pane-notes.md)
> Side-by-side note-taking linked to individual panes.

A plain-text notes editor that opens next to the active pane on `Alt+E`. Notes are stored one file per pane and survive pane destruction, so the context you captured while debugging in a pane is still there next week when the pane is long gone.

Key capabilities:
- **Alt+E toggle** — opens the notes editor alongside the active pane (pane left ~60%, editor right ~40%). Mutually exclusive with focus mode
- **Read-only pane while editing** — all keys route to the editor so there is never ambiguity about where input lands. Exit notes to interact with the pane
- **Three independent save safety nets** — 30-second debounce auto-save, explicit `Ctrl+S`, and unconditional save on exit (`Alt+E`, `Esc`, close/split, TUI quit)
- **Per-pane storage** — `~/.quil/notes/<pane-id>.md`, atomic temp+rename writes, survives daemon restart (pane IDs are stable via `workspace.json`)
- **Notes outlive the pane** — closing or destroying a pane does not delete its notes file; browser ships in Phase 2
- **TextEditor reuse** — the existing rune-aware editor (selection, clipboard, multi-line paste, word jumps) gained a `Highlight` field so it can render plain text with no TOML colouring

### v1.17.0: Robust Restart — Lazy Restore, Log Rotation, IPC Backpressure
> Daemon restart survives a large workspace instead of stalling on it.

`respawnPanes` now spawns only the active tab's panes plus panes flagged **always resume** (`Alt+Shift+E`, `●` in the tab bar) before it starts listening; everything else is marked pending and spawned on first access. IPC gained a dual-queue design per connection — a must-deliver critical queue and a droppable live-output queue — so one wedged client can no longer block broadcast to the others. Log rotation (`max_size_mb` / `max_files`) landed in the same release.

Follow-ups: **v1.34.1** raised the client-side readiness wait from 2 s to 30 s (crash-aware, aborts early if the spawned daemon actually dies) and added a single-instance guard so a redundant `quild --background` defers to the healthy daemon instead of stealing its socket and orphaning it.

### v1.18.0–v1.19.0, v1.33.0: Agent Work-State Indicators & Hook-Events Pipeline
> The TUI knows when an agent is working, blocked, or done — from the agent's own hooks, not from screen scraping.

Quil registers hooks into Claude Code (native `quild claude-hook` subcommand, 12 events, inline `--settings`) and OpenCode (embedded JS plugin, `OPENCODE_CONFIG_CONTENT`) at every spawn, never touching the user's own config. Events land in a per-pane JSONL spool, are rate-limited and debounce-coalesced by `internal/hookevents`, and flow through the existing notification pipeline.

Key capabilities:
- **Working spinner** — braille spinner on the tab label and the pane's top border while a turn or any background subagent is running. Subagents are counted, so a parallel 3-subagent spawn does not undercount to 1
- **Persistent unseen mark** (v1.19.0) — a completed turn leaves a green pane border and green tab label until you focus the pane. Replaced the old 5 s tab flash, which you missed if you were looking elsewhere
- **Blocked-for-input edges** — permission prompts and option prompts park the spinner and raise the unseen mark, then resume when you answer
- **Model + context segment** (v1.33.0) — AI panes show the last completed turn's model id and context-window token count in the status bar (`opus-4.8 · 612k ctx`), riding the same hook events with no new IPC. Compaction renders `· compacting` until the next turn reports the true reduced size
- **Hook health fallback** — if hooks never load, the legacy idle-pattern analysis still fires, so notification coverage degrades rather than disappearing

### v1.20.0–v1.21.0: Daemon Lifecycle CLI + Pane Restart
> `quil restart`, `quil daemon stop|restart`, and `Alt+R` to restart a single pane.

Bounded escalation for stopping a daemon — IPC `MsgShutdown` (5 s) → SIGTERM (3 s, Unix) → SIGKILL (2 s) — shared by the CLI and the upgrade path, with a PID-reuse guard that refuses to signal a PID whose image name is not `quild*`. `Alt+R` restarts the active pane through the same daemon-side kill+respawn the MCP `restart_pane` tool uses, behind a confirm dialog.

### v1.22.0–v1.29.0: Tool Plugins & Discoverability
> lazygit, k9s, and lazysql as first-class pane types; uninstalled tools stay visible.

- **lazygit** (v1.22.0) — built-in plugin plus a per-tab `Alt+G` overlay that drops into a git UI for whatever repo the active pane is in. Overlays are ephemeral: one per tab, excluded from snapshots, auto-destroyed when you quit lazygit. New `discover = "git"` plugin field lists repos near the pane's CWD in the setup dialog
- **k9s** (v1.27.0) — Kubernetes TUI with a kube-context picker backed by the new `discover = "kube"` field, reading context names only from `KUBECONFIG` / `~/.kube/config`, never credentials
- **lazysql** (v1.28.0) — database TUI; connection selection and credentials stay inside lazysql's own manager, deliberately with no Quil-side DSN picker
- **Greyed-not-hidden plugins** (v1.27.0) — plugins whose binary is not on `PATH` now appear greyed in `Ctrl+N` with a `homepage` link instead of vanishing, so the feature is discoverable before the tool is installed

### v1.30.0: Per-Pane Input History
> `Alt+Shift+I` lists every prompt you submitted to an AI pane.

Recorded by the Claude Code `UserPromptSubmit` hook (opt-in per plugin via `[command] record_history = true`), stored at `~/.quil/history/<pane>.jsonl`, ring-trimmed to the last 200 entries, and removed when the pane is destroyed. The list is one row per prompt, scrollable with `↑↓`/`PgUp`/`PgDn`/`Home`/`End`; `Enter` opens the full text in the read-only viewer, soft-wrapped and selectable by drag. Machine-injected turns are filtered on write, on read, and on compaction so they never pollute the list — both background-task notifications and subagent reports (`<agent-message>`). OpenCode support is still pending — its message handler does not yet extract prompt text.

### v1.31.0: Bundled OpenConsole for Windows 10 — [ADR-25](architecture.md)
> Fixes the `Hello` → `H ello` caret gap when typing in AI panes on Windows 10.

The Windows 10 inbox console host re-serializes claude-code's incremental input render incorrectly. Quil now bundles Microsoft's OpenConsole (MIT) and routes the three pseudoconsole syscalls through it — on Windows 10 and older only, detected via `RtlGetVersion().BuildNumber < 22000`. Windows 11 keeps its unaffected inbox host. Falls back to the inbox host if the bundle is missing. Binaries are fetched at build time, not committed.

### v1.32.0: Mouse-Wheel Forwarding + macOS Render Fixes
> The wheel scrolls the app's own viewport in AI/TUI panes; claude-code no longer corrupts on Terminal.app.

Apps that enable DEC mouse tracking run on the alternate screen and never fill Quil's scrollback, so the wheel had nothing to scroll. The daemon is the authority on tracking state — it is the only component that sees the mode-enable burst on every attach, including reattach to an already-running session — and the client forwards each notch as the matching SGR or X10 sequence.

Same release fixed two macOS-only defects: the bundled emulator terminated OSC strings on a raw `0x9C` byte even mid-UTF-8, so claude-code's `✳` title spilled into the visible grid (doubled logo, `AAA` → `AAAude Code`); Quil now filters window-title OSCs before the emulator. And `Alt+<printable>` is forwarded as Meta so Option-based readline word navigation works.

### v1.34.0: Wide-Canvas Panes with a Native Threshold
> AI panes stop reformatting themselves every time the layout changes.

AI panes render on a window-sized canvas so grid, zoom, and sidebar changes never resize their PTY. v1.34.0 added the inverse: once a pane's inner width reaches `[display] min_native_cols` (default 80) it renders **natively** at its real size, which restores mouse and keyboard selection for free. Below the threshold it falls back to the canvas plus a preview crop, with mouse selection mapped through the inverse layout. The `terminal-wide` built-in exposes the same behaviour for shells.

### v1.35.0: `quil status`
> Scriptable daemon and session introspection.

Top-level command (alias `quil daemon status`) reporting daemon liveness, pid, version, environment, approximate uptime, and per-tab/pane session metrics with state and memory. `--json` for scripting; exit codes distinguish healthy / not-running / wedged.

### v1.37.0: Automatic Updates — [plan](superpowers/plans/2026-07-18-auto-update.md)
> Detect, stage, and apply a new release without leaving Quil.

The daemon checks GitHub releases 1 min after startup then every 24 h, publishes the result on the workspace broadcast, and can stage the download in the background. Staging is sha256-verified and extracts to `$QUIL_HOME/update/staged/<ver>/` with `manifest.json` written last as the atomic completion marker. Applying does a rename-aside binary swap with pair rollback, then respawns itself.

Key capabilities:
- **Status-bar segment** — `↑ vX [ready]`, plus an About-menu row that triggers a real check rather than repeating stale broadcast state
- **Two config knobs** — `[update] check` / `auto`, both exposed in the Settings dialog
- **Compiled out of dev/debug builds** — applying a release binary over `quil-dev.exe` would strip its baked-in dev-mode ldflag and silently attach the next launch to production `~/.quil`
- **Backup-slot fallback** — a backup file still held open by an orphaned process cannot be deleted or replaced on Windows, so the swap falls back to `.old.1`, `.old.2`, … instead of wedging every future update

### v1.38.0: Pane Context Menu — [plan](superpowers/plans/2026-07-19-pane-context-menu.md)
> Right-click a pane (or `Alt+A`) for its actions.

A compositor overlay, not a modal — it targets the pane under the cursor, highlights it, and dispatches into the same handler methods the keybindings use. Rows are grouped (view actions / pane settings / destructive) and greyed when unavailable (history without `record_history`, lazygit without the binary). Adds **Mark attention**, a pinned green border that survives focus, unlike the automatic unseen mark. Right-click with an active selection still copies.

### M11: Command Palette — [PRD](roadmap/command-palette.md), [plan](superpowers/plans/2026-07-23-command-palette.md)
> `Alt+Shift+P` fuzzy-find launcher — commands, tabs, panes, and pane content in one query. Shipped v1.39.0–v1.40.0.

A modal, centered, keyboard-first launcher. Entries are grouped under section headers with navigation first (Go to pane / Tabs / Pane / System); headers disappear as soon as you type. A greedy subsequence scorer rewards consecutive runs and word boundaries. Each row shows its keybinding, so the palette teaches the shortcuts as you use it. Default `alt+shift+p` only — `ctrl+shift+p` is intercepted by Windows Terminal and VS Code before Quil ever sees it, so it stays opt-in.

**Content search is unified into the same query** (v1.40.0), not a separate `/` mode as originally specced: every keystroke both filters the command list and fires a debounced search across every pane's buffered scrollback — all tabs, including background and muted panes. Matches appear under a `Found in panes` header with a match count and a preview line, and `Enter` jumps to the pane. Because hits are stored as ordinary jump-to-pane commands, cursor navigation, dispatch, and rendering all reuse the command machinery. A local timeout turns an unanswered request into a diagnosable row instead of an endless `Searching…`.

**Deferred to Phase 2:** per-plugin/instance quick-create, `:` direct-command mode, MRU ordering.

### v1.41.0–v1.42.1: Pane Setup Dialog — Recent Locations & Session Picker
> Two fewer keystrokes between `Ctrl+N` and a working AI pane.

- **Recent locations** (v1.41.0) — the last 5 committed working directories are offered as a one-keystroke quick pick, persisted to `~/.quil/recent-cwds.json`, with deleted folders filtered out. Git-repo discovery keeps priority; **Browse…** still opens the full picker
- **Claude resume-session picker** (v1.42.0) — see M5 below
- **Committed-value marker** (v1.42.1) — the chosen directory (and kube context) stays visible with a `▸` marker once the cursor moves off it or the field loses focus. Previously the selection highlight was drawn only on the focused field's cursor row, so the answer disappeared from the dialog exactly while you configured the rest of it

### v1.47.0: Projects — [PR #123](https://github.com/artyomsv/quil/pull/123)

> A grouping layer above tabs, and one client holding several daemons at once.

Tabs were a flat list, so six tabs across three repositories were visually indistinguishable, and an agent parked on a permission prompt in a background tab stayed invisible until you happened to look. Separately, `quil --remote <host>` bound a whole TUI process to one daemon. Both were the same missing piece: nowhere to hang "which work is this" or "which machine is this".

- **Projects** — named, rooted at a directory, owning their own tabs and remembering which one you left them on. Daemon-owned and persisted, so a second client sees the same grouping. An existing `workspace.json` migrates into a single `Default` project with tab order preserved; no prompt, no data loss
- **Reserved left sidebar** (`Alt+Shift+S`) — projects with a roll-up of their agents, then the active project's panes with live state: `⠹` working — a spinner, the tab bar's own (`⋯N` subagents), `▲` blocked on you and the tool it is asking about, `○` idle, `✓` finished-unseen. The roll-ups keep updating for **background** projects, which is the point
- **Blocked is distinct from done** — the hook events always carried the difference (`Notification`, `PermissionRequest`, `permission.ask`); the old classifier collapsed them because the UI only needed "mark unseen"
- **Attention queue** (`Alt+Shift+A`) — jumps to whichever agent has been blocked longest, anywhere in the workspace, cycling on repeated presses. Oldest-first, deliberately not sidebar order
- **Per-pane git state** — branch, `wt` for a linked worktree, `↑N`/`↓N` against upstream, on a background ticker and cached per checkout so N panes in one repository cost one invocation. Keyed by the **per-checkout** git dir, not the repository's common dir: linked worktrees share a common dir while sitting on different branches, which is the entire reason anyone creates one. `git status --porcelain` is deliberately excluded — the one call that can take seconds on a large repo without fsmonitor. A probe that does not answer keeps its last value and is marked **stale**
- **Several daemons at once** — `[[destinations]]` dials extra hosts beside the local daemon; their projects are siblings in the same sidebar, each with its own reconnect state, so one daemon dying no longer ends the session. A `Router` multiplexes them behind the two-method client the TUI already consumed, and messages carry a client-side-only `Origin` (`json:"-"`), so no protocol bump was needed
- **Runtime connect / disconnect** — tick **Remote (ssh)** in the New Project dialog, give a user and host, press Enter on the Host row: Quil dials, offers to install itself if the host has not got it, then browses *that* machine's filesystem for the root directory. **Disconnect host** removes it from the sidebar and stops nothing on the far end

**Follow-ups since (v1.47.1 and the next release):** the F1 shortcuts list scrolls and fits a narrow terminal, and lists the project keys; the command palette offers every project action; connecting a host with an out-of-date daemon upgrades it from the dialog instead of naming a CLI command; the dialog's message line is coloured by what it means rather than always red. And **one remote host holds one project** — a daemon must have a tab and a tab must have a project, so a host always presented a `Default` nobody asked for; naming a project there now renames that one, so the machine's existing tabs land under the chosen name. A host that already holds **several** folds them into the one you name — every tab moves onto the survivor and the emptied records are dropped, nothing is closed — which is what tidies a machine connected before that rule existed.

**Deferred, each to its own plan:** per-project MCP scoping (a breaking change to shipped tools; wants a `scope: "all"` opt-out and its own release note) and listening-port detection (three platform implementations, named the first thing to cut). The sidebar's `✗ exited-nonzero` glyph is unimplemented — `PaneInfo` carries no exit field.

### v1.48.0–v1.49.0: One Project Per Remote Host

> A host arrived holding a `Default` nobody asked for, and creating your own project left it sitting beside the tabs you cared about.

- **A remote host holds exactly one project, and naming it is how you get it.** A daemon must have a tab and a tab must belong to a project, so the `Default` is structural. Naming a project on such a host now **renames** that one, so the host's existing tabs land under your name. A host whose project you have already named refuses a second. The local daemon is unchanged and holds as many as you like
- **A host that already holds several folds them into the one you name.** Enter describes what it will do — how many projects, the resulting name, how many tabs move, and that **nothing is closed** — and a second Enter carries it out. Every tab moves onto the survivor and the emptied records are dropped. This is the repair path for a host connected before v1.48.0, where each disconnect-and-recreate cycle left another row behind (disconnect is client-side only; the remote daemon keeps every project and replays them on the next connect)
- **The fold leaves the survivor's root directory alone** — the dialog fills that field in by itself once the listing arrives, so overwriting a root you had picked was a change nobody asked for. **Rename** is the way to move one
- **The New Project dialog fills in the host it is aimed at.** Opened over a remote project it showed **Remote (ssh)** unticked and an empty Host while already targeting that machine — it read "this machine" and acted on the far one
- **The message line is coloured by what it means** — red only for a host that cannot be reached or an install that failed, amber while connecting or provisioning, green once connected. Everything used to render as a red ✗, so "installing…" looked identical to "cannot reach that host"

### v1.50.0–v1.54.0: Attention Marks That Survive a Restart

> The sidebar's marks are only useful if they outlive the session that set them.

- **Mark attention is daemon-owned** (v1.54.0) — it lived in TUI memory, so every mark was gone on the next launch, which is most of what a mark that never auto-clears is for. It now sits beside the pane's mute setting and survives a TUI restart, a daemon restart and a reboot, reading the same in every attached client. A `◆N` count on the project row and a `◆` on the tab
- **Mark for deletion** (v1.65.0) — the opposite statement: a red `⌫` recording that you are finished with a pane you deliberately keep alive. Mutually exclusive with Mark attention, enforced daemon-side. It deliberately does **not** recolour the tab — a pane you have decided to throw away must not compete with the three states that want you to act
- **Clear attention** (v1.50.0) — the right-click escape hatch for a mark whose clearing event never arrived. Drops blocked, finished-unseen and pinned together; the pane's next hook event works the truth out again
- **Manual and automatic marks no longer share a colour** — pinned is purple everywhere, green means only "finished while you were away"
- **Sidebar scrolling, numbering and colour** (v1.53.0) — the PANES section scrolls with the wheel with hidden-row markers, tab headings carry their `Alt+N` number and colour, and each roll-up count takes its pane rows' colour instead of a flat grey
- **Blocked classification fixes** (v1.53.1, v1.53.3) — Claude reuses one notification for a permission prompt and its own idle nudge; the nudge is now exempt once the turn has ended. Answering a prompt clears the mark, since approving fires no hook of its own
- **Emoji-safe glyphs** (v1.50.0) — every sidebar state symbol is checked against having no emoji presentation, because a colour-emoji fallback draws two cells wide while advancing one and painted over the count beside it

### v1.51.0–v1.59.0: Worktree-Owned Panes — [plan](superpowers/plans/2026-08-05-worktree-panes-stage-a.md)

> An agent, a shell and lazygit parked in the same checkout — and a route to remove it again.

- **Open a pane in an existing worktree** (v1.51.0) — the create-pane dialog lists the worktrees under the directory you pick, so the sidebar's git row shows that pane's real branch instead of repeating the main checkout's
- **Create one** (v1.52.0) — `+ new branch…` runs `git worktree add -b <branch>` and spawns the pane inside the result, at `<parent>/<repo>-worktrees/<branch>`. Nothing nests inside your checkout, so no tool that walks the tree finds a second one
- **A failed add creates no pane** — git's own message is shown and Quil never falls back to the repository root, because a pane on `master` you believe is isolated is worse than no pane. The same on restart: a pane whose worktree is gone comes back unspawned, naming the directory, with `Alt+R`
- **Branched off the repository's default branch** (v1.53.2), not off whatever the main checkout is on — `origin/HEAD`, then `origin/main`/`origin/master`, then the local pair, then HEAD. Created with no upstream, so `git push -u` behaves normally
- **Replace-a-pane path** (v1.52.2) — the worktree is created before the pane being replaced is touched, so a branch git rejects leaves that pane where it was
- **Delete it when you close the pane** (v1.59.0) — never automatic. The close confirm carries an unticked `Also delete its worktree` row, armed with `space`, off every time the dialog opens. Only a worktree **Quil created**, named by the directory Quil created rather than wherever the shell has since wandered. The dialog asks the daemon what the tree holds and shows `clean` or `⚠ 3 uncommitted or ignored files will be lost` — **ignored files count**, since a `.env` or a `build/` is exactly what a forced removal destroys. The branch is kept
- **A worktree being created is visible** (v1.61.1, v1.63.2) — the tab holds a placeholder naming the branch with a spinner for as long as the checkout runs, rather than a shell prompt in the repository root, which on a monorepo is minutes and is indistinguishable from a create that finished in the wrong tree

### v1.55.0–v1.55.1, v1.63.3–v1.63.4: Remote Upgrade From Inside the TUI

> After every client update a configured host is on the old version and cannot be attached to.

- **A host running an older Quil offers to upgrade itself** (v1.55.0), at startup and whenever a live link drifts out of version. It names both versions and says the remote daemon restarts. Previously the only way forward was `quil remote setup <host>` in a shell
- **The offer survives the launch** (v1.63.3) — it was raised before the provisioner was wired and discarded, so the host stayed unusable for the whole session; and every dialog now hands the offer back on the way out, since the what's-new dialog used to swallow it on the one launch where it matters most
- **A completed push hands the host to the reconnect ladder** (v1.63.4). The completion message was filtered against a field only the New Project dialog sets, so the host was left updated, daemonless and stuck reading "upgrading…". A push that fails now says so, and `r` retries any offline host from inside the client
- **An offline project says what is wrong** — it used to render the same "No tabs — Ctrl+T opens one" as an empty live project, which reads as the tabs having been deleted. It now names the host and whether the cause is version drift, a missing install, or a reconnect in flight
- **Overlay retention** (v1.55.1) — `Alt+G` only hid the overlay, so a session that opened it in several tabs carried one live lazygit per tab indefinitely (~116 MB each). Hidden overlays close after five minutes, at most five stay alive, least-recently-shown dropped first. `[overlay]` in `config.toml`

### v1.56.0: Desktop Notifications (Windows) — [plan](superpowers/plans/2026-08-13-desktop-notifications.md)

> The sidebar is a log. This is the alert.

- **A toast when an agent parks waiting on you, or finishes a turn while you were away** — naming the project, tab and pane, and only while you are not looking at that pane. Six agents finishing together give six separately clickable toasts rather than a storm; answering a prompt withdraws its toast
- **Clicking one switches Quil to that exact pane and focuses it**, over a `quil://` URI handled by `quil-activate.exe` — a windowless seventh binary, because a console binary gets a console window per click that takes the foreground and then disappears
- **Registration is explicit and reversible** — `quil notify setup` writes a Start Menu shortcut and the handler and prints what it wrote; `--remove` undoes both. `notify status` and `notify test` for diagnosis. Nothing is written as a side effect of a config flag
- **The click can only move your cursor** — the handler validates a pane id and forwards it over a per-PID named pipe, with no path to spawning a pane, sending input, or running a command
- **Being seen requires window focus**, not just being on screen — a pane you left visible when you switched to your browser used to count as watched, so the commonest sequence there is produced no toast at all
- **Windows only.** No transport on macOS or Linux carries a click back to a specific pane. **Raising the terminal window was built and removed**: Windows hands the foreground to the notification host, which holds it (measured 5.5 s, then >10 s) and refuses every documented way of taking it back — `SetForegroundWindow`, `AttachThreadInput` queue borrowing, `SwitchToThisWindow` and hotkey injection were each implemented and each verified refused. Windows highlights the taskbar button instead

### v1.57.0: hunk — Review-First Diff Viewer

Reading what an agent just wrote is most of what a Quil workspace produces. `Ctrl+N` → Tools → Hunk opens it as an ordinary pane; **`Alt+D`** toggles a full-tab review of the working tree for the repo resolved from the active pane's directory. `Alt+G` and `Alt+D` **share one overlay slot per tab**, so pressing the other tool's key swaps the tools rather than stacking them. `alt+d` rather than `alt+h` because plain `Alt+H` is left unbound so it reaches the running program, and vim-style layouts rebind it to pane-left.

*(v1.57.1: the release pipeline had been tagging without publishing since v1.56.0 — this Windows-only helper shares one archive definition with `quil` and `quild`, so the archive holds three binaries on Windows and two elsewhere, and packaging rejects an uneven count unless told the difference is deliberate.)*

### v1.58.0, v1.63.5: `Ctrl+T` Asks What the Tab Opens With

A new tab always came up holding a shell, so opening one for an agent meant creating the tab and then replacing its pane — every time. `Ctrl+T` now opens the same picker as `Ctrl+N`, setup step included, so a tab can start as Claude Code in a chosen directory, on a chosen kube context, resuming a chosen session, or on a fresh worktree branch. The tab and its pane are created in **one** IPC frame, so no tab flickers through a shell nobody asked for.

`Esc` **cancels and creates nothing** (v1.63.5) — it used to close the picker and make a plain terminal tab anyway, so the one key that means "back out" everywhere else was the one key that could not. The two-keystroke path to a shell tab is `Ctrl+T` `Enter` `Enter`.

A new branch is the deliberate exception: the tab opens with a PTY-less placeholder naming the branch while `git worktree add` runs, and the requested pane replaces it on success — spawning the requested type as the placeholder would start an agent in the main checkout, the isolation failure the worktree exists to prevent.

### v1.60.0, v1.62.0: Keybinding Registry, Sequences and Presets — [plan](superpowers/plans/2026-08-16-keybinding-sequences-presets.md)

> Keys resolve to named actions, and the keymap moved to its own file.

- **`F1` → Shortcuts is derived from the binding table itself** (v1.60.0). The list was maintained by hand beside the bindings and had drifted — seven of the eight project shortcuts were missing. Each key is now spelled the way it is actually matched (`ctrl+v`, not `Ctrl+V`)
- **A binding that cannot work says so**, in a warning row at the top of that dialog: a duplicate, a cross-tier shadow, a chord Quil reserves, an unparseable spec, an unknown action, an unreachable sequence opener, an unusable `${prefix}`. Each names which side wins and what will never fire
- **`Option+Shift+<letter>` reaches the shell again on macOS** — chord parsing lowercased the key while the same parser read the incoming press, so on Terminal.app with Option-as-Meta ten letters were swallowed by the binding on their lowercase twin. `alt+M` and `alt+m` are distinct chords now
- **Key sequences** (v1.62.0) — `Ctrl+B` then `c`, tmux style. The status bar shows the keys typed so far; `Esc` cancels; a combination bound to nothing says so. **Pressing the opening key twice sends it through to the pane**, which is what keeps a tmux *inside* a Quil pane reachable
- **Keymap presets in `~/.quil/bindings.toml`** — `preset = "tmux"` for a tmux-compatible layout, `prefix = "ctrl+a"` to move the prefix in one line, `[bindings]` keyed by **action ID** to override anything. Presets **replace** rather than add; any action a preset does not name keeps its usual key
- **Existing `[keybindings]` migrate on first launch** — only settings that differ from the shipped defaults are carried across, so a binding you never changed stays free to follow future default changes rather than being frozen at today's value. Keybindings live in their own file because `config.toml` is rewritten in full whenever any setting changes, which would freeze a resolved keymap as literal strings
- **Tab switching, next/previous tab and the shortcuts list became rebindable** — `Alt+1`–`9` used to be fixed keys no setting could reach

### v1.59.2, v1.62.1–v1.62.6: Performance — [plan](superpowers/plans/2026-06-10-perf-leak-remediation.md)

> A 37-tab workspace measured 1.13 GB of client memory and held a core in kernel time while idle.

- **IPC frame encoding is ~35× faster to encode and ~10× to receive** (v1.59.2) — the envelope re-scanned the already-marshalled payload from start to finish before putting it on the wire. A pane streaming output used to occupy about half a core just formatting messages; it is now closer to 1%. The bytes on the wire are identical, so mixed versions interoperate unchanged
- **Frames are ~34% cheaper to draw** (v1.62.6) — Quil was re-measuring the width of every line it had just drawn, roughly half the work of assembling a screen
- **`[ui] scrollback_lines`, then an adaptive default** (v1.62.1, v1.62.6) — depth multiplies by pane count, since every pane holds its own emulator whether visible or not. The default now spends a workspace-wide budget: ten panes or fewer are unchanged, more get proportionally less, with a floor. A value you set always wins and is never adapted, and a chosen depth is written to the log rather than applied silently. Editable in F1 → Settings
- **Inert messages reuse the previous frame** (v1.62.1, v1.62.4) — Bubble Tea asks for a frame per message, and timers drove most of them. Output from a pane you are not looking at was ~65% of all redraws on a 41-tab workspace. The spinner moved from 100 ms to 200 ms, which still reads as motion at half the cost
- **Stale hook spools are unlinked, not truncated** (v1.62.1) — one measured workspace had 349 spool files for 37 live panes, 332 empty husks surviving every restart for the life of the install, driving ~7 000 file-handle operations a second and 21% of a core with the session idle
- **Two same-moment attaches no longer race** (v1.62.1) — `SnapshotState` copied projects out under the lock but handed back live tab pointers
- **A quil parked after an in-session update releases its memory** (v1.62.1) — it stayed holding every pane's emulator and scrollback, measured at 326 MB and 436 MB on a machine that had updated twice without quitting
- **The MCP bridge declines the pane-output broadcast** (v1.62.1) — it decoded every frame only to discard it. Opt-in `subscribe` message; a client that never sends it, including any older build, receives exactly what it did before

### v1.61.0: Changelog Fragments and the What's New Dialog — [plan](superpowers/plans/2026-08-16-changelog-presenter.md)

- **Launching a new version shows what changed** — features and changes in full, fixes collapsed to a count `→` expands. Also at `F1` → What's New. The marker it compares against is the version you last **ran**, not the last one you were told about, so dismissing an update offer never suppresses the summary for a version you never installed
- **One changelog file per PR** — entries used to go under the single `## [Unreleased]` heading, the same anchor line for every PR, so two open PRs conflicted the moment the first merged. Git has no conflict concept for two distinct *added* paths. `changelog.d/<type>-<slug>.md`, collected and deleted by the release workflow
- **Each fragment carries a one-line `headline:`**, appended at release to a file the binary embeds — so the dialog stays in the register you can read in half a minute rather than repeating a full entry. Deliberately not JSON: a headline is single-line by definition, so newline is the only delimiter a POSIX-sh writer needs

### v1.62.2–v1.62.3, v1.62.5, v1.66.3: AI-Pane Correctness

> Four ways a Claude pane could lose or hide its own conversation.

- **A Claude pane no longer clears itself** (v1.62.2). Quil asks a pane to repaint with `Ctrl+L`, and claude-code v2.1.126+ runs `/clear` on two within two seconds — a daemon restart, a reattach or two quick resizes delivered exactly that pair, wiping every AI pane at once with nobody at the keyboard. Deliveries of a plugin's `redraw_key` are now kept three seconds apart and coalesced
- **Reconnecting no longer paints a torn frame into a full-screen pane** (v1.62.3). Replaying recent output reproduces history for a shell but not for a program on the alternate screen, which sends only what changed — so once the buffer wraps there is no complete frame left. Quil now detects the alternate screen and asks those programs to repaint instead
- **The session hook registers reliably on Windows** (v1.62.5) — the settings were passed as inline JSON, and `claude` is an npm `.cmd` shim that Windows re-parses, splitting it at the wrong quote boundaries. A file path is passed now
- **`Alt+R` resumes the conversation** (v1.66.3) — restart respawned with `--session-id` and an id that already had a transcript, which Claude refuses (exit 129), so every restart of a pane that had exchanged a message landed on an error screen. It now passes `--resume` the way a daemon restart does
- **A restored pane shows what its process drew** (v1.66.3) — the replay was skipped for Claude panes on the assumption the respawned process repaints itself, which is wrong once it has drawn a screen that ignores `Ctrl+L`: the first-run setup, the login prompt, a startup error. The pane stayed black with nothing saying why

### v1.59.3, v1.66.2: Work-Indicator Truthfulness

> A spinner that lies in either direction is worse than no spinner.

- **A turn the agent starts by itself now shows** (v1.59.3) — Claude only announced a turn a human began, so an orchestrator resuming after a teammate reported back went dark while it worked. One measured pane reported three finished turns against one started, with a fourteen-minute stretch of ~60 tool calls showing nothing. A tool call now counts as proof, throttled to one spooled event per pane per 15 s of silence, and never a notification card
- **A turn killed by an API error, and one stopped with `esc`, both end the indicator** — an interrupt produces no announcement at all, and one reported pane showed as working for 43 minutes after being stopped
- **A tab no longer spins forever after a subagent dies** (v1.66.2) — a failed subagent turn fires `StopFailure` with the agent's id and never a `SubagentStop`. Quil read it as the pane's own turn ending and kept the dead agent on its books; two production tabs sat lit for days after a usage limit hit
- **Hook events arrive in the order they happened** (v1.66.2) — two events for one pane landing in the same 200 ms spool read were delivered last-first on Windows, because each coalesce key ran its own timer and Go schedules the newest timer goroutine first. A stop applied before its start left a pane lit until session end
- **The green finished-unseen mark survives a restart** (v1.66.2) — it lived only in TUI memory. The daemon keeps a copy in the workspace snapshot, and the clear is remembered too
- **Indicator traffic no longer crowds the sidebar** (v1.59.3) — the driving signals were filed as notifications the sidebar never showed, and because the queue moves a repeat back to the top they steadily pushed genuine events, a permission prompt among them, out of the list. They also woke `watch_notifications` every ~15 s, turning a blocking call into a poll

### v1.63.0, v1.66.0: Process Dialog and pprof — [spec](superpowers/specs/2026-08-20-process-dialog-design.md)

- **`F1` → Processes shows the real process tree under each pane** (v1.63.0) — the shell or agent Quil started and everything it went on to spawn, with memory and CPU for each. `K` stops a process *below* the pane's own shell and everything it started; the pane's own shell is `Restart pane`, and Quil's own processes are never offered. **Replaces `F1` → Memory**; both MCP tools are untouched. Read on the **daemon's** machine, so it works unchanged when the daemon is remote
- **It also lists Quil's own processes** with version, uptime, PID — and, since v1.66.0, memory and CPU. Each process measures and reports itself over the socket rather than anything being inferred from the OS process table, which is also the only thing that works remotely. A bridge on an older binary is flagged
- **Honest sentinels, never a fabricated zero** — a process not yet sampled twice shows `—`, not `0%`, because an unknown is not an idle; a total covering one shows `~4%`. macOS CPU is the kernel's decaying average and is not comparable with a Linux host's, and the dialog says so
- **`QUIL_PPROF` serves Go profiles on demand** (v1.66.0), for both binaries, each needing its own port. Nothing listens when unset. The listener binds loopback only and **refuses** any other address — a bare port becomes `127.0.0.1:<port>`, a hostname is an error rather than something the resolver decides. It is **unauthenticated**, and loopback is a machine boundary rather than a user boundary, so set it for an investigation rather than leaving it in a shell profile. `scripts/pprof.sh` / `scripts/pprof-view.sh`

### v1.64.0, v1.66.0: Unfocused-Window Dim

Typing into a window that only *looked* focused is now visible before the first keystroke lands: on OS blur every colour blends toward the terminal's own background — panes, tab bar, sidebar, borders, status bar alike — and snaps back on focus. Only colours move; text, layout and cell widths are identical. Quil asks the terminal for its real default foreground and background (OSC 10/11) and fades toward those, so the effect follows your theme. A terminal without DEC 1004 focus reporting never dims, since it never reports losing focus.

v1.66.0 added the off switch (`[ui] unfocused_dim_enabled`) as a key **separate** from the level, F1 → Settings rows, and three command-palette presets. Separate keys are the point: with one key, switching the dim off has to write `0` over the level, so switching it back on can only restore the default — a customised `0.35` is destroyed by an off/on round trip.

### v1.66.1: Per-Destination Plugin Availability

Every daemon is asked which plugins its own machine can run, but the client kept **one** answer for all of them — so the last host to reply spoke for every project. Connect a remote box without `claude` and `Ctrl+N` offered "Claude Code (not installed)" in your local project too, on a machine where `claude` was running at that moment. Nothing recovered from it either: detection only ever turns a tool *on*, so the grey-out survived until the client restarted and returned as soon as that host attached again.

Each daemon's answer is now filed against the daemon that gave it, and every consumer — the `Ctrl+N` list and its Enter gate, the palette, the context menu, the `Alt+G` / `Alt+D` overlays, `F1` → Plugins — names the machine it means. `Registry.SetAvailability`, the one global override, was deleted rather than merely left unused.

### v1.67.0: Codex — [plan](superpowers/plans/2026-09-04-codex-plugin.md)

`Ctrl+N` → Codex opens OpenAI's coding agent CLI in a folder of your choice, with setup toggles like Claude Code's. Quil registers its hook per pane through a `-c hooks=…` override carrying its own trust hash — codex runs only trusted hooks, and the hash is computed by Quil — so nothing under `~/.codex` is read or written and no trust prompt appears.

Everything the Claude Code plugin derives from hooks works for Codex: the notification sidebar, the work spinner and tab marks, subagent tracking, the model and context-token status segment, `Alt+Shift+I` input history, and per-pane session resume (`codex resume <id>`). A pane with no recorded session starts fresh — `resume --last` is never used, because on restore it finds the sibling pane that respawned a second earlier. Tier knob: `[notification.hooks] codex`.

### v1.68.0: Tab and Project Reordering

- **Dragging a tab no longer jumps around** — a tab moves only once the pointer passes the middle of the tab it is over. A narrow tab dragged over a wide one used to swap back and forth on every mouse move
- **`Alt+Shift+PgUp` / `Alt+Shift+PgDn`** slide the active tab one slot (`tab.move_left` / `tab.move_right`), also in the palette and F1 → Shortcuts
- **The project sidebar reorders too** — drag a tab name in the PANES list, or a project row, by the same midpoint rule measured in rows. `Alt+Shift+Up` / `Alt+Shift+Down` move the active project. Each daemon saves the order of its own projects; how several hosts' rows interleave is kept for the running session only
- **The Shortcuts list shows which row you are on** — `↑`/`↓` moved an invisible cursor, so the only sign a key had done anything was the list sliding once the cursor reached the edge
- **Wider dialogs force a repaint on open** — Shortcuts, Processes and What's New are drawn in a wider box than the About menu and asked for no repaint, so the old menu's border was left standing inside the new box

### v1.68.1: The 1x1 Resize Guard

A `quil` started with no terminal attached — from a script, or via an unrecognised flag such as `--version` — is reported by Bubble Tea as 1x1, and that size was pushed to every pane on every daemon. Claude Code, lazygit and every shell re-wrapped their entire transcript to a single column, permanently. Observed twice in production.

The TUI already refused to *paint* below 40x10; it now refuses to *send* pane sizes below the same threshold, at the boundary the value enters rather than where the symptom showed — the poisoned size had four doors, including the overlay outside the layout tree and the pair persisted in `workspace.json`. The daemon additionally declines a resize collapsing a pane to 1x1 in **both** dimensions, which no genuine split can ask for.

### v1.69.0: Notification Sidebar as a Timeline — [plan](superpowers/plans/2026-09-06-notification-timeline.md)

> Eight of twelve visible cards were "Output idle", with repeat counts past 2400.

- **`Output idle` and `Command completed` are off by default.** Both describe machine state rather than news, and because the queue merges repeats and re-prepends the merged entry, a repeat carrying a constant title jumps back to position 1 each time it fires — so the two of them permanently occupied the handful of rows the sidebar can draw
- **What is left is the timeline** — turns starting and finishing, permission prompts, processes exiting, panes closed or pinned, MCP agents taking a pane, worktrees finishing their checkout
- **Click a card to jump to its pane; scroll the list with the wheel.** Both gestures were previously swallowed. Cards name the project and tab they will take you to, shrink when they carry no excerpt so about twice as many fit, and read `(closed)` rather than offering a jump that silently does nothing
- **Ten event groups in `F1` → Settings → Notifications**, plus the desktop-toast switches, two of which were previously reachable only by hand-editing `config.toml`. Changes apply immediately; `a` reveals everything for a moment without changing the setting
- **Hiding a group never hides it from MCP agents** — an agent polling for "has this pane gone quiet" wants exactly what a human does not

### M19: Sandbox Panes — [ADR-31](architecture.md#adr-31-docker-sandbox-panes--the-mount-set-as-the-security-boundary), [guide](sandbox-panes.md)

> An AI pane running inside a Docker container, editing your real checkout.
> Unreleased.

`--dangerously-skip-permissions` is how an agent gets useful, and it is also how
an agent reaches every file you can. A sandbox pane keeps the first and removes
the second: the checkout is bind-mounted in, so edits and commits are real, but
the agent's filesystem reach stops at that checkout.

- **The mount set IS the boundary** — no `--privileged`, no `--cap-add`, no
  `--network` flag. The checkout is read-write; the repository's `.git` is
  mounted as a mountpoint (a mountpoint answers `EBUSY` to rename and remove)
  with `objects`, `hooks`, `config`, `config.worktree`, `modules` and
  `worktrees` pinned read-only on top. Those are values host git *executes* —
  without the pin, an agent rewrites `.git/config` with a `core.fsmonitor` and
  Quil's own git ticker runs it on the host
- **History cannot be destroyed from inside** — new objects go to a per-pane
  store with the repository's own store mounted read-only as an alternate. Quil
  copies new objects across every 30 s and again at pane close. Branch pointers
  are deliberately *not* read-only (that would make committing impossible) and
  are recoverable via reflog
- **Hooks keep working** — a Linux `quild` is mounted read-only into the
  container and the agent's hooks call it, so notifications, work-state
  indicators, input history and session resume behave exactly as they do
  locally. Each pane gets its own hook spool and its own agent config directory
- **Three agents** — Claude Code, Codex and OpenCode. Each authenticates the way
  its own vendor documents for containers: Claude Code forwards
  `CLAUDE_CODE_OAUTH_TOKEN` **by name** (Docker reads the value, so it never
  enters argv or a log) or signs in inside the container, chosen **per pane** in
  the create dialog; Codex has its `auth.json` copied in, because it ships no
  minting command; OpenCode signs in per container
- **The token flow needs no manual step** — a pane with no token drives
  `claude setup-token` itself under a pseudo-terminal, behind a daemon-wide
  single-flight so two panes never open two browser tabs, and stores the result
  in the OS environment store. Quil keeps no copy of a credential in any mode
- **Quil publishes no image and pulls none** — `scripts/sandbox-image.sh` builds
  one locally from `docker/sandbox/Dockerfile` and then *verifies* it provides a
  non-root user, the agent binary and `git`. `--with codex,opencode` adds the
  other agents. There is deliberately no default registry name: the
  official-looking `anthropics/claude-code` on Docker Hub is a security
  researcher's honeypot containing no Claude Code

**Known limits, documented rather than worked around:** egress is not bounded (a
default-deny firewall needs `NET_ADMIN`, which Quil does not grant); bind-mount
IO on Docker Desktop is ~20× slower and inotify does not cross it on Windows, so
file watchers need polling; repositories with submodules are unsupported.


---

## In Progress

### Remote Daemon Attach — [PRD](roadmap/remote-daemon.md) · **BETA**

> `quil --remote gpu01` — the panes run on another machine and keep running when the laptop sleeps.

**Phase 1 shipped (beta).** SSH transport with no port opened on the remote host: Quil runs `ssh -T <host> "quil --stdio"` and speaks its normal IPC protocol over that one channel, so bastions, Tailscale/WireGuard, and public-internet hosts all work with no extra setup. The destination is passed to `ssh` verbatim, so `~/.ssh/config` applies unchanged. New `internal/transport` package behind an `ipc.DialFunc` seam (`Local` + `SSH` backends), shaped so a TLS backend can be added later. Every local-daemon lifecycle command refuses under `--remote` rather than acting on the wrong machine, and the status bar carries `[remote <host>]`.

**Phase 2 shipped (v1.45.0), hardened (v1.45.1).** A dropped link is a pause, not an ending: banner on the top row naming the host and what `ssh` said, input frozen rather than queued, redial backing off 500 ms → 30 s with jitter, and every pane's emulator reset before the replay so scrollback is restored rather than doubled. An attempt only counts as restored once the far side answers — `ssh` reports success the moment its own binary starts, long before it has resolved or authenticated the host. v1.45.1 stops retrying on failures that cannot fix themselves (rejected key, changed host key), since every retry is a full login and an overnight loop can get the laptop banned by the server's brute-force protection; `r` resumes. Verification against a real link is **partial** — two of eight manual checks — so treat the behaviour as shipped but not fully proven.

**Phase 3 partly shipped.** The working-directory browser and git-repository
discovery now ask the daemon, so they describe the machine that holds the files
— along with `~` expansion, relative paths, path joins, and drive and root
listings, each of which was answering for the wrong machine. The reproducing
case was one screen making two contradictory claims about one path: `Alt+G`
reporting no repository in a directory where the agent in that very pane
answered `git status` with the branch name. Moving those strings onto the wire
also moved a trust boundary, so daemon-supplied names, paths and errors are
stripped of control sequences and bidi overrides at render.

Kube-context discovery, plugin availability and the recent-directories quick
pick followed. The recent list is now kept per remote host, and whether a
remembered directory still exists is asked of the daemon — a local check
dropped every server path, so the list rendered silently empty, which is
indistinguishable from a feature nobody had used. A remote daemon is also no
longer told the laptop's working directory as the place to spawn new panes.

**Known limits — see the [PRD](roadmap/remote-daemon.md#known-limits) for the full table:**
- Plugin availability comes from the server now, but the daemon detects installed tools only at startup and on plugin reload, so a tool installed mid-session stays greyed until one of those happens — *Phase 3 (RD-023, partial)*
- Plugin *definitions* are still read from the local machine, so a plugin the server defines and your machine does not cannot be offered — *unassigned*
- `quil status` and the update controls are blocked rather than silently targeting the wrong host — *Phase 3 (RD-026/RD-027, both now decided: status gains remote support, update controls target the remote)*
- Six of eight reconnect manual checks are outstanding, chiefly reconnecting a pane whose plugin keeps no ghost buffer — *Phase 2 close-out*

**Planned:** the rest of Phase 3 (`quil status` and update controls over the
transport, plugin definitions served by the daemon) → Phase 4 mTLS transport,
which the dialer seam already anticipates and which is the prerequisite for
anything web-facing (M18 #18–19).

### M5: Polish
> Production-quality UX, plugin refinements, observability, encrypted tokens.

**Completed:**
- Default TOML plugins — claude-code, ssh, stripe shipped as embedded editable TOML files
- Plugin instance management — saved SSH connections, Stripe webhooks persisted to `instances.json`
- Plugin management UI — F1 → Plugins with view, reload, restore defaults, in-app TOML editor
- In-app TOML editor — full-screen editor with syntax highlighting and validation
- Pane creation dialog extended — 5-step flow: category → plugin → instance/form → setup (CWD/toggles) → split direction
- Centralized snapshot queue — event-driven with 500ms debounce, replaces scattered calls
- Per-plugin ghost buffer toggle — `ghost_buffer` bool controls PTY output persistence
- GhostSnap restore — clean ghost buffer replay after daemon restart
- Diagnostic logging — trace-level logging across daemon, TUI, and IPC
- Plugin configuration reference — comprehensive docs for custom plugin creation
- **Pane setup dialog** (ADR-15) — opt-in directory browser + runtime toggle checkboxes via `prompts_cwd` / `[[command.toggles]]`. Claude Code uses both: pre-fills CWD from active pane's OSC 7 directory and offers a `Dangerously skip permissions` toggle. Daemon-side CWD validation re-runs `EvalSymlinks` to defend the spawn against TOCTOU swaps. The `preassign_id` resume strategy now appends `ResumeArgs` to `InstanceArgs` instead of replacing, so toggle args survive a daemon restart.
- **Spatial pane navigation** (ADR-16) — `Alt+Left/Right/Up/Down` replaces linear `Tab`/`Shift+Tab` cycling. Picks the closest neighbour in the requested direction with three tie-breakers (gap, overlap, perpendicular center distance). Tab/Shift+Tab now fall through to the PTY so shell completion and Claude Code's mode-cycling work naturally. Splits moved to `Alt+Shift+H/V` so `Alt+V` reaches Claude Code's image paste.
- **Win32 clipboard image paste proxy** (ADR-17) — Quil decodes the OS clipboard image itself (`CF_DIBV5` / `CF_DIB` → DIB parser → PNG), saves it under `~/.quil/paste/quil-paste-<ts>-<rand>.png` with owner-only `0o600`/`0o700` permissions, and types the absolute path into the active pane. Sidesteps the upstream Claude Code Windows clipboard bug ([anthropics/claude-code#32791](https://github.com/anthropics/claude-code/issues/32791)). New `Ctrl+Alt+V` and `F8` paste aliases — Windows Terminal eats `Ctrl+V` before it reaches the TUI.
- **Leveled logger** (ADR-18) — `internal/logger` wraps stdlib `slog` and bridges all 152 existing `log.Printf` sites at info level so old and new code respect a single filter. Configurable via `[logging] level` (`debug`/`info`/`warn`/`error`). Hot-path `Debug` calls pre-check `slog.Enabled` to skip `fmt.Sprintf` when filtered.
- **Read-only F1 log viewer** (ADR-19) — three new menu items (`View client log` / `View daemon log` / `View MCP logs`) reuse the existing `TextEditor` with a new `ReadOnly` flag that gates every mutation path. Tail-reads up to 256 KB at line boundaries with `[... older lines truncated ...]` marker. Symlink-rejecting via `os.Lstat`, plus a re-stat through the open handle defeats TOCTOU swap. `Alt+Up`/`Alt+Down` page navigation jumps by `[ui] log_viewer_page_lines` (default 40, configurable).
- **Project rule: dev-environment isolation** — `.claude/rules/dev-environment.md` documents the production isolation constraint for Quil's own contributors (Quil-on-Quil dogfooding).
- **Settings dialog persistence fix** — every Settings field now flips `configChanged` so edits actually persist on TUI exit (previously only the disclaimer setter did, silently dropping the rest).
- **Plugin registry hardening** — `LoadFromDir` prunes stale plugins on reload (deleted TOML files no longer leak in-memory entries); `validateSeverity` helper extracted; `searchBinary` Windows-PATH walk gated by `runtime.GOOS == "windows"`.
- **DIB parser hardened** (ADR-17 supporting) — per-axis cap (`maxDIBDimension = 16384`) plus `uint64` stride math defends against crafted clipboard payloads that slip under the 64 MB byte cap but would otherwise allocate gigabytes during decode.
- **Log rotation** — `MaxSizeMB`/`MaxFiles` now honored via `internal/logger/rotate.go` (`RotatingWriter`). Active `quild.log` / `quil.log` rotates to `stem-YYYYMMDD-HHMMSS.log` on size breach; newest `MaxFiles` archives kept, older pruned. Defaults: 5 MB / 10 files. No external dependency.
- **Observability — `quil status`** — top-level command (alias `quil daemon status`) reporting daemon liveness, pid, version, environment, approximate uptime, and per-tab/pane session metrics (state + memory), with `--json` for scripting. Exit codes distinguish healthy / not-running / wedged.
- **Mouse split-border drag-resize** ([PR #93](https://github.com/artyomsv/quil/pull/93)) — click and drag any border between panes to move the split. Ratio clamped in cells against subtree minimums (10×4 floor, nested splits included); adjacent panes show a transient highlight; PTY resize + layout persistence fire once on release (mid-drag only rects move — unpaired emulator resizes garble content). Border hit zone widened and given priority over the scrollbar's overlap cells. Disabled in focus/notes modes.
- **Claude Code resume-session picker** ([PR #105](https://github.com/artyomsv/quil/pull/105)) — the pane setup dialog gains a **Session** field (opt-in per plugin via `[command] sessions = "claude"`) listing the transcripts recorded for the selected folder, newest first, each row titled with the first prompt typed in that session. Picking one spawns `claude --resume <id>` in place of the `preassign_id` strategy's `--session-id <new>`; toggles still compose, and the pane joins the normal restore machinery from the first instant. New `internal/claudesessions` package owns Claude's `~/.claude/projects/<escaped-cwd>` naming rule (moved out of the daemon, so the rule that silently breaks restore when wrong exists once). Sessions held by a live pane are listed but blocked — the guarantee is enforced daemon-side under a claim mutex, not just greyed in the UI, since any IPC client can send an id directly. Press `i` on a row for details: start/last-used timestamps, typed-prompt count, transcript size, and the first and last prompts — read on demand per session, streaming the whole transcript but rejecting each line by byte-compare before any JSON parsing (88 MB in ~1 s).
- **Setup-dialog width accounting fix** (shipped with the above) — every content budget in the pane setup dialog reserved the box's padding but not its border, which lipgloss counts inside `Style.Width`; rows that filled their budget sat two cells past the wrap limit and reflow dropped the last word onto its own line. Session rows, the working-directory pick list, and the footer hints now all derive from one `setupTextWidth()`, pinned to lipgloss's actual behaviour by a two-sided test.
- **Client CWD propagation** (v1.11.0) — the TUI sends `os.Getwd()` on attach so new panes and tabs spawn in the client's directory, not the daemon's frozen-at-spawn-time one.
- **Tab drag-to-reorder, scrollbar drag, multi-binding keys** (v1.15.0) — drag a tab along the bar to reorder it (daemon stays authoritative, so a stale client never has to race for an accurate tab count); click-and-drag the scrollbar thumb with a 3-cell hit zone; any keybinding accepts comma-separated alternatives (`rename_pane = "alt+f2,alt+shift+r"` — F2 is eaten by macOS by default).
- **Notification broadcast hardening** (v1.16.0) — events carry the triggering output lines as an excerpt; per-pane mute (`Alt+M`) drops events at the source.
- **Per-pane restore activity indicator** (v1.26.0) — a checklist shows what the daemon is doing while a restored pane comes back, instead of an opaque wait.
- **Stop daemon moved to the About root menu** (v1.29.0) — behind a `y`-only confirm, deliberately not `Enter`, so finger memory cannot kill every pane child.
- **Wide-canvas terminal variant** — `terminal-wide` built-in ("Terminal (keeps content on squeeze)"): same shell auto-detection as the native terminal, but the pane keeps its PTY/emulator on the window-sized canvas below `min_native_cols` (like AI panes), so pane squeezes never cut content — at the cost of window-width output formatting while narrow. Native terminal remains the default for interactive work; proper reflow-on-resize is tracked below.

**Remaining:**
- JSON transformer (`Ctrl+J`) — format and highlight JSON in terminal output
- Encrypted token storage — OS keyring integration for sensitive scraped values.
  The `[security]` config section that reserved a name for this
  (`encrypt_tokens`, alongside `redact_secrets`) was **removed**: both defaulted
  to `true` and neither was ever read, so the file advertised two guarantees
  nothing provided. Whoever builds this introduces the key then. `redact_secrets`
  is not coming back — MCP log redaction is unconditional on purpose, and an off
  switch for secret redaction is an anti-feature. An old `config.toml` still
  carrying the section loads fine; the table is ignored
- Tab dock positions (top/bottom/left/right)
- OS service integration (`quil service install` — systemd/launchd/Task Scheduler)

---

## Planned — Core Features

### M9: Project Workspace Files — [PRD](roadmap/workspace-files.md)

> `.quil.toml` checked into repo — the "docker-compose.yml for dev environments."

Define workspace blueprints committed to git: tabs, panes, plugins, CWDs, commands. `cd my-project && quil` materializes the entire dev environment. Every team member gets the exact same setup. **Network effect within teams.**

> M11 (Command Palette) shipped in v1.39.0–v1.40.0 — see **Completed** above.

---

## Planned — Growth & Adoption

### The "Holy Shit" Demo — [PRD](roadmap/demo-gif.md)

> 30-second GIF: 5 panes → reboot → `quil` → everything snaps back.

The entire pitch in one visual. Goes on README, Hacker News, r/programming, Twitter/X. Adoption for developer tools is driven by a single viral moment. **Priority 2** — prerequisite for marketing.

### Community Plugin Registry — [PRD](roadmap/community-plugins.md)

> `quil plugin install aider` — community TOML plugins via GitHub.

GitHub repo as registry, `quil plugin install/search/update` CLI. lazygit, k9s, and lazysql now ship as built-ins (v1.22.0–v1.28.0), which is exactly the argument for a registry: each one cost a PR, and the queue behind them — Aider, Docker Compose, ngrok, pgcli, and whatever ships next month — should not. Every plugin makes Quil useful to a new audience.

### Native Docs on quil.cc

> Render the `docs/` markdown tree as first-class pages on the existing site.

[quil.cc](https://quil.cc) (Astro under `site/`, deployed to GitHub Pages by `site.yml` on master pushes touching `site/**`) already covers marketing: landing, features catalog, install, plugins, blog, comparisons. Its `/docs` page is a **link hub** that points back to the markdown on GitHub. The gap: render `docs/*.md` natively on the site (Astro content collections over the existing tree) so keybindings, configuration, plugin reference, and MCP guide are searchable, linkable pages with site navigation — no GitHub round-trip. Requires keeping `docs/` as the single source of truth (site consumes, never forks, the markdown). Also add a docs-freshness check: site deploys only trigger on `site/**`, so feature PRs that touch `docs/` or the feature catalog data layer must remember quil.cc (this PR added the drag-resize entry to `site/src/data/features.ts` by hand — content collections would make `docs/` changes flow automatically).

### tmux Migration Path — [PRD](roadmap/tmux-migration.md) · **keymap half shipped**

> Import keybindings and session layouts from tmux.

**The keymap half shipped in v1.62.0**, as a *preset* rather than an importer:
`preset = "tmux"` in `~/.quil/bindings.toml` selects a tmux-compatible layout,
`prefix = "ctrl+a"` moves the prefix in one line, and `[bindings]` overrides any
single action. A shipped preset beats parsing `~/.tmux.conf`: one readable line,
no parser for tmux's config grammar, and it follows future Quil defaults. See
[tmux comparison](tmux-comparison.md) for how it lines up against tmux's own
prefix table.

**Still planned:** reading a *customised* `.tmux.conf` on top of the preset, and
`quil import-session` — snapshotting a running tmux session's windows, panes and
directories into a Quil workspace. tmux has millions of users; making the switch
painless is the fastest acquisition channel.

---

## Planned — Advanced Features

### Smart Process Health & Auto-Restart — [PRD](roadmap/process-health.md)

> Green/yellow/red health indicators, auto-restart with backoff, stale detection.

Elevate `error_handlers` to a first-class health monitoring system. Auto-restart crashed panes with exponential backoff, detect stale processes, fire desktop notifications. Plugin TOML `[health]` section for configuration. Moves Quil from "terminal organizer" to "workflow orchestrator."

### Cross-Pane Context Awareness — [PRD](roadmap/cross-pane-events.md)

> Build fails → AI pane gets a toast → one keypress sends context.

Event bus connecting panes: build errors notify AI assistants, SSH auto-reconnects, test passes flash green, webhook counters badge tabs. Creates an **integrated experience** that no collection of separate terminals can match.

### Terminal Reflow-on-Resize

> Rewrap terminal pane content on width change, like tmux and Windows Terminal.

Today a terminal pane's emulator crops on width shrink and ConPTY re-emits only the viewport, so content cut by a squeeze is not restored on growing back (`techdebt/3-5-terminal-vt-resize-reflow.md`). The fix is a reflow engine in Quil's emulator layer: rewrap screen + scrollback from soft-wrap continuation flags on width change (upstream `charmbracelet/x/vt` feature or a wrapper). Gives the native terminal the content preservation of `terminal-wide` without its formatting trade-offs — the endgame that makes both terminal variants converge. Epic-sized: wide-char cells, cursor remapping, alt-screen exclusion.

### Session Sharing — [PRD](roadmap/session-sharing.md)

> `quil serve --share` / `quil attach --host` for pair programming.

Remote workspace viewing and collaboration over TCP+TLS. Read-only by default, collaborative mode optional. tmux session sharing but with project context, typed panes, and AI session awareness.

---

## Planned — Competitive Gaps (herdr, AoE)

> Derived from the [competitive analysis](competitive-analysis.md) of the two
> closest direct competitors — [herdr](https://github.com/herdrdev/herdr)
> and [Agent of Empires](https://github.com/agent-of-empires/agent-of-empires).
> These are the 20 most interesting capabilities they ship that Quil lacks or
> only partially supports, grouped into candidate milestones. Several extend
> work already planned above — those are cross-referenced, not duplicated.
> Ghostty is covered in the same document under *The Ghostty axis*, with its own
> shortlist (G1–G7); it is not a rival, so its items are not ranked in here.

Both competitors are agent-orchestrators like Quil, but each is broader in one
axis: herdr on **agent breadth + scriptable extensibility**, AoE on **web/remote
access + sandboxing**. Quil already leads on the **MCP server**, **pane notes**,
**memory reporting**, **per-pane input history**, and **in-app auto-update** —
those are moats to defend, not gaps to close.

**Windows is no longer the clean moat it was.** As of the 2026-09-10 re-read herdr
calls native Windows generally available, so "they are beta, we are not" is now a
false claim and must not be repeated. What survives is narrower and still real: on
herdr there is no direct terminal attach, no live server handoff and no clipboard
image bridge in local native panes, while Quil's bundled OpenConsole fix for
Windows 10 and its Win32 clipboard image-paste proxy remain genuine advantages.
AoE is still WSL2-only. **Windows-as-a-remote-host is not a Quil advantage** —
herdr excludes it and so does Quil (see [remote-daemon](roadmap/remote-daemon.md):
no `uname`, no `sh`, and a running Windows executable cannot be replaced in place).

### M14: Agent Fleet — breadth + detection *(highest ROI)*

The starkest deficit: rivals detect 13–24 agents out of the box; Quil ships 3.
Re-checked 2026-09-10 — herdr had grown from 20+ to 24+ since the original
capture, so this gap is widening rather than holding.

1. **Screen-content agent state detection** — infer blocked/working/done from
   terminal output for *any* agent, with no hooks required (herdr ships updatable
   TOML detection manifests). *Partly closed:* Quil now derives working / blocked /
   done from **hook events** for Claude Code and OpenCode (v1.18.0–v1.19.0, see
   Completed above), which is more accurate than screen scraping but only works for
   agents Quil ships an integration for. The gap that remains is the hookless
   fallback — output heuristics that generalise to an agent nobody wrote an
   integration for. Extends [process-health](roadmap/process-health.md).
2. **Broad agent support + detection registry** — Codex, Gemini, Cursor, Copilot,
   Droid, Devin, Kimi, and more, detected by process name + output heuristics.
   Still the starkest deficit: Quil's agent integrations remain Claude Code and
   OpenCode.
3. **One-command agent integration installer** — `quil integration install
   <agent>` writes the agent's hooks/settings for you (both rivals do this).
   *Partly closed by a different route:* Quil installs its hooks **per spawn**
   via inline `--settings` / `OPENCODE_CONFIG_CONTENT` and never writes to the
   agent's own config, so there is nothing to install for the two supported
   agents. A registry-driven installer only becomes necessary at #2's breadth.
4. **Session fork** — branch a conversation into a new independent session, parent
   untouched (AoE).

### M15: Git Workflow — worktrees + review *(high ROI)*

The most-cited reason people adopt these tools: parallel agents on branches, then
review the diff.

5. ~~**Git worktree-per-session**~~ — **shipped, v1.51.0–v1.59.0.** The
   create-pane and new-tab dialogs list a repository's existing worktrees and
   offer `+ new branch…`, which runs `git worktree add -b` off the repository's
   *default* branch and spawns the pane inside the result. Cleanup is offered on
   close rather than automatic — an unticked, `space`-armed row on the confirm,
   only for a worktree Quil created, with a live count of what would be lost
   (ignored files included) and the branch always kept. See
   [Features → Spawn a pane in a worktree](features.md#spawn-a-pane-in-a-worktree).
   **Still open:** the automation layer proper — a session template that opens
   branch, agent, shell and diff viewer together, which extends
   [workspace-files](roadmap/workspace-files.md).
6. ~~**Built-in diff viewer**~~ — **shipped as an integration, v1.57.0.** `Alt+D`
   toggles a full-tab [hunk](https://github.com/modem-dev/hunk) review of the
   working tree for the repo behind the active pane, sharing one overlay slot
   with lazygit's `Alt+G`. Reviewing and committing happen in those tools rather
   than in a viewer Quil owns, which is the deliberate trade: no diff engine to
   maintain, but no place to hang #7 either.
7. **Inline diff comments → prompt to agent** — annotate a diff; comments assemble
   into one prompt back to the agent (AoE). Builds on #6.
8. **Multi-repo workspaces** — one session/branch spanning several repos (AoE).
   Extends [workspace-files](roadmap/workspace-files.md).

### M16: Extensibility & scripting

9. **Executable/scriptable plugins** — any-language plugins with actions, event
   hooks, and link handlers, not just declarative pane types (both rivals). The
   biggest lever for a real ecosystem. Extends
   [community-plugins](roadmap/community-plugins.md) and
   [cross-pane-events](roadmap/cross-pane-events.md).
10. **Plugin marketplace** — GitHub-topic index + `quil plugin install owner/repo`.
    Already partly planned in [community-plugins](roadmap/community-plugins.md).
11. **General shell CLI to script the multiplexer** — `quil pane split`,
    `quil tab create`, `quil pane run` from any script. MCP serves AI agents;
    humans and shell scripts currently have no equivalent.
12. **Repo config + lifecycle hooks** — per-project `.quil.toml` with
    `on_create` / `on_launch` / `on_destroy` hooks (AoE). Natural extension of
    [workspace-files](roadmap/workspace-files.md).

### M17: Notifications & polish *(cheap, immediately felt)*

13. **Sound notifications** — audible cue when an agent needs you (both rivals).
    Extends [notification-center](roadmap/notification-center.md).
14. ~~**OS / desktop notifications**~~ — **shipped for Windows.** Toasts fire on
    the sidebar's own attention states (parked ▲ / finished ✓) for any pane you
    are not looking at — another tab, another project or another application —
    and clicking one routes to that exact project, tab and pane through a
    registered `quil://` handler. Opt-in via
    `quil notify setup`; see
    [Features → Desktop notifications](features.md#desktop-notifications).
    **Still open:** macOS and Linux. The original sketch here named OSC /
    `notify-send` / `terminal-notifier` — none of those can carry a click back
    to a specific pane, so they would be a different, lesser feature rather
    than the same one ported, and shipping them under the same name would
    overstate what they do. Raising the terminal window on click is also still
    open: `GetConsoleWindow` returns a ConPTY ghost under Windows Terminal, and
    WT exposes no way to select a tab within a window.
15. **Themes + light/dark auto-switch** — multiple presets that follow the host's
    OSC 10/11 colors (herdr ships 18, AoE 8). Quil's theming is minimal today.
16. **Session lifecycle management** — auto-stop idle sessions plus
    groups / favorites / snooze / archive to keep a large fleet tidy (AoE).

### M18: Remote & Web *(largest builds; deliberate "later")*

Where AoE is pulling away for the mobile/remote crowd. Sequenced last because each
is a major surface and cuts against Quil's TUI/Windows-native focus.

17. ~~**Remote SSH thin-client attach**~~ — **Phases 1 and 2 shipped (beta),
    Phase 3 shipped except two items**, see
    [Remote Daemon Attach](roadmap/remote-daemon.md) under *In Progress*.
    `quil --remote host` works today, survives a dropped link, and resolves
    every picker that reads a filesystem or probes a binary against the
    server: the directory browser, git repositories, kube contexts, plugin
    availability and the recent-directories list (v1.46.0). Still refused
    rather than retargeted: `quil status` (RD-026) and the update controls
    (RD-027). Bridging local clipboard image paste into remote agents (herdr)
    did **not** land with Phase 3 and has no registry item yet — the PNG is
    still written locally and a local path typed into a remote pane.
18. **Web dashboard** — real terminal + diffs in the browser, installable as a PWA
    (AoE). The single largest surface Quil is missing. **Gated on Phase 4
    (mTLS)** — a browser cannot speak `ssh -T host "quil --stdio"`, so
    something has to terminate a network connection carrying real client
    identity before this can start. Feasibility was investigated on
    2026-08-15 and the answer is yes — see
    [Browser UI](roadmap/browser-ui.md) for why the architecture already
    suits it (the daemon streams raw PTY bytes; VT emulation is client-side,
    so xterm.js is a drop-in consumer), the three blockers, and a staged
    path whose first stage is read-only and needs no protocol change.
19. **Remote phone access** — expose the dashboard over a Tailscale/Cloudflare
    tunnel with QR + passphrase pairing and Web Push (AoE). Builds on #18.
20. ~~**Container sandboxing**~~ — **shipped**, see
    [M19: Sandbox Panes](#m19-sandbox-panes--adr-31-guide) above. Docker only;
    Podman is untested. Auth is per-pane rather than a shared volume: Claude
    Code forwards a token by name or signs in in-container (your choice per
    pane), Codex has its credential copied in, OpenCode signs in per container.
    A shared Claude config directory is available (`shared_claude_config`) but
    off by default, because it merges every sandbox pane into one trust domain.

---

## Investigated and Rejected

Directions that were explored, measured, and deliberately not taken. Recorded so the
question is not re-opened from intuition.

- **Rewriting the daemon in Rust or Zig** (2026-08-15) —
  [investigation](roadmap/daemon-language-rewrite.md). Rejected: the hot path is
  bottlenecked by a wire-protocol decision, not by the language. `encoding/json`
  re-scanning an already-encoded payload was 99% of encode time, and removing it in Go
  is a 42× encode / 11.5× decode improvement on a byte-identical wire. The document
  lists the four conditions that would re-open the question.

## Priority Matrix

Reordered so the highest-ROI competitive gaps sit alongside the original core and
growth items. Competitive-gap rows are tagged **[gap]** and cross-reference the
section above.

| Priority | Feature | Effort | Impact | Category |
|----------|---------|--------|--------|----------|
| ~~1~~ | ~~Pre-built binaries + one-line install~~ | ~~Small~~ | ~~Critical~~ | ~~Done~~ |
| ~~—~~ | ~~MCP server for AI integration~~ | ~~Medium~~ | ~~Very High~~ | ~~Done~~ |
| ~~—~~ | ~~Notification center (sidebar + pane history)~~ | ~~Medium~~ | ~~High~~ | ~~Done~~ |
| 2 | "Holy Shit" demo GIF/video | Small | Critical | Growth |
| 3 | **[gap]** Broad agent support + hookless state detection (M14) — hook-based detection for Claude Code / OpenCode already ships | Medium | Very High | Core |
| 4 | **[gap]** Git worktree-per-session + diff viewer (M15) | Medium | Very High | Core |
| 5 | Project workspace files (`.quil.toml`) + repo hooks **[gap #19]** | Medium | Very High | Core |
| ~~6~~ | ~~Command palette (`Alt+Shift+P`)~~ | ~~Medium~~ | ~~High~~ | ~~Done (v1)~~ |
| 7 | **[gap]** Sound notifications (M17) — desktop toasts shipped for Windows; sound, macOS and Linux remain | Small | Medium | Polish |
| 8 | **[gap]** General shell CLI to script panes | Medium | High | Core |
| 9 | Community plugin registry + executable plugins **[gap]** | Medium | High | Growth |
| 10 | Smart health monitoring + auto-restart (feeds M14 detection) | Medium | High | Advanced |
| 11 | tmux keybinding import | Small | Medium | Growth |
| 12 | **[gap]** Themes + light/dark auto-switch | Small–Med | Medium | Polish |
| 13 | Cross-pane context / event bus | Large | High | Advanced |
| 14 | **[gap]** Remote SSH thin-client attach | Large | Medium | Advanced |
| 15 | Session sharing | Large | Medium | Advanced |
| 16 | **[gap]** Web dashboard + remote phone access (M18) | Large | Medium | Advanced |
| ~~17~~ | ~~**[gap]** Container sandboxing~~ | ~~Large~~ | ~~Medium~~ | ~~Done (M19)~~ |
| 18 | Native docs rendering on quil.cc | Small | Medium | Growth |
| 19 | Terminal reflow-on-resize | Large | Medium | Polish |

## Strategic Notes

### The Developer Pain (Layered)

| Layer | Pain | Who Feels It |
|-------|------|-------------|
| 1. Context destruction | Reboot = 10-15 min of manual reconstruction | Every multi-terminal developer |
| 2. AI session loss | Losing a Claude conversation means losing reasoning context worth hours | AI-native developers (growing fast) |
| 3. Project fragmentation | 5 terminals + 3 tools + 2 SSH = no single "project view" | Team leads, senior engineers |
| 4. Onboarding friction | "How do I run this?" → README with 8 terminal commands | New team members, OSS contributors |
| 5. Cross-tool blindness | AI assistant can't see the build error in the next pane | Everyone using AI coding tools |

Quil currently solves layers 1-3 well. **Layers 4-5 are where the breakout potential lives.**

Items 1-2 (install + demo) cost almost nothing and are **prerequisites for everything else**. Items 3 (workspace files) and 5 (MCP) are the **strategic differentiators** — workspace files create team adoption and MCP creates the "AI-native" moat that no other multiplexer can claim.

### Feature Synergies

The **notification center** (M12), **process health** (advanced), and **cross-pane events** (advanced) form a layered system:

| Layer | Feature | Status | Role |
|-------|---------|--------|------|
| UI | Notification Center (M12) | Shipped | Sidebar, pane navigation, history stack |
| Signal | Hook-events pipeline (v1.18.0) | Shipped | Agent-reported work state, model/context, input history |
| Monitoring | Process Health | Planned | Health states, auto-restart, stale detection |
| Orchestration | Cross-Pane Events | Planned | Event bus, pane-to-pane context passing |

M12 shipped first as a standalone feature (process exit + output patterns). The hook-events pipeline then landed underneath it, feeding structured agent-reported events into the same queue — which is why work-state indicators, the model/context status segment, and per-pane input history all reuse it with no new IPC. Process health and cross-pane events are the remaining two layers; both extend the same queue rather than replacing it.
