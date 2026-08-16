package update

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

func TestState_SaveLoad_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update", "state.json")
	st := State{LastCheckMs: 1234, LatestVersion: "0.0.2", ReleaseURL: "https://example.invalid/r", StagedVersion: "0.0.2", InstallWritable: true}
	if err := SaveState(path, st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got := LoadState(path)
	if got != st {
		t.Errorf("LoadState = %+v, want %+v", got, st)
	}
}

func TestLoadState_MissingOrCorrupt_ReturnsZero(t *testing.T) {
	dir := t.TempDir()
	if got := LoadState(filepath.Join(dir, "nope.json")); got != (State{}) {
		t.Errorf("missing file: LoadState = %+v, want zero", got)
	}
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte("{not json"), 0600)
	if got := LoadState(bad); got != (State{}) {
		t.Errorf("corrupt file: LoadState = %+v, want zero", got)
	}
}

func TestNotifiedVersion_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update", "notified.json")
	if got := LoadNotifiedVersion(path); got != "" {
		t.Errorf("missing file: LoadNotifiedVersion = %q, want empty", got)
	}
	if err := SaveNotifiedVersion(path, "0.0.2"); err != nil {
		t.Fatalf("SaveNotifiedVersion: %v", err)
	}
	if got := LoadNotifiedVersion(path); got != "0.0.2" {
		t.Errorf("LoadNotifiedVersion = %q, want 0.0.2", got)
	}
}

func TestLastRunVersion_RoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update", "lastrun.json")
	if got := LoadLastRunVersion(path); got != "" {
		t.Errorf("missing file returned %q, want empty", got)
	}
	if err := SaveLastRunVersion(path, "1.60.0"); err != nil {
		t.Fatalf("SaveLastRunVersion: %v", err)
	}
	if got := LoadLastRunVersion(path); got != "1.60.0" {
		t.Errorf("LoadLastRunVersion = %q, want 1.60.0", got)
	}
}

func TestLastRunVersion_CorruptFileReadsAsNeverRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lastrun.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := LoadLastRunVersion(path); got != "" {
		t.Errorf("corrupt file returned %q, want empty", got)
	}
}

// The two markers answer different questions — "a version I told you about"
// versus "a version you ran" — so they must not share a file.
func TestLastRunPath_IsNotTheNotifiedPath(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	if config.LastRunPath() == config.UpdateNotifiedPath() {
		t.Error("lastrun and notified markers share a path")
	}
}

func TestInstallWritable(t *testing.T) {
	if !InstallWritable(t.TempDir()) {
		t.Error("InstallWritable(TempDir) = false, want true")
	}
	if InstallWritable(filepath.Join(t.TempDir(), "does-not-exist")) {
		t.Error("InstallWritable(nonexistent dir) = true, want false")
	}
}
