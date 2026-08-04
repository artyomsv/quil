package daemon

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// projectNames lists every project's name. Caller holds sm.mu.
func (sm *SessionManager) projectNames() []string {
	out := make([]string, 0, len(sm.projects))
	for _, p := range sm.projects {
		out = append(out, p.Name)
	}
	return out
}

// uniqueProjectName returns want, or want with the lowest numeric suffix no
// existing project carries.
//
// It DISAMBIGUATES rather than refusing, because a refusal here would be
// silent: the daemon has no error channel back to a create, and this package
// has already learnt what a silently-ignored project message costs — a daemon
// that accepted create and did nothing read as a broken dialog for an evening.
// A suffixed name keeps the user's own words, so the row stays findable, and
// keeps the create they asked for.
//
// Compared case-insensitively after trimming: neither case nor padding makes
// two rows tellable apart on screen, which is the only property that matters.
func uniqueProjectName(taken []string, want string) string {
	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	used := make(map[string]bool, len(taken))
	for _, t := range taken {
		used[norm(t)] = true
	}
	if !used[norm(want)] {
		return want
	}
	// Bounded by construction: each candidate is distinct and there are
	// finitely many taken names, so one of the first len(taken)+2 is free.
	base := strings.TrimSpace(want)
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s (%d)", base, n)
		if !used[norm(candidate)] {
			return candidate
		}
	}
}

// Project groups tabs under one named piece of work rooted at one directory.
// Daemon-owned and persisted: a client-side-only grouping would be lost on a
// fresh client, invisible to a second client, and unusable for MCP scoping.
//
// Project has NO Dest field. The daemon does not know it is remote — Dest is
// the CLIENT's label for the connection a project arrived on.
type Project struct {
	ID        string
	Name      string
	RootDir   string
	TabIDs    []string
	ActiveTab string

	// Bootstrap marks a project the DAEMON invented rather than one a user
	// named: the one createTabLocked makes when a tab needs a home and none
	// exists, and the one migrateToDefaultProject wraps a pre-projects
	// workspace in. Both are called "Default", but the name cannot be the
	// signal — a user is free to name a project Default, and renaming this one
	// is exactly what stops it being a bootstrap.
	//
	// The client uses it to ADOPT: naming a project on a host whose only
	// project is this one renames it in place, so the host's existing tabs end
	// up under the name the user chose instead of beside it. Persisted, because
	// a daemon restart must not turn an un-adopted default into a real project.
	Bootstrap bool
}

func (sm *SessionManager) CreateProject(name, rootDir string) *Project {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	p := &Project{
		ID: "proj-" + uuid.New().String()[:8],
		// Disambiguated HERE because here is the only place that can be sure.
		// The client refuses a duplicate too, but it checks its own snapshot of
		// this daemon's projects — empty until the first workspace_state
		// arrives, and Enter on the form's Name row submits immediately, so
		// that window is one keystroke wide. Two rows with the same name on the
		// same host are indistinguishable in the sidebar: nothing says which
		// holds the user's tabs, and removing the wrong one takes them.
		Name:    uniqueProjectName(sm.projectNames(), name),
		RootDir: rootDir,
	}
	sm.projects[p.ID] = p
	sm.projectOrder = append(sm.projectOrder, p.ID)
	if sm.activeProject == "" {
		sm.activeProject = p.ID
	}
	return p
}

// DestroyProject removes a project with every tab and pane under it and
// returns the detached panes. Callers MUST hand them to releasePanes OFF-LOCK:
// PTY.Close() blocks until the child is reaped, and doing that under sm.mu
// starves every reader behind the RWMutex writer.
func (sm *SessionManager) DestroyProject(id string) []*Pane {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	p, ok := sm.projects[id]
	if !ok {
		return nil
	}

	var detached []*Pane
	for _, tabID := range p.TabIDs {
		tab, ok := sm.tabs[tabID]
		if !ok {
			continue
		}
		for _, paneID := range tab.Panes {
			if pane, ok := sm.panes[paneID]; ok {
				detached = append(detached, pane)
				delete(sm.panes, paneID)
			}
		}
		delete(sm.tabs, tabID)
		sm.tabOrder = removeString(sm.tabOrder, tabID)
	}

	delete(sm.projects, id)
	sm.projectOrder = removeString(sm.projectOrder, id)
	if sm.activeProject == id {
		sm.activeProject = ""
		if len(sm.projectOrder) > 0 {
			sm.activeProject = sm.projectOrder[0]
		}
	}

	// activeTab must name a tab that belongs to the (possibly new)
	// activeProject — never derived from the global tabOrder, which can
	// hand back a tab belonging to a DIFFERENT project than the one now
	// active. Task 7's client scopes the visible tab list to the active
	// project's TabIDs alone, so a mismatch here renders as a highlighted
	// tab absent from that list, or an active project with nothing
	// highlighted.
	sm.activeTab = ""
	if ap, ok := sm.projects[sm.activeProject]; ok {
		if ap.ActiveTab != "" {
			sm.activeTab = ap.ActiveTab
		} else if len(ap.TabIDs) > 0 {
			sm.activeTab = ap.TabIDs[0]
		}
	}

	return detached
}

