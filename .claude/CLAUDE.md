# Quil — Project Instructions

## What is this?

Quil is a persistent workflow orchestrator / terminal multiplexer for AI-native developers. Written in Go with a Bubble Tea TUI frontend.

## Tech Stack

- **Language:** Go 1.25
- **Module path:** `github.com/artyomsv/quil`
- **TUI:** Bubble Tea v2 (`charm.land/bubbletea/v2` v2.0.2)
- **Styling:** Lipgloss v2 (`charm.land/lipgloss/v2` v2.0.2)
- **PTY (Unix):** `creack/pty/v2`
- **PTY (Windows):** `charmbracelet/x/conpty` v0.2.0
- **Config:** TOML via `BurntSushi/toml`
- **IDs:** `google/uuid`
- **Grapheme segmentation:** `rivo/uniseg` v0.4.7 — direct since the sidebar cell-cutters needed cluster boundaries. It SEGMENTS only; `lipgloss` remains the sole width authority, or a cut can disagree with the `.Width` that paints the row

## Architecture

Client-daemon model:

- `cmd/quil/` — TUI client (Bubble Tea)
- `cmd/quild/` — Background daemon
- `internal/config/` — TOML configuration (`Load` reads, `Save` writes atomically via `.tmp` + rename). `UIConfig.ShowDisclaimer` controls startup beta dialog
- `internal/daemon/` — Session manager, message routing, event queue (`event.go` — bounded, mutex-protected, watcher pub/sub for MCP)
- `internal/persist/` — Atomic workspace/buffer persistence (JSON snapshots, binary ghost buffers)
- `internal/shellinit/` — Automatic OSC 7 + OSC 133 shell integration (embedded init scripts, `//go:embed`)
- `internal/plugin/` — Pane plugin system (registry, built-ins, TOML loading, scraper)
- `internal/gitworktree/` — git worktree listing + creation (the repository WRITES; kept apart from the read-only `gitinfo`, which a ticker runs)
- `internal/clipboard/` — Platform-native clipboard read/write (Win32 API, pbpaste/pbcopy, xclip/xsel)
- `internal/keymap/` — Keybinding action registry: canonical chords, the action table with its dispatch `Tier`, and `Build` (config specs → lookup + conflict list). Stdlib only
- `internal/tui/` — Bubble Tea model, tabs, panes, layout tree, styles, text selection, notification sidebar

Deep package notes — `internal/transport/`, `internal/pty/`, `internal/ipc/`, `internal/claudehook/`,
`internal/claudesessions/`, `internal/opencodehook/`, `internal/hookevents/` — live in the scoped
rules under `.claude/rules/` (table below). They load automatically when you open a file in those
packages, so they cost nothing on unrelated work.

## Building

Go and make are NOT installed locally. Use `scripts/dev.sh` (Docker-based):

```bash
./scripts/dev.sh build          # Build all variants: prod, dev, debug (6 binaries)
./scripts/dev.sh test           # Run tests
./scripts/dev.sh test-race      # Tests with race detector (CGo — handled automatically)
./scripts/dev.sh vet            # Lint
./scripts/dev.sh cross          # Cross-compile all platforms
./scripts/dev.sh image          # Build scratch-based Docker image
./scripts/dev.sh clean          # Remove built binaries
./scripts/dev.sh docs-size      # Check .claude/ agent-context files against size limits
```

`build` runs `docs-size` first (`scripts/check-claude-md-size.sh`, host-side, no Docker) and
refuses to build when `.claude/CLAUDE.md` exceeds 100,000 bytes or a rule file exceeds 140,000.
The harness truncates an over-limit file silently, from the TAIL — so the symptom is not "the
file is too big", it is "the thing documented last week is the one Claude does not know".
There is deliberately no override flag, for the same reason `refuse_if_binaries_held` has none.

### Build Variants

`build` produces three matched pairs via compile-time ldflags:

