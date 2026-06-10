//go:build integration

package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// TestStart_SecondDaemonRefused: a second daemon against the same QUIL_HOME
// must refuse to start while the first is serving, instead of unlinking the
// live socket and clobbering the PID file (the production-bricking incident
// of 2026-06-10).
func TestStart_SecondDaemonRefused(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	// config reads QUIL_HOME from env — Setenv must come first.
	d1 := New(config.Default())
	if err := d1.Start(); err != nil {
		t.Fatalf("first daemon Start: %v", err)
	}
	defer d1.Stop()

	d2 := New(config.Default())
	if err := d2.Start(); err == nil {
		d2.Stop()
		t.Fatal("second daemon started against a live socket — expected refusal")
	}
}

// TestStart_StaleSocketStillCleaned: a leftover socket file with no listener
// behind it must NOT block startup — the existing stale-cleanup behavior is
// preserved.
func TestStart_StaleSocketStillCleaned(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)
	// Plain file at the socket path = stale socket (nothing accepts on it).
	if err := os.WriteFile(filepath.Join(tmp, "quild.sock"), nil, 0600); err != nil {
		t.Fatal(err)
	}

	// config reads QUIL_HOME from env — Setenv must come first.
	d := New(config.Default())
	if err := d.Start(); err != nil {
		t.Fatalf("Start with stale socket file: %v", err)
	}
	d.Stop()
}
