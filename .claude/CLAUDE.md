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
- `internal/gitworktree/` — git worktree listing, creation + removal, and the on-demand `git status` count the close dialog warns with (the repository WRITES; kept apart from the read-only `gitinfo`, which a ticker runs)
- `internal/clipboard/` — Platform-native clipboard read/write (Win32 API, pbpaste/pbcopy, xclip/xsel)
- `internal/keymap/` — Keybinding action registry: canonical chords, the action table with its dispatch `Tier`, multi-chord sequence matching, layered resolution (defaults → preset → user overrides), and the embedded presets. Stdlib + `BurntSushi/toml` only — no `config`, no `tui`, and no `QuilDir()`, so the whole package tests without a `Model` or a `QUIL_HOME`. Path-derived reads live in `internal/config/bindings.go` (`bindings.toml`, migration off the legacy `[keybindings]` table)
- `internal/notify/` — Windows desktop toasts + `quil://` click-to-route activation. Split so every file with logic is platform-neutral (URI codec, toast XML, variant selection) and the `//go:build windows` files hold syscalls only — CI is Linux, so anything behind that tag is never compiled by `dev.sh test`. Raw COM/WinRT interop (no CGo, no new dependency), following `internal/clipboard`'s `NewProc` idiom
- `internal/debugserver/` — opt-in pprof listener for both binaries, started only when `QUIL_PPROF` names a port. Compiled into RELEASED builds deliberately: the workloads worth profiling have been running for days in production, and a dev build profiles an empty workspace. The address is refused unless it is literally loopback (a bare port becomes `127.0.0.1:<port>`; `:6060` and any hostname are errors), and `localhost` is rewritten to `127.0.0.1` in `Addr` so `net.Listen` never receives a NAME — otherwise the resolver, not the validator, picks the bind address, which is precisely what "a hostname is never resolved" claims to prevent. **What a profile exposes is narrower than it looks and the guards' rationale must not overstate it**: Go's heap profile is a SAMPLED ALLOCATION profile (call stacks + byte counts), not a memory dump, so it carries no terminal buffer contents and `net/http/pprof` exposes nothing that dumps heap memory. The real leaks are `/debug/pprof/cmdline` (full argv — for `--remote` that names the destination host) and `/debug/pprof/goroutine?debug=2` (stacks with pointer words and absolute paths). The listener is UNAUTHENTICATED and loopback is a machine boundary, not a user boundary — the IPC socket is chmod 0600 and there is no TCP equivalent — so any local account can read the above while the port is open. `?seconds=` is clamped (`maxProfileSeconds`): `net/http/pprof` applies no ceiling and its own deadline helper is inert while `WriteTimeout` is zero, which it deliberately is here, and `runtime/pprof` refuses a second concurrent CPU profile — so one unbounded request would deny the operator the profiling this package exists to provide. Handlers sit on a private mux; importing `net/http/pprof` still pollutes `http.DefaultServeMux` via its `init`, which is inert only because nothing in quil serves on it. Fetch and analyse with `scripts/pprof.sh` / `scripts/pprof-view.sh` — two steps because the host has no Go toolchain and a container's `127.0.0.1` is the container
- `internal/sandbox/` — the docker sandbox's mount set and container paths. Pure path arithmetic except `docker.go`, deliberately: the mount set IS the security boundary, and a boundary that needs a Docker daemon to test is a boundary nobody tests
- `internal/claudetoken/` — runs `claude setup-token` under a PTY and reads the token out of its output. Nothing here persists it; the caller hands it to the OS
- `internal/userenv/` — writes one persistent, user-scoped environment variable (`HKCU\Environment` on Windows, `ErrUnsupported` elsewhere). Same platform split as `internal/notify`: logic files are neutral, the `//go:build windows` file holds syscalls only
- `internal/tui/` — Bubble Tea model, tabs, panes, layout tree, styles, text selection, notification sidebar

Deep package notes — `internal/transport/`, `internal/pty/`, `internal/ipc/`, `internal/claudehook/`,
`internal/claudesessions/`, `internal/opencodehook/`, `internal/codexhook/`, `internal/hookevents/` — live in the scoped
rules under `.claude/rules/` (table below). They load automatically when you open a file in those
packages, so they cost nothing on unrelated work.

## Building

Go and make are NOT installed locally. Use `scripts/dev.sh` (Docker-based):

```bash
./scripts/dev.sh build          # Build prod, dev, debug pairs FOR THIS HOST (+ quil-activate.exe on Windows)
./scripts/dev.sh test           # Run tests
./scripts/dev.sh test-race      # Tests with race detector (CGo — handled automatically)
./scripts/dev.sh vet            # Lint
./scripts/dev.sh cross          # Cross-compile all platforms
./scripts/dev.sh image          # Build scratch-based Docker image
./scripts/sandbox-image.sh      # Build the image sandbox PANES run in (`--with codex,opencode`, `--check`)
./scripts/dev.sh clean          # Remove built binaries
./scripts/dev.sh docs-size      # Check .claude/ agent-context files against size limits
```

