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
- On Windows the inbox ConPTY host (`conhost.exe`) on Windows 10 mis-renders some TUIs; Quil bundles a newer OpenConsole to host panes there — see [ADR-25](#adr-25-bundled-openconsole-conpty-host-on-windows-10)

## ADR-4: Bubble Tea v2 for TUI

**Decision:** Use Bubble Tea v2 (`charm.land/bubbletea/v2` v2.0.2) with Lipgloss v2 (`charm.land/lipgloss/v2` v2.0.2) for the TUI.

**Context:** Initially built on Bubble Tea v1. Migrated to v2 in M8 for declarative `View()` (returns `tea.View` struct with AltScreen/MouseMode), typed mouse events (`MouseClickMsg`, `MouseMotionMsg`, `MouseReleaseMsg`, `MouseWheelMsg`), `KeyPressMsg` with modifier bitmask, and diff-based rendering.

**Consequences:**

- Elm Architecture (Model-Update-View) provides clean state management
- Declarative view config replaces imperative `tea.EnterAltScreen` commands
- Diff renderer only updates changed cells — requires `tea.ClearScreen` at dialog transitions
- Lipgloss v2 Width/Height includes borders (v1 was additive) — pane/dialog rendering compensates
- `Key.Mod.Contains()` bitmask enables proper Shift/Ctrl/Alt detection for text selection

## ADR-5: Storage — TOML config, JSON state, binary buffers (no SQLite)

**Decision:** Use TOML for hand-edited config, JSON for workspace state, and per-pane binary files for ghost buffers. No SQLite anywhere.

| Layer | Format | What | Why |
|---|---|---|---|
| Configuration | TOML | `config.toml`, plugin definitions, instances | Human-readable, hand-editable, git-friendly |
| Workspace state | JSON | `workspace.json`, `window.json` | Source of truth for layout. Human-inspectable. Atomic writes via temp+rename + `.bak` rollback |
| Pane output history | Binary | `buffers/<pane-id>.bin` | Ring-buffered PTY bytes; one file per pane; cheap append, full-file rewrite on snapshot |
| Pane notes | Plain text | `notes/<pane-id>.md` | One file per pane, atomic temp+rename, readable with `cat` |
| Per-pane Claude session id | Plain text | `sessions/<pane-id>.id` | Single uuid per file, written by the embedded SessionStart hook, atomic rename, readable with `cat` |
| Per-pane MCP interaction logs | Plain text | `mcp-logs/<pane-id>.log` | Append-only log lines; redaction applied at write time |

**Earlier drafts considered SQLite for ghost buffers, token history, and session logs** — that direction was abandoned because (a) the per-pane binary file approach is simpler to reason about, (b) crash-safety via atomic rename is more straightforward than file-locked SQLite from multiple goroutines, and (c) zero additional CGo dependency for users on platforms where the SQLite driver doesn't ship pre-built. **No file under `~/.quil/` is a SQLite database.**

**Alternatives considered:**

| Option | Rejected because |
|---|---|
| SQLite for everything | Adds a CGo / C-driver dependency for what is fundamentally append-only per-pane streams. The tooling advantage (`sqlite3` REPL) doesn't outweigh the build complexity |
| JSON for ghost buffers | Frequent append writes; JSON is not crash-safe under high-frequency append, and re-encoding the full buffer per byte is wasteful |
| BoltDB / bbolt | Same CGo and packaging questions as SQLite, with worse tooling |
| One giant binary file for all panes | Whole-file rewrites on per-pane changes don't scale; per-pane files let snapshots be parallelised and let removal of a pane delete a single file |

## ADR-6: Plugin System via TOML

**Decision:** Pane types are defined as TOML plugin files in `~/.quil/plugins/`. No compiled plugins or scripting engine.

**Plugin schema:** six tables, all optional except `[plugin]` and `[command]`.
The authoritative list of fields is the decode struct in
`internal/plugin/registry.go`; [plugin-reference.md](plugin-reference.md)
documents every one of them. A representative plugin:

```toml
[plugin]
name = "claude-code"
display_name = "Claude Code"
category = "ai"                  # terminal | ai | remote | tools
schema_version = 11              # bumped to raise the migration dialog

[command]
cmd = "claude"
detect = "claude --version"      # decides whether the plugin is offered
prompts_cwd = true               # ask for a working directory at spawn
record_history = true            # capture submitted prompts (Alt+Shift+I)
sessions = "claude"              # offer a resume picker in the setup dialog

[[command.toggles]]              # rendered as checkboxes / radio buttons
name = "dangerously_skip_permissions"
label = "Dangerously skip permissions (no confirmations)"
args_when_on = ["--dangerously-skip-permissions"]
group = "permission_mode"        # same group = mutually exclusive

[persistence]
strategy = "preassign_id"        # cwd_only | rerun | preassign_id | session_scrape | none
start_args = ["--session-id", "{session_id}"]
resume_args = ["--continue"]
ghost_buffer = true
redraw_key = "\f"                # stdin bytes that make this program repaint

[[error_handlers]]               # regex over PTY output → help dialog
pattern = '(?i)ANTHROPIC_API_KEY.*not set'
title = "API Key Missing"
message = "Set ANTHROPIC_API_KEY in your environment or run 'claude auth'."
action = "dialog"

[[idle_handlers]]                # regex over the last lines when a pane goes quiet
pattern = '(?i)waiting for (confirmation|input|approval|permission)'
title = "Needs your approval"
severity = "warning"

[display]
wide_canvas = true               # PTY stays window-sized; small panes show a preview
```

`[[instances]]`, `[[command.form_fields]]` and `[[notification_handlers]]` round
out the schema — saved presets, a spawn-time form, and extra notification
patterns respectively.

**Consequences:**

- Users create custom pane types without recompiling
- Hot-reload on save — the registry reloads from `~/.quil/plugins/` and prunes
  entries whose file was deleted
- No arbitrary code execution — plugins are declarative config
- Patterns are Go regex; `{session_id}` and `{{.Field}}` are the only
  substitutions
- Ships with **11 built-in plugins** — two compiled into the binary (`terminal`,
  `terminal-wide`) and nine embedded TOML defaults written to
  `~/.quil/plugins/` on first run (`claude-code`, `opencode`, `codex`,
  `lazygit`, `hunk`, `k9s`, `lazysql`, `ssh`, `stripe`). A user's copy of a file
  overrides the embedded default; `schema_version` is what raises the migration
  dialog when the shipped definition moves ahead of the copy on disk
- Availability is per machine. Each daemon reports what its own host can run, so
  a remote project greys out what that host lacks rather than what your laptop
  lacks

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
- `LoggingConfig.MaxSizeMB` / `MaxFiles` now drive log rotation natively via `internal/logger/rotate.go` (`RotatingWriter`). When the active `quild.log` / `quil.log` would exceed `MaxSizeMB` (default 5 MB) it is rotated to a timestamped archive (`stem-YYYYMMDD-HHMMSS.log`); the newest `MaxFiles` (default 10) archives are kept and older ones are pruned by modification time. No external dependency.

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

## ADR-20: Client/Daemon Version Handshake (v1.8.0)

**Decision:** The TUI handshakes with the running daemon over IPC before attaching, and self-heals when versions drift. Older daemon → prompt the user, gracefully stop it, auto-spawn the matching daemon next to the TUI binary. Newer daemon than client → refuse to attach and point at the releases page. Dev/debug builds skip the check.

**Context:** Quil ships two binaries (`quil` + `quild`) that share a single IPC protocol. Before this ADR, an upgrade to a new IPC schema required the user to manually `kill quild`, replace both binaries, and restart — easy to get wrong, and hard to debug because a half-upgraded session would surface as cryptic "unknown message type" errors. The dual-binary nature is otherwise invisible to users (the TUI auto-starts the daemon on first invocation), so the upgrade dance broke that abstraction.

**Implementation:**

- New IPC pair `MsgVersionReq`/`MsgVersionResp` added to the protocol — backward compatible because pre-handshake daemons return `unknown message` and the client treats that as "older daemon"
- New shared `internal/version/` package with proper semver comparison so `1.10.0 > 1.9.0` (the previous lexical-string comparison had the opposite ordering — a real breakage waiting to land at v1.10)
- Auto-spawn logic finds `quild` next to the TUI executable (`os.Executable()`+`filepath.Dir()`), falls back to PATH; skips this with a warning if neither resolves so users on weird path setups can still run their existing daemon
- Empty version string short-circuits the comparison so unstamped local builds (`go run ./cmd/quil`) and `quil-dev`/`quil-debug` variants don't trigger the prompt during development
- The graceful-stop path uses an existing `MsgShutdown` channel (added in ADR-7's snapshot redesign), so the daemon flushes a final snapshot and writes its PID file removal before exiting

**Consequences:**

- "Drop in the new tarball, run `quil`" now Just Works for both `quil` and `quild` — single-step upgrade
- Newer-daemon-than-client refuses to attach instead of corrupting the user's session with mismatched message ordinals
- The shared `internal/version/` package is now the canonical place for semver math (used by GoReleaser ldflag injection too)
- Any future IPC-protocol change simply bumps the version; the handshake catches mismatches before either side reads a malformed payload

## ADR-21: Memory Reporting (v1.9.0–v1.9.1)

> **Surfacing superseded in v1.63.0.** The `F1` → Memory dialog was replaced by
> `F1` → Processes, which shows the real process tree under each pane — the
> shell or agent Quil started plus everything it went on to spawn — with memory
> *and* CPU per row, a `K` key to stop a process below the pane's own shell, and
> a section listing Quil's own processes. The collector, the IPC pair, the
> status-bar segment and both MCP tools below are unchanged; only the dialog
> that renders them is different.

**Decision:** A daemon-side 5-second collector (`internal/memreport/`) snapshots per-pane Go-heap (output ring buffer + ghost snapshot + plugin state) and PTY child resident memory; results are surfaced via a `mem <n>` segment in the status bar, an F1 → Memory tree dialog, and two MCP tools.

**Context:** Quil keeps long-running PTY children and per-pane ring buffers — both are silent leak risks. Without an in-app accounting view, users had to attach a debugger or read OS-level process listings to spot a misbehaving plugin. The same data is also valuable to AI agents that drive Quil over MCP (e.g., "the assistant pane's RSS jumped 800 MB after that last run — it's leaking").

**Implementation:**

- `internal/memreport/` package — collector goroutine, 5 s ticker, daemon-owned. Per-pane breakdown: `OutputBufBytes` (ring buffer cap), `GhostSnapBytes` (frozen disk-loaded copy), `PluginStateBytes` (rough JSON-encoded estimate), `NotesBytes` (notes editor approx), and `PTYRSSBytes` (resident memory of the spawned child)
- Cross-platform RSS via the smallest possible per-platform shim — no CGo:

| Platform | Implementation |
|---|---|
| Linux | Read `/proc/<pid>/status`, parse `VmRSS:` line |
| Darwin | Single batched `ps -o pid=,rss= -p <pid1>,<pid2>,...` per tick |
| Windows | `GetProcessMemoryInfo` via `golang.org/x/sys/windows` |
| Other | No-op stub returning 0 |

- New IPC pair `MsgMemoryReportReq`/`MsgMemoryReportResp` so both the TUI and MCP bridge consume the same daemon-computed snapshot — single source of truth, no double-counting
- TUI side: `dialogMemory` (F1 → Memory) renders a tab/pane tree with expand/collapse; status bar gains a `mem <n>` segment polled every 5 s; the notes editor gets `ApproxBytes()` for per-pane attribution
- Two MCP tools: `get_memory_report` (per-tab totals + grand total) and `get_pane_memory` (single pane detail)
- VT-emulator grid memory **explicitly deferred** — `charmbracelet/x/vt` does not expose a stable accessor for cell-grid bytes; opening that surface would require either upstream API changes or a fork. The estimate is documented as missing in the dialog footer

**Consequences:**

- Leaks become visible in the live UI without dropping out of Quil — most often catches plugin-state objects that aren't released after pane destruction
- AI agents can self-monitor over MCP — useful for long-running automated sessions
- The daemon is the single source of truth for memory numbers; multiple TUIs attached to the same daemon all see identical figures
- VT grid memory remains an accepted blind spot; will be revisited when upstream provides an accessor

## ADR-22: VT-Emulator Reply Drain Goroutine + Stuck-Update Watchdog (v1.9.1)

**Decision:** Run a per-pane goroutine that reads and discards replies from `charmbracelet/x/vt`'s emulator (`SafeEmulator`) into `io.Discard`, and add a process-lifetime watchdog that dumps full-process stack traces if any Bubble Tea `Update` call exceeds 10 s.

**Context:** `Emulator.handleRequestMode` writes DECRQM mode-query replies to an unbuffered `io.Pipe`. Quil uses the emulator as a renderer only — ConPTY (Windows) and the real PTY (Unix) are the actual terminal — so nobody was reading from that pipe. When claude-code probed the terminal mode, `SafeEmulator.Write` blocked forever inside `tea.Update` under its own mutex. Result: a single keystroke wedged the entire TUI, requiring a hard kill. The bug is generic — any tool that sends a mode/cursor/window query (most TUI applications) was a potential trigger.

**Implementation:**

- Per-pane goroutine in `internal/tui/pane.go` started after `vt.NewEmulator` returns:
  - `io.Copy(io.Discard, em)` — blocks on `em.Read()` until `em.Close()` returns `io.EOF`
  - One goroutine per pane; teardown is wired into both `ResetVT()` and pane destruction so no goroutine leaks across VT resets or pane closes
- `internal/tui/watchdog.go` — process-lifetime singleton:
  - `sync.Mutex` + `time.Time updateStartedAt` set/cleared by `applyWorkspaceState` and the `WorkspaceStateMsg` handler
  - 2 s ticker; if `updateStartedAt` has been non-zero for ≥ 10 s and the start-time hasn't already triggered a dump, write `runtime.Stack(buf, true)` to the leveled logger at error level
  - Memoised per start-ns so one wedge produces exactly one dump (avoids log spam if the TUI stays wedged)
  - `sync.Pool` reuses the 1 MiB stack buffer
- Eight new `apply: ...` breadcrumb log lines bracket each step of `applyWorkspaceState` and the `WorkspaceStateMsg` handler so the next wedge pinpoints the line that hung to within one statement
- Seven white-box tests in `watchdog_test.go` cover the logic via injected clock/stack/logger so the assertion targets are deterministic

**Consequences:**

- The originally-reported wedge (claude-code on a fresh pane) is fixed
- Future wedges in the Update path are auto-diagnosed — the next user report comes with a stack trace already in the log
- The drain goroutine is allowed to leak the *bytes* it discards (we throw the replies away — Quil has no need for them since ConPTY/PTY is the real terminal); the goroutine itself terminates cleanly via `Close`
- The watchdog never preempts work, only observes it — there is no risk of false-positive cancellation

## ADR-23: Claude Code SessionStart Hook (v1.9.2)

**Decision:** Track Claude Code session-id rotation by registering a `SessionStart` hook via `claude --settings '<inline JSON>'` at every spawn. The hook runs an embedded shell/PowerShell script that writes the live `session_id` into `$QUIL_HOME/sessions/<paneID>.id` atomically. On daemon restore, the resume strategy prefers the hook-recorded id over the original preassigned id.

**Context:** Claude Code's `/clear`, `/resume`, and conversation compaction all rotate the session id to a new jsonl file. ADR-3's `preassign_id` strategy generates a UUID at pane creation and resumes with `--resume <id>` after restart — but that id is now stale. The daemon kept resuming the preassigned jsonl after a restart, silently restoring the pre-rotation conversation and discarding the user's post-rotation work. Critical correctness bug, hard to detect because the resume *succeeded* — just into the wrong session.

**Constraints that shaped the design:**

- Must not modify `~/.claude/settings.json` — it belongs to the user, may be in source control, and is shared across all Claude tools (not just Quil)
- Must not require a one-time setup step — Quil should "just work" the first time the user creates a claude-code pane
- Must survive both daemon restarts and Claude itself restarting between sessions
- The hook runs from Claude's own context, not Quil's — so it has to communicate back via the filesystem, not over IPC

**Update (2026-06):** the embedded `sh`/`ps1` hook scripts were replaced by a native `quild claude-hook` subcommand (`internal/claudehook/runhook.go` + `cmd/quild`). The daemon registers `"<quild>" claude-hook` as the hook command (via `os.Executable`), and the subcommand reads the hook JSON on stdin and writes the session file / spool line natively. This removed the per-event shell cold-start latency (Windows PowerShell 5.1 was ~1–4 s) and the hand-rolled JSON escaping (now `encoding/json`), which had caused a UTF-8 BOM/codepage bug class. `EnsureScripts`/`ScriptPath`/`ValidateQuilDir` no longer exist; the script-existence guard became an `os.Executable` resolution. The original script-based design below is retained for historical context.

**Update (2026-08):** the `claudeSessionExistsFn` probe is no longer a GATE on the resume, and the hook file now records where the transcript actually is. Probing by the pane's CWD was never sound: Claude keys a transcript's project directory off the session's OWN working directory, so an agent that moves into a git worktree moves the transcript out from under the probe. The failed lookup then fell through to `--continue` — Claude's most-recent-session-in-CWD lookup — which attached the pane to whichever sibling had respawned a moment earlier, and in production put three panes on one transcript. The hook now writes the transcript path alongside the id, `refreshPluginStateFromHooks` persists the pair into `workspace.json`, and `claudeResumeCandidates`/`pickResumeCandidate` resume a known id whether or not it could be located (a rejected id is a visible error; a wrong session is silent data loss), with verification used only to choose between candidates. Restore also gained the occupancy guard the create path already had, so one transcript is never handed to two panes. `--continue` now survives only for a pane with no recorded session at all.

**Implementation (original, script-based — superseded; see Update above):**

- New `internal/claudehook/` package with embedded scripts in `scripts/` (sh + ps1) — `//go:embed` makes them part of the binary so there is nothing to install separately
- `claudehook.EnsureScripts()` writes the scripts to `$QUIL_HOME/claudehook/` atomically (`os.CreateTemp` + `os.Rename`) at daemon startup. Writes are owner-only (`0o700`/`0o600`)
- Each spawn passes `--settings '<inline JSON>'` to Claude with a `SessionStart` hook pointing at the on-disk script. Inline JSON, not a config file, so Quil's wiring is fully scoped to that one process invocation
- Each spawn passes `QUIL_PANE_ID=<paneID>` in the PTY env. The hook script reads the variable and writes the captured session id to `$QUIL_HOME/sessions/<QUIL_PANE_ID>.id`
- The hook script reads Claude's stdin JSON, extracts `session_id`, validates it against a uuid regex (`^[0-9a-f-]{36}$`), and atomically rewrites the file. Validation failures are logged to `$QUIL_HOME/claudehook/hook.log` so post-mortem debugging is possible without a second tool
- `daemon.resumeTemplateFor` calls `claudehook.ReadPersistedSessionID(paneID)` first, falls back to the preassigned id from `PluginState["session_id"]` if the hook hasn't written anything yet (e.g., pane closed before any `SessionStart` event fired). The existing on-disk `claudeSessionExistsFn` probe still gates the resume so a deleted jsonl file falls back to `--continue`
- Hardening:
  - `ValidateQuilDir` rejects shell-unsafe paths before hook install (would break the script's `cd "$QUIL_HOME"` line otherwise)
  - `ReadPersistedSessionID` rejects pane ids containing path separators (defends against a malicious pane id injection through a future IPC bug); read is capped at 256 bytes
  - Missing-script detection at spawn time (`claudeHookSpawnPrep`) — if a user wiped `$QUIL_HOME/claudehook/`, the spawn falls back to the pre-feature behaviour rather than registering a dead hook with stale paths
  - Both `readHookSessionIDFn` and `claudeSessionExistsFn` are package-level function vars so `spawn_args_test.go` swaps them out and never touches real `~/.claude/` or `$QUIL_HOME/sessions/`

**Consequences:**

- Claude Code session rotation is tracked transparently — `/clear` creates a new session, daemon restart resumes that new session, the user's post-rotation conversation is preserved
- Multi-pane Claude in the same project keeps each pane on its own session — the per-pane file is the disambiguator, even though all panes share the same project directory under `~/.claude/projects/<escaped-cwd>/`
- The hook mechanism is reusable — any future Claude lifecycle event (`SessionEnd`, `BeforeMessage`) can be wired the same way
- Wiring is fully self-contained in `internal/claudehook/`; the daemon and TUI know the package only as a small API surface (`EnsureScripts`, `ReadPersistedSessionID`, `claudeHookSpawnPrep`)

## ADR-24: TextEditor Soft-Wrap via Visual-Row Layout

**Decision:** Add a `SoftWrap bool` flag to `TextEditor` and route rendering, scrolling, and cursor navigation through a new `visualLayout(contentW) []visualRow` helper when the flag is set. Only `NotesEditor` opts in — the F1 log viewer and the TOML plugin editor keep their legacy hard-truncation behaviour with the trailing `~` marker.

**Context:** The pane-notes editor (M7) is rendered into a side-panel that is only ~40% of window width (`notesPanelWidthNumerator = 2/5`, minimum 30 cols). With the legacy editor truncating each logical line at the panel edge with `~`, every normal prose paragraph disappeared off the right. Word-wrap is a UX expectation users brought from every other text editor; absence was a surprising regression on a multi-paragraph note.

**Why character-wrap (not word-wrap):**

The simplest implementation that delivers the user expectation. Word-wrap adds a second layer of locale-sensitive break rules (CJK, mixed-script notes, hyphenation) on top of an editor that is otherwise rune-pure. The default in most code editors with "soft wrap on" is character wrap. Revisitable later as a config knob if users ask.

**Implementation:**

- New `visualRow` struct: `{ Logical, Start, End int }` — a slice `[Start, End)` of runes within logical line `Logical`
- `visualLayout(contentW) []visualRow` — emits exactly one visual row per logical line when `SoftWrap=false` (so callers do not branch); otherwise splits each logical line at every `contentW`-th rune. Empty lines still produce one zero-width visual row
- `cursorVisualRow(layout)` and `visualToLogical(layout, vrow, vcol)` are the inverse operations. Cursor on a wrap boundary attributes to the *continuation* row, except at end-of-logical-line where it stays on the last visual row — matches vim/most editors
- `ScrollTop` is reinterpreted as a visual-row index when `SoftWrap=true`. Existing surfaces are unaffected because visual-row count equals logical-row count when wrap is off
- Cursor Up/Down (`verticalMove`) walks visual rows with column preservation; Home/End snap to the visual row's `Start`/`End`. Paragraph jumps (`Ctrl+Up`/`Down`) and `PageSize` jumps (`Alt+Up`/`Down`) deliberately stay logical — long-distance jumps want logical semantics
- Selection (`shift+arrow`) stays logical; the per-visual-row render intersects `Sel.ColRange(logical, runeLen)` with each visual row's `[Start, End)` so highlight remains contiguous across wrap boundaries
- `notesEditorPosAt` (model.go) translates mouse-click `(vrow, vcol)` to logical `(row, col)` via `visualToLogical` when wrap is on
- `Render()` is split into `Render` (drives the loop) + `renderVisualRow` (one row's slicing + selection / cursor / plain dispatch). Wrapped continuation rows render with a blank gutter (no line number)

**Why an opt-in flag and not always-on:**

The TOML plugin editor renders at a fixed 70-col width inside a centred dialog — no wrap is correct there because the user is editing source code. The F1 log viewer benefits from `~`-truncation as a visual cue that a log line is being clipped (Alt+Up/Down jump 40 logical lines, not visual lines). Both surfaces explicitly want the legacy behaviour.

**Consequences:**

- The notes editor renders prose as users expect from any other editor; no `~` ever appears in the notes panel
- The shared `TextEditor` API picks up exactly one new field; non-notes callers are byte-identical with the pre-soft-wrap behaviour
- Soft-wrap is a generic capability now — if a future surface (a future read-only text dialog?) wants wrapping, the flag is ready
- Fixed an unrelated pre-existing render bug in `renderLineWithSelection` exposed by the new path: cursor at end-of-line past a shorter selection used to be invisible because the padding math reserved a cell but never emitted a reverse-video glyph

## ADR-25: Bundled OpenConsole ConPTY Host on Windows 10

**Context:** Claude Code's incremental input renderer drew an extra space after the first character on every prompt after the first ("Hello" → "H ello") when run in a Quil pane on Windows 10 — but not on macOS, not in Windows Terminal, and not on Windows 11. Root cause (traced 2026-06-18): the Windows 10 **inbox console host** (`conhost.exe`, used by `kernel32.CreatePseudoConsole`) re-serializes the child's incremental screen updates incorrectly. Windows Terminal escapes this by shipping its own newer **OpenConsole.exe**; macOS uses a real PTY (no re-serialization); Windows 11's inbox host is the modern Terminal-derived code and is unaffected.

**Decision:** On Windows, host pane PTYs through a **bundled** ConPTY (`conpty.dll` + `OpenConsole.exe` from Microsoft's MIT-licensed `Microsoft.Windows.Console.ConPTY` redistributable) instead of the inbox `CreatePseudoConsole` — on **Windows 10 / older only**. Windows 11+ keeps using the inbox host.

**Implementation:**

- `internal/pty/winconpty/` — `charmbracelet/x/conpty` vendored (MIT), with the three pseudoconsole syscalls routed through the bundled `conpty.dll` exports (`Conpty{Create,Resize,Close}PseudoConsole`) loaded via `LoadLibrary` at an absolute path.
- The two binaries are `go:embed`-ed into the Windows build and extracted at daemon startup to `QuilDir()/conpty/<version>/` (`embed_windows.go`), SHA256-verified against the embedded bytes (self-heals corrupt/partial files), with symlink rejection on the extraction targets.
- `prepare_windows.go` gates extraction/use on `RtlGetVersion().BuildNumber < 22000` (Windows 11 = build 22000); `session_windows.go` prefers the bundled backend and falls back to the inbox ConPTY when the dll is absent or fails to load.
- Binaries are gitignored and fetched at build time by `scripts/fetch-conpty.sh` (SHA256-pinned against a poisoned package/CDN), wired into `dev.sh build`/`cross` and the `.goreleaser.yml` before-hooks.

**Consequences:**

- Windows 10 renders modern TUIs (Claude Code, etc.) correctly, matching Windows Terminal and macOS.
- The Windows binary grows ~1.2 MB (the embedded host); Windows 11 carries but never extracts it — one shared `windows/amd64` artifact, runtime-gated rather than two downloads.
- A third-party MIT binary (Microsoft OpenConsole) ships inside Quil — attributed in [`THIRD_PARTY_LICENSES.md`](https://github.com/artyomsv/quil/blob/master/THIRD_PARTY_LICENSES.md).
- Fail-safe: a missing or unloadable bundle degrades automatically to the inbox host.

## ADR-26: SSH stdio Transport for Remote Attach (v1.44.0–v1.46.0)

**Decision:** A client reaches a daemon on another machine by running
`ssh -T <dest> "quil --stdio"` and speaking the ordinary IPC protocol over that
child's stdin/stdout, adapted to `net.Conn`. No port is opened on the remote, and
no new protocol is defined.

**Context:** The machine doing the work and the machine you sit at are
increasingly not the same one, and an AI agent mid-task is exactly the workload
you least want tied to a laptop lid. The alternatives were a listening daemon
(a new port, a new authentication story, and a new attack surface on every host)
or a bespoke tunnel. SSH is already installed, already authenticated, already
trusted with a shell on that host, and already solves bastions, jump hosts,
hardware tokens and certificates.

**Implementation:**

- `internal/transport/` supplies two backends behind one seam, `ipc.DialFunc`
  (`func(ctx) (net.Conn, error)`): `Local(socketPath)` dials the Unix socket,
  `SSH(dest, opts)` starts the child. The `Client` took a dialer rather than a
  path, so nothing above the transport knows which it has.
- The destination string is passed to `ssh` **verbatim** and never parsed, which
  is what makes an `~/.ssh/config` `Host` alias keep its `HostName`, `Port`,
  `User` and `ProxyJump`. It is also the routing key everything else carries.
- The remote command is `quil --stdio`, not `quild`: the daemon-ensure logic
  (`startDaemon`, `waitForDaemonReady`, `findDaemonBinary`) lives in the TUI
  binary, and release archives ship both binaries together.
- `stdioConn` adapts the child's pipes to `net.Conn`. **Reads go through a pump
  goroutine rather than straight to the pipe**, because on Windows the handles
  are non-overlapped and `SetReadDeadline` fails with `os.ErrNoDeadline` — and
  three call sites depend on read deadlines. One uniform implementation beats a
  platform split. Write deadlines are honestly unsupported.
- `forcedSSHOptions` disable agent forwarding, X11, `PermitLocalCommand` and all
  port forwarding ahead of the user's own config, since OpenSSH takes the first
  obtained value and processes command-line `-o` first. Deliberately **not**
  forced: `ProxyCommand` / `ProxyJump` (bastion hops are a core requirement),
  identity and crypto policy, and `StrictHostKeyChecking` — `accept-new` is
  weaker than the default prompt, so the user's host-key policy is left intact.
- `ServerAliveInterval=15` / `CountMax=3` is the **only** liveness check once the
  session is up: `ipc.MsgHeartbeat` is declared but never sent. `ConnectTimeout`
  is forced too, because the dial runs before `tea.NewProgram` — there is no UI
  to press Ctrl+C in and no deadline on the call site, so an unbounded OS connect
  timeout on a dropped SYN is an unkillable hang.
- `LinkFailure` classifies a failed dial as retryable or not. A rejected or
  changed host key stops the ladder rather than retrying, because every retry is
  a full login and an overnight loop gets the laptop banned by the server's
  brute-force protection.

**Consequences:**

- Anything SSH reaches works with no setup on the remote beyond having `quil`
  installed — bastions, Tailscale addresses, public-internet hosts.
- The security model is SSH's. Quil adds no authentication of its own and needs
  none, but it also cannot be reached by anything that is not an SSH client —
  which is why a browser UI is gated on the mTLS backend the seam anticipates.
- A dropped link is a pause, not an ending: the panes never stopped, so there is
  nothing to resume. Keystrokes are **dropped** rather than queued while the link
  is down — a key typed at a dead connection would otherwise arrive minutes later
  in a live agent session, answering a question that had moved on.
- Every local-daemon lifecycle command (`quil daemon *`, `quil restart`,
  `quil status`, the update controls) **refuses** under `--remote` rather than
  acting on the wrong machine.

## ADR-27: Projects and the Multi-Daemon Router (v1.47.0)

**Decision:** A **project** is a named, rooted grouping that owns tabs, is stored
and enforced by the daemon, and belongs to exactly one daemon. One client can
hold several daemon connections at once; a `Router` multiplexes them behind the
same two-method client interface the `Model` already consumed.

**Context:** Tabs were a flat list, so six tabs across three repositories were
visually indistinguishable, and an agent parked on a permission prompt in a
background tab stayed invisible until you happened to look. Separately,
`quil --remote <host>` bound a whole TUI process to one daemon. Both were the
same missing piece — nowhere to hang *which work is this* or *which machine is
this*.

**Implementation:**

- Projects are **daemon-owned and persisted**, not client state, so a second
  client attached to the same daemon sees the same grouping. An existing
  `workspace.json` migrates into a single `Default` project with tab order
  preserved — no prompt, no data loss.
- `Project.Bootstrap` marks a project the daemon *invented* rather than one a
  user named. The name cannot be the signal — a user may legitimately name a
  project `Default`, and renaming the invented one is exactly what stops it being
  a bootstrap. The flag is what lets naming a project on a fresh remote host
  **rename** the structural default in place, so the host's existing tabs land
  under the chosen name instead of beside it.
- `Router` (`internal/tui/router.go`) is a third implementation of the client
  interface, beside `*ipc.Client` and the test fakes — which is why the `Model`
  needed no transport change to gain multi-daemon support. Connections are keyed
  by destination; the empty key is the local daemon.
- Messages carry `Origin`, stamped by the router on receive and tagged
  `json:"-"` so it never reaches the wire. **No protocol bump was needed** —
  "which daemon said this" is a client-side fact.
- `Router.activeDest` is an `atomic.Value`, not a closure over the `Model`. A
  closure built in `main` would capture the value `tea.NewProgram` copies, and
  the program mutates only its own copy — so it would report zero projects
  forever and route every unstamped send, keystrokes included, to the local
  daemon.
- Liveness is keyed off the router's **stop channel, not its conn map**. Retiring
  a conn to express deadness made `Conn(dest)` nil for exactly the destination a
  redial was about to run for, so the dialer was handed nothing to close and
  every reconnect leaked an `ssh` child plus its remote `quil --stdio`.

**Consequences:**

- One daemon dying no longer ends the session; each destination carries its own
  reconnect state, and its projects stay in the sidebar marked offline rather
  than appearing deleted.
- Anything that describes a machine must now name **which** machine. Plugin
  availability, filesystem browsing, git discovery, kube contexts and the recent
  directory list all had to move onto the wire or be filed per destination — see
  `.claude/rules/remote-dialogs.md`.
- A broadcast is one daemon's full state and says nothing about another's tabs,
  so every client-side diff (layout, pane sizes) has to be scoped to the
  broadcasting destination.
- Per-project MCP scoping is deliberately **not** done: it is a breaking change
  to shipped tools and wants its own opt-out and release note.

## ADR-28: Worktree-Owned Panes (v1.51.0–v1.59.0)

**Decision:** A pane may be created into a git worktree Quil made for it. The
daemon records both **whether** it created that worktree and **which** directory
it created, as two separate persisted fields, and offers removal only at close
time behind an explicitly armed toggle.

**Context:** Running several agents on branches in parallel is the most-cited
reason people adopt this class of tool, and the failure mode is silent: an agent
that believes it is isolated but is actually writing to the main checkout.

**Implementation:**

- New worktrees land in a **sibling** directory, `<parent>/<repo>-worktrees/<branch>`
  with `/` flattened to `-`, so nothing nests inside the checkout and no tool that
  walks the repo finds a second one. No `.gitignore` entry is needed.
- The branch is created off the repository's **default** branch — `origin/HEAD`
  where set and still resolving, then `origin/main` / `origin/master`, then the
  local pair, then HEAD — not off whatever the main checkout is on. A fix
  worktree created while the main checkout sat on a feature branch otherwise
  carries that feature's unmerged commits, so the pane is isolated in its
  directory but not in its history.
- `ValidateBranch` enforces **two grammars, neither subsuming the other**: the
  name reaches `git worktree add -b` as argv, so a leading dash reads as a flag
  and git's ref rules bar the rest; and it becomes a path segment, so it must not
  escape its parent and is capped at 255 bytes. git would refuse most of these
  itself — the point is that the refusal arrives beside the field the user typed
  into, rather than as a failed subprocess after a permit and a single-flight
  slot have been spent.
- **A failed add creates no pane.** git's own message is shown and Quil never
  falls back to the repository root: a pane on `master` you believe is isolated
  is worse than no pane. Every abandonment after a *successful* add runs
  `removeWorktreeFn`, or the next attempt at the same name fails against a
  directory nobody made.
- `Pane.WorktreeOwned` (may this be removed?) and `Pane.WorktreePath` (which
  directory?) are **both** persisted, and `CWD` cannot stand in for the second:
  CWD is a live cursor OSC 7 rewrites on every `cd`, so a pane created in
  `feat-a` whose shell walks into `feat-b` would have close-time removal
  force-delete a checkout this daemon never made — reachable by a pane's own
  child with one escape sequence. A pane restored from a snapshot predating
  `WorktreePath` is never offered removal at all.
- `PreparingWorktree` is **runtime-only and deliberately not persisted**. A tab
  opened on a new branch holds a PTY-less placeholder naming the branch, with a
  spinner, for as long as the checkout runs — on a monorepo, minutes. Spawning
  the *requested* pane type as the placeholder would start an agent in the main
  checkout, which is the failure this exists to prevent.
- `worktreeAddTimeout` is 120 s, far longer than any other git budget, because an
  add checks out a tree. Holding a blocking-FS permit that long would normally be
  the pool-drain hazard, but the `worktreeAdding` single-flight *is* the budget:
  at most one add runs daemon-wide however many clients ask.

**Consequences:**

- Removal is never automatic. The close confirm carries an unticked row, armed
  with `space`, off every time the dialog opens, with a live count of what would
  be lost — **ignored files included**, since a `.env` or a `build/` is exactly
  what a forced removal destroys with no branch to recover from. The branch is
  always kept.
- Quil deletes only what Quil created. A worktree you made by hand never gets the
  offer.
- A worktree that goes missing restores the pane **unspawned**, naming the gone
  directory, with `Alt+R` to retry — never quietly reopened in the main checkout,
  which for an AI pane would resume the recorded conversation against the wrong
  tree.

## ADR-29: Zero-Copy IPC Frame Encoding (v1.59.2)

**Decision:** Encoding and decoding an IPC frame takes a fast path that writes an
already-marshalled payload into the envelope verbatim, falling back to the
pre-existing `encoding/json` path whenever the shape is not certainly identical.
The bytes on the wire are unchanged.

**Context:** `EncodeFrame` marshalled a `Message` whose `Payload` was already
encoded JSON, and `encoding/json` compacts and validates every `RawMessage` on
the way out — walking an 8 KB `pane_output`'s ~11 KB of base64 through its
scanner at ~10 ns/byte. That second pass was **99% of encode time** and cost 7×
the base64 encode that produced the bytes it re-scanned. A pane streaming output
occupied about half a CPU core purely formatting messages, and the cost
multiplied by the number of busy panes.

**Implementation:**

- Every fast-path function returns a bool meaning *I handled it*. False means the
  caller runs the JSON path, which stays the sole definition of correct
  behaviour including its exact error values. **When a shape is not certainly
  identical, decline** — a slower correct frame costs microseconds, a wrong one
  corrupts a length-prefixed stream.
- `fastStringSafe` rejects `<`, `>` and `&` because `json.Marshal` escapes them
  by default and `respondTo` echoes whatever ID an IPC client sent. Bytes above
  0x7E are rejected not because valid non-ASCII needs escaping — `café` survives
  verbatim — but because *invalid* UTF-8 is silently replaced with U+FFFD, and
  rejecting all non-ASCII is cheaper than checking validity.
- `payloadInlinable` must establish the payload is **one complete JSON value with
  nothing after it**, and that is a security property rather than tidiness. The
  payload is concatenated between `,"payload":` and the closing brace, so
  trailing content becomes *sibling envelope keys* and `encoding/json` resolves
  duplicates last-wins: a payload of `{},"type":"shutdown","x":{}` turns a
  `pane_output` frame into a `shutdown` frame, decoding cleanly. An earlier
  version checked only the first and last byte.
- The check is split by cost: `paneOutputSpan` is the hot path and is already
  injection-proof by construction (exactly one `{` and one `}`, at the two ends),
  so it is free; `json.Valid` covers everything else at ~5.8 ns/byte, measured at
  1.4 µs for a two-pane `list_panes_resp` — deliberately *not* applied to
  `pane_output`, where 47 µs on an 8 KB payload would give back most of the win.

**Consequences:**

- Encoding a pane-output frame is ~35× faster and receiving one ~10× faster, with
  roughly half the allocations. The half-core became closer to 1%.
- **Nothing changed on the wire**, so mixed versions of the TUI, the daemon and
  the MCP bridge interoperate exactly as before — the property that made this
  shippable without a protocol bump.
- A fuzz target (`fastframe_fuzz_test.go`) pins the fast and slow paths to
  identical output, which is the only durable defence for an optimisation whose
  failure mode is a corrupted stream rather than a wrong answer.

## ADR-30: Keybinding Action Registry, Dispatch Tiers and Presets (v1.60.0–v1.62.0)

**Decision:** Keys resolve to **named actions**, not to config fields. The
registry, the resolution rules and the presets live in `internal/keymap`, which
imports neither `config` nor `tui`; the keymap is read from its own file,
`~/.quil/bindings.toml`, keyed by action ID.

**Context:** `F1` → Shortcuts was maintained by hand beside the bindings and had
drifted — seven of the eight project shortcuts were missing from it. Adding an
action meant touching a config struct, a dispatch switch and a display list, with
nothing tying the three together. Separately, tmux users wanted a whole layout,
not a field-by-field rebind.

**Implementation:**

- `internal/keymap` is stdlib + `BurntSushi/toml` only. No `config`, no `tui`, no
  `QuilDir()` — so the whole package tests without a `Model` or a `QUIL_HOME`.
  Path-derived reads live in `internal/config/bindings.go`.
- Each `Action` carries a `Tier` saying **which of `handleKey`'s two switches it
  dispatches from**, because `tryPluginRawKey` runs between them: an early action
  beats a plugin's `raw_keys` claim, a late action loses to it. This is a real
  dispatch-order fact, not a category label, and putting it in the registry is
  what lets the conflict checker see cross-tier shadowing.
- `Resolve` merges layers with **replace, not union** semantics, lowest first:
  shipped defaults → preset → the user's `[bindings]`. Absence inherits, presence
  replaces, and `""` is a *value* — an explicit unbind — rather than a deletion
  that propagates upward. Treating `""` as removal would let a preset veto an
  explicit user override, inverting the model.
- Keybindings live in **their own file** because `config.toml` is rewritten in
  full whenever any setting changes. A keymap resolved into it would be frozen as
  literal strings the first time the user edited an unrelated option, and no
  future preset or default change could ever reach them.
- Presets are embedded TOML (`internal/keymap/presets/`). They **replace rather
  than add**: a preset that keeps both key sets is neither keymap and doubles the
  conflict surface. An action a preset does not name keeps its default.
- Anything unhonourable is reported rather than dropped — duplicates, cross-tier
  shadowing, collisions with built-in keys, unparseable specs, unreachable
  sequence openers, and an unusable `${prefix}` — as warning rows at the top of
  `F1` → Shortcuts and in the client log, so a binding that breaks the dialog
  itself is still diagnosable.

**Consequences:**

- `F1` → Shortcuts is **derived from the registry**, so the list cannot drift
  from the bindings again, and it is where you read a binding's canonical
  spelling back.
- Migration from the old `[keybindings]` table carries across **only settings
  that differ from the shipped defaults**, so a binding the user never changed
  stays free to follow future default changes rather than being frozen at
  today's value.
- A bad line never costs the rest of the keymap: only the affected action falls
  back to its default.
- Sequences (`"ctrl+b c"`) become expressible, and pressing the opening key twice
  sends it through to the pane — which is what keeps a tmux *inside* a Quil pane
  reachable.
- `bindings.toml` is read once at startup with no hot reload. `F1` → Shortcuts
  always renders the live keymap, which is how a user confirms an edit took.

## ADR-31: Docker Sandbox Panes — the Mount Set as the Security Boundary

**Context:** An AI agent with `--dangerously-skip-permissions` can reach every file the user can. Running it in a container is the obvious answer, but a container that bind-mounts a git checkout is only as safe as the mount set: git reads *executable* configuration out of `.git` (`core.fsmonitor`, `core.hooksPath`, `.git/hooks/`, `config.worktree`, `.git/modules/<sub>/`), and Quil's own git ticker runs those on the host every few seconds. Two further constraints shaped the design. Anthropic's Commercial Terms make a vendor that ships Claude Code preinstalled in its own image "preinstalling or running Claude Code in your products or services", and its authentication policy forbids collecting, storing or intermediating credentials — so Quil can neither publish an image nor copy `~/.claude/.credentials.json`.

**Decision:** Sandbox panes run one container per pane, launched by the **daemon**, with an image the **user** supplies. The boundary is the mount set and nothing else — no `--privileged`, no `--cap-add`, no `--network` restriction. Every mount decision lives in `internal/sandbox`, which is pure path arithmetic apart from one file, so the whole boundary is table-testable with no Docker present.

**Implementation:**

- `internal/sandbox/` — `NewMapping` resolves a host CWD to a checkout and computes every path; `runargs.go` turns a `Mapping` into the `docker run` argv. `docker.go` is the only file that executes anything, which is what keeps the boundary unit-testable.
- **Mount set.** The checkout is read-write. The repository's `.git` is mounted **as a mountpoint itself**, read-write (git must write the index, HEAD and refs), with the executable pieces pinned read-only *on top*: `objects`, `hooks`, `config`, `config.worktree`, `modules`, `worktrees`. Mounting `.git` itself is load-bearing — a mountpoint answers `EBUSY` to rename and remove, and without it the agent renames `.git` and writes a fresh one carrying `core.fsmonitor`, which the host then executes. The pane's own object store `/quil/objects` is a mountpoint for the same reason.
- **Object isolation.** New objects go to a per-pane store (`GIT_OBJECT_DIRECTORY`) with the repository's own store as a read-only alternate, so nothing inside can delete history. Quil copies new objects across every 30 s and again at pane close.
- **Refusals never soften.** `NewMapping` refuses a mapping whose worktree or `.git` contains `$QUIL_HOME` — the container would otherwise get every pane's settings, the workspace and the daemon socket.
- **Per-pane state on the host** at `$QUIL_HOME/sandbox/panes/<pane-id>/` — hook spool, `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, object store — mounted at `/quil`. One shared Claude config directory is opt-in (`shared_claude_config`) because it merges every sandbox pane into one trust domain.
- **Hooks keep working.** A Linux `quild` is mounted read-only at a fixed container path and the agent's hooks invoke it, so notifications, work state, input history and session resume behave as they do locally.
- **Authentication is per vendor.** Claude Code: forward `CLAUDE_CODE_OAUTH_TOKEN` by *name* (Docker reads the value from the daemon's environment, so it never enters argv or a log), or sign in inside the container — chosen per pane in the create dialog. When no token exists the pane drives `claude setup-token` itself under a PTY, behind a daemon-wide single-flight, and stores the result in the OS environment store (`HKCU\Environment` on Windows), never a Quil-owned file. Codex: its `auth.json` is **copied** from the host, because Codex ships no minting command and a copy cannot be written back over. OpenCode: in-container sign-in, per container.
- **No image is published or pulled.** `docker/sandbox/Dockerfile` plus `scripts/sandbox-image.sh` build one locally and then *verify* it (non-root user, agent binary, `git`). There is deliberately no default registry name: `anthropics/claude-code` on Docker Hub is a honeypot, not Anthropic's.

**Consequences:**

- The user must build an image before the feature does anything. That is the cost of the Commercial Terms position, and the build script exists to make it one command.
- Egress is **not** bounded. A default-deny firewall needs `NET_ADMIN`, which Quil does not grant; it has to come from the image's own runtime.
- Branch pointers are **not** bounded — a pane can move or delete any branch. Recoverable via reflog, and read-only refs would make committing impossible.
- Bind-mount IO on Docker Desktop is ~20× slower, and inotify does not cross the mount on Windows, so file watchers need polling. Both are documented rather than worked around.
- Repositories with submodules are unsupported: a submodule's `.git` in a linked worktree is a relative path that escapes the worktree.
- Full guide: [Sandbox panes](sandbox-panes.md); the invariants live in `.claude/rules/sandbox.md`.

## ADR-32: Protocol Base — Hello, Error Replies, Numbered Typed State (v1.82.0)

**Context:** #237 set out to find what a future scripting surface (#143) needs from the wire protocol, and found three gaps. The daemon could not tell what was on the other end of a conn — an MCP bridge, the TUI, a one-shot probe like `quil status` — short of the best-effort, answerless `client_hello` the process dialog already used. A request type the daemon did not recognize, or a payload it could not decode, was simply dropped: the caller paid its own timeout with nothing to say why, which read as "the daemon is slow or wedged" for what was actually "the daemon does not have this feature yet". And a `workspace_state` broadcast carried no ordering at all — a client that failed to decode one had no way to ask for a fresh copy, and the existing code applied whatever it *could* decode, which for a decode failure meant reconciling every tab away in place of a merely-unreadable frame.

**Decision:** Add a small, versioned protocol base underneath the existing message set, without breaking a single conn that does not speak it.

- **`hello` is fire-and-forget, sent from the same funnel as the existing `client_hello`.** `sendClientHello` (`cmd/quil/hello.go`) sends both on every durable dial — the TUI, every MCP bridge, a redial. `hello` carries an id and protocol version 1 and is answered with `hello_resp`, but nothing in this phase blocks waiting on that answer: making it mandatory now would refuse a mixed-version fleet the existing version gate already tolerates. Phase 4 promotes it to first-and-mandatory once every client in a supported fleet can be assumed to send it.
- **Error replies go only to an ID-BEARING request on a conn that has said hello (`helloRegistry.protoOf(conn) >= 1`), and the reason is concrete, not defensive.** A pre-3a MCP bridge's `requestWithTimeout` (`cmd/quil/mcp.go:162-167` pre-3a) matched a response to its own request by correlation id alone and handed it back as the answer — it never inspected `resp.Type`. Sending that bridge an `error` frame for a request it does not recognize would have it decode the error payload's `code`/`message` fields as if they were the tool's own JSON result: silently wrong, not merely unhelpful. A conn that has said hello has, by construction, told the daemon it understands `error` (`Daemon.replyError`, `internal/daemon/hello.go`, which requires `msg.ID != ""` as well); every other conn, and every id-less request whatever the conn's protocol, keeps the old silence.
- **Workspace state gets a typed schema with frozen keys.** `ipc.WorkspaceState`/`TabState`/`PaneState`/`ProjectState` (`internal/ipc/state.go`) replace the old `map[string]any` builder, keeping the exact same JSON keys and the same presence rules — no `omitempty` where the map always wrote a key, `omitempty` or a pointer where it wrote one conditionally. A frozen oracle of the old builder (`internal/daemon/state_oracle_test.go`) is compared against the typed one field-for-field, so a decode failure downstream is now a real type error rather than an unnoticed key silently missing.
- **`rev` is taken last, under one lock (`Daemon.stateMu`).** `buildWorkspaceState` (`internal/daemon/daemon.go`) reads every state field first and increments `d.stateRev` only immediately before returning, inside the same lock span — rev order therefore always matches content order, which is what lets a client refuse a frame by number alone rather than by racing to compare content. `RunID` is minted once per daemon process, so a client can tell a restart (rev restarting at 1) from a merely-stale frame of the same run.
- **A client tolerates a gap in the rev sequence rather than treating it as a sign a frame was lost.** Every `workspace_state` frame is a full snapshot, so there is nothing to reconcile a skipped number against; a client only ever refuses a frame at or below the highest `(run_id, rev)` it has already applied for that destination (`acceptStateRev`, `internal/tui/staterev.go`), never one that jumped ahead.
- **The typed schema trades per-field tolerance for a stricter failure mode.** The old `map[string]any` builder let one field of the wrong JSON type through as a decode-time no-op for that key alone; the typed `WorkspaceState` fails the WHOLE frame's decode on the same mistake. A same-version daemon cannot actually produce such a frame — the typed builder and the client's typed decode share the one Go definition — so this is a theoretical cost paid for a real one: a real per-field failure mode nobody could see happening. Recovery is the same `state_req` path below, not a per-field fallback.
- **An undecodable frame is dropped, never applied as an empty state, and triggers one `state_req`.** Turning a frame the client cannot read into an empty `WorkspaceState` would reconcile every tab away for a single malformed broadcast. Instead it is dropped, and — only for a destination that has already applied at least one numbered frame, so the daemon is known to answer `state_req` — the client asks for a full copy: single-flighted per destination, an 8 s timeout, and generation-guarded so a late timeout cannot clear a newer request's own bookkeeping (`requestStateFor`/`endStateReq`, `internal/tui/staterev.go`; wired in `internal/tui/model.go`'s `MsgWorkspaceState` decode arm). `state_req` needs no attach (`handleStateReq`, `internal/daemon/staterev.go`) — a script or a bridge can ask cold.
- **`refused` is reserved, unused in this phase.** The code is defined in `ipc.ErrCodeRefused` so its spelling is settled ahead of need; phase 4's rights checks are what will send it.

**Implementation:**

- `internal/ipc/hello.go` — `MsgHello`/`MsgHelloResp`/`MsgError`/`MsgStateReq`, `ProtocolVersion = 1`, `HelloPayload`/`HelloRespPayload`/`ErrorPayload`, the two active error codes plus the reserved `refused`, and `DaemonCaps()` (`error`, `state_rev`, `state_req` alongside the pre-existing `GatedRequests`).
- `internal/daemon/hello.go` — `handleHello` (synchronous by requirement: frames on one conn dispatch in order, which is what guarantees a request sent right after `hello` is already seen as non-legacy) and `replyError`/`sendError`. `internal/daemon/procreport.go`'s `helloRegistry` gained `putHello`/`protoOf` alongside its pre-existing `client_hello` bookkeeping — a hello with no PID (a web or script client) makes a conn non-legacy but names no process for the dialog to list. `HelloPayload.ClientID` is truncated on receipt (`internal/daemon/hello.go`) but `putHello` does not carry it into the `helloRecord` it stores — the wire carries the id now, and nothing in this phase reads it; phase 4 is the first consumer.
- `internal/daemon/staterev.go` — `handleStateReq`. `internal/daemon/daemon.go`'s `buildWorkspaceState` and the default dispatch arm (`replyError` with `unknown_type`); `handlePaneHistoryReq`/`handlePaneHistoryEntryReq` now reply `bad_payload` on an undecodable request instead of silently answering nothing.
- `internal/tui/staterev.go` — `acceptStateRev`, `requestStateFor`, `endStateReq`, `forgetStateMark` (called on reattach, `internal/tui/reconnect.go` — a restarted daemon's numbering starts over, so what a client has "seen" for that destination must be forgotten, not merely superseded).
- `cmd/quil/hello.go` — `sendClientHello`/`sendHello`, and `processClientID`, the same id `SetClientID` hands to the `Model` so `hello` and `attach` name one process identically.
- `cmd/quil/mcp.go`'s `requestWithTimeout` now checks `resp.Type == ipc.MsgError` and turns it into a Go error carrying the code and message, instead of returning the frame as if it were the tool's own response.

**Consequences:**

- A conn that never sends `hello` — an old build, a bare probe — sees exactly what it always has for the one thing hello gates: no `error` replies, ever, for a request it sends. `rev` and `run_id` are NOT gated on hello — `buildWorkspaceState` stamps both onto every `workspace_state` frame it builds, so every conn (`internal/daemon/daemon.go`) receives them; a pre-3a client simply has no field for the extra keys and ignores them, same as any other forward-compatible JSON addition. Separately, the typed builder changed `workspace.json`'s key ORDER (it now follows the Go struct's declaration order rather than the old map's alphabetical one) while the keys themselves and every presence rule (`omitempty` vs. always-present) are unchanged.
- #143's scripting surface builds directly on this: `hello`'s `kind` field already accepts `"script"`, and the error codes, `state_req`, and the typed schema are the vocabulary it needs rather than a second protocol layered beside it.
- Phase 4 makes `hello` first and mandatory — gating the rest of a session on it — once every client in a supported fleet can be assumed to send one.

## ADR-33: Shared Data on the Daemon — Groups, Recent Folders, Notes

**Context:** #237's step 3b asked what a second client of one daemon, or a client attached over ssh, could not see that a single local window always could. Three answers: project groups (`project-groups.json`), the recent-folder list `Ctrl+N` offers (`recent-cwds[-<dest>].json`), and a pane's note (`notes/<pane-id>.md`) all lived on the CLIENT's own disk, keyed by data the daemon never saw. A second window on the same machine had its own copy of each; a remote window had its own copy on a different machine entirely, unrelated to the daemon actually running the panes. Nothing was broken — each file worked exactly as designed for one window — but nothing about them followed the workspace they described.

**Decision:** Move the three into the daemon that owns the projects and panes they describe, and let every attached client read and change them through IPC. What stays per client is exactly the part that has no daemon-side answer: which group is expanded, and in what order the groups are drawn.

- **A project's group is `Project.Group`, filed on the daemon that holds the project.** Each daemon also keeps its own list of group names, so a group with no member yet still exists — a name is never inferred purely from who belongs to it. The client renders every project, from any daemon, whose group name matches (case-insensitively) as one merged group; identity is the name, exactly as it always was for a single daemon.
- **Group DISPLAY ORDER and the collapsed flag stay client-side, in `project-groups.json`.** A cross-host merged group has no daemon whose list order could serve as the sequence, so there is no `move` request at all — only `create`/`rename`/`delete`. The client file becomes a CACHE of each daemon's membership for that daemon's own projects, and the user's own ordering and collapse choices, which no daemon needs to know.
- **The TUI never reads `hello_resp`, so a daemon states this capability on the one frame the TUI does read.** `WorkspaceState.SharedData` (`shared_data`, broadcast-only, never on disk) is `true` on every frame a 3b daemon sends; per DESTINATION, a client either has daemon-held groups/recent/notes for that daemon's projects and panes, or — an older daemon, or one not yet answered — behaves exactly as before for it. `ipc.DaemonCaps()` lists the same string in `hello_resp` for anything that does read it.
- **A note's text never rides the frame; a monotonic version number does.** `Pane.NoteRev` travels as `note_rev`; the text sits in `QUIL_HOME/notes/<pane-id>.md` on the DAEMON's own machine, fetched with `note_get` and written with `note_set`. A save names the revision it was loaded from; a stale one is refused (`conflict`, `current_rev`) and nothing is written, so two editors racing the same base can never silently overwrite one another. The counter never resets on a delete — an empty note is rev N+1, not rev 0 — because resetting it would let a save based on a revision the delete already invalidated overwrite whatever a later writer put there. Zero means only "this pane has never had a note"; "no note right now" is an empty file at a nonzero revision.
- **The save's own answer reaches the saving client before the frame that carries its new revision can.** `handleNoteSet` calls `respondTo` and only afterwards stores the bumped `NoteRev` and asks for a broadcast — on the SAME goroutine, so any read of the new value happens only after the response was already enqueued on the same conn's ordered queue, ahead of any state frame built from it. Without that order, a client's own save would race its own frame and could read its own change back as a conflict.
- **Every note read and write runs on a worker goroutine, under one lock scoped to that pane's note alone.** `Pane.noteMu` is a leaf lock held across the revision check and the file write, so two saves against one base can never both apply; the pane's general-purpose `PluginMu` is never held across the I/O, and nothing that already holds `PluginMu` calls into notes. Dispatching straight into file I/O would block that connection's other traffic behind however long the write takes.
- **The one-time import is a single check-and-apply, never a merge.** On a destination's first `shared_data` frame the client sends its old files once, per kind (`groups`, `recent`, `notes`), each only for a kind whose marker (`shared-import.json`) is still unset. The daemon applies groups only while it holds no group and no grouped project, applies the recent list only while its own is empty, and imports a note only while that pane has no note history — `note_rev` still 0 and no note file on disk — checked under the pane's note lock. The client already leaves out a pane whose frame shows a nonzero `note_rev`, but that frame can be old when the import arrives; the daemon's own rev check is what keeps a note deleted in between (the revision never resets) from coming back through this door. The client also sends a note only when its pane id is on that daemon and on no other connected one, so a destination's notes wait until every connected daemon has reported its pane ids. Two clients importing at once therefore cannot both apply. Old client files are never modified or deleted, whatever the daemon answers.
- **Every new field carries a cap because it is remote-controlled data another client renders or a daemon stores indefinitely**: 256 KiB per note (a note stored before the cap existed stays editable up to its own size, but cannot grow), 32-rune group names validated through the shared `internal/textsafe` strip rule (moved out of `internal/tui/remotetext.go` so the daemon, which validates names from any IPC client, can use the identical rule), 64 groups per daemon, 5 recent folders, 8 MiB per import request.

**Consequences:**

| Client | Daemon | Result |
|---|---|---|
| new | old | No `shared_data` on that destination's frames — groups, recent folders and notes for its projects work exactly as before, client-side. |
| old | new | Unknown keys are ignored; the old client keeps using its own files and its edits never reach the daemon. A release build is refused outright by the existing version gate; a non-release (dev) build skips that gate and can still attach — and, for the LOCAL destination only, can write `notes/<pane-id>.md` directly without going through `note_set`, so `NoteRev` does not move even though the file did. Accepted as a dev-only limit: the daemon's in-memory revision then understates what is really on disk until the next read notices. |
| new | new | Full behaviour described above. |

Renaming or deleting a group fans the operation out to every connected shared destination that lists the name, each as its own request; an offline host or one that refuses keeps the old name until the user repeats the change — best-effort, not transactional, because there is no daemon that could hold the answer for all of them at once. A save the daemon refused after its editor already closed, or one still unanswered at quit (bounded at two seconds), is written to `QUIL_HOME/notes-conflicts/` on the client rather than lost, with the destination, pane id and a timestamp in the name.

Out of scope for this phase, deliberately: MCP tools for groups or notes, authentication (phase 4), incremental deltas (every import and every frame is a full list), and any cleanup of a note when its pane is destroyed — notes keep the pre-existing "survive the pane" rule.

## ADR-34: Client Authentication and Rights (phase 4)

**Context:** #238, phase 4 of #234. Until now the daemon's only entry was a unix socket whose guard was the OS account. A browser gateway (phase 5) and TLS (phase 6) need a network transport first, and a network transport needs to know who is calling and what they may do.

**Decision:** One step: a loopback-only TCP listener, off by default (`[listener] tcp`), plus tokens, three rights levels, local-socket hardening and an audit log.

- **The daemon owns the listener, the login and ONE rights check** (`clientauth.Allows`, run by `admitRequest` first in `handleMessage`). An unauthenticated TCP conn reaches only `daemon/auth.go`; it is sent no broadcast, its frames are capped at 4 KiB until it has logged in, at most 8 may be logging in at once (the 9th is closed at accept), and a timer (not a read deadline) bounds each login step.
- **Mutual proof, the token never crosses the wire:** a SCRAM-SHA-256-shaped exchange without password stretching (a token carries 256 random bits). The daemon stores `StoredKey`/`ServerKey`, never the token. `AuthMessage = "quil-auth-v1," + id + "," + nonce_c + "," + nonce_s`. The client checks `server_sig` before anything else, so a port squatter learns nothing reusable and is refused. A stolen `tokens.json` can impersonate the daemon to a client but cannot log in. There is no channel binding: the exchange resists replay, not a live relay — which this threat model cannot reach, since the listener is loopback-only and a squatter holding the port means the daemon has no listener there. TLS comes in phase 6.
- **Every login failure is one shape:** `error refused` carrying the hello's ID and one reason (`login required` before the challenge; `token refused` after it, whether the id is unknown, the proof wrong or the token expired; `login timeout`; `too large` for an oversized proof frame), flushed for up to 1 s, then the conn closes. An oversized FIRST frame (an HTTP request, for one) has no ID to answer and is closed with no frame. A guessing backoff (`min(250 ms × 2^(n-1), 2 s)`) runs after the proof arrives, never before the first frame.
- **Every message type is in exactly one class** — view, act, admin, local, never — and an unclassified type is admin. A sweep test parses `internal/ipc` so a new type cannot ship unclassified, and the whole table is pinned, so a type cannot quietly move to a weaker class either.
- **Levels:** `read-only` sends nothing that mutates and never spawns, writes to or resizes a PTY; it does disclose the workspace and pane output, and it follows the project of the daemon's active tab. `standard` can type into a shell, which IS code execution as the owner; what it cannot do is start a program by raw arguments or as an overlay, run lifecycle/settings requests (shutdown, plugin reload, overlay policy, kill process, update check and staging), or manage tokens. `full` equals a local client except token management, which is local-socket only.
- **Named selections:** the TUI sends toggle names and the kube context; the daemon resolves them (`applyNamedSelections`), so a standard token keeps the setup dialog while raw `instance_args` stay full-only.
- **The TUI enforces the same rights before the daemon has to.** The router sends a read-only destination view-class messages only; every workspace change aimed at one is refused locally with a flash (create, close, rename, tab switches, reorders, layout arrangements, drags, marks), and create/close/rename are greyed in the palette and context menus. Admin actions — stop daemon, plugin reload, kill process, the overlay policy — are refused locally for read-only AND standard, so the viewer does not, for example, quit after a stop the daemon would refuse.
- **Revocation is atomic with admission:** admission, revoke and expiry run under the token store's lock; revoke silences live conns before it closes them.
- **Client ids are bound to their principal** (local, or the token), so a viewer cannot take the owner's master role. The local socket always wins an id. After a daemon restart the size-master reserve waits for the LOCAL principal only: a TCP client naming the reserved id is admitted as an ordinary client without the slot, and while the reserve stands every state frame names the master as `reserved-for-local:<id>` (padded past the client-id cap, so it names no client), which keeps every TUI a follower.

**Consequences:** the local TUI needs no setup and every pre-phase client keeps working (old dial order, id-less hello, `quil status`). An agent in a pane runs as the owner and can mint a `full` token — unchanged trust. Another local account can keep the 8 pending slots full and delay TCP logins (availability is not guaranteed); it cannot affect the local socket. `quil --connect` is a remote destination: `quil daemon`, `quil clients`, `quil restart`, `quil status` and `quil mcp` refuse to run under it, it never starts or restarts a daemon, and it does not apply a staged update at launch (the respawn would lose the token). On Windows the first start of the updated daemon rewrites the quil folder's access list to owner + SYSTEM, recursively, and the socket's own ACL is only claimed once the two-account test in [Security](security.md) has run. Full user guide: [Security](security.md); the invariants live in `.claude/rules/client-auth.md`.

## Storage Layout

```
~/.quil/                           (or ./.quil/ in dev mode — see .claude/rules/dev-environment.md)
├── config.toml                    # User configuration (TOML)
├── bindings.toml                  # Keymap: preset, prefix, per-action overrides (ADR-30)
├── quil.log                       # TUI client log (slog text format)
├── quild.log                      # Daemon log
├── quild.stderr.log               # Daemon stderr — SIGQUIT dumps and panics survive here
├── quild.sock                     # IPC socket (Unix) / Named Pipe (Windows)
├── quild.pid                      # Daemon PID file
├── tokens.json                    # Token verifiers (never tokens), owner-only (ADR-34)
├── audit.log                      # TCP logins, refusals, token changes, privileged requests (ADR-34)
├── shellinit/                     # Auto-generated shell integration scripts
│   ├── bash-init.sh
│   ├── pwsh-init.ps1
│   └── zsh/
│       ├── .zshenv
│       └── .zshrc
├── workspace.json                 # Project/tab/pane/layout state
├── workspace.json.bak             # Previous snapshot (rollback)
├── buffers/                       # Ghost buffer binary files
│   └── pane-XXXXXXXX.bin
├── window.json                    # Window size/position persistence
├── instances.json                 # Saved plugin instances (SSH connections, etc.)
├── recent-cwds.json               # Recent directory quick-pick — local daemon
├── recent-cwds-<dest>.json        # …and one per remote destination (ADR-27)
├── remote-projects.json           # Last-seen project list, so an offline host
├── remote-projects-<dest>.json    #   still shows by name instead of vanishing
├── plugins/                       # User TOML plugin definitions (ADR-6)
│   └── *.toml
├── notes/                         # Pane notes (M7) — one file per pane
│   └── pane-XXXXXXXX.md
├── history/                       # Per-pane submitted-prompt history (Alt+Shift+I)
│   └── pane-XXXXXXXX.jsonl
├── events/                        # Hook event spools, polled every 200 ms
│   └── pane-XXXXXXXX.jsonl        #   unlinked at daemon start, never truncated
├── paste/                         # Clipboard image proxy output (ADR-17)
│   └── quil-paste-<ts>-<rand>.png
├── conpty/                        # Bundled OpenConsole host — Windows 10 only (ADR-25)
│   └── <version>/                 # conpty.dll + OpenConsole.exe, extracted from the embed
├── mcp-logs/                      # Per-pane MCP interaction logs (M10)
│   └── pane-XXXXXXXX.log
├── claudehook/                    # ADR-23. Created lazily on first hook.log write;
│   └── hook.log                   # absent on a healthy install. The sh/ps1 scripts
│                                  # this once held were replaced in v1.18.0 by the
│                                  # `quild claude-hook` subcommand — see the ADR
├── opencodehook/                  # Embedded OpenCode JS plugin
│   └── *.js
├── codexhook/                     # Codex hook log
│   └── hook.log
├── sandbox/                       # Docker sandbox panes (ADR-31)
│   ├── empty/                     # Read-only shadow directory for pinned mounts
│   ├── empty-file                 # Read-only shadow file for pinned mounts
│   ├── overlays/<pane-id>/        # Never mounted into the container
│   └── panes/<pane-id>/           # Mounted at /quil — hook spool, claude/, codex/, objects/
├── sessions/                      # Per-pane agent session ids (ADR-23)
│   ├── pane-XXXXXXXX.id           #   Claude Code
│   ├── opencode-pane-XXXXXXXX.id
│   └── codex-pane-XXXXXXXX.id
└── update/                        # Auto-update staging
    ├── notified.json              # a version the user was TOLD about
    └── lastrun.json               # a version the user actually RAN — the
                                   #   What's New marker, deliberately not the above
```

## Project Rules

The project ships rule files under `.claude/rules/` that document non-obvious constraints for both human contributors and AI coding assistants:

- **`.claude/rules/dev-environment.md`** — production isolation. The Quil author runs Quil itself in production from `~/.quil/`; any operation that touches that directory or the running daemon during development is destructive. All development work uses dev mode (`./quil --dev`, data in project-root `.quil/`). The `kill-daemon`/`reset-daemon` helper scripts target production paths and are off-limits during development.
