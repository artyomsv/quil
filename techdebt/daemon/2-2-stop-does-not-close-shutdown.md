# Daemon.Stop() does not close the shutdown channel

| Field | Value |
|---|---|
| Criticality | High |
| Complexity | Small |
| Location | `internal/daemon/daemon.go` — `Stop()` and long-running goroutines (`idleChecker`, `memReport` bridge) |
| Found during | Code review of Task 10 (memory-reporting feature, commits c6f08cf / 249df58 / 5e1ad6f) |
| Date | 2026-04-20 |

## Issue

`(*Daemon).Stop()` tears down the IPC server and snapshot machinery but does
not close `d.shutdown`. Several long-running goroutines use
`<-d.shutdown` as their sole exit signal:

- `idleChecker()` (line ~1322 before this fix).
- The bridge goroutine added in Task 10 that pipes `<-d.shutdown` into
  `context.Cancel` for `memReport.Run(ctx)`.
- `sendGhostChunked()` per-pane goroutines (line ~567).

When `Stop()` is called programmatically (tests, future code that manages
the daemon lifecycle without going through `MsgShutdown`), these goroutines
stay alive until process exit. Under `MsgShutdown` this works only because
the dispatcher closes `d.shutdown` via `shutdownOnce.Do(...)` BEFORE
`Wait()` calls `Stop()`.

Task 10 worked around the issue locally by adding `d.collectorCancel` and
calling it from `Stop()` so the memreport collector tears down cleanly.
That is a point fix — `idleChecker` and others still leak on
programmatic shutdown.

## Risks

- Goroutine leaks in any new test that uses `Stop()` directly.
- If `Stop()` is ever wired to a UI "quit daemon" button or programmatic
  lifecycle manager, the daemon process would not exit cleanly.
- Invariant of "Stop drains everything" is implicit and violated — future
  refactors that rely on it (e.g. a reload-daemon-in-place flow) will
  behave incorrectly.

## Suggested Solutions

**Option A (minimal):** make `Stop()` call `d.shutdownOnce.Do(func() { close(d.shutdown) })` as its first action. Idempotent; all existing goroutines wake up, see the closed channel, and exit. Remove the `collectorCancel` field + bridge goroutine at the same time since `memReport.Run(ctx)` can derive its ctx from the shutdown channel:

```go
ctx, cancel := context.WithCancel(context.Background())
go func() { <-d.shutdown; cancel() }()
go d.memReport.Run(ctx)
```

(The bridge stays, but the dedicated cancel-func state goes away.)

**Option B:** thread a `context.Context` through the daemon from `New()`
forward, replacing the `shutdown` channel with ctx everywhere. Larger
surgery but better aligns with Go convention.

Option A is a ~10-line change and closes the leak for all existing
goroutines in one shot.
