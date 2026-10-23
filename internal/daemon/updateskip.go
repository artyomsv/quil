package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/artyomsv/quil/internal/atomicfile"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/logger"
	"github.com/artyomsv/quil/internal/version"
)

// updateSkips is the Not-now store for plugin update notices: per plugin,
// the newest offered version the user declined. A notice for that version
// or an older one is ignored; a newer one interrupts again.
//
// It lives on the DAEMON, in QUIL_HOME, rather than in each TUI: the notice
// is matched here, every attached client must agree, and a restart must not
// bring the dialog back. One file per daemon also means a remote host with
// its own QUIL_HOME keeps its own answer, which is right — it is that host's
// binary that is outdated.
type updateSkips struct {
	mu      sync.Mutex
	path    string
	skipped map[string]string // plugin name → declined version
}

// updateSkipsFile is the on-disk form. A map under one key rather than the
// bare map, so a later field (a timestamp, say) needs no migration.
type updateSkipsFile struct {
	Skipped map[string]string `json:"skipped"`
}

// updateSkipsPath is the store's file, at the top of QUIL_HOME. The name is
// in ipc.IsQuilHomeEntry's stem list; a new top-level file that is not would
// make ProtectDir read the home as foreign and stop the TCP listener.
func updateSkipsPath() string {
	return filepath.Join(config.QuilDir(), "plugin-update-skips.json")
}

// loadUpdateSkips reads the store at path. A missing or unreadable file is
// an empty store — the worst that follows is one dialog the user already
// dismissed, and a refusal to start would be far worse.
func loadUpdateSkips(path string) *updateSkips {
	s := &updateSkips{path: path, skipped: make(map[string]string)}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var f updateSkipsFile
	if err := json.Unmarshal(data, &f); err != nil {
		logger.Warn("plugin update skips: %s unreadable, starting empty: %v", path, err)
		return s
	}
	for name, v := range f.Skipped {
		if name != "" && v != "" {
			s.skipped[name] = v
		}
	}
	return s
}

// Suppressed reports whether a notice offering version for plugin was
// declined: the declined version is the same or newer by semver. A stored
// or offered version that is not semver (a tool that prints a date, a
// build hash) suppresses on an exact match only, so an unparseable pair
// never hides a genuinely new one.
func (s *updateSkips) Suppressed(plugin, offered string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.skipped[plugin]
	if !ok {
		return false
	}
	return !newerVersion(offered, stored)
}

// Skip records that the user declined version for plugin and writes the
// store. An older version than the one already stored changes nothing: the
// mark only ever moves forward, so two clients answering two notices land on
// the newer one whichever order their presses arrive in.
//
// The write happens under the store's own mutex, on purpose. The store is
// touched on a Not-now press and once per notice per PTY run, so nothing hot
// waits on it, and serialising the write is what makes the file hold the
// LAST press rather than whichever write finished last.
func (s *updateSkips) Skip(plugin, declined string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, had := s.skipped[plugin]
	if had && !newerVersion(declined, stored) {
		return nil
	}
	s.skipped[plugin] = declined
	err := s.write()
	if err != nil {
		// Memory and file must agree, or the dialog stays away until the
		// next restart and then comes back: the caller reports the failure
		// and the notice is still live.
		if had {
			s.skipped[plugin] = stored
		} else {
			delete(s.skipped, plugin)
		}
	}
	return err
}

// write replaces the file with the in-memory map. Under s.mu.
func (s *updateSkips) write() error {
	data, err := json.MarshalIndent(updateSkipsFile{Skipped: s.skipped}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(s.path, append(data, '\n'))
}

// newerVersion reports whether a is a newer version than b: by semver when
// both parse, else by being a different string — the only answer that can
// be given without inventing an order for text that has none.
func newerVersion(a, b string) bool {
	c, err := version.Compare(a, b)
	if err != nil {
		return a != b
	}
	return c > 0
}

// pluginUpdateSkips is the daemon's store, loaded on first use so the
// hand-built daemons in tests need no setup and a daemon that never sees a
// notice never reads the file.
func (d *Daemon) pluginUpdateSkips() *updateSkips {
	d.updateSkipsOnce.Do(func() {
		d.updateSkips = loadUpdateSkips(updateSkipsPath())
	})
	return d.updateSkips
}
