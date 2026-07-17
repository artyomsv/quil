# `quil status` — Design Spec

**Date:** 2026-07-09
**Milestone:** M5 (Polish) — closes the "Observability commands — `quil status`, session metrics" remaining item.
**Status:** Approved (design)

## Problem

Quil ships a minimal `quil daemon status` (`cmd/quil/main.go:233` → `daemonStatus()`)
that prints only daemon liveness and pid. The roadmap's M5 observability line
also calls for a top-level `quil status` with **session metrics** — tabs, panes,
per-pane state, memory. That half is unbuilt.

There is no need for new daemon state or protocol messages: everything except
uptime is already reachable by composing existing request-response IPC.

## Goals

- A top-level `quil status` command with a human-readable default and a
  `--json` machine format.
- Report daemon liveness, pid, version, environment (prod/dev), approximate
  uptime, and workspace session metrics (tab/pane counts, per-pane state, memory,
  pending notification events).
- Zero daemon-side protocol changes (lowest-risk path — "finish M5").
- Distinguish a **wedged** daemon (accepts the connection but never answers)
  from a **dead** one, via a distinct exit code.

## Non-goals

- No new daemon-side `MsgStatusReq/Resp` endpoint (rejected in favor of composing
  existing messages).
- No exact daemon-recorded uptime (approximated from the PID file mtime instead).
- No live-watching / `--watch` mode. One-shot only.
- No changes to the TUI.

## Command surface

```
quil status [--json] [-v|--verbose]
```

- New `case "status"` in the top-level switch (`cmd/quil/main.go`, alongside
  `daemon` / `mcp` / `version` / `restart`).
- `quil daemon status` is **rewired to call the same implementation** so both
  invocations share one code path. The old thin `daemonStatus()` is removed.
- `--dev` needs no special handling: the existing arg-preprocessing that sets
  `QUIL_HOME` before the switch runs already retargets all derived paths, so
  `quil status --dev` inspects the dev daemon.
- Flag parsing is local to the command (simple manual scan of the remaining
  args — matches how the rest of `cmd/quil` handles subcommand flags). Unknown
  flags print a usage line to stderr and exit non-zero.

## Data flow

New file: `cmd/quil/status.go`.

1. `client, err := ipc.NewClient(config.SocketPath())`.
   - Dial error ⇒ daemon not running (see **Error behavior**).
2. **Synchronous round-trip helper** (local to `status.go`):
   - Generate a fresh `uuid` `ID`.
   - `client.SetReadDeadline(time.Now().Add(statusTimeout))` (`statusTimeout = 2s`).
   - `client.Send(msg)` with `msg.ID = id`.
   - Loop `client.Receive()`, skipping any frame whose `ID != id` (ignores
     broadcast noise; the status command never attaches). Return on the matching
     frame, decode its payload, or surface a deadline/timeout error.
   - Mirrors the existing pre-attach version-handshake pattern.
3. Three composed requests:
   - `MsgVersionReq` → `VersionRespPayload.Version`.
   - `MsgListPanesReq` → `ListPanesRespPayload.Panes` (`PaneInfo`: id, tab_id,
     tab_name, name, type, cwd, running, pending, instance_name).
   - `MsgMemoryReportReq` → `MemoryReportRespPayload` (`Panes []PaneMemInfo`,
     `Total`, plus a `Tabs []TabInfo` view carrying id/name/active/pane_count).
   - Tab grouping/metadata comes from `MemoryReportRespPayload.Tabs` (no
     separate `MsgListTabsReq` needed); per-pane details from `list_panes`;
     merge panes ↔ memory by pane ID (`mergePaneMemory`).
4. **Uptime:** `os.Stat(config.PidPath()).ModTime()`, rendered as an approximate
   duration (`uptime ~2h13m`). The PID file is written once at daemon startup, so
   its mtime is a faithful (clearly-hedged) proxy. If the stat fails, the uptime
   segment is omitted rather than faked. Honors the no-synthetic-data rule — a
   real derivation, labeled approximate, never a fabricated exact value.
5. **Environment label:** reuse the `production | dev (<dir>)` helper that
   `quil daemon stop` already prints (dev = `QUIL_HOME` set). If a single shared
   helper does not yet exist, extract the existing inline logic into one so both
   call sites share it.

### Pending-events count

Obtained from `MsgGetNotificationsReq` → `GetNotificationsRespPayload.Events`
length. If the round-trip fails or the daemon is older and does not answer, the
`events` segment is omitted (never a hard error) — the rest of the report still
renders.

## Human output (default)

```
quil ● running    pid 48213    v1.34.2    production    uptime ~2h13m

tabs 4    panes 12 (10 running · 2 pending)    mem 84.2 MB    events 3 pending

1:Shell *
  ├ shell        terminal     running     1.1 MB
  └ build        terminal     running     3.4 MB
2:AI ●
  ├ claude       claude-code  running    28.7 MB
  └ notes        terminal     pending       —
3:...
```

