# Windows: child process HANDLE never closed

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Trivial |
| Location | `internal/pty/session_windows.go:57-63` (handle stored), `:88-93` (Close), `:99-111` (WaitExit) |
| Found during | Whole-project memory-leak audit |
| Date | 2026-06-10 |

## Issue

`cp.Spawn` returns a process handle stored in `s.handle`; neither `Close` nor
`WaitExit` ever calls `windows.CloseHandle`. One kernel HANDLE (and the kernel
process object after child exit) is retained per destroyed/restarted pane
until the daemon exits.

Related (speculative): `winSession.Close` closes only the ConPTY and does not
terminate the child; a child that survives ConPTY closure leaves the
coalescer goroutine parked in `WaitForSingleObject(..., INFINITE)` forever
(Unix `Close` kills the process, so this is Windows-only).

## Risks

Slow kernel-object accumulation in a long-running daemon; bounded by pane
churn. The speculative wedge would also pin the pane's buffers.

## Suggested Solutions

`CloseHandle` inside the `waitOnce` after `WaitForSingleObject` returns (or in
`Close` after a final bounded wait). For the wedge: bounded wait +
`TerminateProcess` fallback, mirroring Unix `Process.Kill()`.
