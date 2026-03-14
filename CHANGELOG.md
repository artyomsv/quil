# Changelog

All notable changes to Aethel will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
