package main

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/remoteinstall"
)

// deadClient builds a real *ipc.Client whose peer is already gone, which is
// what a failed remote dial leaves behind: a live conn to a process that never
// answered. net.Pipe rather than a socket so the test touches no filesystem.
func deadClient(t *testing.T) *ipc.Client {
	t.Helper()
	ours, peer := net.Pipe()
	peer.Close()

	client, err := ipc.NewClientWithDialer(context.Background(),
		func(context.Context) (net.Conn, error) { return ours, nil })
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return client
}

// captureStderr redirects os.Stderr for the duration of fn.
//
// The pipe is drained CONCURRENTLY with the write. A single Fprintf of a few
// hundred bytes fits any platform's pipe buffer, so writing first and reading
// after happens to work — but it deadlocks rather than failing loudly the day
// the message grows past the buffer, and the message embeds a remote-supplied
// string bounded only by transport.maxStderrInMessage. Draining in parallel
// removes the size assumption entirely.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	prev := os.Stderr
	os.Stderr = w

	out := make(chan string, 1)
	go func() {
		var buf [1 << 16]byte
		n, _ := r.Read(buf[:])
		out <- string(buf[:n])
	}()

	fn()

	os.Stderr = prev
	w.Close()
	got := <-out
	r.Close()
	return got
}

// TestGateVersionCheck_RemoteLinkNeverEstablished_ExitsBeforeTheSwitch pins the
// ordering the guard depends on, at the call site rather than in its parts.
//
// Three separate invariants live in that ordering, and each has been wrong at
// some point in this branch's history:
//
//  1. It must run BEFORE the switch. The first arm returns early for every
//     non-release build, so a check placed inside the mismatch arm never runs
//     on a dev binary — and dev builds are what this repo mandates for its own
//     development. The TUI would then launch against a closed connection and
//     render a blank screen with no diagnosis.
//  2. It must run BEFORE client.Close(). Close unblocks the transport's pump
//     via its done channel, and that path can return without ever recording an
//     error — so LinkErr() would go nil and the misdiagnosis would return.
//  3. It must exit, not fall through. exitFn is a swappable var, so the
//     compiler cannot prove it does not return.
//
// This test is the only thing that fails if a refactor reorders them.
func TestGateVersionCheck_RemoteLinkNeverEstablished_ExitsBeforeTheSwitch(t *testing.T) {
	withRemote(t, "gpu01")

	prevEstablished := remoteLinkEstablishedFn
	prevErr := remoteLinkErrFn
	prevExit := exitFn
	t.Cleanup(func() {
		remoteLinkEstablishedFn = prevEstablished
		remoteLinkErrFn = prevErr
		exitFn = prevExit
	})

	remoteLinkEstablishedFn = func() bool { return false }
	remoteLinkErrFn = func() error { return errLinkTest }

	exitCode := -1
	exitFn = func(code int) { exitCode = code }

	var out string
	var got *ipc.Client
	out = captureStderr(t, func() {
		got = gateVersionCheck(deadClient(t))
	})

	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1", exitCode)
	}
	if got != nil {
		t.Error("gateVersionCheck returned a client after a dead link, want nil — it fell through into the switch")
	}
	if want := "Cannot reach the Quil daemon on gpu01"; !strings.Contains(out, want) {
		t.Errorf("stderr = %q, want it to contain %q", out, want)
	}
	// The whole point: this must not be reported as a version problem.
	if strings.Contains(out, "Version mismatch") {
		t.Errorf("stderr blames a version mismatch on a dead link:\n%s", out)
	}
}

// remoteGateSeams parks every remote seam the gate reads and restores them.
func remoteGateSeams(t *testing.T, established bool, exitCode int) *remoteinstall.Remedy {
	t.Helper()
	prevEstablished := remoteLinkEstablishedFn
	prevErr := remoteLinkErrFn
	prevExitCode := remoteExitCodeFn
	prevOffer := offerRemoteInstallFn
	prevExit := exitFn
	t.Cleanup(func() {
		remoteLinkEstablishedFn = prevEstablished
		remoteLinkErrFn = prevErr
		remoteExitCodeFn = prevExitCode
		offerRemoteInstallFn = prevOffer
		exitFn = prevExit
	})

	remoteLinkEstablishedFn = func() bool { return established }
	remoteLinkErrFn = func() error { return errLinkTest }
	remoteExitCodeFn = func() int { return exitCode }

	var offered remoteinstall.Remedy
	offerRemoteInstallFn = func(_ string, r remoteinstall.Remedy) bool {
		offered = r
		return false // decline, so the gate falls through to its report
	}
	exitFn = func(int) {}
	return &offered
}

