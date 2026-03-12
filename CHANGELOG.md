# Changelog

All notable changes to Aethel will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
