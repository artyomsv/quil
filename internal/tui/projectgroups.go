package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

// Project groups are the sidebar's CLIENT-side grouping of projects. Nothing
// here is sent to a daemon — which is what lets one group hold a laptop's
// project beside a build host's — and nothing here touches the Model: every
// operation is pure over projectGroups, so the rules test without a sidebar.

const (
	// projectGroupsFileCap bounds project-groups.json. The read stops one byte
	// past it, and a file that reaches it is refused like a corrupt one.
	projectGroupsFileCap = 256 << 10
	// maxGroupNameRunes is the longest group name. The name editor stops
	// accepting input here; a longer name in a hand-edited file is cut on load.
	maxGroupNameRunes = 32
	// projectGroupsFileVersion is written so a later format can tell this one
	// apart. Load does not branch on it yet.
	projectGroupsFileVersion = 1
)

// The feature's flash texts, exact.
const (
	groupNameTakenFlash  = "A group with that name already exists"
	groupNameEmptyFlash  = "Group name cannot be empty"
	groupSaveFailedFlash = "Could not save project groups"
)

var (
	errGroupNameEmpty = errors.New("group name is empty")
	errGroupNameTaken = errors.New("group name is already taken")
	errNoSuchGroup    = errors.New("no such group")
)

// groupMember names one project by (Dest, ID) — the pair, never the ID alone.
// A project ID is "proj-" plus eight hex digits minted independently by every
// daemon, so two daemons in one sidebar can hand out the same one (the same
// hazard moveProject answers with pointer identity).
type groupMember struct {
	Dest string `json:"dest"`
	ID   string `json:"id"`
}

// projectGroup is one named group. Members' order is NOT display order: the
// sidebar lists a group's projects in m.projects order, like every section.
type projectGroup struct {
	Name      string        `json:"name"`
	Collapsed bool          `json:"collapsed"`
	Members   []groupMember `json:"members"`
}

// projectGroups is every group, in display order. A project is in at most one.
type projectGroups struct {
	Groups []projectGroup `json:"groups"`
}

// projectGroupsFile is the on-disk envelope.
type projectGroupsFile struct {
	Version int            `json:"version"`
	Groups  []projectGroup `json:"groups"`
}

// normalizeGroupName trims s and cuts it to maxGroupNameRunes, re-trimming so
// a cut that exposes a space does not keep an invisible trailing blank.
func normalizeGroupName(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxGroupNameRunes {
		s = strings.TrimSpace(string([]rune(s)[:maxGroupNameRunes]))
	}
	return s
}

// indexOf is the group named name (case-insensitively, after normalising),
// or -1.
func (g *projectGroups) indexOf(name string) int {
	want := normalizeGroupName(name)
	for i := range g.Groups {
		if strings.EqualFold(g.Groups[i].Name, want) {
			return i
		}
	}
	return -1
}

// groupOf is the group holding (dest, id), or -1.
func (g *projectGroups) groupOf(dest, id string) int {
	for i := range g.Groups {
		for _, mb := range g.Groups[i].Members {
			if mb.Dest == dest && mb.ID == id {
				return i
			}
		}
	}
	return -1
}

// memberIndex maps every member to its group, for a caller resolving many
// projects at once (the sidebar, every frame). First group wins, as in groupOf.
func (g *projectGroups) memberIndex() map[groupMember]int {
	idx := make(map[groupMember]int)
	for i := range g.Groups {
		for _, mb := range g.Groups[i].Members {
			if _, dup := idx[mb]; !dup {
				idx[mb] = i
			}
		}
	}
	return idx
}

// validName normalises name and refuses an empty one or one another group
// (any index but except) already carries, ignoring case.
func (g *projectGroups) validName(name string, except int) (string, error) {
	n := normalizeGroupName(name)
	if n == "" {
		return "", errGroupNameEmpty
	}
	for i := range g.Groups {
		if i != except && strings.EqualFold(g.Groups[i].Name, n) {
			return "", errGroupNameTaken
		}
	}
	return n, nil
}

// addGroup appends an empty, expanded group and returns its index.
func (g *projectGroups) addGroup(name string) (int, error) {
	n, err := g.validName(name, -1)
	if err != nil {
		return -1, err
	}
	g.Groups = append(g.Groups, projectGroup{Name: n})
	return len(g.Groups) - 1, nil
}

