# Quil

**The Persistent Workflow Orchestrator for AI-Native Development**

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8.svg)](https://go.dev)
[![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20macOS%20%7C%20Windows-lightgrey.svg)]()

---

Quil is a terminal multiplexer built for developers who work with AI coding assistants. Unlike tmux or screen, Quil understands **projects** — it persists your entire workspace across reboots, automatically resumes AI sessions, and provides typed panes with context-aware behaviors.

Type `quil` after a reboot and your entire multi-tool environment snaps back: Claude Code conversations resumed, webhooks re-connected, builds re-watching.

## The Problem

Agentic developers run 5-10 terminal sessions per project: AI assistants, webhook listeners, build watchers, SSH tunnels. Every reboot destroys this setup. Re-opening tabs, re-attaching sessions, and re-typing resume commands is a daily 10-15 minute tax.

**Existing tools don't solve this:**

| Tool | Gap |
|---|---|
| tmux / screen | No concept of "projects" or typed sessions. No automatic resume. |
| Terminal emulators | Great rendering, zero persistence across reboots. |
| IDE terminals | Tied to a single editor. Can't orchestrate standalone CLI tools. |

## Features

### Reboot-Proof Sessions

Quil continuously snapshots your workspace — tabs, panes, layouts, working directories, and metadata. On restart, everything is restored. Ghost buffers render the last 500 lines instantly while shells re-initialize.

- **Output replay** — a ring buffer per pane captures PTY output. Reconnecting clients instantly see previous terminal content.
- **Layout persistence** — the pane split tree is serialized to JSON and stored in the daemon. On reconnect, the exact split configuration is restored.

### tmux-Style Pane Splits

Binary split tree enables arbitrarily nested horizontal and vertical splits. Each split has its own direction and ratio. Mouse clicks resolve to the correct pane via spatial hit-testing.

### Live CWD Tracking

Pane borders display the shell's current working directory in real-time. Quil auto-injects OSC 7 hooks into bash, zsh, and PowerShell at spawn time — no manual shell configuration required. Fish emits OSC 7 natively.

### Mouse & Keyboard

Full mouse support — click tabs to switch, click panes to focus, scroll wheel for terminal history. All keybindings are configurable via `config.toml`.

### Text Selection & Clipboard

Select text in terminal panes with Shift+Arrow (character), Ctrl+Shift+Arrow (word), or mouse click+drag. Enter copies selection to clipboard, Ctrl+V pastes with bracketed paste support. The TOML plugin editor also supports full text selection, clipboard, and paragraph navigation.

### Tab Customization

Rename tabs (F2) and panes (Alt+F2). Cycle through 8 tab colors (Alt+C) for visual distinction. Clipboard paste (Ctrl+V) with cross-platform support.

### AI Session Resume

Claude Code sessions resume automatically after reboot. Quil assigns a UUID to each AI pane at creation and uses `claude --resume <session-id>` on restart — no manual copy-paste. Other tools can use regex scraping or command re-run strategies.

### Typed Panes via Plugins

Panes aren't just shells. Press `Ctrl+N` to create a typed pane from 4 built-in plugins:

| Plugin | Description | Resume Strategy |
|--------|-------------|-----------------|
| **Terminal** | System shell | Restore working directory |
| **Claude Code** | AI coding assistant | UUID-based session resume |
| **SSH** | Remote connection (POC) | Re-run same command |
| **Stripe** | Webhook listener (POC) | Re-run same command |

Create your own plugins as TOML files in `~/.quil/plugins/` without recompiling. Plugins define commands, error handlers, persistence strategies, and pre-configured instances.

### Cross-Platform

Linux, macOS, and Windows from day one. PTY management via `creack/pty` (Unix) and ConPTY (Windows). IPC over Unix domain sockets or Named Pipes.

## Architecture

```
quil (TUI Client)
    ├── Bubble Tea UI with tabs, splits, status bar
    ├── Keyboard-driven navigation
    └── Connects via IPC
            │
            ▼
quild (Daemon)
    ├── PTY session management
    ├── State persistence (JSON snapshots)
    ├── Resume engine (regex scrapers)
    └── Plugin registry (TOML definitions)
```

See [ARCHITECTURE.md](ARCHITECTURE.md) for detailed design decisions.

## Quick Start

### Install

```bash
# Linux / macOS — one-line install
curl -sSfL https://raw.githubusercontent.com/artyomsv/quil/master/scripts/install.sh | sh

# Go users
go install github.com/artyomsv/quil/cmd/quil@latest
go install github.com/artyomsv/quil/cmd/quild@latest

# Windows — download .zip from GitHub Releases
# https://github.com/artyomsv/quil/releases/latest
```

### Run

```bash
# Launch the TUI (auto-starts daemon if needed)
quil
```

### Build from Source

```bash
# With Docker (no local Go required)
./scripts/dev.ps1 build    # PowerShell (Windows)
./scripts/dev.sh build     # Bash (Linux/macOS)

# With local Go
make build
```

### Key Bindings

| Key | Action |
|---|---|
| `Ctrl+T` | New tab |
| `Ctrl+N` | New typed pane (plugin dialog) |
| `Ctrl+W` | Close active pane |
| `Alt+W` | Close active tab |
| `Alt+H` | Split horizontal |
| `Alt+V` | Split vertical |
| `Tab` / `Shift+Tab` | Navigate panes |
| `F2` | Rename active tab |
| `Alt+F2` | Rename active pane |
| `Alt+C` | Cycle tab color |
| `Alt+PgUp` / `Alt+PgDn` | Scroll page up/down |
| `Ctrl+V` | Paste from clipboard |
| `Ctrl+E` | Toggle focus mode |
| `Shift+Arrows` | Select text |
| `Enter` | Copy selection |
| `Ctrl+Q` | Quit |

All keybindings are configurable in `~/.quil/config.toml` under `[keybindings]`.

## Configuration

Quil looks for `~/.quil/config.toml`:

```toml
[daemon]
snapshot_interval = "30s"
auto_start = true

[ghost_buffer]
max_lines = 500
dimmed = true

[ui]
tab_dock = "top"
theme = "default"
mouse_scroll_lines = 3
page_scroll_lines = 0    # 0 = half-page (dynamic)
show_disclaimer = true   # beta disclaimer on startup

[keybindings]
quit = "ctrl+q"
new_tab = "ctrl+t"
close_pane = "ctrl+w"
split_horizontal = "alt+h"
split_vertical = "alt+v"
next_pane = "tab"
prev_pane = "shift+tab"
rename_tab = "f2"
rename_pane = "alt+f2"
cycle_tab_color = "alt+c"
scroll_page_up = "alt+pgup"
scroll_page_down = "alt+pgdown"
paste = "ctrl+v"
focus_pane = "ctrl+e"
```

## Project Structure

```
cmd/
├── quil/          # TUI client
└── quild/         # Background daemon
internal/
├── clipboard/       # Cross-platform clipboard read (Win32 API, pbpaste, xclip)
├── config/          # TOML configuration
├── daemon/          # Session management, message routing
├── ipc/             # Length-prefixed JSON protocol, client/server
├── persist/         # Atomic workspace/buffer persistence (JSON + binary)
├── plugin/          # Pane plugin system (registry, TOML loading, scraper)
├── pty/             # Cross-platform PTY (Unix + Windows)
├── ringbuf/         # Circular byte buffer for PTY output history
├── shellinit/       # Automatic shell integration (OSC 7 injection)
└── tui/             # Bubble Tea model, tabs, panes, layout tree, styles
```

## Development

All commands are available via `scripts/dev.sh` (Docker, no local Go) or `make` (local Go):

| Task | Docker (`scripts/dev.ps1` / `scripts/dev.sh`) | Local Go |
|---|---|---|
| Build | `build` | `make build` |
| Test | `test` | `make test` |
| Test + race detector | `test-race` | `make test-race` |
| Lint | `vet` | `make vet` |
| Cross-compile | `cross` | `make cross` |
| Docker image | `image` | — |

See [CONTRIBUTING.md](CONTRIBUTING.md) for development guidelines.

## Roadmap

| Milestone | Status | Description |
|---|---|---|
| **M1: Foundation** | Done | Daemon, TUI, IPC, PTY, tabs, splits, shell integration, mouse, scrollback, daemon lifecycle |
| **M2: Persistence** | Done | Workspace snapshots, ghost buffer persistence, shell respawn, reboot-proof sessions |
| **M3: Resume Engine** | Done | Regex scrapers, token extraction, AI session resume via pre-assigned UUIDs |
| **M4: Plugin System** | Done | TOML plugins, typed panes, pane creation dialog, error handlers, window size persistence |
| **M5: Polish** | In Progress | JSON transformer, observability, encrypted tokens, OS service integration |
| **M6: Pane Focus** | Done | Ctrl+E toggles active pane full-screen, other panes keep running |
| **M8: Bubble Tea v2** | Done | Bubble Tea v2/Lipgloss v2 migration, text selection, clipboard, editor enhancements |
| **Pre-built Binaries** | In Progress | GoReleaser, GitHub Releases, install script, cross-platform archives |

See [ROADMAP.md](ROADMAP.md) for detailed progress and feature descriptions.

## License

[MIT](LICENSE) — Copyright (c) 2026 Artjoms Stukans
