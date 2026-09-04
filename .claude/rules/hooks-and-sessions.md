---
description: Claude Code / OpenCode / Codex hooks, session-id rotation tracking, the hook-events pipeline, and the work-in-progress indicators derived from it. Load when touching hook producers, session discovery, or work state.
paths:
  - "**/internal/claudehook/**"
  - "**/internal/opencodehook/**"
  - "**/internal/codexhook/**"
  - "**/internal/hookevents/**"
  - "**/internal/claudesessions/**"
  - "**/internal/tui/workstate.go"
  - "**/internal/tui/modelinfo.go"
---

# Hooks and Sessions

Extracted verbatim from `.claude/CLAUDE.md`. Loaded only when the files above are in play.

## Hook packages

### `internal/claudehook/`

Claude Code multi-event hook (native subcommand, no scripts). `BuildSettingsJSON` registers Quil's hook command under 14 Claude events (SessionStart for session-id tracking + 13 forwarded to the JSONL spool: SessionEnd, UserPromptSubmit, Notification, PermissionRequest, Stop, StopFailure, PreToolUse, PreCompact, PostCompact, SubagentStart/Stop, TaskCreated/TaskCompleted). The hook command is `HookCommand(exePath)` → `"<quild>" claude-hook` — the daemon passes `os.Executable()`. `RunHook` (runhook.go) is the handler: reads the hook JSON on stdin, branches on `hook_event_name`, and either writes the session id file (SessionStart) or appends a `hookevents.Payload` JSONL line via `encoding/json` (no hand-rolled escaping; eliminates the BOM/codepage bug class the old `.ps1`/`.sh` had). Wired in `cmd/quild/main.go` as a fast-path subcommand that never starts the daemon — replaces the per-event PowerShell/sh spawn (~1-4 s cold start) with a native Go process (~tens of ms). `claudeHookSpawnPrep` (daemon.go) builds the settings JSON from `quildExeFn` (≈`os.Executable`, injectable for tests; named for the binary because the codex prep reads the same seam), writes it to a per-pane FILE via `claudehook.WriteSettingsFile` (`$QUIL_HOME/sessions/<paneID>.settings.json`, 0600, cleaned up with the `.id`/`.transcript` artifacts) and passes `--settings <path>` — never the JSON as an argv token. On Windows `claude` resolves to an npm `.cmd` shim that cmd.exe re-parses with different quoting rules than the process spawner already quoted for, so an inline JSON was re-split at the wrong quote boundaries and the hook silently never registered; a path carries no shell metacharacters. A write failure logs and disables rotation tracking rather than failing the spawn and sets PTY env `QUIL_PANE_ID` + `QUIL_HOOK_MODE` (`"default"|"verbose"|"off"`) + `QUIL_HOOK_HOME` (renamed from `QUIL_HOME`; consumers fall back to `QUIL_HOME` for one release). `ReadPersistedSessionID` consults `config.SessionsDir()/<paneID>.id` on restore. (OpenCode still uses an embedded JS plugin — see `internal/opencodehook/`.)

### `internal/claudesessions/`

Pure, stdlib-only discovery of the Claude Code transcripts recorded for a CWD (sibling of `gitdiscover`/`kubediscover`). `ConfigDir()` resolves Claude's config directory — `$CLAUDE_CONFIG_DIR` when set (`~`-expanded and absolutised), else `~/.claude` — and `ProjectDirIn(configDir, cwd)` joins `<configDir>/projects/<escaped>`; `ProjectDir`/`List`/`ReadDetail` delegate through it, so ignoring the env var used to list an empty picker for anyone who relocates that directory. Resolution is DAEMON-side: the directory describes the daemon's disk, and under `--remote` a client-supplied path would be the laptop's answer about the server's. `EscapeCWD` is the single definition of Claude's `<config-dir>/projects/<escaped>` naming rule, transcribed from the claude binary (per UTF-16 code unit; >200 chars truncates + base36 Java-31x hash of the ORIGINAL cwd) — **moved here from `daemon.escapeClaudeCWD`**, which now consumes `TranscriptPath` for its restore probe, so the rule that silently breaks restore when wrong exists once. `List(cwd)` enumerates `*.jsonl` (regular files only — claude also keeps sibling DIRECTORIES named after session uuids), sorts mtime-desc, caps at `MaxSessions` (200), then head-reads each survivor up to `titleScanBytes` (64 KiB) for the first `"promptSource":"typed"` non-sidechain user entry and uses its text as the title (`message.content` is decoded as either a bare string or a content-block array). `sanitizeTitle` maps `\n\r\t` → space but DROPS other control chars (ESC included) — substituting would split `\x1b[31mtext` into two visible fragments — then truncates to `MaxTitleRunes` on a rune boundary. Every failure degrades to fewer/untitled sessions, never an error that blocks pane creation

### `internal/opencodehook/`

