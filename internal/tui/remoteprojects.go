package tui

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/artyomsv/quil/internal/config"
)

// maxCachedProjects and maxCachedFieldLen bound what a cache file can hand
// back to the rest of the client. The values in it are not typed by hand —
// a remote daemon's broadcast reaches the cache with no cap of its own — so
// a destination that reports an unreasonable count, or a single unreasonably
// long name, grows this file into a shape nothing downstream expects.
// Rendering already bounds width (truncateCells), so this is defence in
// depth rather than a live exploit: a malformed cache must degrade to a
// smaller one, never to a failed launch. Both caps are enforced on the WRITE
// side (cacheRemoteProjects) as well as the load side below — round 2 of
// this feature's review found the write path uncapped, so a single oversized
// broadcast could grow the file before any load ever ran.
const (
	maxCachedProjects = 200
	maxCachedFieldLen = 4096
)

// maxCacheFileBytes bounds how much of a cache file LoadRemoteProjects will
// read before the count/length caps above ever get a chance to run. A bare
// os.ReadFile fully materializes the whole file in memory first, so on its
// own an oversized file is a memory-exhaustion vector independent of what
// the parsed caps enforce afterward.
//
// Derived from the other two caps plus JSON encoding slack, rather than a
// standalone number, so widening either cap widens the read budget with it
// and the two cannot silently drift apart: maxCachedProjects entries, each
// carrying three capped string fields (ID, Name, RootDir), doubled for
// worst-case JSON escaping (every byte becomes a two-byte \uXXXX-style
// escape), plus a flat allowance for field names, quotes, braces and commas.
const maxCacheFileBytes = maxCachedProjects*3*maxCachedFieldLen*2 + 4096

// truncateBytes cuts s to at most max bytes, backing off to the nearest rune
// boundary so the cut cannot split a multi-byte UTF-8 sequence — a plain
// byte-index slice can, and the corrupted tail survives silently through the
// JSON round-trip this value came from.
func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

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
//
// The result is capped at maxCachedProjects entries, each with ID/Name/RootDir
// capped at maxCachedFieldLen: the content traces back to a remote daemon's
// broadcast, so an oversized or overlong cache is a class of hazard this repo
// already treats as real (see formMsgNameCap), and the cap here is silent —
// a cache is a convenience, and a malformed one must shrink, not fail a launch.
//
// The read itself is bounded BEFORE either cap gets a chance to run
// (io.LimitReader against maxCacheFileBytes) — a bare os.ReadFile would fully
// materialize an oversized file in memory first, so the count/length caps
// would only bound what survives the read, not the read itself.
func LoadRemoteProjects(path string) []CachedProject {
	// Symlink refusal matches LoadRecentCWDs and persist/notes.go.
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCacheFileBytes))
	if err != nil {
		return nil
	}
	var list []CachedProject
	if err := json.Unmarshal(data, &list); err != nil {
		return nil
	}
	if len(list) > maxCachedProjects {
		list = list[:maxCachedProjects]
	}
	for i := range list {
		list[i].ID = truncateBytes(list[i].ID, maxCachedFieldLen)
		list[i].Name = truncateBytes(list[i].Name, maxCachedFieldLen)
		list[i].RootDir = truncateBytes(list[i].RootDir, maxCachedFieldLen)
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
//
// ID/Name/RootDir are truncated and the list is capped at maxCachedProjects
// HERE, before the change-detection compare below — not left to load time.
// Round 2 of this feature's review found the write path uncapped: a daemon
// broadcasting an oversized name or an unreasonable project count grew this
// process's memory (m.cachedRemote) and the on-disk file to match, with the
// load-side caps only ever trimming it back down on the NEXT launch. Capping
// here instead keeps memory, disk and the change-detection compare all
// looking at the same bounded values.
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
		list = append(list, CachedProject{
			ID:      truncateBytes(p.ID, maxCachedFieldLen),
			Name:    truncateBytes(p.Name, maxCachedFieldLen),
			RootDir: truncateBytes(p.RootDir, maxCachedFieldLen),
		})
	}
	if len(list) == 0 {
		return
	}
	if len(list) > maxCachedProjects {
		list = list[:maxCachedProjects]
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
