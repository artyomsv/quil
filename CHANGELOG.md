# Changelog

All notable changes to Aethel will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.13.0] - 2026-04-07

### Added

- **Pane Notes (M7)** — `Alt+E` opens a plain-text notes editor alongside the bound pane. The bound pane auto-expands to fill the available area on the left (other panes hidden, like `Ctrl+E` focus mode) and the editor takes ~40% on the right. `Alt+E` again or `Esc` exits, reverting the original layout
- **Tab/Shift+Tab focus cycle** — while notes mode is active, `Tab` and `Shift+Tab` cycle keyboard focus between the editor (default) and the bound pane. Editor-focused: text input goes to notes, border bright blue, status bar `[notes]`/`[notes*]`. Pane-focused: keys reach the PTY normally, border dim grey, status bar `[notes pane]`
- **Mouse selection in the notes editor** — click positions the cursor; click+drag creates a selection (highlighted in reverse video). Works with the existing `editorExtractText` so `Enter` and right-click both copy. Click in the pane area while notes mode is on hands keyboard focus to the pane (no Tab needed)
- **Right-click copy** — right-click in the notes editor copies the active selection to the clipboard and clears the highlight, mirroring the existing pane right-click behaviour. The notes selection takes priority over a pane selection while notes mode is active
- **Per-pane notes storage** — one markdown file per pane at `~/.aethel/notes/<pane-id>.md`. Atomic temp+rename writes via `internal/persist/notes.go` (`os.CreateTemp` for race-free temp filenames, `Lstat` symlink rejection, Windows reserved-name validation). Notes survive pane destruction — orphan notes remain on disk for a future browser
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
- `cmd/aethel/main.go` calls `Model.FlushNotes()` on TUI exit as a safety net for unsaved notes

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

- **MCP Server (M10)** — `aethel mcp` subcommand exposes Aethel to AI assistants via Model Context Protocol. 13 tools: `list_panes`, `read_pane_output`, `send_to_pane`, `get_pane_status`, `create_pane`, `send_keys`, `restart_pane`, `screenshot_pane`, `switch_tab`, `list_tabs`, `destroy_pane`, `set_active_pane`, `close_tui`
- **Official MCP SDK** — uses `modelcontextprotocol/go-sdk` v1.4+ with typed tool handlers and struct-based input schemas
- **Request-response IPC** — backward-compatible `Message.ID` field for correlating MCP requests; daemon responds to specific connection when ID is set, broadcasts when empty
- **Process exit tracking** — `WaitExit()` on PTY `Session` interface with `sync.Once` for safe concurrent access; `Pane.ExitCode` and `Pane.ExitedAt` fields
- **VT-emulated screenshots** — `screenshot_pane` tool feeds ring buffer through `charmbracelet/x/vt` terminal emulator to capture actual screen state; essential for interactive TUI apps
- **Named key sequences** — `send_keys` tool with 50+ key mappings (arrows, function keys, ctrl+a-z); escape sequences sent individually with 50ms pacing for TUI compatibility
- **Orange MCP highlight** — pane border flashes orange (color 208) when AI interacts via MCP; configurable duration via `[mcp] highlight_duration` (default 10s)
- **Per-pane MCP logging** — interaction metadata logged to `~/.aethel/mcp-logs/{pane-id}.log`; two-layer redaction: AI markers (`<<REDACT>>...<</REDACT>>`) + regex fallback for common secret patterns
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
- **One-line install script** — `scripts/install.sh` detects OS/arch, fetches latest release from GitHub API, verifies SHA256 checksum, installs to `~/.local/bin/`; supports `AETHEL_VERSION` for pinned installs and `GITHUB_TOKEN` for API auth
- **Daemon version reporting** — `aetheld version` subcommand, version logged at startup; consistent `-ldflags` injection across all build paths (GoReleaser, dev.sh, dev.ps1, rebuild.ps1, Makefile)

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