OpenCode session-id tracker + hook events forwarder. Embedded JS plugin (`scripts/quil-session-tracker.js`) loaded by opencode at spawn via `OPENCODE_CONFIG_CONTENT='{"plugin":["<abs path>"]}'`. Plugin file lives at `$QUIL_HOME/opencodehook/quil-session-tracker.js`. Two responsibilities: (1) session-id rotation tracking — per-pane ids at `$QUIL_HOME/sessions/opencode-<paneID>.id` (prefix avoids collision with claudehook's `<paneID>.id`); (2) hook events forwarding — filtered bus subscriptions (session.idle/error/compacted, session.status retry-only, file.edited batched 1 s) + typed handlers (permission.ask, experimental.session.compacting) append hookevents.Payload JSONL lines to `$QUIL_HOME/events/<paneID>.jsonl`. Per-pane token bucket (20/s sustained, 50 burst) drops with single warn-log when exhausted. UTF-8-aware truncation respects hook-side caps. `OPENCODE_CONFIG_CONTENT` MERGES with the user's existing opencode config so user plugins/agents/modes survive (verified against opencode 1.14.x). `ReadPersistedSessionID` `Lstat`-rejects symlinks. PTY env carries `QUIL_PANE_ID`, `QUIL_HOOK_HOME` (renamed from `QUIL_HOME`; JS consumer falls back to `QUIL_HOME` for one release), `QUIL_HOOK_MODE`, and the inline config content per opencode spawn

### `internal/codexhook/`

Codex CLI hook producer — sibling of `claudehook`, sharing NO code with it on purpose (that path is incident-hardened; codex differences would live inside it as branches). Codex speaks Claude's hook JSON (same `hook_event_name`/`session_id`/`transcript_path`/`agent_id`/`agent_type` fields) but has no `--settings <file>` and runs only TRUSTED hooks. Quil passes ONE argv token, `-c hooks={<Event>=[{hooks=[{type="command",command="<quild> codex-hook",timeout=N}]}],…,state={"<key>"={trusted_hash="sha256:…"}}}` (`BuildConfigOverride`), where `<key>` is `C:\<session-flags>\config.toml:<event_snake>:0:0` (Windows) / `/<session-flags>/config.toml:…` (elsewhere — the synthetic path codex assigns its session-flags layer) and the hash is SHA-256 of codex's canonical identity JSON (`{"event_name","hooks":[{"async":false,"command","timeout","type":"command"}]}`, keys sorted, compact, HTML escaping OFF — Go escapes `<>&` by default and serde_json does not). **Verified on codex-cli 0.146.0 (2026-09-04)**: with the hash the hook runs, without it codex silently skips it, and `~/.codex` is never touched; `TestTrustHash_MatchesProbe` pins the probe's hash. Timeouts are written EXPLICITLY and SHORT (`hookTimeoutSec` 15; SessionEnd 3, codex's cap for that event): the hash covers the timeout, so writing it pins the hash to a value Quil chose rather than to a codex default (a wrong guess is not a failed spawn, it is codex's startup hook review prompting in every pane), and the handlers are synchronous, so the timeout is how long a wedged hook process holds codex's turn — codex's own 600 s default would stall the agent ten minutes per tool call. **The hook command is UNQUOTED on Windows (`HookCommand` / `hookCommandFor`), and that is measured, not stylistic**: codex runs hooks through `%COMSPEC% /C`, and 0.146.0 passes the line as an ordinary argv token, so Rust escapes every `"` as `\"` and cmd.exe looks for a program literally named `\"E:\...\quild.exe\"` — every hook exited 1 and wrote nothing ("Stop hook (failed)" in the transcript) while the same path unquoted recorded the session; newer codex wraps the line in its own quotes (`raw_arg`), where either form runs. A path with a space takes its 8.3 short name (`shortPathName`, GetShortPathNameW); only when that is unavailable does the quoted form go out, with a note the daemon logs. Unix keeps the quoted form (`$SHELL -lc` handles it). Registered: SessionStart, SessionEnd, UserPromptSubmit, PermissionRequest, PreToolUse (throttled heartbeat with the `agent_id` gate, exactly as Claude's), Stop, SubagentStart/Stop, PreCompact/PostCompact. NOT PostToolUse (no prompt tools to match; the keystroke clears a park) and NOT Interrupt (absent from 0.146.0; the TUI synthesises ESC for every pane). Handlers stay SYNCHRONOUS: codex runs async handlers concurrently and could deliver a Stop before its UserPromptSubmit, which the ingester would replay as a stop for a turn never seen starting. `IsShim` refuses a `.cmd`/`.bat` codex (npm install on Windows) — cmd.exe would re-parse the quoted override, the inline-`--settings` bug with no file fallback — and `codexSpawnPrep` (daemon.go) logs and spawns without hooks; it resolves the binary itself because `spawnPane`'s own `LookPath` runs after the prep switch. Record: `$QUIL_HOME/sessions/codex-<paneID>.id`, two lines (id, rollout path), refused unless a regular file (`O_NOFOLLOW` where available); read through `readCodexSessionFn`. Restore: `codexResumeTemplate` (name-keyed, tested BEFORE the claude capability arm because a codex TOML may legally set `sessions = "claude"`) emits `resume <id>` for a UUID-shaped record and otherwise the plugin's EMPTY `resume_args` — `resume --last` is codex's most-recent lookup, the `--continue` sibling trap, and is never emitted. No occupancy guard and no transcript probe in v1 (opencode's level). `Stop` carries `data.model` from the payload and `data.context_tokens` from the rollout's last `token_count` line (`info.last_token_usage.total_tokens`, codex's own `tokens_in_context_window`; a null `info` is a rate-limit-only update and is skipped). Tier knob `[notification.hooks] codex`. Breadcrumbs: `$QUIL_HOME/codexhook/hook.log`. Follow-ups: `sessions = "codex"` picker (codex keeps one date-sharded tree, `~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl`, first line `session_meta` with `cwd` — needs a reader package and a source on the sessions IPC), and Interrupt once the installed codex has it.

### `internal/hookevents/`

