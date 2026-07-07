package main

import (
	"errors"
	"log"
	"net"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// guardHandshakeTimeout caps how long the single-instance guard waits for a
// MsgVersionResp before concluding the peer on the socket is not a healthy
// Quil daemon.
const guardHandshakeTimeout = 2 * time.Second

// daemonAlreadyHealthy reports whether a healthy Quil daemon is already
// listening on sockPath.
//
// A bare connection is NOT sufficient evidence: a wedged daemon (accepts
// connections but never processes IPC — a documented failure class, see
// snapshotWatchdog / wedge_regression_test.go) and an unrelated same-user
// process squatting the socket path would both accept a dial. Deferring to
// either would refuse a legitimate startup and leave the user with a daemon
// that can't actually serve clients. The guard therefore completes a real
// MsgVersionReq/Resp handshake: only a peer that answers the Quil protocol
// within the timeout counts as "already running". A dial failure, a timeout,
// or any protocol error means the socket is stale, wedged, or foreign — the
// caller should reclaim it (Server.Start removes + re-listens) rather than
// defer to something that isn't a working daemon.
func daemonAlreadyHealthy(sockPath string) bool {
	return daemonAlreadyHealthyWithin(sockPath, guardHandshakeTimeout)
}

// daemonAlreadyHealthyWithin is daemonAlreadyHealthy with the handshake timeout
// injected so tests can exercise the wedged-daemon (accepts, never answers)
// branch without the 2 s production budget.
func daemonAlreadyHealthyWithin(sockPath string, timeout time.Duration) bool {
	client, err := ipc.NewClient(sockPath)
	if err != nil {
		return false // nothing listening, or an unusable socket file
	}
	defer client.Close()

	req, err := ipc.NewMessage(ipc.MsgVersionReq, struct{}{})
	if err != nil {
		return false
	}
	req.ID = "guard-version-probe"
	if err := client.Send(req); err != nil {
		return false
	}

	if err := client.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return false
	}
	defer client.SetReadDeadline(time.Time{})

	// Loop past any unrelated frame until we see the version response or the
	// deadline fires. The daemon answers MsgVersionReq unconditionally
	// (daemon.go handleMessage), so a live daemon always replies in time.
	for {
		msg, err := client.Receive()
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				log.Printf("guard: socket answered but no MsgVersionResp within %s — treating as stale/wedged", timeout)
			}
			return false
		}
		if msg.Type == ipc.MsgVersionResp {
			return true // a live daemon speaking the Quil protocol
		}
	}
}
