# Defense-in-depth: deadline-guard sm.mu acquisition in IPC handlers

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Medium |
| Location | `internal/daemon/daemon.go` (handleAttach, handleSwitchTab, and other handlers taking `sm.mu`) |
| Found during | 2026-06-11/12 daemon wedge incidents (root cause fixed: blocking PTY Write on dispatch + PTY Close under sm.mu) |
| Date | 2026-06-12 |

## Issue

The two known wedge mechanisms are fixed (per-pane input writer goroutines; async off-lock PTY close — see `wedge_regression_test.go`), and `snapshotWatchdog` now dumps goroutine stacks when the snapshot loop stalls. What remains is the structural property that made those bugs catastrophic: an IPC handler that parks on `sm.mu` parks its conn's dispatch goroutine forever, with no error surfaced to the client. Any *future* unbounded-block-under-lock bug recreates the full freeze.

## Risks

A new blocking call under `sm.mu` (easy to introduce — e.g. file I/O in a future handler) silently reintroduces the all-clients-frozen failure mode; the watchdog makes it diagnosable but not survivable.

## Suggested Solutions

1. Bounded lock acquisition for handlers: a `tryLockTimeout(d.session, 5*time.Second)` helper (TryLock + ticker loop); on timeout, respond with an error frame ("daemon busy") instead of parking — the TUI can surface it.
2. Alternatively a lock-free read snapshot (atomic.Pointer to an immutable session view, swapped on mutation) for the read-heavy paths (SnapshotState, attach, memreport), removing reader starvation entirely.
3. CI lint (custom vet check or code-review rule): no function call with potentially-unbounded blocking (PTY/file/network I/O, `cmd.Wait`, channel send without default) inside an `sm.mu`/`PluginMu` critical section.
