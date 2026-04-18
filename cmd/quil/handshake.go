package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	versionpkg "github.com/artyomsv/quil/internal/version"
)

// handshakeTimeout caps how long the TUI will wait for MsgVersionResp.
// Daemons from before the version-negotiation protocol silently ignore
// the request; the timeout is the only way to notice.
const handshakeTimeout = 2 * time.Second

// handshakeResult bundles the outcome of asking the daemon its version.
type handshakeResult struct {
	// Matched is true only when both versions parse as semver and compare equal.
	Matched bool

	// Cmp is -1 when TUI < daemon, +1 when TUI > daemon, 0 when equal.
	// Zero when ClientSkipped or DaemonUnknown is true.
	Cmp int

	// DaemonVersion is the raw version string the daemon reported. Empty
	// when the request timed out or failed to parse.
	DaemonVersion string

	// DaemonUnknown is true when the response timed out, errored, or
	// returned an unparseable version. Callers treat this as "pre-
	// versioning daemon" and drive it through the restart-confirm flow.
	DaemonUnknown bool

	// ClientSkipped is true when the client is running a non-release
	// build (`dev`, empty, garbage) and the handshake was skipped. No
	// action should be taken.
	ClientSkipped bool
}

// versionHandshake sends MsgVersionReq to the daemon and interprets the
// response. Callers use the returned handshakeResult to decide whether
// to attach normally, open the upgrade-client dialog, or enter the
// daemon-restart confirmation flow.
//
// The function is defensive: any transport error, parse error, or
// timeout becomes DaemonUnknown = true rather than an error return.
// Propagating errors here would force every caller to branch the same
// way twice (error vs match vs mismatch); encoding the "unknown" state
// in the result keeps the call site simple.
func versionHandshake(client *ipc.Client) handshakeResult {
	tuiVer := versionpkg.Current()

	// Skip the handshake entirely for non-release builds. A `dev` TUI
	// running against any daemon is almost certainly a developer's own
	// build — they can deal with version drift manually. Skipping also
	// sidesteps false positives when `main.version` was never set via
	// ldflags.
	if !versionpkg.IsRelease() {
		log.Printf("handshake: skipping — TUI version %q is not a release build", tuiVer)
		return handshakeResult{ClientSkipped: true}
	}

	reqID := fmt.Sprintf("hs-%d", time.Now().UnixNano())
	req, err := ipc.NewMessage(ipc.MsgVersionReq, struct{}{})
	if err != nil {
		log.Printf("handshake: build request: %v", err)
		return handshakeResult{DaemonUnknown: true}
	}
	req.ID = reqID

	if err := client.Send(req); err != nil {
		log.Printf("handshake: send MsgVersionReq: %v", err)
		return handshakeResult{DaemonUnknown: true}
	}

	// Install a read deadline so a pre-versioning daemon (which drops
	// the request silently) doesn't block us forever.
	deadline := time.Now().Add(handshakeTimeout)
	if err := client.SetReadDeadline(deadline); err != nil {
		log.Printf("handshake: set read deadline: %v", err)
		return handshakeResult{DaemonUnknown: true}
	}
	defer client.SetReadDeadline(time.Time{})

	// Loop until we see a matching response or hit the deadline. Any
	// unrelated messages (there shouldn't be any pre-attach) are ignored.
	for {
		msg, err := client.Receive()
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				log.Printf("handshake: timeout waiting for MsgVersionResp — treating daemon as pre-versioning")
			} else {
				log.Printf("handshake: receive: %v", err)
			}
			return handshakeResult{DaemonUnknown: true}
		}
		if msg.Type != ipc.MsgVersionResp || msg.ID != reqID {
			log.Printf("handshake: ignoring unrelated message type=%q id=%q", msg.Type, msg.ID)
			continue
		}

		var payload ipc.VersionRespPayload
		if err := msg.DecodePayload(&payload); err != nil {
			log.Printf("handshake: decode payload: %v", err)
			return handshakeResult{DaemonUnknown: true}
		}

		cmp, err := versionpkg.Compare(tuiVer, payload.Version)
		if err != nil {
			log.Printf("handshake: compare %q vs %q: %v", tuiVer, payload.Version, err)
			return handshakeResult{DaemonVersion: payload.Version, DaemonUnknown: true}
		}
		return handshakeResult{
			Matched:       cmp == 0,
			Cmp:           cmp,
			DaemonVersion: payload.Version,
		}
	}
}