// Exit 127 from the remote shell means quil is missing over there, which is
// actionable in a way "unreachable host" is not.
func TestGateVersionCheck_OffersInstallOnCommandNotFound(t *testing.T) {
	withRemote(t, "gpu01")
	offered := remoteGateSeams(t, false, 127)

	captureStderr(t, func() { gateVersionCheck(deadClient(t)) })

	if *offered != remoteinstall.RemedyInstall {
		t.Errorf("remedy = %v, want RemedyInstall", *offered)
	}
}

// Exit 255 is ssh's own failure — auth, host key, DNS. There is nothing on the
// far side to install, and offering would send the user to fix the wrong thing.
func TestGateVersionCheck_DoesNotOfferInstallOnSSHFailure(t *testing.T) {
	withRemote(t, "gpu01")
	offered := remoteGateSeams(t, false, 255)

	captureStderr(t, func() { gateVersionCheck(deadClient(t)) })

	if *offered != remoteinstall.RemedyNone {
		t.Errorf("remedy = %v, want RemedyNone for an ssh-level failure", *offered)
	}
}

// The exit code must be read AFTER Close, which is what reaps the ssh child.
// Reading it before races a child that is still exiting and reports "still
// running" for one that already failed — the mirror image of LinkErr, which
// Close can clear and so must be read first.
func TestGateVersionCheck_ReadsExitCodeAfterClose(t *testing.T) {
	withRemote(t, "gpu01")

	prevEstablished := remoteLinkEstablishedFn
	prevErr := remoteLinkErrFn
	prevExitCode := remoteExitCodeFn
	prevOffer := offerRemoteInstallFn
	prevExit := exitFn
	t.Cleanup(func() {
		remoteLinkEstablishedFn = prevEstablished
		remoteLinkErrFn = prevErr
		remoteExitCodeFn = prevExitCode
		offerRemoteInstallFn = prevOffer
		exitFn = prevExit
	})

	var order []string
	remoteLinkEstablishedFn = func() bool { return false }
	remoteLinkErrFn = func() error { order = append(order, "linkerr"); return errLinkTest }
	remoteExitCodeFn = func() int { order = append(order, "exitcode"); return 127 }
	offerRemoteInstallFn = func(string, remoteinstall.Remedy) bool { return false }
	exitFn = func(int) {}

	client := deadClient(t)
	// The client's Close is not observable from here, so the ordering is pinned
	// on the two seams that bracket it: LinkErr before, ExitCode after.
	captureStderr(t, func() { gateVersionCheck(client) })

	if len(order) != 2 || order[0] != "linkerr" || order[1] != "exitcode" {
		t.Errorf("seam order = %v, want [linkerr exitcode]", order)
	}
}

// TestGateVersionCheck_LocalSession_SkipsTheLinkGuard is the control: the guard
// must be conditional on remote mode, or every local session would exit.
func TestGateVersionCheck_LocalSession_SkipsTheLinkGuard(t *testing.T) {
	prevDest := remoteDest
	prevEstablished := remoteLinkEstablishedFn
	prevExit := exitFn
	t.Cleanup(func() {
		remoteDest = prevDest
		remoteLinkEstablishedFn = prevEstablished
		exitFn = prevExit
	})

	remoteDest = "" // local session
	remoteLinkEstablishedFn = func() bool { return false }

	exitCalled := false
	exitFn = func(int) { exitCalled = true }

	captureStderr(t, func() { gateVersionCheck(deadClient(t)) })

	if exitCalled {
		t.Error("link guard fired in a local session, want it skipped")
	}
}
