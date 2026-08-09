package main

import (
	"testing"

	"github.com/artyomsv/quil/internal/transport"
)

// The far side is ALLOWED 30 s to bring its daemon up (stdio.go's
// waitForDaemonReady), and the daemon respawns active and eager panes serially
// before it listens. A client budget below that abandons a remote that is still
// legitimately starting — and the penalty was the host's projects vanishing.
// Asserted against the constants rather than written as two numbers, because
// two numbers are what drifted apart in the first place.
func TestExtraDialTimeout_ExceedsTheRemotesOwnReadinessBudget(t *testing.T) {
	min := daemonReadyTimeout + transport.DefaultConnectTimeout
	if extraDialTimeout <= min {
		t.Errorf("extraDialTimeout = %v, must exceed %v (remote readiness + ssh connect)",
			extraDialTimeout, min)
	}
}

// 2 s is sized for a Unix socket. The ssh path adds a WAN round-trip against a
// daemon that may be finishing a restore.
func TestRemoteHandshakeTimeout_ExceedsTheLocalOne(t *testing.T) {
	if remoteHandshakeTimeout <= handshakeTimeout {
		t.Errorf("remoteHandshakeTimeout = %v, want more than the local %v",
			remoteHandshakeTimeout, handshakeTimeout)
	}
}
