# Quil — Project Instructions

## What is this?

Quil is a persistent workflow orchestrator / terminal multiplexer for AI-native developers. Written in Go with a Bubble Tea TUI frontend.

## Tech Stack

- **Language:** Go 1.25
- **Module path:** `github.com/artyomsv/quil`
- **TUI:** Bubble Tea v2 (`charm.land/bubbletea/v2` v2.0.2)
- **Styling:** Lipgloss v2 (`charm.land/lipgloss/v2` v2.0.2)
- **PTY (Unix):** `creack/pty/v2`
- **PTY (Windows):** `charmbracelet/x/conpty` v0.2.0
- **Config:** TOML via `BurntSushi/toml`
- **IDs:** `google/uuid`

## Architecture

Client-daemon model:
- `cmd/quil/` — TUI client (Bubble Tea)
- `cmd/quild/` — Background daemon
- `internal/config/` — TOML configuration (`Load` reads, `Save` writes atomically via `.tmp` + rename). `UIConfig.ShowDisclaimer` controls startup beta dialog
- `internal/daemon/` — Session manager, message routing, event queue (`event.go` — bounded, mutex-protected, watcher pub/sub for MCP)
- `internal/ipc/` — Length-prefixed JSON protocol (4-byte big-endian uint32 + JSON)
- `internal/persist/` — Atomic workspace/buffer persistence (JSON snapshots, binary ghost buffers)
- `internal/pty/` — Cross-platform PTY (build tags: `linux || darwin || freebsd`, `windows`)
- `internal/shellinit/` — Automatic OSC 7 + OSC 133 shell integration (embedded init scripts, `//go:embed`)
- `internal/plugin/` — Pane plugin system (registry, built-ins, TOML loading, scraper)
- `internal/clipboard/` — Platform-native clipboard read/write (Win32 API, pbpaste/pbcopy, xclip/xsel)
- `internal/tui/` — Bubble Tea model, tabs, panes, layout tree, styles, text selection, notification sidebar

## Building

Go and make are NOT installed locally. Use `scripts/dev.sh` (Docker-based):

```bash
./scripts/dev.sh build          # Build all variants: prod, dev, debug (6 binaries)
./scripts/dev.sh test           # Run tests
./scripts/dev.sh test-race      # Tests with race detector (CGo — handled automatically)
./scripts/dev.sh vet            # Lint
./scripts/dev.sh cross          # Cross-compile all platforms
./scripts/dev.sh image          # Build scratch-based Docker image
./scripts/dev.sh clean          # Remove built binaries
```

### Build Variants

`build` produces three matched pairs via compile-time ldflags:

| Variant | TUI | Daemon | Behavior |
|---------|-----|--------|----------|
| **prod** | `quil.exe` | `quild.exe` | Stripped (`-s -w`), normal behavior |
| **dev** | `quil-dev.exe` | `quild-dev.exe` | Auto dev mode (`QUIL_HOME=.quil/`), debug logging, finds `quild-dev` |
| **debug** | `quil-debug.exe` | `quild-debug.exe` | Debug logging, connects to production `~/.quil/`, finds `quild-debug` |

Ldflags: `buildDevMode` (auto-sets `QUIL_HOME`), `buildLogLevel` (overrides config log level), `daemonBinary` (daemon binary name for `findDaemonBinary`). Dev variant is self-contained — just run `./quil-dev.exe`, no flags needed.

Go module cache is persisted in a Docker volume (`quil-gomod`) for fast repeated builds.

### Windows Icon

`build` and `cross` embed the Quil brand mark as a Windows executable icon via `go-winres` (v0.3.3). Build assets live in `winres/` (icon PNGs + `winres.json` manifest with `RT_GROUP_ICON` + `RT_VERSION`). The build script installs `go-winres` inside the Docker container and generates `.syso` files in `cmd/quil/` and `cmd/quild/` before `go build`. The Go linker picks up `.syso` files automatically (Windows only — ignored on Linux/Darwin). Generated `.syso` files are gitignored.

