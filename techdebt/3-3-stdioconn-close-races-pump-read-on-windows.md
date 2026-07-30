# stdioConn.Close can block forever racing the pump's read on Windows

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Medium |
| Location | `internal/transport/stdioconn.go` (`Close` line ~276, `pump` line ~176) |
| Found during | Windows native test run for remote-daemon Phase 2 (RD-010…RD-016) |
| Date | 2026-07-30 |
| Platform | Windows only — Linux is unaffected and deterministic |

## Issue

`internal/transport`'s test binary intermittently hangs on Windows and is killed
by the test timeout. Captured goroutine dump at the moment of the hang:

```
goroutine 117 [syscall]:
  syscall.ReadFile(...)
  internal/poll.(*FD).Read.func1 → internal/poll.execIO → internal/poll.(*FD).Read
  os.(*File).Read
  transport.(*stdioConn).pump(...)            stdioconn.go:176

goroutine 116 [semacquire]:
  internal/poll.runtime_Semacquire(...)
  internal/poll.(*FD).Close(...)              fd_windows.go:489
  os.(*File).Close
  transport.(*stdioConn).Close.func1()        stdioconn.go:276
```

`Close` blocks in `c.r.Close()`. Go's `internal/poll.FD.Close` waits on a
semaphore until every in-flight I/O reference on the descriptor drains, and the
pump's parked `ReadFile` holds one. On Windows the pipe handle is
**non-overlapped** — the same property that already forces `SetReadDeadline` to
be implemented in the adapter rather than by the OS — so that read cannot be
cancelled and the reference never drops.

Linux does not have this problem: `os.Pipe` descriptors go through netpoll,
where `Close` unblocks pending reads.

**The ordering compounds it.** `Close` runs `c.w.Close()` then `c.r.Close()`
*before* the `c.cmd.Process != nil` block that kills the child. Killing ssh is
what closes ssh's stdout and thereby ends the pump's read — but on Windows we
block before ever reaching it.

## How reproducible

| Run mode | Result |
|---|---|
| `internal/transport` whole package, Windows | hangs ~1 in 2 to 3 in 4 runs |
| `TestStdioConn_ReadAfterClose_ReturnsError` isolated, Windows | 5/5 pass |
| Whole package, Linux | deterministic pass |
| Same code on `master` | identical behaviour (transport is unchanged by Phase 2) |

It is a **race**, not a state. A probe that sleeps 300 ms so the pump is
definitely inside `ReadFile` returns promptly in all three process states
(never-started, started-with-shared-stdin, started-unattached) — so the hang
needs `Close` to land in a narrow window, and whole-package load is what makes
that window get hit.

## Risks

- **Windows test suites are unreliable.** This is the primary development
  platform for this repo, and a flaky hang trains people to re-run rather than
  read failures. It also makes `internal/transport` unusable as a pre-merge gate
  on Windows.
- **A plausible but UNPROVEN production hang.** The same `Close` runs on two
  live paths: the TUI exit path for a remote session, and `redialRemote`, which
  closes the dead client at the top of every reconnect attempt. If `Close`
  blocks there, the redial runs on a `tea.Cmd` goroutine and the reconnect never
  reports a result — the banner would sit at "attempt 1" forever with input
  frozen. **This has not been observed on a real ssh session**; it is asserted
  here as a mechanism with no structural guard against it, not as a sighting.
- The window is widest exactly when the pump has nothing to read, which is the
  steady state of a healthy idle link.

## Suggested Solutions

1. **Kill the child before closing the read handle.** Reordering `Close` so the
   process is signalled first means ssh's stdout closes, the pump's `ReadFile`
   returns, and `c.r.Close()` finds no in-flight reference. This is the direct
   fix and it is NOT free: `Close` deliberately waits for a natural exit before
   killing, because on Windows `Kill` is `TerminateProcess(handle, 1)` and would
   overwrite the child's real exit status — and `remoteinstall` detection reads
   exactly those codes (127 = remote command not found, 126 = found but not
   executable). Any reordering has to keep the `pumpFailed()`/`exitGrace` path
   that preserves them, so this is a considered change rather than a swap of two
   lines.
2. **Do not close the read handle from `Close` at all.** Let the pump own the
   descriptor's lifetime and close it on its own way out, with `Close` only
   signalling `done` and killing the child. Removes the cross-goroutine
   close-during-read entirely, at the cost of a descriptor living until the pump
   notices.
3. **Bound it.** Run `c.r.Close()` in a goroutine and give it a short grace
   period, matching the existing `exitGrace` idiom. Weakest option: it converts
   a hang into a leaked goroutine and descriptor rather than fixing the cause,
   but it is small and it un-wedges the reconnect path.

Not viable: `SetReadDeadline` on the pump's read. That is precisely what the
adapter exists to work around, because Windows non-overlapped pipe handles do
not support it.

## Related

- `internal/transport/stdioconn.go` — the single-reader contract and the
  adapter-implemented deadline, both consequences of the same non-overlapped
  handle property.
- `docs/roadmap/remote-daemon.md` § Work registry — RD-011 (`redialRemote`) is
  the caller that makes this reachable more than once per session.
- `techdebt/3-3-discovery-scan-cannot-be-interrupted-mid-syscall.md` — same
  shape of problem one layer up: a blocking syscall that no context or close can
  interrupt.
