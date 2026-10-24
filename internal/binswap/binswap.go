// Package binswap replaces an executable on disk with the rename-aside swap
// Quil's own auto-update uses: the live file is renamed to a backup slot
// (legal even while a process runs it — NT locks the image by open handle,
// not by path), the new file is copied into place, and a failed copy renames
// the backup back. Shared by `quil`'s self-update (cmd/quil/update_apply.go)
// and `quild plugin-update`, so the two cannot drift. Stdlib plus
// golang.org/x/sys/windows, for the OS file lock and the process probe.
package binswap

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"
)

// maxBackupSlots bounds the ".old.N" search in freeBackupPath. Reaching it
// means N surviving processes each pin a distinct backup — a real problem
// worth reporting rather than probing forever.
const maxBackupSlots = 20

// defaultMode is what a first install (no target to inherit from) gets.
const defaultMode fs.FileMode = 0755

// backupPaths is every slot SwapOne may put a backup in, canonical first.
func backupPaths(target string) []string {
	paths := []string{target + ".old"}
	for i := 1; i <= maxBackupSlots; i++ {
		paths = append(paths, fmt.Sprintf("%s.old.%d", target, i))
	}
	return paths
}

// freeBackupPath returns a path that can receive target's backup, clearing a
// stale backup left by an earlier update when it can. The canonical
// "<target>.old" is preferred and recycled whenever it is deletable.
//
// When it is NOT deletable, fall back to "<target>.old.1", ".2", … On Windows
// a backup is unremovable whenever some process still runs it as its image:
// an earlier update renamed a live quild aside, and that daemon kept running
// from the renamed file. NT refuses DELETE on a mapped image, which fails
// os.Remove AND the rename-replace that follows (MOVEFILE_REPLACE_EXISTING
// must delete the destination first) — both with "Access is denied". Without
// a fallback, one such leftover wedges EVERY future update permanently, since
// nothing ever clears it. Third-party handles (antivirus, indexers) pin files
// the same way, so this is not specific to orphaned daemons.
func freeBackupPath(target string) (string, error) {
	for _, path := range backupPaths(target) {
		os.Remove(path) // best-effort: a pinned backup survives this
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			return path, nil
		}
	}
	return "", fmt.Errorf("no free backup slot for %s (%s.old and .1-.%d are all occupied and undeletable)",
		target, target, maxBackupSlots)
}

// SwapOne backs the target up (renaming a running executable is legal on
// Windows — NT locks the image by open handle, not path) and copies the
// staged binary into place with the target's own file mode. Returns the
// backup path actually used so the caller can roll back; it is usually
// "<target>.old" but see freeBackupPath. On failure the backup is renamed
// back.
//
// A target that is MISSING is first restored from the backup an interrupted
// earlier swap left (RestoreMissing): between its rename-aside and its copy
// the target path is empty, and a run killed there must not leave the next
// one refusing on "no such file" beside the only good copy of the binary.
func SwapOne(target, staged string) (string, error) {
	if _, err := RestoreMissing(target); err != nil {
		return "", fmt.Errorf("back up %s: %w", target, err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("back up %s: %w", target, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("back up %s: not a regular file", target)
	}
	backup, err := freeBackupPath(target)
	if err != nil {
		return "", fmt.Errorf("back up %s: %w", target, err)
	}
	if err := os.Rename(target, backup); err != nil {
		return "", fmt.Errorf("back up %s: %w", target, err)
	}
	if err := CopyFileMode(staged, target, info.Mode().Perm()); err != nil {
		if rbErr := os.Rename(backup, target); rbErr != nil {
			return "", fmt.Errorf("install %s: %w (AND rollback failed: %v — restore %s manually)", target, err, rbErr, backup)
		}
		return "", fmt.Errorf("install %s: %w", target, err)
	}
	return backup, nil
}

// RestoreMissing renames the backup of an interrupted swap back into place
// when target does not exist and a regular-file backup does. With several
// backups the NEWEST by mtime is the one — an interrupted swap renamed the
// live binary aside last, into whichever slot was free, while an older slot
// may hold a release behind it (pinned, so never swept). Reports whether it
// restored one. A target that exists, whatever it is, is left alone, and so
// is a target with no backup: the caller's own stat then says what is
// missing.
func RestoreMissing(target string) (bool, error) {
	if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	var newest string
	var newestAt time.Time
	for _, backup := range backupPaths(target) {
		info, err := os.Stat(backup)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if newest == "" || info.ModTime().After(newestAt) {
			newest, newestAt = backup, info.ModTime()
		}
	}
	if newest == "" {
		return false, nil
	}
	if err := os.Rename(newest, target); err != nil {
		return false, fmt.Errorf("restore %s from %s: %w", target, newest, err)
	}
	return true, nil
}

// RemoveBackups deletes every backup SwapOne may have left for target — the
// canonical "<target>.old" plus any ".old.N" fallbacks. Best-effort and
// gap-tolerant (slot 1 can outlive slot 2): a backup still pinned by a
// surviving process stays, and a later launch retries. Built by construction
// rather than filepath.Glob so an install path containing glob metacharacters
// ("[", "*") still matches its own backups.
//
// Nothing is removed unless target itself is present as a regular file: when
// it is missing or something else stands at its path, a backup may be the
// only good copy of the binary — the state an interrupted swap leaves, which
// RestoreMissing exists to repair — and a sweep would make it unrepairable.
func RemoveBackups(target string) {
	if info, err := os.Stat(target); err != nil || !info.Mode().IsRegular() {
		return
	}
	for _, path := range backupPaths(target) {
		os.Remove(path)
	}
}

// CopyFile copies src to dst as a new executable (defaultMode); see
// CopyFileMode for how dst is written.
func CopyFile(src, dst string) error {
	return CopyFileMode(src, dst, defaultMode)
}

// CopyFileMode copies src to dst with the given permission bits. The bytes go
// to "<dst>.new" first and are renamed into place only once they are all
// there, so a copy that fails or is killed midway never leaves a short file
// at dst: whatever stood there stands untouched, and a dst that did not
// exist still does not. The leftover ".new" is removed on failure (and
// overwritten by the next attempt if the process died before it could be).
// The mode is applied with Chmod rather than left to OpenFile, which would
// let the umask take bits off it. The rename is retried once after a short
// pause, as internal/atomicfile's is: on Windows an antivirus scanner opens
// a fresh executable the moment it is closed, and a rename while it holds
// the handle fails — which would fail Quil's own self-update on the very
// file it just wrote.
func CopyFileMode(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, cpErr := io.Copy(out, in)
	if closeErr := out.Close(); cpErr == nil {
		cpErr = closeErr
	}
	if cpErr == nil {
		cpErr = os.Chmod(tmp, mode)
	}
	if cpErr == nil {
		if cpErr = renameFn(tmp, dst); cpErr != nil {
			time.Sleep(renameRetryDelay)
			cpErr = renameFn(tmp, dst)
		}
	}
	if cpErr != nil {
		os.Remove(tmp)
		return cpErr
	}
	return nil
}

// renameFn is os.Rename; a test replaces it to make an attempt fail.
var renameFn = os.Rename

// renameRetryDelay mirrors internal/atomicfile's (unexported there).
const renameRetryDelay = 50 * time.Millisecond
