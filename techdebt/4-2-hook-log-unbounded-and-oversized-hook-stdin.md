# hook.log has no rotation, and a >1 MiB hook stdin silently loses the event

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Small |
| Location | `internal/claudehook/runhook.go` (`hookLog`, `maxStdinBytes`, `RunHook`); reproduced verbatim in `internal/codexhook/` (`session.go` `hookLog`, `runhook.go` `maxStdinBytes`/`RunHook`) on 2026-09-04, with the same match-all `PreToolUse` registration that makes both reachable |
| Found during | Security review of PR #162 (work-indicator start edges) |
| Date | 2026-08-15 |

## Issue

Two pre-existing bounds in the hook producer that PR #162 made reachable far
more often, by registering `PreToolUse` match-all — the hook now runs once per
tool call rather than a handful of times per turn.

1. **`hookLog` appends with no rotation and no size cap.** Every other log file
   in the project goes through `internal/logger`'s `RotatingWriter`
   (`max_size_mb`, `max_files`); `$QUIL_HOME/claudehook/hook.log` does not.

2. **`RunHook` reads stdin through `io.LimitReader(r, maxStdinBytes)` with
   `maxStdinBytes = 1 << 20`.** A `PreToolUse` payload carries `tool_input`, so
   a large `Write` or `Edit` exceeds it. The reader truncates mid-JSON,
   `json.Unmarshal` fails, and the handler writes a `parse stdin failed`
   breadcrumb and returns. The event is dropped.

These compound: the events most likely to blow the stdin cap are now also the
ones firing most often, so each one appends another line to a file nothing
prunes.

## Risks

Low, in both halves.

The dropped event costs at most one heartbeat, and the heartbeat is a LEVEL
rather than an edge — any later tool call in the same turn re-arms the identical
state, bounded by `workHeartbeatInterval` (15 s). It is NOT a lost turn-ending
edge: `Stop` and `StopFailure` payloads are small and never approach the cap.

The log growth is slow (one line per oversized tool call) but unbounded over a
daemon that runs for weeks, in a directory the user does not routinely inspect.

## Fix sketch

- Route `hookLog` through the same rotating writer the daemon and TUI use, or
  give it a simple size check with truncate-on-exceed. It runs in a short-lived
  per-event process, so it needs to be cheap and lock-free rather than correct
  under concurrency.
- For the stdin cap, decide deliberately rather than by raising the number:
  either stream-decode so the fields Quil actually reads (`hook_event_name`,
  `session_id`, `tool_name`, `agent_id`) survive a truncated `tool_input`, or
  keep the cap and downgrade the breadcrumb so an oversized payload is not a
  logged error at per-tool-call frequency.

## Notes

Deliberately not fixed in PR #162: both bounds predate it and belong to the hook
producer's I/O layer rather than the work-indicator change, and the fix for the
second one is a design decision (stream-decode vs cap) that deserves its own
commit.