Hook-driven notifications pipeline. `Payload` wire schema (v=1, ts_ms, seq, pane_id, src=claude|opencode|codex, hook_event, session_id, title, sev, data). `Spool` reads JSONL files at `$QUIL_HOME/events/<paneID>.jsonl` appended by the claude .sh / opencode .js hook producers; polled by `daemon.hookEventsWatcher` every 200 ms, tracks per-file byte offset, skips trailing partial lines. `Ingester` per-pane sliding-window rate limit (100/2s — on trip emits synthetic `internal.event_storm` then drops 10 s) + per-(paneID, hook_event, agent_type) 50 ms debounce coalescer (last-wins with `data["coalesced"]` burst count; `agent_type` joins the key only when non-empty — see the work-state section for why last-wins would otherwise erase a subagent's identity). **The coalescer emits in ARRIVAL order across keys, and that is a correctness property**: the consumer replays these as state transitions, so a stop edge delivered before the start it belongs to leaves a pane lit until session end. The first version armed one `time.AfterFunc` per key and let each fire on its own — the watcher submits a whole tick's lines back to back, both timers expired in the same pass, and Go runs the most recently created goroutine first, so the SECOND event went out first (measured 300/300 on Windows, 299/300 on Linux; with three, the last jumped ahead: C,A,B). Keys now queue in first-arrival order (`Ingester.order`), a key's own timer only marks it `due`, and `drainDue` releases the queue's PREFIX under `emitMu` — a later key whose timer goroutine ran first waits for the earlier one, and two batches cannot interleave. A coalesced burst keeps the slot its first arrival opened; `FlushAll` drains in the same order (it used to sort by key). Two edges of that design are load-bearing: `Cancel` removing a pane's HEAD can uncover a key that is already `due` (its timer ran while the head was open and will not run again), so `Cancel` schedules a `drainDue` itself or the event sits until some unrelated timer fires — on a quiet daemon, shutdown; and `coalesce` re-checks `closed` under `mu`, because `Submit`'s own check runs before it drops the lock and a `FlushAll` completing in that gap would otherwise be followed by a timer firing into a torn-down pipeline. The one emit outside the order is the rate limiter's storm diagnostic, which `allowAndRecord` emits inline (never coalesced, no work-state edge). `TestIngester_Submit_DistinctKeysEmitInArrivalOrder` pins the order with real timers; `TestIngester_Cancel_ReleasesTheDueKeyItUncovers` and `TestIngester_Submit_RacingFlushAllIsDropped` pin the two edges. Daemon-side translation `emitHookEvent(Payload) → PaneEvent` enriches with TabID/PaneName, sets `Pane.HookHealthy` + `Pane.LastHookEventAt`, routes through existing `emitEvent` (mute + aggregation + broadcast). `checkIdlePanes.shouldFire` skips the legacy idle excerpt when `HookHealthy && now-LastHookEventAt < 30 s` — fallback to legacy idle if hooks never load (plugin throws at init, settings JSON malformed). Spool init REMOVES stale files on daemon start (falling back to truncation only where the unlink is refused, e.g. Windows holding the file open); `DestroyPane` unlinks the spool file. **Removing rather than truncating is load-bearing for cost**: `Tick` walks every `.jsonl` in the directory 5x/s and pays open+stat+close on each, so a zero-byte husk left for a dead pane costs syscalls for the daemon's whole life — and truncating meant the set only ever grew, across every restart. Measured in production 2026-08-18: 349 spool files for 37 live panes, 332 of them empty, ~7,000 handle ops/sec, 21% of a core in kernel time. The 16 MiB rotation threshold bounds a file's SIZE; nothing bounded the COUNT. **`Tick` no longer opens a file with nothing new**: `os.ReadDir` already carries the size (on Windows `DirEntry.Info()` is served from the `FindFirstFile` data at no extra syscall; on Linux it trades open+fstat+close for one `lstat`), and the idle case — offset equals size — is the common one. Measured after the husk fix: still 151 IO-other + 58 IO-read ops/sec with ~10 live spools. Two refusals are load-bearing: any `Info()` error falls THROUGH to the full read, because a failing stat must not silently stop a pane's events draining; and a file at or past `rotationThreshold` is never skipped, because rotation runs from `readPaneFile`'s idle branch and skipping there strands the file to grow without bound. The offsets map's zero value covers a pane never seen before — an untracked file matches only when it is also empty, which has nothing to read and nothing to rotate — so no separate "is it known" test is needed; external truncation (`size < off`) also falls through, where `readPaneFile` restarts from zero. **The shortcut is safe only while producers are short-lived**: on Windows the size comes from the directory entry and NTFS updates that lazily for a file with an open write handle, so a producer mid-write can present a stale size and take the skip. Today's producers append and close immediately (`claudehook.RunHook`), bounding the cost at one 200 ms tick of latency — but a long-lived producer holding the spool handle open would be stranded until it closed, and would need an age or generation check rather than size alone. Wire caps enforced hook-side (title ≤ 200, data value ≤ 128, total ≤ 2 KiB); daemon's PaneEvent caps (4 KiB / 1 KiB) are the outer backstop. Tier knob `[notification.hooks] claude = "default"|"verbose"|"off"` flows to scripts via `QUIL_HOOK_MODE` env at pane spawn

## Session-id rotation

### Claude Code session-id rotation tracking

`/clear`, `/resume`, and compaction rotate Claude's session id to a new jsonl file. Quil registers a `SessionStart` hook via `claude --settings <path>` at every spawn — a per-pane file under `$QUIL_HOME/sessions/`, never an inline JSON argv token and never a modification of `~/.claude/settings.json` and passes `QUIL_PANE_ID=<paneID>` + `QUIL_HOOK_MODE` + `QUIL_HOOK_HOME` (renamed from `QUIL_HOME`; `hookHomeDir()` in `cmd/quild/hook.go` falls back to `QUIL_HOME` for one release) in the PTY env. The hook is the native `quild claude-hook` subcommand (`claudehook.RunHook` in `runhook.go`, dispatched in `cmd/quild`) — registered via `HookCommand(os.Executable())` → `"<quild>" claude-hook` — which reads Claude's stdin JSON, extracts `session_id` (validated by `validatePaneID` + a uuid regex), and atomically writes `$QUIL_HOME/sessions/<paneID>.id`. The file is a **two-line `SessionRecord`** — id, then the session's transcript path (`ReadPersistedSession`; `ReadPersistedSessionID` is the id-only wrapper the other call sites keep using). The path is recorded because it **cannot be derived**: Claude keys a transcript's project directory off the session's OWN working directory, so an agent that moves into a git worktree moves the transcript with it and the pane's spawn CWD points at a directory the file was never in. `parseSessionRecord` trims PER LINE — a whole-file `TrimSpace` would return both lines glued into one argv token — and rejects an over-long id line outright rather than truncating it. A session can also migrate MID-session, so `Stop` calls `refreshTranscriptPath` — which writes a **sidecar**, `<paneID>.transcript` (`<id>\n<path>`), never `<paneID>.id`. That split is structural, not stylistic: hook invocations are independent processes with no locking, so a read-modify-write of the id file could lose a race with a concurrent `SessionStart` and put the PRE-rotation id back, resurrecting the session the user just left. Confining the refresh to the sidecar makes the id unreachable from there — the worst a lost race can do is leave a path stale, and a stale path never renames a session. `ReadPersistedSession` merges the two, taking the sidecar's path only when its id matches the record's; a sidecar left by a previous session names a different id and is ignored. Older records carry no path; that is "unknown", never "missing".

On daemon restore, `resumeTemplateFor` (daemon.go) dispatches on the CAPABILITY `p.UsesClaudeSessions()` (= `Command.Sessions == "claude"`, `internal/plugin/plugin.go`) — never on the plugin's name — to `claudeResumeTemplate`, which builds a candidate list (`claudeResumeCandidates`) in **source-authority order**: hook record → `PluginState["session_id"]` (refreshed only at shutdown, so it lags a rotation) → `PluginState["resume_session_id"]` (the user's pick at creation). **The CWD-derived probe is GONE.** Deriving a transcript's project directory from the pane's CWD was the root cause — Claude keys that directory off the SESSION's working directory — and removing it also removes an unbounded `os.Stat` from a path that runs before the IPC server listens. What remains is `transcriptExistsFn` on the *recorded* absolute path, through `statExistsWithinBudget`, which joins the same process-wide budget as the browse probes (`claimBlockingFSCall` + `transcriptProbeTimeout`) because the value is influenced by a pane's own child and persists on disk.

`transcriptState` classifies each candidate `located` / `unknown` / `missing`. A recorded path may only speak for the id it **names** (`filepath.Base(path) == id+".jsonl"`) — the path is an independent string, so without that binding any existing file would vouch for any id. An unanswered probe is `unknown`, never `missing`: a stat that times out on a dead mount is not proof a session was deleted. `usableResumeCandidates` drops only `missing` and **never reorders** — ranking a located low-authority id above an unlocated high-authority one would resume the pre-rotation conversation, the same silent swap in a narrower case. Every id is shape-checked against `resumeSessionIDRe` regardless of state, since a recorded path cannot stand in for that check the way an on-disk `<id>.jsonl` filename does; rejects are logged by LENGTH only, because the daemon log is rendered by the F1 viewer, which does not pass through a VT emulator. Both seams are package vars so tests never touch real `~/.claude/` or `$QUIL_HOME/sessions/`.

**`--continue` is not a neutral fallback and must never be reached with a known id.** It is Claude's most-recent-session-in-CWD lookup, so a pane whose own session we merely failed to LOCATE silently attaches to a sibling's conversation — and on restore the sibling that respawned a second earlier is exactly the one it finds. Shipped 2026-08-01 as three panes converging on one transcript, their claude processes interleaving appends into it, after a restart was the first since those panes were created. Hence an unlocated id is resumed anyway — a rejected id is a visible error, a wrong session is silent data loss — and `--continue` survives ONLY for a pane that recorded no session at all. "A session we refuse to name" is a different state and must not share that exit: `claudeResumeCandidates` returns `sawRecorded` alongside the list, and when every candidate was proven gone or rejected as malformed the pane takes a **fresh** identity (`freshClaudeSession`) instead. Minting that id is load-bearing rather than tidy — leaving the old value in `PluginState` would have the pane advertise a session it is not in, so the occupancy map would report it as the holder, and a later Alt+R (which spawns with `restoring=false`) would hand that id straight to `--session-id`.

