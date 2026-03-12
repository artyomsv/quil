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

## Documents

- `PRD.md` — Full product requirements document
- `VISION.md` — Project vision
- `ARCHITECTURE.md` — Architecture Decision Records
- `CHANGELOG.md` — Keep a Changelog format
- `docs/plans/` — Implementation plans

## Milestones

- **M1 (Done):** Foundation — daemon, TUI, IPC, PTY, tabs, splits
- **M2 (Next):** State persistence — snapshots, ghost buffers, reboot-proof sessions
- **M3:** Resume engine — regex scrapers, AI session resume
- **M4:** Plugin system — TOML plugins, typed panes
- **M5:** Polish — JSON transformer, observability, encrypted tokens