| Variant | TUI | Daemon | Behavior |
|---------|-----|--------|----------|
| **prod** | `quil.exe` | `quild.exe` | Stripped (`-s -w`), normal behavior |
| **dev** | `quil-dev.exe` | `quild-dev.exe` | Auto dev mode (`QUIL_HOME=.quil/`), debug logging, finds `quild-dev` |
| **debug** | `quil-debug.exe` | `quild-debug.exe` | Debug logging, connects to production `~/.quil/`, finds `quild-debug` |

Ldflags: `buildDevMode` (auto-sets `QUIL_HOME`), `buildLogLevel` (overrides config log level), `daemonBinary` (daemon binary name for `findDaemonBinary`). Dev variant is self-contained — just run `./quil-dev.exe`, no flags needed.

Go module cache is persisted in a Docker volume (`quil-gomod`) for fast repeated builds.

`build` and `clean` refuse to run while any binary they would write is held by a running process (`refuse_if_binaries_held` in `scripts/dev.sh`). Neither platform can overwrite a running executable — Windows fails the open with a sharing violation, Linux returns ETXTBSY — and the ORDER is what makes it dangerous rather than the failure: the six builds are chained with `&&`, so a holder on the Nth leaves the first N-1 freshly built and the rest stale, and a new TUI beside a stale daemon fails the version gate at launch, reading as a bug in whatever you were working on. Detection is a **non-destructive probe, not a process query**: `(exec 3>>"$target")` asks the OS the exact question the build is about to ask, and append never truncates. The first version instead read `.quil/quild.pid` and looked for a daemon — it missed a dev TUI holding `quil-dev.exe` and let the half-build through on its first real use, which is why this checks the condition rather than a proxy for it. Only files in the project directory are probed, so a production install elsewhere is never touched, and a stale or malformed pid file cannot block a build because no pid file is consulted. There is deliberately no override flag: "build anyway" produces exactly the mismatched set it exists to prevent.

### Windows Icon

`build` and `cross` embed the Quil brand mark as a Windows executable icon via `go-winres` (v0.3.3). Build assets live in `winres/` (icon PNGs + `winres.json` manifest with `RT_GROUP_ICON` + `RT_VERSION`). The build script installs `go-winres` inside the Docker container and generates `.syso` files in `cmd/quil/` and `cmd/quild/` before `go build`. The Go linker picks up `.syso` files automatically (Windows only — ignored on Linux/Darwin). Generated `.syso` files are gitignored.

## Release Process

Single workflow (`release.yml`) with two jobs:

1. **`release` job** — triggers on push to master. Analyzes conventional commits since last tag, computes version bump (major/minor/patch), updates `VERSION` + `CHANGELOG.md`, commits `chore(release): vX.Y.Z`, creates git tag, pushes. Outputs version to the next job.
2. **`goreleaser` job** — runs after `release` job. Checks out the tagged commit, extracts release notes from `CHANGELOG.md` via sed, runs GoReleaser to cross-compile 5 platforms (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64), creates `.tar.gz` (Unix) / `.zip` (Windows) archives with both `quil` + `quild`, publishes GitHub Release with SHA256 checksums. Release notes applied via `gh release edit` (decoupled from GoReleaser's changelog system — `--release-notes` flag broke with `changelog.disable: true` in newer GoReleaser v2).

GoReleaser config: `.goreleaser.yml` (version 2). Version injected via `-ldflags "-s -w -X main.version={{.Version}}"` on both binaries. Note: both jobs are in one workflow because tags pushed with `GITHUB_TOKEN` don't trigger other workflows.

Install script: `scripts/install.sh` — POSIX shell, detects OS/arch, downloads from GitHub Releases, verifies checksum, installs to `~/.local/bin/`.

## MCP Server

`quil mcp` subcommand exposes Quil as an MCP (Model Context Protocol) server over stdio. AI tools (Claude Desktop, VS Code, Cursor) spawn this as a child process and communicate via JSON-RPC 2.0.

Architecture: thin bridge between MCP JSON-RPC (stdio) and daemon IPC (socket). The MCP bridge connects to the daemon as another IPC client — same as the TUI.

MCP SDK: `github.com/modelcontextprotocol/go-sdk` (official SDK, v1.4+). Typed tool handlers with struct-based input schemas.

18 MCP tools: `list_panes`, `read_pane_output` (ANSI-stripped), `send_to_pane`, `get_pane_status`, `create_pane`, `send_keys` (named key sequences), `restart_pane`, `screenshot_pane` (VT-emulated text screenshot), `switch_tab`, `list_tabs`, `destroy_pane`, `set_active_pane` (TUI cooperation), `close_tui` (TUI cooperation), `get_notifications` (non-blocking; carries `data.excerpt` with the triggering lines), `watch_notifications` (blocking, replaces polling; optional `since_timestamp` closes the race-on-registration window), `dismiss_notifications` (ack handled events from the agent side), `get_memory_report` (per-tab totals + Go-heap + PTY RSS), `get_pane_memory` (single pane detail).

IPC request-response: `Message.ID` field (omitempty, backward compatible) correlates requests with responses. Daemon responds to the requesting connection when `ID` is set, broadcasts when empty.

Key files: `cmd/quil/mcp.go` (bridge + daemon connection), `cmd/quil/mcp_tools.go` (18 tool implementations), `cmd/quil/mcp_keys.go` (key name → escape sequence map), `cmd/quil/mcp_log.go` (per-pane interaction logging + two-layer redaction).

Bridge lifetime: `watchParentExit()` (`cmd/quil/parentwatch_windows.go` / `parentwatch_unix.go`, armed first thing in `runMCP`) ties the bridge to the AI client that spawned it. Stdin EOF is NOT a reliable termination signal on Windows — the MCP client spawns stdio servers concurrently and same-second siblings inherit each other's pipe handles, so after the client dies a sibling still holds the bridge's stdin write end and `server.Run` blocks forever (observed: 20 orphaned bridges accumulated over a week, in same-second spawn pairs, each holding a live IPC conn to the production daemon). Windows: `OpenProcess(SYNCHRONIZE)` on the parent + `WaitForSingleObject` in a goroutine → `os.Exit(0)` when the parent exits, with a PID-reuse guard (`parentHandleTrustworthy` — a real parent's creation time is ≤ the child's; an impostor wearing a reused PID is treated as parent-already-dead). Unix: 2 s `Getppid()` reparent poll as belt-and-suspenders (EOF is reliable there). Covers pane kill, pane restart, session restart, and client crash with zero daemon coupling — the pane's claude process IS the bridge's parent.

AI tool configuration:
```json
{"mcpServers": {"quil": {"command": "quil", "args": ["mcp"]}}}
```

## Key Conventions

### Scoped rules — where the detail lives

`.claude/CLAUDE.md` holds only what applies no matter which file you open. Everything
package-specific moved to `.claude/rules/*.md`, each gated by a `paths:` glob so it loads
**only** when you touch the matching code. Nothing was deleted — these are verbatim moves.

| Rule file | Loads when you touch | Covers |
|---|---|---|
| `remote-transport.md` | `internal/transport/`, `internal/remoteinstall/`, `cmd/quil/remote*.go`, `stdio.go`, `version_gate.go`, `tui/reconnect.go` | ssh dialer + `stdioConn`, remote-mode guards, reconnect/backoff/parking, `quil remote setup` |
| `remote-dialogs.md` | `daemon/browse*.go`, `daemon/discover.go`, `tui/browse_client.go`, `tui/discover_client.go`, `tui/remotetext.go` | daemon-side filesystem dialogs, single-flight slots, blocking-FS-call budget, remote-text sanitizing |
| `tui-dialogs.md` | `tui/dialog*.go`, `palette*.go`, `sessions.go`, `history.go`, `ctxmenu.go`, `editor*.go`, `notes.go`, `internal/panehistory/` | dialog system, command palette, context menu, resume picker, input history, editors/notes |
| `tui-rendering.md` | `tui/pane*.go`, `tab.go`, `layout.go`, `model.go`, `compose.go`, `selection.go`, `keymatch.go`, `keyspecs.go`, `oscfilter.go`, `internal/keymap/`, `internal/clipboard/` | tab bar, mouse routing, split-border drag, wheel forwarding, OSC filtering, cursor model, selection, action registry + dispatch tiers |
| `daemon-lifecycle.md` | `internal/daemon/`, `internal/ipc/`, `internal/persist/`, `internal/ringbuf/`, `cmd/quild/` | IPC queues, startup guards, readiness wait, restart/shutdown, restore, snapshots, ghost buffers, logging |
| `hooks-and-sessions.md` | `internal/claudehook/`, `opencodehook/`, `hookevents/`, `claudesessions/`, `tui/workstate.go`, `modelinfo.go` | hook producers, session-id rotation, hook-events pipeline, work-in-progress indicators |
| `windows-pty.md` | `internal/pty/`, any `*_windows.go`, `tui/consolefix*.go` | ConPTY + bundled OpenConsole, console-mode restore, window geometry, spawn-size healing |
| `plugins.md` | `internal/plugin/`, `gitdiscover/`, `kubediscover/`, `defaults/*.toml`, `tui/instances.go`, `overlay.go` | plugin schema + registry, instances, `discover`/`sessions` opt-ins, lazygit/k9s/lazysql |
| `auto-update.md` | `internal/update/`, `cmd/quil/update_apply.go`, `daemon/update.go`, `tui/update.go` | update check, staging, rename-aside swap + rollback |
| `projects.md` | `daemon/project.go`, `daemon/gitcache.go`, `daemon/worktree*.go`, `internal/gitinfo/`, `internal/gitworktree/`, `tui/project*.go`, `tui/worktree_client.go`, `sidebar.go`, `router.go`, `dialdest.go`, `attention.go` | projects above tabs, multi-daemon routing, runtime connect/disconnect, the project form, sidebar layout, git subsystem, worktree creation |
| `dev-environment.md` | *(always on)* | production-isolation rule — never touch the running production daemon |

**Adding to this file?** Ask: *does this apply when I open a file in a different package?*
If no, it belongs in a `paths:`-scoped rule, not here. `.claude/CLAUDE.md` is capped at
100,000 chars by `scripts/check-claude-md-size.sh`, which `dev.sh` runs on every build.

### Always-on invariants

These hold regardless of which file you open. Violating one breaks something.

- Platform-specific code uses `//go:build` tags (not `// +build`)
- ConPTY API: `conpty.New(width, height, flags)` — 3 args, uses `Spawn()`, reads/writes directly on ConPty object
- Bubble Tea v2 / Lipgloss v2 — import paths: `charm.land/bubbletea/v2`, `charm.land/lipgloss/v2`. View() returns `tea.View` struct (not string). KeyMsg is `tea.KeyPressMsg`. MouseMsg split into `tea.MouseClickMsg`, `tea.MouseWheelMsg`, `tea.MouseMotionMsg`, `tea.MouseReleaseMsg`. Clipboard via `internal/clipboard` (platform-native: Win32/pbcopy/xclip). Paste wraps in bracketed paste sequences (`\x1b[200~...\x1b[201~`). Mouse modifiers: `msg.Mod.Contains(tea.ModCtrl)`. Quit: `tea.Quit` (function value, not call)
- IPC protocol: 4-byte big-endian length prefix + JSON payload. Optional `ID` field for request-response correlation (MCP bridge). When `ID` is set, daemon responds to specific connection; when empty, broadcasts to all. `AttachPayload` carries an optional `CWD` field (omitempty) — the TUI sends `os.Getwd()` on attach so the daemon can spawn new panes/tabs in the client's directory rather than the daemon's frozen-at-spawn-time CWD. Stored in `Daemon.clientCWD` (atomic.Pointer for race-free cross-goroutine access) and consumed via `defaultCWD()` which validates with `os.Stat` + `EvalSymlinks` and falls back to the daemon's own `os.Getwd()` if the client value is empty/stale
- `.gitignore` uses root-anchored patterns (`/quil`, `/quild`) to avoid matching `cmd/` directories
- Pane layout uses a binary split tree (`LayoutNode` in `internal/tui/layout.go`) — each internal node has its own `SplitDir`, enabling mixed H/V splits (tmux-style). The tree is serialized to JSON and persisted in the daemon's `Tab.Layout` field for reconnect restoration
- Layout persistence: the TUI sends `MsgUpdateLayout` only when a tab's tree DIFFERS from the one the broadcast just reported (`diffLayouts`/`sendDiffedLayouts`, `internal/tui/model.go`); the same broadcast arm diffs pane sizes (`diffResizes`). Both are scoped to the broadcasting `Dest` — a broadcast is one daemon's full state and says nothing about another's tabs. Daemon stores the layout opaquely (no broadcast, to avoid a feedback loop). On reconnect, `applyWorkspaceState()` deserializes the tree and prunes missing panes. **The comparison is structural, never by bytes**: `MarshalLayout` emits a struct (declaration order) while `parseWorkspaceState` re-marshals a `map[string]any` (alphabetical order), so identical trees encode differently and a byte-diff matches only single-leaf tabs — which is invisible in a workspace that has none. Sending unconditionally is what overflowed the client's own 64-slot critical queue at 33 tabs + 36 panes and made the TUI close its connection and exit (2026-08-09); split-drag release still does a full sweep (`sendAllLayouts`/`resizeAllPanes`), where everything genuinely changed. The FIRST resize per pane is never suppressed (`Model.sizedOnce`, cleared by `armReattachReset`) because the daemon's own guard is `appliedCols/appliedRows`, which a PTY install zeroes
- Pane naming: `MsgUpdatePane` IPC message, `Pane.Name` field in daemon, Alt+F2 keybinding to rename active pane (mirrors F2 tab rename pattern)
- Shell integration: Daemon auto-injects OSC 7 + OSC 133 hooks via `internal/shellinit/` — bash (`--rcfile`), zsh (`ZDOTDIR`), PowerShell (`-File`), fish (native). Init scripts written to `~/.quil/shellinit/` at daemon startup. PTY `SetEnv()` passes env vars to child process. OSC 133 markers (`A` prompt start, `B` command start, `D;exitcode` command done) enable precise command completion detection for notification events
- Daemon detachment: `cmd/quil/proc_unix.go` and `proc_windows.go` supply `daemonSysProcAttr()` via build tags — mirrors the `internal/pty/` pattern. Unix uses `Setsid`, Windows uses `DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP`
- Daemon auto-start: TUI auto-starts `quild --background` if not running. `findDaemonBinary()` checks PATH then the executable's own directory. PID file at `~/.quil/quild.pid`, stale socket cleanup before spawn
- Daemon shutdown: `MsgShutdown` signals via channel (not `os.Exit`) so defers in `main()` run cleanly (PID file removal, log file close). `sync.Once` guards `close(d.shutdown)` against double-close panic
- Pane input pipeline, TUI side: ALL `MsgPaneInput` producers go through `Model.enqueueInput` (`internal/tui/model.go`) — never build and send one directly. It resolves the pane's owning `dest` synchronously on the Update goroutine (`destOfPane` walks `m.projects`, which Update rebuilds on every broadcast, so resolving it off-goroutine is a race AND a stale answer types into another machine's pane), then pushes onto the ordered `inputCh` that the single `inputForwarder` goroutine drains FIFO. Ordering is the whole point: `forwardInputBytes` used to return a `tea.Cmd` doing the send, and Bubble Tea runs every Cmd on its OWN goroutine with no inter-Cmd ordering — one goroutine per keystroke raced to the socket and adjacent characters swapped under CPU pressure (`image containers` → `iamg ecotniaesnr`; a permutation with zero loss is the signature). Keystrokes, wheel notches (`sendInputToPane`) and both paste paths share the queue, or one could overtake another on the same PTY stdin. Enqueue BLOCKS rather than drops when full — safe only because `forwardOne` recovers per entry, so the sole drainer cannot die and deadlock Update. Never forward ordered input through a `tea.Cmd`. Regression tests: `internal/tui/input_order_test.go`
- Pane input pipeline, daemon side: ALL PTY stdin writes go through a per-pane writer goroutine (`Pane.EnqueueInput`/`inputWriter` in `internal/daemon/session.go`, bounded 256-msg queue) — never `pane.PTY.Write` on a dispatch goroutine. A child that stops reading stdin (claude wedged post-compaction) fills the kernel PTY buffer and blocks Write forever; doing that on the conn's dispatch goroutine froze input for every pane (2026-06-11/12 incidents). Queue overflow drops input + emits an `input_blocked` sidebar event (30 s per-pane cooldown via `LastInputBlockedAt`). Similarly, `pane.PTY.Close()` (→ `cmd.Wait`, blocks until the child is reaped) must NEVER run under `sm.mu` — `DestroyTab`/`DestroyPane`/`ReplacePane` detach panes under the lock and close via `releasePanes` (async, off-lock); `handleRestartPaneReq` closes the old PTY in a goroutine. RWMutex writer-priority means one parked writer starves every reader (snapshot loop, attach, switch_tab, hook enrichment). `snapshotWatchdog` (daemon.go) dumps all goroutine stacks to the log when no snapshot completes for 2 min (10 min dump throttle) — the snapshot loop is the liveness canary. `startDaemon` (cmd/quil/main.go) points quild stderr at `$QUIL_HOME/quild.stderr.log` so SIGQUIT dumps/panics survive. Regression tests: `internal/daemon/wedge_regression_test.go` (`wedgedSession` fake with blocking Write/Close)
- Persistence: `internal/persist/` handles atomic file I/O — `snapshot.go` for workspace JSON (write `.tmp` → rotate to `.bak` → rename), `ghostbuf.go` for per-pane binary buffers. Both use temp+rename for crash safety. Ring buffers (`internal/ringbuf`) are fixed-allocation circular buffers; `Tail(n)` copies only the trailing window (excerpts) and `Gen()` exposes a mutation counter (snapshot change-detection).
- Output coalescing: `streamPTYOutput()` uses goroutine + 2ms timer to batch rapid PTY output before IPC broadcast, preventing visual tearing with interactive TUI tools
- Auto-recovery: deleting the last tab auto-creates a new "Shell" tab; deleting the last pane in a tab auto-creates a fresh pane
- Resume strategies: `cwd_only` (terminal), `rerun` (stripe, ssh), `preassign_id` (claude-code), `session_scrape`, `none`. Dispatched in `spawnPane()` with `restoring` flag