## Release Process

Single workflow (`release.yml`) with two jobs:

1. **`release` job** — triggers on push to master. Analyzes conventional commits since last tag, computes version bump (major/minor/patch), updates `VERSION` + `CHANGELOG.md`, commits `chore(release): vX.Y.Z`, creates git tag, pushes. Outputs version to the next job.
2. **`goreleaser` job** — runs after `release` job. Checks out the tagged commit, extracts release notes from `CHANGELOG.md` via sed, runs GoReleaser to cross-compile 5 platforms (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64), creates `.tar.gz` (Unix) / `.zip` (Windows) archives with both `quil` + `quild`, publishes GitHub Release with SHA256 checksums. Release notes applied via `gh release edit` (decoupled from GoReleaser's changelog system — `--release-notes` flag broke with `changelog.disable: true` in newer GoReleaser v2).

GoReleaser config: `.goreleaser.yml` (version 2). Version injected via `-ldflags "-s -w -X main.version={{.Version}}"` on both binaries. Note: both jobs are in one workflow because tags pushed with `GITHUB_TOKEN` don't trigger other workflows.

Install script: `scripts/install.sh` — POSIX shell, detects OS/arch, downloads from GitHub Releases, verifies checksum, installs to `~/.local/bin/`.

## MCP Server

`quil mcp` subcommand exposes Quil as an MCP (Model Context Protocol) server over stdio. AI tools (Claude Desktop, VS Code, Cursor) spawn this as a child process and communicate via JSON-RPC 2.0.

Architecture: thin bridge between MCP JSON-RPC (stdio) and daemon IPC (socket). The MCP bridge connects to the daemon as another IPC client — same as the TUI.

MCP SDK: `github.com/modelcontextprotocol/go-sdk` (official SDK, v1.4+). Typed tool handlers with struct-based input schemas.

15 MCP tools: `list_panes`, `read_pane_output` (ANSI-stripped), `send_to_pane`, `get_pane_status`, `create_pane`, `send_keys` (named key sequences), `restart_pane`, `screenshot_pane` (VT-emulated text screenshot), `switch_tab`, `list_tabs`, `destroy_pane`, `set_active_pane` (TUI cooperation), `close_tui` (TUI cooperation), `get_notifications` (non-blocking), `watch_notifications` (blocking, replaces polling).

IPC request-response: `Message.ID` field (omitempty, backward compatible) correlates requests with responses. Daemon responds to the requesting connection when `ID` is set, broadcasts when empty.

Key files: `cmd/quil/mcp.go` (bridge + daemon connection), `cmd/quil/mcp_tools.go` (15 tool implementations), `cmd/quil/mcp_keys.go` (key name → escape sequence map), `cmd/quil/mcp_log.go` (per-pane interaction logging + two-layer redaction).

AI tool configuration:
```json
{"mcpServers": {"quil": {"command": "quil", "args": ["mcp"]}}}
```

## Key Conventions

- Platform-specific code uses `//go:build` tags (not `// +build`)
- ConPTY API: `conpty.New(width, height, flags)` — 3 args, uses `Spawn()`, reads/writes directly on ConPty object
- Bubble Tea v2 / Lipgloss v2 — import paths: `charm.land/bubbletea/v2`, `charm.land/lipgloss/v2`. View() returns `tea.View` struct (not string). KeyMsg is `tea.KeyPressMsg`. MouseMsg split into `tea.MouseClickMsg`, `tea.MouseWheelMsg`, `tea.MouseMotionMsg`, `tea.MouseReleaseMsg`. Clipboard via `internal/clipboard` (platform-native: Win32/pbcopy/xclip). Paste wraps in bracketed paste sequences (`\x1b[200~...\x1b[201~`). Mouse modifiers: `msg.Mod.Contains(tea.ModCtrl)`. Quit: `tea.Quit` (function value, not call)
- IPC protocol: 4-byte big-endian length prefix + JSON payload. Optional `ID` field for request-response correlation (MCP bridge). When `ID` is set, daemon responds to specific connection; when empty, broadcasts to all
- `.gitignore` uses root-anchored patterns (`/quil`, `/quild`) to avoid matching `cmd/` directories
- Pane layout uses a binary split tree (`LayoutNode` in `internal/tui/layout.go`) — each internal node has its own `SplitDir`, enabling mixed H/V splits (tmux-style). The tree is serialized to JSON and persisted in the daemon's `Tab.Layout` field for reconnect restoration
- Layout persistence: TUI sends `MsgUpdateLayout` after every state sync; daemon stores it opaquely (no broadcast to avoid feedback loop). On reconnect, `applyWorkspaceState()` deserializes the tree and prunes missing panes
- Pane naming: `MsgUpdatePane` IPC message, `Pane.Name` field in daemon, Alt+F2 keybinding to rename active pane (mirrors F2 tab rename pattern)
- Shell integration: Daemon auto-injects OSC 7 + OSC 133 hooks via `internal/shellinit/` — bash (`--rcfile`), zsh (`ZDOTDIR`), PowerShell (`-File`), fish (native). Init scripts written to `~/.quil/shellinit/` at daemon startup. PTY `SetEnv()` passes env vars to child process. OSC 133 markers (`A` prompt start, `B` command start, `D;exitcode` command done) enable precise command completion detection for notification events
- Daemon detachment: `cmd/quil/proc_unix.go` and `proc_windows.go` supply `daemonSysProcAttr()` via build tags — mirrors the `internal/pty/` pattern. Unix uses `Setsid`, Windows uses `DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP`
- Daemon auto-start: TUI auto-starts `quild --background` if not running. `findDaemonBinary()` checks PATH then the executable's own directory. PID file at `~/.quil/quild.pid`, stale socket cleanup before spawn
- Daemon shutdown: `MsgShutdown` signals via channel (not `os.Exit`) so defers in `main()` run cleanly (PID file removal, log file close). `sync.Once` guards `close(d.shutdown)` against double-close panic
- Notification center: `internal/daemon/event.go` — bounded event queue (mutex-protected, not channel) survives TUI disconnects and replays on attach. `connWatcher` one-shot pub/sub for `watch_notifications` MCP tool. Events emitted by: process exit, OSC 133 command completion, bell detection (30s cooldown), smart idle analysis. `internal/tui/notification.go` — sidebar toggled via Alt+N (3-state: hidden → visible+unfocused → visible+focused → hidden), F3 focuses sidebar. Pane history stack with Alt+Backspace navigation
- Smart idle analysis: `idleChecker()` goroutine ticks 1s, `checkIdlePanes()` reads last 4KB of ring buffer at idle (5s no output), strips ANSI, matches against plugin `[[idle_handlers]]` patterns. 30s cooldown per pane via `LastIdleEventAt`. Single `PluginMu` lock span for read+conditional write (race fix)
- Persistence: `internal/persist/` handles atomic file I/O — `snapshot.go` for workspace JSON (write `.tmp` → rotate to `.bak` → rename), `ghostbuf.go` for per-pane binary buffers. Both use temp+rename for crash safety
- Workspace restore: On daemon start, `restoreWorkspace()` loads `~/.quil/workspace.json`, reconstructs tabs/panes, loads ghost buffers from `~/.quil/buffers/`, then `respawnPanes()` spawns processes per plugin type with saved CWD and resume strategy
- Snapshot triggers: centralized event queue via `snapshotCh` channel — `requestSnapshot()` sends non-blocking request, `Wait()` loop debounces with 500ms `time.AfterFunc`. Triggers: create/destroy tab/pane, switch tab, update layout, update pane, client disconnect. Periodic timer (30s) as safety net. Final flush on daemon stop
- Dialog system: `internal/tui/dialog.go` — modal dialogs with `dialogScreen` iota (`dialogAbout`, `dialogSettings`, `dialogShortcuts`, `dialogConfirm`, `dialogCreatePane`, `dialogCreatePaneSetup`, `dialogPluginError`, `dialogInstanceForm`, `dialogPlugins`, `dialogTOMLEditor`, `dialogLogViewer`, `dialogDisclaimer`, `dialogPluginMigration`). F1 opens About dialog with 6 items: Settings, Shortcuts, Plugins, View client log, View daemon log, View MCP logs. Ctrl+N opens typed pane creation dialog (category → plugin → instance → setup → split). `dialogCreatePaneSetup` (between plugin/instance and split) renders the directory browser + runtime toggle checkboxes for plugins that opt in via `prompts_cwd` / `[[command.toggles]]`. CWD browser pre-fills from `lastSelectedCWD` (remembers previous choice within the TUI session), then active pane OSC 7 CWD, then home dir; candidates are tried in order — stale directories are skipped and the memory is cleared. Instance form rendered from plugin `FormFields`. TOML editor renders full-screen (bypasses dialog box). `dialogLogViewer` reuses the same full-screen `TextEditor` but with `ReadOnly=true` and `HighlightPlain` so users can scroll/copy log content without overwriting it. `Alt+Up`/`Alt+Down` jump the cursor by `cfg.UI.LogViewerPageLines` lines (default 40). `openLogViewer(label, path)` and `openMCPLogsViewer()` build the read-only buffer; `readLogTail` caps each file at 256 KB and trims to a clean line boundary. Confirm dialog used for pane/tab close (Ctrl+W / Alt+W) and instance deletion
- Beta disclaimer: `dialogDisclaimer` shown on startup when `ui.show_disclaimer = true` (default). Random tip from `disclaimerTips` slice. Two buttons: OK (close) and "Don't show again" (sets `cfg.UI.ShowDisclaimer = false`, persisted via `config.Save()` on TUI exit). `configChanged` flag on Model signals `main.go` to write config
- Plugin schema migration: `dialogPluginMigration` shown on startup when `EnsureDefaultPlugins` detects plugins whose `schema_version` < embedded default. Full-screen split view (`internal/tui/migration.go`): editable user config on the left, read-only new default on the right (both `TextEditor` with TOML highlighting). Tab bar for multiple stale plugins. Blocks until all resolved — Esc is a no-op, only Enter (save merged), A (accept default), or Ctrl+Q (quit) exit. On save: validates TOML syntax + `schema_version >= required`, writes to disk, reloads plugin registry. `StalePlugin` struct in `internal/plugin/plugin.go` carries user data + default data from `EnsureDefaultPlugins`. `ParseSchemaVersion` (exported) extracts `[plugin].schema_version` from TOML bytes
- Output coalescing: `streamPTYOutput()` uses goroutine + 2ms timer to batch rapid PTY output before IPC broadcast, preventing visual tearing with interactive TUI tools
- Ghost buffer dimming: `PaneOutputPayload.Ghost` flag distinguishes replay from live output. Panes show muted border + "restored" label until first live output clears the flag. Controlled by `GhostBufferConfig.Dimmed` (default true)
- GhostSnap restore: `Pane.GhostSnap` stores pure disk-loaded ghost data separately from `OutputBuf` (live ring buffer). `handleAttach` prefers GhostSnap for first client after restore (prevents respawned shell init output from contaminating history replay), falls back to OutputBuf for reconnects. Cleared after first replay. TUI skips `ResetVT()` for terminal panes on ghost→live transition
- Diagnostic logging: daemon logs IPC dispatch (excluding high-frequency input/resize/layout), client attach/disconnect, snapshot metrics (tabs, panes, buffer bytes, duration), ghost replay details (source, bytes), spawn commands, tab/pane lifecycle. TUI logs ghost transitions, workspace state, layout restore. IPC server logs connection count on connect/disconnect. **Leveled logging**: `internal/logger/` wraps Go's stdlib `slog` and bridges the existing 152 `log.Printf` call sites at info level. Configure via `[logging] level = "debug|info|warn|error"` in `~/.quil/config.toml`. Use `logger.Debug(...)` for verbose traces (clipboard pipeline, per-key handler, etc.) that should be off by default. Both daemon (`cmd/quild/main.go`) and TUI (`cmd/quil/main.go`) load config FIRST then call `logger.Init(cfg.Logging.Level, file)` so legacy and new log paths share one filter
- Tab bar: tabs show 1-based index prefix (`1:Shell`, `2:Build`) matching Alt+1-9 shortcuts. Index hidden during rename editing
- Auto-recovery: deleting the last tab auto-creates a new "Shell" tab; deleting the last pane in a tab auto-creates a fresh pane
- Spatial pane navigation: `internal/tui/tab.go` — `TabModel.NavigateDirection(dir Direction)` walks `CollectRects` (top-down geometry), filters by half-plane (`directionScore`), and picks the candidate with three tie-breakers: smallest gap, largest perpendicular overlap, smallest perpendicular center distance (tmux/vim parity). Default keys are `Alt+Left/Right/Up/Down` (`pane_left/right/up/down` in `[keybindings]`). Tab/Shift+Tab and `Alt+H/V` are deliberately NOT bound at the global level — they fall through to the PTY (shell completion, Claude Code mode toggle, claude-code's image paste). Splits live on `Alt+Shift+H/V`. Disabled in focus mode and on single-pane tabs (no-op). Vim users can rebind to `alt+h/l/k/j` in `config.toml`. Tests in `layout_test.go` cover all four directions, the no-overlap rejection branch, and the center-distance tie-breaker
- Settings dialog persistence: every Settings setter (snapshot interval, ghost dimmed, ghost buffer lines, mouse scroll lines, page scroll lines, log level, show disclaimer) flips `m.configChanged = true` so edits survive `cmd/quil/main.go`'s `if m.ConfigChanged()` check on TUI exit. Earlier versions only flagged the disclaimer field, silently dropping every other Settings edit. Log-level changes apply on the next launch (no live re-init); the file handle owned by `main.go` isn't re-plumbed into the Model
- Plugin system: `internal/plugin/` — pane types defined via `PanePlugin` struct. Terminal is a Go built-in in `builtin.go`; claude-code, ssh, stripe are embedded TOML defaults in `defaults/*.toml` (written to `~/.quil/plugins/` on first run via `EnsureDefaultPlugins`). User TOML plugins override defaults. `schema_version` field in `[plugin]` section tracks TOML schema — `EnsureDefaultPlugins` detects stale files (user version < embedded) and returns `[]StalePlugin` for the TUI migration dialog instead of silently overwriting. Bump `schema_version` in embedded defaults when adding new fields or changing defaults. `Registry` manages loading, detection, and lookup. Plugins define `FormFields` + `ArgTemplate` for instance creation forms. `GhostBuffer` bool controls per-plugin ghost buffer persistence. `[[idle_handlers]]` TOML section for context-aware idle notifications (parallel to `[[error_handlers]]`). Optional `path` field in `[command]` for explicit binary location (bypasses PATH lookup). 3-tier detection: path override → `exec.LookPath` → `searchBinary` fallback. **Pane setup dialog opt-ins**: `prompts_cwd = true` under `[command]` opens a directory browser at pane creation (pre-filled from active pane's OSC 7 CWD); `[[command.toggles]]` array-of-tables (fields: `name`, `label`, `args_when_on`, `default`) renders one checkbox per toggle — enabled toggles' args are appended to `InstanceArgs`; `raw_keys = [...]` declares keys that bypass Quil's global shortcut layer and are forwarded directly to the PTY (no built-in plugin currently opts in — Tab and Shift+Tab reach the PTY naturally because pane navigation moved to `Alt+Arrow`. The mechanism stays available for future plugins that need to override some other global shortcut.). claude-code uses `prompts_cwd` + `[[command.toggles]]` for the dangerous-permissions opt-in. The TUI side lives in `internal/tui/dialog.go` (`enterSetupOrSplit`, `loadBrowseDir`, `handleCreatePaneSetupKey`, `renderCreatePaneSetupDialog`, `validateAndNormalizeCWD`) and `internal/tui/model.go` (`tryPluginRawKey`). See `docs/plugin-reference.md` for the full reference
- Plugin instances: `internal/tui/instances.go` — `InstanceStore` (map[pluginName][]SavedInstance) persisted to `~/.quil/instances.json`. `BuildArgs` expands `{placeholder}` templates from form field values
- TOML editor: `internal/tui/editor.go` — full-screen multi-line text editor with rune-aware cursor, TOML syntax highlighting (comments grey, sections orange, keys blue, strings green), TOML validation on save, text selection and clipboard support. Accessible via F1 → Plugins → select plugin. `TextEditor.Highlight` field selects the highlighter (`"toml"` default, `"plain"` for notes)
- Pane notes: `internal/tui/notes.go` — `NotesEditor` wraps `TextEditor` in plain-text mode (`HighlightPlain`). Intercepts `Ctrl+S` (calls `persist.SaveNotes`, bypassing TOML validation) and `Esc` (clears selection first, exits on second press). Public selection API: `SetCursor`, `BeginSelection`, `ExtendSelection`, `HasSelection`, `ExtractSelection`, `ClearSelection`. 30s debounce via `notesTick` Bubble Tea timer — `MaybeAutoSave()` checks `time.Since(lastEditAt)` on each tick. Storage: `internal/persist/notes.go` at `~/.quil/notes/<pane-id>.md` with atomic `os.CreateTemp`+rename, `Lstat` symlink rejection, and Windows reserved-name validation. Model state: `notesMode`, `notesEditor`, `notesPaneFocused`, `notesEnteredFocus`, `notesMouseDown`, `notesAnchorRow/Col`. Layout math (sidebar + notes panel widths) lives in a single helper `notesPanelWidth()` shared by `View()` and `notesEditorBox()`. **Auto-enters focus mode** so the bound pane fills the available area on the left; the editor takes ~40% on the right. **Tab/Shift+Tab cycles keyboard focus** between editor (default) and the bound pane — editor focused = bright blue border + keys to editor; pane focused = dim grey border + keys to PTY. Mouse: click in editor positions cursor + focuses editor; click+drag selects (anchor resolved once at click time, stored in `notesAnchorRow/Col` so scroll cannot drift it); right-click copies selection (notes selection takes priority over pane selection); click in pane area focuses the pane. Key routing: `Alt+E` exits, `Ctrl+Q` quits with flush, `Tab`/`Shift+Tab` cycle focus, structural keys (`Ctrl+W`/`Alt+W`/`Alt+Shift+H`/`Alt+Shift+V`, plus directional `Alt+Arrow` pane-nav) flush + fall through, `notesKeyExempt` allow-list of global shortcuts (rename/dialogs/F1/`Alt+1..9`/etc.) bypass the editor when editor-focused. **Single canonical teardown** in `exitNotesModeInPlace` (pointer receiver) — `exitNotesMode`, `applyWorkspaceState` reconciliation, and `switchTab` all delegate to it. The reconciliation also re-syncs `tab.ActivePane = bound` if the daemon promoted a different pane, so the editor stays next to its target. `main.go` calls `model.FlushNotes()` on TUI exit as a safety net
- Editor selection: `internal/tui/editor_selection.go` — `EditorSel`/`EditorPos` types (rune-based, independent from terminal `Selection`). Shift+Arrow char select, Ctrl+Shift+Arrow word jump, Ctrl+Alt+Shift+Arrow 3-word jump, Shift+Home/End line select, Ctrl+A select all. Enter copies selection, Ctrl+X cuts, Ctrl+V pastes (async via `editorPasteMsg`). Selection rendering via reverse video `\x1b[7m]`, cursor within selection uses `\x1b[7;4m]` (reverse+underline). Typing with selection replaces selected text
- Pane type fields: `Pane.Type` (plugin name, default "terminal"), `Pane.PluginState` (scraped key-values), `Pane.InstanceName`, `Pane.InstanceArgs`. All persisted in workspace JSON, backward compatible (missing `type` → "terminal")
- Resume strategies: `cwd_only` (terminal), `rerun` (stripe, ssh), `preassign_id` (claude-code), `session_scrape`, `none`. Dispatched in `spawnPane()` with `restoring` flag
- Window size persistence: `~/.quil/window.json` stores cols, rows, pixel dimensions, and maximized state. Saved on TUI exit, restored on launch via platform-specific code (`cmd/quil/window_windows.go` uses Win32 `MoveWindow`/`ShowWindow`, `cmd/quil/window_unix.go` uses xterm resize sequence). Follows the same build-tag file-split pattern as `proc_unix.go`/`proc_windows.go`
- Text selection: `internal/tui/selection.go` — keyboard (Shift+Arrow, Ctrl+Shift+Arrow word jump, Ctrl+Alt+Shift+Arrow 3-word jump) and mouse (click+drag). Enter copies selection to clipboard via `internal/clipboard`. Shell cursor follows selection horizontally in real-time (same-line only; cross-line is visual-only to avoid triggering command history). Selection bounded by `lastContentLine()` — won't extend into empty terminal area
- Clipboard: `internal/clipboard/` — platform-native Read/Write. Windows: Win32 `GetClipboardData`/`SetClipboardData`. Unix: `pbpaste`/`pbcopy` (macOS), `xclip`/`xsel` (Linux). Paste (`Ctrl+V`) wraps content in bracketed paste sequences. Dialog paste sanitizes control characters. **Image paste proxy**: `clipboard.ReadImage()` reads `CF_DIBV5`/`CF_DIB` on Windows (Unix is a stub), `dib.go` parses the DIB into an `image.Image` (24bpp BI_RGB, 32bpp BI_RGB and BI_BITFIELDS, top-down + bottom-up, all-zero-alpha promotion). `pasteClipboard` falls through to image when text is empty: saves PNG to `config.PasteDir()` (`~/.quil/paste/quil-paste-<timestamp>.png`) and types the path into the PTY. Works around the upstream Claude Code Windows clipboard bug (anthropics/claude-code#32791). Paste keys: `Ctrl+V` (kb.Paste — eaten by Windows Terminal), `Ctrl+Alt+V` and `F8` are hardcoded aliases; `F8` is the recommended Windows trigger because it has no AltGr ambiguity

## Dev Mode

> **Production isolation rule:** the project owner runs Quil in production from `~/.quil/`. All development work on this repo **must** happen via dev mode — do not touch the production daemon, socket, PID, workspace, or any file under `~/.quil/`. See [`.claude/rules/dev-environment.md`](./rules/dev-environment.md) for the full rule, which includes the dev-mode workflow and the list of scripts (`kill-daemon`, `reset-daemon`, bare `./quil`) that are forbidden during development.

Run a separate dev instance alongside production using the dev build, `--dev` flag, or `QUIL_HOME`:

```bash
./quil-dev.exe                    # Recommended: auto dev mode + debug logging (no flags needed)
./quil --dev                      # Uses .quil/ in project root (gitignored)
./scripts/quil-dev.sh             # Shortcut — launches quil-dev (Linux/macOS)
./scripts/quil-dev.ps1            # Shortcut — launches quil-dev.exe (Windows PowerShell)
QUIL_HOME=/custom/path ./quil     # Arbitrary data directory
```

`QUIL_HOME` overrides `QuilDir()` — all derived paths (socket, PID, config, workspace, buffers, logs, shellinit) use the specified directory. The `[dev]` indicator appears in the status bar when active. The dev build (`quil-dev.exe`) bakes in `QUIL_HOME` and debug logging via ldflags — no flags or env vars needed.

## Developer Utilities

```bash
./scripts/kill-daemon.sh        # Force-stop daemon (Linux/macOS)
./scripts/kill-daemon.ps1       # Force-stop daemon (Windows PowerShell)
./scripts/reset-daemon.sh       # Stop daemon + wipe persisted state (Linux/macOS)
./scripts/reset-daemon.ps1      # Stop daemon + wipe persisted state (Windows PowerShell)
```

## Documents

- `PRD.md` — Full product requirements document
- `VISION.md` — Project vision
- `ARCHITECTURE.md` — Architecture Decision Records
- `CHANGELOG.md` — Keep a Changelog format
- `docs/plans/` — Implementation plans
- `docs/plugin-reference.md` — Plugin configuration reference (TOML format, fields, strategies, examples)

## Milestones

- **M1 (Done):** Foundation — daemon, TUI, IPC, PTY, tabs, splits, shell integration, mouse, scrollback, daemon lifecycle
- **M2 (Done):** Persistence — workspace snapshots, ghost buffer persistence, shell respawn, reboot-proof sessions
- **M3 (Done):** Resume engine — preassign_id strategy for Claude Code, session_scrape for tools with output tokens, rerun for SSH/Stripe
- **M4 (Done):** Plugin system — typed panes, TOML plugins, plugin registry, error handlers, pane creation dialog (Ctrl+N), 4 built-in plugins: terminal + claude-code (production), ssh + stripe (POC)
- **M5 (In Progress):** Polish — pane setup dialog (CWD browser + runtime toggle checkboxes opt-in via `prompts_cwd` / `[[command.toggles]]`), spatial pane navigation (Alt+Arrow), Win32 clipboard image paste proxy (DIB→PNG, workaround for anthropics/claude-code#32791), leveled logger (`internal/logger/`), F1 read-only log viewer with `Alt+Up/Down` page navigation, project rule for production isolation, daemon `EvalSymlinks` on CWD, DIB parser dimension caps, plugin registry stale-prune. Remaining: JSON transformer, log rotation, encrypted tokens, OS service install
- **M6 (Done):** Pane Focus — Ctrl+E toggles active pane full-screen (`TabModel.focusMode`). Layout tree stays intact; `Resize()`/`View()` skip non-active panes. `* FOCUS *` in pane top border, `[focus]` in status bar. Pane nav disabled in focus. Split/close auto-exit focus. Not persisted
- **M7 (Done):** Pane Notes — `Alt+E` opens a plain-text editor alongside the active pane (split ~60/40). Notes stored one file per pane at `~/.quil/notes/<pane-id>.md` via `internal/persist/notes.go` (atomic temp+rename). Three save safety nets: 30s debounce via `notesTick`, `Ctrl+S` explicit save, flush on exit. Read-only pane while editing (all keys route to `NotesEditor`). Mutually exclusive with focus mode. Reuses `TextEditor` with new `Highlight string` field (`"plain"` bypasses TOML colouring). Notes survive pane destruction — orphans kept for Phase 2 browser
- **M8 (Done):** Bubble Tea v2 + Lipgloss v2 migration — declarative View, typed mouse events, platform-native clipboard (Win32/pbcopy/xclip), text selection (keyboard + mouse), bracketed paste
- **M10 (Done):** MCP Server — `quil mcp` exposes 15 tools via Model Context Protocol. Phase A: list_panes, read_pane_output, send_to_pane, get_pane_status, create_pane. Phase B: send_keys, restart_pane, screenshot_pane (VT-emulated), switch_tab, list_tabs, destroy_pane, set_active_pane, close_tui. Official Go SDK (`modelcontextprotocol/go-sdk`). Request-response IPC via `Message.ID` field. TUI cooperation via broadcast messages for set_active_pane and close_tui
- **M12 (Done):** Notification Center — daemon event queue (`internal/daemon/event.go`) with process exit detection and output pattern matching via `[[notification_handlers]]` TOML. TUI sidebar (`internal/tui/notification.go`) toggled via Alt+N, non-modal, coexists with panes. Pane history stack with Alt+Backspace navigation. Status bar badge `[N events]`. MCP tools: `get_notifications` (non-blocking) and `watch_notifications` (blocking, replaces polling). `requestWithTimeout` for long waits up to 5 min
