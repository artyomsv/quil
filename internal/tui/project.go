package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// interimProjectID / interimProjectName name the single synthetic project the
// client folds every broadcast tab into until WorkspaceStateMsg carries real
// projects. They are deliberately constants rather than literals so the site
// Task 7 must replace is greppable.
// interimProjectName does NOT read like a real project's, deliberately. It
// used to be "Default", which is also what a daemon calls the project it
// bootstraps — so a host running a build with no project support was
// indistinguishable from one that simply had a project by that name. Every
// pane on it worked, the directory browser worked, and only Create and Rename
// were silently dropped by a daemon with no case for those messages, which
// cost an evening to recognise. The label now says which daemon it belongs to
// and that it is not one of that daemon's own.
const (
	interimProjectID   = "proj-interim"
	interimProjectName = "(no projects)"
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
	// Bootstrap mirrors the daemon's flag: this project exists because a tab
	// needed a home, not because anyone named it. Naming a project on a host
	// whose only project is this one renames it in place.
	Bootstrap bool

	tabs      []*TabModel
	activeTab int
}

// switchProject moves the client to project i, tells the OWNING daemon so its
// own activeProject stays in step, and resyncs PTY geometry.
//
// The daemon notification is not bookkeeping: CreateTab files a new tab
// against whatever project the daemon last had marked active, so without
// MsgSwitchProject every Ctrl+T after a client-side switch lands in the
// project that happened to be active when the daemon started.
//
// The resize is because the incoming project's panes were last sized under
// whatever geometry was current when it went to the background — a window
// resize while it was hidden never reached them.
func (m *Model) switchProject(i int) tea.Cmd {
	if i < 0 || i >= len(m.projects) || i == m.activeProject {
		return nil
	}
	// Each project carries its own activeTab, so switching projects changes
	// the active tab implicitly — which makes this one of the callers
	// exitNotesModeInPlace's contract names ("callers that are about to
	// change that tab must invoke this FIRST"), exactly like switchTab.
	// Without it the editor stays open bound to the OUTGOING project's pane,
	// painted beside the incoming project's panes, still claiming
	// notesPanelWidth() and still suppressing the split-border and scrollbar
	// hit tests. applyWorkspaceState's reconciliation does not rescue it: the
	// bound pane is alive in a background project, so it re-syncs there.
	if m.notesMode && m.notesEditor != nil {
		m.exitNotesModeInPlace()
	}
	// Recorded by ID: the bounce target has to survive a workspace
	// reconciliation that reorders the list, and an index does not.
	m.prevProject = ""
	if p := m.cur(); p != nil {
		m.prevProject = p.ID
	}
	m.activeProject = i
	// Before the send: syncActiveDest is what makes an UNSTAMPED message
	// resolve to the incoming project's daemon rather than the outgoing
	// one's, and resizeAllPanes below is about to fan out across all of them.
	m.syncActiveDest()

	p := m.projects[i]
	msg, _ := ipc.NewMessage(ipc.MsgSwitchProject, ipc.SwitchProjectPayload{ProjectID: p.ID})
	// Through sendForDest, not a raw Origin assignment: a LOCAL project's
	// Dest is "", which routeDest reads as UNSTAMPED — so a hand-stamped
	// local switch would be re-aimed at whatever the router's current dest
	// happens to be. stampDest maps "" to the explicit destLocal sentinel.
	m.sendForDest(p.Dest, msg)

	return m.resizeAllPanes()
}

// activateSidebarRow applies a left click on an actionable sidebar row.
func (m Model) activateSidebarRow(kind string, index int) (tea.Model, tea.Cmd) {
	switch kind {
	case sidebarRowProject:
		// Sequenced, not `return m, m.switchProject(index)`: switchProject
		// mutates m through a pointer receiver, and Go does not order a
		// plain operand against a call in the same return statement.
		cmd := m.switchProject(index)
		return m, cmd
	case sidebarRowPane:
		// The row carries its tab, which sidebarHit's (kind, index) pair
		// cannot: re-resolve it from the same rows the click was tested
		// against — same width AND same height cap — rather than re-deriving
		// the walk order a second time.
		for _, row := range m.sidebarVisibleRows(m.projectSidebarWidth(), m.sidebarContentHeight()) {
			if row.kind == sidebarRowPane && row.index == index {
				return m.focusSidebarPane(row.tabIdx, row.paneID)
			}
		}
	}
	return m, nil
}

// focusSidebarPane raises a pane clicked in the sidebar: its tab becomes
// active (through switchTab, so the daemon's active-tab bookkeeping and the
// notes teardown run exactly as they do for a tab-bar click) and the pane
// itself takes focus within it.
func (m Model) focusSidebarPane(tabIdx int, paneID string) (tea.Model, tea.Cmd) {
	tabs := m.curTabs()
	if tabIdx < 0 || tabIdx >= len(tabs) {
		return m, nil
	}
	var cmd tea.Cmd
	if tabIdx != m.activeTabIdx() {
		cmd = m.switchTab(tabIdx)
	}
	tab := tabs[tabIdx]
	if tab.Root == nil {
		return m, cmd
	}
	for _, pane := range tab.Leaves() {
		if pane.ID != paneID {
			continue
		}
		if old := tab.ActivePaneModel(); old != nil {
			old.Active = false
		}
		pane.Active = true
		tab.ActivePane = pane.ID
		break
	}
	return m, cmd
}

