# Architecture Decisions

This document records the key architectural decisions for Quil and the reasoning behind them.

## ADR-1: Client-Daemon Architecture

**Decision:** Split Quil into two binaries — `quil` (TUI client) and `quild` (background daemon).

**Context:** A terminal multiplexer needs to outlive any single terminal session. The daemon manages PTY sessions and state, while clients attach/detach freely.

**Consequences:**

- Sessions survive client disconnection — close the terminal, reopen, and reattach
- Multiple clients can observe the same workspace simultaneously
- Daemon auto-starts on first `quil` invocation if not already running
- Adds IPC complexity vs. a single-process design

## ADR-2: Length-Prefixed JSON over IPC

**Decision:** Use a length-prefixed JSON protocol over Unix domain sockets (Linux/macOS) and Named Pipes (Windows).

**Format:**

```
[4 bytes: uint32 big-endian length][JSON payload]
```

**Alternatives considered:**

| Option | Rejected because |
|---|---|
| gRPC | Heavy dependency for local-only IPC. Protobuf adds build complexity. |
| MessagePack | Binary format harder to debug. JSON is human-readable in logs. |
| Raw newline-delimited JSON | Can't handle payloads containing newlines (e.g., PTY output). |

**Consequences:**

- Simple to implement — ~100 lines for the full protocol
- Debuggable — JSON messages can be logged and inspected
- 4-byte length prefix handles arbitrary payload sizes
- Sufficient throughput for local terminal I/O

## ADR-3: Cross-Platform PTY via Build Tags

**Decision:** Use Go build tags to provide platform-specific PTY implementations behind a common `Session` interface.

**Implementation:**

| Platform | Library | Build tag |
|---|---|---|
| Linux, macOS, FreeBSD | `creack/pty/v2` | `//go:build linux \|\| darwin \|\| freebsd` |
| Windows | `charmbracelet/x/conpty` | `//go:build windows` |

**Interface:**

```go
type Session interface {
    Start(cmd string, args ...string) error
    SetEnv(env []string)
    Read(buf []byte) (int, error)
    Write(data []byte) (int, error)
    Resize(rows, cols uint16) error
    Close() error
    Pid() int
}
```

`SetEnv` was added to support shell integration — the daemon passes environment variables (e.g., `ZDOTDIR` for zsh) to the child shell process before starting it.

**Consequences:**

- Single codebase compiles for all platforms
- Each platform uses its native PTY mechanism
- Interface abstraction keeps daemon code platform-agnostic
- ConPTY API differences (e.g., `Spawn()` vs `exec.Command()`) are isolated

## ADR-4: Bubble Tea v2 for TUI

**Decision:** Use Bubble Tea v2 (`charm.land/bubbletea/v2` v2.0.2) with Lipgloss v2 (`charm.land/lipgloss/v2` v2.0.2) for the TUI.

**Context:** Initially built on Bubble Tea v1. Migrated to v2 in M8 for declarative `View()` (returns `tea.View` struct with AltScreen/MouseMode), typed mouse events (`MouseClickMsg`, `MouseMotionMsg`, `MouseReleaseMsg`, `MouseWheelMsg`), `KeyPressMsg` with modifier bitmask, and diff-based rendering.

**Consequences:**

- Elm Architecture (Model-Update-View) provides clean state management
- Declarative view config replaces imperative `tea.EnterAltScreen` commands
- Diff renderer only updates changed cells — requires `tea.ClearScreen` at dialog transitions
- Lipgloss v2 Width/Height includes borders (v1 was additive) — pane/dialog rendering compensates
- `Key.Mod.Contains()` bitmask enables proper Shift/Ctrl/Alt detection for text selection

## ADR-5: Hybrid Storage — JSON + SQLite

**Decision:** Use JSON for configuration and workspace state, SQLite for volatile cached data.

| Layer | Format | What | Why |
|---|---|---|---|
| Configuration | TOML | `config.toml`, plugin definitions | Human-readable, hand-editable, git-friendly |
| Workspace state | JSON | `workspace.json` | Source of truth for layout. Human-inspectable. Atomic writes via temp+rename. |
| Volatile data | SQLite | Ghost buffers, token history, session logs | High-write frequency, queryable. Rebuildable cache — can be deleted safely. |

**Alternatives considered:**

| Option | Rejected because |
|---|---|
| SQLite for everything | Config files should be hand-editable. TOML/JSON is more accessible. |
| JSON for everything | Ghost buffers write frequently. JSON append is not crash-safe for high-frequency writes. |
| BoltDB / bbolt | SQLite has better tooling, broader ecosystem, and handles concurrent reads well. |

## ADR-6: Plugin System via TOML

**Decision:** Pane types are defined as TOML plugin files in `~/.quil/plugins/`. No compiled plugins or scripting engine.

**Plugin schema:**

