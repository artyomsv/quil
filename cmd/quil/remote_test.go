package main

import (
	"errors"
	"os"
	"path/filepath"
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

// TestStartDaemon_RemoteMode_ExitFnReturns_DoesNotProceed pins the guard's
// structural safety, independent of exitFn's behavior. TestStartDaemon_
// RemoteMode_Exits above proves the guard fires when exitFn panics (what
// os.Exit effectively does — it never returns control to the caller). This
// test proves the guard ALSO holds when exitFn is a double that simply
// records the call and returns, which is a completely reasonable double for
// someone to write. Without the explicit `return` after exitFn(1) in the
// guard, execution would fall through into the spawn path — os.MkdirAll on
// config.QuilDir(), then exec.Command against config.SocketPath() — which in
// remote mode is the LOCAL machine's daemon, not the one the session is
// attached to. Do not delete this test.
func TestStartDaemon_RemoteMode_ExitFnReturns_DoesNotProceed(t *testing.T) {
	withRemote(t, "gpu01")

	// Isolate config.QuilDir() so that even if the guard fails to stop
	// execution, the spawn path's os.MkdirAll/exec.Command land in a
	// throwaway temp dir instead of the developer's real ~/.quil/.
	quilDir := filepath.Join(t.TempDir(), "quilhome")
	t.Setenv("QUIL_HOME", quilDir)

	exitCalls := 0
	prev := exitFn
	exitFn = func(c int) { exitCalls++ } // records and RETURNS, unlike os.Exit
	t.Cleanup(func() { exitFn = prev })

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("startDaemon panicked (%v) — it proceeded past the guard into the spawn path", r)
		}
	}()

	got := startDaemon(true)

	if exitCalls != 1 {
		t.Fatalf("exitFn called %d times, want 1", exitCalls)
	}
	// The spawn path — reached only if the guard fell through — creates
	// quilDir via os.MkdirAll before it ever touches the daemon binary. Its
	// absence is the observable proof that startDaemon returned at the guard
	// instead of continuing into the spawn path.
	if _, err := os.Stat(quilDir); !os.IsNotExist(err) {
		t.Fatalf("quilDir %q was created (stat err = %v) — startDaemon proceeded past the guard", quilDir, err)
	}
	if got == 0 {
		t.Error(`startDaemon returned 0, which asserts "daemon already listening" — false after a remote-mode refusal`)
	}
}
