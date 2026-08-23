package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

// A daemon this client did not start controls the version string, and nothing
// on the wire bounds it below ipc.maxFrameSize (10 MB). versionpkg.Parsed
// truncates at the first "-", so "1.0.0-"+<megabytes> compares as an ordinary
// 1.0.0 and the whole payload would flow into gateExtraVersion's error text,
// then into OfflineState.Detail, and be re-measured on every rendered frame.
func TestClampVersionString(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"a real version is untouched", "1.63.2", "1.63.2"},
		{"a pre-release version is untouched", "1.63.2-rc1+build.7", "1.63.2-rc1+build.7"},
		{"empty stays empty", "", ""},
		{"exactly at the limit is untouched", strings.Repeat("a", maxVersionStringLen), strings.Repeat("a", maxVersionStringLen)},
		{"one over is cut", strings.Repeat("a", maxVersionStringLen+1), strings.Repeat("a", maxVersionStringLen)},
		{"a hostile payload is cut", "1.0.0-" + strings.Repeat("A", 9_000_000), "1.0.0-" + strings.Repeat("A", maxVersionStringLen-6)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampVersionString(tc.in); got != tc.want {
				t.Errorf("clampVersionString(%d bytes) = %d bytes, want %d",
					len(tc.in), len(got), len(tc.want))
			}
		})
	}
}

// The cut must land on a rune boundary: sanitizeRemoteText downstream documents
// valid UTF-8 as a PRECONDITION, and a severed multi-byte sequence decodes to
// U+FFFD — a rune outside every range it strips, so it would pass through.
func TestClampVersionString_CutsOnARuneBoundary(t *testing.T) {
	// Multi-byte runes chosen so the byte limit falls mid-sequence.
	got := clampVersionString(strings.Repeat("日", 40)) // 3 bytes each = 120
	if !utf8.ValidString(got) {
		t.Fatalf("clamped value is not valid UTF-8: %q", got)
	}
	if len(got) > maxVersionStringLen {
		t.Errorf("clamped to %d bytes, over the %d-byte limit", len(got), maxVersionStringLen)
	}
	if len(got) < maxVersionStringLen-3 {
		t.Errorf("clamped to %d bytes, wasting more than one rune of the %d-byte budget",
			len(got), maxVersionStringLen)
	}
}
