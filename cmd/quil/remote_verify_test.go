package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// clientOverPipe builds a real *ipc.Client whose far end is a net.Pipe the test
// controls, so the probe can be driven without a socket or an ssh child.
func clientOverPipe(t *testing.T) (*ipc.Client, net.Conn) {
	t.Helper()
	ours, theirs := net.Pipe()
	client, err := ipc.NewClientWithDialer(context.Background(),
		func(context.Context) (net.Conn, error) { return ours, nil })
	if err != nil {
		t.Fatalf("NewClientWithDialer: %v", err)
	}
	t.Cleanup(func() { client.Close(); theirs.Close() })
	return client, theirs
}

// The IPC wire format is a 4-byte big-endian length prefix followed by the JSON
// message. Spoken directly here rather than through an exported test helper on
// internal/ipc: a production API added only for a test in another package is a
// worse trade than twenty lines that also exercise the real framing.
func readFrame(peer net.Conn) error {
	var n uint32
	if err := binary.Read(peer, binary.BigEndian, &n); err != nil {
		return err
	}
	_, err := io.CopyN(io.Discard, peer, int64(n))
	return err
}

func writeFrame(peer net.Conn, typ string) error {
	msg, err := ipc.NewMessage(typ, ipc.VersionRespPayload{Version: "1.44.1"})
	if err != nil {
		return err
	}
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

// fakeDaemon reads one frame and replies with the given message type.
func fakeDaemon(t *testing.T, peer net.Conn, replyType string) {
	t.Helper()
	go func() {
		if err := readFrame(peer); err != nil {
			return
		}
		writeFrame(peer, replyType)
	}()
}

// A daemon that answers the probe means the link is genuinely up.
func TestVerifyRemoteLink_LiveDaemon_Succeeds(t *testing.T) {
	client, peer := clientOverPipe(t)
	fakeDaemon(t, peer, ipc.MsgVersionResp)

	if err := verifyRemoteLink(client, 3*time.Second); err != nil {
		t.Errorf("verifyRemoteLink on a live daemon = %v, want nil", err)
	}
}

// THE REGRESSION. A dial "succeeds" as soon as ssh's binary starts, so against
// an unreachable host the connection exists and simply never answers. Reporting
// that as a restored link produced a reconnect loop that cleared the banner and
// reset every pane every few hundred milliseconds, leaving them blank, with the
// attempt counter stuck at 1 because each false success zeroed the backoff.
func TestVerifyRemoteLink_SilentPeer_FailsRatherThanReportingSuccess(t *testing.T) {
	client, peer := clientOverPipe(t)
	_ = peer // deliberately never answers, like ssh still trying to connect

	err := verifyRemoteLink(client, 250*time.Millisecond)
	if err == nil {
		t.Fatal("a silent peer was reported as a verified link — this is the flicker/blank-panes bug")
	}
	if !strings.Contains(err.Error(), "await version response") {
		t.Errorf("err = %v, want it to name the awaited response", err)
	}
}

// A peer that closes without answering — ssh exiting after ConnectTimeout — must
// also fail rather than pass.
func TestVerifyRemoteLink_PeerCloses_Fails(t *testing.T) {
	client, peer := clientOverPipe(t)
	peer.Close()

	if err := verifyRemoteLink(client, 3*time.Second); err == nil {
		t.Fatal("a closed peer was reported as a verified link")
	}
}

// Frames that are not the response are skipped, not mistaken for it. A broadcast
// can land in this window, and treating the first frame as the answer would make
// the probe pass on any traffic at all.
func TestVerifyRemoteLink_SkipsUnrelatedFramesThenSucceeds(t *testing.T) {
	client, peer := clientOverPipe(t)
	go func() {
		if err := readFrame(peer); err != nil {
			return
		}
		for _, typ := range []string{ipc.MsgWorkspaceState, ipc.MsgPaneOutput, ipc.MsgVersionResp} {
			if err := writeFrame(peer, typ); err != nil {
				return
			}
		}
	}()

	if err := verifyRemoteLink(client, 3*time.Second); err != nil {
		t.Errorf("verifyRemoteLink = %v, want nil after skipping unrelated frames", err)
	}
}

// The probe must clear its deadline on the way out. Leaving one set would make
// the session's ordinary reads start timing out a few seconds after a reconnect,
// which reads as a second link failure and would loop.
func TestVerifyRemoteLink_ClearsDeadlineOnSuccess(t *testing.T) {
	client, peer := clientOverPipe(t)
	fakeDaemon(t, peer, ipc.MsgVersionResp)

	if err := verifyRemoteLink(client, 3*time.Second); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// A read that outlives the probe's deadline must still be waiting, not
	// timed out. net.Pipe honours deadlines, so a leaked one surfaces here.
	done := make(chan error, 1)
	go func() {
		_, err := client.Receive()
		done <- err
	}()
	select {
	case err := <-done:
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			t.Error("the probe's read deadline outlived it; ordinary reads will time out after a reconnect")
		}
	case <-time.After(500 * time.Millisecond):
		// Still blocked, which is correct: no deadline is in force.
	}
}
