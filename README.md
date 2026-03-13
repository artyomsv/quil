# Aethel

**The Persistent Workflow Orchestrator for AI-Native Development**

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.24-00ADD8.svg)](https://go.dev)
[![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20macOS%20%7C%20Windows-lightgrey.svg)]()

---

Aethel is a terminal multiplexer built for developers who work with AI coding assistants. Unlike tmux or screen, Aethel understands **projects** — it persists your entire workspace across reboots, automatically resumes AI sessions, and provides typed panes with context-aware behaviors.

Type `aethel` after a reboot and your entire multi-tool environment snaps back: Claude Code conversations resumed, webhooks re-connected, builds re-watching.

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

Aethel continuously snapshots your workspace — tabs, panes, layouts, working directories, and metadata. On restart, everything is restored. Ghost buffers render the last 500 lines instantly while shells re-initialize.

- **Output replay** — a ring buffer per pane captures PTY output. Reconnecting clients instantly see previous terminal content.
- **Layout persistence** — the pane split tree is serialized to JSON and stored in the daemon. On reconnect, the exact split configuration is restored.

### tmux-Style Pane Splits

Binary split tree enables arbitrarily nested horizontal and vertical splits. Each split has its own direction and ratio. Mouse clicks resolve to the correct pane via spatial hit-testing.

### Live CWD Tracking

Pane borders display the shell's current working directory in real-time. Aethel auto-injects OSC 7 hooks into bash, zsh, and PowerShell at spawn time — no manual shell configuration required. Fish emits OSC 7 natively.

### Mouse & Keyboard

Full mouse support — click tabs to switch, click panes to focus, scroll wheel for terminal history. All keybindings are configurable via `config.toml`.

### Tab Customization

Rename tabs (F2) and panes (Alt+F2). Cycle through 8 tab colors (Alt+C) for visual distinction. Clipboard paste (Ctrl+V) with cross-platform support.

### AI Session Resume (Planned)

A regex-based scraper watches terminal output for session IDs. When you restart, Aethel executes `claude --resume <session-id>` automatically — no manual copy-paste.

### Typed Panes via Plugins (Planned)

Panes aren't just shells. Each pane type is a TOML plugin that defines:

- **Scraper patterns** — extract tokens from terminal output
- **Resume templates** — auto-restart tools with captured context
- **Border color rules** — red on errors, green on success
- **Quick actions** — one-key shortcuts per pane type

Ships with 4 built-in plugins: `ai`, `webhook`, `infrastructure`, `build`. Create your own without recompiling.

### Cross-Platform

Linux, macOS, and Windows from day one. PTY management via `creack/pty` (Unix) and ConPTY (Windows). IPC over Unix domain sockets or Named Pipes.

## Architecture

```
aethel (TUI Client)
    ├── Bubble Tea UI with tabs, splits, status bar
    ├── Keyboard-driven navigation
    └── Connects via IPC
            │
            ▼
aetheld (Daemon)
    ├── PTY session management
    ├── State persistence (JSON + SQLite)
    ├── Resume engine (regex scrapers)
    └── Plugin registry (TOML definitions)
```

See [ARCHITECTURE.md](ARCHITECTURE.md) for detailed design decisions.

## Quick Start

### Prerequisites

- Docker **or** Go 1.24+

### Build

```bash
# With Docker (no local Go required)
./dev.ps1 build    # PowerShell (Windows)
./dev.sh build     # Bash (Linux/macOS)

# With local Go
make build
```

### Run

```bash
# Start the daemon
aethel daemon start

# Launch the TUI (auto-starts daemon if needed)
aethel
```

### Key Bindings

| Key | Action |
|---|---|
| `Ctrl+T` | New tab |
| `Ctrl+W` | Close active pane |
| `Alt+H` | Split horizontal |
| `Alt+V` | Split vertical |
| `Tab` / `Shift+Tab` | Navigate panes |
| `F2` | Rename active tab |
| `Alt+F2` | Rename active pane |
| `Alt+C` | Cycle tab color |
| `Alt+PgUp` / `Alt+PgDn` | Scroll page up/down |
| `Ctrl+V` | Paste from clipboard |
| `Ctrl+Q` | Quit |

All keybindings are configurable in `~/.aethel/config.toml` under `[keybindings]`.

## Configuration

Aethel looks for `~/.aethel/config.toml`:

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
```

## Project Structure

```
cmd/
├── aethel/          # TUI client
└── aetheld/         # Background daemon
internal/
├── clipboard/       # Cross-platform clipboard read (Win32 API, pbpaste, xclip)
├── config/          # TOML configuration
├── daemon/          # Session management, message routing
├── ipc/             # Length-prefixed JSON protocol, client/server
├── persist/         # Atomic workspace/buffer persistence (JSON + binary)
├── pty/             # Cross-platform PTY (Unix + Windows)
├── ringbuf/         # Circular byte buffer for PTY output history
├── shellinit/       # Automatic shell integration (OSC 7 injection)
└── tui/             # Bubble Tea model, tabs, panes, layout tree, styles
```

## Development

All commands are available via `dev.sh` (Docker, no local Go) or `make` (local Go):

| Task | Docker (`dev.ps1` / `dev.sh`) | Local Go |
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
| **M2: Persistence** | In Progress | Workspace snapshots, ghost buffer persistence, shell respawn, reboot-proof sessions |
| **M3: Resume Engine** | Planned | Regex scrapers, token extraction, AI session resume |
| **M4: Plugin System** | Planned | TOML plugins, typed panes, hot-reload |
| **M5: Polish** | Planned | JSON transformer, observability, encrypted tokens, OS service integration (`aethel service install` — systemd/launchd/Task Scheduler) |

## License

[MIT](LICENSE) — Copyright (c) 2026 Artjoms Stukans
