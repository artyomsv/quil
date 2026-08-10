# `Conn.write`'s 30 s deadline silently does nothing over ssh

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Small |
| Location | `internal/ipc/server.go` (`write`, `SetWriteDeadline` call) · `internal/transport/stdioconn.go` (`SetWriteDeadline`) |
| Found during | Security review of PR #148 |
| Date | 2026-08-09 |

## Issue

`Conn.write` installs `writeDeadline` (30 s) before every frame and documents it
as the "belt-and-suspenders catch for kernel-buffer wedges" that guarantees a
deterministic cleanup ceiling.

On the ssh transport that guarantee does not exist. `stdioConn.SetWriteDeadline`
returns `os.ErrNoDeadline` and installs nothing — Windows `os.Pipe` handles are
non-overlapped, so the adapter cannot honour it — and `Conn.write` discards the
returned error. A `sendLoop` writing to a remote daemon that has stopped reading
can therefore park in `Write` indefinitely.

Two further properties make it worse than a missing timeout on its own:

- When `write` does fail, `sendLoop` merely returns. It does not `Close`, so
  `c.done` stays open and `c.closed`/`c.overflow` stay false — the conn becomes
  a zombie rather than a detectably dead one.
- `MsgHeartbeat` is declared but never sent, so nothing else notices.

## Why it is not currently a bug

`Client.Send` no longer depends on it. PR #148 bounded the client-side wait with
`clientSendTimeout`, which fires on its own timer and closes the conn regardless
of whether the write deadline exists — which is precisely what makes the bound
hold on both transports.

The daemon side is unaffected: it only ever writes to Unix sockets, where
`SetWriteDeadline` works.

## Why it is still worth fixing

The comment on `writeDeadline` claims a ceiling the code cannot deliver on one
of its two transports, and the next person to rely on it will not discover that
from reading `server.go`. The failure mode is an indefinitely parked goroutine
in a daemon that runs for weeks.

## Fix

1. Stop discarding `SetWriteDeadline`'s error in `Conn.write` — at minimum log
   once per conn when the transport cannot honour a deadline, so the gap is
   visible rather than inferred.
2. Have `sendLoop` `Close` the conn on write failure instead of returning, so a
   failed write produces a detectably dead conn.
3. Amend `writeDeadline`'s comment to say which transports honour it.

A real deadline for `stdioConn` writes would need the same pump-and-select
treatment its `Read` side already has; that is a larger change and is not
required to close this.
