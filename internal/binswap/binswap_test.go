package binswap

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

	backupPath, err := SwapOne(target, staged)
	if err != nil {
		t.Fatalf("SwapOne: %v", err)
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
	if _, err := SwapOne(target, staged); err == nil {
		t.Error("SwapOne with no usable backup slot = nil error, want refusal")
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

	backupPath, err := SwapOne(target, staged)
	if err != nil {
		t.Fatalf("SwapOne with pinned backup = %v, want the swap to proceed via a fallback path", err)
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

	if _, err := SwapOne(target, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("SwapOne with missing staged = nil error, want error")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "old-binary" {
		t.Errorf("target after rollback = %q (err %v), want old-binary restored from the fallback backup", got, err)
	}
	if _, err := os.Stat(target + ".old.1"); !os.IsNotExist(err) {
		t.Errorf("fallback backup should be gone after rollback, stat err = %v", err)
	}
}

func TestSwapOne_MissingStaged_RollsBack(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "quil.exe")
	os.WriteFile(target, []byte("old-binary"), 0755)

	if _, err := SwapOne(target, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("SwapOne with missing staged = nil error, want error")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "old-binary" {
		t.Errorf("target after rollback = %q (err %v), want old-binary restored", got, err)
	}
}

// TestSwapOne_RollbackOnCopyFailure: the copy fails AFTER the target was
// renamed aside. A directory as the staged "file" produces exactly that shape
// on every platform — os.Open on a directory succeeds, the first Read fails
// (EISDIR on Unix, ERROR_INVALID_FUNCTION on Windows) — so the failure lands
// mid-copy rather than at the open, as a missing staged file would. The
// rename-back must then put the backup at the target path, and the copy's
// ".new" must be gone.
func TestSwapOne_RollbackOnCopyFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "tool")
	staged := filepath.Join(dir, "staged-is-a-dir")
	os.WriteFile(target, []byte("old-binary"), 0755)
	if err := os.Mkdir(staged, 0755); err != nil {
		t.Fatalf("mkdir staged: %v", err)
	}

	backup, err := SwapOne(target, staged)
	if err == nil {
		t.Fatalf("SwapOne with an unreadable staged file = nil error (backup %q), want error", backup)
	}
	if !strings.Contains(err.Error(), "install "+target) {
		t.Errorf("error = %q, want the install step named (the backup step succeeded)", err)
	}
	got, rerr := os.ReadFile(target)
	if rerr != nil || string(got) != "old-binary" {
		t.Errorf("target after rollback = %q (err %v), want old-binary restored", got, rerr)
	}
	if _, err := os.Stat(target + ".old"); !os.IsNotExist(err) {
		t.Errorf("backup should be gone after rollback, stat err = %v", err)
	}
	if _, err := os.Stat(target + ".new"); !os.IsNotExist(err) {
		t.Errorf("the failed copy's .new should be gone, stat err = %v", err)
	}
}

// TestCopyFile_MidCopyFailureLeavesTargetIntact: the truncated-copy case. A
// copy that fails midway (the directory-as-source shape above) must leave an
// existing destination exactly as it was and create nothing at a destination
// that did not exist — the bytes go to "<dst>.new" and are renamed in only
// once they are all there.
func TestCopyFile_MidCopyFailureLeavesTargetIntact(t *testing.T) {
	dir := t.TempDir()
	badSrc := filepath.Join(dir, "src-is-a-dir")
	if err := os.Mkdir(badSrc, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	existing := filepath.Join(dir, "tool")
	os.WriteFile(existing, []byte("old-binary"), 0755)
	if err := CopyFile(badSrc, existing); err == nil {
		t.Fatal("CopyFile from an unreadable source = nil error, want error")
	}
	if got, _ := os.ReadFile(existing); string(got) != "old-binary" {
		t.Errorf("existing destination = %q after a failed copy, want old-binary untouched", got)
	}
	if _, err := os.Stat(existing + ".new"); !os.IsNotExist(err) {
		t.Errorf("the failed copy left %s.new, stat err = %v", filepath.Base(existing), err)
	}

	absent := filepath.Join(dir, "helper")
	if err := CopyFile(badSrc, absent); err == nil {
		t.Fatal("CopyFile to an absent destination = nil error, want error")
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Errorf("a failed first copy created %s, stat err = %v", filepath.Base(absent), err)
	}
	if _, err := os.Stat(absent + ".new"); !os.IsNotExist(err) {
		t.Errorf("the failed copy left %s.new, stat err = %v", filepath.Base(absent), err)
	}

	// A successful copy goes through the same path and lands whole.
	good := filepath.Join(dir, "staged")
	os.WriteFile(good, []byte("new-binary"), 0644)
	if err := CopyFile(good, absent); err != nil {
		t.Fatalf("CopyFile: %v", err)
	}
	if got, _ := os.ReadFile(absent); string(got) != "new-binary" {
		t.Errorf("destination = %q, want new-binary", got)
	}
	if _, err := os.Stat(absent + ".new"); !os.IsNotExist(err) {
		t.Errorf("a successful copy left %s.new behind, stat err = %v", filepath.Base(absent), err)
	}
}

// TestSwapOne_PreservesTargetMode: the installed binary's own permission
// bits survive the swap, whatever the staged file (extracted from an
// archive, possibly without its executable bit) or the umask says. Unix
// only: Windows has no permission bits to preserve.
func TestSwapOne_PreservesTargetMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no permission bits on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "tool")
	staged := filepath.Join(dir, "staged-tool")
	os.WriteFile(target, []byte("old-binary"), 0700)
	os.WriteFile(staged, []byte("new-binary"), 0644)
	if err := os.Chmod(target, 0700); err != nil { // WriteFile's mode is umask-trimmed
		t.Fatalf("chmod: %v", err)
	}

	if _, err := SwapOne(target, staged); err != nil {
		t.Fatalf("SwapOne: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0700 {
		t.Errorf("target mode = %o after the swap, want the original 0700", got)
	}
	if got, _ := os.ReadFile(target); string(got) != "new-binary" {
		t.Errorf("target = %q, want new-binary", got)
	}
}

// TestSwapOne_MissingTargetRestoredFromBackup: a run killed between its
// rename-aside and its copy leaves the target path empty and the binary in
// "<target>.old". The next swap puts it back first and then proceeds, rather
// than refusing on "no such file" beside the only good copy.
func TestSwapOne_MissingTargetRestoredFromBackup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "tool")
	staged := filepath.Join(dir, "staged-tool")
	os.WriteFile(target+".old", []byte("old-binary"), 0755)
	os.WriteFile(staged, []byte("new-binary"), 0755)

	backup, err := SwapOne(target, staged)
	if err != nil {
		t.Fatalf("SwapOne with a missing target and a backup = %v, want the restore-then-swap", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "new-binary" {
		t.Errorf("target = %q, want new-binary", got)
	}
	if got, _ := os.ReadFile(backup); string(got) != "old-binary" {
		t.Errorf("backup %s = %q, want the restored old-binary renamed aside again", backup, got)
	}

	// With no backup either there is nothing to restore: the swap refuses
	// and names the target.
	if _, err := SwapOne(filepath.Join(dir, "nothing"), staged); err == nil || !strings.Contains(err.Error(), "nothing") {
		t.Errorf("SwapOne with neither target nor backup = %v, want a refusal naming the target", err)
	}
}

// TestRestoreMissing_PicksNewestBackup: with several backups the one an
// interrupted swap renamed aside LAST is the live binary; an older slot may
// hold a release behind it, pinned and never swept.
func TestRestoreMissing_PicksNewestBackup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "tool")
	os.WriteFile(target+".old", []byte("older-release"), 0755)
	os.WriteFile(target+".old.2", []byte("newest"), 0755)
	os.WriteFile(target+".old.5", []byte("oldest"), 0755)
	for suffix, age := range map[string]time.Duration{".old": -time.Hour, ".old.2": 0, ".old.5": -2 * time.Hour} {
		when := time.Now().Add(age)
		if err := os.Chtimes(target+suffix, when, when); err != nil {
			t.Fatal(err)
		}
	}

	restored, err := RestoreMissing(target)
	if err != nil || !restored {
		t.Fatalf("RestoreMissing = %v, %v; want restored", restored, err)
	}
	if got, _ := os.ReadFile(target); string(got) != "newest" {
		t.Errorf("restored target = %q, want the newest backup", got)
	}
	if _, err := os.Stat(target + ".old.2"); !os.IsNotExist(err) {
		t.Errorf("the restored slot should be empty, stat err = %v", err)
	}
	for _, kept := range []string{target + ".old", target + ".old.5"} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s should be left alone: %v", filepath.Base(kept), err)
		}
	}
}

