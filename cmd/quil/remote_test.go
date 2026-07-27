package main

import (
	"errors"
	"testing"
)

// withRemote sets remote mode for one test and restores it afterwards.
func withRemote(t *testing.T, dest string) {
	t.Helper()
	prev := remoteDest
	remoteDest = dest
	t.Cleanup(func() { remoteDest = prev })
}

func TestRemoteMode_ReflectsDest(t *testing.T) {
	if remoteMode() {
		t.Fatal("remoteMode() is true by default, want false")
	}
	withRemote(t, "gpu01")
	if !remoteMode() {
		t.Error("remoteMode() = false after setting remoteDest, want true")
	}
}

// TestStopDaemonEscalating_RemoteMode_Refuses is a regression canary.
//
// Without it: over a deadline-less transport the version handshake returns
// DaemonUnknown, gateVersionCheck falls to its default branch and calls
// restartDaemonForUpgrade, which reads config.SocketPath()/config.PidPath() —
// the LAPTOP's — and SIGKILLs the user's local production daemon while the
// remote one sits untouched. Do not delete this test.
func TestStopDaemonEscalating_RemoteMode_Refuses(t *testing.T) {
	withRemote(t, "gpu01")

	wasRunning, err := stopDaemonEscalating(false)
	if !errors.Is(err, errRemoteMode) {
		t.Fatalf("err = %v, want errRemoteMode", err)
	}
	if wasRunning {
		t.Error("wasRunning = true, want false — nothing local was touched")
	}
}

func TestRestartDaemonForUpgrade_RemoteMode_Refuses(t *testing.T) {
	withRemote(t, "gpu01")

	client, err := restartDaemonForUpgrade()
	if !errors.Is(err, errRemoteMode) {
		t.Fatalf("err = %v, want errRemoteMode", err)
	}
	if client != nil {
		t.Error("client is non-nil, want nil")
	}
}

func TestStartDaemon_RemoteMode_Exits(t *testing.T) {
	withRemote(t, "gpu01")

	var code int
	called := false
	prev := exitFn
	exitFn = func(c int) { called = true; code = c; panic(errTestExit) }
	t.Cleanup(func() { exitFn = prev })

	defer func() {
		if r := recover(); r != errTestExit {
			t.Fatalf("recover() = %v, want errTestExit — startDaemon did not exit", r)
		}
		if !called {
			t.Error("exitFn was not called")
		}
		if code == 0 {
			t.Errorf("exit code = 0, want non-zero")
		}
	}()

	startDaemon(true)
	t.Fatal("startDaemon returned in remote mode, want exit")
}

var errTestExit = errors.New("test exit")
