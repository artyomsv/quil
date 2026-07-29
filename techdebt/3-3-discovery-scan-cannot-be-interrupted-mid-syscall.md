# Discovery scans cannot be interrupted once blocked in a syscall

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Medium |
| Location | `internal/daemon/claudesessions.go` (`handleClaudeSessionsReq`, `handleClaudeSessionDetailReq`); `internal/claudesessions`, `internal/gitdiscover`, `internal/kubediscover` |
| Found during | Code review of PR #112 (RD-004, remote-daemon Phase 1.5) |
| Date | 2026-07-29 |

## Issue

This replaces `3-3-discovery-packages-have-no-io-timeout.md`, which was deleted
when RD-004 landed. RD-004 fixed the majority of that debt — all three
discovery packages now take a `context.Context`, and the daemon derives a 10 s
`discoveryTimeout` for both handlers — but it explicitly did **not** fix the
risk that file named first, and the residual deserves to stay tracked here
rather than only in a roadmap document.

`ctx` is checked **between** syscalls. That bounds a scan which is still making
progress: a directory with thousands of entries, a slow-but-live network mount.
It cannot interrupt a call already blocked in the kernel — Go has no mechanism
to cancel an in-flight `stat`, `open`, or `read`. The guarantee is *bounded
work*, not *bounded time*, and the code comments say so.

The daemon-side consequence is worse than a slow request, because the
single-flight slot is released by the worker itself:

```go
go func() {
    defer d.sessionScanning.Store(false)
    respondTo(conn, msg.ID, ipc.MsgClaudeSessionsResp, d.claudeSessionsResponse(msg))
}()
```

If `claudeSessionsResponse` parks forever inside a syscall, that `defer` never
runs. `sessionScanning` stays `true` for the life of the daemon, and every
later request is refused with `another session scan is already running`. The
10 s timeout fires and `cancel()` is called, but nothing observes it — the
goroutine is not at a cancellation point.

`sessionDetailReading` has the same shape.

## Risks

- A single stalled NFS/SMB/sshfs mount under a Claude project directory
  permanently disables the resume-session picker until the daemon is
  restarted. The user sees "another session scan is already running" forever,
  with no indication of the real cause.
- One leaked goroutine per stalled scan, each holding an fd and up to a 64 KiB
  buffer.
- The failure is invisible in logs beyond the refusal message, and the refusal
  names the wrong cause — it reads as a concurrency guard doing its job.
- Phase 3 (RD-020/021/022) moves directory listing, git discovery and kube
  discovery behind the same handler shape, multiplying the number of features
  a single dead mount can wedge.

## Suggested Solutions

1. **Release the slot from the waiter, not the worker.** Run the scan in a
   goroutine that writes to a buffered channel; the handler selects on that
   channel against `ctx.Done()`. On timeout the handler answers, clears the
   single-flight slot, and abandons the goroutine. The goroutine leaks — that
   is accepted and bounded by the mount being dead — but the feature recovers.
   This is the smallest change that removes the permanent-wedge property.
2. **Cap the number of abandoned scans.** With (1) in place, a mount that
   stalls repeatedly leaks one goroutine per attempt. A counter that refuses
   new scans past N outstanding turns an unbounded leak into a bounded one with
   an honest error message.
3. **Probe the directory before scanning.** A cheap `stat` with its own
   abandon-on-timeout wrapper, run before committing to the full scan, catches
   the common case (mount is gone) without restructuring the scan itself.
   Weakest option — it narrows the window rather than closing it.

Not recommended: platform-specific I/O timeouts. They do not exist portably for
filesystem operations, and the three packages are deliberately stdlib-only
(`internal/claudesessions` is imported by the hot-path hook subprocess).

## Related

- `docs/roadmap/remote-daemon.md` § Work registry — RD-004 and the residual
  note under the Phase 1.5 table.
- PR #112.