`build` runs `docs-size` first (`scripts/check-claude-md-size.sh`, host-side, no Docker) and
refuses to build when `.claude/CLAUDE.md` exceeds 100,000 bytes or a rule file exceeds 140,000.
The harness truncates an over-limit file silently, from the TAIL — so the symptom is not "the
file is too big", it is "the thing documented last week is the one Claude does not know".
There is deliberately no override flag, for the same reason `refuse_if_binaries_held` has none.

### Build Variants

**`build` targets the HOST platform, not Windows.** `TARGET_GOOS`/`TARGET_GOARCH` come
from `uname` and are overridable with `QUIL_BUILD_GOOS`/`QUIL_BUILD_GOARCH`
(`QUIL_BUILD_GOOS=windows ./scripts/dev.sh build` restores the old behaviour from any
host). On a non-Windows target the entire Windows prologue is SKIPPED, not merely
unused: `fetch-conpty.sh`, both `go-winres` invocations and `quil-activate.exe` are each
gated behind `//go:build windows` in the tree, so their output cannot be linked into a
darwin or linux binary, and each costs a network fetch. The names lose the `.exe` —
`.gitignore` already lists both spellings of all six, and `findDaemonBinary` appends
`.exe` itself on Windows, so `-X main.daemonBinary=` stays suffix-less on every target.
It hardcoded `GOOS=windows` until September 2026, which left every macOS and Linux
checkout holding seven executables that could not run on the machine that built them —
and the unsigned, console-less `quil-activate.exe` among them is the exact shape a
static-AI endpoint scanner quarantines. `cross` is deliberately unchanged: emitting
every platform is what it is for. `clean` now iterates `BUILT_BINARIES` rather than a
hand-written list, which is why a native `quil-dev` used to survive it.

`build` produces three matched pairs via compile-time ldflags (`.exe` on Windows only):

| Variant | TUI | Daemon | Behavior |
|---------|-----|--------|----------|
| **prod** | `quil.exe` | `quild.exe` | Stripped (`-s -w`), normal behavior |
| **dev** | `quil-dev.exe` | `quild-dev.exe` | Auto dev mode (`QUIL_HOME=.quil/`), debug logging, finds `quild-dev` |
| **debug** | `quil-debug.exe` | `quild-debug.exe` | Debug logging, connects to production `~/.quil/`, finds `quild-debug` |

Ldflags: `buildDevMode` (auto-sets `QUIL_HOME`), `buildLogLevel` (overrides config log level), `daemonBinary` (daemon binary name for `findDaemonBinary`). Dev variant is self-contained — just run `./quil-dev.exe`, no flags needed.

**`quil-activate.exe` is a seventh binary and deliberately variant-agnostic** — one copy serves prod, dev and debug, because the two things it cannot derive (the URI scheme and `QUIL_HOME`) are written INTO the registry command by `quil notify setup`. It is linked `-H windowsgui`: Windows gives a console binary a console WINDOW for every toast click, which appears in front of the user, takes the foreground, and is destroyed when the handler exits — leaving focus on a window that no longer exists. `FreeConsole` is not sufficient, because the console is created during process startup before any Go code runs. `quil notify setup` registers this binary when it is present beside the quil binary and falls back to `quil activate` when it is absent, so an in-place upgrade cannot leave the registry naming a file that does not exist.

Go module cache is persisted in a Docker volume (`quil-gomod`) for fast repeated builds.

`build` and `clean` refuse to run while any binary they would write is held by a running process (`refuse_if_binaries_held` in `scripts/dev.sh`). Neither platform can overwrite a running executable — Windows fails the open with a sharing violation, Linux returns ETXTBSY — and the ORDER is what makes it dangerous rather than the failure: the six builds are chained with `&&`, so a holder on the Nth leaves the first N-1 freshly built and the rest stale, and a new TUI beside a stale daemon fails the version gate at launch, reading as a bug in whatever you were working on. Detection is a **non-destructive probe, not a process query**: `(exec 3>>"$target")` asks the OS the exact question the build is about to ask, and append never truncates. The first version instead read `.quil/quild.pid` and looked for a daemon — it missed a dev TUI holding `quil-dev.exe` and let the half-build through on its first real use, which is why this checks the condition rather than a proxy for it. Only files in the project directory are probed, so a production install elsewhere is never touched, and a stale or malformed pid file cannot block a build because no pid file is consulted. There is deliberately no override flag: "build anyway" produces exactly the mismatched set it exists to prevent.

### Windows Icon

`build` and `cross` embed the Quil brand mark as a Windows executable icon via `go-winres` (v0.3.3). Build assets live in `winres/` (icon PNGs + `winres.json` manifest with `RT_GROUP_ICON` + `RT_VERSION`). The build script installs `go-winres` inside the Docker container and generates `.syso` files in `cmd/quil/` and `cmd/quild/` before `go build`. The Go linker picks up `.syso` files automatically (Windows only — ignored on Linux/Darwin). Generated `.syso` files are gitignored.

## Release Process

Single workflow (`release.yml`) with two jobs:

