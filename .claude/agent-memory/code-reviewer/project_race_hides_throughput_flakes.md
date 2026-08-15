---
name: race-hides-throughput-flakes
description: A green `go test -race` does not clear a producer/consumer timing test — race instrumentation slows the producer and hides flakes the plain run exposes
metadata:
  type: project
---

When reviewing a test whose premise is "this reader keeps up with that writer"
(the `internal/ipc` broadcast/backpressure family), a green `go test -race` run
is NOT evidence of stability. Race instrumentation slows the *producer* far more
than a socket drain, so it widens the very margin the test depends on. Run the
plain `go test -count=1` form repeatedly — and compare against the pre-change
version of the same file back-to-back, since flake rates here are load-dependent
and a warm idle machine can hide one for 100+ runs.

**Why:** the IPC-frame-encoding PR (2026-08-15) made `EncodeFrame` ~40x faster,
which turned `TestBroadcast_SlowConnDoesNotBlockFastConn` flaky: the fast conn's
own 64-slot `critCh` now overflows and the server closes it. It failed 3x on
branch code in plain runs and 0x on master over the same period, while passing
`-race -count=6` every time. QA reported "passes under race" and missed it.

**How to apply:** for any test in `internal/ipc` that pairs an unpaced producer
loop with a reader goroutine, count the buffering: 64 critical slots + one
~200 KiB socket buffer is the entire slack. If the burst exceeds that, the test
is a throughput race. The fix that works is pacing the producer and summing only
the `Broadcast` call durations, so a ">Ns means wedged" assertion survives.

Related: [[local-race-run-misses-parallel-var]] covers the opposite direction —
green local race run, red in CI. Both say the same thing: `-race` is a different
experiment, not a stronger one.
