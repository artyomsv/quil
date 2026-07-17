---
name: goroutine-shutdown-convention
description: quil's established goroutine shutdown pattern (done-channel or os.Exit) is an accepted alternative to context.Context under go-conventions.md
metadata:
  type: project
---

quil's codebase never uses `context.Context` for its long-running background
goroutines. Instead it uses two patterns, both already established before the
2026-07 parentwatch feature:

1. **Daemon-side long-lived goroutines** (`internal/daemon/daemon.go`:
   `idleChecker`, `hookEventsWatcher`, the notification watcher loop, etc.)
   select on a shared `d.shutdown` channel, closed once via `sync.Once` in
   `Daemon.Stop()`. This is the "done channel" alternative go-conventions.md
   explicitly allows alongside `context.Context`.
2. **Single-purpose CLI helper processes** (e.g. `cmd/quil/parentwatch_windows.go`
   / `parentwatch_unix.go` — the MCP bridge's parent-death watchdog) launch a
   goroutine whose entire job is to call `os.Exit(0)` when a condition is met.
   There is no coordinating parent structure to hand a context or done-channel
   to, because the goroutine's own shutdown act ends the whole process. This
   is a stronger shutdown guarantee than a context cancel, not a weaker one.

**Why this matters for review:** go-conventions.md says goroutines must
"always" pair with `context.Context`. Reviewers should NOT flag pattern (2) as
a HIGH violation — it's a deliberate, repo-consistent exception for
process-lifetime helper goroutines in short-lived `cmd/*` binaries, mirroring
the done-channel pattern already accepted for daemon-side goroutines. Do flag
it (LOW, informational) only if the goroutine lacks any comment explaining why
no context/channel is used — code should self-document the exception.

**How to apply:** When reviewing new `cmd/quil` or `cmd/quild` goroutines that
terminate via `os.Exit` or process death rather than context cancellation,
check for (a) a clear comment justifying the process-exit-as-shutdown design,
and (b) consistency with this precedent, rather than treating the missing
`context.Context` as a rule violation by itself.
