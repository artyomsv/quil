package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/remoteinstall"
	"github.com/artyomsv/quil/internal/transport"
	"github.com/artyomsv/quil/internal/tui"
	versionpkg "github.com/artyomsv/quil/internal/version"
)

// fakeChild is an ssh child whose natural exit and kill a test controls. Its
// status follows the real one: the code it chose once it exits on its own, -1
// once it was killed first.
type fakeChild struct {
	mu     sync.Mutex
	exited chan struct{}
	code   int
	killed bool
}

// newFakeChild exits with code after `after`; after <= 0 means never.
func newFakeChild(t *testing.T, after time.Duration, code int) *fakeChild {
	t.Helper()
	c := &fakeChild{exited: make(chan struct{}), code: code}
	if after > 0 {
		tm := time.AfterFunc(after, func() { close(c.exited) })
		t.Cleanup(func() { tm.Stop() })
	}
	return c
}

func (c *fakeChild) kill() {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.exited:
	default:
		c.killed = true
	}
}

func (c *fakeChild) exitCode() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.killed {
		return -1
	}
	select {
	case <-c.exited:
		return c.code
	default:
		return -1
	}
}

func (c *fakeChild) waitExited(d time.Duration) bool {
	select {
	case <-c.exited:
		return true
	case <-time.After(d):
		return false
	}
}

// killingConn kills the fake child on Close, as stdioConn.Close does.
type killingConn struct {
	net.Conn
	child *fakeChild
}

func (c killingConn) Close() error {
	c.child.kill()
	return c.Conn.Close()
}

// deadLinkGateSeams wires a gate run against a link that never delivered a byte
// and whose child is child. It returns the remedy the gate offered.
func deadLinkGateSeams(t *testing.T, child *fakeChild, grace time.Duration) (*ipc.Client, *remoteinstall.Remedy) {
	t.Helper()
	prevEstablished := remoteLinkEstablishedFn
	prevErr := remoteLinkErrFn
	prevExitCode := remoteExitCodeFn
	prevWait := remoteWaitExitedFn
	prevOffer := offerRemoteInstallFn
	prevExit := exitFn
	prevGrace := deadLinkExitGrace
	t.Cleanup(func() {
		remoteLinkEstablishedFn = prevEstablished
		remoteLinkErrFn = prevErr
		remoteExitCodeFn = prevExitCode
		remoteWaitExitedFn = prevWait
		offerRemoteInstallFn = prevOffer
		exitFn = prevExit
		deadLinkExitGrace = prevGrace
	})

	remoteLinkEstablishedFn = func() bool { return false }
	remoteLinkErrFn = func() error { return nil }
	remoteExitCodeFn = child.exitCode
	remoteWaitExitedFn = child.waitExited
	deadLinkExitGrace = grace
	offered := remoteinstall.Remedy(-1)
	offerRemoteInstallFn = func(_ string, r remoteinstall.Remedy) bool {
		offered = r
		return false
	}
	exitFn = func(int) {}

	ours, peer := net.Pipe()
	peer.Close()
	client, err := ipc.NewClientWithDialer(context.Background(),
		func(context.Context) (net.Conn, error) { return killingConn{Conn: ours, child: child}, nil })
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return client, &offered
}

// The measured PowerShell case: the far side rejects the command and exits 1,
// but only after the gate has given up waiting for a byte. Closed at once, the
// kill turned the 1 into -1 and the probe that heals a stale shell record
// never ran; the grace lets the real status through.
func TestGateVersionCheck_SlowShellExitsOne_ReadsItAndProbes(t *testing.T) {
	withRemote(t, "winbox")
	child := newFakeChild(t, 300*time.Millisecond, 1)
	client, offered := deadLinkGateSeams(t, child, 5*time.Second)

	captureStderr(t, func() { gateVersionCheck(client) })

	if *offered != remoteinstall.RemedyProbe {
		t.Errorf("remedy = %v, want RemedyProbe — the gate closed before the child's exit 1", *offered)
	}
}

// A child that never exits is a genuinely hung connection: after the grace the
// gate closes it and reports exactly what it reported before the grace existed.
func TestGateVersionCheck_HungShell_ClosesAfterTheGrace(t *testing.T) {
	withRemote(t, "winbox")
	child := newFakeChild(t, 0, 1)
	client, offered := deadLinkGateSeams(t, child, 50*time.Millisecond)

	start := time.Now()
	out := captureStderr(t, func() { gateVersionCheck(client) })

	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("gate took %v against a hung child, want about its 50 ms grace", elapsed)
	}
	if *offered != remoteinstall.RemedyNone {
		t.Errorf("remedy = %v, want RemedyNone for a killed child", *offered)
	}
	if !strings.Contains(out, "Cannot reach the Quil daemon on winbox") {
		t.Errorf("stderr = %q, want the link-failure report", out)
	}
}

// childLink is a transport.LinkStatus over a fakeChild that never delivered a
// byte.
type childLink struct{ child *fakeChild }