Restore also gained the occupancy guard the create path has had since the resume picker shipped, as `(*Daemon).claimResumeSession` behind the `sessionClaimFn` seam (threaded through `resolveSpawnArgs` so the arg matrix stays a table test; `claimAny` is the no-occupancy stand-in, so the parameter is **never nil** and a forgotten wiring fails in a test rather than silently dropping the guard). It **selects and claims in one step**: an earlier version queried occupancy under `resumeClaimMu` and wrote the claim after releasing it, so two panes restoring concurrently could both observe the same session free — reachable in the ordinary case where two panes were left holding the same id. It walks the whole candidate list under one lock (a refused top candidate must not cost the pane its own next-best session) and builds the occupancy map ONCE, since building it re-walks every pane and re-reads every hook record inside the pre-listen readiness budget. Lock order is `resumeClaimMu` → `PluginMu`, matching `applyResumeSessionID`; `claudeResumeCandidates` captures every pane field under `PluginMu` and runs each probe after releasing it. `Daemon.Stop()` also calls `refreshPluginStateFromHooks()` before the final snapshot, copying the live hook-recorded id into `PluginState["session_id"]` for every claude-code and opencode pane so `workspace.json` carries the post-rotation id even if the hook file is later lost — empty/error reads preserve the existing value rather than clobbering with `""`. For claude-code it also copies the transcript path into `PluginState["transcript_path"]`, so a pane stays locatable when the hook file is gone (the path cannot be rebuilt from the pane's CWD — that is why it is recorded at all). **The id and the path are stored as ONE unit** — by `refreshPluginStateFromHooks` at shutdown AND by `recordResumeSession` when a claim lands: an id arriving without a path DELETES the key rather than leaving the previous session's behind, and a persisted path verifies only the id it was recorded with. A path that outlives its id would vouch for a transcript nobody checked, which is the same class of confidently-wrong answer as `--continue`. Both sources are consulted because either can be the survivor: the hook file dies with a wiped `$QUIL_HOME/sessions`, the workspace copy with a daemon killed before its shutdown refresh

### OpenCode session-id rotation tracking

opencode mints a new session id on `/new`, fork, or compaction. Quil registers a JS plugin via `OPENCODE_CONFIG_CONTENT='{"plugin":["<abs path>"]}'` at every spawn (never writes to `~/.config/opencode/`) and passes `QUIL_PANE_ID=<paneID>` + `QUIL_HOOK_HOME=<dir>` (renamed from `QUIL_HOME`; JS reads `QUIL_HOOK_HOME || QUIL_HOME` for one release) in the PTY env. The plugin — embedded in `internal/opencodehook/scripts/quil-session-tracker.js` and written to `$QUIL_HOME/opencodehook/` by `opencodehook.EnsureScripts()` — hooks opencode's `session.created` / `session.updated` / `session.idle` / `session.compacted` / `session.deleted` events, extracts `event.sessionID` / `event.session_id`, and atomically writes `$QUIL_HOME/sessions/opencode-<paneID>.id`. On daemon restore, `resumeTemplateFor` → `opencodeResumeTemplate` calls `readOpencodeSessionIDFn` (defaults to `opencodehook.ReadPersistedSessionID`) and promotes the resume args to `["--session", "{session_id}"]` when an id is present, falling back to `["--continue"]` otherwise. No session-exists probe in v1 — opencode handles stale ids itself; SQLite probe (`~/.local/share/opencode/opencode.db`) deferred to v2 if needed. `opencodeHookScriptStatFn` and `readOpencodeSessionIDFn` are swappable via package-level vars so tests never touch real filesystem state. Static templates (e.g. `--continue` with no `{placeholder}`) now pass through `resolveSpawnArgs`'s gate without requiring `PluginState` — see `templateHasPlaceholder` helper — so a fresh opencode pane that closed before its first session event still respawns with the fallback args

### Codex session-id tracking

Codex mints a new session id on `/new` and reports it (with the rollout path) through the `SessionStart` hook Quil registers via the `-c hooks=…` override described under `internal/codexhook/`; the hook rewrites `$QUIL_HOME/sessions/codex-<paneID>.id` on every rotation. On daemon restore `resumeTemplateFor` → `codexResumeTemplate` reads it through `readCodexSessionFn` and promotes the resume args to `["resume", "{session_id}"]`; with no usable record the pane starts FRESH (`resume_args = []` in the shipped TOML) — never `resume --last`, for exactly the reason `--continue` is banned above. `refreshPluginStateFromHooks` copies id + path as one unit at shutdown, like Claude's arm.

## Work state

### Work-in-progress indicators

`internal/tui/workstate.go` derives a per-pane `working` bool entirely TUI-side from the existing `paneEventMsg` stream (`Type == "hook.<src>.<event>"`) — no new IPC, no daemon state. `working` is derived from two inputs tracked per pane: `turnActive` (main turn) OR `len(subagents) > 0` (outstanding background subagents). Start edges (→ `turnActive`): `hook.claude.UserPromptSubmit`, `hook.opencode.chat.message`, `hook.claude.PreToolUse`, and their codex twins `hook.codex.UserPromptSubmit` / `hook.codex.PreToolUse` (codex reuses Claude's event names; every `hook.codex.*` edge below maps exactly as the Claude one it sits beside, minus the events codex never emits — Notification, StopFailure, the Task pair).

**`PreToolUse` is the only start edge that does not assume a HUMAN began the
turn, and it exists because the other two do.** `UserPromptSubmit` is a typed
prompt and `PostToolUse` is matched to the interactive-prompt tools the user
has just answered, so a turn Claude Code starts BY ITSELF has neither. When a
teammate/subagent reports back, its result is injected as a user-ROLE
transcript entry and the orchestrator resumes with no prompt submitted.
Measured on one orchestrator pane (2026-08-15): **3 `Stop`s against 1
`UserPromptSubmit`**, and between the last `SubagentStop` (18:19:57) and the
next `Stop` (18:34:38) the pane ran ~60 tool calls with `working` false the
whole time — 14m41s of visible work with no indicator. A `Stop` for a turn
Quil never saw start is machine-checkable proof of a missed start edge, but it
arrives at the END of the work, so it can only diagnose the hole, never fill
it. There is no turn-start hook to register instead: the upstream event list
has none, and the only signals emitted during autonomous work are tool-level.

**It is registered MATCH-ALL and throttled at the PRODUCER, and that split is
load-bearing.** A resumed turn's first action is whatever tool the agent picks,
so any tool-name matcher would restore the blind spot — which is why the volume
is handled by `claudehook.spoolIsFresh` instead: the branch runs once per tool
call (the only Quil hook that does) but drops the line unless the pane's SPOOL
has been quiet for `workHeartbeatInterval` (15 s). The spool is the right clock
because the question is "does Quil already know this pane is alive", not "when
did the last heartbeat fire" — so an ordinary `UserPromptSubmit` turn suppresses
every heartbeat behind it for free, and a live 4-tool-call run spooled exactly
ONE `PreToolUse` line (verified against Claude Code 2.1.233). Dropping a line is
free because the signal is a LEVEL, not an edge: any later tool call in the same
turn re-arms the identical state. Both failure directions fall toward speaking —
a missing/unreadable spool means Quil has heard nothing at all (loudest reason to
emit), and a FUTURE mtime yields a negative age that must not read as "recently
audible", or clock skew would mute a pane's indicator indefinitely. Uncapped, the
per-tool-call stream would also spend a pane's whole 100-events-per-2 s ingester
budget on heartbeats and take a real permission prompt down with it.

**It is work-state-only, like `PostToolUse`, and that predicate must live in
`hookevents.IsWorkStateOnly` because BOTH ends need it.** Being a work-state
edge is NOT the test: `Stop`, `StopFailure`, `PermissionRequest` and a named
`SubagentStop` are exactly what the sidebar and the attach replay are for. The
test is whether the event says anything a user can act on, and a heartbeat that
repeats every 15 s for as long as an agent works says only "still running".

The TUI half (`tui.workStateOnlyEvent`, which now delegates) keeps the card off
the sidebar. **The DAEMON half is the one that was missed, and a render-side
suppression could not cover it**: `emitEvent` pushed every heartbeat into the
bounded (50-slot) notification queue, where `eventQueue.Push` aggregates by
`(PaneID, Title)` and then RE-PREPENDS the aggregated entry. A constant
`"Working"` title therefore holds one slot per working pane AND jumps ahead of
every older event each time it fires, displacing genuine notifications out of
the attach-replay window — and this project's own position is that a missed park
is silent and terminal. It also woke every `watch_notifications` watcher on the
pane (that handler filters by pane, not by type), turning the tool documented as
replacing polling back into a ~15 s poll, and re-pushing a dismissed card under
its existing ID. `emitEvent` now takes the same broadcast-but-don't-queue path
the mute branch uses.

**The broadcast is not optional on that path.** With the event out of the queue,
the live broadcast is the ONLY route by which a client learns the pane is
working, so an "optimisation" that returns early there silences the spinner
outright — the exact bug the heartbeat exists to fix. The cost is that a
reattaching client cannot rebuild `working` from the replay and waits for the
next live heartbeat instead; that is bounded by `workHeartbeatInterval` and
matches the standing contract that work state is not persisted.
`TestEmitEvent_WorkStateOnlyEventBroadcastsWithoutQueueing` drives a real IPC
server and a real client for exactly this reason — a queue-count assertion
passes just as happily with the broadcast deleted.

**It is gated on `agent_id` being ABSENT, which is what keeps it a statement
about the MAIN turn** — the only thing `turnActive` means. Hooks fire inside
subagents too, and a background subagent outlives the main turn's `Stop` by
design, so admitting its tool calls would reopen a turn that has already ended
with nothing left to close it again: the subagent's own completion is a
`SubagentStop`, not a `Stop`, so the pane would hold a lit spinner until
`SessionEnd`. Nothing is lost by dropping them, because the subagent ledger
already keeps exactly those panes `working` via `SubagentStart`/`SubagentStop`.
Verified against Claude Code 2.1.233 by dumping raw hook stdin: a main-agent
`PreToolUse` carries neither `agent_id` nor `agent_type` (including the one for
the `Agent` tool that SPAWNS a subagent), while the subagent's own `PreToolUse`
carries both, matching the `SubagentStart`/`SubagentStop` pair that brackets it.
**`agent_type` cannot serve as the gate** — a session started with `--agent`
carries one on every event, main-agent events included — so `agent_id`, which is
documented as present only inside a subagent, is the discriminator.

Subagent edges: `hook.claude.SubagentStart` adds to the ledger (spinner on), `hook.claude.SubagentStop` drains it; both honor the ingester's `data["coalesced"]` burst count (N events sharing the debounce key in the 50 ms window arrive as ONE PaneEvent) via `coalescedCount`, or a parallel 3-subagent spawn would undercount as 1.

**`subagents` is a `map[string]int` keyed by `data["agent_type"]`, not a bare count, and that is load-bearing rather than tidy: a `SubagentStop` may only cancel a `SubagentStart` it can be MATCHED to.** Claude Code emits ONE unpaired `SubagentStop` carrying an EMPTY `agent_type` at the end of every main turn — it is the ROOT turn's own completion, whose start edge is `UserPromptSubmit` rather than a `SubagentStart`, so it can never have a partner. Measured 2026-08-02 across every AI pane in a live workspace: empty-`agent_type` stops track the `Stop` count 1:1 (52 vs 53 on the worst pane, 51 of them landing 66 ms–5.2 s after a `Stop`), and a `SubagentStart` with an empty `agent_type` NEVER occurs. With a fungible counter each phantom is spent on whichever background agent happens to be outstanding: a named agent's start is cancelled ~2 s later by a stop that names nothing, the spinner goes dark while the agent runs (observed: a 27-minute `impl-task7` with no indicator), and the ledger then stays desynced — 18 REAL named stops on that pane were subsequently swallowed as orphans. The old zero-guard could not catch this and was never the wrong idea, only the wrong condition: it fires when the count is already 0, which is exactly when no agent is at risk. A stop naming no live agent is now ignored — which covers the phantom AND the replay-gap orphan under one rule, so `delete`-on-drain keeps the map self-correcting instead of accumulating.

**The empty key is REFUSED on the start edge, not merely never observed.** `workSubagentStart` drops a start carrying no `agent_type`, which is what converts "the empty key is never live" from a measurement into an invariant: the empty key is precisely the one the phantom carries, so admitting it would let the phantom drain real work again — silently — if the producer ever renamed or dropped the field. The pre-existing subagent tests all passed `nil` data and therefore ran entirely on the `""` key, i.e. on the one shape production never emits; they now name an agent.

**The ledger is bounded** (`maxTrackedSubagents`, 64). `agent_type` is producer-controlled, so key cardinality is too, in a TUI process that runs for weeks — and `clear()` empties a Go map without returning its table, so the old "self-heals to zero" property is weaker than `= 0` was. **The cap sets `subagentsOverflow` rather than silently dropping the start, and `working` derives from `turnActive || len(subagents) > 0 || subagentsOverflow`.** The obvious-looking reasoning — "refusing a new key cannot turn the spinner off, since `len() > 0` is already true there" — is true only at the instant of refusal and was wrong as a justification: once the 64 tracked agents drain, `len()` reaches zero while the refused agent is still running, reinstating the precise bug this ledger exists to prevent at the cap boundary. The flag is sticky until a terminal edge because we never learn that an untracked agent finished (its stop names a key we do not hold), so no earlier clearing point is sound; `SessionEnd` and `process_exit` are where nothing can still be live. Wrong-on is the safe direction here — a lingering spinner on a pathological pane costs a glyph, wrong-off costs the user the only cue that work is happening. Both the unnamed-start refusal and the cap `break` rather than `return`, so the single derivation point still owns `working` on every path.

**The phantom is also dropped at the PRODUCER** (`internal/claudehook/runhook.go`, `SubagentStop` with empty `agent_type` → no spool line). It names no agent and reports nothing actionable, but spooled it became a sidebar card titled literally `" done"` once per turn on every AI pane, which the queue's `(PaneID, Title)` aggregation collapsed to `" done" ×N` and re-promoted to the top on each occurrence. The TUI-side match-by-name guard stays as defence in depth — the producer drop removes the noise, the ledger rule is what keeps the indicator correct.

**`agent_type` is part of the ingester's coalesce key for exactly this reason** (`internal/hookevents/ingest.go` — `coalesceKey(paneID, hook_event, agent_type)`, appended only when non-empty so every other event keys as before). Coalescing is last-wins, so merging two DIFFERENT agents' starts would erase the loser's identity: its own stop would then match nothing while the winner's count never drained, wedging the spinner until `SessionEnd` — the ledger's identity guarantee only holds because the wire preserves it. A burst of the SAME agent still collapses to one emit with the burst count, which is what the count exists for. **The key's two free-form components are escaped** (`keyFieldEscaper`): `paneID` is NUL-free by `safePaneID` and stays first (so `Cancel`'s prefix match is unaffected), but `hook_event` and `agent_type` are arbitrary payload strings and JSON admits U+0000 in either — two variable fields joined by a separator either may contain is not injective, and `("SubagentStart", "\x00X")` would otherwise key identically to `("SubagentStart\x00", "X")`, coalescing them last-wins and erasing an identity. The escape is identity for every value a real producer emits. Claude Code runs subagents detached by default, so the main turn's `Stop` routinely fires while they still run: stop edges only end the spinner once the counter is drained, and the unseen mark is deferred to the drain edge (the LAST `SubagentStop` becomes the completion edge). Stop edges (→ persistent green unseen mark on the pane): `hook.claude.Stop`, `hook.claude.StopFailure`, `hook.opencode.session.idle`/`session.error`, `hook.codex.Stop` (and `hook.codex.SessionEnd` as the terminal stop, `hook.codex.SubagentStart/Stop` on the ledger).

**ESC is the third turn ending, and the only one with NO upstream event behind
it — so the TUI synthesises one.** Measured against Claude Code 2.1.233 by
driving a real pane over IPC: submitting a prompt spools `UserPromptSubmit`,
interrupting the streaming response with ESC spools **nothing at all** — not
`Stop`, not `StopFailure`, not `Notification`, still nothing 80 s later. So
`turnActive` stayed true until `SessionEnd` and a stopped pane went on claiming
work indefinitely (reported from a live workspace at 43 minutes and counting).
`tui.interruptWorkingPane` feeds the synthetic type `internal.user_interrupt`
(`tui.userInterruptEvent`) through `applyWorkTransition`, which
`ClassifyWorkEvent` maps to a plain `WorkEventStop` — reusing the ordinary
turn-ending edge instead of hand-rolling a second way to clear the same fields.
It is TUI-only and never crosses IPC.

Three constraints make a keystroke acceptable where one normally would not be.
It is wired to the two **key** paths only — not paste (a pasted `0x1b` is not a
decision to interrupt) and not `enqueueInput` (which also carries wheel notches
and the selection handler's arrow keys), the same boundary
`answerBlockedByInput` draws. It ends the **main turn only** and leaves the
subagent ledger alone, because a subagent's own tool calls are dropped by the
`agent_id` gate, so nothing could ever re-light a spinner cleared here for a
teammate that is still running — whereas the opposite error is the wrong-on
direction `subagentsOverflow` already accepts and `SessionEnd`/`process_exit`
both clear. And being wrong is now **recoverable**: ESC has other uses inside
Claude (dismissing a menu, leaving a mode), so this can close a turn that is
still running, but the `PreToolUse` heartbeat re-lights it at the next tool
call. Before that heartbeat existed the same heuristic would have gone dark for
good, which is why this fix belongs after it rather than before.

**`StopFailure` is the turn ending Claude reports INSTEAD of `Stop` when the API
call fails, and leaving it unregistered stranded the spinner.** A turn killed by
an API error emits no `Stop` at all, so `turnActive` stayed true with only
`SessionEnd` or `process_exit` able to clear it — neither of which a user reaches
without restarting the pane or the session, so the pane claimed to be working for
as long as it stayed open. Same missing-edge class as the `PreToolUse` gap below,
opposite direction: that one loses the indicator, this one strands it. Mapped to
plain `WorkEventStop`, NOT `StopFinal`: an API error ends the TURN and says
nothing about background subagents, which are separate processes carrying their
own edges, so clearing the ledger here would drop agents that are still running.
It spools the `error` where `Stop` spools model usage — there is no completed
assistant turn to read usage from, and the error is what separates a network
blip from a pane worth restarting. **The payload field is `error`, not
`reason`** (observed live on Claude Code 2.1.260: `"error":"model_not_found"`
beside `last_assistant_message`; `reason` exists on `PreCompact` only) — the
first version read `reason` and every production "Turn failed" card shipped with
an empty explanation.

**`StopFailure` fires INSIDE subagents too, and there it is the only ending the
agent ever gets.** A subagent whose turn dies emits `StopFailure` carrying its
`agent_id` + `agent_type` and NEVER a `SubagentStop` — verified on 2.1.260 by
forcing a subagent onto a model that does not exist (`SubagentStart`, then
`StopFailure` with the same `agent_id`/`agent_type`, then nothing for that
agent). Spooled as a bare "Turn failed", the TUI read it as the MAIN turn ending
and kept the dead agent in its ledger with nothing left to drain it: two
production panes (software-factory, keycloak-prod-release) held a lit spinner
for two days after the claude.ai usage limit killed their QA agents at 11:16 on
2026-09-02 — each spool held one `SubagentStart` with no stop, and the only edge
Claude had sent was the `StopFailure` a minute later. The producer now spools a
subagent's failure under `data["agent_type"]` (title `<agent> failed: <error>`),
and `applyWorkTransition`'s `workStop` arm treats a named `StopFailure` as the
`SubagentStop` it stands in for — `PaneModel.drainSubagent`, shared with the
real stop — leaving `turnActive` alone, because a subagent's death says nothing
about the main turn (reading it as one was also a wrong-off on a turn still
running). `agent_id` is the discriminator, exactly as in the `PreToolUse` gate:
a `--agent` session carries `agent_type` on every event, so a `StopFailure` with
`agent_type` but no `agent_id` is the main turn; one with `agent_id` but no
`agent_type` names nothing to drain and must not end the main turn either, so
the producer drops it like the unnamed `SubagentStop`. The classifier still sees
only the type — the split lives at the two ends, as with `Notification` — and
the consumer's gate is the event TYPE (`tui.subagentFailureEvent`), not the
kind: `WorkEventStop` also covers `hook.claude.Stop`, the two opencode stops and
the synthetic interrupt, none of which is ever a subagent's ending, and a
kind-based gate would turn every main-turn `Stop` into a no-op the day one of
them grew an `agent_type`. Diagnosed by replaying the spool: a per-`agent_type`
start/stop net over `$QUIL_HOME/events/<paneID>.jsonl` names the orphan in
seconds, and reading that file needs the user's explicit OK (see
`dev-environment.md`). `hook.claude.SessionEnd` is a *terminal* stop (`WorkEventStopFinal`): it also clears the subagent ledger (no subagent outlives its session — a lost SubagentStop must not wedge the spinner). `TaskCreated`/`TaskCompleted` are deliberately unmapped (task-list bookkeeping, not execution). Resume edge: `hook.claude.PostToolUse` (registered with a tool-name matcher `AskUserQuestion|ExitPlanMode` in `internal/claudehook` so it fires only for interactive-prompt tools — the user just answered → re-arm spinner; `workStart` clears the pane's unseen mark; suppressed from the notification sidebar as work-state-only). `process_exit` clears `working` AND the subagent ledger WITHOUT marking unseen (a crash is not a completed turn). A single shared 100 ms `workSpinnerTickMsg` animates the braille `spinnerFrames` on both the tab label (`tabLabel` prefix when `tabHasWorkingPane`) and each working pane's top-left border (left segment of `buildTopBorder`, reserved so the CWD truncation never eats the glyph); the loop self-stops via `workTickRunning` when no pane is working. `unseen` lives on `PaneModel` (set on workStop unless the pane is the focused pane of the active tab; cleared by `ackFocusedPane` at the single `Update` entry choke point — focusing the pane is the acknowledgement, no timer). **It is the one piece of work state with a DAEMON COPY, and the arrangement is the pin's inverted**: the client derives the mark (only it knows what the user was looking at) and reports every CHANGE as `MsgUpdatePane{Unseen}` (`Model.reportUnseen` — from `applyWorkTransition`'s edges, `ackFocusedPane`'s clear and the context menu's Clear attention), the daemon persists `Pane.Unseen` in the snapshot and hands it back as `unseen` in the workspace state, and `syncPaneMeta` copies it EXACTLY ONCE per `PaneModel` (`unseenSeeded`) — never on later broadcasts, which would revert a clear the user just made within one git tick. The seed is the TUI restart: before it every green tab was lost with the process, because the attach replay can only re-derive a mark whose START edge is still inside the 200-event queue. Two constraints are load-bearing. The report is SYNCHRONOUS on the Update goroutine, never a `tea.Cmd`, because a set and its clear can be milliseconds apart and Cmds have no ordering. And an unseen-only update does NOT broadcast (`handleUpdatePane` diverts it to `requestSnapshot` alone; `updateTouchesBroadcastState` deliberately omits it): the replay re-derives marks across the whole workspace at attach, and a full state frame per report is the 64-slot force-disconnect shape. Three more edges, each a review finding. **`finishReconnect` RESTATES every seeded mark for the reattached daemon**: a report made while the link was down is dropped (the router answers nil, a dead conn errors), a restarted daemon restores a snapshot up to one debounce older than the last report, and nothing re-derives a *report* — the replay re-derives the local value, which reports only on change — so without the restatement a mark cleared during an outage came back green on the next TUI start. **A seeded mark does not toast** (`unseenFromSeed`, cleared when this process sets the mark itself): `raiseDeferredToasts` is state-based and runs on the first message of every TUI start, so every remembered green tab would otherwise toast on every restart, and the per-pane cooldown cannot help because `lastToastAt` is zero on a fresh `PaneModel`. **The copy is per PANE, not per client**: two attached clients (a local TUI and a `--remote` one) each derive and report their own mark, and a restart of either seeds from whichever wrote last. Marked panes render a green border (precedence below active/ghost/MCP-highlight); background tabs derive a green label via `tabUnseen` + `unseenTabStyle` — `tabStyle(idx)` precedence is `blockedTabStyle` (amber, includes the active tab: the parked pane may be in an unfocused split) > `pinnedTabStyle` (purple 141, a pane pinned by hand — it had shared `unseenTabStyle`'s green, which made the mark the user set indistinguishable from the one the agent caused, and only the latter clears itself on focus) > `unseenTabStyle` (green, `tabUnseen` alone now) > custom tab color > active/inactive default, and is shared by `renderTabBar` + `hitTestTab` so rendered widths and click hit-testing never diverge. The active tab label never shows green (you're already looking at it); an unfocused split sibling still shows its green border. OpenCode's start edge is produced by the `chat.message` handler in `internal/opencodehook/scripts/quil-session-tracker.js`; Claude needs no producer change (both edges already arrive). Work state (`turnActive`, the ledger, `blockedSince`) is not persisted — panes start idle on restart and the next hook event corrects them; the `unseen` mark is the exception described above.

Park-for-input edges (`hook.claude.PermissionRequest`,
`hook.opencode.permission.ask`, `hook.codex.PermissionRequest`) set `blockedSince` and do **NOT** clear
`turnActive` — a permission prompt arrives mid-turn, and approving a
Bash/Edit/Write prompt fires no hook of its own, so clearing `turnActive` on
the park left the pane reading blocked-not-working until the turn's `Stop`,
for however long the agent was already back at work.

**`hook.claude.Notification` is a DIFFERENT `WorkEventKind` (`WorkEventNotify`
/ `workNotify`), not a synonym for `PermissionRequest` — because it is
AMBIGUOUS in a way `PermissionRequest` is not.** Claude reuses the same hook
event for a permission prompt (arrives mid-turn, `turnActive` still true) and
for its own idle nudge, "Claude is waiting for your input" (arrives AFTER the
turn's own `Stop` already cleared `turnActive`, often while background
subagents are still draining). Collapsing both into one `WorkEventPark` was
the bug: a production pane ran `UserPromptSubmit` → 4×`SubagentStart` →
`Stop` → `Notification` → `SubagentStop` → `Stop` → `Stop` → `Notification`,
and the unconditional park painted its tab amber and hid the `◐ ⋯3` a
still-working pane should show, because `paneRow`'s blocked-outranks-working
precedence picked the stale `▲` over the live subagent count.

**The ambiguity is resolved at the two ENDS, not by the classifier, and the
match runs in ONE direction on purpose.** `hookevents.ClassifyWorkEvent` is
handed the event type and nothing else — neither the hook's `message` text nor
`turnActive` — so it reports the ambiguity as its own kind. The PRODUCER
(`internal/claudehook`'s `notifyKindData`) is the one place holding the message
and marks the idle nudge it recognises as
`data["notify_kind"]="idle"` (`hookevents.DataNotifyKind`/`NotifyKindIdle`,
declared next to the kind so the two halves cannot drift apart quietly).
`tui.applyWorkTransition`'s `workNotify` case then parks **unless** the event
is marked idle AND `turnActive` is false. Matched positively — idle recognised,
everything else parked — because upstream English prose is not ours to depend
on, and the direction decides which way a reworded, unknown, or unmarked
message fails.

**`turnActive` alone is a LOSSY discriminator, which is why it is the second
condition and not the only one.** It is false in several states where a
permission prompt is genuinely outstanding: a background subagent asking for
permission after the main turn's `Stop` (the subagent ledger exists precisely
because subagents outlive it), a TUI restart or remote reattach
(`resetWorkStateForReattach` zeroes it on every pane of a destination), and a
replay truncated past its `UserPromptSubmit`. `PermissionRequest` is
documented as "when available" — on the Claude version that produced the trace
above it never fired at all, so `Notification` was the ONLY permission signal
and this gate the only guard on it. The failure modes are not symmetric: a
wrong park is a visible amber tab that the next `Stop` or a keystroke
(`answerBlockedByInput`) clears, while a missed park is silent and terminal —
`tabBlocked` false, uncounted in `counts()`, not offered by `Alt+Shift+A`, and
a parked agent emits no further hook to recover it. So the unmarked case parks,
which is also what makes an OLD hook binary beside a new TUI safe: it marks
nothing, and every `Notification` behaves exactly as it did before the split.

Once the mark says idle and the turn is over, the case changes NOTHING —
leaving the pane exactly as `Stop` left it (`unseen` if nothing was
outstanding, still `working` if subagents are). Marked idle but mid-turn parks
anyway; only `Stop`-then-nudge is unambiguous. The split does not touch
`PermissionRequest`/`permission.ask`, which stay unconditional — they are
never ambiguous, so gating them on `turnActive` would be a regression, not a
fix (a permission prompt firing after a pane's own `Stop`, from a hook whose
event name says exactly what it is, must still block). Both park arms write
`blockedReason` only when `data["tool"]` is non-empty: `Notification` carries
no tool, and an unguarded assignment would erase the `▲ Bash` a
`PermissionRequest` for the same prompt had just given the sidebar.

Because `working` no longer falls on a park, the falling edge no longer sets
`unseen`. `tabBlocked` (`workstate.go`) + `blockedTabStyle` carry a parked
background pane to the tab bar instead.

**`ackFocusedPane` clears `unseen` ONLY — focus is not an answer to a park.**
It was written to clear `blockedSince`/`blockedReason` alongside `unseen`
("you are looking straight at the prompt") and that was reversed. The function
runs at the top of EVERY `Update`, including the shared 100 ms
`workSpinnerTickMsg` that is guaranteed to be ticking because the pane is
working — so a pane parked while it held focus had the mark set and cleared
about 100 ms later, and the sidebar `▲`, the amber tab, the project badge's
blocked count and the attention-queue entry were **none of them ever
observable**. That is not an edge: the agent asking for permission while the
user sits in its pane is the commonest park there is, and with `turnActive`
kept the pane went on claiming to be *working* the whole time it waited.

The distinction the two flags need: `unseen` is a "you missed something" flag
that looking genuinely answers, while `blockedSince` is a fact about the
**agent**, so clearing it on a spinner tick destroys information rather than
acknowledging a notification. `paneRow` (`sidebar.go`) therefore suppresses the
blocked **presentation** for the focused pane — glyph and reason both — while
`tabBlocked`, `ProjectModel.counts()` and `blockedPanes()` all keep reading the
same live `blockedSince`. Leaving the pane restores every signal with no hook
edge required.

**A glance is not an answer, but a keystroke is: real user input to a pane
clears its mark** (`PaneModel.answerBlockedByInput`, workstate.go). This is the
other half of the same rule and it is required, not a convenience: approving a
Bash/Edit/Write prompt fires **no hook at all** — `promptToolMatcher` is
`AskUserQuestion|ExitPlanMode`, so `PostToolUse` does not cover it — and the
pane's next event is the turn's `Stop`, minutes away. Without it an ANSWERED
prompt kept its tab amber, kept counting as blocked rather than working, kept
being offered by `Alt+Shift+A`, and put the `▲` back the moment the user
switched away.

The clear is wired at the producers that represent a HUMAN acting on the pane —
the two `handleKey` forward paths and both paste paths — and deliberately **not**
at `enqueueInput`, the ordering choke point every producer shares including
forwarded wheel notches, nor at `forwardInputBytes`, which the selection handler
also uses to walk the shell cursor during a mouse DRAG (arrow-key escapes a
permission prompt would consume as a choice). Scrolling or dragging across a
parked pane is a glance with a mouse. It is keyed to input REACHING a pane
rather than to focus, so the asynchronous paste path answers a pane that is no
longer the active one. The only other non-agent route to a clear is the context
menu's **Clear attention** row, which is what that row exists for.

### Model/context status-bar segment

AI panes show the last completed turn's model id + context-window token count in the status bar (`opus-4.8 · 612k ctx`, formatted by `internal/tui/modelinfo.go:modelStatusSegment` — raw tokens, deliberately no percentage: the max window isn't recorded in either tool's data and a Claude session can run 200k or 1M). Data rides the existing hook-events pipeline as `data.model` + `data.context_tokens` on turn-boundary work events (no new IPC, no new hook_event name, mute-bypass for free): Claude side, `runhook.go` parses `transcript_path` from the hook stdin and `internal/claudehook/transcript.go:readTranscriptUsage` tail-reads the session transcript (last 256 KB, last non-sidechain assistant line, context = input + cache_read + cache_creation) on `Stop`. `PostCompact` deliberately does NOT read the transcript — right after compaction the reduced size isn't in it yet (the compaction summary is system/user entries with no assistant usage, so a read returns the stale PRE-compaction count), so it emits `data.compacting=1`, resetting the count to the `ipc.ContextTokensCompacting` (-1) sentinel (rendered `<model> · compacting`) until the next `Stop` reports the true reduced size; OpenCode side, the JS plugin caches `modelID`/`tokens` from the inline `message.updated` bus event (finished assistant messages only, never spooled per-update) and attaches them to the `session.idle` spool event. Daemon: `emitHookEvent` mirrors the values onto `Pane.LastModel`/`LastContextTokens` (PluginMu-guarded, runtime-only — broadcast in the workspace snapshot's `includeOverlays` block, never persisted; cleared on restart like `MouseModes`). TUI: live update from `paneEventMsg.Data`, attach-time sync via `PaneInfo.Model`/`ContextTokens` → `syncPaneMeta` (unconditional copy — the daemon writes the fields before broadcasting the event and IPC is ordered per conn, so a snapshot never lags a live value; an ABSENT snapshot key is meaningful, it propagates the restart-clear to the status bar)

