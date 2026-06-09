package logger

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
	arch, err := os.ReadFile(archives[0])
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if string(arch) != strings.Repeat("a", 60) {
		t.Errorf("archive content = %q", arch)
	}
	cur, err := os.ReadFile(filepath.Join(dir, "quild.log"))
	if err != nil {
		t.Fatalf("read active: %v", err)
	}
	if string(cur) != strings.Repeat("b", 60) {
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
	if err := os.WriteFile(filepath.Join(dir, "quil.log"), []byte(strings.Repeat("z", 200)), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
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

// TestRotatingWriter_CollisionSuffix verifies that when a rotation's target
// archive path already exists (same-second timestamp), rotate() appends a
// numeric collision suffix (-1, -2, …) rather than overwriting the existing file.
func TestRotatingWriter_CollisionSuffix(t *testing.T) {
	dir := t.TempDir()

	// Pin the clock so the collision is deterministic.
	fixed := time.Date(2024, 1, 15, 10, 30, 45, 0, time.UTC)
	t.Cleanup(func() { nowFn = time.Now })
	nowFn = func() time.Time { return fixed }

	// Pre-create the archive that would normally be the first rotation target.
	ts := fixed.Format("20060102-150405")
	firstArchive := filepath.Join(dir, fmt.Sprintf("quild-%s.log", ts))
	if err := os.WriteFile(firstArchive, []byte("pre-existing"), 0o600); err != nil {
		t.Fatalf("seed collision file: %v", err)
	}

	w, err := NewRotatingWriter(dir, "quild.log", 50, 10)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer w.Close()

	// Write enough to trigger a rotation.
	if _, err := w.Write([]byte(strings.Repeat("a", 51))); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The collision-suffix archive must exist.
	collisionArchive := filepath.Join(dir, fmt.Sprintf("quild-%s-1.log", ts))
	if _, err := os.Stat(collisionArchive); err != nil {
		t.Fatalf("want collision archive %q to exist: %v", collisionArchive, err)
	}

	// The pre-existing archive must be untouched.
	data, err := os.ReadFile(firstArchive)
	if err != nil {
		t.Fatalf("read pre-existing archive: %v", err)
	}
	if string(data) != "pre-existing" {
		t.Errorf("pre-existing archive was overwritten; got %q", data)
	}
}

// TestRotatingWriter_RenameFailureBacksOff verifies that when os.Rename
// persistently fails (simulating a Windows locked-file scenario), the writer:
//  1. Does not panic.
//  2. Continues to accept writes successfully.
//  3. Does NOT attempt rotation on every write — rename call count must be much
//     less than the write count, proving the per-maxSize backoff is in effect.
func TestRotatingWriter_RenameFailureBacksOff(t *testing.T) {
	dir := t.TempDir()

	var renameCalls atomic.Int64
	t.Cleanup(func() { renameFn = os.Rename })
	renameFn = func(src, dst string) error {
		renameCalls.Add(1)
		return errors.New("rename: access denied (simulated)")
	}

	const maxSize = 100
	w, err := NewRotatingWriter(dir, "quild.log", maxSize, 10)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer w.Close()

	// Write 20× maxSize worth of data in small chunks to cross the threshold many times.
	const writeSize = 11 // slightly over 10% of maxSize — crosses maxSize ~18 times
	const totalWrites = 200
	for i := 0; i < totalWrites; i++ {
		if _, err := w.Write([]byte(strings.Repeat("x", writeSize))); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	calls := renameCalls.Load()

	// With backoff, rename is attempted at most once per maxSize bytes grown.
	// Total data written = totalWrites * writeSize = 2200 bytes.
	// maxSize = 100, so at most ceil(2200/100) = 22 rotation attempts.
	// Without backoff it would be called ~totalWrites times (200).
	// We assert calls << totalWrites to confirm backoff is active.
	if calls >= totalWrites/2 {
		t.Errorf("renameFn called %d times for %d writes — backoff not working (want <%d)", calls, totalWrites, totalWrites/2)
	}
	// Sanity: rename must have been called at least once (otherwise the test is vacuous).
	if calls == 0 {
		t.Errorf("renameFn was never called — maxSize threshold may not have been crossed")
	}
	t.Logf("renameFn called %d times for %d writes (backoff confirmed)", calls, totalWrites)
}
