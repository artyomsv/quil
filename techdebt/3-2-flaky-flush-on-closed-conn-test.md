# `TestConn_FlushOnClosedConnReturnsImmediately` is timing-dependent

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Small |
| Location | `internal/ipc/flush_test.go:62-84` |
| Found during | CI on PR #135 — the same commit passed at 00:17 and failed at 08:03 |
| Date | 2026-08-07 |

## Issue

The test asserts that `Flush` returns **false** on a closed conn with a frame
still queued:

```go
c.Send(msg)   // frame onto critCh
c.Close()     // sendLoop stops; Close DISCARDS queued frames
if c.Flush(5 * time.Second) {
    t.Error("Flush reported success on a closed conn with a frame still pending")
}
```

Whether a frame is still "pending" at the moment `Flush` runs is decided by a
goroutine race, and both outcomes are legitimate:

- `sendLoop` has already dequeued the frame and is parked in the `net.Pipe`
  write (nothing reads it) → the frame counts as pending → `Flush` returns
  false → **test passes**.
- `sendLoop` has not yet dequeued it, so the frame is still in `critCh` when
  `Close` drains the queue → nothing is pending → `Flush` returns true →
  **test fails**.

Nothing in the test forces the first ordering. It passes locally (10/10 under
`-race`) because the scheduler reliably runs `sendLoop` first on an unloaded
machine; a loaded CI runner does not guarantee that.

Observed: run `31134100043` (green) and run `31160132924` (red) on the
identical commit `fe7934b`, seven hours apart, with no change to
`internal/ipc` in between.

## Risks

A red CI on an unrelated PR reads as "this change broke something", which
costs whoever sees it the time to prove otherwise — exactly what happened on
PR #135. Repeated false reds also erode the signal: the next genuine `ipc`
failure is likelier to be waved through as "that flaky one again".

This is the second test of this shape in the repo; see
`techdebt/3-2-flaky-router-removed-pump-test.md`. Both assert a negative about
concurrent state without synchronising on the event that makes it true.

## Suggested Solutions

1. **Synchronise on the dequeue rather than hoping for it.** Have the test
   observe that `sendLoop` is parked in the write before calling `Close` — e.g.
   read one byte from the server side of the pipe with a deadline, or expose a
   test-only "frames in flight" counter. This makes the intended ordering a
   fact rather than a scheduling accident, and keeps the assertion the test was
   written for.
2. **Assert the property that is actually invariant**: `Flush` on a closed conn
   must return *promptly* regardless of what it reports. The elapsed-time check
   below the boolean is already the load-bearing half — the doc comment says
   the point is "waiting could only burn the whole timeout on the exit path" —
   so dropping the boolean assertion would keep what matters and remove the
   race.
3. Lengthening a sleep or retrying is not a fix here; there is no sleep, and
   the window is a scheduling decision rather than a duration.
