package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/tui"
	versionpkg "github.com/artyomsv/quil/internal/version"
)

// writeVersionFrame sends a MsgVersionResp carrying version, echoing
// probeRequestID as a real daemon's respondTo would. writeFrame in
// remote_verify_test.go hardcodes "1.44.1" with no way to vary it, which is
// exactly what these gate tests need to do.
func writeVersionFrame(peer net.Conn, version string) error {
	msg, err := ipc.NewMessage(ipc.MsgVersionResp, ipc.VersionRespPayload{Version: version})
	if err != nil {
		return err
	}
	msg.ID = probeRequestID
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if err := binary.Write(peer, binary.BigEndian, uint32(len(raw))); err != nil {
		return err
	}
	_, err = peer.Write(raw)
	return err
}

// fakeDaemonAnswering returns a client wired to a daemon stand-in that answers
// every version probe it receives with version, so a gate test can pick the
// exact version the "far side" reports without touching the shared
// clientOverPipe/fakeDaemon fixtures other tests depend on.
//
// It loops rather than answering once: TestVerifyRemoteLinkGated_
// MatchingVersionPassesEitherWay drives two full round trips over the same
// client (gate=true then gate=false), and each verifyRemoteLinkGated call
// sends its own probe. Errors are swallowed rather than reported through t —
// t.Cleanup on clientOverPipe closes both ends once the test body returns,
// and this loop's next read or write is expected to fail at that point.
func fakeDaemonAnswering(t *testing.T, version string) *ipc.Client {
	t.Helper()
	client, peer := clientOverPipe(t)
	go func() {
		for {
			if err := readFrame(peer); err != nil {
				return
			}
			if err := writeVersionFrame(peer, version); err != nil {
				return
			}
		}
	}()
	return client
}

// A destination unreachable at launch was never gated, so the premise
// verifyRemoteLink was written on — "gateVersionCheck matched the versions at
// launch" — is false for it. Attaching anyway puts the client on a daemon whose
// protocol it does not fully speak, and an unhandled message type is dropped
// with no error anywhere.
func TestVerifyRemoteLinkGated_RefusesMismatchWhenGating(t *testing.T) {
	client := fakeDaemonAnswering(t, "1.53.0") // this TUI's version differs
	err := verifyRemoteLinkGated(client, time.Second, true)
	if err == nil {
		t.Fatal("gated verify accepted a version mismatch")
	}
	if !errors.Is(err, tui.ErrRemoteVersionMismatch) {
		t.Errorf("err = %v, want it to wrap ErrRemoteVersionMismatch so the row "+
			"can flip to needsUpgrade", err)
	}
}

// Mid-session behaviour is deliberately unchanged: refusing there would end a
// session whose panes are healthy.
func TestVerifyRemoteLinkGated_UngatedStillAcceptsMismatch(t *testing.T) {
	client := fakeDaemonAnswering(t, "1.53.0")
	if err := verifyRemoteLinkGated(client, time.Second, false); err != nil {
		t.Errorf("ungated verify returned %v, want nil (liveness probe only)", err)
	}
}

func TestVerifyRemoteLinkGated_MatchingVersionPassesEitherWay(t *testing.T) {
	client := fakeDaemonAnswering(t, versionpkg.Current())
	for _, gate := range []bool{true, false} {
		if err := verifyRemoteLinkGated(client, time.Second, gate); err != nil {
			t.Errorf("gate=%v: %v, want nil", gate, err)
		}
	}
}