- `*` after a tab label = active tab (from `TabInfo.Active`). The eager-pane
  `●` marker is **not** shown in v1: the `Eager` flag is not carried on the
  `PaneInfo`/`TabInfo` wire types and surfacing it would require a daemon-side
  change, which the zero-daemon-change approach rules out.
- Pending (not-yet-spawned) panes render `—` for memory — no PTY exists yet.
- `-v` / `--verbose`: additionally show each pane's CWD under its row. Without
  `-v`, the per-tab tree is still shown (per the approved detail level); `-v`
  only adds CWD lines.

Memory sizes via `formatBytes` (KB/MB/GB, one decimal). Uptime via
`formatUptime` (`2h13m`, `3d4h`, `<1m`).

## JSON output (`--json`)

One object, full tree regardless of `-v`:

```json
{
  "running": true,
  "responding": true,
  "pid": 48213,
  "version": "1.34.2",
  "environment": "production",
  "environment_dir": "",
  "uptime_seconds": 7980,
  "started_at": "2026-07-09T08:12:04Z",
  "totals": {
    "tabs": 4,
    "panes": 12,
    "running": 10,
    "pending": 2,
    "memory_bytes": 88267489,
    "pending_events": 3
  },
  "tabs": [
    {
      "id": "…", "name": "Shell", "active": true,
      "panes": [
        {"id":"…","name":"shell","type":"terminal","running":true,"pending":false,"cwd":"/home/x","memory_bytes":1153433}
      ]
    }
  ]
}
```

- `uptime_seconds` / `started_at` omitted (or null) when the pidfile stat fails.
- `pending_events` omitted when the notifications round-trip fails.
- `environment_dir` carries the dev `QUIL_HOME` path when `environment == "dev"`,
  empty otherwise.

## Error / edge behavior

| Condition | Human | JSON | Exit |
|-----------|-------|------|------|
| Healthy | full report | full object | `0` |
| Dial fails (not running) | `quil ○ not running` | `{"running":false}` | `1` |
| Connects but a core round-trip times out (wedged) | `quil ⚠ running but not responding (pid N)` | `{"running":true,"responding":false,"pid":N}` | `2` |
| Optional segment (events) unavailable | segment omitted | key omitted | unchanged |

- Exit codes let scripts branch: `0` healthy, `1` dead, `2` wedged.
- The pid in the "wedged" and "not running" paths comes from the PID file when
  present (best-effort; omitted if unreadable).

## Components & testability

Split so the pure logic is unit-testable without a socket:

- `mergePaneMemory(panes []ipc.PaneInfo, mem []ipc.PaneMemInfo) []statusPane`
  — join by pane ID; panes with no memory entry (pending) get zero/`—`.
- `buildStatus(version string, panes, mem, events, pidModTime, env) statusReport`
  — assembles the full in-memory model (totals, tab grouping, uptime).
- `renderHuman(r statusReport, verbose bool) string`.
- `renderJSON(r statusReport) ([]byte, error)`.
- `formatBytes(n uint64) string`, `formatUptime(d time.Duration) string`.

Tests (`cmd/quil/status_test.go`, `package main`, table-driven, per
`go-testing` rules):

- `mergePaneMemory`: matched, unmatched-pane (pending), unmatched-memory
  (stale) cases.
- `buildStatus`: totals math (running/pending split, memory sum), tab grouping
  order, active/eager flags, uptime from a fixed mtime.
- `formatBytes`, `formatUptime`: boundary cases (0, sub-KB, exact MB, multi-day,
  `<1m`).
- `renderHuman`: golden-ish substring assertions for the not-running, healthy,
  and wedged headers + a pending-pane `—` row.
- `renderJSON`: round-trips through `encoding/json` into a map and asserts key
  presence/omission (events omitted, uptime omitted on stat failure).

The IPC round-trip glue (`runStatus`, the `Receive` loop) is thin I/O and stays
untested, per the `cmd/*/main.go` convention.

## Files touched

| File | Change |
|------|--------|
| `cmd/quil/status.go` | **new** — command entry, round-trip helper, model, renderers, formatters |
| `cmd/quil/status_test.go` | **new** — table-driven tests for the pure logic |
| `cmd/quil/main.go` | add `case "status"`; route `daemon status` to the new impl; remove old `daemonStatus()` |
| `docs/*` (troubleshooting / MCP or CLI reference) | document `quil status`, its flags, and exit codes |

## Risks

- **Broadcast interleaving:** filtering `Receive()` by `Message.ID` is the
  mitigation; the command never attaches, so broadcast volume is low.
- **Pidfile mtime drift:** if any future code rewrites the pidfile mid-session,
  uptime would reset. Acceptable for an approximate, clearly-labeled value; the
  spec commits to the honest-approximation framing.
- **Older daemon compatibility:** all three core messages predate this work;
  the only optional round-trip (events) degrades gracefully.
```

