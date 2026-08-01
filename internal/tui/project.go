package tui

// interimProjectID / interimProjectName name the single synthetic project the
// client folds every broadcast tab into until WorkspaceStateMsg carries real
// projects. They are deliberately constants rather than literals so the site
// Task 7 must replace is greppable.
const (
	interimProjectID   = "proj-interim"
	interimProjectName = "Default"
)

// ProjectModel is the client's view of one daemon-side project plus the
// destination it arrived on. Each project owns its OWN tab slice and its own
// activeTab index — nothing is ever filtered, so no index can be invalidated
// from under a caller.
//
// Dest is client-side only: the daemon does not know it is remote. Empty means
// the local daemon.
type ProjectModel struct {
	ID      string
	Name    string
	RootDir string
	Dest    string

	tabs      []*TabModel
	activeTab int
}

// cur returns the active project, or nil when the Model has no projects yet.
func (m *Model) cur() *ProjectModel {
	if m.activeProject < 0 || m.activeProject >= len(m.projects) {
		return nil
	}
	return m.projects[m.activeProject]
}

// curTabs returns the active project's tabs — the default for any read. Every
// tab INDEX in the client (activeTab, PaneRef.TabIndex, hitTestTab, the tab
// bar) is an index into this slice, never into allTabs.
func (m *Model) curTabs() []*TabModel {
	if p := m.cur(); p != nil {
		return p.tabs
	}
	return nil
}

// activeTabModel returns the tab the user is looking at, or nil. Its call
// sites predate projects and are unchanged: they already asked the right
// question, only the answer's derivation moved.
func (m Model) activeTabModel() *TabModel {
	p := m.cur()
	if p == nil || p.activeTab < 0 || p.activeTab >= len(p.tabs) {
		return nil
	}
	return p.tabs[p.activeTab]
}

// allTabs iterates EVERY project. Use it only where the operation genuinely
// spans projects and carries no index — resolving an incoming pane event,
// sweeping caches, the memory report. Everywhere else wants curTabs().
//
// Single-project fast path: handlePaneOutput calls this twice per PTY-output
// message and both spinner loops call it per frame, so the common case (one
// project — every install until Task 7 parses real ones) must not allocate.
// Every caller only reads the result, so handing back the lone project's own
// slice directly is safe.
func (m *Model) allTabs() []*TabModel {
	if len(m.projects) == 1 {
		return m.projects[0].tabs
	}
	var out []*TabModel
	for _, p := range m.projects {
		out = append(out, p.tabs...)
	}
	return out
}

// projectOf returns the project owning the tab with the given ID, or nil.
func (m *Model) projectOf(tabID string) *ProjectModel {
	for _, p := range m.projects {
		for _, t := range p.tabs {
			if t.ID == tabID {
				return p
			}
		}
	}
	return nil
}

// activeTabIdx is the active project's tab ordinal. Returns 0 on a Model with
// no projects, matching the pre-project zero value.
func (m Model) activeTabIdx() int {
	if p := m.cur(); p != nil {
		return p.activeTab
	}
	return 0
}

// broadcastProjects returns the projects a workspace broadcast from dest
// describes.
//
// A daemon that sent tabs but no projects is either older than the project
// layer or lost its project list; folding every tab into ONE synthetic project
// keeps those tabs on screen instead of blanking the client, and reproduces
// exactly the shape the client used before it parsed projects at all. Tab
// order and active tab come from the broadcast's own global fields, which is
// what a single-project workspace means.
//
// A broadcast with no tabs AND no projects describes an empty daemon and gets
// no synthetic project — an empty project list is the honest answer there.
func broadcastProjects(state WorkspaceStateMsg, dest string) []ProjectInfo {
	if len(state.Projects) > 0 || len(state.Tabs) == 0 {
		return state.Projects
	}
	ids := make([]string, 0, len(state.Tabs))
	for _, t := range state.Tabs {
		ids = append(ids, t.ID)
	}
	return []ProjectInfo{{
		ID:        interimProjectIDFor(dest),
		Name:      interimProjectName,
		TabIDs:    ids,
		ActiveTab: state.ActiveTab,
	}}
}

// interimProjectIDFor qualifies the synthetic project's ID with the
// destination it was synthesised for. Two project-less daemons would otherwise
// both be called proj-interim, and indexOfProject resolves by ID alone — so
// focusing the second one's project would hand focus straight back to the
// first on its next broadcast, which is exactly the focus steal the
// per-destination scoping exists to prevent.
//
// An empty dest keeps the bare constant byte-identically: that is the local
// daemon, it is the ID every existing client already holds, and changing it
// there would strand the client's own tabs on the first broadcast.
func interimProjectIDFor(dest string) string {
	if dest == "" {
		return interimProjectID
	}
	return interimProjectID + "@" + dest
}

// indexOfTab returns the ordinal of the tab with the given ID, or 0 when it is
// absent — the active-tab pointer must always land on a real tab, and a
// project whose remembered tab the daemon has since dropped falls back to its
// first one rather than to an index that renders nothing.
func indexOfTab(tabs []*TabModel, id string) int {
	for i, t := range tabs {
		if t.ID == id {
			return i
		}
	}
	return 0
}

// indexOfProject is indexOfTab for the project list, with the same
// always-somewhere-valid contract.
func indexOfProject(projects []*ProjectModel, id string) int {
	for i, p := range projects {
		if p.ID == id {
			return i
		}
	}
	return 0
}

// interimProject returns the project the tab WRITERS below target: the active
// project when there is one, otherwise a freshly created synthetic one. It
// also normalises activeProject, so a Model built directly by a test (or a
// client at startup, before the first broadcast) is a legal write target.
//
// Workspace broadcasts no longer come through here — applyWorkspaceState
// parses the daemon's real projects and rebuilds each one's tabs in place. The
// synthetic project survives for the paths that still have no project to name:
// the pre-attach client, the ~46 tests that build a Model directly, and
// broadcastProjects' fallback for a daemon that sent tabs without projects.
//
// Resolving through cur() rather than indexing projects[0] keeps the writers
// and the readers on the SAME project. With one project the two are the same
// slot; the day they are not, a writer that hardcoded [0] would silently edit
// a project nobody is looking at.
func (m *Model) interimProject() *ProjectModel {
	if p := m.cur(); p != nil {
		return p
	}
	if len(m.projects) == 0 {
		m.projects = []*ProjectModel{{ID: interimProjectID, Name: interimProjectName}}
	}
	m.activeProject = 0
	return m.projects[0]
}

// setTabs replaces the interim project's tab list.
func (m *Model) setTabs(tabs []*TabModel) { m.interimProject().tabs = tabs }

// appendTab adds tabs to the interim project.
func (m *Model) appendTab(tabs ...*TabModel) {
	p := m.interimProject()
	p.tabs = append(p.tabs, tabs...)
}

// setActiveTabIdx moves the active project's tab ordinal.
func (m *Model) setActiveTabIdx(i int) { m.interimProject().activeTab = i }
