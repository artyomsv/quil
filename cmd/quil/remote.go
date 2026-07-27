package main

import (
	"errors"
	"os"
)

// remoteDest is empty for a local session and holds the --remote destination
// otherwise. Written once in main() before any daemon-lifecycle function can
// run, then read-only for the life of the process.
var remoteDest string

// remoteMode reports whether this TUI is attached to a daemon on another host.
func remoteMode() bool { return remoteDest != "" }

// errRemoteMode is returned by the local daemon-lifecycle functions when the
// session is attached to a remote daemon. They all resolve
// config.SocketPath()/config.PidPath() internally, so running them here would
// act on THIS machine's daemon — which is not the one the user is looking at.
var errRemoteMode = errors.New("not available while attached to a remote daemon")

// exitFn is os.Exit, swappable so the guards can be tested. Mirrors the
// existing seam pattern (stopDaemonForUpgradeFn, spawnDaemonForUpgradeFn).
var exitFn = os.Exit