// renderEmptyTabArea fills the pane area for a frame with no active tab. It is
// deliberately honest about which of the two reasons applies: a project with no
// tabs is a state the user can act on and is told how to, while a client that
// has not yet heard from any daemon is still connecting and has nothing to
// offer. Sized to exactly w×h so the project sidebar and the notification strip
// composite against it the same way they do against a real tab.
func (m Model) renderEmptyTabArea(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	msg := "Connecting to quild…"
	if p := m.cur(); p != nil {
		msg = "No tabs in " + sanitizeRemoteText(p.displayName()) + "\n\nCtrl+T opens one"
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, dialogSubtle.Render(msg))
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

// mergeProjects folds one destination's freshly rebuilt project list back into
// the client's, IN PLACE: every surviving project keeps the slot it already
// occupied, projects from other destinations are untouched, and only genuinely
// new ones are appended.
//
// Appending the rebuilt list wholesale (kept + rebuilt) is what this replaces,
// and it made the ORDER a function of which daemon broadcast last: with a local
// daemon beside one remote, the sidebar flipped on every alternating broadcast
// — and pane/tab lifecycle, overlay exit and a mouse-mode change all broadcast,
// so it flipped often enough to see. The order is also read as identity in two
// places (the picker renders m.projects verbatim; Alt+O resolves a project by
// it before Task-17's ID rewrite), so an unstable one silently moves the user
// between daemons.
//
// A project this broadcast no longer describes is DROPPED — the broadcast is
// the full state of that daemon, so its absence is the daemon saying it is gone.
func mergeProjects(current, rebuilt []*ProjectModel, dest string) []*ProjectModel {
	byID := make(map[string]*ProjectModel, len(rebuilt))
	for _, p := range rebuilt {
		byID[p.ID] = p
	}

	out := make([]*ProjectModel, 0, len(current)+len(rebuilt))
	placed := make(map[string]bool, len(rebuilt))
	for _, p := range current {
		if p == nil {
			continue
		}
		if p.Dest != dest {
			out = append(out, p)
			continue
		}
		if np, ok := byID[p.ID]; ok {
			out = append(out, np)
			placed[p.ID] = true
		}
	}
	for _, p := range rebuilt {
		if !placed[p.ID] {
			out = append(out, p)
		}
	}
	return out
}

// projectsOnDest lists every project this client currently holds for dest, in
// sidebar order.
//
// It replaced a hostProjectState helper that answered "is there one to adopt,
// is there one that blocks" — two booleans that could describe a host holding
// three projects no better than a host holding one, which is exactly the state
// the create-time guard leaves behind once a host predates it. The COUNT is
// what the caller needs, so the count is what this returns.
func (m *Model) projectsOnDest(dest string) []*ProjectModel {
	var out []*ProjectModel
	for _, p := range m.projects {
		if p.Dest == dest {
			out = append(out, p)
		}
	}
	return out
}

// hostLabel renders a destination for a message. Empty means the local daemon,
// which has no host to name.
func hostLabel(dest string) string {
	if dest == "" {
		return "this machine"
	}
	return dest
}

// projectNamedOnDest finds a project with this name on this destination, or
// nil. Names are compared case-insensitively after trimming, because the
// sidebar renders them as typed and "Cluster" beside "cluster" is the same
// indistinguishable pair as two exact duplicates.
//
// Scoped to ONE destination on purpose: the same project name on a laptop and
// on a build host is ordinary — the row carries the host, so those two are
// told apart on sight, which is exactly what two on one host are not.
// excludeID is the project being renamed, which must not collide with itself —
// submitting a rename that changes only the root directory is ordinary.
func (m *Model) projectNamedOnDest(name, dest, excludeID string) *ProjectModel {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, p := range m.projects {
		if p.ID == excludeID {
			continue
		}
		if p.Dest == dest && strings.ToLower(strings.TrimSpace(p.Name)) == want {
			return p
		}
	}
	return nil
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

// projectByID returns the project with the given ID, or nil. Unlike
// indexOfProject (an always-somewhere-valid fallback for an active-project
// pointer), a caller resolving an ID from a confirm dialog or the sidebar
// context menu needs to know when the project is simply gone — the item
// destroyed itself, or a workspace reconciliation removed it, while the
// dialog sat open.
func (m *Model) projectByID(id string) *ProjectModel {
	for _, p := range m.projects {
		if p.ID == id {
			return p
		}
	}
	return nil
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

// isSyntheticProject reports the placeholder the client invents for a daemon
// that has reported no projects yet (interimProjectIDFor). Its ID exists only
// in this process, so a message naming it reaches a daemon that has never
// heard of it and UpdateProject's map lookup silently misses — the dialog
// accepts a new name and nothing changes. Callers refuse the action instead.
func isSyntheticProject(id string) bool {
	return id == interimProjectID || strings.HasPrefix(id, interimProjectID+"@")
}

// destSupportsProjects reports whether a destination's daemon understands the
// project messages. False means every create, rename and destroy aimed at it
// is accepted by this client and then dropped by a daemon with no case for it.
//
// The test is BEHAVIOURAL, not a version comparison: a daemon that supports
// projects always reports at least one, because it bootstraps a Default rather
// than run with none — so the client synthesising a placeholder for it is
// exactly the observation "this daemon told us about no projects". A version
// number would have to be maintained against a release that has not happened
// yet, and would answer wrongly for any build in between.
//
// Unknown destinations answer TRUE. A host still connecting has no projects
// yet either, and refusing there would block the ordinary case to catch the
// rare one.
func (m *Model) destSupportsProjects(dest string) bool {
	for _, p := range m.projects {
		if p.Dest == dest && isSyntheticProject(p.ID) {
			return false
		}
	}
	return true
}
