package persist

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// windowsReservedNames are device names that the Windows kernel intercepts
// before any filesystem driver sees them. Creating a file called "CON.md"
// or "NUL.md" still resolves to the device, not a regular file. We reject
// these unconditionally so behaviour is identical across platforms.
var windowsReservedNames = map[string]struct{}{
	"con": {}, "prn": {}, "aux": {}, "nul": {},
	"com1": {}, "com2": {}, "com3": {}, "com4": {}, "com5": {},
	"com6": {}, "com7": {}, "com8": {}, "com9": {},
	"lpt1": {}, "lpt2": {}, "lpt3": {}, "lpt4": {}, "lpt5": {},
	"lpt6": {}, "lpt7": {}, "lpt8": {}, "lpt9": {},
}

// NotesFileName returns the sanitized filename for a pane's notes file.
// The pane ID must not contain any path separators, path traversal tokens,
// Windows reserved device names, or other special characters.
func NotesFileName(paneID string) (string, error) {
	if paneID == "" {
		return "", fmt.Errorf("invalid pane ID: empty")
	}
	if strings.ContainsAny(paneID, `/\`) {
		return "", fmt.Errorf("invalid pane ID: contains path separator: %q", paneID)
	}
	if paneID == "." || paneID == ".." {
		return "", fmt.Errorf("invalid pane ID: %q", paneID)
	}
	// Reject control characters and Windows-forbidden ASCII so the file is
	// portable across all supported platforms.
	for _, r := range paneID {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("invalid pane ID: contains control character")
		}
		if strings.ContainsRune(`<>:"|?*`, r) {
			return "", fmt.Errorf("invalid pane ID: contains forbidden character %q", r)
		}
	}
	// Reject Windows reserved device names (case-insensitive).
	if _, reserved := windowsReservedNames[strings.ToLower(paneID)]; reserved {
		return "", fmt.Errorf("invalid pane ID: reserved device name %q", paneID)
	}
	// Defense-in-depth: if filepath.Base changes the value, something unexpected
	// is inside the ID (e.g. a null byte or platform-specific separator).
	if filepath.Base(paneID) != paneID {
		return "", fmt.Errorf("invalid pane ID: %q", paneID)
	}
	return paneID + ".md", nil
}

// NotesPath returns the absolute path to a pane's notes file within dir.
func NotesPath(dir, paneID string) (string, error) {
	name, err := NotesFileName(paneID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// LoadNotes reads the notes content for a pane from dir.
// Returns an empty string when no notes file exists, or when the notes
// directory is missing/not yet created. Refuses to follow symlinks so an
// attacker who plants a symlink in the notes directory cannot trick the
// editor into reading another file.
func LoadNotes(dir, paneID string) (string, error) {
	path, err := NotesPath(dir, paneID)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		// Treat "missing file", "missing parent directory", and "parent is
		// not a directory" all as "no notes yet" — the editor opens with an
		// empty buffer and the next save will create the directory.
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return "", nil
		}
		return "", fmt.Errorf("stat notes: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("notes file is a symlink, refusing to follow: %s", path)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read notes: %w", err)
	}
	return string(data), nil
}

// SaveNotes writes notes content atomically for a pane.
// Creates the notes directory if missing. Uses a unique temp file +
// rename pattern so readers never observe a partial write and concurrent
// writers do not race on the same temp filename.
func SaveNotes(dir, paneID, content string) error {
	// Validate first so we never create a directory just to fail.
	if _, err := NotesFileName(paneID); err != nil {
		return err
	}
	path, err := NotesPath(dir, paneID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create notes dir: %w", err)
	}
	// Best-effort: tighten permissions if the directory pre-existed with
	// looser bits (e.g., created by a previous tool that did not respect
	// the umask). Failure is non-fatal — the file mode is still 0600.
	_ = os.Chmod(dir, 0700)

	// Create a unique temp file in the same directory so the rename is
	// atomic on the same filesystem. os.CreateTemp gives us a fresh,
	// unguessable name and protects against the deterministic .tmp race
	// flagged in security review M3.
	tmp, err := os.CreateTemp(dir, paneID+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp notes: %w", err)
	}
	tmpPath := tmp.Name()
	// Cleanup-on-failure: if anything below fails, remove the temp file.
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := io.WriteString(tmp, content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp notes: %w", err)
	}
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp notes: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp notes: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename notes: %w", err)
	}
	committed = true
	return nil
}

// DeleteNotes removes a pane's notes file. Missing files are not an error.
func DeleteNotes(dir, paneID string) error {
	path, err := NotesPath(dir, paneID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete notes: %w", err)
	}
	return nil
}