```toml
[plugin]
name = "ai"
display_name = "AI Assistant"

[scraper]
patterns = ['(?P<SessionID>Conversation ID: [a-f0-9-]+)']

[resume]
command = "claude --resume {{.SessionID}}"
fallback = "claude"

[display]
border_rules = [
  { pattern = "Error", color = "red" },
  { pattern = "Success", color = "green" },
]
```

**Consequences:**

- Users create custom pane types without recompiling
- Hot-reload — daemon watches plugin directory for changes
- No arbitrary code execution — plugins are declarative config
- Scraper patterns use Go regex, resume commands use Go `text/template`
- Ships with 4 built-in plugins: `ai`, `webhook`, `infrastructure`, `build`

## ADR-7: Workspace State Persistence Strategy

**Decision:** Atomic writes with backup for crash safety.

**Process:**

1. Serialize workspace state to JSON
2. Write to `workspace.json.tmp`
3. Rename `workspace.json` to `workspace.json.bak`
4. Rename `workspace.json.tmp` to `workspace.json`

**Triggers:** Structural changes (tab/pane create/delete), configurable interval (default 30s), clean shutdown.

**Consequences:**

- No partial writes — `os.Rename` is atomic on all target platforms
- Previous state always available as `.bak` for manual recovery
- Human-readable format enables debugging with `cat` or `jq`

## ADR-8: Go as the Implementation Language

**Decision:** Go (Golang) for the entire project.

**Rationale:**

- Single static binary per platform — no runtime dependencies
- First-class concurrency (goroutines for PTY I/O, IPC, scrapers)
- Excellent cross-compilation (`GOOS`/`GOARCH`)
- Strong standard library for networking, file I/O, JSON, regex
- Bubble Tea and the Charm ecosystem are Go-native

**Alternatives considered:**

| Language | Rejected because |
|---|---|
| Rust | Steeper learning curve, slower iteration for prototyping |
| Python | Not suitable for terminal multiplexing performance. Deployment complexity. |
| TypeScript (Node) | Poor PTY support on Windows. Runtime dependency. |

## ADR-9: Binary Split Layout Tree

**Decision:** Represent pane layout as a binary tree (`LayoutNode`) instead of a flat array.

**Context:** Flat pane arrays can only express uniform grids. Real terminal workflows need tmux-style mixed splits — e.g., a large editor pane on the left, two stacked terminals on the right.

**Implementation:**

```
Internal Node (Split)          Leaf Node (Pane)
├── SplitDir: H or V           └── *PaneModel
├── Ratio: float (left share)
├── Left: *LayoutNode
└── Right: *LayoutNode
```

- `SplitLeaf(paneID, dir)` splits a leaf into two children
- `RemoveLeaf(paneID)` promotes the sibling
- `FindPaneAt(x, y, ...)` enables mouse-click pane selection
- `SerializeLayout()` / `DeserializeLayout()` persist the tree as JSON in the daemon's `Tab.Layout` field

**Consequences:**

- Supports arbitrarily nested mixed H/V splits
- Mouse clicks resolve to the correct pane via spatial hit-testing
- Layout survives client disconnect — daemon stores serialized JSON, TUI rebuilds tree on reconnect
- Minimum pane dimensions (10 cols, 4 rows) enforced during recursive resize

## ADR-10: Shell Integration via Init Script Injection

**Decision:** Automatically inject OSC 7 working-directory hooks when spawning shells.

**Context:** Terminal emulators track the shell's CWD via OSC 7 escape sequences (`\e]7;file://host/path\e\\`), but most shells don't emit them by default. Requiring manual shell configuration is a poor UX.

**Approach (per shell):**

| Shell | Injection method |
|---|---|
| bash | `--rcfile ~/.quil/shellinit/bash-init.sh` |
| zsh | `ZDOTDIR=~/.quil/shellinit/zsh` (custom `.zshenv` + `.zshrc`) |
| PowerShell | `-NoProfile -File ~/.quil/shellinit/pwsh-init.ps1` |
| fish | No injection needed — emits OSC 7 natively |

Each init script sources the user's original shell config first, then appends the OSC 7 hook. Scripts are embedded in the binary via `//go:embed` and written to `~/.quil/shellinit/` at daemon startup.

**Consequences:**

- Zero-config CWD tracking — pane borders show the live working directory automatically
- User's shell customizations (PS1, aliases, functions) are fully preserved
- PTY `SetEnv()` interface method enables passing environment variables to child processes
- Fish users get CWD tracking with no changes at all

## ADR-11: Ring Buffer for Output History

**Decision:** Use a fixed-capacity circular byte buffer per pane to capture PTY output for replay on client reconnect.

**Context:** When a TUI client disconnects and reconnects, it needs to see previous terminal content. Storing all output is infeasible for long-running sessions. A ring buffer automatically evicts old data while keeping the most recent output.

**Implementation:**

- `internal/ringbuf.RingBuffer` — thread-safe circular buffer
- Capacity: `ghost_buffer.max_lines * 512` bytes (default ~256KB per pane)
- Write path: `daemon.streamPTYOutput()` writes to both the ring buffer and the broadcast channel
- Replay path: `handleAttach()` iterates all panes and sends buffered output to the reconnecting client