func (sm *SessionManager) UpdateProject(id, name, rootDir string) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	p, ok := sm.projects[id]
	if !ok {
		return false
	}
	// Naming it is what makes it the user's. A project that stayed Bootstrap
	// after a rename would be adopted a second time by the next create, which
	// would silently rename the work the user had just named.
	p.Name, p.RootDir, p.Bootstrap = name, rootDir, false
	return true
}

// SwitchProject makes id the active project and moves the global active tab
// onto that project's OWN remembered tab. It returns that tab's ID (empty for
// a project with no tabs) and whether the project existed.
//
// Moving sm.activeTab is not bookkeeping. It is what respawnPanes eagerly
// restores on the next daemon start, so leaving it on the OUTGOING project's
// tab makes the following restore warm up the wrong project.
//
// The tab ID is returned rather than kept private because the caller has to
// spawn it: after a lazy restore every tab but sm.activeTab's is Pending, and
// nothing else on the project-switch path reaches ensureTabSpawned — so the
// incoming project's panes would sit on the restore indicator forever, with no
// process behind them and no resize able to rescue them.
func (sm *SessionManager) SwitchProject(id string) (string, bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	p, ok := sm.projects[id]
	if !ok {
		return "", false
	}
	sm.activeProject = id

	// The remembered tab can have been destroyed since; fall back to the
	// project's first, exactly like the client's indexOfTab. A project with no
	// tabs at all leaves activeTab alone — there is nothing to point it at,
	// and clearing it would strand every later "which tab" question.
	tabID := p.ActiveTab
	if _, live := sm.tabs[tabID]; !live {
		tabID = ""
		if len(p.TabIDs) > 0 {
			tabID = p.TabIDs[0]
		}
	}
	if tabID != "" {
		sm.activeTab = tabID
		p.ActiveTab = tabID
	}
	return tabID, true
}

func (sm *SessionManager) ReorderProject(id string, newIndex int) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if _, ok := sm.projects[id]; !ok {
		return false
	}
	order := removeString(sm.projectOrder, id)
	if newIndex < 0 {
		newIndex = 0
	}
	if newIndex > len(order) {
		newIndex = len(order)
	}
	rest := append([]string{id}, order[newIndex:]...)
	sm.projectOrder = append(order[:newIndex:newIndex], rest...)
	return true
}

// Projects returns COPIES. Returning live pointers would let a caller holding
// the slice past the unlock race UpdateProject mutating Name/RootDir.
func (sm *SessionManager) Projects() []Project {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make([]Project, 0, len(sm.projectOrder))
	for _, id := range sm.projectOrder {
		p, ok := sm.projects[id]
		if !ok {
			continue
		}
		cp := *p
		cp.TabIDs = append([]string(nil), p.TabIDs...)
		out = append(out, cp)
	}
	return out
}

func (sm *SessionManager) ActiveProject() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.activeProject
}

func (sm *SessionManager) CreateTabInProject(projectID, name string) *Tab {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.createTabLocked(projectID, name)
}

func removeString(s []string, v string) []string {
	for i, x := range s {
		if x == v {
			return append(s[:i:i], s[i+1:]...)
		}
	}
	return s
}

func indexOfString(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

// slideString moves v to ordinal newIdx IN PLACE, shifting the entries between
// the old and new positions by one. That is the tab drag's own semantics: a
// swap would teleport the displaced tab into the dragged tab's old slot, which
// feels wrong when dragging across several positions.
//
// newIdx is clamped to the slice. Reports whether anything actually moved.
func slideString(s []string, v string, newIdx int) bool {
	from := indexOfString(s, v)
	if from < 0 {
		return false
	}
	if newIdx < 0 {
		newIdx = 0
	}
	if newIdx >= len(s) {
		newIdx = len(s) - 1
	}
	if from == newIdx {
		return false
	}
	if from < newIdx {
		copy(s[from:newIdx], s[from+1:newIdx+1])
	} else {
		copy(s[newIdx+1:from+1], s[newIdx:from])
	}
	s[newIdx] = v
	return true
}

// reanchorTab repositions tabID in the GLOBAL tab order so that its position
// relative to its own project's other tabs matches projTabs, without moving
// any other project's tab.
//
// It anchors on a NEIGHBOUR rather than on an index because the two lists are
// not parallel: the global order interleaves projects in tab-creation order,
// so the project-relative ordinal that named the destination has no meaning
// in it. The tab that now precedes it inside the project is the only stable
// reference point.
func reanchorTab(order, projTabs []string, tabID string) []string {
	at := indexOfString(projTabs, tabID)
	if at < 0 {
		return order
	}
	rest := removeString(order, tabID)
	if at > 0 {
		if i := indexOfString(rest, projTabs[at-1]); i >= 0 {
			return insertStringAt(rest, i+1, tabID)
		}
	}
	if at+1 < len(projTabs) {
		if i := indexOfString(rest, projTabs[at+1]); i >= 0 {
			return insertStringAt(rest, i, tabID)
		}
	}
	// A project with no other tab in the global order has no neighbour to
	// anchor against, and no visible ordering to get wrong.
	return append(rest, tabID)
}

func insertStringAt(s []string, i int, v string) []string {
	s = append(s, "")
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}
