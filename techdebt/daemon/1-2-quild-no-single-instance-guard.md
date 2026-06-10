# quild has no single-instance guard — second daemon clobbers PID file and socket

| Field | Value |
|-------|-------|
| Criticality | Critical |
| Complexity | Small |
| Location | `cmd/quild/main.go` (startup), `internal/daemon` (socket bind path) |
| Found during | debugging (ConPTY ghost window investigation, 2026-06-10) |
| Date | 2026-06-10 |

## Issue

The TUI checks the PID file before auto-starting a daemon, but `quild --background` itself does
not. A second `quild` started against the same `QUIL_HOME` while a daemon is already running:

1. overwrites `quild.pid` with its own PID, and
2. unlinks/re-binds `quild.sock` (stale-socket cleanup assumes the file is stale),

even though the original daemon is alive and serving clients. The original daemon keeps its
(now unlinked) listening socket — existing connections survive, but **no new client can ever
reach it again**. If the second daemon then exits, the socket path points at a dead socket and
connections are refused entirely.

Observed live on 2026-06-10: a stray `quild-dev.exe` (see
`1-3-pane-env-quil-home-retargets-dev-builds-at-production.md`) clobbered the production socket;
the running daemon's panes stayed alive but new attaches failed with `ECONNREFUSED` until a
manual graceful restart.

## Risks

- Production daemon silently bricked for new connections — looks like "quil won't start" to the user.
- PID file lies about which process is the daemon → kill scripts target the wrong process.
- Data-loss adjacent: a hard kill of the "wrong" daemon loses unsnapshotted state.

## Suggested Solutions

1. On startup, before touching PID file or socket: read existing PID file; if the process is
   alive (and ideally verifiably a quild — check executable name), log and exit with a clear
   "daemon already running (pid N)" error.
2. Only treat the socket as stale when a test-connect fails AND the PID-file process is dead.
3. Optional: hold an exclusive lock file (`O_EXCL`/`LockFileEx`) for the daemon's lifetime so the
   check is race-free.