**Consequences:**

- Reconnecting clients instantly see recent terminal content
- Memory usage is bounded and predictable
- Old output is silently evicted — no manual cleanup needed
- Basis for future ghost buffer rendering (dimmed historical content)

## ADR-12: Centralized Snapshot Queue

**Decision:** Replace scattered `snapshotDebounced()` calls with a single event-driven snapshot channel processed in the daemon's main event loop.

**Context:** Snapshot triggers were spread across handler functions, each calling `snapshotDebounced()` with its own mutex-based time check. This was fragile — missing a trigger in a new handler meant state loss. Multiple concurrent debounce checks also created TOCTOU races.

**Implementation:**

- `snapshotCh` — buffered channel (capacity 1) for non-blocking snapshot requests
- `requestSnapshot()` sends to the channel; drops if already pending
- `Wait()` event loop receives from `snapshotCh`, starts a 500ms `time.AfterFunc` debounce timer
- When the timer fires, `snapshot()` executes in the main loop (no mutex needed)
- Triggers: create/destroy tab/pane, switch tab, update layout, client disconnect
- 30-second periodic timer as safety net; final `snapshot()` in `Stop()` before PTY close

**Consequences:**

- Single place to audit all snapshot triggers — the event loop in `Wait()`
- Debounce collapses rapid operations (e.g., bulk pane creation) into one snapshot
- No mutex needed — snapshot runs single-threaded in the event loop
- Layout changes now trigger snapshots (was missing before, causing layout loss on daemon kill)

## ADR-13: GhostSnap Restore

**Decision:** Store pure disk-loaded ghost buffer data in a separate `Pane.GhostSnap` field, keeping it isolated from the live `OutputBuf` ring buffer.

**Context:** After daemon restart, `restoreWorkspace()` loads ghost buffers into `OutputBuf`, then `respawnPanes()` starts new shell processes whose init output (including potential ConPTY clear screen sequences) is appended to the same buffer. When `handleAttach()` sends `OutputBuf.Bytes()` as ghost, the new shell output contaminates the historical data — the VT processes history, then a clear screen wipes it.

**Implementation:**

- `Pane.GhostSnap []byte` — set in `restoreWorkspace()` as a copy of the disk-loaded data
- `handleAttach()` prefers `GhostSnap` over `OutputBuf.Bytes()` for ghost replay
- After first client replay, `GhostSnap` is set to `nil` — subsequent reconnects use the full `OutputBuf`
- TUI skips `ResetVT()` for terminal panes on ghost→live transition (non-terminal panes still reset to avoid cursor pollution)

**Consequences:**

- Terminal history survives daemon restart — ghost data is clean, not contaminated by shell init
- Reconnects (without daemon restart) still replay the full live buffer — correct behavior
- Snapshots continue saving `OutputBuf.Bytes()` (ghost + new output) — the combined state is the terminal's current state

## ADR-14: Config Persistence via Atomic Write

**Decision:** Add `config.Save()` for runtime config write-back using the same atomic `.tmp` + rename pattern as workspace persistence.

**Context:** Config was read-only at startup — the Settings dialog warned "changes apply to this session only." The beta disclaimer's "Don't show again" feature requires persisting a config change (`ui.show_disclaimer = false`) across restarts.

**Implementation:**

- `config.Save(path, cfg)` encodes the full `Config` struct to TOML, writes to `.tmp`, renames atomically
- `Model.configChanged` flag tracks whether any persistent config change was made during the session
- On TUI exit, `main.go` checks `ConfigChanged()` and calls `Save()` only if needed
- `MkdirAll` ensures parent directory exists; stale `.tmp` cleaned up on rename failure

**Consequences:**

- Config changes from the disclaimer dialog (and future settings) persist across restarts
- Only writes when something actually changed — no unnecessary disk I/O
- Follows the same crash-safe pattern as `persist/snapshot.go`

## Storage Layout

```
~/.quil/
├── config.toml
├── quil.log               # TUI client log
├── quild.log              # Daemon log
├── quild.sock             # IPC socket (Unix) / Named Pipe (Windows)
├── shellinit/               # Auto-generated shell integration scripts
│   ├── bash-init.sh
│   ├── pwsh-init.ps1
│   └── zsh/
│       ├── .zshenv
│       └── .zshrc
├── workspace.json              # Tab/pane/layout state
├── workspace.json.bak          # Previous snapshot (rollback)
├── buffers/                    # Ghost buffer binary files
│   └── pane-XXXXXXXX.bin
├── window.json                 # Window size/position persistence
├── instances.json              # Saved plugin instances (SSH connections, etc.)
├── plugins/                    # User TOML plugin definitions
│   └── *.toml
├── notes/                      # Pane notes (planned)
│   └── pane-XXXXXXXX.txt
└── secrets/                    # (planned)
    └── tokens.enc
```
