package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// recentCWDMax caps how many recent working directories are remembered and
// offered as a quick pick in the pane setup dialog.
const recentCWDMax = 5

// pushRecentCWD returns list with dir cleaned and moved to the front,
// de-duplicated (case-insensitively on Windows) and truncated to limit. A
// blank dir is a no-op. Pure — it never mutates the input slice.
func pushRecentCWD(list []string, dir string, limit int) []string {
	if strings.TrimSpace(dir) == "" {
		return list
	}
	if limit < 0 {
		limit = 0
	}
	dir = filepath.Clean(dir)
	out := make([]string, 0, len(list)+1)
	out = append(out, dir)
	for _, d := range list {
		if pathEqual(d, dir) {
			continue
		}
		out = append(out, d)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// pathEqual compares two paths, case-insensitively on Windows where the
// filesystem is case-preserving but case-insensitive.
func pathEqual(a, b string) bool {
	return pathEqualCase(a, b, runtime.GOOS == "windows")
}

// pathEqualCase is the platform-parameterised core of pathEqual, split out so
// both the case-sensitive and case-insensitive branches are testable on any OS.
func pathEqualCase(a, b string, caseInsensitive bool) bool {
	if caseInsensitive {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// LoadRecentCWDs reads the recent-CWD list from a JSON file. Returns nil on a
// missing, symlinked, or corrupt file — a fresh, empty history is always a
// valid state. The result is capped at recentCWDMax so a hand-edited oversized
// file can't grow the pick list.
func LoadRecentCWDs(path string) []string {
	// Reject a symlink at the history path, matching the repo convention in
	// persist/notes.go and opencodehook.ReadPersistedSessionID.
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return nil
	}
	if len(list) > recentCWDMax {
		list = list[:recentCWDMax]
	}
	return list
}

// SaveRecentCWDs writes the list to a JSON file atomically (.tmp + rename),
// mirroring the persistence pattern in instances.go.
func SaveRecentCWDs(path string, list []string) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	// Ensure the parent dir exists so the save is self-sufficient even on a
	// brand-new QUIL_HOME (matches the defensive pattern in persist/snapshot.go).
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
