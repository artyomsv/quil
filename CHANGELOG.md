# Changelog

All notable changes to Quil will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.10.2] - 2026-04-26

### Fixed

- **Daemon `Stop()` leaked goroutines on programmatic shutdown** — `Stop()` tore down the IPC server and snapshot machinery but never closed `d.shutdown`, so `idleChecker`, the memreport ctx-bridge, and `sendGhostChunked` workers stayed alive until process exit on any Stop path that didn't go through `MsgShutdown`. `Stop()` now closes the channel via `shutdownOnce` as its first action and wraps the rest in `stopOnce` for full idempotency. The IPC server is now also stopped before the final snapshot so a late-arriving `MsgCreatePane`/`MsgDestroyPane` cannot be ACK'd to a client after the on-disk snapshot is sealed.
- **Snapshot pane-count inconsistency between `workspace.json` and ghost buffers** — `snapshot()` called `SessionManager.SnapshotState()` twice (once via `buildWorkspaceState`, once for the buffer-flush loop). A pane create/destroy slipping between the two atomic reads produced an off-by-one mismatch on disk. The two halves now share a single snapshot via the new `workspaceStateFromSnapshot` helper. The periodic 30 s ticker still calls `snapshot()` directly so the safety-net write cannot be starved by sustained event-driven traffic resetting the debounce timer.
- **`paneSourceAdapter` could observe a torn pane state** — the memreport collector called six methods per pane per tick, each acquiring `PluginMu` independently. Under interleaving with a pane-exit write, the trio (`Alive`, `PID`, plugin-state size) could be inconsistent — e.g. "alive with PID 0". The seven-method `PaneSource` interface collapses into a single `Snapshot() PaneSourceSnapshot` call that takes `PluginMu` once (with `defer Unlock` for panic safety) and returns a frozen value type.

### Changed

- **MCP `get_memory_report` halves its IPC latency** — the daemon now embeds the current tab list (`Tabs []TabInfo`) directly in `MemoryReportRespPayload`, eliminating the second `MsgListTabsReq` round-trip and the tab create/destroy race window between the two requests. The MCP bridge falls back to bare tab IDs against pre-1.10 daemons during a rolling upgrade.
- **Notes editor focus indicator is now non-subtle** — when the pane-notes editor (Alt+E) is open, the header carries a persistent reverse-video badge: `INPUT` on bright blue when keystrokes route to the editor, `PANE` on orange when they route to the bound PTY. Border colour alone was easy to miss in peripheral vision, leaving a defence-in-depth gap against synthesised mouse-click focus redirection. At narrow widths the badge degrades to single-letter form (`I` / `P`) before falling back to an empty header — never to a corrupted partial that would give the same visual on both sides. Implementation uses explicit `Background`+`Foreground` rather than `Reverse(true)` so the fill colour is stable across terminal themes.

## [1.10.1] - 2026-04-25

## [1.10.0] - 2026-04-24

### Added

- **Notes editor: soft-wrap** — long lines in the pane-notes editor (Alt+E) now wrap onto the next visual row instead of being hard-truncated at the column edge with a trailing `~`. Character-wrap (not word-wrap), opt-in per editor via a new `TextEditor.SoftWrap` flag — the TOML plugin editor and F1 log viewer keep their existing truncation. Cursor Up/Down walks visual rows with column preservation; Home/End snap to the current visual row; Shift-arrow selections stay contiguous across wrap boundaries. Mouse clicks on a wrapped continuation row now resolve to the correct logical column via a new `visualToLogical` helper in `notesEditorPosAt`. Internals: `visualLayout(contentW) []visualRow` drives rendering, scroll (`ScrollTop` reinterpreted as a visual-row index when wrap is on), and navigation from a single source of truth.

### Fixed

- **End-of-line cursor invisible past a shorter selection** — in `renderLineWithSelection`, when the cursor sat at end-of-line and the selection ended earlier on the same row, the padding math reserved a cell for the cursor but never painted a reverse-video glyph on it. The cursor now renders correctly in that state. Pre-existing bug exposed more often by the new soft-wrap path.

## [1.9.2] - 2026-04-23

### Fixed

- **claude-code: session-id rotation tracking** — `/clear`, `/resume`, and compaction rotate Claude's session id to a new jsonl file. Before this fix, the daemon kept resuming the preassigned jsonl after a restart, silently restoring the pre-rotation conversation and discarding the user's post-rotation work. Quil now registers a `SessionStart` hook via `claude --settings '<inline JSON>'` at every spawn (never touches `~/.claude/settings.json`) and passes `QUIL_PANE_ID=<paneID>` in the PTY env; the hook script — shipped in `$QUIL_HOME/claudehook/` and written atomically on daemon start — writes the live session id to `$QUIL_HOME/sessions/<paneID>.id` on every rotation. `resumeTemplateFor` consults this file on restore (snapshotting `PluginState["session_id"]` under `PluginMu` before the disk probe) and resumes the current session with per-pane attribution. Hardening: `ValidateQuilDir` rejects shell-unsafe paths before hook install, `ReadPersistedSessionID` rejects pane ids containing path separators and caps reads at 256 bytes, scripts validate the extracted id matches a uuid regex before persisting and log failures to `$QUIL_HOME/claudehook/hook.log`, missing script on disk is detected at spawn time (`claudeHookSpawnPrep`) so the spawn falls back to the pre-feature behaviour instead of silently registering a dead hook. Introduces `internal/claudehook/` package with embedded sh + ps1 scripts.

## [1.9.1] - 2026-04-22

### Fixed

- **TUI freeze on claude-code pane creation** — creating a new claude-code pane could hard-wedge the Bubble Tea main goroutine, requiring a client kill. Root cause: `charmbracelet/x/vt`'s `Emulator.handleRequestMode` writes DECRQM replies to an unbuffered `io.Pipe`. Quil uses the emulator as a renderer only (ConPTY is the real terminal), so nobody drained the pipe — when claude-code sent a mode query, `SafeEmulator.Write` blocked forever *inside* Update, under its own mutex. Fix: per-pane goroutine in `internal/tui/pane.go` that reads and discards emulator replies; shutdown via `em.Close()` → `io.EOF`, wired into `ResetVT` so no goroutine leaks on VT reset. Any TUI pane running software that probes terminal modes is covered.

### Added

