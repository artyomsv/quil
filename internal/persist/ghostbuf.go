package persist

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// sanitizeID strips directory components to prevent path traversal.
func sanitizeID(id string) string {
	return filepath.Base(id)
}

// SaveBuffer writes a pane's output buffer to a file atomically.
func SaveBuffer(dir, paneID string, data []byte) error {
	if len(data) == 0 {
		return nil
	}

	paneID = sanitizeID(paneID)
	path := filepath.Join(dir, paneID+".bin")
	tmpPath := path + ".tmp"

	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("write buffer %s: %w", paneID, err)
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("warning: remove old buffer %s: %v", paneID, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename buffer %s: %w", paneID, err)
	}
	return nil
}

// LoadBuffer reads a pane's saved output buffer from disk.
// Returns nil, nil if the file doesn't exist.
func LoadBuffer(dir, paneID string) ([]byte, error) {
	paneID = sanitizeID(paneID)
	path := filepath.Join(dir, paneID+".bin")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}

// SaveAllBuffers writes all pane buffers to disk (best-effort).
// Returns the first error encountered but continues saving remaining buffers.
func SaveAllBuffers(dir string, buffers map[string][]byte) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create buffer dir: %w", err)
	}

	var errs []error
	for paneID, data := range buffers {
		if err := SaveBuffer(dir, paneID, data); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// CleanBuffers removes buffer files for panes that no longer exist.
func CleanBuffers(dir string, activePaneIDs []string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	active := make(map[string]bool, len(activePaneIDs))
	for _, id := range activePaneIDs {
		active[id] = true
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".bin") {
			continue
		}
		paneID := strings.TrimSuffix(e.Name(), ".bin")
		if !active[paneID] {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				log.Printf("warning: clean orphan buffer %s: %v", paneID, err)
			}
		}
	}
	return nil
}
