# Aethel — Project Instructions

## What is this?

Aethel is a persistent workflow orchestrator / terminal multiplexer for AI-native developers. Written in Go with a Bubble Tea TUI frontend.

## Tech Stack

- **Language:** Go 1.24
- **Module path:** `github.com/artyomsv/aethel`
- **TUI:** Bubble Tea v1 (`github.com/charmbracelet/bubbletea` v1.3.10)
- **Styling:** Lipgloss v1 (`github.com/charmbracelet/lipgloss` v1.1.0)
- **PTY (Unix):** `creack/pty/v2`
- **PTY (Windows):** `charmbracelet/x/conpty` v0.2.0
- **Config:** TOML via `BurntSushi/toml`
- **IDs:** `google/uuid`

## Architecture

Client-daemon model:
- `cmd/aethel/` — TUI client (Bubble Tea)
- `cmd/aetheld/` — Background daemon
- `internal/config/` — TOML configuration
- `internal/daemon/` — Session manager, message routing
- `internal/ipc/` — Length-prefixed JSON protocol (4-byte big-endian uint32 + JSON)
- `internal/persist/` — Atomic workspace/buffer persistence (JSON snapshots, binary ghost buffers)
- `internal/pty/` — Cross-platform PTY (build tags: `linux || darwin || freebsd`, `windows`)
- `internal/shellinit/` — Automatic OSC 7 shell integration (embedded init scripts, `//go:embed`)
- `internal/plugin/` — Pane plugin system (registry, built-ins, TOML loading, scraper)
- `internal/tui/` — Bubble Tea model, tabs, panes, layout tree, styles

## Building

Go and make are NOT installed locally. Use `dev.sh` (Docker-based):

```bash
./dev.sh build          # Build TUI binaries (aethel + aetheld)
./dev.sh test           # Run tests
./dev.sh test-race      # Tests with race detector (CGo — handled automatically)
./dev.sh vet            # Lint
./dev.sh cross          # Cross-compile all platforms
./dev.sh image          # Build scratch-based Docker image
./dev.sh clean          # Remove built binaries
```

Go module cache is persisted in a Docker volume (`aethel-gomod`) for fast repeated builds.

## Key Conventions

