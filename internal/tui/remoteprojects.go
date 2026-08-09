package tui

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"github.com/artyomsv/quil/internal/config"
)

// CachedProject is the client's memory of one project on a remote daemon.
//
// The ID is the payload, not a decoration: an offline row reuses it, so
// applyWorkspaceState's existingProjects lookup resolves to that same
// *ProjectModel when the host returns and fills it in place.
type CachedProject struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	RootDir string `json:"root_dir,omitempty"`
}

// LoadRemoteProjects reads one destination's cached project list. Every failure
// answers nil: a cache that cannot be read must degrade to label-named rows,
// never to no rows, because the row is what says the host is configured.
func LoadRemoteProjects(path string) []CachedProject {
	// Symlink refusal matches LoadRecentCWDs and persist/notes.go.
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var list []CachedProject
	if err := json.Unmarshal(data, &list); err != nil {
		return nil
	}
	return list
}

// SaveRemoteProjects writes the list atomically (.tmp + rename), the same
// pattern LoadRecentCWDs' writer uses.
func SaveRemoteProjects(path string, list []CachedProject) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// cacheRemoteProjects records one destination's current project list, so a
// launch that cannot reach it can still show those projects by name.
//
// Best effort throughout: a cache that cannot be written is a log line, never a
// failed anything. The destination is live at this point — the caller is a
// workspace broadcast — so nothing the user can see depends on this succeeding.
func (m *Model) cacheRemoteProjects(dest string) {
	if dest == "" {
		return // the local daemon is never seeded offline
	}

	list := make([]CachedProject, 0, len(m.projects))
	for _, p := range m.projects {
		if p.Dest != dest {
			continue
		}
		// Neither of these is evidence about the host. A synthetic ID exists
		// only in this process, and an offline row is a replay of the cache
		// itself — writing it back would pin a stale list that the host's real
		// answer could never overwrite.
		if isSyntheticProject(p.ID) || p.Offline != nil {
			continue
		}
		list = append(list, CachedProject{ID: p.ID, Name: p.Name, RootDir: p.RootDir})
	}
	if len(list) == 0 {
		return
	}

	if sameCachedProjects(m.cachedRemote[dest], list) {
		return
	}
	if m.cachedRemote == nil {
		m.cachedRemote = map[string][]CachedProject{}
	}
	m.cachedRemote[dest] = list

	save := m.saveRemoteProjectsFn
	if save == nil {
		save = SaveRemoteProjects
	}
	if err := save(config.RemoteProjectsPath(dest), list); err != nil {
		log.Printf("remote projects cache for %s: %v", dest, err)
	}
}

// sameCachedProjects compares by value and by order. Order is meaningful — it
// is the order the rows appear in — so a reordering is a change worth writing.
func sameCachedProjects(a, b []CachedProject) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
