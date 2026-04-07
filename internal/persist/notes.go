package persist

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NotesFileName returns the sanitized filename for a pane's notes file.
// The pane ID must not contain any path separators, path traversal tokens,
// or other special characters — otherwise the function returns an error.
func NotesFileName(paneID string) (string, error) {
	if paneID == "" {
		return "", fmt.Errorf("invalid pane ID: empty")
	}
	if strings.ContainsAny(paneID, `/\`) {
		return "", fmt.Errorf("invalid pane ID: contains path separator: %q", paneID)
	}
	if paneID == "." || paneID == ".." || strings.HasPrefix(paneID, ".") {
		return "", fmt.Errorf("invalid pane ID: %q", paneID)
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
// Returns an empty string when no notes file exists.
func LoadNotes(dir, paneID string) (string, error) {
	path, err := NotesPath(dir, paneID)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read notes %s: %w", path, err)
	}
	return string(data), nil
}

// SaveNotes writes notes content atomically for a pane.
// Creates the notes directory if missing. Uses a temp+rename pattern so
// readers never observe a partially written file.
func SaveNotes(dir, paneID, content string) error {
	path, err := NotesPath(dir, paneID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create notes dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0600); err != nil {
		return fmt.Errorf("write temp notes: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// Best-effort cleanup of the stale temp file.
		_ = os.Remove(tmp)
		return fmt.Errorf("rename notes: %w", err)
	}
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