- Platform-specific code uses `//go:build` tags (not `// +build`)
- ConPTY API: `conpty.New(width, height, flags)` — 3 args, uses `Spawn()`, reads/writes directly on ConPty object
- Bubble Tea v2 / Lipgloss v2 are NOT available — use v1 import paths
- IPC protocol: 4-byte big-endian length prefix + JSON payload
- `.gitignore` uses root-anchored patterns (`/aethel`, `/aetheld`) to avoid matching `cmd/` directories
- Pane layout uses a binary split tree (`LayoutNode` in `internal/tui/layout.go`) — each internal node has its own `SplitDir`, enabling mixed H/V splits (tmux-style). The tree is serialized to JSON and persisted in the daemon's `Tab.Layout` field for reconnect restoration
- Layout persistence: TUI sends `MsgUpdateLayout` after every state sync; daemon stores it opaquely (no broadcast to avoid feedback loop). On reconnect, `applyWorkspaceState()` deserializes the tree and prunes missing panes
- Pane naming: `MsgUpdatePane` IPC message, `Pane.Name` field in daemon, Alt+F2 keybinding to rename active pane (mirrors F2 tab rename pattern)
- Shell integration: Daemon auto-injects OSC 7 hooks via `internal/shellinit/` — bash (`--rcfile`), zsh (`ZDOTDIR`), PowerShell (`-File`), fish (native). Init scripts written to `~/.aethel/shellinit/` at daemon startup. PTY `SetEnv()` passes env vars to child process
- Daemon detachment: `cmd/aethel/proc_unix.go` and `proc_windows.go` supply `daemonSysProcAttr()` via build tags — mirrors the `internal/pty/` pattern. Unix uses `Setsid`, Windows uses `DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP`
- Daemon auto-start: TUI auto-starts `aetheld --background` if not running. `findDaemonBinary()` checks PATH then the executable's own directory. PID file at `~/.aethel/aetheld.pid`, stale socket cleanup before spawn
- Daemon shutdown: `MsgShutdown` signals via channel (not `os.Exit`) so defers in `main()` run cleanly (PID file removal, log file close)
- Persistence: `internal/persist/` handles atomic file I/O — `snapshot.go` for workspace JSON (write `.tmp` → rotate to `.bak` → rename), `ghostbuf.go` for per-pane binary buffers. Both use temp+rename for crash safety
- Workspace restore: On daemon start, `restoreWorkspace()` loads `~/.aethel/workspace.json`, reconstructs tabs/panes, loads ghost buffers from `~/.aethel/buffers/`, then `respawnPanes()` spawns processes per plugin type with saved CWD and resume strategy
- Snapshot triggers: centralized event queue via `snapshotCh` channel — `requestSnapshot()` sends non-blocking request, `Wait()` loop debounces with 500ms `time.AfterFunc`. Triggers: create/destroy tab/pane, switch tab, update layout, update pane, client disconnect. Periodic timer (30s) as safety net. Final flush on daemon stop
- Dialog system: `internal/tui/dialog.go` — modal dialogs with `dialogScreen` iota (`dialogAbout`, `dialogSettings`, `dialogShortcuts`, `dialogConfirm`, `dialogCreatePane`, `dialogPluginError`, `dialogInstanceForm`, `dialogPlugins`, `dialogTOMLEditor`). F1 opens About dialog with Settings/Shortcuts/Plugins menu. Ctrl+N opens typed pane creation dialog (category → plugin → instance → split). Instance form rendered from plugin `FormFields`. TOML editor renders full-screen (bypasses dialog box). Confirm dialog used for pane/tab close (Ctrl+W / Alt+W) and instance deletion
- Output coalescing: `streamPTYOutput()` uses goroutine + 2ms timer to batch rapid PTY output before IPC broadcast, preventing visual tearing with interactive TUI tools
- Ghost buffer dimming: `PaneOutputPayload.Ghost` flag distinguishes replay from live output. Panes show muted border + "restored" label until first live output clears the flag. Controlled by `GhostBufferConfig.Dimmed` (default true)
- GhostSnap restore: `Pane.GhostSnap` stores pure disk-loaded ghost data separately from `OutputBuf` (live ring buffer). `handleAttach` prefers GhostSnap for first client after restore (prevents respawned shell init output from contaminating history replay), falls back to OutputBuf for reconnects. Cleared after first replay. TUI skips `ResetVT()` for terminal panes on ghost→live transition
- Diagnostic logging: daemon logs IPC dispatch (excluding high-frequency input/resize/layout), client attach/disconnect, snapshot metrics (tabs, panes, buffer bytes, duration), ghost replay details (source, bytes), spawn commands, tab/pane lifecycle. TUI logs ghost transitions, workspace state, layout restore. IPC server logs connection count on connect/disconnect
- Tab bar: tabs show 1-based index prefix (`1:Shell`, `2:Build`) matching Alt+1-9 shortcuts. Index hidden during rename editing
- Auto-recovery: deleting the last tab auto-creates a new "Shell" tab; deleting the last pane in a tab auto-creates a fresh pane
- Plugin system: `internal/plugin/` — pane types defined via `PanePlugin` struct. Terminal is a Go built-in in `builtin.go`; claude-code, ssh, stripe are embedded TOML defaults in `defaults/*.toml` (written to `~/.aethel/plugins/` on first run via `EnsureDefaultPlugins`). User TOML plugins override defaults. `Registry` manages loading, detection, and lookup. Plugins define `FormFields` + `ArgTemplate` for instance creation forms. `GhostBuffer` bool controls per-plugin ghost buffer persistence
- Plugin instances: `internal/tui/instances.go` — `InstanceStore` (map[pluginName][]SavedInstance) persisted to `~/.aethel/instances.json`. `BuildArgs` expands `{placeholder}` templates from form field values
- TOML editor: `internal/tui/editor.go` — full-screen multi-line text editor with rune-aware cursor, TOML syntax highlighting (comments grey, sections orange, keys blue, strings green), TOML validation on save. Accessible via F1 → Plugins → select plugin
- Pane type fields: `Pane.Type` (plugin name, default "terminal"), `Pane.PluginState` (scraped key-values), `Pane.InstanceName`, `Pane.InstanceArgs`. All persisted in workspace JSON, backward compatible (missing `type` → "terminal")
- Resume strategies: `cwd_only` (terminal), `rerun` (stripe, ssh), `preassign_id` (claude-code), `session_scrape`, `none`. Dispatched in `spawnPane()` with `restoring` flag
- Window size persistence: `~/.aethel/window.json` stores cols, rows, pixel dimensions, and maximized state. Saved on TUI exit, restored on launch via platform-specific code (`cmd/aethel/window_windows.go` uses Win32 `MoveWindow`/`ShowWindow`, `cmd/aethel/window_unix.go` uses xterm resize sequence). Follows the same build-tag file-split pattern as `proc_unix.go`/`proc_windows.go`

## Dev Mode

Run a separate dev instance alongside production using `--dev` or `AETHEL_HOME`:

```bash
./aethel --dev              # Uses .aethel/ in project root (gitignored)
./aethel-dev.sh             # Shortcut (Linux/macOS)
./aethel-dev.ps1            # Shortcut (Windows PowerShell)
AETHEL_HOME=/custom/path ./aethel  # Arbitrary data directory
```

`AETHEL_HOME` overrides `AethelDir()` — all derived paths (socket, PID, config, workspace, buffers, logs, shellinit) use the specified directory. The `[dev]` indicator appears in the status bar when active.

## Developer Utilities

```bash
./kill-daemon.sh        # Force-stop daemon (Linux/macOS)
./kill-daemon.ps1       # Force-stop daemon (Windows PowerShell)
./reset-daemon.sh       # Stop daemon + wipe persisted state (Linux/macOS)
./reset-daemon.ps1      # Stop daemon + wipe persisted state (Windows PowerShell)
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
- **M5:** Polish — JSON transformer, observability, encrypted tokens
- **M6:** Pane Focus — full-window focus mode for single pane
- **M7:** Pane Notes — side-by-side note-taking linked to panes
- **M8:** Bubble Tea v2 migration