## Dev Mode

> **Production isolation rule:** the project owner runs Quil in production from `~/.quil/`. All development work on this repo **must** happen via dev mode — do not touch the production daemon, socket, PID, workspace, or any file under `~/.quil/`. See [`.claude/rules/dev-environment.md`](./rules/dev-environment.md) for the full rule, which includes the dev-mode workflow and the list of scripts (`kill-daemon`, `reset-daemon`, bare `./quil`) that are forbidden during development.

Run a separate dev instance alongside production using the dev build, `--dev` flag, or `QUIL_HOME`:

```bash
./quil-dev.exe                    # Recommended: auto dev mode + debug logging (no flags needed)
./quil --dev                      # Uses .quil/ in project root (gitignored)
./scripts/quil-dev.sh             # Shortcut — launches quil-dev (Linux/macOS)
./scripts/quil-dev.ps1            # Shortcut — launches quil-dev.exe (Windows PowerShell)
QUIL_HOME=/custom/path ./quil     # Arbitrary data directory
```

`QUIL_HOME` overrides `QuilDir()` — all derived paths (socket, PID, config, workspace, buffers, logs, shellinit) use the specified directory. The `[dev]` indicator appears in the status bar when active. The dev build (`quil-dev.exe`) bakes in `QUIL_HOME` and debug logging via ldflags — no flags or env vars needed.

