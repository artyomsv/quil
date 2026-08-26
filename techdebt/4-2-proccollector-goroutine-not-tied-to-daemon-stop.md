# procCollector's Renew-spawned goroutine is not tied to daemon shutdown

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Small |
| Location | `internal/daemon/procreport.go` (`procCollector.Renew`/`run`), `internal/daemon/daemon.go` (`Daemon.Stop`, `collectorWG`) |
| Found during | QA of PR #195 (quil's own processes report their cpu and rss) — pre-existing, not introduced there |
| Date | 2026-08-26 |

## Issue

`procCollector` has no `Close` or `Stop` method. The goroutine `Renew()` starts
on the first `WithTrees: true` request exits only when its own
`procGateWindow` (15 s) deadline lapses — nothing else can end it.

`Daemon.Stop()` closes `d.shutdown`, stops the IPC server, waits on
`d.collectorWG`, snapshots, and closes pane PTYs. `collectorWG` wraps only
`d.memReport.Run(ctx)`; `procReport` is never referenced in `Stop()` at all. So
the collector goroutine outlives the daemon that owns it by up to a gate window,
holding a reference to a session that has already been torn down.

Every other daemon background loop — `idleChecker`, the update checker, the
snapshot loop — is wired to `d.shutdown`. This one is the exception.

## Why it matters

In production the gap is nearly invisible: `Stop()` is followed by process exit,
so the lingering goroutine dies with everything else.

It is visible in tests, and PR #195 added the first ones that reach it —
`internal/daemon/procstat_ipc_test.go` drives a real `Start()`/`Stop()` cycle
with `WithTrees: true`, so each such test leaves a goroutine running for up to
~15–20 s after `t.Cleanup(d.Stop())` returns. `dev.sh test-race internal/daemon`
is clean today because each test builds its own isolated `daemon.New(cfg)` and
the orphan only ever touches its own dead instance — but that is a property of
how the tests are written, not a guarantee. A future test that shares a daemon,
or any code that starts inspecting collector state after `Stop()`, turns this
into a real race.

The second-order cost is that "every background loop is tied to `d.shutdown`" is
currently a rule with one silent exception, which is exactly the shape that makes
the next loop's author think it is optional.

## Fix

Give `procCollector` a `Close()` that stops the ticker and returns from `run()`,
and either wire it into `collectorWG` alongside `memReport` or call it directly
from `Daemon.Stop()`. `expired()` already contains the whole teardown (drops the
samplers, clears `last`, flips `running`) — the missing piece is a second way to
reach it that does not require waiting out the deadline.

Verify the way this repo verifies wiring claims: grep `Stop()` for `procReport`
rather than trusting that the call was added.