// renameGroup renames group i. A case change of its own name is allowed.
func (g *projectGroups) renameGroup(i int, name string) error {
	if i < 0 || i >= len(g.Groups) {
		return errNoSuchGroup
	}
	n, err := g.validName(name, i)
	if err != nil {
		return err
	}
	g.Groups[i].Name = n
	return nil
}

// deleteGroup removes group i and ONLY the group: its members become
// ungrouped, and no project is touched.
func (g *projectGroups) deleteGroup(i int) bool {
	if i < 0 || i >= len(g.Groups) {
		return false
	}
	g.Groups = append(g.Groups[:i], g.Groups[i+1:]...)
	return true
}

// moveGroup slides group from to slot to, the groups between shifting by one
// — the slide moveProject does, for the same reason (a swap teleports).
func (g *projectGroups) moveGroup(from, to int) bool {
	n := len(g.Groups)
	if from == to || from < 0 || to < 0 || from >= n || to >= n {
		return false
	}
	grp := g.Groups[from]
	if from < to {
		copy(g.Groups[from:to], g.Groups[from+1:to+1])
	} else {
		copy(g.Groups[to+1:from+1], g.Groups[to:from])
	}
	g.Groups[to] = grp
	return true
}

// setCollapsed sets group i's state and reports whether it changed.
func (g *projectGroups) setCollapsed(i int, collapsed bool) bool {
	if i < 0 || i >= len(g.Groups) || g.Groups[i].Collapsed == collapsed {
		return false
	}
	g.Groups[i].Collapsed = collapsed
	return true
}

// assign puts (dest, id) in group i, taking it OUT of any other group first —
// a project is in at most one. Reports whether anything changed.
func (g *projectGroups) assign(i int, dest, id string) bool {
	if i < 0 || i >= len(g.Groups) || id == "" {
		return false
	}
	if g.groupOf(dest, id) == i {
		return false
	}
	g.unassign(dest, id)
	g.Groups[i].Members = append(g.Groups[i].Members, groupMember{Dest: dest, ID: id})
	return true
}

// unassign removes (dest, id) from whichever group holds it.
func (g *projectGroups) unassign(dest, id string) bool {
	changed := false
	for i := range g.Groups {
		kept := g.Groups[i].Members[:0]
		for _, mb := range g.Groups[i].Members {
			if mb.Dest == dest && mb.ID == id {
				changed = true
				continue
			}
			kept = append(kept, mb)
		}
		g.Groups[i].Members = kept
	}
	return changed
}

// prune drops every member whose destination IS a key of liveByDest and whose
// ID is not in that destination's live set. A destination absent from the map
// was not heard from — offline, parked, disconnected — and keeps every member:
// its rows are the client's stand-ins, not the daemon's answer.
func (g *projectGroups) prune(liveByDest map[string]map[string]bool) bool {
	changed := false
	for i := range g.Groups {
		kept := g.Groups[i].Members[:0]
		for _, mb := range g.Groups[i].Members {
			if live, known := liveByDest[mb.Dest]; known && !live[mb.ID] {
				changed = true
				continue
			}
			kept = append(kept, mb)
		}
		g.Groups[i].Members = kept
	}
	return changed
}

// clone deep-copies g — the snapshot a save Cmd writes from its own goroutine
// while Update keeps editing the original.
func (g projectGroups) clone() projectGroups {
	out := projectGroups{Groups: make([]projectGroup, len(g.Groups))}
	for i, grp := range g.Groups {
		grp.Members = append([]groupMember(nil), grp.Members...)
		out.Groups[i] = grp
	}
	return out
}

// sanitizeLoadedGroups holds a loaded file to the same rules the operations
// enforce: blank and duplicate names dropped (first wins), empty IDs dropped,
// and a project claimed by an earlier group dropped from a later one.
func sanitizeLoadedGroups(in projectGroups) projectGroups {
	var out projectGroups
	claimed := make(map[groupMember]bool)
	for _, grp := range in.Groups {
		name, err := out.validName(grp.Name, -1)
		if err != nil {
			continue
		}
		ng := projectGroup{Name: name, Collapsed: grp.Collapsed}
		for _, mb := range grp.Members {
			if mb.ID == "" || claimed[mb] {
				continue
			}
			claimed[mb] = true
			ng.Members = append(ng.Members, mb)
		}
		out.Groups = append(out.Groups, ng)
	}
	return out
}

