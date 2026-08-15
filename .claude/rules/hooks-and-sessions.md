---
description: Claude Code / OpenCode hooks, session-id rotation tracking, the hook-events pipeline, and the work-in-progress indicators derived from it. Load when touching hook producers, session discovery, or work state.
paths:
  - "**/internal/claudehook/**"
  - "**/internal/opencodehook/**"
  - "**/internal/hookevents/**"
  - "**/internal/claudesessions/**"
  - "**/internal/tui/workstate.go"
  - "**/internal/tui/modelinfo.go"
---

# Hooks and Sessions

Extracted verbatim from `.claude/CLAUDE.md`. Loaded only when the files above are in play.

## Hook packages

### `internal/claudehook/`

Claude Code multi-event hook (native subcommand, no scripts). `BuildSettingsJSON` registers Quil's hook command under 12 Claude events (SessionStart for session-id tracking + 11 forwarded to the JSONL spool: SessionEnd, UserPromptSubmit, Notification, PermissionRequest, Stop, PreCompact, PostCompact, SubagentStart/Stop, TaskCreated/TaskCompleted). The hook command is `HookCommand(exePath)` → `"<quild>" claude-hook` — the daemon passes `os.Executable()`. `RunHook` (runhook.go) is the handler: reads the hook JSON on stdin, branches on `hook_event_name`, and either writes the session id file (SessionStart) or appends a `hookevents.Payload` JSONL line via `encoding/json` (no hand-rolled escaping; eliminates the BOM/codepage bug class the old `.ps1`/`.sh` had). Wired in `cmd/quild/main.go` as a fast-path subcommand that never starts the daemon — replaces the per-event PowerShell/sh spawn (~1-4 s cold start) with a native Go process (~tens of ms). `claudeHookSpawnPrep` (daemon.go) builds the `--settings` JSON from `claudeHookExeFn` (≈`os.Executable`, injectable for tests) and sets PTY env `QUIL_PANE_ID` + `QUIL_HOOK_MODE` (`"default"|"verbose"|"off"`) + `QUIL_HOOK_HOME` (renamed from `QUIL_HOME`; consumers fall back to `QUIL_HOME` for one release). `ReadPersistedSessionID` consults `config.SessionsDir()/<paneID>.id` on restore. (OpenCode still uses an embedded JS plugin — see `internal/opencodehook/`.)

### `internal/claudesessions/`

Pure, stdlib-only discovery of the Claude Code transcripts recorded for a CWD (sibling of `gitdiscover`/`kubediscover`). `EscapeCWD` is the single definition of Claude's `~/.claude/projects/<escaped>` naming rule, transcribed from the claude binary (per UTF-16 code unit; >200 chars truncates + base36 Java-31x hash of the ORIGINAL cwd) — **moved here from `daemon.escapeClaudeCWD`**, which now consumes `TranscriptPath` for its restore probe, so the rule that silently breaks restore when wrong exists once. `List(cwd)` enumerates `*.jsonl` (regular files only — claude also keeps sibling DIRECTORIES named after session uuids), sorts mtime-desc, caps at `MaxSessions` (200), then head-reads each survivor up to `titleScanBytes` (64 KiB) for the first `"promptSource":"typed"` non-sidechain user entry and uses its text as the title (`message.content` is decoded as either a bare string or a content-block array). `sanitizeTitle` maps `\n\r\t` → space but DROPS other control chars (ESC included) — substituting would split `\x1b[31mtext` into two visible fragments — then truncates to `MaxTitleRunes` on a rune boundary. Every failure degrades to fewer/untitled sessions, never an error that blocks pane creation

### `internal/opencodehook/`

