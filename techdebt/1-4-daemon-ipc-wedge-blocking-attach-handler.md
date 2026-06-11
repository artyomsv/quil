# Daemon-wide wedge: blocked goroutine serializes snapshot loop, event pipeline, and IPC handlers

| Field | Value |
|-------|-------|
| Criticality | Critical |
| Complexity | Large |
| Location | `internal/daemon/daemon.go` (handleAttach, snapshot Wait loop, emitEvent path), `internal/ipc/server.go` (per-conn dispatch) |
| Found during | Production incident debugging (macOS, quil v1.19.1) |
| Date | 2026-06-11 |

## Issue

Production daemon (v1.19.1, macOS) wedged progressively and could only be recovered by killing the process. Log-reconstructed timeline from `quild.log` / `quil.log`:

1. **20:24:45** — last periodic 30 s snapshot log line. The snapshot loop never logs again (daemon ran 1h+ more). Wedge begins between 20:24:51 (last pane_event delivered to the TUI: `hook.claude.Notification pane-3e9dcbd4`) and 20:25:15 (first missed snapshot tick).
2. **20:24:51 → 21:20:30** — daemon keeps logging `ipc recv: memory_report_req` every 5 s (old TUI alive, its read loop + the memreport handler don't need the wedged resource), but: no snapshots, no pane events reach the TUI (`pane-out bytes=0` in TUI perf logs), TUI appears "hung" to the user.
3. **~21:20:30** — old TUI's connection dispatch goes silent too (likely a user-triggered request that needed the wedged lock; its read loop is now parked inside a handler).
4. **21:22:27** — user quits TUI ("TUI exited normally"). Daemon NEVER logs the disconnect — per-conn read loop is stuck inside a handler, so conn close is never observed; conn count stays wrong (total=3 at next connect).
5. **21:22:29** — new TUI attaches: daemon logs `ipc recv: attach` + `attach: client connected (tabs=9, restored=true)` — then **nothing, ever again** (these are the final lines in quild.log). `handleAttach` blocked before sending workspace state / ghost replay. The new TUI renders an empty workspace (no tabs/panes) while the daemon is alive and `ConnCount` grows.

Native `sample` of the process shows threads parked (pthread_cond_wait) — a Go-level lock convoy, not a spin. Goroutine dump unobtainable: daemon stderr → `/dev/null`, so SIGQUIT's dump is lost (see also "Suggested Solutions" #4).

Net effect: ONE goroutine blocking forever while holding (or queueing on) a central lock (`SessionManager` mutex / `PluginMu`) converts into: dead snapshot loop → dead event pipeline → dead attach path → daemon that accepts connections but can never again serve a workspace. Suspect window coincides with hook-event emission (`hook.claude.Notification`) + idle checker activity at 20:24:51.

## Risks

- Total production outage of the daemon with NO crash, NO error log, NO watchdog — silent. User-visible as "quil hung" then "tabs don't reopen".
- Workspace snapshots stop while the daemon keeps running — hours of layout/pane changes lost on eventual kill (this incident: state frozen at 20:24, user worked until 21:22).
- Wedged conn close detection means client reconnect storms accumulate dead conns.
- Unreproducible post-mortem: no goroutine dump capture path exists (stderr discarded).

## Suggested Solutions

1. **Watchdog on the snapshot loop**: the 30 s periodic snapshot is a perfect liveness probe. If a snapshot hasn't completed for N intervals (e.g. 3), log a loud ERROR with full goroutine dump (`runtime.Stack(buf, true)`) to quild.log. Turns the next occurrence into a diagnosable incident instead of a mystery.
2. **Timeout-guard the attach path**: `handleAttach` (and any handler that takes the sessions lock) should acquire with a deadline (`TryLock` loop or context) and fail the attach with an error frame instead of parking the conn's read loop forever. A TUI that gets an explicit "daemon busy/wedged" error can tell the user; a silent park cannot.
3. **Audit lock-hold sites for unbounded blocking**: any path that does I/O (PTY writes, file I/O, channel sends without default) while holding `SessionManager`/`PluginMu`. The 20:24:51 window points at the hook-event → emitEvent → broadcast chain and/or `checkIdlePanes`. The IPC enqueue side is supposedly non-blocking post-#51 — verify nothing else under those locks can block (e.g. spool/file ops, `SendBlocking` reachable under a lock).
4. **Capture SIGQUIT dumps**: point daemon stderr at a file (e.g. `$QUIL_HOME/quild.stderr.log` or dup onto the rotating logger) instead of `/dev/null`, so `kill -QUIT` produces a usable goroutine dump for the next wedge.

## Incident recovery (for reference)

State was safe: `workspace.json` (9 tabs/9 panes, mtime 20:24) + hook session-id files intact. Recovery = kill wedged daemon (graceful IPC shutdown impossible — dispatch dead), relaunch `quil` → auto-start → restore from snapshot; claude-code panes resume via persisted session ids.
