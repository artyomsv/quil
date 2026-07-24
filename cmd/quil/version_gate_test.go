package main

import (
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

// stubStopSpawn swaps both side effects of restartDaemonForUpgrade for the
// duration of a test and restores them afterwards.
func stubStopSpawn(t *testing.T, stop func(bool) (bool, error), spawn func() (int, error)) {
	t.Helper()
	origStop, origSpawn := stopDaemonForUpgradeFn, spawnDaemonForUpgradeFn
	stopDaemonForUpgradeFn, spawnDaemonForUpgradeFn = stop, spawn
	t.Cleanup(func() {
		stopDaemonForUpgradeFn, spawnDaemonForUpgradeFn = origStop, origSpawn
	})
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

	client, err := restartDaemonForUpgrade(filepath.Join(t.TempDir(), "quild.sock"))
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

	_, err := restartDaemonForUpgrade(filepath.Join(t.TempDir(), "quild.sock"))
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
	sock := filepath.Join(t.TempDir(), "quild.sock")
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

	client, err := restartDaemonForUpgrade(sock)
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

	if _, err := restartDaemonForUpgrade(filepath.Join(t.TempDir(), "quild.sock")); err == nil {
		t.Fatal("spawn failure = nil error, want it reported")
	}
}
