package main

import (
	"os"
	"path/filepath"
	"testing"
)

// pinBackup makes "<target>.old" impossible to remove on EVERY platform, so
// the fallback path is exercised on Linux CI too. A non-empty directory is
// the portable stand-in for what Windows actually does in production: a
// backup file still mapped as a surviving process's executable image, which
// NT refuses to DELETE (see the //go:build windows sibling test for the real
// thing).
func pinBackup(t *testing.T, target string) string {
	t.Helper()
	backup := target + ".old"
	if err := os.Mkdir(backup, 0755); err != nil {
		t.Fatalf("mkdir pinned backup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(backup, "keep"), []byte("x"), 0644); err != nil {
		t.Fatalf("populate pinned backup: %v", err)
	}
	return backup
}

func TestSwapOne_ReplacesAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "quil.exe")
	staged := filepath.Join(dir, "staged-quil.exe")
	os.WriteFile(target, []byte("old-binary"), 0755)
	os.WriteFile(staged, []byte("new-binary"), 0755)

	backupPath, err := swapOne(target, staged)
	if err != nil {
		t.Fatalf("swapOne: %v", err)
	}
	if backupPath != target+".old" {
		t.Errorf("backup path = %q, want the canonical %q", backupPath, target+".old")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new-binary" {
		t.Errorf("target content = %q, want new-binary", got)
	}
	backup, _ := os.ReadFile(target + ".old")
	if string(backup) != "old-binary" {
		t.Errorf("backup content = %q, want old-binary", backup)
	}
}

func TestFreeBackupPath_StaleButDeletable_ReusesCanonical(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "quil.exe")
	os.WriteFile(target, []byte("current"), 0755)
	os.WriteFile(target+".old", []byte("stale"), 0755)

	got, err := freeBackupPath(target)
	if err != nil {
		t.Fatalf("freeBackupPath: %v", err)
	}
	if got != target+".old" {
		t.Errorf("path = %q, want canonical %q reused", got, target+".old")
	}
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Errorf("canonical backup should have been cleared, stat err = %v", err)
	}
}

func TestFreeBackupPath_PinnedCanonical_FallsBackToSuffix(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "quild.exe")
	os.WriteFile(target, []byte("current"), 0755)
	pinBackup(t, target)

	got, err := freeBackupPath(target)
	if err != nil {
		t.Fatalf("freeBackupPath with pinned backup: %v", err)
	}
	if got != target+".old.1" {
		t.Errorf("path = %q, want fallback %q", got, target+".old.1")
	}
	if _, err := os.Stat(target + ".old"); err != nil {
		t.Errorf("pinned backup must be left untouched, stat err = %v", err)
	}
}

func TestSwapOne_PinnedBackup_StillSwaps(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "quild.exe")
	staged := filepath.Join(dir, "staged-quild.exe")
	os.WriteFile(target, []byte("old-binary"), 0755)
	os.WriteFile(staged, []byte("new-binary"), 0755)
	pinBackup(t, target)

	backupPath, err := swapOne(target, staged)
	if err != nil {
		t.Fatalf("swapOne with pinned backup = %v, want the swap to proceed via a fallback path", err)
	}
	if backupPath != target+".old.1" {
		t.Errorf("backup path = %q, want %q", backupPath, target+".old.1")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new-binary" {
		t.Errorf("target content = %q, want new-binary", got)
	}
	backup, _ := os.ReadFile(backupPath)
	if string(backup) != "old-binary" {
		t.Errorf("fallback backup content = %q, want old-binary", backup)
	}
}

func TestSwapOne_PinnedBackup_MissingStaged_RollsBackFromFallback(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "quild.exe")
	os.WriteFile(target, []byte("old-binary"), 0755)
	pinBackup(t, target)

	if _, err := swapOne(target, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("swapOne with missing staged = nil error, want error")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "old-binary" {
		t.Errorf("target after rollback = %q (err %v), want old-binary restored from the fallback backup", got, err)
	}
	if _, err := os.Stat(target + ".old.1"); !os.IsNotExist(err) {
		t.Errorf("fallback backup should be gone after rollback, stat err = %v", err)
	}
}

