# Codex plugin — design

**Date:** 2026-09-04
**Status:** Approved in chat (option A: full plugin now, resume picker later)
**Verified against:** codex-cli 0.146.0 (native `codex.exe`, Windows 10), source at
`openai/codex@main` via opensrc

## Summary

A third AI pane plugin, `codex`, with the same capabilities the `claude-code` plugin has:
hook-driven notifications, the work-in-progress indicator (spinner, green tab, amber
"needs you" tab, subagent ledger), model + context-token status segment, input history,
and per-pane session resume across daemon restarts. Delivered as one PR on `feat/codex`.

Out of scope for this PR, deliberately: the setup dialog's "resume an existing session"
picker (`sessions = "codex"`). Codex keeps every session in one date-sharded tree
(`~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl`) rather than per-project
directories, so listing sessions for a CWD needs a new reader package and a source field
on the sessions IPC. It is a self-contained follow-up.

## What was verified about codex

Facts below were measured on the installed binary or read from the source, not assumed.

1. **Codex has Claude-compatible hooks** (feature `hooks`, stage `stable`). Events:
   `SessionStart`, `SessionEnd`, `UserPromptSubmit`, `PermissionRequest`, `PreToolUse`,
   `PostToolUse`, `Stop`, `SubagentStart`, `SubagentStop`, `PreCompact`, `PostCompact`.
   Newer codex also has `Interrupt` (the ESC edge); 0.146.0 does not. There is no
   `Notification`, `StopFailure`, `TaskCreated` or `TaskCompleted`.
2. **The stdin JSON uses Claude's field names**: `hook_event_name`, `session_id`,
   `transcript_path` (nullable), `cwd`, `tool_name`, `tool_input`, `agent_id`,
   `agent_type`, `prompt`, `model`, plus codex extensions `turn_id`, `trigger`
   (compaction, `manual|auto`), `last_assistant_message`, `stop_hook_active`. A
   subagent's events carry `agent_id` and `agent_type`; main-turn events carry neither.
   Root turns run `Stop`; thread-spawned child turns run `SubagentStop`.
3. **Hooks must be trusted or they silently do not run.** Trust is a per-hook
   `trusted_hash` under `hooks.state."<key>"`, read from the USER config layer and from
   the SESSION-FLAGS layer (the `-c key=value` overrides). Quil therefore injects the hook
   AND its trust in one argv token:

       -c hooks={<Event>=[{hooks=[{type="command",command="<cmd>"}]}],…,state={"<key>"={trusted_hash="sha256:<hex>"}}}

   Probe: a `SessionStart` hook passed this way fired under `codex exec`; the same hook
   without `state` did not. Nothing under `~/.codex` is read for this or written.
4. **Key and hash.** Key = `<synthetic path>:<event_snake>:<group>:<handler>`, where the
   synthetic path is `C:\<session-flags>\config.toml` on Windows and
   `/<session-flags>/config.toml` elsewhere; group and handler are `0` for one handler per
   event. Hash = `sha256:` + hex(SHA-256(canonical compact JSON with keys sorted)) of
   `{"event_name":"<event_snake>","hooks":[{"async":false,"command":"<cmd>","timeout":<t>,"type":"command"}]}`.
   `<t>` is the NORMALIZED timeout: 600 for every event except `SessionEnd`, which
   defaults to 1 (max 3). No `matcher` key when the matcher is unset. Verified value:
   command `echo hooked > hookfired.txt`, `session_start`, timeout 600 →
   `sha256:5be9d5089b64165ae3661e509b80789b0e314361111505f1b2c86a5490dbb86e`.
5. **`-c` is global.** `codex -c … resume <id>` parses; so does a top-level toggle placed
   before `resume`. The interactive TUI's startup hook review only prompts for hooks whose
   status is Untrusted or Modified, so a correctly hashed hook never prompts.
6. **Hook commands run through the shell** (`%COMSPEC% /C` on Windows, `$SHELL -lc`
   elsewhere) with the codex PROCESS environment (default
   `shell_environment_policy.inherit = "all"`), so `QUIL_PANE_ID`, `QUIL_HOOK_HOME`,
   `QUIL_HOOK_MODE` and `QUIL_RECORD_HISTORY` reach the hook exactly as they do for Claude.
7. **Resume is `codex resume <SESSION_ID>`** (a subcommand, UUID). `resume --last` is
   codex's most-recent-session lookup — the same silent-sibling trap `claude --continue`
   is, and is never emitted. Session ids are UUIDv7; `/new` mints a new id, and
   `SessionStart` fires again with `source = "clear"`.
