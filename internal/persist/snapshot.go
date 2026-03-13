package persist

import (
	"encoding/json"
	"fmt"
	"os"
)

// Save writes workspace state as JSON to path atomically.
// The previous file is renamed to path.bak for rollback.
func Save(path string, state map[string]any) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal workspace: %w", err)
	}

	tmpPath := path + ".tmp"
	bakPath := path + ".bak"

	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	// Rotate: current → .bak (ignore error if current doesn't exist)
	if _, err := os.Stat(path); err == nil {
		os.Remove(bakPath)
		os.Rename(path, bakPath)
	}

	// Promote: .tmp → current
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp to workspace: %w", err)
	}

	return nil
}

// Load reads workspace state from a JSON file.
// Falls back to path.bak if the primary file is missing or corrupt.
// Returns nil, nil if neither file exists (fresh workspace).
func Load(path string) (map[string]any, error) {
	state, err := loadFile(path)
	if err == nil {
		return state, nil
	}

	// Try backup
	bakPath := path + ".bak"
	state, bakErr := loadFile(bakPath)
	if bakErr == nil {
		return state, nil
	}

	// Neither file exists → fresh workspace
	if os.IsNotExist(err) && os.IsNotExist(bakErr) {
		return nil, nil
	}

	return nil, fmt.Errorf("load workspace: primary: %w, backup: %v", err, bakErr)
}

func loadFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return state, nil
}
