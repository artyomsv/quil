# Claude Code — Resume an Existing Session at Pane Creation

**Date:** 2026-07-25
**Status:** Approved, ready for planning

## Problem

Creating a Claude Code pane always starts a fresh session. To pick up an earlier
conversation you must open the pane, then run `/resume` inside it and wait for
Claude's own picker to boot and render. That picker is also thin: it does not
show enough per row to tell two sessions apart at a glance.

The goal is to choose the session *before* the pane spawns, from a list Quil
renders itself — instantly, and with a useful title on every row.

## Non-goals

- Resuming a session for an **already running** pane (no "switch this pane to
  session X" action). Pane creation only.
- Cross-directory / global "recent sessions everywhere" browsing. The list is
  always scoped to the directory selected in the setup dialog.
- OpenCode session resume. OpenCode stores sessions in SQLite
  (`~/.local/share/opencode/opencode.db`), a different discovery problem. The
  plugin key is designed so it can opt in later, but no OpenCode code ships here.
- Exposing resume through the MCP `create_pane` tool.

## User-facing behavior

The Claude Code setup dialog gains a **Session** field between the working
directory and the runtime toggles.

Unfocused — the common case, one line, dialog height unchanged:

```
Claude Code — Setup

> Working directory:
  > E:\Projects\Stukans\quil
    E:\Projects\Stukans\monorepo
    Browse…

  Session:  New session          (Tab here to resume)

  ( ) Dangerously skip permissions
  ( ) Enable auto mode
  [ ] Chrome support

  [Continue]
```

Focused — expands in place into a scrolling list:

```
> Session:
  > (o) New session
    ( ) 2h ago   Add resume option to claude pane setup dialog
    ( ) 1d ago   fix(update): release only our own apply lock
    ( ) 3d ago   I would like to add more mouse controls. For e…
    ( ) 5d ago   Lets fix the daemon restart abort path
    ( ) 6d ago   review the wedge regression tests
    2/22  ↑↓ move  Enter select
```

Row format is one line: relative age, then the title. Row 0 is always
`New session` and is pre-selected, so a user who never touches the field gets
exactly today's behavior.

Sessions already open in another live pane render greyed with an
`[open in 2.Claude]` marker. The cursor may land on them — a footer hint then
explains why they are blocked — but Enter is refused. This mirrors how the
Ctrl+N plugin list treats an uninstalled binary (`dialog.go:1193`), rather than
the command palette's skip-the-cursor behavior; landing on the row is what lets
the user read the explanation.

Changing the working directory discards any session selection and triggers a
fresh scan on the next focus. A session from another project is meaningless.

## Architecture

Three units, each independently testable.

### 1. `internal/claudesessions/` — pure discovery

Stdlib only, no daemon dependencies. Sibling of `internal/gitdiscover` and
`internal/kubediscover`.

```go
type Session struct {
    ID       string    // uuid = .jsonl filename stem
    Title    string    // first `"promptSource":"typed"` user prompt
    Modified time.Time // file mtime
}

func ProjectDir(cwd string) string       // ~/.claude/projects/<escaped>
func List(cwd string) ([]Session, error) // mtime-desc, bounded reads
```

`escapeClaudeCWD` **moves here** from `internal/daemon/daemon.go:2060` and
becomes the exported `ProjectDir`. The daemon's `claudeSessionFileExists` calls
it instead of keeping a private copy, so Claude's on-disk naming rule has one
definition. The existing `escapeClaudeCWD` test cases move with it.

Title extraction reads at most `titleScanBytes` (64 KB) from the head of each
`.jsonl` and stops at the first entry with `"promptSource":"typed"`. In every
file sampled from this repo the first typed prompt was within the first ~16 KB
(largest observed offset: byte 14 442 in a 3 MB transcript), so the head window
is generous. A file with no typed prompt in that window yields an empty title
and the row falls back to showing the session UUID.

Bounds and failure behavior:

- `maxSessions` = 200, newest first; overflow sets a `Truncated` flag upstream.
- Title truncated to `maxTitleRunes` (240) on the wire, cut on a rune boundary;
  the TUI truncates again to the render width.
- Control characters stripped from titles (a prompt may contain ANSI escapes,
  and the value is rendered into a TUI). Same defense `kubediscover.sanitize`
  applies.
- Malformed JSON lines are skipped, not fatal.
- Missing project directory returns an empty slice and a nil error — a
  directory with no Claude history is normal, not an error.

### 2. Daemon — IPC pair and live-session cross-check

New message pair:

```go
MsgClaudeSessionsReq  = "claude_sessions_req"
MsgClaudeSessionsResp = "claude_sessions_resp"
```

```go
type ClaudeSessionsReqPayload struct {
    CWD string `json:"cwd"`
}

type ClaudeSessionInfo struct {
    ID         string `json:"id"`
    Title      string `json:"title"`
    ModifiedMs int64  `json:"modified_ms"`
    InUseBy    string `json:"in_use_by,omitempty"` // pane display name
}

type ClaudeSessionsRespPayload struct {
    CWD       string              `json:"cwd"` // echoed verbatim
    Sessions  []ClaudeSessionInfo `json:"sessions"`
    Truncated bool                `json:"truncated,omitempty"`
    Error     string              `json:"error,omitempty"`
}
```

The handler runs on a **worker goroutine**, never the dispatch goroutine — the
rule `MsgStageUpdateReq` already follows, because this is file I/O and a slow
disk would otherwise stall every other pane's IPC.

The in-use map is built in two phases to respect the lock discipline in
`CLAUDE.md`: snapshot `(paneID, type, displayName, PluginState["session_id"])`
under `sm.mu`, **release the lock**, then read each claude-code pane's
`$QUIL_HOME/sessions/<paneID>.id` off-lock via
`claudehook.ReadPersistedSessionID`. The hook file is authoritative — it
reflects `/clear`, `/resume`, and compaction rotations — and `PluginState` is
the fallback for a pane whose hook has not fired yet.

`CWD` is echoed back **verbatim**, unmodified, as the TUI's staleness check.
This is the same contract `paneSearchResponse` documents for its query: any
daemon-side normalization would make a request look permanently stale.

Discovery itself is delegated to `claudesessions.List`; the daemon only adds
the in-use annotation, the cap, and the response envelope. The pure part is
split out as `claudeSessionsResponse(cwd, sessions, inUse)` so the envelope
logic is testable without a running daemon.

### 3. TUI — collapsed field and request plumbing

New file `internal/tui/sessions.go`, structured like `palette_search.go`.

Model state:

| Field | Purpose |
|---|---|
| `sessionRows []ipc.ClaudeSessionInfo` | Rows from the last accepted response |
| `sessionCursor int` | 0 = "New session"; 1..N = `sessionRows[i-1]` |
| `sessionScroll int` | Scroll offset into the row list |
| `sessionScanCWD string` | CWD the current rows belong to |
| `sessionState` | `idle` / `scanning` / `ready` / `timeout` / `error` |
| `selectedSessionID string` | Committed choice, empty = new session |

The request fires when the Session field is focused and `sessionScanCWD` does
not match the current `cwdBrowseDir` — so a fresh pane with no resume costs
zero I/O, and re-focusing without changing directory reuses the rows.

A 3 s `tea.Tick` turns a never-answered request into a diagnosable
`Search timed out — is the daemon running?`-style row rather than an endless
`Scanning…`. Like the palette's timer it is a **local** timer and must not
re-arm `listenForMessages`.

The `claudeSessionsMsg` Update branch **must** re-arm `m.listenForMessages()`.
Omitting it kills the IPC listen loop — the trap already documented for
`historyListMsg` and `memoryReportMsg`.

Responses whose echoed `CWD` differs from the current `cwdBrowseDir` are
dropped as stale.

## Data flow

```
focus Session field
  → MsgClaudeSessionsReq{CWD: cwdBrowseDir}
  → daemon worker: claudesessions.List + in-use annotation
  → MsgClaudeSessionsResp{CWD echoed, Sessions}
  → TUI drops if stale, else renders rows
  → Enter on a row  → selectedSessionID
  → Continue        → CreatePanePayload.ResumeSessionID
  → daemon          → PluginState["resume_session_id"]
  → resolveSpawnArgs → claude --resume <id>
```

## Spawn path

One new payload field:

```go
type CreatePanePayload struct {
    ...
    // ResumeSessionID resumes an existing Claude session instead of starting
    // a fresh one. Trust: same socket trust model as Overlay — any IPC client
    // can set it, so the daemon validates it as a UUID before it reaches argv.
    ResumeSessionID string `json:"resume_session_id,omitempty"`
}
```

Three daemon edits:

**1. `handleCreatePane`** — validate `ResumeSessionID` against the UUID pattern
already used by `claudehook`, then store it as
`pane.PluginState["resume_session_id"]`. An invalid value is logged once and
ignored, spawning a fresh session: a malformed id should degrade to a working
pane, not a failed spawn.

**2. `spawnPane` (`daemon.go:2313`)** — seed instead of mint:

```go
if pane.PluginState["session_id"] == "" {
    if rid := pane.PluginState["resume_session_id"]; rid != "" {
        pane.PluginState["session_id"] = rid
    } else {
        pane.PluginState["session_id"] = uuid.New().String()
    }
}
```

so `refreshPluginStateFromHooks`, the model/context status segment, and restore
all observe a coherent id from the first instant.

**3. `resolveSpawnArgs` (`daemon.go:2219`)** — the only behavioral branch:

```go
if !restoring && p.Persistence.Strategy == "preassign_id" {
    if rid := pane.PluginState["resume_session_id"]; rid != "" {
        args = append(args, "--resume", rid)   // instead of StartArgs
    } else if len(p.Persistence.StartArgs) > 0 {
        ...existing expansion...
    }
}
```

`--session-id <new>` and `--resume <existing>` are contradictory, so this is a
replacement, not an append. Because the branch runs *after* `InstanceArgs`, the
permission-mode and `--chrome` toggles compose with `--resume` unchanged.

### Restore interaction

Deliberately none. Once claude boots, its `SessionStart` hook writes the live id
to `$QUIL_HOME/sessions/<paneID>.id`, and `claudeResumeTemplate` reads that on
every later restore. A resumed pane rejoins the existing machinery with no
special-casing.

One narrow gap is worth closing: if the daemon restarts in the seconds *before*
that hook fires, there is no hook file and the preassigned-id probe fails, so
`claudeResumeTemplate` falls back to `--continue` (claude's most-recent-in-CWD
lookup) — possibly a different session than the user picked. Add
`resume_session_id` as the final fallback in that chain, ahead of `--continue`.
`PluginState` is persisted in `workspace.json`, so the value is already there.

That is also why `resume_session_id` is **not** cleared after spawn: retaining
it costs one UUID in the snapshot and buys that fallback. It is only ever read
on the fresh-spawn branch, so a stale value cannot affect a restore.

## Plugin schema

New key under `[command]`:

```toml
sessions = "claude"
```

`Command.Sessions string`. Valid values: `""` (off, default) and `"claude"`.
Any other value fails plugin load, mirroring how `discover` rejects unknown
values. `defaults/claude-code.toml` sets it and bumps `schema_version` 8 → 9,
which shows the plugin-migration dialog once on next launch.

The TUI renders the Session field for any plugin declaring
`sessions = "claude"`; only the resolver behind it is Claude-specific.

## Error handling

| Condition | Behavior |
|---|---|
| Project directory missing (no history for this CWD) | Empty list, `New session` only, no error row |
| Daemon never answers | 3 s timeout → explicit timed-out row; `New session` still selectable |
| Daemon returns `Error` | Error row rendered dim; `New session` still selectable |
| `.jsonl` unreadable / malformed | That file skipped; the rest still listed |
| No typed prompt in the head window | Row shows the session UUID instead of a title |
| More than 200 sessions | Newest 200 listed, `Truncated` note in the footer |
| Session in use by a live pane | Greyed, marked, Enter refused |
| CWD changed after a scan | Rows and selection reset; rescan on next focus |
| `ResumeSessionID` not a UUID | Logged once, ignored, fresh session spawned |

The through-line: every failure still leaves a working `New session` path. The
picker is an accelerator and must never block pane creation.

## Testing

**`internal/claudesessions`**
- `ProjectDir` table tests — the ported `escapeClaudeCWD` cases (Windows drive
  colon, underscore, dot, the 200-char truncation + base36 hash tail).
- `List` against synthetic `.jsonl` files in `t.TempDir()`: title extraction,
  mtime-desc ordering, malformed-line skip, missing-prompt fallback, no-typed-
  prompt-in-window fallback, `maxSessions` cap, control-character stripping.

**`internal/daemon`**
- `claudeSessionsResponse` pure tests: in-use annotation, verbatim CWD echo,
  truncation flag.
- `resolveSpawnArgs` table additions: fresh + `resume_session_id` emits
  `--resume` and no `--session-id`; fresh without it is unchanged; restoring is
  unchanged; toggles still compose.
- `claudeResumeTemplate`: falls back to `resume_session_id` ahead of
  `--continue` when no hook id exists.

**`internal/tui`**
- `setupFieldCount` / `setupFieldKind` with the field present and absent.
- Cursor lands on but cannot select an in-use row.
- Stale response (mismatched CWD) is dropped.
- Changing CWD resets `selectedSessionID` and the rows.
- Collapsed vs expanded render snapshots.

**`internal/plugin`**
- `sessions` parsed; unknown value fails load.

Test fixtures are synthetic `.jsonl` files in `t.TempDir()` that never reach a
user-visible surface, and no test touches real `~/.claude` or `~/.quil` state.

## Files touched

| File | Change |
|---|---|
| `internal/claudesessions/claudesessions.go` | New — discovery + `ProjectDir` |
| `internal/claudesessions/claudesessions_test.go` | New |
| `internal/ipc/protocol.go` | Message pair, payloads, `ResumeSessionID` |
| `internal/daemon/sessions.go` | New — handler + pure response builder |
| `internal/daemon/daemon.go` | `escapeClaudeCWD` removal, 3 spawn edits, resume-template fallback |
| `internal/plugin/plugin.go` | `Command.Sessions` + validation |
| `internal/plugin/defaults/claude-code.toml` | `sessions = "claude"`, schema_version 9 |
| `internal/tui/sessions.go` | New — request/response/timeout plumbing |
| `internal/tui/dialog.go` | Field insert, render, key handling |
| `internal/tui/model.go` | Model state fields |
| `docs/plugin-reference.md`, `docs/features.md` | The `sessions` key and the picker |