// TestCopyFile_RenameRetriedOnce: a rename that fails once (an antivirus
// handle on the fresh .new) is retried and the copy lands; one that keeps
// failing is an error with no .new left behind.
func TestCopyFile_RenameRetriedOnce(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "staged")
	os.WriteFile(src, []byte("new-binary"), 0755)
	orig := renameFn
	t.Cleanup(func() { renameFn = orig })

	calls := 0
	renameFn = func(a, b string) error {
		calls++
		if calls == 1 {
			return errors.New("busy")
		}
		return orig(a, b)
	}
	dst := filepath.Join(dir, "tool")
	if err := CopyFile(src, dst); err != nil {
		t.Fatalf("CopyFile with one failing rename = %v, want the retry to land it", err)
	}
	if calls != 2 {
		t.Errorf("rename called %d times, want 2", calls)
	}
	if got, _ := os.ReadFile(dst); string(got) != "new-binary" {
		t.Errorf("dst = %q, want new-binary", got)
	}

	renameFn = func(string, string) error { return errors.New("always") }
	always := filepath.Join(dir, "tool2")
	if err := CopyFile(src, always); err == nil {
		t.Fatal("CopyFile with a rename that keeps failing = nil error")
	}
	if _, err := os.Stat(always + ".new"); !os.IsNotExist(err) {
		t.Errorf("the failed copy left %s.new, stat err = %v", filepath.Base(always), err)
	}
}

