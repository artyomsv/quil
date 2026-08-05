# TestRouterRemovedPumpPublishesNothing is timing-dependent and fails CI intermittently

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Small |
| Location | `internal/tui/router_test.go:342` |
| Found during | Code review of PR #132 (sidebar attention indicators) — CI failed on an unrelated branch change |
| Date | 2026-08-05 |

## Issue

Both subtests assert a NEGATIVE ("the removed pump published nothing") by
sleeping and then polling a channel:

```go
gpu.recv <- out
r.Remove("gpu01")
close(gpu.recv)

time.Sleep(50 * time.Millisecond)
select {
case leaked := <-r.in:
    t.Fatalf("removed pump published %s", leaked.Type)
default:
}
```

The message is queued **before** `Remove`, deliberately — the comment says it is
"guaranteed to have something to publish once it wakes". But whether the pump
goroutine wakes and publishes *before* `Remove` takes effect is decided by the
scheduler, not by the test. The 50 ms sleep does not order those two events; it
only bounds how long the test waits before concluding.

Observed failing in CI under `-race` on a loaded runner
(run 31052116767, `--- FAIL: TestRouterRemovedPumpPublishesNothing/message`)
while passing 5/5 consecutively under `-race` locally, on a branch whose changes
touch none of the router code. The change that surfaced it only perturbed
package timing (a new import).

## Risks

- **CI failures that are not the PR's fault.** A red check on an unrelated
  change costs a rerun and, worse, trains reviewers to re-run rather than read a
  failure — which is exactly when a real regression gets waved through.
- The failure is a *sleep-tuning* problem, so the tempting fix is a longer
  sleep. That trades flakiness for a slower suite and does not remove the race;
  it only makes the losing schedule rarer and the eventual failure more
  confusing.
- `-race` makes it materially more likely, so the flake concentrates in the run
  that matters most.

## Suggested Solutions

1. **Make the ordering explicit rather than probable.** Have `Remove` expose a
   synchronisation point the test can wait on — the pump's exit — so the test
   asserts "after the pump has stopped, nothing was published" instead of
   "after 50 ms, nothing was published yet". Deterministic, and it also removes
   the sleep from the suite's runtime.
2. **Assert on the pump's own lifecycle** rather than on the absence of a
   message: if the property under test is "a removed pump does not publish",
   the observable is the pump goroutine having exited with the message
   undelivered, which can be checked directly.
3. **Least preferred: keep the sleep but drain deterministically** — queue the
   message, wait for the pump to have *consumed* it (a signal from the fake
   conn), then `Remove`, then assert. Still sleep-free but keeps the current
   shape.

Option 1 is the one that generalises: `TestRouterPumpSurvivesANilMessage`
immediately below uses the same 50 ms-sleep-then-poll shape and has the same
latent weakness.
