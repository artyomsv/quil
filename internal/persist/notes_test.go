package persist

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestNotesFileName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		paneID  string
		want    string
		wantErr bool
	}{
		{"simple", "pane-abc123", "pane-abc123.md", false},
		{"empty rejected", "", "", true},
		{"dot rejected", ".", "", true},
		{"double dot rejected", "..", "", true},
		{"forward slash rejected", "pane/../etc", "", true},
		{"backslash rejected", `pane\evil`, "", true},
		{"path stripped by base", "subdir/pane-xyz", "", true}, // contains /
		{"null byte rejected", "pane\x00bad", "", true},
		{"control char rejected", "pane\x01x", "", true},
		{"angle bracket rejected", "pane<x", "", true},
		{"colon rejected", "pane:x", "", true},
		{"pipe rejected", "pane|x", "", true},
		{"asterisk rejected", "pane*x", "", true},
		{"win reserved CON", "CON", "", true},
		{"win reserved con lowercase", "con", "", true},
		{"win reserved NUL mixed case", "NuL", "", true},
		{"win reserved COM1", "COM1", "", true},
		{"win reserved LPT9", "LPT9", "", true},
		{"unicode pane id", "pane-héllo", "pane-héllo.md", false},
		{"hidden-file prefix accepted", ".pane-x", ".pane-x.md", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := NotesFileName(tt.paneID)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSaveLoadNotes_Roundtrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	paneID := "pane-a1b2c3d4"
	content := "# Meeting notes\n\nRemember to test the edge cases.\n"

	if err := SaveNotes(dir, paneID, content); err != nil {
		t.Fatalf("SaveNotes: %v", err)
	}

	got, err := LoadNotes(dir, paneID)
	if err != nil {
		t.Fatalf("LoadNotes: %v", err)
	}
	if got != content {
		t.Errorf("got %q, want %q", got, content)
	}
}

func TestLoadNotes_Missing_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	got, err := LoadNotes(dir, "pane-nothing")
	if err != nil {
		t.Fatalf("LoadNotes: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestSaveNotes_CreatesDir(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	dir := filepath.Join(parent, "notes")
	if err := SaveNotes(dir, "pane-new", "hello"); err != nil {
		t.Fatalf("SaveNotes: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("expected notes dir created: %v", err)
	}
}

func TestSaveNotes_AtomicTempCleanedUp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	paneID := "pane-clean"
	if err := SaveNotes(dir, paneID, "first"); err != nil {
		t.Fatalf("SaveNotes: %v", err)
	}
	if err := SaveNotes(dir, paneID, "second"); err != nil {
		t.Fatalf("SaveNotes second: %v", err)
	}
	// No stray .tmp file should remain after a successful save.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("found stray temp file: %s", e.Name())
		}
	}
}

func TestSaveNotes_Overwrites(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	paneID := "pane-overwrite"
	if err := SaveNotes(dir, paneID, "first"); err != nil {
		t.Fatalf("SaveNotes: %v", err)
	}
	if err := SaveNotes(dir, paneID, "second"); err != nil {
		t.Fatalf("SaveNotes second: %v", err)
	}
	got, err := LoadNotes(dir, paneID)
	if err != nil {
		t.Fatalf("LoadNotes: %v", err)
	}
	if got != "second" {
		t.Errorf("got %q, want %q", got, "second")
	}
}

func TestDeleteNotes_Missing_NoError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := DeleteNotes(dir, "pane-gone"); err != nil {
		t.Errorf("DeleteNotes on missing file should not error, got: %v", err)
	}
}

func TestDeleteNotes_Existing_Removes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	paneID := "pane-byebye"
	if err := SaveNotes(dir, paneID, "content"); err != nil {
		t.Fatalf("SaveNotes: %v", err)
	}
	if err := DeleteNotes(dir, paneID); err != nil {
		t.Fatalf("DeleteNotes: %v", err)
	}
	path, _ := NotesPath(dir, paneID)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected file removed, got: %v", err)
	}
}

func TestSaveNotes_RejectsBadPaneID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := SaveNotes(dir, "../etc/passwd", "oops"); err == nil {
		t.Error("expected error for path traversal pane ID")
	}
}

func TestSaveNotes_RejectsWindowsReservedName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"CON", "PRN", "NUL", "com1", "LPT9"} {
		if err := SaveNotes(dir, name, "oops"); err == nil {
			t.Errorf("SaveNotes(%q) should fail (Windows reserved name)", name)
		}
	}
}

func TestSaveNotes_ConcurrentSafe(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	paneID := "pane-concurrent"

	const writers = 8
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			if err := SaveNotes(dir, paneID, "content"); err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	// File should exist with valid content; no .tmp leftovers should remain.
	got, err := LoadNotes(dir, paneID)
	if err != nil {
		t.Fatalf("LoadNotes: %v", err)
	}
	if got != "content" {
		t.Errorf("got %q, want %q", got, "content")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("stray temp file after concurrent saves: %s", e.Name())
		}
	}
}

func TestLoadNotes_RefusesSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "real-target.md")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	link, _ := NotesPath(dir, "pane-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	if _, err := LoadNotes(dir, "pane-link"); err == nil {
		t.Error("LoadNotes should refuse to follow a symlinked notes file")
	}
}