// TestRemoveBackups_KeepsBackupWithoutTarget: the sweep runs only beside a
// present regular target. When the target is missing or something else
// stands at its path, the backup may be the only good copy, and the restore
// above needs it.
func TestRemoveBackups_KeepsBackupWithoutTarget(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "gone")
	os.WriteFile(missing+".old", []byte("only-copy"), 0755)
	os.WriteFile(missing+".old.1", []byte("only-copy-1"), 0755)
	// RestoreMissing restores the NEWEST backup. The two writes above land
	// within one mtime tick on a coarse filesystem and in order on a fine one
	// (CI's ext4 saw ".old.1" newer and restored it), so the age is set
	// explicitly: the canonical slot is the newest here.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(missing+".old.1", old, old); err != nil {
		t.Fatal(err)
	}
	RemoveBackups(missing)
	for _, p := range []string{missing + ".old", missing + ".old.1"} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s swept while the target is missing: %v", filepath.Base(p), err)
		}
	}

	asDir := filepath.Join(dir, "dir")
	if err := os.Mkdir(asDir, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(asDir+".old", []byte("only-copy"), 0755)
	RemoveBackups(asDir)
	if _, err := os.Stat(asDir + ".old"); err != nil {
		t.Errorf("backup swept while a directory stands at the target path: %v", err)
	}

	// RestoreMissing is the repair: it moves the newest backup back and
	// leaves a present target alone.
	if restored, err := RestoreMissing(missing); err != nil || !restored {
		t.Fatalf("RestoreMissing = %v, %v; want restored", restored, err)
	}
	if got, _ := os.ReadFile(missing); string(got) != "only-copy" {
		t.Errorf("restored target = %q, want only-copy from the newest slot", got)
	}
	if restored, err := RestoreMissing(missing); err != nil || restored {
		t.Errorf("RestoreMissing on a present target = %v, %v; want nothing done", restored, err)
	}
	if got, _ := os.ReadFile(missing + ".old.1"); string(got) != "only-copy-1" {
		t.Errorf("the numbered slot = %q, want left alone once a target is present", got)
	}
}

// TestSwapOne_TargetHeldOpenStillSwaps: the production premise. A binary some
// process is still running is renamed aside, not overwritten — the live
// process keeps its old bytes, the path gets the new ones. An os.Open handle
// models that on Unix (rename and unlink never disturb an open inode).
//
// Not on Windows: Go opens files there WITHOUT FILE_SHARE_DELETE, so an open
// handle denies the rename as well — stricter than a mapped image, which
// permits it. The Windows sibling test pins the backup instead of the target
// for the same reason.
func TestSwapOne_TargetHeldOpenStillSwaps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("an os.Open handle denies rename on Windows, unlike a mapped image")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "tool")
	staged := filepath.Join(dir, "staged-tool")
	os.WriteFile(target, []byte("old-binary"), 0755)
	os.WriteFile(staged, []byte("new-binary"), 0755)

	held, err := os.Open(target)
	if err != nil {
		t.Fatalf("open target to hold it: %v", err)
	}
	defer held.Close()

	backup, err := SwapOne(target, staged)
	if err != nil {
		t.Fatalf("SwapOne with the target held open = %v, want the swap to proceed", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "new-binary" {
		t.Errorf("target content = %q, want new-binary", got)
	}
	if got, _ := os.ReadFile(backup); string(got) != "old-binary" {
		t.Errorf("backup %s = %q, want old-binary", backup, got)
	}
	// The held handle still reads the OLD file: the swap moved it, it did
	// not write over it — which is what keeps a running process intact.
	buf := make([]byte, 16)
	n, _ := held.Read(buf)
	if string(buf[:n]) != "old-binary" {
		t.Errorf("held handle reads %q, want old-binary (the swap must not write through the old inode)", buf[:n])
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

	RemoveBackups(target)

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