## Developer Utilities

```bash
./scripts/kill-daemon.sh        # Force-stop daemon (Linux/macOS)
./scripts/kill-daemon.ps1       # Force-stop daemon (Windows PowerShell)
./scripts/reset-daemon.sh       # Stop daemon + wipe persisted state (Linux/macOS)
./scripts/reset-daemon.ps1      # Stop daemon + wipe persisted state (Windows PowerShell)
```

## Documents

Project docs are now organized as a navigable tree under `docs/` (with the index at `docs/README.md`). Only `README.md`, `CHANGELOG.md`, `CONTRIBUTING.md`, and `LICENSE` stay at the repo root.

- `README.md` — Landing page (install + 5-command quick start + Documentation table)
- `CHANGELOG.md` — Keep a Changelog format
- `CONTRIBUTING.md` — Branch / commit / PR conventions
- `docs/README.md` — Documentation index
- `docs/quick-start.md` — First-launch walkthrough
- `docs/installation.md` — All install paths (one-liner, Go, manual, build from source)
- `docs/features.md` — Feature catalog grouped by area
- `docs/keybindings.md` — Full keymap + customization syntax
- `docs/configuration.md` — `~/.quil/config.toml` reference
- `docs/mcp.md` — User-facing MCP guide (client wiring, all 18 tools, redaction model)
- `docs/plugin-reference.md` — TOML plugin schema (every field, every strategy, examples)
- `docs/troubleshooting.md` — Daemon won't start, MCP not detected, log file locations, reset
- `docs/architecture.md` — 25 ADRs (moved from root `ARCHITECTURE.md`)
- `docs/vision.md` — Project vision (moved from root `VISION.md`)
- `docs/prd.md` — Original v1 PRD, historical reference (moved from root `PRD.md`)
- `docs/roadmap.md` — Milestone status + planned work (moved from root `ROADMAP.md`)
- `docs/versioning.md` — SemVer policy (moved from root `VERSIONING.md`)
- `docs/plans/` — Historical implementation plans
- `docs/roadmap/` — Per-feature roadmap PRDs (done + planned)
- `docs/superpowers/` — Detailed plans + specs for large feature efforts

