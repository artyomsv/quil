# Changelog

All notable changes to Aethel will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
- **Default TOML plugins** — claude-code, ssh, stripe shipped as editable embedded TOML files via `//go:embed`; written to `~/.aethel/plugins/` on first run, user edits preserved across upgrades
- **Plugin instance management** — `InstanceStore` persists saved SSH connections, Stripe webhooks, etc. to `~/.aethel/instances.json`; form fields + arg templates defined per-plugin
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
- TOML plugin format — user-created plugins in `~/.aethel/plugins/*.toml` with command, persistence, error handlers, and instances
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
- Window size persistence — save/restore terminal dimensions via `~/.aethel/window.json`
- Platform-specific window restore — Win32 `MoveWindow`/`ShowWindow` on Windows, xterm sequence on Unix
- Maximized window state detection and restoration via `IsZoomed`/`SW_MAXIMIZE`
- `PluginsDir()` and `WindowStatePath()` config path helpers
- Plugin state fields on `Pane` struct — `Type`, `PluginState`, `InstanceName`, `InstanceArgs`
- Workspace JSON backward compatibility — missing `type` defaults to `"terminal"`

### Changed

- `spawnShell()` replaced with generalized `spawnPane()` — dispatches by plugin type and resume strategy
- `respawnShells()` replaced with `respawnPanes()` — fallback to terminal shell on plugin spawn failure
- Ghost buffer replay skipped for TUI app panes (`preassign_id`, `session_scrape`) — prevents cursor state pollution
- Aethel cursor overlay disabled for non-terminal panes — TUI apps render their own cursor
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

- Multi-instance support via `AETHEL_HOME` env var — run production and dev instances simultaneously
- `--dev` CLI flag — uses `.aethel/` in project root for isolated dev data
- Dev launcher scripts: `aethel-dev.sh` / `aethel-dev.ps1`
- `[dev]` indicator in status bar when running in dev mode
- `TestAethelDir_EnvOverride` test for env var override

### Fixed

- Daemon log file permission changed from `0644` to `0600` for consistency with other sensitive files
- `resizeAllPanes()` nil guard — prevents panic when tab has no panes
- `os.Executable()` error handling in `--dev` flag — exits with clear message instead of silent fallback

## [0.4.0] - 2026-03-14

### Added

- Workspace snapshot persistence — tabs, panes, layout, and CWD saved to `~/.aethel/workspace.json`
- Atomic file writes with `.bak` rollback for crash-safe persistence
- Ghost buffer persistence — raw PTY output saved per pane to `~/.aethel/buffers/*.bin`
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
- `aethel daemon status` command — reports daemon PID and connectivity
- PID file tracking (`~/.aethel/aetheld.pid`) for lifecycle management
- `aetheld --background` flag — suppresses stdout/stderr for silent auto-start
- Daemon binary co-location lookup — finds `aetheld` alongside `aethel` when not on PATH (fixes Windows Go 1.19+ LookPath)
- Stale socket cleanup — detects dead daemon sockets and removes them before starting fresh

### Fixed

- Daemon dying when TUI exits on Windows (missing `DETACHED_PROCESS` creation flag)
- `os.Exit(0)` in shutdown handler skipping deferred cleanup — replaced with channel-based signaling
- PID file written before `~/.aethel/` directory guaranteed to exist

## [0.2.0] - 2026-03-12

### Added

- Client-daemon architecture with IPC over Unix sockets (Named Pipes on Windows)
- Cross-platform PTY management (`creack/pty` on Unix, ConPTY on Windows)
- Bubble Tea TUI with tab bar, bordered panes, and status bar
- Horizontal and vertical pane splitting
- Keyboard navigation between panes and tabs
- TOML configuration with sensible defaults (`~/.aethel/config.toml`)
- Daemon auto-start on first client attach
- `aethel daemon start/stop` CLI commands
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
- Structured logging for both client and daemon (`~/.aethel/*.log`)
