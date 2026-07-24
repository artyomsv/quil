//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSwapOne_BackupPinnedByOpenHandle is the regression test for the
// production failure: an orphaned quild survived an earlier update, which had
// renamed its binary to "<target>.old" underneath it, so NT keeps that file
// alive as the process image. Windows refuses DELETE on such a file, so
// os.Remove(backup) AND the rename-replace that follows both fail with
// "Access is denied" — and because the pinned backup never goes away, EVERY
// subsequent update failed the same way.
//
// An ordinary os.Open reproduces the lock: Go opens files on Windows with
// FILE_SHARE_READ|FILE_SHARE_WRITE and deliberately not FILE_SHARE_DELETE, so
// the handle denies DELETE — which is all freeBackupPath needs to observe.
// The proxy is strictly stricter than production, not identical: a mapped
// image denies DELETE but PERMITS rename (that is the whole premise of
// swapOne renaming the running binary aside), whereas an open handle without
// FILE_SHARE_DELETE denies both. The gap does not weaken the test, because
// nothing ever renames INTO an occupied slot.
func TestSwapOne_BackupPinnedByOpenHandle(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "quild.exe")
	staged := filepath.Join(dir, "staged-quild.exe")
	backup := target + ".old"
	os.WriteFile(target, []byte("old-binary"), 0755)
	os.WriteFile(staged, []byte("new-binary"), 0755)
	os.WriteFile(backup, []byte("ancient-binary"), 0755)

	pin, err := os.Open(backup)
	if err != nil {
		t.Fatalf("open backup to pin it: %v", err)
	}
	defer pin.Close()

	// Guard the premise: if a future Go release starts passing
	// FILE_SHARE_DELETE, this test would silently stop reproducing the bug.
	if err := os.Remove(backup); err == nil {
		t.Skip("open handle no longer blocks delete on this platform — premise gone")
	}

	backupPath, err := swapOne(target, staged)
	if err != nil {
		t.Fatalf("swapOne with a pinned backup = %v, want a fallback backup path", err)
	}
	if backupPath != target+".old.1" {
		t.Errorf("backup path = %q, want %q", backupPath, target+".old.1")
	}
	if got, _ := os.ReadFile(target); string(got) != "new-binary" {
		t.Errorf("target content = %q, want new-binary", got)
	}
	if got, _ := os.ReadFile(backupPath); string(got) != "old-binary" {
		t.Errorf("fallback backup content = %q, want old-binary", got)
	}
	if got, _ := os.ReadFile(backup); string(got) != "ancient-binary" {
		t.Errorf("pinned backup content = %q, want it left untouched", got)
	}
}
