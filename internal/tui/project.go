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

// interimProject returns the project every broadcast tab is folded into until
// Task 7 parses real ones: the active project when there is one, otherwise a
// freshly created synthetic one. It also normalises activeProject, so a Model
// built directly by a test (or a client at startup, before the first
// broadcast) is a legal write target.
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
