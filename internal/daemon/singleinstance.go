package daemon

import (
	"fmt"
	"net"
	"time"
)

// probeExistingDaemon dials the daemon socket. A successful connect means a
// live listener is serving this QUIL_HOME — starting a second daemon would
// unlink its socket (Server.Start removes the path unconditionally) and
// overwrite the PID file, bricking the original for new clients. A failed
// connect means the socket is stale or absent; the normal stale-cleanup
// proceeds.
//
// Socket reachability is the invariant that matters, and it is portable —
// a PID-file liveness check would need per-OS process probing and can lie
// after PID reuse. On Windows this relies on Go's net.DialTimeout("unix", ...)
// support for Windows 10+ AF_UNIX; quil's production IPC already serves
// quild.sock over AF_UNIX on Windows, so the probe is portable by the same
// precondition the daemon itself depends on.
//
// Limitation: reachability == refusal, identity is not verified. The probe
// cannot tell a healthy quil daemon from a foreign process bound to the
// path, or from a wedged daemon that accepts but never reads.
func probeExistingDaemon(socketPath string) error {
	// 500 ms is generous margin only: an absent/stale socket fails instantly
	// (ECONNREFUSED / not-found) and a live daemon accepts in <1 ms — the
	// timeout exists solely to bound a pathologically slow kernel path.
	conn, err := net.DialTimeout("unix", socketPath, 500*time.Millisecond)
	if err != nil {
		return nil
	}
	_ = conn.Close() // probe only — the answer is already known
	return fmt.Errorf("socket %s is accepting connections (an existing quil daemon, most likely) — refusing to start; if you are certain no daemon is running, remove the socket file", socketPath)
}
