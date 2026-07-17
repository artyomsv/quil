package update

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// State is the daemon-owned check/stage status persisted at
// config.UpdateStatePath(). The TUI reads it never — update info reaches
// the TUI over IPC; this file exists so a restarted daemon remembers its
// last check across runs.
type State struct {
	LastCheckMs     int64  `json:"last_check_ms"`
	LatestVersion   string `json:"latest_version,omitempty"`
	ReleaseURL      string `json:"release_url,omitempty"`
	StagedVersion   string `json:"staged_version,omitempty"`
	InstallWritable bool   `json:"install_writable"`
}

// LoadState returns the persisted state, or the zero State on any error
// (missing file, corrupt JSON) — the pipeline treats that as "never
// checked".
func LoadState(path string) State {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}
	}
	return st
}

// SaveState writes atomically (temp+rename), creating parent dirs.
func SaveState(path string, st State) error {
	return saveJSON(path, st)
}

// notifiedFile is the TUI-owned once-per-version dialog marker.
type notifiedFile struct {
	Version string `json:"version"`
}

// LoadNotifiedVersion returns the last version the startup dialog was
// shown for, or "" (never notified / unreadable).
func LoadNotifiedVersion(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var n notifiedFile
	if err := json.Unmarshal(data, &n); err != nil {
		return ""
	}
	return n.Version
}

// SaveNotifiedVersion records that the startup dialog was shown for version.
func SaveNotifiedVersion(path, version string) error {
	return saveJSON(path, notifiedFile{Version: version})
}

func saveJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create dir for %s: %w", path, err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

// InstallWritable probes whether dir accepts file creation — the gate for
// self-update. Package-manager / system installs fail this; the pipeline
// then degrades to notify-only.
func InstallWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".quil-update-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}
