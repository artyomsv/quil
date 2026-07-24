package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestFreeBackupPath_AllSlotsPinned_Errors: the exhaustion branch. Its message
// is the one hand-written string on this path that reaches the user, and the
// swap must refuse rather than pick a slot it cannot actually use.
func TestFreeBackupPath_AllSlotsPinned_Errors(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "quild.exe")
	os.WriteFile(target, []byte("current"), 0755)

	pinBackup(t, target) // the canonical slot
	for i := 1; i <= maxBackupSlots; i++ {
		slot := fmt.Sprintf("%s.old.%d", target, i)
		if err := os.Mkdir(slot, 0755); err != nil {
			t.Fatalf("pin slot %d: %v", i, err)
		}
		if err := os.WriteFile(filepath.Join(slot, "keep"), []byte("x"), 0644); err != nil {
			t.Fatalf("populate slot %d: %v", i, err)
		}
	}

	got, err := freeBackupPath(target)
	if err == nil {
		t.Fatalf("freeBackupPath with every slot pinned = %q, want an error", got)
	}
	if !strings.Contains(err.Error(), target) {
		t.Errorf("error = %q, want it to name the target", err)
	}

	// And the swap must refuse rather than proceed.
	staged := filepath.Join(dir, "staged")
	os.WriteFile(staged, []byte("new"), 0755)
	if _, err := swapOne(target, staged); err == nil {
		t.Error("swapOne with no usable backup slot = nil error, want refusal")
	}
	if got, _ := os.ReadFile(target); string(got) != "current" {
		t.Errorf("target = %q, want the live binary untouched when no slot is free", got)
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
	// The last slot freeBackupPath can hand out — pins the loop bound against
	// an off-by-one that would orphan it forever.
	os.WriteFile(fmt.Sprintf("%s.old.%d", target, maxBackupSlots), []byte("v4"), 0755)

	removeBackups(target)

	for _, p := range []string{target + ".old", target + ".old.1", target + ".old.3",
		fmt.Sprintf("%s.old.%d", target, maxBackupSlots)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed, stat err = %v", filepath.Base(p), err)
		}
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("the live binary must survive cleanup, stat err = %v", err)
	}
}

// TestAcquireApplyLock_SecondCallerRefused: the point of the lock. Two quil
// processes swapping at once can have one delete the other's in-flight backup,
// leaving no binary at the target if the victim's copy then fails.
func TestAcquireApplyLock_SecondCallerRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "update")

	release, ok := acquireApplyLock(dir)
	if !ok {
		t.Fatal("first acquire failed, want the lock taken")
	}
	if _, ok := acquireApplyLock(dir); ok {
		t.Error("second acquire succeeded while the lock was held")
	}
	if !applyInProgress(dir) {
		t.Error("applyInProgress = false while the lock was held")
	}

	release()

	if applyInProgress(dir) {
		t.Error("applyInProgress = true after release")
	}
	if _, ok := acquireApplyLock(dir); !ok {
		t.Error("acquire after release failed, want the lock reusable")
	}
}

// TestAcquireApplyLock_ReleaseOnlyRemovesOwnLock: release() must not delete a
// lock it no longer owns. If P1's swap outran the staleness window, P2 can take
// the lock over — and P1 blindly removing the path on the way out would hand
// P3 a lock while P2 is mid-swap, which is the exact interleaving the lock
// exists to prevent.
func TestAcquireApplyLock_ReleaseOnlyRemovesOwnLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "update")

	release, ok := acquireApplyLock(dir)
	if !ok {
		t.Fatal("acquire failed")
	}
	// Another process takes it over.
	lock := filepath.Join(dir, applyLockName)
	if err := os.WriteFile(lock, []byte("424242\n"), 0600); err != nil {
		t.Fatalf("simulate takeover: %v", err)
	}

	release()

	data, err := os.ReadFile(lock)
	if err != nil {
		t.Fatalf("release deleted a lock it did not own: %v", err)
	}
	if strings.TrimSpace(string(data)) != "424242" {
		t.Errorf("lock content = %q, want the new holder's pid intact", data)
	}
}

// TestAcquireApplyLock_DeadHolderReclaimedImmediately: a crashed applier's lock
// is reclaimable at once, without waiting out the age window — the recorded pid
// makes that a fact rather than a guess.
func TestAcquireApplyLock_DeadHolderReclaimedImmediately(t *testing.T) {
	const deadPID = 0x7fffffff
	if alive, _ := processProbe(deadPID); alive {
		t.Skipf("pid %d unexpectedly alive; cannot exercise the dead-holder path", deadPID)
	}
	dir := filepath.Join(t.TempDir(), "update")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Freshly written, so only the liveness check can free it.
	if err := os.WriteFile(filepath.Join(dir, applyLockName),
		[]byte(fmt.Sprintf("%d\n", deadPID)), 0600); err != nil {
		t.Fatalf("plant lock: %v", err)
	}

	if applyInProgress(dir) {
		t.Error("applyInProgress = true for a dead holder's fresh lock")
	}
	release, ok := acquireApplyLock(dir)
	if !ok {
		t.Fatal("could not reclaim a dead holder's lock")
	}
	release()
}

// TestApplyInProgress_UnstattableLockFailsClosed: an unreadable lock must
// suppress the sweep, not enable it. A lock we cannot stat is not proof that
// no swap is running, and the cost of skipping a sweep is a few stale files.
func TestApplyInProgress_UnstattableLockFailsClosed(t *testing.T) {
	// A regular file where the update dir should be: stat of "<file>/apply.lock"
	// fails with something other than NotExist on every platform.
	notADir := filepath.Join(t.TempDir(), "update")
	if err := os.WriteFile(notADir, []byte("x"), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Windows reports "a path component is not a directory" as
	// ERROR_PATH_NOT_FOUND, which Go classifies as fs.ErrNotExist — so this
	// construct cannot produce a non-NotExist stat error there. The branch
	// still matters on Windows (a permission failure reaches it); it just is
	// not reproducible portably, so assert only where the premise holds.
	if _, err := os.Stat(filepath.Join(notADir, applyLockName)); errors.Is(err, fs.ErrNotExist) {
		t.Skip("stat reports NotExist for a non-directory parent on this platform")
	}
	if !applyInProgress(notADir) {
		t.Error("applyInProgress = false for an unstattable lock path, want fail-closed")
	}
}

// TestAcquireApplyLock_StaleLockTakenOver: a lock too old to belong to a live
// swap is claimable — the backstop for a pid since recycled by an unrelated
// process, which liveness alone would read as "held" forever.
func TestAcquireApplyLock_StaleLockTakenOver(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "update")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	lock := filepath.Join(dir, applyLockName)
	if err := os.WriteFile(lock, []byte("999999\n"), 0600); err != nil {
		t.Fatalf("plant lock: %v", err)
	}
	stale := time.Now().Add(-applyLockStale - time.Minute)
	if err := os.Chtimes(lock, stale, stale); err != nil {
		t.Fatalf("age lock: %v", err)
	}

	if applyInProgress(dir) {
		t.Error("applyInProgress = true for a stale lock, want it ignored")
	}
	release, ok := acquireApplyLock(dir)
	if !ok {
		t.Fatal("could not take over a stale lock — a crashed apply would wedge updates forever")
	}
	release()
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
