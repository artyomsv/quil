package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// The Not-now store: a declined version suppresses itself and everything
// older, a newer one interrupts again, and the answer survives a daemon
// restart because it is a file in QUIL_HOME, not a field on a TUI.

func TestUpdateSkips_SuppressesSameAndOlder(t *testing.T) {
	s := loadUpdateSkips(filepath.Join(t.TempDir(), "plugin-update-skips.json"))
	if s.Suppressed("stripe", "1.55.1") {
		t.Fatal("an empty store suppressed a notice")
	}
	if err := s.Skip("stripe", "1.55.1"); err != nil {
		t.Fatalf("Skip: %v", err)
	}
	for _, v := range []string{"1.55.1", "v1.55.1", "1.55.0", "1.31.1", "0.9.9"} {
		if !s.Suppressed("stripe", v) {
			t.Errorf("Suppressed(stripe, %q) = false after declining 1.55.1", v)
		}
	}
	if s.Suppressed("lazygit", "1.55.1") {
		t.Error("a decline for one plugin suppressed another")
	}
}

func TestUpdateSkips_NewerInterrupts(t *testing.T) {
	s := loadUpdateSkips(filepath.Join(t.TempDir(), "plugin-update-skips.json"))
	if err := s.Skip("stripe", "1.55.1"); err != nil {
		t.Fatalf("Skip: %v", err)
	}
	for _, v := range []string{"1.55.2", "1.56.0", "2.0.0"} {
		if s.Suppressed("stripe", v) {
			t.Errorf("Suppressed(stripe, %q) = true: a newer version must interrupt", v)
		}
	}
	// Declining the newer one moves the mark forward; declining an older one
	// afterwards does not move it back.
	if err := s.Skip("stripe", "1.56.0"); err != nil {
		t.Fatalf("Skip newer: %v", err)
	}
	if err := s.Skip("stripe", "1.40.0"); err != nil {
		t.Fatalf("Skip older: %v", err)
	}
	if !s.Suppressed("stripe", "1.56.0") || !s.Suppressed("stripe", "1.55.9") {
		t.Error("the mark did not stay at 1.56.0")
	}
	if s.Suppressed("stripe", "1.56.1") {
		t.Error("1.56.1 suppressed after declining 1.56.0")
	}
	// Text that is not semver suppresses on an exact match only.
	if err := s.Skip("other", "2026-10-23"); err != nil {
		t.Fatalf("Skip non-semver: %v", err)
	}
	if !s.Suppressed("other", "2026-10-23") || s.Suppressed("other", "2026-10-24") {
		t.Error("non-semver versions must compare by equality")
	}
}

func TestUpdateSkips_SurviveReload(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIL_HOME", home)
	path := updateSkipsPath()
	if filepath.Dir(path) != home {
		t.Fatalf("updateSkipsPath = %q, want a file at the top of QUIL_HOME", path)
	}
	if !ipc.IsQuilHomeEntry(filepath.Base(path)) {
		t.Fatalf("ProtectDir would read %q as foreign", filepath.Base(path))
	}

	s := loadUpdateSkips(path)
	if err := s.Skip("stripe", "1.55.1"); err != nil {
		t.Fatalf("Skip: %v", err)
	}
	if err := s.Skip("k9s", "0.32.5"); err != nil {
		t.Fatalf("Skip: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("store not written: %v", err)
	}
	if leftovers, _ := filepath.Glob(path + ".*.tmp"); len(leftovers) != 0 {
		t.Fatalf("temp files left beside the store: %v", leftovers)
	}

	again := loadUpdateSkips(path)
	if !again.Suppressed("stripe", "1.55.1") || !again.Suppressed("k9s", "0.32.5") {
		t.Fatal("declines did not survive a reload")
	}
	if again.Suppressed("stripe", "1.56.0") {
		t.Fatal("a reloaded store suppressed a newer version")
	}

	// A corrupt file is an empty store, never a refusal.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt store: %v", err)
	}
	if corrupt := loadUpdateSkips(path); corrupt.Suppressed("stripe", "1.55.1") {
		t.Fatal("a corrupt store suppressed a notice")
	}
}

// A write that fails leaves memory as it was: a decline the file does not
// remember would hold the dialog back until the next restart and then let
// it return, with the caller told it was saved.
func TestUpdateSkips_FailedWriteKeepsMemoryAndFileAgreed(t *testing.T) {
	home := t.TempDir()
	blocker := filepath.Join(home, "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	unwritable := filepath.Join(blocker, "plugin-update-skips.json")

	s := loadUpdateSkips(unwritable)
	if err := s.Skip("stripe", "1.55.1"); err == nil {
		t.Fatal("Skip under a file reported success")
	}
	if s.Suppressed("stripe", "1.55.1") {
		t.Fatal("a decline that was never written suppresses in memory")
	}

	// An earlier decline is kept, not the one that failed to land.
	s = loadUpdateSkips(filepath.Join(home, "plugin-update-skips.json"))
	if err := s.Skip("stripe", "1.55.1"); err != nil {
		t.Fatalf("Skip: %v", err)
	}
	s.path = unwritable
	if err := s.Skip("stripe", "1.56.0"); err == nil {
		t.Fatal("Skip under a file reported success")
	}
	if !s.Suppressed("stripe", "1.55.1") || s.Suppressed("stripe", "1.56.0") {
		t.Fatal("a failed write moved the mark in memory")
	}
}

// The daemon's accessor loads from QUIL_HOME once and hands every caller the
// same store, so a Not-now pressed through one handler is seen by the notice
// matcher without a reload.
func TestPluginUpdateSkips_SharedAcrossCallers(t *testing.T) {
	d := newTestDaemon(t)
	if err := d.pluginUpdateSkips().Skip("stripe", "1.55.1"); err != nil {
		t.Fatalf("Skip: %v", err)
	}
	if !d.pluginUpdateSkips().Suppressed("stripe", "1.55.1") {
		t.Fatal("the second call did not see the first call's decline")
	}
	if !loadUpdateSkips(updateSkipsPath()).Suppressed("stripe", "1.55.1") {
		t.Fatal("the daemon's store was not written to updateSkipsPath()")
	}
}
