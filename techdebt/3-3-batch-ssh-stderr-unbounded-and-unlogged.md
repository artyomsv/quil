# Batch ssh dials buffer stderr without a cap and without logging it

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Medium |
| Location | `internal/transport/ssh.go` (`lockedBuffer`, the `opts.Batch` arm), `internal/transport/stdioconn.go` (`Stderr`, `LinkErr`), `cmd/quil/remote.go` (`redialRemote`) |
| Found during | Code review of PR #113 (remote-daemon Phase 2 reconnect) — raised independently by the security and code-quality passes |
| Date | 2026-07-30 |

## Issue

`SSHOptions.Batch` makes `SSH()` capture the child's stderr into a `lockedBuffer`
— a mutex around a plain `bytes.Buffer` with `Write` and `String` and nothing
else. Nothing drains it, caps it, or resets it. `LinkErr`'s 2000-byte
`truncateForMessage` bounds the error MESSAGE, not the buffer behind it; the
existing comment in `ssh.go` says as much.

That was fine while batch dials were short-lived: the `quil remote setup` probe
and `RunSSH` one-shots both exit in seconds. **Phase 2 changed the lifetime.**
`redialRemote` dials with `Batch=true`, and a reconnect that SUCCEEDS keeps that
conn for the rest of the session — so the buffer is now session-length.

ssh multiplexes the remote command's fd 2 onto its own stderr
(`SSH_MSG_CHANNEL_EXTENDED_DATA`), and this project's threat model already treats
that stream as remote-influenced — which is why the non-batch path wraps it in
`terminalSanitizer` and why `LinkErr` sanitizes before returning.

Two consequences:

1. **Unbounded growth with a remote-influenced writer.** A compromised or merely
   noisy remote that writes continuously to fd 2 after a reconnect grows the
   local process without bound. The next drop amplifies it: `LinkErr` runs
   `sanitizeForTerminal` over the WHOLE buffer — a full `strings.Map` copy —
   before truncating. Not reachable benignly: `quil --stdio` writes two bounded
   lines. This is the hostile-remote case the transport defends against
   everywhere else.
2. **An observability regression, and this one bites in normal use.** On the
   non-batch startup dial those diagnostics are sanitized into `quil.log` via
   `RedirectStderr`. After a reconnect they go into the invisible buffer instead,
   because `remoteStderrRedirectFn` cannot help — `termErr` is nil on the batch
   path. So a genuine mid-session `Timeout, server not responding` is lost unless
   a LATER drop happens to surface it through `LinkErr`. Diagnosing a flapping
   link gets harder after the first reconnect, which is exactly when you need it.

## Risks

- Memory growth in a long-lived TUI, attributable to the far side.
- Post-reconnect ssh warnings vanish, degrading exactly the diagnosis path that
  Phase 1.5 (RD-002) built deliberately.
- Both scale with session length, so they are worst for the workload remote mode
  exists to serve — an agent left running for days.

## Suggested Solutions

1. **Cap the buffer, keep the tail.** Give `lockedBuffer` a ring or a
   trim-on-write that retains the last N KiB. `LinkErr` already truncates to
   2000 bytes and wants the most recent output, so nothing useful is lost. The
   smallest change that removes the unbounded property.
2. **Decouple "BatchMode=yes" from "capture into a buffer."** Add
   `SSHOptions.StderrSink io.Writer`; batch dials tee into it. `redialRemote` can
   pass the rotating log writer `launchTUI` already holds, which fixes the
   observability half as well — the sanitizer still sits in front, so nothing
   unsanitized reaches the log. Preferred: it addresses both consequences with
   one seam, and the sanitizer/`switchWriter` machinery already exists.
3. Drain the buffer into the log on a timer. Rejected — a periodic goroutine per
   conn to work around a missing cap is more moving parts than either fix above.

## Related

- `techdebt/3-3-stdioconn-close-races-pump-read-on-windows.md` — also
  `internal/transport`, also a property that only became reachable once Phase 2
  gave batch dials a long life.
- PR #113. `docs/roadmap/remote-daemon.md` § Work registry (RD-011).