func (l childLink) Established() bool               { return false }
func (l childLink) LinkErr() error                  { return nil }
func (l childLink) ExitCode() int                   { return l.child.exitCode() }
func (l childLink) WaitExited(d time.Duration) bool { return l.child.waitExited(d) }

// The background paths close too, and their verdict is the same exit code: a
// slow shell's 127 must survive to mark the host as missing quil rather than
// read as an unreachable one.
func TestDialExtra_SlowShellExit_KeepsItsStatus(t *testing.T) {
	asReleaseBuild(t, "1.0.0")
	prevGrace := deadLinkExitGrace
	t.Cleanup(func() { deadLinkExitGrace = prevGrace })
	deadLinkExitGrace = 5 * time.Second

	child := newFakeChild(t, 300*time.Millisecond, 127)
	ours, peer := net.Pipe()
	peer.Close()
	client, err := ipc.NewClientWithDialer(context.Background(),
		func(context.Context) (net.Conn, error) { return killingConn{Conn: ours, child: child}, nil })
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	stubDial(t, func(context.Context, config.Config, string, bool, io.Writer) (*ipc.Client, transport.LinkStatus, error) {
		return client, childLink{child: child}, nil
	})

	_, err = dialExtra(config.Config{}, config.Destination{Dest: "winbox"})()
	if !errors.Is(err, tui.ErrRemoteQuilMissing) {
		t.Errorf("err = %v, want ErrRemoteQuilMissing — Close killed the child before its 127", err)
	}
}

// delayedVersionResponder answers one version request after delay, marking
// answered just before its first byte leaves — which is what Established
// reports on a real link.
func delayedVersionResponder(t *testing.T, peer net.Conn, version string, delay time.Duration, answered *atomic.Bool) {
	t.Helper()
	go func() {
		var length uint32
		if err := binary.Read(peer, binary.BigEndian, &length); err != nil {
			return
		}
		raw := make([]byte, length)
		if _, err := io.ReadFull(peer, raw); err != nil {
			return
		}
		var req ipc.Message
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Errorf("responder: decode request: %v", err)
			return
		}
		time.Sleep(delay)
		resp, err := ipc.NewMessage(ipc.MsgVersionResp, ipc.VersionRespPayload{Version: version})
		if err != nil {
			t.Errorf("responder: build reply: %v", err)
			return
		}
		resp.ID = req.ID
		out, err := json.Marshal(resp)
		if err != nil {
			t.Errorf("responder: encode reply: %v", err)
			return
		}
		answered.Store(true)
		if err := binary.Write(peer, binary.BigEndian, uint32(len(out))); err != nil {
			return
		}
		peer.Write(out)
	}()
}

// healthyRemoteSeams makes Established report what the responder did.
func healthyRemoteSeams(t *testing.T, answered *atomic.Bool) *bool {
	t.Helper()
	prevEstablished := remoteLinkEstablishedFn
	prevExit := exitFn
	t.Cleanup(func() {
		remoteLinkEstablishedFn = prevEstablished
		exitFn = prevExit
	})
	remoteLinkEstablishedFn = answered.Load
	exited := false
	exitFn = func(int) { exited = true }
	return &exited
}

// An UNSTAMPED build (version "dev", e.g. a plain `go build` — dev.sh stamps
// every variant from VERSION) skips the version comparison, and used to skip
// the request with it — so the dead-link guard ran microseconds after ssh
// started, found no byte, and reported a healthy remote as unreachable.
func TestGateVersionCheck_UnstampedClient_WaitsForAHealthyRemote(t *testing.T) {
	withRemote(t, "gpu01")
	prev := versionpkg.Current()
	versionpkg.SetCurrent("dev")
	t.Cleanup(func() { versionpkg.SetCurrent(prev) })

	var answered atomic.Bool
	exited := healthyRemoteSeams(t, &answered)
	client, peer := clientOverPipe(t)
	delayedVersionResponder(t, peer, "1.2.3", 200*time.Millisecond, &answered)

	var got *ipc.Client
	out := captureStderr(t, func() { got = gateVersionCheck(client) })

	if *exited || got == nil {
		t.Errorf("gate refused a healthy remote (exited=%v client=%v):\n%s", *exited, got, out)
	}
}

// A stamped build — every dev.sh variant, dev included — waited only the local
// 2 s for its answer, so a remote that took longer (ssh connecting, `quil
// --stdio` starting a cold daemon, Windows PowerShell starting) was reported
// unreachable although it was about to answer.
func TestGateVersionCheck_ReleaseClient_WaitsPastTheLocalTimeout(t *testing.T) {
	withRemote(t, "gpu01")
	asReleaseBuild(t, "1.0.0")

	var answered atomic.Bool
	exited := healthyRemoteSeams(t, &answered)
	client, peer := clientOverPipe(t)
	delayedVersionResponder(t, peer, "1.0.0", handshakeTimeout+500*time.Millisecond, &answered)

	var got *ipc.Client
	out := captureStderr(t, func() { got = gateVersionCheck(client) })

	if *exited || got == nil {
		t.Errorf("gate refused a remote that answered after %v (exited=%v client=%v):\n%s",
			handshakeTimeout+500*time.Millisecond, *exited, got, out)
	}
}