OpenCode session-id tracker + hook events forwarder. Embedded JS plugin (`scripts/quil-session-tracker.js`) loaded by opencode at spawn via `OPENCODE_CONFIG_CONTENT='{"plugin":["<abs path>"]}'`. Plugin file lives at `$QUIL_HOME/opencodehook/quil-session-tracker.js`. Two responsibilities: (1) session-id rotation tracking — per-pane ids at `$QUIL_HOME/sessions/opencode-<paneID>.id` (prefix avoids collision with claudehook's `<paneID>.id`); (2) hook events forwarding — filtered bus subscriptions (session.idle/error/compacted, session.status retry-only, file.edited batched 1 s) + typed handlers (permission.ask, experimental.session.compacting) append hookevents.Payload JSONL lines to `$QUIL_HOME/events/<paneID>.jsonl`. Per-pane token bucket (20/s sustained, 50 burst) drops with single warn-log when exhausted. UTF-8-aware truncation respects hook-side caps. `OPENCODE_CONFIG_CONTENT` MERGES with the user's existing opencode config so user plugins/agents/modes survive (verified against opencode 1.14.x). `ReadPersistedSessionID` `Lstat`-rejects symlinks. PTY env carries `QUIL_PANE_ID`, `QUIL_HOOK_HOME` (renamed from `QUIL_HOME`; JS consumer falls back to `QUIL_HOME` for one release), `QUIL_HOOK_MODE`, and the inline config content per opencode spawn

### `internal/hookevents/`

Hook-driven notifications pipeline. `Payload` wire schema (v=1, ts_ms, seq, pane_id, src=claude|opencode, hook_event, session_id, title, sev, data). `Spool` reads JSONL files at `$QUIL_HOME/events/<paneID>.jsonl` appended by the claude .sh / opencode .js hook producers; polled by `daemon.hookEventsWatcher` every 200 ms, tracks per-file byte offset, skips trailing partial lines. `Ingester` per-pane sliding-window rate limit (100/2s — on trip emits synthetic `internal.event_storm` then drops 10 s) + per-(paneID, hook_event, agent_type) 50 ms debounce coalescer (last-wins with `data["coalesced"]` burst count; `agent_type` joins the key only when non-empty — see the work-state section for why last-wins would otherwise erase a subagent's identity). Daemon-side translation `emitHookEvent(Payload) → PaneEvent` enriches with TabID/PaneName, sets `Pane.HookHealthy` + `Pane.LastHookEventAt`, routes through existing `emitEvent` (mute + aggregation + broadcast). `checkIdlePanes.shouldFire` skips the legacy idle excerpt when `HookHealthy && now-LastHookEventAt < 30 s` — fallback to legacy idle if hooks never load (plugin throws at init, settings JSON malformed). Spool init truncates stale files on daemon start; `DestroyPane` unlinks the spool file. Wire caps enforced hook-side (title ≤ 200, data value ≤ 128, total ≤ 2 KiB); daemon's PaneEvent caps (4 KiB / 1 KiB) are the outer backstop. Tier knob `[notification.hooks] claude = "default"|"verbose"|"off"` flows to scripts via `QUIL_HOOK_MODE` env at pane spawn

## Session-id rotation

### Claude Code session-id rotation tracking

`/clear`, `/resume`, and compaction rotate Claude's session id to a new jsonl file. Quil registers a `SessionStart` hook via `claude --settings '<inline JSON>'` at every spawn (never modifies `~/.claude/settings.json`) and passes `QUIL_PANE_ID=<paneID>` + `QUIL_HOOK_MODE` + `QUIL_HOOK_HOME` (renamed from `QUIL_HOME`; `hookHomeDir()` in `cmd/quild/hook.go` falls back to `QUIL_HOME` for one release) in the PTY env. The hook is the native `quild claude-hook` subcommand (`claudehook.RunHook` in `runhook.go`, dispatched in `cmd/quild`) — registered via `HookCommand(os.Executable())` → `"<quild>" claude-hook` — which reads Claude's stdin JSON, extracts `session_id` (validated by `validatePaneID` + a uuid regex), and atomically writes `$QUIL_HOME/sessions/<paneID>.id`. The file is a **two-line `SessionRecord`** — id, then the session's transcript path (`ReadPersistedSession`; `ReadPersistedSessionID` is the id-only wrapper the other call sites keep using). The path is recorded because it **cannot be derived**: Claude keys a transcript's project directory off the session's OWN working directory, so an agent that moves into a git worktree moves the transcript with it and the pane's spawn CWD points at a directory the file was never in. `parseSessionRecord` trims PER LINE — a whole-file `TrimSpace` would return both lines glued into one argv token — and rejects an over-long id line outright rather than truncating it. A session can also migrate MID-session, so `Stop` calls `refreshTranscriptPath` — which writes a **sidecar**, `<paneID>.transcript` (`<id>\n<path>`), never `<paneID>.id`. That split is structural, not stylistic: hook invocations are independent processes with no locking, so a read-modify-write of the id file could lose a race with a concurrent `SessionStart` and put the PRE-rotation id back, resurrecting the session the user just left. Confining the refresh to the sidecar makes the id unreachable from there — the worst a lost race can do is leave a path stale, and a stale path never renames a session. `ReadPersistedSession` merges the two, taking the sidecar's path only when its id matches the record's; a sidecar left by a previous session names a different id and is ignored. Older records carry no path; that is "unknown", never "missing".

On daemon restore, `resumeTemplateFor` (daemon.go) dispatches by plugin name to `claudeResumeTemplate`, which builds a candidate list (`claudeResumeCandidates`) in **source-authority order**: hook record → `PluginState["session_id"]` (refreshed only at shutdown, so it lags a rotation) → `PluginState["resume_session_id"]` (the user's pick at creation). **The CWD-derived probe is GONE.** Deriving a transcript's project directory from the pane's CWD was the root cause — Claude keys that directory off the SESSION's working directory — and removing it also removes an unbounded `os.Stat` from a path that runs before the IPC server listens. What remains is `transcriptExistsFn` on the *recorded* absolute path, through `statExistsWithinBudget`, which joins the same process-wide budget as the browse probes (`claimBlockingFSCall` + `transcriptProbeTimeout`) because the value is influenced by a pane's own child and persists on disk.

`transcriptState` classifies each candidate `located` / `unknown` / `missing`. A recorded path may only speak for the id it **names** (`filepath.Base(path) == id+".jsonl"`) — the path is an independent string, so without that binding any existing file would vouch for any id. An unanswered probe is `unknown`, never `missing`: a stat that times out on a dead mount is not proof a session was deleted. `usableResumeCandidates` drops only `missing` and **never reorders** — ranking a located low-authority id above an unlocated high-authority one would resume the pre-rotation conversation, the same silent swap in a narrower case. Every id is shape-checked against `resumeSessionIDRe` regardless of state, since a recorded path cannot stand in for that check the way an on-disk `<id>.jsonl` filename does; rejects are logged by LENGTH only, because the daemon log is rendered by the F1 viewer, which does not pass through a VT emulator. Both seams are package vars so tests never touch real `~/.claude/` or `$QUIL_HOME/sessions/`.

**`--continue` is not a neutral fallback and must never be reached with a known id.** It is Claude's most-recent-session-in-CWD lookup, so a pane whose own session we merely failed to LOCATE silently attaches to a sibling's conversation — and on restore the sibling that respawned a second earlier is exactly the one it finds. Shipped 2026-08-01 as three panes converging on one transcript, their claude processes interleaving appends into it, after a restart was the first since those panes were created. Hence an unlocated id is resumed anyway — a rejected id is a visible error, a wrong session is silent data loss — and `--continue` survives ONLY for a pane that recorded no session at all. "A session we refuse to name" is a different state and must not share that exit: `claudeResumeCandidates` returns `sawRecorded` alongside the list, and when every candidate was proven gone or rejected as malformed the pane takes a **fresh** identity (`freshClaudeSession`) instead. Minting that id is load-bearing rather than tidy — leaving the old value in `PluginState` would have the pane advertise a session it is not in, so the occupancy map would report it as the holder, and a later Alt+R (which spawns with `restoring=false`) would hand that id straight to `--session-id`.

Restore also gained the occupancy guard the create path has had since the resume picker shipped, as `(*Daemon).claimResumeSession` behind the `sessionClaimFn` seam (threaded through `resolveSpawnArgs` so the arg matrix stays a table test; `claimAny` is the no-occupancy stand-in, so the parameter is **never nil** and a forgotten wiring fails in a test rather than silently dropping the guard). It **selects and claims in one step**: an earlier version queried occupancy under `resumeClaimMu` and wrote the claim after releasing it, so two panes restoring concurrently could both observe the same session free — reachable in the ordinary case where two panes were left holding the same id. It walks the whole candidate list under one lock (a refused top candidate must not cost the pane its own next-best session) and builds the occupancy map ONCE, since building it re-walks every pane and re-reads every hook record inside the pre-listen readiness budget. Lock order is `resumeClaimMu` → `PluginMu`, matching `applyResumeSessionID`; `claudeResumeCandidates` captures every pane field under `PluginMu` and runs each probe after releasing it. `Daemon.Stop()` also calls `refreshPluginStateFromHooks()` before the final snapshot, copying the live hook-recorded id into `PluginState["session_id"]` for every claude-code and opencode pane so `workspace.json` carries the post-rotation id even if the hook file is later lost — empty/error reads preserve the existing value rather than clobbering with `""`. For claude-code it also copies the transcript path into `PluginState["transcript_path"]`, so a pane stays locatable when the hook file is gone (the path cannot be rebuilt from the pane's CWD — that is why it is recorded at all). **The id and the path are stored as ONE unit** — by `refreshPluginStateFromHooks` at shutdown AND by `recordResumeSession` when a claim lands: an id arriving without a path DELETES the key rather than leaving the previous session's behind, and a persisted path verifies only the id it was recorded with. A path that outlives its id would vouch for a transcript nobody checked, which is the same class of confidently-wrong answer as `--continue`. Both sources are consulted because either can be the survivor: the hook file dies with a wiped `$QUIL_HOME/sessions`, the workspace copy with a daemon killed before its shutdown refresh

### OpenCode session-id rotation tracking

opencode mints a new session id on `/new`, fork, or compaction. Quil registers a JS plugin via `OPENCODE_CONFIG_CONTENT='{"plugin":["<abs path>"]}'` at every spawn (never writes to `~/.config/opencode/`) and passes `QUIL_PANE_ID=<paneID>` + `QUIL_HOOK_HOME=<dir>` (renamed from `QUIL_HOME`; JS reads `QUIL_HOOK_HOME || QUIL_HOME` for one release) in the PTY env. The plugin — embedded in `internal/opencodehook/scripts/quil-session-tracker.js` and written to `$QUIL_HOME/opencodehook/` by `opencodehook.EnsureScripts()` — hooks opencode's `session.created` / `session.updated` / `session.idle` / `session.compacted` / `session.deleted` events, extracts `event.sessionID` / `event.session_id`, and atomically writes `$QUIL_HOME/sessions/opencode-<paneID>.id`. On daemon restore, `resumeTemplateFor` → `opencodeResumeTemplate` calls `readOpencodeSessionIDFn` (defaults to `opencodehook.ReadPersistedSessionID`) and promotes the resume args to `["--session", "{session_id}"]` when an id is present, falling back to `["--continue"]` otherwise. No session-exists probe in v1 — opencode handles stale ids itself; SQLite probe (`~/.local/share/opencode/opencode.db`) deferred to v2 if needed. `opencodeHookScriptStatFn` and `readOpencodeSessionIDFn` are swappable via package-level vars so tests never touch real filesystem state. Static templates (e.g. `--continue` with no `{placeholder}`) now pass through `resolveSpawnArgs`'s gate without requiring `PluginState` — see `templateHasPlaceholder` helper — so a fresh opencode pane that closed before its first session event still respawns with the fallback args

## Work state

### Work-in-progress indicators

`internal/tui/workstate.go` derives a per-pane `working` bool entirely TUI-side from the existing `paneEventMsg` stream (`Type == "hook.<src>.<event>"`) — no new IPC, no daemon state. `working` is derived from two inputs tracked per pane: `turnActive` (main turn) OR `len(subagents) > 0` (outstanding background subagents). Start edges (→ `turnActive`): `hook.claude.UserPromptSubmit`, `hook.opencode.chat.message`, `hook.claude.PreToolUse`.

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

It is work-state-only, like `PostToolUse` — `tui.workStateOnlyEvent`
(workstate.go) keeps both off the notification sidebar. Being a work-state edge
is NOT the test for that: `Stop`, `PermissionRequest` and a named `SubagentStop`
are exactly what the sidebar is for. The test is whether the event says anything
a user can act on, and a heartbeat that repeats every 15 s for as long as an
agent works says only "still running" — carded, it would bury the events that
need an answer under a progress log.

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

**`agent_type` is part of the ingester's coalesce key for exactly this reason** (`internal/hookevents/ingest.go` — `coalesceKey(paneID, hook_event, agent_type)`, appended only when non-empty so every other event keys as before). Coalescing is last-wins, so merging two DIFFERENT agents' starts would erase the loser's identity: its own stop would then match nothing while the winner's count never drained, wedging the spinner until `SessionEnd` — the ledger's identity guarantee only holds because the wire preserves it. A burst of the SAME agent still collapses to one emit with the burst count, which is what the count exists for. **The key's two free-form components are escaped** (`keyFieldEscaper`): `paneID` is NUL-free by `safePaneID` and stays first (so `Cancel`'s prefix match is unaffected), but `hook_event` and `agent_type` are arbitrary payload strings and JSON admits U+0000 in either — two variable fields joined by a separator either may contain is not injective, and `("SubagentStart", "\x00X")` would otherwise key identically to `("SubagentStart\x00", "X")`, coalescing them last-wins and erasing an identity. The escape is identity for every value a real producer emits. Claude Code runs subagents detached by default, so the main turn's `Stop` routinely fires while they still run: stop edges only end the spinner once the counter is drained, and the unseen mark is deferred to the drain edge (the LAST `SubagentStop` becomes the completion edge). Stop edges (→ persistent green unseen mark on the pane): `hook.claude.Stop`, `hook.opencode.session.idle`/`session.error`. `hook.claude.SessionEnd` is a *terminal* stop (`WorkEventStopFinal`): it also clears the subagent ledger (no subagent outlives its session — a lost SubagentStop must not wedge the spinner). `TaskCreated`/`TaskCompleted` are deliberately unmapped (task-list bookkeeping, not execution). Resume edge: `hook.claude.PostToolUse` (registered with a tool-name matcher `AskUserQuestion|ExitPlanMode` in `internal/claudehook` so it fires only for interactive-prompt tools — the user just answered → re-arm spinner; `workStart` clears the pane's unseen mark; suppressed from the notification sidebar as work-state-only). `process_exit` clears `working` AND the subagent ledger WITHOUT marking unseen (a crash is not a completed turn). A single shared 100 ms `workSpinnerTickMsg` animates the braille `spinnerFrames` on both the tab label (`tabLabel` prefix when `tabHasWorkingPane`) and each working pane's top-left border (left segment of `buildTopBorder`, reserved so the CWD truncation never eats the glyph); the loop self-stops via `workTickRunning` when no pane is working. `unseen` lives on `PaneModel` (set on workStop unless the pane is the focused pane of the active tab; cleared by `ackFocusedPane` at the single `Update` entry choke point — focusing the pane is the acknowledgement, no timer). Marked panes render a green border (precedence below active/ghost/MCP-highlight); background tabs derive a green label via `tabUnseen` + `unseenTabStyle` — `tabStyle(idx)` precedence is `blockedTabStyle` (amber, includes the active tab: the parked pane may be in an unfocused split) > `pinnedTabStyle` (purple 141, a pane pinned by hand — it had shared `unseenTabStyle`'s green, which made the mark the user set indistinguishable from the one the agent caused, and only the latter clears itself on focus) > `unseenTabStyle` (green, `tabUnseen` alone now) > custom tab color > active/inactive default, and is shared by `renderTabBar` + `hitTestTab` so rendered widths and click hit-testing never diverge. The active tab label never shows green (you're already looking at it); an unfocused split sibling still shows its green border. OpenCode's start edge is produced by the `chat.message` handler in `internal/opencodehook/scripts/quil-session-tracker.js`; Claude needs no producer change (both edges already arrive). State is not persisted — panes start idle on restart and the next hook event corrects them.

Park-for-input edges (`hook.claude.PermissionRequest`,
`hook.opencode.permission.ask`) set `blockedSince` and do **NOT** clear
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

