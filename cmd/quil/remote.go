package main

import (
	"errors"
	"os"
	"strings"
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

// parseRemoteFlag extracts --remote <dest> or --remote=<dest> from args,
// returning the destination and args with the flag removed. args includes
// argv[0]; it is returned unchanged when the flag is absent.
//
// The destination is never interpreted here — it goes to ssh verbatim so a
// ~/.ssh/config Host alias keeps its HostName, Port, User and ProxyJump.
func parseRemoteFlag(args []string) (dest string, rest []string, err error) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--remote":
			if i+1 >= len(args) || args[i+1] == "" {
				return "", nil, errors.New("--remote requires a destination, e.g. --remote gpu01")
			}
			dest = args[i+1]
			i++
		case strings.HasPrefix(arg, "--remote="):
			dest = strings.TrimPrefix(arg, "--remote=")
			if dest == "" {
				return "", nil, errors.New("--remote requires a destination, e.g. --remote gpu01")
			}
		default:
			rest = append(rest, arg)
		}
	}
	return dest, rest, nil
}
