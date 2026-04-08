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
- Every Settings dialog setter (`Snapshot interval`, `Ghost dimmed`, `Ghost buffer lines`, `Mouse scroll lines`, `Page scroll lines`, `Log level`, `Show disclaimer`) flips the flag — earlier versions only flagged the disclaimer field, silently dropping every other edit
- Log-level changes apply on the next launch (no live re-init); the file handle owned by `main.go` is not re-plumbed into the Model
- `MkdirAll` ensures parent directory exists; stale `.tmp` cleaned up on rename failure

**Consequences:**

- Config changes from the disclaimer dialog AND every Settings field persist across restarts
- Only writes when something actually changed — no unnecessary disk I/O
- Follows the same crash-safe pattern as `persist/snapshot.go`

## ADR-15: Pane Setup Dialog (Per-Spawn Plugin Configuration)

**Decision:** Insert an opt-in setup step between plugin selection and split-direction selection in the Ctrl+N flow. Activated by two new TOML flags on `[command]`: `prompts_cwd = true` (renders a directory browser) and `[[command.toggles]]` (renders one checkbox per declared toggle).

**Context:** Claude Code ties session memory and project plans to the working directory, so spawning the pane in `~` instead of the project root silently loses context. There is also a real demand for `--dangerously-skip-permissions` per-pane, but burying it in plugin TOML is the wrong UX — it should be a one-click opt-in at creation time, off by default.

**Implementation:**

- New `dialogCreatePaneSetup` screen rendered between steps 1 (plugin) and 3 (split direction)
- `enterSetupOrSplit(p)` decides the route: if `p.Command.PromptsCWD || len(p.Command.Toggles) > 0` open the dialog, otherwise advance straight to step 3
- Directory browser (`loadBrowseDir`, `loadBrowseDirAndSelect`, `adjustBrowseScroll`) — `os.ReadDir`, sorted, ".." prepended unless at root, follows directory symlinks/Windows junctions via `os.Stat`. Pre-loaded with the active pane's CWD (tracked via OSC 7) or `os.UserHomeDir()` as fallback
- `validateAndNormalizeCWD` — trim whitespace + quotes, expand `~`, `filepath.Abs`, `os.Stat`, `EvalSymlinks` to canonicalise (closes a small TOCTOU window before the daemon spawn)
- `sanitizePastedPath` — strips control bytes from clipboard input so an OSC/CSI payload can't inject terminal escapes into a rendered error message
- Toggle args ride through the existing `CreatePanePayload.InstanceArgs` field — no new IPC surface
- Daemon-side validation: `handleCreatePane` and `handleCreatePaneReq` re-stat the CWD and re-resolve symlinks, defending against IPC clients beyond the TUI (MCP, future tooling)
- `daemon.spawnPane` restore branch was fixed to **append** `ResumeArgs` instead of replacing `args`, so toggle args (e.g. `--dangerously-skip-permissions`) survive a daemon restart alongside `--resume <uuid>`

**Consequences:**

- Claude Code panes pick up the project's `.claude/` context automatically — the most common use of the feature
- Dangerous-permissions mode is one keystroke + one space-bar away, never the default
- The mechanism is fully generic — any future plugin can opt in to either flag without TUI changes
- Existing plugins (terminal, ssh, stripe) are unaffected — they don't set either opt-in
- Toggle state is intentionally NOT persisted per-instance: dangerous flags should be a conscious per-spawn decision, not a saved preference

## ADR-16: Spatial Pane Navigation (Alt+Arrow)

**Decision:** Replace linear pane cycling (`Tab`/`Shift+Tab`) with directional spatial navigation (`Alt+Left`/`Right`/`Up`/`Down`) that picks the closest neighbour in the requested direction. `Tab` and `Shift+Tab` fall through to the PTY.

**Context:** Linear `next_pane`/`prev_pane` is awkward in mixed H/V layouts — the next pane in tree-traversal order rarely matches the next pane on screen. Worse, binding `Tab` globally ate shell tab-completion and Claude Code's mode-cycling key, both of which need to reach the PTY.

**Algorithm:**

