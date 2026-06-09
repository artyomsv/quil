package logger

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRotatingWriter_RotatesAtMaxSize(t *testing.T) {
	dir := t.TempDir()
	w, err := NewRotatingWriter(dir, "quild.log", 100, 10)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte(strings.Repeat("a", 60))); err != nil {
		t.Fatalf("write1: %v", err)
	}
	// +60 crosses 100 → rotation happens before this write lands.
	if _, err := w.Write([]byte(strings.Repeat("b", 60))); err != nil {
		t.Fatalf("write2: %v", err)
	}

	archives, _ := filepath.Glob(filepath.Join(dir, "quild-*.log"))
	if len(archives) != 1 {
		t.Fatalf("want 1 archive, got %d: %v", len(archives), archives)
	}
	if arch, _ := os.ReadFile(archives[0]); string(arch) != strings.Repeat("a", 60) {
		t.Errorf("archive content = %q", arch)
	}
	if cur, _ := os.ReadFile(filepath.Join(dir, "quild.log")); string(cur) != strings.Repeat("b", 60) {
		t.Errorf("active content = %q", cur)
	}
}

func TestRotatingWriter_PrunesBeyondMaxFiles(t *testing.T) {
	dir := t.TempDir()
	w, err := NewRotatingWriter(dir, "quild.log", 10, 2)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer w.Close()
	for i := 0; i < 5; i++ { // each 11-byte write forces a rotation
		if _, err := w.Write([]byte(strings.Repeat("x", 11))); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	archives, _ := filepath.Glob(filepath.Join(dir, "quild-*.log"))
	if len(archives) > 2 {
		t.Errorf("want <=2 archives after prune, got %d", len(archives))
	}
}

func TestRotatingWriter_RotatesOversizedExistingFileOnOpen(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "quil.log"), []byte(strings.Repeat("z", 200)), 0o600)
	w, err := NewRotatingWriter(dir, "quil.log", 100, 10)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer w.Close()
	archives, _ := filepath.Glob(filepath.Join(dir, "quil-*.log"))
	if len(archives) != 1 {
		t.Fatalf("want 1 archive from oversized-on-open, got %d", len(archives))
	}
}

func TestRotatingWriter_ConcurrentWrite(t *testing.T) {
	dir := t.TempDir()
	w, err := NewRotatingWriter(dir, "quild.log", 1<<20, 10)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer w.Close()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 64; j++ {
				if _, err := w.Write([]byte("concurrent line\n")); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
