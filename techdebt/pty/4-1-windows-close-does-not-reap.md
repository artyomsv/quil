# Windows Close() does not call WaitExit — handle reap depends on the coalescer

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Trivial |
| Location | `internal/pty/session_windows.go` (`Close`), contrast `internal/pty/session_unix.go` (`Close` calls `s.WaitExit()`) |
| Found during | Code review of the WaitExit CloseHandle fix (2026-06-10) |
| Date | 2026-06-10 |

## Issue

`WaitExit` now closes the child-process HANDLE, but Windows `Close()` only
closes the ConPTY and never calls `WaitExit()`. The Unix counterpart calls
`s.WaitExit()` from `Close()` exactly to guarantee the reap regardless of
caller behavior (idempotent via `waitOnce`).

In practice every pane-destroy path reaps anyway: closing the ConPTY EOFs the
reader goroutine, the coalescer exits, and `streamPTYOutput` calls
`pty.WaitExit()`. The handle leaks only if a session is `Close()`d without
its `streamPTYOutput` ever having started (spawn-error edge paths).

## Risks

A few kernel HANDLEs retained on rare error paths; bounded, cleared at daemon
exit.

## Suggested Solutions

Mirror the Unix pattern: have Windows `Close()` call `s.WaitExit()` after
closing the ConPTY (idempotent via `waitOnce`). One caveat: `WaitExit` blocks
in `WaitForSingleObject(..., INFINITE)` — only safe in `Close` if the ConPTY
close reliably terminates the child; otherwise pair with a bounded wait +
`TerminateProcess` fallback.
