package daemon

import "github.com/google/uuid"

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
}

func (sm *SessionManager) CreateProject(name, rootDir string) *Project {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	p := &Project{
		ID:      "proj-" + uuid.New().String()[:8],
		Name:    name,
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
		// Repair activeTab the way DestroyTab does — a stale one feeds
		// SnapshotState, the restore eager-spawn decision, and the broadcast.
		if sm.activeTab == tabID {
			sm.activeTab = ""
		}
	}
	if sm.activeTab == "" && len(sm.tabOrder) > 0 {
		sm.activeTab = sm.tabOrder[0]
	}

	delete(sm.projects, id)
	sm.projectOrder = removeString(sm.projectOrder, id)
	if sm.activeProject == id {
		sm.activeProject = ""
		if len(sm.projectOrder) > 0 {
			sm.activeProject = sm.projectOrder[0]
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
	p.Name, p.RootDir = name, rootDir
	return true
}

func (sm *SessionManager) SwitchProject(id string) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if _, ok := sm.projects[id]; !ok {
		return false
	}
	sm.activeProject = id
	return true
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
