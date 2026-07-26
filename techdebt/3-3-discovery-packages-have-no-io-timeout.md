# Pure-discovery packages do filesystem I/O with no timeout or cancellation

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Medium |
| Location | `internal/claudesessions/claudesessions.go`, `internal/gitdiscover/gitdiscover.go`, `internal/kubediscover/kubediscover.go`; daemon caller at `internal/daemon/claudesessions.go:handleClaudeSessionsReq` |
| Found during | Code review of the Claude resume-session picker (feature/claude-resume-session-picker) |
| Date | 2026-07-26 |

## Issue

The three "pure discovery" packages all read the filesystem with unbounded,
uncancellable calls — `os.ReadDir`, `os.Open`, `os.ReadFile`,
`io.ReadAll(io.LimitReader(...))`. None accepts a `context.Context`, so none can
be cancelled or bounded once started.

`claudesessions` makes this more visible than its two siblings because it is the
only one reached from a **daemon worker goroutine**:

```go
func (d *Daemon) handleClaudeSessionsReq(conn *ipc.Conn, msg *ipc.Message) {
    go func() {
        respondTo(conn, msg.ID, ipc.MsgClaudeSessionsResp, d.claudeSessionsResponse(msg))
    }()
}
```

That goroutine is untracked: nothing holds a handle to it, nothing can cancel
it, and it may head-read up to 200 transcript files per request.

The TUI's 3 s `sessionScanTimeoutCmd` does **not** solve this. It only changes
what the client renders; the daemon-side goroutine keeps running. Re-focusing
the session field after a timeout issues another request, so on a genuinely
wedged filesystem the stuck goroutines accumulate rather than replace each
other.

## Risks

- **Goroutine accumulation on a degraded filesystem.** A network home directory
  (`~/.claude` on NFS/SMB), a stalled fuse mount, or a disk with pending I/O
  errors makes each request park indefinitely. Repeated dialog focus multiplies
  the parked goroutines, each holding its captured `*ipc.Conn`.
- **Silent divergence between what the user sees and what the daemon does.**
  The dialog says "Timed out", implying the work stopped; it has not.
- `gitdiscover` and `kubediscover` carry the same exposure on the TUI side,
  where a stall blocks the dialog rather than a goroutine — different symptom,
  same root cause.

Not a regression introduced by the resume picker: every failure path still
degrades to an empty list and pane creation is never blocked, which is the
contract CLAUDE.md documents for all three packages. This is a latent
robustness gap, not a live defect.

## Suggested Solutions

1. **Shared bounded-discovery helper (preferred).** Give all three packages a
   `ListContext(ctx, …)` variant and have the existing `List` delegate with
   `context.Background()`. The daemon passes a `context.WithTimeout` (a few
   seconds) and the TUI's client-side timeout becomes the second line of
   defence instead of the only one. Fixes the class rather than one instance.
2. **Track the worker goroutine.** Keep the API as-is but have the daemon run
   the scan under a supervised goroutine with single-flight per CWD, so a
   second request for the same directory joins the first instead of spawning a
   new parked worker. Cheaper, but leaves the underlying call unbounded.
3. **Do nothing and accept it.** Defensible while `~/.claude` is a local
   directory on every supported platform. Revisit the moment a user reports a
   network home directory.
