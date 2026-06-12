# Flaky TestSendLoop_ExitsOnWriteError blocks releases

| Field | Value |
|-------|-------|
| Criticality | High |
| Complexity | Trivial |
| Location | `internal/ipc/conn_internal_test.go:19` (TestSendLoop_ExitsOnWriteError) |
| Found during | v1.20.0 release failure investigation (run 27374517802) |
| Date | 2026-06-11 |

## Issue

The test marks itself `t.Parallel()` but asserts on a **global** `runtime.NumGoroutine()` delta (`baseline` captured at test start vs count after `Close`). Other ipc tests running concurrently spawn their own conns/servers/goroutines between the two samples, so the delta randomly exceeds the `baseline+1` tolerance. Observed: `goroutine leak after sendLoop write-error exit: baseline=20, after=26` — failing the `release.yml` test gate and silently blocking the v1.20.0 release (PR #54 merged, no release published; users installing got stale v1.19.1).

The fixed `time.Sleep` settle windows (50/100 ms) make it timing-sensitive on loaded CI runners too.

## Risks

- Releases fail nondeterministically after a successful PR CI run — merge appears done but no release ships, and nothing notifies anyone (failure is only visible in the Actions tab).
- Re-running the workflow "fixes" it, training people to ignore red releases.

## Suggested Solutions

1. Drop the global count assertion: have `sendLoop` signal its exit (it already returns on write error — wait on a sentinel, e.g. poll `len(critCh)`+a done flag, or expose the existing `done`/loop-exit via a test hook) instead of counting all goroutines.
2. Or remove `t.Parallel()` AND poll-until-deadline for the count to return to baseline (loop with timeout instead of fixed sleeps) — weaker, still global-state-coupled.
3. Consider `go.uber.org/goleak`-style per-test leak checking only if the project wants a dependency; otherwise option 1.