1. `CollectRects` walks the layout tree top-down, computing each leaf's `(OX, OY, W, H)` relative to the tab.
2. For each candidate pane (excluding the active one), `directionScore` checks the half-plane and perpendicular overlap:
   - The candidate must lie strictly in the target half-plane (e.g. `cand.OX + cand.W <= active.OX` for `DirLeft`). The strict-vs-loose comparison uses `>`, not `>=`, so adjacent panes that share a border column qualify.
   - The candidate's perpendicular range (`OY..OY+H` for left/right, `OX..OX+W` for up/down) must overlap the active pane's perpendicular range. Zero overlap = rejected — a pane that is strictly above-and-to-the-right is not reachable via "up".
3. Tie-breakers, applied in order:
   1. **Smallest gap** along the direction axis (nearest edge wins).
   2. **Largest perpendicular overlap** (most-aligned wins).
   3. **Smallest perpendicular center distance** (closest-aligned center wins — tmux/vim/iTerm parity, prevents tree-traversal-order ambiguity in symmetric layouts).
4. If no candidate qualifies, navigation is a no-op.

**Implementation:**

- New `Direction` enum (`DirLeft`/`DirRight`/`DirUp`/`DirDown`) and `TabModel.NavigateDirection(dir)` method
- `directionScore` returns `(gap, overlap, perpDist, ok bool)` so the caller has all three sort keys without recomputation
- New keybindings `pane_left`/`pane_right`/`pane_up`/`pane_down` in `[keybindings]` (defaults `alt+left/right/up/down`); legacy `next_pane`/`prev_pane` default to empty (unbound)
- Split shortcuts moved off `Alt+H`/`Alt+V` to `Alt+Shift+H`/`Alt+Shift+V` so `Alt+V` reaches Claude Code's image paste

**Consequences:**

- Pane motion now matches user intuition in arbitrary mixed layouts
- Tab/Shift+Tab work naturally in shells and Claude Code without any per-plugin opt-in
- Disabled in focus mode and on single-pane tabs (no-op, no error)
- Vim users can rebind to `alt+h/l/k/j` in `config.toml`

## ADR-17: Win32 Clipboard Image Paste Proxy