8. **Model and usage.** The `Stop` payload carries `model`. The rollout file (its path is
   the hook's `transcript_path`) holds `{"type":"event_msg","payload":{"type":"token_count",
   "info":{"last_token_usage":{…,"total_tokens":N},"model_context_window":W}}}` lines;
   codex's own `tokens_in_context_window()` is `total_tokens` of the last usage.

## Architecture

### `internal/codexhook/` (new)

Sibling of `internal/claudehook/`, same shape, no shared code with it. The Claude producer
is a hot, incident-hardened path; adding a `Source` branch to it for a second tool would
put codex-only differences (rollout usage, no prompt-tool gate, a different record file)
inside it, and the repo already accepts parallel producers (`opencodehook`).

- `codexhook.go`
  - `HookCommand(exePath) string` → `"<quild>" codex-hook` (mirrors claudehook).
  - `BuildConfigOverride(cmd string, goos string) (string, error)` → the TOML value for
    `-c hooks=…`: one matcher-less group per registered event plus `state` with one
    `trusted_hash` per event. Golden-tested against the probe-verified hash. The hash is
    computed with `encoding/json` on a sorted map with `SetEscapeHTML(false)` — Go escapes
    `<>&` by default and codex does not.
  - `ConfigOverrideArgs(cmd, goos) ([]string, error)` → `["-c", "hooks=<value>"]`.
  - `registeredEvents`: `SessionStart`, `SessionEnd`, `UserPromptSubmit`,
    `PermissionRequest`, `PreToolUse`, `Stop`, `SubagentStart`, `SubagentStop`,
    `PreCompact`, `PostCompact`. NOT `PostToolUse` (codex has no `AskUserQuestion` /
    `ExitPlanMode`; the answered-prompt resume edge is covered by
    `answerBlockedByInput`), NOT `Interrupt` (absent from 0.146.0; the TUI already
    synthesises the ESC stop for every pane).
  - `IsShim(resolvedCmd) bool` — true for `.cmd`/`.bat`. An npm-installed `codex` is a
    `.cmd` shim that cmd.exe re-parses, and the inline `-c` value carries quotes: the same
    bug class as the inline Claude `--settings` JSON, with no file form available here.
    The spawn prep logs and skips the hook prefix in that case rather than passing an
    argument that may be split at the wrong quote.
  - Session record: `sessions/codex-<paneID>.id`, two lines (id, transcript path) written
    atomically by `SessionStart`; `ReadPersistedSession` / `ReadPersistedSessionID`
    (Lstat-rejects symlinks, same as opencodehook). Prefixed so the claude reader
    (`<paneID>.id`) and this one stay disjoint by construction.
  - `IsValidSessionID(id)` — canonical UUID regex, the shape codex mints.
- `runhook.go` — `RunHook(r, HookEnv, nowMs)`: same contract as claudehook's (empty pane
  id → no-op; never blocks the tool; breadcrumbs to `$QUIL_HOME/codexhook/hook.log`).
  Dispatch:
  | event | action |
  |---|---|
  | `SessionStart` | write record (id + transcript path) |
  | `SessionEnd` | spool "Session ended" (info) |
  | `UserPromptSubmit` | history append when `QUIL_RECORD_HISTORY=1`; spool "Working on: …" with `prompt_preview` |
  | `PermissionRequest` | spool "Needs approval: <tool>" (warning), `data.tool` |
  | `PreToolUse` | drop when `agent_id` set; drop in off mode; drop when spool mtime < 15 s old; else spool "Working" with `data.tool` |
  | `Stop` | spool "Reply ready" (warning) with `data.model` from the payload and `data.context_tokens` from the rollout tail |
  | `SubagentStart` | spool "Spawned: <agent_type>", `data.agent_type` |
  | `SubagentStop` | drop when `agent_type` empty; else "<agent_type> done" |
  | `PreCompact` | "Compacting context (<trigger>)", `data.trigger` |
  | `PostCompact` | "Compaction complete", `data.compacting = "1"` (never read usage here — same reasoning as Claude) |
  | other | breadcrumb, ignore |
- `rollout.go` — `readRolloutUsage(path) (contextTokens int64, ok bool)`: tail-read
  (256 KB) the last `token_count` line, return `info.last_token_usage.total_tokens`.
  Absolute `.jsonl` paths only. Best-effort with the same short retry ladder Claude uses,
  since the final line lands asynchronously around `Stop`.

### `cmd/quild/`

`codex-hook` fast-path subcommand beside `claude-hook` (before dev-mode env handling, for
the same reason).

### `internal/hookevents/`

- `SourceCodex = "codex"`; `Validate` accepts it.
- `ClassifyWorkEvent`: `hook.codex.UserPromptSubmit` / `PreToolUse` → Start;
  `hook.codex.Stop` → Stop; `SessionEnd` → StopFinal; `SubagentStart` / `SubagentStop`;
  `PermissionRequest` → Park.
- `IsWorkStateOnly` and `IsWorkHeartbeat` include `hook.codex.PreToolUse`.

### `internal/config/`

`HookNotificationsConfig.Codex` (`[notification.hooks] codex = "default"`), defaulted
with the others.

### `internal/daemon/`

- `codexSpawnPrep(quilDir, paneID, hookMode, resolvedCmd) (prefix, env []string)`:
  resolves the quild path via the existing `claudeHookExeFn` seam, builds the override,
  returns `["-c", "hooks=…"]` + the three `QUIL_*` env vars; nil prefix (env still set)
  when the exe is unresolvable, the override fails, or `resolvedCmd` is a shim.
- `spawnPane`: new `case p.Name == "codex"` arm, before the claude arm like opencode.
  Prefix goes BEFORE user args and the `resume` subcommand (`-c` is global).
- `resumeTemplateFor`: `p.Name == "codex" && Strategy == "session_scrape"` →
  `codexResumeTemplate`: recorded id (shape-checked) → `["resume", "{session_id}"]`, else
  the plugin's `ResumeArgs`, which the shipped TOML leaves EMPTY: a pane with no recorded
  session starts fresh. `readCodexSessionFn` is the test seam, mirroring
  `readOpencodeSessionIDFn`.
- `refreshPluginStateFromHooks`: codex arm copies id + transcript path as one unit.
- `emitHookEvent`: the off-mode switch gains `SourceCodex`.
- No occupancy guard and no transcript probe in v1 — same level as opencode. With the
  fresh-start fallback, the worst a lost record costs is the conversation, never a
  sibling's.

### `internal/plugin/`

- `defaults/codex.toml`: `cmd = "codex"`, `detect = "codex --version"`,
  `prompts_cwd = true`, `record_history = true`, `sessions = ""`. Toggles: group
  `permission_mode` — "Bypass approvals and sandbox (dangerous)"
  (`--dangerously-bypass-approvals-and-sandbox`) and "Auto: workspace-write sandbox, never
  ask" (`-a never -s workspace-write`); independent "Web search" (`--search`).
  `[persistence] strategy = "session_scrape"`, `resume_args = []`, `ghost_buffer = false`
  (codex runs on the alternate screen by default; a replay would be garbage, and `resume`
  repaints the conversation). No `redraw_key` (unmeasured; the resize fallback stays).
  `[display] wide_canvas = true`. Error handler for "not logged in" → "Run `codex login`".
  Idle handlers as opencode's.
- `CodexPluginName = "codex"` beside `ClaudeCodePluginName`.

### `internal/tui/`

`resumeLabel`: `case "codex": "resuming codex"`. Nothing else: work state, the status
segment, the interrupt synthesis and the sidebar all key on `hookevents` and on data keys.

### Docs and release

`docs/plugin-reference.md`, `docs/features.md`, `docs/configuration.md`, `docs/mcp.md`,
`docs/troubleshooting.md`, `README.md`, `docs/quick-start.md`, `docs/keybindings.md`
gain codex beside opencode; `.claude/rules/hooks-and-sessions.md` gets an
`internal/codexhook/` section and its `paths:` glob; `.claude/CLAUDE.md` lists the
package. One changelog fragment `changelog.d/added-codex-plugin.md` with a headline.

## Error handling

Every failure degrades the way the Claude path does: a spawn never fails because of the
hook. Unresolvable exe, override build error, shim → one daemon log line, pane spawns
without hooks, legacy idle detection covers it. Hook-side: a malformed pane id, bad JSON
or unwritable spool logs a breadcrumb and exits 0. A recorded id that fails the UUID shape
is logged by length and the pane starts fresh.

## Testing

- `codexhook`: golden hash (the probe value) and key format per GOOS; override contains
  every registered event and one `state` entry per event; `SessionEnd` hashes with
  timeout 1; `RunHook` table (record written on `SessionStart`; each spool mapping; the
  three `PreToolUse` drops; unnamed `SubagentStop` dropped; off mode; unknown event;
  history append gated on env); rollout usage reader on a fixture tail; symlink refusal;
  `IsShim`.
- `hookevents`: `Validate` accepts codex; classifier + work-state-only + heartbeat cases.
- `daemon`: `codexSpawnPrep` env (`QUIL_HOOK_HOME`, never `QUIL_HOME`) and prefix shape;
  shim → no prefix; `resolveSpawnArgs` restore matrix for codex (recorded → `resume <id>`,
  missing/empty/malformed → fresh); `resumeTemplateFor` dispatch; refresh arm.
- `plugin`: `codex.toml` loads with the expected strategy, toggles, group, empty
  `resume_args`.
- `config`: `[notification.hooks] codex` round-trips.
- Runtime check (dev daemon): open a codex pane, send a prompt → spinner, then green tab;
  restart the daemon → pane resumes the same session; `dev.sh test` green.

## Follow-ups (not this PR)

- `sessions = "codex"` resume picker: `internal/codexsessions/` walking the date tree and
  matching `session_meta.cwd`, a `Source` on the sessions IPC, registry acceptance.
- Register `Interrupt` once the installed codex carries it (0.15x): exact ESC edge.
- Occupancy guard for codex sessions if two panes are ever observed on one rollout.
