package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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

// The cache's content traces back to a remote daemon's broadcast, so a load
// must bound both the entry count and each entry's field length rather than
// hand back whatever a malformed or oversized file contains.
func TestLoadRemoteProjects_BoundsCountAndFieldLength(t *testing.T) {
	list := make([]CachedProject, maxCachedProjects+50)
	longName := make([]byte, maxCachedFieldLen+100)
	for i := range longName {
		longName[i] = 'x'
	}
	for i := range list {
		list[i] = CachedProject{ID: "proj-over", Name: string(longName), RootDir: string(longName)}
	}
	path := filepath.Join(t.TempDir(), "oversized.json")
	if err := SaveRemoteProjects(path, list); err != nil {
		t.Fatalf("SaveRemoteProjects: %v", err)
	}

	got := LoadRemoteProjects(path)
	if len(got) != maxCachedProjects {
		t.Fatalf("loaded %d projects, want capped at %d", len(got), maxCachedProjects)
	}
	for i, p := range got {
		if len(p.Name) > maxCachedFieldLen {
			t.Errorf("entry %d: Name length = %d, want <= %d", i, len(p.Name), maxCachedFieldLen)
		}
		if len(p.RootDir) > maxCachedFieldLen {
			t.Errorf("entry %d: RootDir length = %d, want <= %d", i, len(p.RootDir), maxCachedFieldLen)
		}
	}
}

// A file bigger than LoadRemoteProjects will ever need must not be fully
// materialized in memory before the count/length caps get a chance to run —
// that is the whole point of bounding the read with io.LimitReader rather
// than a bare os.ReadFile. A file this large is invalid JSON by construction
// (its close bracket falls outside the read window), so the assertion is
// simply that loading it degrades to nil rather than allocating the whole
// thing.
func TestLoadRemoteProjects_BoundsFileReadItself(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString("["); err != nil {
		t.Fatalf("write: %v", err)
	}
	// One entry well past maxCacheFileBytes, so the limited read truncates
	// mid-content and the JSON never closes.
	entry := `{"id":"proj-x","name":"` + strings.Repeat("x", maxCacheFileBytes) + `"},`
	if _, err := f.WriteString(entry); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := f.WriteString(`{"id":"proj-y","name":"y"}]`); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := LoadRemoteProjects(path); got != nil {
		t.Errorf("got %+v, want nil for a file the read cap truncates mid-content", got)
	}
}

// TestTruncateBytes_BacksOffToARuneBoundary is the property truncateBytes
// exists for. The only other test reaching this function
// (TestLoadRemoteProjects_BoundsCountAndFieldLength) fills its oversized
// field with ASCII 'x', so it passes unchanged even with the rune-boundary
// backoff loop deleted outright — this is the one that actually exercises it.
func TestTruncateBytes_BacksOffToARuneBoundary(t *testing.T) {
	// "ab" (ASCII, 2 bytes) + five 4-byte emoji (20 bytes) = 22 bytes total.
	// max=20 lands two bytes into the FIFTH emoji's 4-byte sequence, forcing
	// the backoff loop to step back twice before it finds a rune boundary — a
	// 2-byte rune ("é") would only exercise a single step.
	s := "ab" + strings.Repeat("\U0001F600", 5)
	const max = 20

	got := truncateBytes(s, max)

	if !utf8.ValidString(got) {
		t.Fatalf("truncateBytes(%q, %d) = %q, not valid UTF-8", s, max, got)
	}
	if !strings.HasPrefix(s, got) {
		t.Fatalf("truncateBytes(%q, %d) = %q, not a prefix of the input", s, max, got)
	}
	// "ab" + 4 complete emoji = 2 + 16 = 18 bytes; the incomplete fifth is cut
	// away entirely rather than left dangling mid-sequence.
	if want := 18; len(got) != want {
		t.Errorf("truncateBytes(%q, %d) = %d bytes, want %d", s, max, len(got), want)
	}
}