- **Stuck-Update watchdog + breadcrumbs** — `internal/tui/watchdog.go` launches a process-lifetime goroutine that ticks every 2 s and, if a Bubble Tea Update has been in flight for more than 10 s, writes `runtime.Stack(buf, true)` to the log. Memoized per start-ns so one wedge produces exactly one dump; `sync.Pool` reuses the 1 MiB buffer. Eight new `apply: ...` breadcrumb log lines bracket each step of `applyWorkspaceState` and the `WorkspaceStateMsg` handler so the next wedge pinpoints the line that hung to within one statement. Seven white-box tests in `watchdog_test.go` cover the logic kernel via an injected clock/stack/logger.
- **Memory reporting** — F1 → Memory opens a collapsible tab/pane tree showing Go-heap (ring buffer + ghost snapshot + plugin state), PTY child resident memory, and notes-editor bytes per pane. The status bar gains a `mem <n>` segment updated every 5 s from a new daemon-side collector (`internal/memreport/`). Cross-platform PTY RSS: `/proc/<pid>/status` on Linux, `ps -o rss=` batched on Darwin, `GetProcessMemoryInfo` on Windows. Two new MCP tools — `get_memory_report` (per-tab totals + grand total) and `get_pane_memory` (single pane detail) — expose daemon-side layers for external agents. Spec at `docs/superpowers/specs/2026-04-20-memory-reporting-design.md`, plan at `docs/superpowers/plans/2026-04-20-memory-reporting.md`.
- **claude-code: per-pane resume** — multi-pane Claude sessions sharing a working directory now reattach to their own session on restore, instead of all converging on claude's "most recent in cwd" lookup. On restart, the daemon checks `~/.claude/projects/<escaped-cwd>/<session_id>.jsonl`; if present, it promotes the pane's resume args to `--resume <uuid>`. Otherwise (pane closed during claude's startup screens before any exchange persisted a session file), it falls back to `--continue`. Plugin schema bumped to v4 — users with edited `~/.quil/plugins/claude-code.toml` get the standard side-by-side migration dialog on next launch.

## [1.9.0] - 2026-04-20

### Added

- **Memory reporting** — F1 → Memory opens a collapsible tab/pane tree showing Go-heap (ring buffer + ghost snapshot + plugin state), PTY child resident memory, and notes-editor bytes per pane. The status bar gains a `mem <n>` segment updated every 5 s from a new daemon-side collector (`internal/memreport/`). Cross-platform PTY RSS: `/proc/<pid>/status` on Linux, `ps -o rss=` batched on Darwin, `GetProcessMemoryInfo` on Windows. Two new MCP tools — `get_memory_report` (per-tab totals + grand total) and `get_pane_memory` (single pane detail) — expose daemon-side layers for external agents. Spec at `docs/superpowers/specs/2026-04-20-memory-reporting-design.md`, plan at `docs/superpowers/plans/2026-04-20-memory-reporting.md`.
- **claude-code: per-pane resume** — multi-pane Claude sessions sharing a working directory now reattach to their own session on restore, instead of all converging on claude's "most recent in cwd" lookup. On restart, the daemon checks `~/.claude/projects/<escaped-cwd>/<session_id>.jsonl`; if present, it promotes the pane's resume args to `--resume <uuid>`. Otherwise (pane closed during claude's startup screens before any exchange persisted a session file), it falls back to `--continue`. Plugin schema bumped to v4 — users with edited `~/.quil/plugins/claude-code.toml` get the standard side-by-side migration dialog on next launch.

## [1.8.0] - 2026-04-18

### Added

- **Client/daemon version negotiation** — the TUI now performs a version handshake with the running daemon before attaching. If the daemon is older (or pre-dates version negotiation), the TUI prompts before gracefully stopping it and auto-spawning the matching daemon from alongside the TUI binary. If the daemon is newer (i.e., the TUI is stale), the TUI refuses to attach and points the user at the releases page. Eliminates the manual "stop daemon → replace both binaries → restart" dance on every upgrade. Dev/debug builds and unstamped local builds skip the check. New IPC pair `MsgVersionReq`/`MsgVersionResp` added to the protocol; new shared `internal/version/` package provides proper semver comparison (no more lexical-ordering traps with `1.10.0` vs `1.9.0`).

## [1.7.0] - 2026-04-18

### Added

- **claude-code: `--enable-auto-mode` toggle** — the pane setup dialog (Ctrl+N → AI Tools → Claude Code) now offers Claude Code's safer auto-mode alongside the existing `--dangerously-skip-permissions` option. Both toggles share a new `permission_mode` mutual-exclusion group: enabling one automatically disables the other, and "neither" remains valid (Claude's default interactive confirmations). claude-code's plugin schema is bumped to v3 — users with edited `~/.quil/plugins/claude-code.toml` get the standard side-by-side migration dialog on next launch.
- **Plugin toggles: mutually-exclusive groups** — `[[command.toggles]]` entries now accept an optional `group = "name"` field. Toggles that share a non-empty group value render as radio buttons (`( ) / (•)`) instead of checkboxes (`[ ] / [x]`); enabling one disables the others in the group. Empty `group` keeps the existing independent-checkbox behaviour. Documented in `docs/plugin-reference.md`.
- **Event-loop perf instrumentation** — new `internal/tui/perf.go` measures per-Update-message cost, View duration, pane-output throughput, and key-backlog depth on the Bubble Tea program goroutine. Emits one aggregate Info line every 5 s and per-event Debug lines above tunable thresholds (50 ms Update, 30 ms View, 10 ms pane-output, 20 msgs key backlog). Zero overhead when stats are disabled (nil-receiver guard on every method).

### Fixed

- **Pane rendering corruption after focus toggle** — toggling focus mode (Ctrl+E) on a wide screen left narrow-column ghost rows from the pre-focus layout in TUI panes (most visible in claude-code's tool-output tree). Root cause: `PaneModel.ResizeVT` was rebuilding the VT emulator from scratch on every resize and replaying the entire raw-PTY ring buffer — including cursor-positioning sequences laid out for the previous width. The replay now uses the `x/vt` library's in-place `Resize`, which preserves the current cell grid; the PTY child redraws via SIGWINCH (already wired through `MsgResizePane`) into the resized emulator. Same fix benefits any TUI pane that resizes (vim, htop, fzf, less).
- **Shift+Tab silently swallowed in claude-code panes** — pressing Shift+Tab to cycle Claude Code modes (auto-accept / plan / etc.) had no effect since selection support landed. The pane-input router was matching every `shift+*` key with `strings.HasPrefix` and routing it into the scrollback selection handler, whose `default:` branch silently dropped any non-arrow shift combo. The guard is now a precise allow-list (`shift+arrow`, `ctrl+shift+arrow`, `ctrl+alt+shift+arrow`); everything else falls through to plugin raw-key handling and PTY forwarding. Locked in via `TestIsSelectionExtendKey`.
- **Release workflow silently skipped when squash subject came from branch name** — `release.yml` parsed conventional commits via `git log --oneline`, which strips bodies. When GitHub's "Squash and merge" defaulted the subject to the branch name (e.g. `Feat/claude-code-permission-modes`), the strict `feat(:|()` regex didn't match `Feat/`, the parser fell into the no-bump branch, and the release was silently skipped despite the body containing proper `feat(scope):` lines. The parser now scans both subject and body (`--format='%s%n%b'`), matches case-insensitively (`-i`), and accepts the `feat/branch-name` shape via `\bfeat[(:/]`.

## [1.6.0] - 2026-04-15

### Added

- **CWD memory in pane creation dialog** — the directory browser (Ctrl+N → setup) now remembers the last selected working directory within the TUI session. On the next pane creation, the browser starts from the previous selection instead of always defaulting to the Quil launch directory. Priority order: last selected CWD → active pane's OSC 7 CWD → user home. Stale directories (deleted between creations) are detected, cleared from memory, and the next candidate is tried automatically.

## [1.5.0] - 2026-04-15

### Added

- **Windows executable icon** — `quil.exe` and `quild.exe` now embed the Quil brand mark (ember Q) as a Windows resource icon, visible in Explorer, taskbar, and Alt+Tab. Build assets live in `winres/` (icon PNGs + `winres.json` manifest). `go-winres` v0.3.3 generates `.syso` files at build time — both `build`, `cross`, and GoReleaser invoke it automatically. `RT_VERSION` metadata (ProductName, FileDescription, version) surfaces in Explorer's file properties dialog.

### Fixed

- **Pane CWD ignored on creation** — selecting a working directory in the pane setup dialog (Ctrl+N → CWD browser) had no effect; the spawned process always started in the daemon's own working directory. `spawnPane()` now calls `ptySession.SetCWD(pane.CWD)` before `Start()`. The redundant `SetCWD` calls in `respawnPanes()` were removed — `spawnPane` is now the single source of truth for CWD application.

## [1.4.2] - 2026-04-14
## [1.4.1] - 2026-04-14

## [1.4.0] - 2026-04-14

### Added

- **Three-variant build system** — `./scripts/dev.sh build` now produces 6 binaries: `quil.exe`/`quild.exe` (prod, stripped), `quil-dev.exe`/`quild-dev.exe` (auto dev mode + debug logging), `quil-debug.exe`/`quild-debug.exe` (debug logging, production data dir). Compile-time ldflags (`buildDevMode`, `buildLogLevel`, `daemonBinary`) bake in behavior — dev variant needs no `--dev` flag. Each variant auto-starts its matching daemon (e.g., `quil-dev.exe` starts `quild-dev.exe`).
- **Plugin schema migration dialog** — when a plugin's on-disk `schema_version` is lower than the embedded default, a full-screen side-by-side merge dialog blocks startup. Left pane shows the user's config (editable), right pane shows the new default (read-only). Diff highlighting: red tint for lines only in the user config, green tint for new lines in the default. Ctrl+C copies, Ctrl+V pastes, Ctrl+S saves and advances, F5 accepts the full default. Esc is blocked — migration must be resolved before using Quil.
- **Plugin schema versioning** — `schema_version` field in `[plugin]` section of embedded default TOMLs. `EnsureDefaultPlugins` returns `[]StalePlugin` for stale files instead of silently overwriting. `ParseSchemaVersion` exported for TUI validation.
- **Windows drive navigation** — the CWD directory browser (Ctrl+N → plugin setup) can now switch between Windows drive letters. Pressing backspace at a drive root (e.g., `C:\`) shows all available drives (`A:\` through `Z:\`). Selecting a drive navigates into it.
- **TextEditor: Ctrl+C copy** — copies the current selection to the system clipboard without deleting it. Previously only Enter (copy) and Ctrl+X (cut) were available.
- **TextEditor: Ctrl+Y delete line** — deletes the current line. On a single-line document, clears the line content.

### Fixed

- **Ghost buffer replay freeze** — large ghost buffers (80KB+) sent as single IPC messages starved Bubble Tea's unbuffered input channel on Windows, freezing the TUI on startup. Ghost buffers are now sent in 8 KB chunks with 2 ms yield between each, matching the live-output coalescing interval. The `sendGhostChunked` function supports early abort via the daemon's shutdown channel.
- **Stale plugin configs on upgrade** — existing users who installed Quil before v1.3.0 never received `prompts_cwd`, `[[command.toggles]]`, or the updated `resume_args = ["--continue"]` in their `claude-code.toml` because `EnsureDefaultPlugins` was create-only. Now detected and surfaced via the migration dialog.
- **Resize artifacts in full-screen dialogs** — the migration and disclaimer dialogs now skip the 150 ms resize debounce, applying window size changes immediately. Previously, maximizing the window caused rendering artifacts during the debounce window.

### Changed

- **`quil-dev.ps1` / `quil-dev.sh`** — now launch the self-contained `quil-dev.exe` / `quil-dev` binary directly instead of `quil.exe --dev`. No flags or env vars needed.
- **`scripts/dev.sh` PROJECT_DIR** — derived dynamically via `pwd -W` instead of hardcoded absolute path.
- **`quild` background mode** — stdout/stderr prints gated on `!background` instead of redirecting to `/dev/null` (eliminates a file descriptor leak).

## [1.3.1] - 2026-04-09

## [1.3.0] - 2026-04-08

### Added

- **Pane setup dialog — working directory prompt** — when creating a `claude-code` pane (Ctrl+N → AI Tools → Claude Code), the TUI now asks for the working directory with a smart default (the active pane's CWD, tracked via OSC 7). This preserves project-specific `.claude/` context that Claude Code ties to the directory. The empty input falls back to the daemon's `os.Getwd()`, matching the old behaviour.
- **Pane setup dialog — runtime toggles (checkboxes)** — the same setup dialog renders one checkbox per plugin-declared `[[command.toggles]]` entry. claude-code ships with a single toggle, `Dangerously skip permissions`, which appends `--dangerously-skip-permissions` to the claude command line when checked. Off by default, per-pane, persists across daemon restarts.
- **Plugin TOML opt-ins** — new `prompts_cwd = true` flag under `[command]` triggers the CWD prompt for a plugin. New `[[command.toggles]]` array-of-tables declares runtime boolean switches (`name`, `label`, `args_when_on`, `default`). New `raw_keys = [...]` list forwards specific keys directly to the PTY bypassing Quil's global shortcut layer. All three are opt-in; default plugins don't set them (terminal / ssh / stripe untouched).
- **Spatial pane navigation (`Alt+Arrow`)** — `Alt+Left`/`Alt+Right`/`Alt+Up`/`Alt+Down` focus the pane in that direction. Navigation is directional, not linear: it picks the closest neighbor in the target direction based on screen coordinates, matching `tmux`'s `select-pane -L/R/U/D`. New `pane_left`/`pane_right`/`pane_up`/`pane_down` fields in `[keybindings]` — vim users can rebind to `alt+h/l/k/j` (but they'd want to move `split_horizontal` off `alt+h` first).
- **Image paste from clipboard** — pressing the paste key now reads the system clipboard for image data when no text is present. Quil decodes the DIB (or DIBV5 for alpha), encodes it as PNG, saves it under `~/.quil/paste/quil-paste-<timestamp>.png`, and types the absolute path into the active pane. AI tools like Claude Code can then read the file via their normal file tools. This sidesteps the upstream Claude Code Windows clipboard bug ([anthropics/claude-code#32791](https://github.com/anthropics/claude-code/issues/32791)).
- **Paste key aliases for Windows Terminal** — `Ctrl+Alt+V` and `F8` are now hardcoded as alternate paste triggers. Windows Terminal captures the default `Ctrl+V` for its own paste action and never delivers the key event to the running TUI; the aliases bypass that interception. `F8` is the recommended choice on Windows because it has no AltGr ambiguity on European keyboard layouts. Linux/macOS native ttys continue to receive `Ctrl+V` and don't need the aliases.
- **`internal/clipboard.ReadImage()` API** — new platform dispatch (`internal/clipboard/clipboard.go`). Win32 implementation in `image_windows.go` reads `CF_DIBV5`/`CF_DIB`, copies the DIB out of the GlobalLock, and hands off to the platform-independent DIB parser in `dib.go`. Unix/macOS get a stub returning `ErrNoImage` for now.
- **`config.PasteDir()`** — returns `~/.quil/paste/` (or `./.quil/paste/` in dev mode). The directory is created lazily by `tryPasteClipboardImage`.
- **Leveled logger** — new `internal/logger` package wraps Go's stdlib `slog`, exposes `Debug/Info/Warn/Error` helpers, and **bridges the existing 152 stdlib `log.Printf` call sites** through the same handler at info level so both old and new code respect a single filter. The level is read from `[logging] level` in `config.toml` (`"debug" | "info" | "warn" | "error"`, case-insensitive) by both `cmd/quild/main.go` and `cmd/quil/main.go` at startup. Useful for diagnosing missing-key bugs and clipboard-paste issues — flip `level = "debug"` to see the per-key handler trace, the paste pipeline, and the Win32 clipboard image read step-by-step. Default is `"info"`.
- **F1 → log viewers** — three new menu items in the F1 About dialog: `View client log` (`~/.quil/quil.log`), `View daemon log` (`~/.quil/quild.log`), and `View MCP logs` (aggregates per-pane files in `~/.quil/mcp-logs/`, most recently modified first, with file-name headers). Reuses the existing `TextEditor` in **read-only** mode (new `TextEditor.ReadOnly` field gates every mutation path: typing, paste, cut, save, enter/backspace/delete, tab, multi-line insert from clipboard). Tail-reads the last 256 KB of each file at line boundaries with a `[... older lines truncated ...]` marker. Cursor starts at the bottom so the most recent lines are in view. The viewer also rejects symlinks via `os.Lstat` so a swapped link inside `~/.quil/` cannot redirect the read to an arbitrary file.
- **Alt+Up / Alt+Down page navigation in the log viewer** — jumps the cursor by `[ui] log_viewer_page_lines` (default `40`). Configurable via `config.toml`. New `TextEditor.PageSize` field; works in both read-only and editable modes; clamps to first/last line at the edges.
- **`.claude/rules/dev-environment.md`** — project-level rule documenting the production/dev isolation constraint. Developers of Quil who run Quil in production must use dev mode (`./quil --dev`, data in project-root `.quil/`) for all testing, and never touch the production daemon or `~/.quil/` metadata.

### Changed

- **Tab and Shift+Tab are no longer intercepted globally** — previously bound to `next_pane` / `prev_pane`, which ate the keys before they could reach shell tab-completion or Claude Code's mode-cycling. Both keys now fall through to the PTY. Pane navigation moved to `Alt+Arrow` (see Added). `next_pane` / `prev_pane` config fields remain for backward compat but default to empty (unbound); users who had customized configs keep their old bindings until they edit.
- **Split shortcuts moved to `Alt+Shift+H` / `Alt+Shift+V`** (were `Alt+H` / `Alt+V`). Claude Code uses `Alt+V` to paste an image, and leaving the plain `Alt+letter` keys free for the PTY is consistent with the Tab/Shift+Tab policy. The `H for horizontal, V for vertical` mnemonic is preserved via the extra Shift.
- **Notes-mode focus toggle** (editor ↔ bound pane) is now hard-coded to Tab / Shift+Tab instead of reading `kb.NextPane`, which is now empty by default. Behavior unchanged for the end user.
- **Settings dialog (F1 → Settings) now persists every field**, not just `Show disclaimer`. Snapshot interval, ghost dimmed, ghost buffer lines, mouse scroll lines, page scroll lines, and log level all flag the config as dirty so the change is written to `~/.quil/config.toml` on TUI exit. Log-level changes apply on the next launch (no live re-init).
- **Spatial pane navigation now uses center-distance as a third tie-breaker** (after gap and overlap), matching tmux/vim/iTerm muscle memory. Previously, ties resolved by layout-tree order — now the pane whose perpendicular center is closer to the active pane's center wins.
- **`internal/plugin/registry.LoadFromDir` prunes stale plugins** — deleting a plugin's TOML file and reloading the registry now removes the in-memory entry. The Go built-in `terminal` plugin is always preserved.

### Fixed

- **`preassign_id` resume strategy preserves `InstanceArgs` across daemon restarts** — `spawnPane`'s restore branch previously replaced `args` with `ExpandResumeArgs(...)`, which dropped any runtime args (notably `--dangerously-skip-permissions` from the new setup toggle). Now the resume args are appended to the existing args slice, so both InstanceArgs and `--resume <uuid>` reach the child process on restart.

### Security

- **Paste PNG files are now owner-only.** `~/.quil/paste/` is created with mode `0o700`, individual `quil-paste-*.png` files with `0o600`, and the filename gains an 8-byte `crypto/rand` suffix so a co-tenant on a Unix machine can no longer enumerate or guess recently-pasted screenshots.
- **DIB parser hardened against degenerate dimensions.** A new per-axis cap (`maxDIBDimension = 16384`) plus `uint64` stride math defends against crafted clipboard payloads that slip under the 64 MB byte cap but would otherwise allocate gigabytes during decode. Inert on 64-bit builds today; defends future 32-bit builds.
- **Daemon CWD validation now re-resolves symlinks** in both `handleCreatePane` and `handleCreatePaneReq`. Combined with the existing TUI-side `EvalSymlinks`, this closes the small TOCTOU window where a symlink swap between Stat and exec could redirect a child process to a different directory. Applies to all IPC clients (TUI, MCP, future tooling).
- **Log viewer rejects non-regular files.** `readLogTail` runs `os.Lstat` before opening, refusing symlinks, devices, and named pipes. A re-stat through the open handle defeats a TOCTOU swap between Lstat and Open.

## [1.2.1] - 2026-04-07

## [1.2.0] - 2026-04-07

## [1.1.0] - 2026-04-07

## [1.0.0] - 2026-04-07

## [0.13.0] - 2026-04-07

### Added

- **Pane Notes (M7)** — `Alt+E` opens a plain-text notes editor alongside the bound pane. The bound pane auto-expands to fill the available area on the left (other panes hidden, like `Ctrl+E` focus mode) and the editor takes ~40% on the right. `Alt+E` again or `Esc` exits, reverting the original layout
- **Tab/Shift+Tab focus cycle** — while notes mode is active, `Tab` and `Shift+Tab` cycle keyboard focus between the editor (default) and the bound pane. Editor-focused: text input goes to notes, border bright blue, status bar `[notes]`/`[notes*]`. Pane-focused: keys reach the PTY normally, border dim grey, status bar `[notes pane]`
- **Mouse selection in the notes editor** — click positions the cursor; click+drag creates a selection (highlighted in reverse video). Works with the existing `editorExtractText` so `Enter` and right-click both copy. Click in the pane area while notes mode is on hands keyboard focus to the pane (no Tab needed)
- **Right-click copy** — right-click in the notes editor copies the active selection to the clipboard and clears the highlight, mirroring the existing pane right-click behaviour. The notes selection takes priority over a pane selection while notes mode is active
- **Per-pane notes storage** — one markdown file per pane at `~/.quil/notes/<pane-id>.md`. Atomic temp+rename writes via `internal/persist/notes.go` (`os.CreateTemp` for race-free temp filenames, `Lstat` symlink rejection, Windows reserved-name validation). Notes survive pane destruction — orphan notes remain on disk for a future browser
- **Three save safety nets** — 30-second debounce auto-save (reset on every edit), explicit `Ctrl+S` shortcut, and an unconditional flush on exit (toggling off, structural actions, tab switch, TUI quit). Saved files always end with a trailing newline
- **`TextEditor.Highlight` field** — new typed `HighlightMode` (`HighlightTOML` default, `HighlightPlain` for notes) so the existing rune-aware editor can render plain text without TOML syntax colouring
- **`TextEditor.GutterWidth`** — dynamic line-number gutter width derived from `len(Lines)` so files with 1000+ lines render correctly and mouse-to-document coordinate mapping stays accurate
- **`NotesEditor` wrapper** — `internal/tui/notes.go` intercepts `Ctrl+S` and `Esc` before delegating to `TextEditor`, so notes bypass the TOML-specific validation path and `Esc` only exits on a second press (first press clears selection). Public API: `SetCursor`, `BeginSelection`, `ExtendSelection`, `HasSelection`, `ExtractSelection`, `ClearSelection`, `Save`, `Close`

### Changed

- **`Model.handleKey` notes routing** — restructured around `notesKeyExempt` (allow-list of global shortcuts that bypass the editor) and `exitNotesModeInPlace` (canonical teardown delegated to by `exitNotesMode`, `applyWorkspaceState`, `switchTab`)
- **`Model.notesPanelWidth`** — single source of truth for the notes layout math. Both `View()` and `notesEditorBox()` (used by mouse handlers) call it so they cannot drift apart
- **`applyWorkspaceState` notes reconciliation** — detects when the bound pane is pruned (exits notes) AND when the daemon promotes a different pane to active in the bound tab (re-syncs `ActivePane` back to the bound pane so the editor stays next to its target)
- **`Model.exitNotesMode` is pointer-receiver** — discarded calls (`m.exitNotesMode()` as a statement) still mutate the model, eliminating the silent-reinstate footgun the previous review flagged
- **Clipboard write errors logged consistently** — `model.go:294`, `:312`, and `:1086` all wrap `clipboard.Write` in an error-check + `log.Printf`
- `TextEditor` struct gained a `Highlight` field; existing call sites default to TOML highlighting for backward compatibility
- `cmd/quil/main.go` calls `Model.FlushNotes()` on TUI exit as a safety net for unsaved notes

## [0.12.1] - 2026-04-05

## [0.12.0] - 2026-04-05

### Added

- **Notification Center (M12)** — daemon event queue with process exit detection, output pattern matching via `[[idle_handlers]]` TOML, and bell character detection with 30s cooldown. TUI sidebar toggled via Alt+N (visibility) / F3 (focus+navigate). Pane history stack with Alt+Backspace navigation. Status bar `[N events]` badge
- **Smart idle analysis** — when a pane goes idle (5s no output), last lines are analyzed against plugin `[[idle_handlers]]` patterns. SSH `[Y/n]` → "Waiting for confirmation", Claude Code prompt → "Waiting for input", password prompts detected. AI panes default to "warning" severity
- **OSC 133 command markers** — shell integration hooks extended for bash, zsh, PowerShell to emit command start/end sequences. Daemon parses `OSC 133;D` for precise command completion with exit code
- **MCP notification tools** — `get_notifications` (non-blocking) and `watch_notifications` (blocking up to 5 min, replaces polling). `requestWithTimeout` for long MCP waits
- **Plugin `path` field** — optional `path = "/full/path/to/binary"` in plugin TOML overrides PATH lookup. Fallback search in `~/.local/bin/` for Explorer-launched apps on Windows
- **Plugin `[[idle_handlers]]`** — new TOML section for context-aware idle notifications, parallel to existing `[[error_handlers]]`. Default patterns for terminal, claude-code, and ssh plugins

### Fixed

- **Focus mode mouse selection** — bypasses layout tree traversal when Ctrl+E focus mode is active, uses active pane directly
- **SSH cursor visibility** — added `"ssh"` to terminal-type check so cursor renders in SSH panes
- **Paste cursor position** — delayed re-render (100ms) after paste so cursor updates to end of pasted text
- **DecodePayload error checking** — all 11 pre-existing IPC handlers now check decode errors (was silently ignored)
- **Shutdown double-close panic** — `sync.Once` guards `close(d.shutdown)` against multiple shutdown messages
- **Watcher timer leak** — `time.NewTimer` + `defer timer.Stop()` replaces `time.After` in watch goroutine and MCP bridge
- **Idle detection race** — single `PluginMu` lock span for read+write in `checkIdlePanes` prevents race with `flushPaneOutput`
- **PowerShell 5.1 compat** — shell init uses `[char]0x1b` for escape instead of `` `e `` which only works in PowerShell 7+
- **Zsh exit code capture** — `precmd` saves `$?` to local immediately, inserted first in `precmd_functions` before OSC 7

### Changed

- **IPC server** — `onDisconnect` callback now receives `*Conn` for watcher cleanup on disconnect
- **`flushPaneOutput` refactored** — extracted `detectBellEvent`, `detectOSC133Exit`, `applyPluginHandlers` helpers
- **Notification matching moved to idle time** — patterns run against last 5 lines at idle, not on every output chunk (eliminates false positives from arrow keys, command history)

## [0.11.0] - 2026-03-25

### Added

- **MCP Server (M10)** — `quil mcp` subcommand exposes Quil to AI assistants via Model Context Protocol. 13 tools: `list_panes`, `read_pane_output`, `send_to_pane`, `get_pane_status`, `create_pane`, `send_keys`, `restart_pane`, `screenshot_pane`, `switch_tab`, `list_tabs`, `destroy_pane`, `set_active_pane`, `close_tui`
- **Official MCP SDK** — uses `modelcontextprotocol/go-sdk` v1.4+ with typed tool handlers and struct-based input schemas
- **Request-response IPC** — backward-compatible `Message.ID` field for correlating MCP requests; daemon responds to specific connection when ID is set, broadcasts when empty
- **Process exit tracking** — `WaitExit()` on PTY `Session` interface with `sync.Once` for safe concurrent access; `Pane.ExitCode` and `Pane.ExitedAt` fields
- **VT-emulated screenshots** — `screenshot_pane` tool feeds ring buffer through `charmbracelet/x/vt` terminal emulator to capture actual screen state; essential for interactive TUI apps
- **Named key sequences** — `send_keys` tool with 50+ key mappings (arrows, function keys, ctrl+a-z); escape sequences sent individually with 50ms pacing for TUI compatibility
- **Orange MCP highlight** — pane border flashes orange (color 208) when AI interacts via MCP; configurable duration via `[mcp] highlight_duration` (default 10s)
- **Per-pane MCP logging** — interaction metadata logged to `~/.quil/mcp-logs/{pane-id}.log`; two-layer redaction: AI markers (`<<REDACT>>...<</REDACT>>`) + regex fallback for common secret patterns
- **MCP server instructions** — tool usage guidelines and sensitive data handling protocol sent to AI clients during initialize handshake
- **TUI cooperation tools** — `set_active_pane` broadcasts to TUI for pane focus; `close_tui` exits TUI while daemon persists
- **Notification center PRD update** — added MCP integration section: `watch_notifications` blocking tool, event hub architecture, AI as event consumer

## [0.10.2] - 2026-03-24

## [0.10.1] - 2026-03-24

### Fixed

- **GoReleaser workflow not triggering** — tags pushed with `GITHUB_TOKEN` don't trigger other workflows; merged goreleaser into `release.yml` as a second job with `needs: release`
- **Dry run executing goreleaser** — boolean vs string comparison bug in job `if:` condition; `DRY_RUN` now forwarded through job outputs as string
- **Actions pinned to commit SHAs** — `actions/checkout`, `actions/setup-go`, `goreleaser/goreleaser-action` pinned to immutable SHAs for supply-chain security
- **Per-job permissions** — `contents: write` moved from workflow-level to per-job blocks for least-privilege

## [0.10.0] - 2026-03-24

### Added

- **Roadmap PRDs** — 11 detailed Product Requirements Documents in `docs/roadmap/`: workspace files, MCP server, command palette, notification center, pre-built binaries, demo GIF, community plugins, process health, tmux migration, cross-pane events, session sharing
- **Restructured ROADMAP.md** — organized into Core/Growth/Advanced categories with priority matrix, strategic pain-layer analysis, and feature synergy notes
- **Notification center concept (M12)** — centralized event sidebar with pane navigation and history stack; PRD covers process exit detection, plugin notification handlers, and incremental integration path
- **Pre-built binaries & release infrastructure** — GoReleaser config for 5 platforms (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64); `release.yml` handles version bump + tag + GoReleaser build, publishes GitHub Release with `.tar.gz`/`.zip` archives and SHA256 checksums
- **One-line install script** — `scripts/install.sh` detects OS/arch, fetches latest release from GitHub API, verifies SHA256 checksum, installs to `~/.local/bin/`; supports `QUIL_VERSION` for pinned installs and `GITHUB_TOKEN` for API auth
- **Daemon version reporting** — `quild version` subcommand, version logged at startup; consistent `-ldflags` injection across all build paths (GoReleaser, dev.sh, dev.ps1, rebuild.ps1, Makefile)

### Fixed

- CI Go version mismatch — updated from 1.24 to 1.25 in `ci.yml` and `release.yml` to match `go.mod`

## [0.9.0] - 2026-03-23

### Added

- **Pane focus mode (M6)** — Ctrl+E toggles active pane to full-screen; other panes keep running in background; `* FOCUS *` border label; `[focus]` status bar indicator; splits/close auto-exit focus
- `focus_pane` keybinding config field (default `ctrl+e`)

## [0.8.0] - 2026-03-22

### Added

- **Editor text selection** — Shift+Arrow (character), Ctrl+Shift+Arrow (word), Ctrl+Alt+Shift+Arrow (3 words), Shift+Home/End (line) in TOML editor
- **Editor clipboard** — Enter copies selection, Ctrl+X cuts, Ctrl+V pastes (async via `editorPasteMsg`), Ctrl+A selects all
- **Editor selection rendering** — reverse video highlight with cursor-within-selection underline
- **Editor selection-aware editing** — typing with selection replaces selected text; backspace/delete removes selection
- **Editor multi-line paste** — Ctrl+V and bracketed paste handle newlines, splitting into editor lines
- **Editor shortcuts in help** — F1 → Shortcuts shows editor selection and clipboard shortcuts
- **Editor paragraph navigation** — Ctrl+Up/Down jumps to next/previous empty line; Ctrl+Shift+Up/Down selects to paragraph boundary
- **Editor word navigation** — Ctrl+Arrow (1-word) and Ctrl+Alt+Arrow (3-word) jump in editor
- **Beta disclaimer dialog** — shown on first launch with random tips/shortcuts; "Don't show again" persists to `config.toml`
- **Config save** — `config.Save()` function for atomic config persistence (used by disclaimer opt-out)
- `ui.show_disclaimer` config field (default `true`)

## [0.7.0] - 2026-03-22

### Added

- **Bubble Tea v2 + Lipgloss v2 migration** — declarative `View()` returning `tea.View`, typed mouse events (`MouseClickMsg`, `MouseWheelMsg`, `MouseMotionMsg`, `MouseReleaseMsg`), `KeyPressMsg` replaces `KeyMsg`, Go 1.25
- **Text selection** — keyboard selection via Shift+Arrow (character), Ctrl+Shift+Arrow (word), Ctrl+Alt+Shift+Arrow (3 words), and mouse click+drag; Enter copies selection to clipboard; Esc clears; right-click copies
- **Platform-native clipboard** — `internal/clipboard/` with Read/Write: Win32 `GetClipboardData`/`SetClipboardData` on Windows, `pbpaste`/`pbcopy` on macOS, `xclip`/`xsel` on Linux; bounded reads (10MB max); cached tool detection on Unix
- **Bracketed paste** — Ctrl+V wraps clipboard content in `ESC[200~...ESC[201~` sequences for safe multi-line paste
- **Paste in dialogs** — Ctrl+V works in dialog input fields (SSH connection form, Settings); control characters sanitized before insertion
- **Ctrl+Arrow word jump** — sends `ESC[1;5C`/`ESC[1;5D` to PTY for shell word navigation
- **Ctrl+Alt+Arrow 3-word jump** — sends triple word-jump escape sequences
- **Stripe dialog wider** — `dialog_width = 75` for long forward URLs
- **SSH dialog wider** — `dialog_width = 100` for long connection details
- **Selection shortcuts in help** — F1 → Shortcuts shows Shift+Arrows, Ctrl+Shift+Arrows, Enter, Right-click, Esc
- `FindPaneRectAt` layout method for mouse-to-pane coordinate mapping
- `scripts/rebuild.ps1` — kill daemon, reset state, rebuild executables

### Changed

- Scripts moved from project root to `scripts/` directory
- `dialogBorder.Width()` uses Lipgloss v2 border-inclusive semantics (`Width(width)` on border, `Width(innerW+2).Height(innerH+1)` on pane body)
- Plugin `dialog_width` override now scoped to instance-specific screens only (instance list and form), not all create-pane dialog steps
- `tea.ClearScreen` fired on dialog open and width-changing transitions to prevent BT v2 diff renderer artifacts
- Ghost buffer VT reset now only for `claude-code` pane type (SSH and other terminal-like panes preserve history)
- Docker images updated from `golang:1.24-alpine` to `golang:1.25-alpine`
- Cursor hidden via `\x1b[?25l` — custom cursor rendered via `insertCursor()`

### Fixed

- Pane border/size wrong after Lipgloss v2 migration — Width/Height now compensate for border-inclusive semantics
- Dialog border broken on first render — `tea.ClearScreen` on pane-to-dialog transitions
- Dialog border broken on width change — `tea.ClearScreen` on plugin selection with custom `dialog_width`
- Edit cursor glyph not rendering on Windows — replaced `▎` (U+258E) with `│` (U+2502)
- Paste broken everywhere after v2 migration — restored platform-native `clipboard.Read()` (OSC 52 read not supported by most terminals)
- SSH ghost buffer not restored after daemon restart — VT reset condition changed from "all non-terminal" to "only claude-code"
- Selection extending into empty terminal lines — bounded by `lastContentLine()`
- Soft-wrap detection in text extraction — detects both VT character wraps and near-edge content

### Removed

- Custom `utf16PtrToString` — replaced with `windows.UTF16PtrToString` from `golang.org/x/sys/windows`

## [0.6.0] - 2026-03-18

### Added

- **Plugin configuration reference** — comprehensive documentation for creating custom plugins covering every TOML section, field, strategy, and behavior with annotated examples (`docs/plugin-reference.md`)
- **Default TOML plugins** — claude-code, ssh, stripe shipped as editable embedded TOML files via `//go:embed`; written to `~/.quil/plugins/` on first run, user edits preserved across upgrades
- **Plugin instance management** — `InstanceStore` persists saved SSH connections, Stripe webhooks, etc. to `~/.quil/instances.json`; form fields + arg templates defined per-plugin
- **Plugin management UI** — F1 → Plugins dialog with view, reload, restore defaults, and in-app TOML editor
- **In-app TOML editor** — full-screen multi-line editor with rune-aware cursor, TOML syntax highlighting (comments grey, sections orange, keys blue, strings green), validation on save
- **Pane creation instance step** — Ctrl+N dialog extended: category → plugin → instance selection → split direction (4 steps)
- **Centralized snapshot queue** — event-driven `snapshotCh` channel with 500ms debounce replaces scattered `snapshotDebounced()` calls; triggers on create/destroy tab/pane, switch tab, update layout, client disconnect
- **Per-plugin ghost buffer toggle** — `GhostBuffer` bool on `PersistenceConfig` controls whether PTY output is saved to disk per plugin type
- **GhostSnap restore** — pure disk-loaded ghost data stored separately from live ring buffer, preventing respawned shell init output (e.g., ConPTY clear screen) from contaminating history replay
- **Diagnostic logging** — comprehensive trace logging across daemon (IPC dispatch, attach, snapshot metrics, ghost replay, spawn, tab/pane lifecycle) and TUI (ghost transitions, workspace state, layout restore); IPC server logs client connect/disconnect
- `MsgReloadPlugins` IPC message for hot-reloading plugin configuration
- `onDisconnect` callback on IPC server — triggers snapshot on client disconnect
- Socket permissions restricted to `0600` after creation
- `InstancesPath()` config path helper

### Changed

- 3 of 4 built-in plugins moved from Go code to embedded TOML defaults — only terminal remains in Go (needs runtime shell detection)
- `NewServer()` accepts `onDisconnect` callback as third parameter
- Ghost buffer replay in `handleAttach` prefers `GhostSnap` (clean disk data) over `OutputBuf` (may contain post-restore shell output)
- `handleUpdateLayout` now triggers snapshot request (was missing — caused layout loss on daemon kill)

### Fixed

- Terminal history not restored after daemon restart — `ResetVT()` no longer called for terminal panes on ghost→live transition; GhostSnap prevents shell init contamination of ghost replay
- Fresh pane on first run incorrectly showing "resuming..." spinner — only set `resuming=true` when tab has saved layout
- Confirm dialog extended for instance deletion (`confirmKind = "instance"`)

## [0.5.0] - 2026-03-16

### Added

- **Plugin system** — typed panes with 4 built-in plugins: Terminal, Claude Code (AI), Stripe (webhook), SSH (remote)
- `internal/plugin/` package — plugin structs, registry, TOML loading, regex scraper, error handler matching
- TOML plugin format — user-created plugins in `~/.quil/plugins/*.toml` with command, persistence, error handlers, and instances
- Plugin registry with `DetectAvailability()` — checks PATH for plugin binaries at startup
- Pane creation dialog (`Ctrl+N`) — three-step flow: category → plugin → split direction (horizontal, vertical, replace)
- Atomic pane replacement via `ReplacePane()` — swap pane type in-place without layout disruption
- **Session resume for Claude Code** — pre-assigned UUID via `--session-id`, resumed with `--resume` after daemon restart
- `preassign_id` persistence strategy — generate UUID at pane creation, store in `PluginState`, resume on restore
- `session_scrape` persistence strategy — regex scraper extracts tokens from PTY output for resume
- `rerun` persistence strategy — re-execute same command + args on restore (SSH, Stripe)
- Error handler system — match PTY output against regex patterns, show help dialogs (e.g., SSH auth failure, missing API key)
- `MsgPluginError` IPC message — daemon-to-TUI error notification with modal dialog display
- Resuming/preparing spinner — animated braille indicator (`⠹ resuming...` / `⠹ preparing...`) on pane border during startup
- Window size persistence — save/restore terminal dimensions via `~/.quil/window.json`
- Platform-specific window restore — Win32 `MoveWindow`/`ShowWindow` on Windows, xterm sequence on Unix
- Maximized window state detection and restoration via `IsZoomed`/`SW_MAXIMIZE`
- `PluginsDir()` and `WindowStatePath()` config path helpers
- Plugin state fields on `Pane` struct — `Type`, `PluginState`, `InstanceName`, `InstanceArgs`
- Workspace JSON backward compatibility — missing `type` defaults to `"terminal"`

### Changed

- `spawnShell()` replaced with generalized `spawnPane()` — dispatches by plugin type and resume strategy
- `respawnShells()` replaced with `respawnPanes()` — fallback to terminal shell on plugin spawn failure
- Ghost buffer replay skipped for TUI app panes (`preassign_id`, `session_scrape`) — prevents cursor state pollution
- Quil cursor overlay disabled for non-terminal panes — TUI apps render their own cursor
- `CreatePanePayload` extended with `Type`, `InstanceName`, `InstanceArgs`, `ReplacePaneID`
- `NewModel()` accepts plugin registry parameter
- Status bar updated with `^N pane` hint

### Fixed

- Regex compilation uses `regexp.Compile` (not `MustCompile`) — invalid TOML patterns log errors instead of crashing daemon
- Nil guard in `ScrapeOutput`/`MatchError` for uncompiled patterns
- Data race on `Pane.PluginState` — protected with `PluginMu` mutex
- `hitTestTab` missing tab index prefix — click targets now match rendered tab widths
- Scraped values truncated in log output — prevents leaking tokens/secrets
- Error handler patterns anchored — `Permission denied (publickey` and `error.*API key` avoid false matches
- `loadPluginTOML` validates strategy, cmd, and error handler action fields
- `loadPluginTOML` defaults `DisplayName` to `Name` and `Category` to `"tools"` when empty
- Layout `resizeNode` nil guard for placeholder nodes during pane replacement
- `ExpandResumeArgs` returns nil when placeholders are unresolved — prevents passing literal `{session_id}` to tools
- `window_windows.go` bounds-checks pixel dimensions and `GetWindowRect` return value
- `saveWindowSize` logs `WriteFile` errors

## [0.4.1] - 2026-03-14

### Added

- Multi-instance support via `QUIL_HOME` env var — run production and dev instances simultaneously
- `--dev` CLI flag — uses `.quil/` in project root for isolated dev data
- Dev launcher scripts: `quil-dev.sh` / `quil-dev.ps1`
- `[dev]` indicator in status bar when running in dev mode
- `TestQuilDir_EnvOverride` test for env var override

### Fixed

- Daemon log file permission changed from `0644` to `0600` for consistency with other sensitive files
- `resizeAllPanes()` nil guard — prevents panic when tab has no panes
- `os.Executable()` error handling in `--dev` flag — exits with clear message instead of silent fallback

## [0.4.0] - 2026-03-14

### Added

- Workspace snapshot persistence — tabs, panes, layout, and CWD saved to `~/.quil/workspace.json`
- Atomic file writes with `.bak` rollback for crash-safe persistence
- Ghost buffer persistence — raw PTY output saved per pane to `~/.quil/buffers/*.bin`
- Automatic workspace restore on daemon restart — tabs, panes, and layouts reconstructed from disk
- Shell respawn with saved CWD — panes reopen in the directory you were last working in
- Periodic snapshot timer (configurable via `snapshot_interval`, default 30s)
- Immediate snapshot on structural changes (tab/pane create/destroy) with 1s debounce
- Orphan buffer cleanup — removes `.bin` files for panes that no longer exist
- Ghost buffer dimming — restored panes show muted border and "restored" label until live output arrives
- Modal dialog system — F1 opens About screen with Settings editor and Shortcuts reference
- Confirmation dialogs for pane close (Ctrl+W) and tab close (Alt+W)
- Tab index numbers in tab bar (`1:Shell`, `2:Build`) matching Alt+1-9 shortcuts
- Auto-recovery — deleting last tab or last pane auto-creates a fresh replacement
- PTY output coalescing — 2ms timer batches rapid output to prevent visual tearing with interactive tools
- Version display in status bar and About dialog
- Developer utility scripts: `kill-daemon.sh/.ps1`, `reset-daemon.sh/.ps1`
- Build-time version injection via `-ldflags` in `dev.sh`

### Fixed

- Scrollback rendering now preserves ANSI colors (cell styles were previously dropped)
- Escape key forwarded to PTY — was mapped as `"escape"` but Bubble Tea uses `"esc"`
- Tab switch state broadcast — `handleSwitchTab` now calls `broadcastState()` + `snapshotDebounced()`
- Tab switch evaluation order — separated `switchTab()` from return to prevent stale `activeTab`
- Active tab index clamped after workspace state sync to prevent out-of-bounds

## [0.3.0] - 2026-03-12

### Added

- Daemon process detachment — survives TUI exit on all platforms (Unix: `Setsid`, Windows: `DETACHED_PROCESS`)
- `quil daemon status` command — reports daemon PID and connectivity
- PID file tracking (`~/.quil/quild.pid`) for lifecycle management
- `quild --background` flag — suppresses stdout/stderr for silent auto-start
- Daemon binary co-location lookup — finds `quild` alongside `quil` when not on PATH (fixes Windows Go 1.19+ LookPath)
- Stale socket cleanup — detects dead daemon sockets and removes them before starting fresh

### Fixed

- Daemon dying when TUI exits on Windows (missing `DETACHED_PROCESS` creation flag)
- `os.Exit(0)` in shutdown handler skipping deferred cleanup — replaced with channel-based signaling
- PID file written before `~/.quil/` directory guaranteed to exist

## [0.2.0] - 2026-03-12

### Added

- Client-daemon architecture with IPC over Unix sockets (Named Pipes on Windows)
- Cross-platform PTY management (`creack/pty` on Unix, ConPTY on Windows)
- Bubble Tea TUI with tab bar, bordered panes, and status bar
- Horizontal and vertical pane splitting
- Keyboard navigation between panes and tabs
- TOML configuration with sensible defaults (`~/.quil/config.toml`)
- Daemon auto-start on first client attach
- `quil daemon start/stop` CLI commands
- Length-prefixed JSON IPC protocol with typed messages
- Default shell workspace created on first attach
- Docker-based development workflow (`dev.sh`) — no local Go or make required
- Multi-stage Dockerfile producing minimal scratch-based release images
- `.dockerignore` for optimized Docker build context
- Binary split pane layout with mixed horizontal/vertical splits (tmux-style)
- Layout persistence — pane tree serialized to JSON, restored on reconnect
- Output history replay — ring buffer captures PTY output, replayed to reconnecting clients
- VT100 terminal emulation via `charmbracelet/x/vt` for proper ANSI rendering
- Live CWD tracking — pane border updates via OSC 7 escape sequences
- Automatic shell integration — OSC 7 hooks injected into bash, zsh, PowerShell at spawn time
- Tab renaming (F2) and pane renaming (Alt+F2)
- Tab color cycling (Alt+C) with 8 color options
- Mouse support — click to switch tabs/panes, scroll wheel for terminal history
- Clipboard paste (Ctrl+V) with cross-platform support (Win32 API, pbpaste, xclip)
- Terminal scrollback with page scroll (Alt+PgUp/PgDown) and scrollbar indicator
- Resize debouncing for smooth terminal resizing
- Configurable keybindings via `[keybindings]` in config.toml
- Configurable mouse scroll lines and page scroll lines via `[ui]` in config.toml
- Structured logging for both client and daemon (`~/.quil/*.log`)
