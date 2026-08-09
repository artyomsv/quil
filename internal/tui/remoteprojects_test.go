package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoteProjects_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote-projects-gpu01.json")
	want := []CachedProject{
		{ID: "proj-1a2b3c4d", Name: "cluster-management", RootDir: "/home/u/work"},
		{ID: "proj-5e6f7a8b", Name: "infra", RootDir: "/home/u/infra"},
	}
	if err := SaveRemoteProjects(path, want); err != nil {
		t.Fatalf("SaveRemoteProjects: %v", err)
	}
	got := LoadRemoteProjects(path)
	if len(got) != len(want) {
		t.Fatalf("loaded %d projects, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("project %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A missing cache is the ordinary first-launch state, not an error.
func TestLoadRemoteProjects_MissingReturnsNil(t *testing.T) {
	if got := LoadRemoteProjects(filepath.Join(t.TempDir(), "absent.json")); got != nil {
		t.Errorf("got %+v, want nil for a missing cache", got)
	}
}

// Same symlink refusal as LoadRecentCWDs and persist/notes.go.
func TestLoadRemoteProjects_RefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	if err := os.WriteFile(real, []byte(`[{"id":"proj-1a2b3c4d","name":"x"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable on this platform/account")
	}
	if got := LoadRemoteProjects(link); got != nil {
		t.Errorf("got %+v, want nil — a symlinked cache is refused", got)
	}
}

// Corrupt JSON degrades to "no cache", which yields label-named rows rather
// than no rows at all.
func TestLoadRemoteProjects_CorruptReturnsNil(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := LoadRemoteProjects(path); got != nil {
		t.Errorf("got %+v, want nil for corrupt JSON", got)
	}
}
