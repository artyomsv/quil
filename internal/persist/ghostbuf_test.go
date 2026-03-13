package persist_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/aethel/internal/persist"
)

func TestSaveAndLoadBuffer(t *testing.T) {
	dir := t.TempDir()
	bufDir := filepath.Join(dir, "buffers")
	os.MkdirAll(bufDir, 0700)

	data := []byte("hello terminal output\x1b[32mgreen\x1b[0m")
	if err := persist.SaveBuffer(bufDir, "pane-abc123", data); err != nil {
		t.Fatalf("SaveBuffer: %v", err)
	}

	loaded, err := persist.LoadBuffer(bufDir, "pane-abc123")
	if err != nil {
		t.Fatalf("LoadBuffer: %v", err)
	}
	if string(loaded) != string(data) {
		t.Errorf("loaded = %q, want %q", loaded, data)
	}
}

func TestLoadBufferMissing(t *testing.T) {
	dir := t.TempDir()
	loaded, err := persist.LoadBuffer(dir, "pane-nonexistent")
	if err != nil {
		t.Fatalf("LoadBuffer missing: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil for missing buffer, got %d bytes", len(loaded))
	}
}

func TestSaveAllBuffers(t *testing.T) {
	dir := t.TempDir()
	bufDir := filepath.Join(dir, "buffers")

	buffers := map[string][]byte{
		"pane-001": []byte("output 1"),
		"pane-002": []byte("output 2"),
	}

	if err := persist.SaveAllBuffers(bufDir, buffers); err != nil {
		t.Fatalf("SaveAllBuffers: %v", err)
	}

	for id, expected := range buffers {
		loaded, err := persist.LoadBuffer(bufDir, id)
		if err != nil {
			t.Fatalf("LoadBuffer %s: %v", id, err)
		}
		if string(loaded) != string(expected) {
			t.Errorf("%s: got %q, want %q", id, loaded, expected)
		}
	}
}

func TestCleanBuffers(t *testing.T) {
	dir := t.TempDir()
	bufDir := filepath.Join(dir, "buffers")
	os.MkdirAll(bufDir, 0700)

	// Write 3 buffers
	persist.SaveBuffer(bufDir, "pane-keep1", []byte("data"))
	persist.SaveBuffer(bufDir, "pane-keep2", []byte("data"))
	persist.SaveBuffer(bufDir, "pane-remove", []byte("data"))

	// Clean: only keep1 and keep2 are active
	if err := persist.CleanBuffers(bufDir, []string{"pane-keep1", "pane-keep2"}); err != nil {
		t.Fatalf("CleanBuffers: %v", err)
	}

	// Removed buffer should be gone
	if _, err := os.Stat(filepath.Join(bufDir, "pane-remove.bin")); !os.IsNotExist(err) {
		t.Error("pane-remove.bin should have been cleaned up")
	}

	// Kept buffers should still exist
	for _, id := range []string{"pane-keep1", "pane-keep2"} {
		if _, err := os.Stat(filepath.Join(bufDir, id+".bin")); err != nil {
			t.Errorf("%s.bin should still exist: %v", id, err)
		}
	}
}

func TestSaveBufferSanitizesPathTraversal(t *testing.T) {
	dir := t.TempDir()
	bufDir := filepath.Join(dir, "buffers")
	os.MkdirAll(bufDir, 0700)

	// A malicious pane ID with path traversal should be sanitized
	data := []byte("should stay in bufDir")
	if err := persist.SaveBuffer(bufDir, "../../etc/evil", data); err != nil {
		t.Fatalf("SaveBuffer with traversal: %v", err)
	}

	// File should be written inside bufDir as "evil.bin" (filepath.Base strips traversal)
	if _, err := os.Stat(filepath.Join(bufDir, "evil.bin")); err != nil {
		t.Errorf("expected sanitized file in bufDir: %v", err)
	}

	// No file should exist outside bufDir
	if _, err := os.Stat(filepath.Join(dir, "evil.bin")); !os.IsNotExist(err) {
		t.Error("file should not be written outside bufDir")
	}

	// LoadBuffer should also sanitize
	loaded, err := persist.LoadBuffer(bufDir, "../../etc/evil")
	if err != nil {
		t.Fatalf("LoadBuffer with traversal: %v", err)
	}
	if string(loaded) != string(data) {
		t.Errorf("loaded = %q, want %q", loaded, data)
	}
}

func TestSaveBufferSkipsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := persist.SaveBuffer(dir, "pane-empty", nil); err != nil {
		t.Fatalf("SaveBuffer nil: %v", err)
	}
	if err := persist.SaveBuffer(dir, "pane-empty", []byte{}); err != nil {
		t.Fatalf("SaveBuffer empty: %v", err)
	}
	// No file should be created
	if _, err := os.Stat(filepath.Join(dir, "pane-empty.bin")); !os.IsNotExist(err) {
		t.Error("empty buffer should not create a file")
	}
}
