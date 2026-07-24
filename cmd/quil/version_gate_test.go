package main

import (
	"errors"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// stubStopSpawn swaps both side effects of restartDaemonForUpgrade for the
// duration of a test and restores them afterwards.
//
// These are package-level vars, so every test using this helper MUST stay
// sequential — adding t.Parallel() to any of them (or running the package
// with -parallel) would race the swap against a concurrent test's restore.
// The vars are the pattern CONTRIBUTING.md sanctions for test seams; the
// sequential assumption is the price, and it is cheap here because these
// tests are pure bookkeeping with no I/O to overlap.
func stubStopSpawn(t *testing.T, stop func(bool) (bool, error), spawn func() (int, error)) {
	t.Helper()
	origStop, origSpawn := stopDaemonForUpgradeFn, spawnDaemonForUpgradeFn
	stopDaemonForUpgradeFn, spawnDaemonForUpgradeFn = stop, spawn
	t.Cleanup(func() {
		stopDaemonForUpgradeFn, spawnDaemonForUpgradeFn = origStop, origSpawn
	})
}

// quilHome points config.SocketPath() (and every other derived path) at a
// temp dir for the duration of a test, so nothing here can reach the
// developer's live ~/.quil. Returns the socket path the daemon would use.
//
// Deliberately NOT t.TempDir(): that embeds the test's own name in the path,
// and a sockaddr_un holds ~108 bytes. These test names are long enough to
// blow that budget once Windows prepends C:\Users\<user>\AppData\Local\Temp —
// the listen then fails with "bind: invalid argument" on Windows while
// passing on Linux, where the /tmp prefix is short enough to squeak under.
func quilHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "quil")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("QUIL_HOME", dir)
	return config.SocketPath()
}

// TestRestartDaemonForUpgrade_StopFails_NeverSpawns is the regression test for
// the orphan-daemon defect: the upgrade path used to delete the socket and PID
// file of a daemon that had not exited in time and spawn a replacement anyway.
// The old daemon kept running — detached, still owning every pane PTY — with
// its bookkeeping erased, so nothing could find or stop it again, and the new
// daemon restored the same workspace snapshot into a duplicate set of panes.
// Spawning a second daemon while the first may still be alive is exactly what
// must never happen.
func TestRestartDaemonForUpgrade_StopFails_NeverSpawns(t *testing.T) {
	spawned := false
	stubStopSpawn(t,
		func(bool) (bool, error) { return true, errors.New("pid 4242 still alive after SIGKILL") },
		func() (int, error) { spawned = true; return 0, nil },
	)

	quilHome(t)
	client, err := restartDaemonForUpgrade()
	if err == nil {
		t.Fatal("restartDaemonForUpgrade with a failed stop = nil error, want the upgrade aborted")
	}
	if spawned {
		t.Error("spawned a second daemon while the old one may still be alive — this is the orphan defect")
	}
	if client != nil {
		t.Error("returned a client despite aborting")
	}
}

// TestRestartDaemonForUpgrade_StopFails_ErrorNamesTheDaemon: the abort is
// user-visible (gateVersionCheck prints it and exits), so the underlying
// escalation failure has to survive the wrapping.
func TestRestartDaemonForUpgrade_StopFails_ErrorNamesTheDaemon(t *testing.T) {
	stubStopSpawn(t,
		func(bool) (bool, error) { return true, errors.New("pid 4242 still alive after SIGKILL") },
		func() (int, error) { t.Fatal("must not spawn"); return 0, nil },
	)

	quilHome(t)
	_, err := restartDaemonForUpgrade()
	if err == nil {
		t.Fatal("want an error")
	}
	if got := err.Error(); !strings.Contains(got, "pid 4242 still alive after SIGKILL") {
		t.Errorf("error = %q, want it to carry the escalation failure", got)
	}
}

// TestRestartDaemonForUpgrade_StopSucceeds_SpawnsAndReconnects covers the happy
// path: once the old daemon is confirmed gone, a fresh one is spawned and the
// caller gets a client connected to it.
func TestRestartDaemonForUpgrade_StopSucceeds_SpawnsAndReconnects(t *testing.T) {
	sock := quilHome(t)
	ln, err := net.Listen("unix", sock) // stands in for the freshly spawned daemon
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	spawned := false
	stubStopSpawn(t,
		func(bool) (bool, error) { return true, nil },
		func() (int, error) { spawned = true; return 4242, nil },
	)

	client, err := restartDaemonForUpgrade()
	if err != nil {
		t.Fatalf("restartDaemonForUpgrade: %v", err)
	}
	defer client.Close()
	if !spawned {
		t.Error("did not spawn a replacement daemon after a successful stop")
	}
	if client == nil {
		t.Error("no client returned for a reachable daemon")
	}
}

// TestRestartDaemonForUpgrade_SpawnFails_Reports: a spawn failure must surface
// rather than fall through to the readiness wait's generic timeout message.
func TestRestartDaemonForUpgrade_SpawnFails_Reports(t *testing.T) {
	stubStopSpawn(t,
		func(bool) (bool, error) { return true, nil },
		func() (int, error) { return 0, errors.New("exec: no such file") },
	)

	quilHome(t)
	_, err := restartDaemonForUpgrade()
	if err == nil {
		t.Fatal("spawn failure = nil error, want it reported")
	}
	// Assert the CAUSE, not merely that something failed: without the early
	// return on spawn error, the code falls through to waitForDaemonReady on
	// a socket nothing will ever open and returns the generic "did not open
	// socket" error after the full 30 s budget — an err != nil check alone
	// passes that too, just slowly.
	if !strings.Contains(err.Error(), "exec: no such file") {
		t.Errorf("error = %q, want it to carry the spawn failure", err)
	}
}