**Decision:** When a paste keystroke fires and the clipboard contains an image but no text, Quil itself reads the image, decodes it, encodes it as PNG, drops it in `~/.quil/paste/`, and types the absolute path into the active pane. This is a workaround for the upstream Claude Code Windows clipboard image bug ([anthropics/claude-code#32791](https://github.com/anthropics/claude-code/issues/32791)).

**Context:** Claude Code on Windows can't read images from the clipboard reliably — the broken read path drops the image silently. Forcing users to manually save screenshots and type paths breaks the agentic flow. Since the AI tools all have file-reading tools, dropping the file and pasting its path is functionally equivalent and works for every tool.

**Implementation:**

- `clipboard.ReadImage()` (`internal/clipboard/clipboard.go`) — platform dispatch interface, returns `(pngBytes []byte, err error)` or the sentinel `clipboard.ErrNoImage`
- `internal/clipboard/image_windows.go` — Win32 read path:
  - `OpenClipboard` → check `CF_DIBV5` first (preserves alpha and color profiles), fall back to `CF_DIB`
  - `GlobalLock` + `GlobalSize`, copy out before `CloseClipboard` (defensive — Win32 invalidates the locked pointer immediately on close)
  - 64 MB clipboard byte cap (`maxClipboardImageBytes`) before any allocation
  - Hand off to `decodeDIB` for parsing, then `image/png` for encoding
- `internal/clipboard/dib.go` — platform-agnostic DIB parser (testable on Linux CI):
  - Supports BITMAPINFOHEADER (40-byte), BITMAPV4HEADER (108), BITMAPV5HEADER (124)
  - 24bpp BI_RGB and 32bpp BI_RGB / BI_BITFIELDS with default BGRA masks
  - Handles bottom-up (positive height) and top-down (negative height) row order
  - **Zero-alpha promotion** — some apps leave the 32bpp alpha channel as 0; the decoder detects "all alpha = 0" and promotes to 0xFF, otherwise downstream tools see a fully-transparent image
  - Per-axis cap (`maxDIBDimension = 16384`) + uint64 stride math defends against crafted payloads that slip under the 64 MB byte cap but would otherwise allocate gigabytes during decode
- `internal/clipboard/image_unix.go` — stub returning `ErrNoImage` (macOS / Linux paste image support not implemented yet)
- `tryPasteClipboardImage` (in `internal/tui/model.go`) — falls through from text paste when text is empty:
  - `os.MkdirAll(config.PasteDir(), 0o700)` — owner-only directory
  - Filename: `quil-paste-<timestamp>-<8-byte hex from crypto/rand>.png` — unguessable, defeats enumeration on multi-user Unix boxes
  - `os.WriteFile(abs, png, 0o600)` — owner-only file
  - Types the absolute path into the PTY via the same bracketed-paste path as text
- Paste keys: `Ctrl+V` (default), `Ctrl+Alt+V` and `F8` (hardcoded aliases). `F8` is the recommended Windows trigger because **Windows Terminal captures `Ctrl+V` for its own paste action** and never delivers the key event to the running TUI

**Consequences:**

- Claude Code can read pasted screenshots on Windows even though its native clipboard read is broken
- The same proxy works for any AI tool with file-reading tools (Cursor, Aider, etc.) without per-tool wiring
- Paste files inherit the tight `~/.quil/` permission posture — no co-tenant on Unix can enumerate or read them
- Linux/macOS get the file API but currently always return `ErrNoImage` — the platform reader is a future addition

## ADR-18: Leveled Logger via slog Bridge

**Decision:** Wrap Go's stdlib `log/slog` (Go 1.21+) in a tiny `internal/logger` package that exposes `Debug/Info/Warn/Error` helpers AND bridges the existing 152 stdlib `log.Printf` call sites at info level so both old and new code respect a single configurable filter.

**Context:** Quil's diagnostic logging started as `log.Printf` calls scattered across daemon/TUI/IPC. Useful, but unfilterable — every paste keystroke and per-key handler trace was always on, regardless of whether the user wanted that level of detail. Migrating 152 sites to a new logger interface in one PR is impractical, and removing the diagnostic value of the existing prints is not acceptable.

**Implementation:**

- `internal/logger/logger.go` — single file, no dependencies beyond stdlib
- `Init(level string, w io.Writer)` builds a `slog.NewTextHandler` at the requested level, sets it as `slog.Default()`, AND calls `log.SetOutput(slog.NewLogLogger(handler, slog.LevelInfo).Writer())` to route stdlib `log.Printf` through the same handler at info level. All three mutations happen under one `sync.Mutex` span so a concurrent reader can't see a half-initialised state.
- `Debug`/`Info`/`Warn`/`Error` helpers share a `logAt` body that pre-checks `slog.Enabled(ctx, level)` so the (potentially expensive) `fmt.Sprintf` is skipped when the configured level filters the call out — important for the per-keystroke `Debug` calls in the TUI hot path.
- `ParseLevel` accepts `"debug" | "info" | "warn"/"warning" | "error"/"err"` (case-insensitive), defaults unknowns to info.
- Wired from both `cmd/quil/main.go` and `cmd/quild/main.go` after the log file is opened. Both entry points call `Init` BEFORE the first meaningful `log.Printf`.

**Consequences:**

- Flip `[logging] level = "debug"` in `config.toml` to see clipboard pipeline traces, per-key handler decisions, and Win32 image read step-by-step. Default `info` keeps the existing log volume.
- Existing 152 `log.Printf` sites work unchanged — they get a single-level filter for free.
- New code can be explicit about levels (`logger.Debug(...)`) without dragging in any third-party dependency.
- `LoggingConfig.MaxSizeMB` / `MaxFiles` are reserved fields documented as "not yet honored" — log rotation is planned via lumberjack in a future PR; the fields exist now to avoid breaking users' configs when rotation lands.

## ADR-19: Read-Only TextEditor for the F1 Log Viewer

**Decision:** Add a `ReadOnly bool` field to the existing `TextEditor` and reuse it for F1 → "View client/daemon/MCP log". Every mutation path (typing, paste, cut, save, enter, backspace, delete, tab, multi-line insert) is gated by the flag; cursor movement and clipboard COPY remain enabled.

**Context:** The F1 menu needs a way to inspect logs without leaving the TUI. Spawning a separate viewer (`less`, `tail`) would break the single-process model. The existing `TextEditor` has all the navigation, scrolling, and selection plumbing already — gating mutation is much smaller than building a viewer from scratch.

**Implementation:**

- `TextEditor.ReadOnly bool` — checked at the top of every mutation case in `HandleKey`. The public `InsertMultiLine` is also gated so `Ctrl+V` paste cannot bypass the check.
- `TextEditor.PageSize int` — added at the same time so `Alt+Up`/`Alt+Down` can jump by N lines (default 40 via `editorDefaultPageSize`, configurable via `[ui] log_viewer_page_lines`). Navigation works in both read-only and editable modes.
- `dialogLogViewer` reuses `m.tomlEditor` (the field name predates this use; renaming was deferred). Cursor starts at the bottom so the freshest log lines are in view.
- `readLogTail` reads up to `maxLogViewBytes = 256 * 1024` from the END of a log file:
  - `os.Lstat` first to reject symlinks, devices, named pipes — defends against link swaps inside `~/.quil/` that could redirect the read to an arbitrary file (e.g., `~/.ssh/id_rsa`)
  - Re-stat through the open handle defeats a TOCTOU swap between Lstat and Open
  - Seeks to `(size - maxBytes)` (with `io.SeekStart`), reads, then drops everything before the first newline so the result starts at a clean line boundary
  - Prepends `[... older lines truncated ...]` marker
- `openMCPLogsViewer` aggregates per-pane MCP interaction logs (`~/.quil/mcp-logs/*.log`) into one buffer with file-name headers, most-recently-modified first, capping each file to a fair share of the total budget

**Consequences:**

- F1 → log viewers behave like a real read-only `less` overlay without spawning anything
- The same `ReadOnly` flag is now available for any other "look but don't touch" use case (future: read-only TOML preview, read-only config dump)
- Symlink-rejecting reads make the log viewer safe to use against attacker-writable directories under the same user account

## Storage Layout

```
~/.quil/                           (or ./.quil/ in dev mode — see .claude/rules/dev-environment.md)
├── config.toml                    # User configuration (TOML)
├── quil.log                       # TUI client log (slog text format)
├── quild.log                      # Daemon log
├── quild.sock                     # IPC socket (Unix) / Named Pipe (Windows)
├── quild.pid                      # Daemon PID file
├── shellinit/                     # Auto-generated shell integration scripts
│   ├── bash-init.sh
│   ├── pwsh-init.ps1
│   └── zsh/
│       ├── .zshenv
│       └── .zshrc
├── workspace.json                 # Tab/pane/layout state
├── workspace.json.bak             # Previous snapshot (rollback)
├── buffers/                       # Ghost buffer binary files
│   └── pane-XXXXXXXX.bin
├── window.json                    # Window size/position persistence
├── instances.json                 # Saved plugin instances (SSH connections, etc.)
├── plugins/                       # User TOML plugin definitions
│   └── *.toml
├── notes/                         # Pane notes (M7) — one file per pane
│   └── pane-XXXXXXXX.md
├── paste/                         # Clipboard image proxy output (ADR-17)
│   └── quil-paste-<ts>-<rand>.png
├── mcp-logs/                      # Per-pane MCP interaction logs (M10)
│   └── pane-XXXXXXXX.log
└── secrets/                       # (planned)
    └── tokens.enc
```

## Project Rules

The project ships rule files under `.claude/rules/` that document non-obvious constraints for both human contributors and AI coding assistants:

- **`.claude/rules/dev-environment.md`** — production isolation. The Quil author runs Quil itself in production from `~/.quil/`; any operation that touches that directory or the running daemon during development is destructive. All development work uses dev mode (`./quil --dev`, data in project-root `.quil/`). The `kill-daemon`/`reset-daemon` helper scripts target production paths and are off-limits during development.
