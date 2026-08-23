# TestHandleMessage_KillProcessIsWiredUpAndSingleFlighted races the worker it observes

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Small |
| Location | `internal/daemon/procreport_dispatch_test.go:183` |
| Found during | Code review of PR #186 (remote upgrade offer) — CI failed on a branch that touches no daemon file |
| Date | 2026-08-23 |

## Issue

The second half of the test asserts a POSITIVE by polling for a flag that the
code under test sets and then clears on its own:

```go
d.killRunning.Store(false)
d.handleMessage(nil, msg)

deadline := time.Now().Add(5 * time.Second)
claimed := false
for time.Now().Before(deadline) {
    if d.killRunning.Load() {
        claimed = true
        break
    }
    time.Sleep(time.Millisecond)
}
if !claimed {
    t.Fatal("the kill request never claimed the single-flight — ...")
}
```

`handleMessage` claims the flight and hands the work to a goroutine, which
releases it when it finishes. The request names `PaneID: "no-such-pane"` and
`PID: 999999`, so that work is a lookup miss — it can complete before the
polling loop's first `Load()`. Nothing distinguishes "never claimed" from
"claimed and released before I looked", and the failure message asserts the
first with confidence.

The first half of the test does not have this problem: it pre-claims the flight
so the handler takes its synchronous busy path, and the test's own comment says
why ("there is no goroutine to race"). The property is only unobservable on the
half that lets the goroutine run.

## Evidence

CI run 32640108969 on commit 6d74cf4:

```
--- FAIL: TestHandleMessage_KillProcessIsWiredUpAndSingleFlighted (5.00s)
    procreport_dispatch_test.go:193: the kill request never claimed the
    single-flight — the dispatch arm for MsgKillProcessReq is not reaching
    handleKillProcessReq
FAIL	github.com/artyomsv/quil/internal/daemon	21.276s
```

Re-run of the same job on the same commit, no code change: pass in 3m10s. The
branch it failed on modifies only `cmd/quil` and `internal/tui`.

The 5.00s duration is the whole polling budget being spent, which is the
signature: a genuinely disconnected dispatch arm and a flag released too early
both burn the full deadline and print the same line.

## Risks

- A red CI on a PR that changed nothing in `internal/daemon` sends the author
  looking for a fault in their own diff. That cost real time here.
- Repeated unexplained reds train the reflex of re-running until green, which is
  how a genuine regression in the dispatch arm gets waved through — this test
  exists precisely to catch that arm being disconnected.
- Same family as `3-2-flaky-router-removed-pump-test.md` and
  `3-2-flaky-flush-on-closed-conn-test.md`: an assertion about a concurrent
  transition observed by polling from outside.

## Suggested Solutions

1. Observe the completion instead of the claim. Have the worker signal a channel
   the test waits on, then assert the flight was released — the release is the
   stable end state, and reaching it proves the arm ran. Preferred: it tests the
   same property without a window.
2. Make the work slow enough to observe by pointing the request at a pane that
   exists with a process that lingers. Rejected — it swaps a timing race for a
   timing assumption and makes the test depend on process spawning.
3. Count invocations. Give the handler a test-visible counter incremented on
   entry, and assert it moved. Simple and race-free, but adds a field that exists
   only for the test.

Whichever is chosen, the `t.Fatal` message must stop asserting a cause it cannot
distinguish — "never claimed" and "released before observed" need different text
or the failure keeps misdirecting whoever reads it.
