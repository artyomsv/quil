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
- Workspace restore: On daemon start, `restoreWorkspace()` loads `~/.aethel/workspace.json`, reconstructs tabs/panes, loads ghost buffers from `~/.aethel/buffers/`, then `respawnShells()` spawns new shell processes with saved CWD
- Snapshot triggers: periodic timer (configurable `snapshot_interval`, default 30s) + immediate on structural changes (create/destroy tab/pane) with 1s debounce + final flush on daemon stop
- Dialog system: `internal/tui/dialog.go` — modal dialogs with `dialogScreen` iota (`dialogAbout`, `dialogSettings`, `dialogShortcuts`, `dialogConfirm`). F1 opens About dialog. Settings editor mutates `m.cfg` in-memory (session-only). Confirm dialog used for pane/tab close (Ctrl+W / Alt+W)
- Output coalescing: `streamPTYOutput()` uses goroutine + 2ms timer to batch rapid PTY output before IPC broadcast, preventing visual tearing with interactive TUI tools
- Ghost buffer dimming: `PaneOutputPayload.Ghost` flag distinguishes replay from live output. Panes show muted border + "restored" label until first live output clears the flag. Controlled by `GhostBufferConfig.Dimmed` (default true)
- Tab bar: tabs show 1-based index prefix (`1:Shell`, `2:Build`) matching Alt+1-9 shortcuts. Index hidden during rename editing
- Auto-recovery: deleting the last tab auto-creates a new "Shell" tab; deleting the last pane in a tab auto-creates a fresh pane

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

## Milestones

- **M1 (Done):** Foundation — daemon, TUI, IPC, PTY, tabs, splits, shell integration, mouse, scrollback, daemon lifecycle
- **M2 (Done):** Persistence — workspace snapshots, ghost buffer persistence, shell respawn, reboot-proof sessions
- **M3:** Resume engine — regex scrapers, AI session resume
- **M4:** Plugin system — TOML plugins, typed panes
- **M5:** Polish — JSON transformer, observability, encrypted tokens