1. **`release` job** — triggers on push to master. Analyzes conventional commits since last tag, computes version bump (major/minor/patch), updates `VERSION`, promotes the `changelog.d/` fragments into `CHANGELOG.md`, commits `chore(release): vX.Y.Z`, creates git tag, pushes. Outputs version to the next job.
2. **`goreleaser` job** — runs after `release` job. Checks out the tagged commit, extracts release notes from `CHANGELOG.md` via sed, runs GoReleaser to cross-compile 5 platforms (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64), creates `.tar.gz` (Unix) / `.zip` (Windows) archives with both `quil` + `quild`, publishes GitHub Release with SHA256 checksums. Release notes applied via `gh release edit` (decoupled from GoReleaser's changelog system — `--release-notes` flag broke with `changelog.disable: true` in newer GoReleaser v2).

GoReleaser config: `.goreleaser.yml` (version 2). Version injected via `-ldflags "-s -w -X main.version={{.Version}}"` on both binaries. Note: both jobs are in one workflow because tags pushed with `GITHUB_TOKEN` don't trigger other workflows.

**Changelog entries are per-PR fragments, never direct edits to `CHANGELOG.md`.** Each PR adds one `changelog.d/<type>-<slug>.md` holding the bullet text; `scripts/promote-changelog.sh` collects them into a version section at release and deletes them. Every PR used to edit the same `## [Unreleased]` anchor line, so two open PRs conflicted the moment the first merged — git has no conflict concept for two distinct ADDED paths, which is the whole point. The grammar of a valid fragment name lives ONLY in that script: `ci.yml`'s PR gate pipes candidates through `--filter-names` and `release.yml` gates with `--check`, because a gate that can disagree with the action it guards is how #130 turned master red. `## [Unreleased]` is retained as a static anchor so the goreleaser job's release-note extraction sed is untouched. `changelog.d/` is in BOTH denylists — a typo fix in a pending fragment must not cut a release of byte-identical binaries. `release.yml` stages consumed fragments with a PATH-SCOPED `git add -A -- changelog.d`; a plain add records no deletion and re-promotes them forever. Regression tests: `scripts/test-promote-changelog.sh` (run by `ci.yml`).

**Each fragment also carries a one-line `headline:` front-matter block**, which the promoter strips from the prose and appends to `internal/changelog/highlights.txt` — a line-oriented file the binary `go:embed`s and the TUI renders in the post-upgrade What's New dialog (`internal/tui/whatsnew.go`, also reachable from F1 → What's New). Deliberately not JSON: a headline is single-line by definition, so newline is the only delimiter and the POSIX-sh writer never needs escaping logic — which is also why the headline charset forbids `"` and `\`. Every release writes a `V` record even when its only fragment was `none-*`, because the dialog header counts releases crossed and the F1 path walks the record list. Nothing is backfilled: the file starts empty, so a version is either present with complete data or absent entirely. **`release.yml` names that file EXPLICITLY in the release commit (`:352`)** — there is no bare `git add -A` in that job, only an explicit list plus the path-scoped one above, so an unnamed generated file is written into the CI workspace and silently discarded, and the tagged commit goreleaser checks out would embed a file that never grows. No test can catch that: the release job's own `go test` runs against the workspace copy, which IS updated. The duplicate-version guard lives in `check()`, before `CHANGELOG.md` is rewritten, so a refusal cannot leave two sections for one version. The marker the dialog compares against is `lastrun.json`, NOT `notified.json` — "a version you ran" versus "a version I told you about"; conflating them would let dismissing an update offer suppress the what's-new for a version never installed.

Install script: `scripts/install.sh` — POSIX shell, detects OS/arch, downloads from GitHub Releases, verifies checksum, installs to `~/.local/bin/`.

## MCP Server

`quil mcp` subcommand exposes Quil as an MCP (Model Context Protocol) server over stdio. AI tools (Claude Desktop, VS Code, Cursor) spawn this as a child process and communicate via JSON-RPC 2.0.

Architecture: thin bridge between MCP JSON-RPC (stdio) and daemon IPC (socket). The MCP bridge connects to the daemon as another IPC client — same as the TUI.

MCP SDK: `github.com/modelcontextprotocol/go-sdk` (official SDK, v1.4+). Typed tool handlers with struct-based input schemas.

35 MCP tools: `list_panes` (marks the caller's own pane `self`, reports `agent_state`), `read_pane_output` (ANSI-stripped), `send_to_pane` (`paste` for multi-line), `get_pane_status`, `create_pane` (the dialog's options: `name`, `toggles` by NAME, `resume_session_id`, `worktree_branch`, `sandbox_image`/`sandbox_auth`), `send_keys` (named key sequences), `restart_pane`, `screenshot_pane` (VT-emulated text screenshot), `switch_tab`, `list_tabs`, `destroy_pane`, `set_active_pane` (TUI cooperation), `close_tui` (TUI cooperation), `get_notifications` (non-blocking; carries `data.excerpt` with the triggering lines), `watch_notifications` (blocking, replaces polling; optional `since_timestamp` closes the race-on-registration window), `dismiss_notifications` (ack handled events from the agent side), `get_memory_report` (per-tab totals + Go-heap + PTY RSS), `get_pane_memory` (single pane detail); projects and tabs: `list_projects`, `create_project`, `update_project`, `switch_project`, `destroy_project`, `create_tab` (any first pane, in a named project, no focus steal), `create_from_template` (ordered panes, frozen args and prompts from templates.toml), `rename_tab`, `destroy_tab`, `rename_pane`; discovery: `list_plugins` (toggle names, `sandbox_available`), `list_sessions`, `list_hosts`; tasking: `delegate_task`, `get_task`, `wait_task`, `list_tasks`.

IPC request-response: `Message.ID` field (omitempty, backward compatible) correlates requests with responses. Daemon responds to the requesting connection when `ID` is set, broadcasts when empty. **The six project mutations, `update_tab`, `destroy_tab` and `update_pane` answer an `OpRespPayload` ONLY when the request carries an ID** (`answerOp`, `internal/daemon/project_req.go`) — the TUI sets none and keeps getting nothing; answering its id-less sends would put a critical frame per keystroke-class message on its 64-slot queue, the 2026-08-09 disconnect shape.

**`create_pane_req` never takes raw arguments from an agent** — `toggles` are plugin toggle NAMES resolved by `resolveToggles` (`internal/daemon/create_req.go`), because `InstanceArgs` REPLACE `Command.Args` and a free-form list lets any IPC client run any program under a plugin's name. Unknown names and two names from one group are refused, never dropped: an unattended pane without the permission mode it asked for is a pane stuck on a prompt nobody answers. `worktree_branch` has its repo root resolved daemon-side from the CWD (a client-built path nests a worktree inside a checkout). Everything lands on the same `CreatePanePayload` the TUI sends, so the daemon's own validation (`applySandboxSpec`, `applyResumeSessionID`, `worktreeAddAndCreate`) is the guard — which is why the "the MCP bridge deliberately does not expose this field" notes on resume/worktree/sandbox were reversed; overlay stays TUI-only.

**The daemon now holds per-pane agent work state** (`Pane.Work`, a `hookevents.WorkLedger`, `internal/daemon/workstate.go`), fed from `emitEvent` AHEAD of the mute and heartbeat bypasses so it sees every edge the TUI's spinner sees. `internal/tui/workstate.go` keeps its own copy of the derivation because that one is entangled with render state; the pure core moved to `internal/hookevents/ledger.go` and the two switches must stay in step. `agent_idle` is queued only after the falling edge has held for `agentIdleSettle` (2 s): the raw `Stop` fires while background subagents run, and Claude resumes on its own when a teammate reports back. **`AgentState` empty means UNKNOWN, not idle** — a terminal pane, or an AI pane whose hooks never loaded.

**Tasks (`internal/daemon/task.go`)** are runtime-only and bounded (200, oldest TERMINAL evicted — a live task has a waiter parked on it). A prompt to an AI pane is a bracketed paste, then `\r` after `pasteSettle`; a terminal gets `text\n`. `done` = the target's ledger settled idle (through `onPaneIdle`); `failed` = `process_exit`; a terminal target completes on `command_complete`. The notify-back is typed into the requester only while it is not mid-turn (`onPaneIdle` again), and only when both panes share a daemon — the bridge blanks `from_pane` for a remote target.

**The bridge routes to `[[destinations]]` hosts** (`cmd/quil/mcp_hosts.go`, `mcpRouter`): one `mcpBridge` per host, dialled in the background with `dialRemoteTransportFn` (batch) + `gateExtraVersion` + the bridge hello, re-dialled lazily with a 30 s backoff (a LOST link earns one immediate retry; the backoff is for a host that refused). Resolution: explicit `host` → the id→host cache every list/create fills → local. `mcpBridge.dead` is set when `readLoop` exits so a dropped link is detected before the next request times out. The bridge learns its own pane from `QUIL_PANE_ID`, which the daemon sets on every AI pane's child. **Every bridge probes its daemon's version at dial (`probeDaemonVersion`, before the read loop owns the connection), and tools that send the request types added in this branch call `requireDaemon` first** (`cmd/quil/mcp_version.go`, floor `mcpDaemonMinVersion` = 1.72.0): an older daemon drops an unknown message type SILENTLY, so a `list_projects` at a remote still on 1.71.0 read as a 10 s timeout rather than "that host is not upgraded" (measured 2026-09-10). The TUI's `versionHandshakeWithin` cannot serve here — it skips itself for a non-release client, and the dev bridge needs the number precisely then. Unknown / unparseable versions pass; only a release below the floor is refused. **Unscoped aggregation skips a failing REMOTE and records the error on the host (`forEachHost`, `hostConn.reqErr` → `list_hosts` `error`)**; a named host or the local daemon failing still fails the call.

Key files: `cmd/quil/mcp.go` (bridge + daemon connection, server instructions), `cmd/quil/mcp_hosts.go` (host router), `cmd/quil/mcp_tools.go` (workspace and interaction tools, now host-aware), `cmd/quil/mcp_tools_projects.go` (projects, tabs, hosts, catalog), `cmd/quil/mcp_tools_tasks.go` (delegation), `cmd/quil/mcp_keys.go` (key name → escape sequence map), `cmd/quil/mcp_log.go` (per-pane interaction logging + two-layer redaction); daemon side `internal/daemon/create_req.go`, `project_req.go`, `workstate.go`, `task.go`.

Bridge lifetime: `watchParentExit()` (`cmd/quil/parentwatch_windows.go` / `parentwatch_unix.go`, armed first thing in `runMCP`) ties the bridge to the AI client that spawned it. Stdin EOF is NOT a reliable termination signal on Windows — the MCP client spawns stdio servers concurrently and same-second siblings inherit each other's pipe handles, so after the client dies a sibling still holds the bridge's stdin write end and `server.Run` blocks forever (observed: 20 orphaned bridges accumulated over a week, in same-second spawn pairs, each holding a live IPC conn to the production daemon). Windows: `OpenProcess(SYNCHRONIZE)` on the parent + `WaitForSingleObject` in a goroutine → `os.Exit(0)` when the parent exits, with a PID-reuse guard (`parentHandleTrustworthy` — a real parent's creation time is ≤ the child's; an impostor wearing a reused PID is treated as parent-already-dead). Unix: 2 s `Getppid()` reparent poll as belt-and-suspenders (EOF is reliable there). Covers pane kill, pane restart, session restart, and client crash with zero daemon coupling — the pane's claude process IS the bridge's parent.

AI tool configuration:
```json
{"mcpServers": {"quil": {"command": "quil", "args": ["mcp"]}}}
```

## Sandbox panes

`quil sandbox login|status` — the manual entry point to the Claude token flow a
sandbox pane otherwise runs on the user's behalf. `login` drives Anthropic's own
`claude setup-token` under a PTY, reads the token out of its output, saves it to
the OS user environment and restarts the daemon; `status` says which auth mode
is in effect and whether a token is present in this process and persisted.

**Quil never keeps its own copy of a Claude credential** — `setup-token` mints
one for exactly this purpose, and Anthropic's authentication policy speaks
directly to intermediating theirs. Codex is the deliberate exception and a
different posture: it ships no equivalent command, so its `~/.codex/auth.json`
is COPIED into the pane's own `CODEX_HOME`, which is what its own users do for
containers.

`QUIL_SANDBOX_QUILD` overrides the Linux hook binary a container mounts, for
development. Full detail in `.claude/rules/sandbox.md` and
`docs/sandbox-panes.md`.

## Key Conventions

### Scoped rules — where the detail lives

`.claude/CLAUDE.md` holds only what applies no matter which file you open. Everything
package-specific moved to `.claude/rules/*.md`, each gated by a `paths:` glob so it loads
**only** when you touch the matching code. Nothing was deleted — these are verbatim moves.

| Rule file | Loads when you touch | Covers |
|---|---|---|
| `templates.md` | `config/templates*`, `daemon/template*.go`, `daemon/mcp_spawn*.go`, `daemon/worktree_add.go`, `ipc/template.go`, `tui/template*.go`, `cmd/quil/mcp*` | template validation, frozen arguments, ordinary MCP adapters, completed-frame layouts, input isolation and TOML settings |
| `remote-transport.md` | `internal/transport/`, `internal/remoteinstall/`, `cmd/quil/remote*.go`, `stdio.go`, `version_gate.go`, `tui/reconnect.go` | ssh dialer + `stdioConn`, remote-mode guards, reconnect/backoff/parking, `quil remote setup` |
| `remote-dialogs.md` | `daemon/browse*.go`, `daemon/discover.go`, `tui/browse_client.go`, `tui/discover_client.go`, `tui/remotetext.go` | daemon-side filesystem dialogs, single-flight slots, blocking-FS-call budget, remote-text sanitizing |
| `tui-dialogs.md` | `tui/dialog*.go`, `palette*.go`, `sessions.go`, `history.go`, `ctxmenu.go`, `editor*.go`, `notes.go`, `internal/panehistory/` | dialog system, command palette, context menu, resume picker, input history, editors/notes |
| `tui-rendering.md` | `tui/pane*.go`, `tab.go`, `layout.go`, `model.go`, `compose.go`, `selection.go`, `keymatch.go`, `keyspecs.go`, `oscfilter.go`, `internal/keymap/`, `internal/clipboard/` | tab bar, mouse routing, split-border drag, wheel forwarding, OSC filtering, cursor model, selection, action registry + dispatch tiers |
| `daemon-lifecycle.md` | `internal/daemon/`, `internal/ipc/`, `internal/persist/`, `internal/ringbuf/`, `cmd/quild/` | IPC queues, startup guards, readiness wait, restart/shutdown, restore, snapshots, ghost buffers, logging |
| `hooks-and-sessions.md` | `internal/claudehook/`, `opencodehook/`, `codexhook/`, `hookevents/`, `claudesessions/`, `tui/workstate.go`, `modelinfo.go` | hook producers, session-id rotation, hook-events pipeline, work-in-progress indicators |
| `windows-pty.md` | `internal/pty/`, any `*_windows.go`, `tui/consolefix*.go` | ConPTY + bundled OpenConsole, console-mode restore, window geometry, spawn-size healing |
| `plugins.md` | `internal/plugin/`, `gitdiscover/`, `kubediscover/`, `defaults/*.toml`, `tui/instances.go`, `overlay.go` | plugin schema + registry, instances, `discover`/`sessions` opt-ins, the shared overlay slot, lazygit/hunk/k9s/lazysql |
| `sandbox.md` | `internal/sandbox/`, `daemon/sandbox*.go`, `tui/sandbox*.go` | docker sandbox panes — the mount set as a security boundary, the per-pane object store and its ordered teardown, refusals that never soften |
| `auto-update.md` | `internal/update/`, `cmd/quil/update_apply.go`, `daemon/update.go`, `tui/update.go` | update check, staging, rename-aside swap + rollback |
| `projects.md` | `daemon/project.go`, `daemon/gitcache.go`, `daemon/worktree*.go`, `internal/gitinfo/`, `internal/gitworktree/`, `tui/project*.go`, `tui/worktree_*.go`, `sidebar.go`, `router.go`, `dialdest.go`, `attention.go` | projects above tabs, multi-daemon routing, runtime connect/disconnect, the project form, sidebar layout, git subsystem, worktree creation + close-time removal |
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
- **Every `MsgResizePane` this client produces is gated on `Model.terminalPaintable()`** (`m.width >= minTermWidth && m.height >= minTermHeight`) — `resizeAllPanes`, `diffResizes` and `overlayResizeCmd` are the only three producers, and `attachMessage` reports 0x0 below that threshold so `handleAttach`'s own 80x24 default stands. The gate belongs at the FAN-OUTS: `paneVTSize` floors both dimensions at 1 on purpose for genuinely narrow SPLIT panes, so a degenerate geometry is indistinguishable from a legal one by the time it reaches the wire. A console-less client attaches at 1x1 (Bubble Tea reports that for a process with no console), and without the gate that resized every pane in a 48-tab workspace to one column and permanently re-wrapped every child's transcript — observed 2026-08-25 and 2026-09-05. It sits AHEAD of every `sizedOnce` write, so each pane is still owed its first-resize kick once a usable geometry arrives. `handleResizePane` repeats the refusal daemon-side, but only when BOTH dimensions are at the floor together — a narrow split is narrow in one dimension and wide in the other. Regression tests: `internal/tui/tinyterm_test.go` (driven through `Update`, because the defect was that the call sites were unconditional), `internal/daemon/resize_guard_test.go`
- Pane restart boundary: `PaneOutputPayload.Generation` carries the daemon's PTY run counter on every live chunk. The TUI resets its VT before a replacement run's output and drops older frames; reattach forgets the counter because the daemon may have restarted. `newPaneSession` picks sibling/client dimensions before spawn, rejecting a degenerate client size via `degenerateSize` and falling back to 80x24. Keep the IPC fast-frame decoder and its wire-shape test in sync with this payload.
- Pane naming: `MsgUpdatePane` IPC message, `Pane.Name` field in daemon, Alt+F2 keybinding to rename active pane (mirrors F2 tab rename pattern)
- Hand-started agents (#221): typing `claude`/`codex`/`opencode` at a terminal pane's shell opens the pane as that agent. The seam is a shell FUNCTION that shadows the binary, not a hook on a running process — **Quil never attaches to a running agent**. Detail in [`.claude/rules/daemon-lifecycle.md`](./rules/daemon-lifecycle.md)
- Shell integration: Daemon auto-injects OSC 7 + OSC 133 hooks via `internal/shellinit/` — bash (`--rcfile`), zsh (`ZDOTDIR`), PowerShell (`-File`), fish (native). Init scripts written to `~/.quil/shellinit/` at daemon startup. PTY `SetEnv()` passes env vars to child process. OSC 133 markers (`A` prompt start, `B` command start, `D;exitcode` command done) enable precise command completion detection for notification events. **`D` is emitted only after a command actually ran** (bash/zsh: preexec set a flag; pwsh: the history id moved) — every script writes `D` BEFORE `A` in the prompt, so the startup prompt used to report a "Command completed" for nothing, and a task delegated to a fresh terminal completed on it. bash's `DEBUG` trap fires for every piece of `PROMPT_COMMAND` too, which is why `__quil_arm` is its LAST piece and preexec counts only while armed. **Enter is CR, never LF**: LF executes in a Unix shell only because the tty maps it (ICRNL); PowerShell under ConPTY echoes an LF-terminated line and leaves it at the prompt (measured 2026-09-10 — every `send_to_pane` on Windows typed its command and never ran it). `send_to_pane`, `delegate_task` and `send_keys enter` all send `\r`
- Daemon detachment: `cmd/quil/proc_unix.go` and `proc_windows.go` supply `daemonSysProcAttr()` via build tags — mirrors the `internal/pty/` pattern. Unix uses `Setsid`, Windows uses `DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP`
- Daemon auto-start: TUI auto-starts `quild --background` if not running. `findDaemonBinary()` checks PATH then the executable's own directory. PID file at `~/.quil/quild.pid`, stale socket cleanup before spawn
- Daemon shutdown: `MsgShutdown` signals via channel (not `os.Exit`) so defers in `main()` run cleanly (PID file removal, log file close). `sync.Once` guards `close(d.shutdown)` against double-close panic
- Pane input pipeline, TUI side: ALL `MsgPaneInput` producers go through `Model.enqueueInput` (`internal/tui/model.go`) — never build and send one directly. It resolves the pane's owning `dest` synchronously on the Update goroutine (`destOfPane` walks `m.projects`, which Update rebuilds on every broadcast, so resolving it off-goroutine is a race AND a stale answer types into another machine's pane), then pushes onto the ordered `inputCh` that the single `inputForwarder` goroutine drains FIFO. Ordering is the whole point: `forwardInputBytes` used to return a `tea.Cmd` doing the send, and Bubble Tea runs every Cmd on its OWN goroutine with no inter-Cmd ordering — one goroutine per keystroke raced to the socket and adjacent characters swapped under CPU pressure (`image containers` → `iamg ecotniaesnr`; a permutation with zero loss is the signature). Keystrokes, wheel notches (`sendInputToPane`) and both paste paths share the queue, or one could overtake another on the same PTY stdin. Enqueue BLOCKS rather than drops when full — safe only because `forwardOne` recovers per entry, so the sole drainer cannot die and deadlock Update. Never forward ordered input through a `tea.Cmd`. Regression tests: `internal/tui/input_order_test.go`
- Pane input pipeline, daemon side: ALL PTY stdin writes go through a per-pane writer goroutine (`Pane.EnqueueInput`/`inputWriter` in `internal/daemon/session.go`, bounded 256-msg queue) — never `pane.PTY.Write` on a dispatch goroutine. A child that stops reading stdin (claude wedged post-compaction) fills the kernel PTY buffer and blocks Write forever; doing that on the conn's dispatch goroutine froze input for every pane (2026-06-11/12 incidents). Queue overflow drops input + emits an `input_blocked` sidebar event (30 s per-pane cooldown via `LastInputBlockedAt`). **An ID-BEARING `pane_input` is answered with `pane_input_resp`** (`paneInputOutcome`) saying whether the bytes were queued and, when not, which of the four refusals applied — no such pane, no process, a worktree still being prepared, or a full queue. Only id-bearing requests get one: the TUI sets no ID and sends one of these per keystroke, so a response per keystroke would be a frame per keystroke on that client's 64-slot must-deliver queue. It exists because the MCP `send_to_pane`/`send_keys` tools were reporting "Sent N bytes" for input the daemon had dropped, so an agent waited for output from a command that never ran. Delivered means QUEUED — the writer goroutine owns the PTY write precisely so a wedged child cannot block the answer. Similarly, `pane.PTY.Close()` (→ `cmd.Wait`, blocks until the child is reaped) must NEVER run under `sm.mu` — `DestroyTab`/`DestroyPane`/`ReplacePane` detach panes under the lock and close via `releasePanes` (async, off-lock); `handleRestartPaneReq` closes the old PTY in a goroutine. RWMutex writer-priority means one parked writer starves every reader (snapshot loop, attach, switch_tab, hook enrichment). `snapshotWatchdog` (daemon.go) dumps all goroutine stacks to the log when no snapshot completes for 2 min (10 min dump throttle) — the snapshot loop is the liveness canary. `startDaemon` (cmd/quil/main.go) points quild stderr at `$QUIL_HOME/quild.stderr.log` so SIGQUIT dumps/panics survive. Regression tests: `internal/daemon/wedge_regression_test.go` (`wedgedSession` fake with blocking Write/Close)
- Persistence: `internal/persist/` handles atomic file I/O — `snapshot.go` for workspace JSON (write `.tmp` → rotate to `.bak` → rename), `ghostbuf.go` for per-pane binary buffers. Both use temp+rename for crash safety. Ring buffers (`internal/ringbuf`) are fixed-allocation circular buffers; `Tail(n)` copies only the trailing window (excerpts) and `Gen()` exposes a mutation counter (snapshot change-detection).
- Output coalescing: `streamPTYOutput()` uses goroutine + 2ms timer to batch rapid PTY output before IPC broadcast, preventing visual tearing with interactive TUI tools
- Auto-recovery: deleting the last tab auto-creates a new "Shell" tab; deleting the last pane in a tab auto-creates a fresh pane — a MOVE is the exception: moving a tab's last normal pane to another tab destroys the source tab (and its overlays) instead of refilling it; `recoverEmptyProject` still runs
- New-tab first pane: `CreateTabPayload.FirstPane *FirstPaneSpec` (omitempty) names the pane a new tab opens with. NIL keeps the historical hardcoded `terminal`, which every non-interactive producer needs — an older client, and the three OTHER daemon sites that mint a tab-plus-pane (`daemon.go` attach bootstrap, `recoverEmptyProject`, `ensureTabNotEmpty`), all recovery invariants that must never block on a prompt. It is a NARROW type, not a `*CreatePanePayload`: that one also carries `TabID` (the daemon owns the id it is minting), `ReplacePaneID` (would destroy an arbitrary pane as a side effect of "create tab") and `Overlay` (a tab whose only pane is a muted overlay is a state `ensureTabNotEmpty` reads as empty and no create path repairs) — any IPC client can set those, so the guarantee is structural, per the `MergeProjectsPayload` precedent. `handleCreateTab` calls `constructPaneAt` (the non-broadcasting split of `createPaneAt`) so the tab and its pane reach clients as ONE frame — `createPaneAt` broadcasts and snapshots itself, so reusing it puts two full workspace-state frames back to back on the 64-slot must-deliver queue per new tab. The CWD fallback is `projectCWD`, NOT `handleCreatePane`'s `defaultCWD` (`resolveRequestedCWD` takes it as a parameter for exactly that reason): a submit that picks no directory would otherwise silently stop opening in the project root, a regression in the projects feature caused from inside the pane feature. A `Worktree` spec opens the tab with a PTY-LESS PLACEHOLDER — a real pane in the tab, spawning no child, carrying the branch on `Pane.PreparingWorktree` and rendering a spinner (`constructPreparingPane`; a failed add clears the branch and writes `SpawnError` in its place) — and swaps in the requested pane via the ordinary worktree REPLACE path (`createFirstPaneWorktree` → `worktreeAddAndCreate` with `ReplacePaneID`). It used to be a live `terminal`, which is indistinguishable from a create that FINISHED in the wrong tree, for the minutes a monorepo checkout takes; creating the tab and adding the worktree first would instead leave it pane-less for up to `worktreeAddTimeout`, broadcast blank and persisted that way by any snapshot in the window, with nothing to recover it; and spawning the REQUESTED type as the placeholder would start an agent in the main checkout, the isolation failure the worktree exists to prevent
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
- `CHANGELOG.md` — Keep a Changelog format; written by the release workflow, never by hand
- `changelog.d/` — pending per-PR changelog fragments (`README.md` documents the convention)
- `CONTRIBUTING.md` — Branch / commit / PR conventions
- `docs/README.md` — Documentation index
- `docs/quick-start.md` — First-launch walkthrough
- `docs/installation.md` — All install paths (one-liner, Go, manual, build from source)
- `docs/features.md` — Feature catalog grouped by area
- `docs/keybindings.md` — Full keymap + customization syntax
- `docs/configuration.md` — `~/.quil/config.toml` reference
- `docs/workspace-templates.md` — Template fields, layouts, prompts, creation, settings and adapter limits
- `docs/mcp.md` — User-facing MCP guide (client wiring, all 35 tools, redaction model)
- `docs/plugin-reference.md` — TOML plugin schema (every field, every strategy, examples)
- `docs/troubleshooting.md` — Daemon won't start, MCP not detected, log file locations, reset
- `docs/sandbox-panes.md` — Docker sandbox panes: building the image, signing in, what the sandbox does and does not bound
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
| M4 | Done | Plugin system — typed panes, TOML plugins, registry, error handlers, Ctrl+N dialog, 9 built-ins |
| M5 | **In progress** | Polish — setup dialog, spatial nav, clipboard image paste, leveled logger, log rotation, lazy restore, IPC backpressure, lazygit, split drag-resize, resume picker. Remaining: JSON transformer, encrypted tokens, OS service install |
| M6 | Done | Pane focus — Ctrl+E full-screen active pane |
| M7 | Done | Pane notes — Alt+E editor bound per pane, three save safety nets |
| M8 | Done | Bubble Tea v2 + Lipgloss v2 migration |
| M10 | Done | MCP server — `quil mcp`, 35 tools; projects, hosts and task delegation added September 2026; request-response IPC via `Message.ID` |
| M11 | Done | Command palette — Alt+Shift+P, fuzzy find, unified content search |
| M12 | Done | Notification center — daemon event queue, per-pane mute, sidebar, 3 MCP tools |
| M13 | Done | Memory reporting — 5s collector, per-pane Go-heap + PTY RSS, dialog + 2 MCP tools |
| M14 | Done (v1.47.0) | Projects — grouping above tabs, sidebar with agent+git state, multi-daemon router, runtime host connect/disconnect. Deferred to their own plans: MCP project scoping, listening ports |
| M17 | Partial | Desktop notifications — Windows toasts on the sidebar's attention states, `quil://` click-to-route via the windowless `quil-activate.exe`, `quil notify setup/status/test/--remove`, F1 → Settings → Notifications (enabled / blocked / done). Still open in M17: sound, macOS/Linux (no transport there carries a click back to a pane). **Raising the terminal window on click was built and REMOVED** — clicking a notification hands the foreground to the shell's notification host, which holds it (measured: 5.5 s for one click, >10 s for the next, released on user input rather than on a timer) and refuses every documented way of taking it back. Windows highlights the taskbar button instead. Do not rebuild this without new evidence: `SetForegroundWindow`, `AttachThreadInput` queue borrowing, `SwitchToThisWindow` and Chromium's F22 hotkey injection were each implemented and each verified refused |
| M19 | Done (unreleased) | Sandbox panes — an AI pane (claude-code, codex, opencode) inside a per-pane Docker container. **The mount set IS the boundary**: no `--privileged`, no `--cap-add`, no `--network`; the checkout read-write, `.git` mounted AS a mountpoint with `objects`/`hooks`/`config`/`config.worktree`/`modules`/`worktrees` pinned read-only on top, and new objects in a per-pane store with the repo's own as a read-only alternate. Auth is per-vendor and per-pane: Claude Code forwards `CLAUDE_CODE_OAUTH_TOKEN` BY NAME or signs in in-container (radio row in the create dialog; a token-mode pane with no token drives `claude setup-token` itself, single-flighted daemon-wide), codex gets its `auth.json` COPIED in, opencode signs in per container. Quil publishes and pulls NO image — `scripts/sandbox-image.sh` builds `docker/sandbox/Dockerfile` locally and verifies the result. See `.claude/rules/sandbox.md`, [ADR-31](../docs/architecture.md), `docs/sandbox-panes.md` |

Full detail: `docs/roadmap.md` and `docs/roadmap/*.md`.