## Reference Source — opensrc

Dependency/reference source is cached at `~/.opensrc/` via [opensrc](https://opensrc.sh/) (global cache, nothing written into this repo). Read source inside other commands: `rg "pattern" $(opensrc path <package-or-owner/repo>)` or `cat $(opensrc path <owner/repo>)/path/to/file`. `opensrc path` fetches on cache miss, so the command form is self-healing.

Cached reference repos:
- `jesseduffield/lazygit` — TUI-patterns reference (panel layout, keybinding dispatch); its user docs live in `docs/` (e.g. `docs/Config.md`, `docs/keybindings/`)

## Milestones

| Milestone | Status | Summary |
|---|---|---|
| M1 | Done | Foundation — daemon, TUI, IPC, PTY, tabs, splits, shell integration, mouse, scrollback, lifecycle |
| M2 | Done | Persistence — workspace snapshots, ghost buffers, shell respawn, reboot-proof sessions |
| M3 | Done | Resume engine — `preassign_id`, `session_scrape`, `rerun` strategies |
| M4 | Done | Plugin system — typed panes, TOML plugins, registry, error handlers, Ctrl+N dialog, 8 built-ins |
| M5 | **In progress** | Polish — setup dialog, spatial nav, clipboard image paste, leveled logger, log rotation, lazy restore, IPC backpressure, lazygit, split drag-resize, resume picker. Remaining: JSON transformer, encrypted tokens, OS service install |
| M6 | Done | Pane focus — Ctrl+E full-screen active pane |
| M7 | Done | Pane notes — Alt+E editor bound per pane, three save safety nets |
| M8 | Done | Bubble Tea v2 + Lipgloss v2 migration |
| M10 | Done | MCP server — `quil mcp`, 18 tools, request-response IPC via `Message.ID` |
| M11 | Done | Command palette — Alt+Shift+P, fuzzy find, unified content search |
| M12 | Done | Notification center — daemon event queue, per-pane mute, sidebar, 3 MCP tools |
| M13 | Done | Memory reporting — 5s collector, per-pane Go-heap + PTY RSS, dialog + 2 MCP tools |
| M14 | Done (v1.47.0) | Projects — grouping above tabs, sidebar with agent+git state, multi-daemon router, runtime host connect/disconnect. Deferred to their own plans: MCP project scoping, listening ports |

Full detail: `docs/roadmap.md` and `docs/roadmap/*.md`.
