package update

import (
	"os"
	"path/filepath"
	"testing"
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

func TestInstallWritable(t *testing.T) {
	if !InstallWritable(t.TempDir()) {
		t.Error("InstallWritable(TempDir) = false, want true")
	}
	if InstallWritable(filepath.Join(t.TempDir(), "does-not-exist")) {
		t.Error("InstallWritable(nonexistent dir) = true, want false")
	}
}