// readProjectGroupsFile reads at most projectGroupsFileCap+1 bytes — one past
// the cap, so an over-cap file is detectable without reading all of it — and
// CLOSES the file before returning: Windows cannot rename an open file, and
// the caller may be about to move it aside.
func readProjectGroupsFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, projectGroupsFileCap+1))
}

// quarantineProjectGroups moves a file that cannot be used to <path>.bak, so
// the next save cannot silently overwrite the only copy of the user's groups.
func quarantineProjectGroups(path string, cause error) error {
	if err := os.Rename(path, path+".bak"); err != nil {
		return fmt.Errorf("%w; moving it aside also failed: %v", cause, err)
	}
	return fmt.Errorf("%w; moved to %s", cause, filepath.Base(path)+".bak")
}

// loadProjectGroups reads the groups file. A missing file is the first launch
// and answers (empty, nil). Every other failure answers empty groups plus an
// error for the caller to log; an unreadable, over-cap or unparseable file is
// moved to .bak first. A symlink is refused and left alone, as LoadRecentCWDs
// does — the next save's rename replaces the link itself, never its target.
func loadProjectGroups(path string) (projectGroups, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return projectGroups{}, nil
	}
	if err != nil {
		return projectGroups{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return projectGroups{}, fmt.Errorf("%s is a symlink; ignoring it", path)
	}
	data, err := readProjectGroupsFile(path)
	if err != nil {
		return projectGroups{}, quarantineProjectGroups(path, fmt.Errorf("read %s: %w", path, err))
	}
	if len(data) > projectGroupsFileCap {
		return projectGroups{}, quarantineProjectGroups(path,
			fmt.Errorf("%s is over the %d-byte cap", path, projectGroupsFileCap))
	}
	var file projectGroupsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return projectGroups{}, quarantineProjectGroups(path, fmt.Errorf("parse %s: %w", path, err))
	}
	return sanitizeLoadedGroups(projectGroups{Groups: file.Groups}), nil
}

// saveProjectGroups writes g atomically: .tmp, then rename over the real file.
func saveProjectGroups(path string, g projectGroups) error {
	groups := g.Groups
	if groups == nil {
		groups = []projectGroup{}
	}
	data, err := json.MarshalIndent(projectGroupsFile{Version: projectGroupsFileVersion, Groups: groups}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode project groups: %w", err)
	}
	if len(data) > projectGroupsFileCap {
		return fmt.Errorf("project groups encode to %d bytes, over the %d-byte cap", len(data), projectGroupsFileCap)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// Best effort: the rename error is the one worth reporting, and a
		// leftover .tmp is overwritten by the next save anyway.
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// groupsWriter serialises saves. Bubble Tea runs every Cmd on its own
// goroutine with no ordering between them, so two quick changes can reach the
// disk in either order — and both would write the same .tmp. The mutex covers
// the second; the sequence the Update goroutine stamped covers the first: a
// snapshot older than one already written is dropped.
type groupsWriter struct {
	mu   sync.Mutex
	last uint64
}

// write saves g unless a newer snapshot (higher seq) was already written. The
// sequence is recorded BEFORE the write, so a failed newer save still keeps an
// older snapshot from landing after it.
func (w *groupsWriter) write(path string, seq uint64, g projectGroups) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if seq <= w.last {
		return nil
	}
	w.last = seq
	return saveProjectGroups(path, g)
}

// ProjectGroupsState is a loaded groups file, opaque outside this package:
// cmd/quil loads it (LoadProjectGroups) and hands it to the Model
// (SetProjectGroups), so NewModel never reads the disk.
type ProjectGroupsState struct {
	groups projectGroups
}

// LoadProjectGroups reads path. The error is for the caller to log: the state
// returned beside it is always usable (empty on any failure).
func LoadProjectGroups(path string) (ProjectGroupsState, error) {
	g, err := loadProjectGroups(path)
	return ProjectGroupsState{groups: g}, err
}