func TestRemoveBackups_RemovesCanonicalAndSuffixed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "quil.exe")
	os.WriteFile(target, []byte("current"), 0755)
	os.WriteFile(target+".old", []byte("v1"), 0755)
	os.WriteFile(target+".old.1", []byte("v2"), 0755)
	os.WriteFile(target+".old.3", []byte("v3"), 0755) // gap: .old.2 never existed

	removeBackups(target)

	for _, p := range []string{target + ".old", target + ".old.1", target + ".old.3"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed, stat err = %v", filepath.Base(p), err)
		}
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("the live binary must survive cleanup, stat err = %v", err)
	}
}

func TestSwapOne_MissingStaged_RollsBack(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "quil.exe")
	os.WriteFile(target, []byte("old-binary"), 0755)

	if _, err := swapOne(target, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("swapOne with missing staged = nil error, want error")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "old-binary" {
		t.Errorf("target after rollback = %q (err %v), want old-binary restored", got, err)
	}
}

func TestSwapBinaries_SecondSwapFails_RollsBackFirst(t *testing.T) {
	installDir := t.TempDir()
	stagedDir := t.TempDir()

	quilTarget := filepath.Join(installDir, "quil.exe")
	quildTarget := filepath.Join(installDir, "quild.exe")
	os.WriteFile(quilTarget, []byte("old-quil"), 0755)
	os.WriteFile(quildTarget, []byte("old-quild"), 0755)

	// Staged dir has ONLY the quil binary — quild is missing, forcing the
	// second swap to fail and the pair-rollback branch to run.
	os.WriteFile(filepath.Join(stagedDir, "quil.exe"), []byte("new-quil"), 0755)

	if err := swapPair(quilTarget, quildTarget, stagedDir, "windows"); err == nil {
		t.Fatal("swapPair with missing staged quild = nil error, want error")
	}

	gotQuil, err := os.ReadFile(quilTarget)
	if err != nil || string(gotQuil) != "old-quil" {
		t.Errorf("quil target after rollback = %q (err %v), want old-quil restored", gotQuil, err)
	}
	if _, err := os.Stat(quilTarget + ".old"); !os.IsNotExist(err) {
		t.Errorf("quil.old backup should be gone after rollback, stat err = %v", err)
	}
	gotQuild, err := os.ReadFile(quildTarget)
	if err != nil || string(gotQuild) != "old-quild" {
		t.Errorf("quild target = %q (err %v), want old-quild (untouched by its own inner rollback)", gotQuild, err)
	}
	if _, err := os.Stat(quildTarget + ".old"); !os.IsNotExist(err) {
		t.Errorf("quild.old backup should be gone after its own inner rollback, stat err = %v", err)
	}
}

func TestSwapPair_PinnedQuilBackup_SecondSwapFails_RollsBackFromFallback(t *testing.T) {
	installDir := t.TempDir()
	stagedDir := t.TempDir()

	quilTarget := filepath.Join(installDir, "quil.exe")
	quildTarget := filepath.Join(installDir, "quild.exe")
	os.WriteFile(quilTarget, []byte("old-quil"), 0755)
	os.WriteFile(quildTarget, []byte("old-quild"), 0755)
	// quil's canonical backup slot is pinned, so its swap lands on
	// "quil.exe.old.1" — the pair rollback must restore from THAT path, not
	// from the hardcoded canonical one.
	pinBackup(t, quilTarget)
	os.WriteFile(filepath.Join(stagedDir, "quil.exe"), []byte("new-quil"), 0755)

	if err := swapPair(quilTarget, quildTarget, stagedDir, "windows"); err == nil {
		t.Fatal("swapPair with missing staged quild = nil error, want error")
	}

	gotQuil, err := os.ReadFile(quilTarget)
	if err != nil || string(gotQuil) != "old-quil" {
		t.Errorf("quil target after rollback = %q (err %v), want old-quil restored", gotQuil, err)
	}
	if _, err := os.Stat(quilTarget + ".old.1"); !os.IsNotExist(err) {
		t.Errorf("fallback backup should be gone after rollback, stat err = %v", err)
	}
}

func TestUpdateRestartPreapproved(t *testing.T) {
	t.Setenv("QUIL_UPDATE_RESTART", "")
	if updateRestartPreapproved() {
		t.Error("preapproved with empty env, want false")
	}
	t.Setenv("QUIL_UPDATE_RESTART", "1")
	if !updateRestartPreapproved() {
		t.Error("not preapproved with env=1, want true")
	}
}
