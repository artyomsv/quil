package tui

import "testing"

// oneProject wraps tabs in a single project, mirroring the shape every
// pre-project test used to build with a bare `tabs:` field. Tests that only
// ever had one flat tab list want exactly this.
func oneProject(tabs ...*TabModel) []*ProjectModel {
	return []*ProjectModel{{ID: interimProjectID, Name: interimProjectName, tabs: tabs}}
}

func TestCurTabsReturnsOnlyActiveProject(t *testing.T) {
	m := Model{
		projects: []*ProjectModel{
			{ID: "proj-a", tabs: []*TabModel{NewTabModel("tab-1", "One"), NewTabModel("tab-2", "Two")}},
			{ID: "proj-b", tabs: []*TabModel{NewTabModel("tab-3", "Three")}},
		},
		activeProject: 1,
	}
	got := m.curTabs()
	if len(got) != 1 || got[0].ID != "tab-3" {
		t.Fatalf("curTabs = %v, want only proj-b's tab", got)
	}
	if len(m.allTabs()) != 3 {
		t.Fatalf("allTabs = %d, want 3", len(m.allTabs()))
	}
}

func TestActiveTabModelRestoresPerProjectTab(t *testing.T) {
	m := Model{
		projects: []*ProjectModel{
			{ID: "proj-a", tabs: []*TabModel{NewTabModel("tab-1", "One"), NewTabModel("tab-2", "Two")}, activeTab: 1},
			{ID: "proj-b", tabs: []*TabModel{NewTabModel("tab-3", "Three")}},
		},
		activeProject: 0,
	}
	if m.activeTabModel().ID != "tab-2" {
		t.Fatalf("activeTabModel = %s, want tab-2 (the tab proj-a was left on)",
			m.activeTabModel().ID)
	}
	m.activeProject = 1
	if m.activeTabModel().ID != "tab-3" {
		t.Fatalf("activeTabModel = %s, want tab-3", m.activeTabModel().ID)
	}
}

func TestAccessorsAreNilSafeOnEmptyModel(t *testing.T) {
	var m Model
	if m.cur() != nil || m.activeTabModel() != nil {
		t.Fatal("accessors must tolerate a Model with no projects")
	}
	if len(m.curTabs()) != 0 {
		t.Fatal("curTabs on an empty Model must be empty, not panic")
	}
}

func TestProjectOfFindsOwningProject(t *testing.T) {
	m := Model{
		projects: []*ProjectModel{
			{ID: "proj-a", tabs: []*TabModel{NewTabModel("tab-1", "One")}},
			{ID: "proj-b", tabs: []*TabModel{NewTabModel("tab-3", "Three")}},
		},
	}
	if p := m.projectOf("tab-3"); p == nil || p.ID != "proj-b" {
		t.Fatalf("projectOf(tab-3) = %v, want proj-b", p)
	}
	if p := m.projectOf("tab-missing"); p != nil {
		t.Fatalf("projectOf(tab-missing) = %v, want nil", p)
	}
}

// The interim writers must materialise a project on a zero Model — startup and
// ~46 direct-construction tests both hit that state before any broadcast.
func TestInterimWritersMaterialiseAProject(t *testing.T) {
	var m Model
	m.appendTab(NewTabModel("tab-1", "One"))
	m.appendTab(NewTabModel("tab-2", "Two"))
	if len(m.projects) != 1 {
		t.Fatalf("projects = %d, want 1 synthetic project", len(m.projects))
	}
	if got := m.projects[0].ID; got != interimProjectID {
		t.Fatalf("interim project ID = %q, want %q", got, interimProjectID)
	}
	m.setActiveTabIdx(1)
	if m.activeTabModel().ID != "tab-2" {
		t.Fatalf("activeTabModel = %s, want tab-2", m.activeTabModel().ID)
	}
	m.setTabs(nil)
	if len(m.curTabs()) != 0 {
		t.Fatalf("curTabs after setTabs(nil) = %d, want 0", len(m.curTabs()))
	}
}

// A pre-existing project is reused rather than shadowed by a second synthetic
// one — otherwise every workspace broadcast would strand the previous tabs.
func TestInterimProjectReusesProjectZero(t *testing.T) {
	m := Model{projects: []*ProjectModel{{ID: "proj-real", Name: "Real"}}}
	m.appendTab(NewTabModel("tab-1", "One"))
	if len(m.projects) != 1 || m.projects[0].ID != "proj-real" {
		t.Fatalf("projects = %v, want the existing proj-real reused", m.projects)
	}
	if len(m.curTabs()) != 1 {
		t.Fatalf("curTabs = %d, want 1", len(m.curTabs()))
	}
}

// The wire keys are the daemon's (workspaceStateFromSnapshot): a project list
// of {id, name, root_dir, tab_ids, active_tab} plus a per-tab project_id. A
// key that drifts apart from that shape does not error anywhere — it parses to
// an empty project list, and every tab silently disappears from the client.
func TestParseWorkspaceStateReadsProjects(t *testing.T) {
	state := parseWorkspaceState(map[string]any{
		"active_tab":     "tab-1",
		"active_project": "proj-a",
		"projects": []any{map[string]any{
			"id": "proj-a", "name": "quil", "root_dir": "/src/quil",
			"tab_ids": []any{"tab-1", "tab-2"}, "active_tab": "tab-2",
		}},
		"tabs": []any{map[string]any{"id": "tab-1", "name": "One", "project_id": "proj-a"}},
	})

	if state.ActiveProject != "proj-a" {
		t.Errorf("ActiveProject = %q, want proj-a", state.ActiveProject)
	}
	if len(state.Projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(state.Projects))
	}
	p := state.Projects[0]
	if p.ID != "proj-a" || p.Name != "quil" || p.RootDir != "/src/quil" || p.ActiveTab != "tab-2" {
		t.Errorf("project = %+v, want id/name/root_dir/active_tab from the payload", p)
	}
	if len(p.TabIDs) != 2 || p.TabIDs[0] != "tab-1" || p.TabIDs[1] != "tab-2" {
		t.Errorf("TabIDs = %v, want [tab-1 tab-2] in payload order", p.TabIDs)
	}
	if len(state.Tabs) != 1 || state.Tabs[0].ProjectID != "proj-a" {
		t.Errorf("tab ProjectID not parsed: %+v", state.Tabs)
	}
}

func TestApplyWorkspaceStateReplacesOnlyItsOwnDest(t *testing.T) {
	m := Model{projects: []*ProjectModel{
		{ID: "proj-local", Name: "quil", Dest: "", tabs: []*TabModel{NewTabModel("tab-1", "One")}},
		{ID: "proj-gpu", Name: "api", Dest: "gpu01", tabs: []*TabModel{NewTabModel("tab-9", "Nine")}},
	}}

	m.applyWorkspaceState(WorkspaceStateMsg{
		Dest:     "gpu01",
		Projects: []ProjectInfo{{ID: "proj-gpu", Name: "api-renamed", TabIDs: []string{"tab-9"}, ActiveTab: "tab-9"}},
		Tabs:     []TabInfo{{ID: "tab-9", Name: "Nine", ProjectID: "proj-gpu"}},
	}, "gpu01")

	if len(m.projects) != 2 {
		t.Fatalf("projects = %d, want 2 — the local project was clobbered", len(m.projects))
	}
	for _, p := range m.projects {
		switch p.Dest {
		case "":
			if p.Name != "quil" || len(p.tabs) != 1 {
				t.Fatalf("local project changed: %+v", p)
			}
		case "gpu01":
			if p.Name != "api-renamed" {
				t.Fatalf("gpu project not updated: %+v", p)
			}
		}
	}
}

// A rebuild must REUSE the objects the client already holds. A fresh TabModel
// drops the layout tree and a fresh PaneModel drops the VT emulator with its
// scrollback, so a pane would visibly blank on a broadcast that said nothing
// new about it — and the original would leak, never being disposed.
func TestApplyWorkspaceStateReusesTabAndPaneAcrossRebuild(t *testing.T) {
	pane := NewPaneModel("pane-1", 4096)
	tab := NewTabModel("tab-1", "One")
	tab.Root = NewLeaf(pane)
	proj := &ProjectModel{ID: "proj-a", tabs: []*TabModel{tab}}
	m := Model{projects: []*ProjectModel{proj}}

	m.applyWorkspaceState(WorkspaceStateMsg{
		Projects: []ProjectInfo{{ID: "proj-a", Name: "quil", TabIDs: []string{"tab-1"}, ActiveTab: "tab-1"}},
		Tabs:     []TabInfo{{ID: "tab-1", Name: "One", ProjectID: "proj-a", Panes: []string{"pane-1"}}},
		Panes:    []PaneInfo{{ID: "pane-1", TabID: "tab-1", Type: "terminal"}},
	}, "")

	if len(m.projects) != 1 || m.projects[0] != proj {
		t.Fatalf("projects = %+v, want the same ProjectModel reused", m.projects)
	}
	if got := m.curTabs(); len(got) != 1 || got[0] != tab {
		t.Fatalf("tabs = %v, want the same TabModel reused", got)
	}
	if got := tab.Leaves(); len(got) != 1 || got[0] != pane {
		t.Fatalf("leaves = %v, want the same PaneModel reused", got)
	}
}

// A stale TabIDs entry — the project list still names a tab whose own
// project_id has moved on — must not build that tab into both projects: one
// TabModel in two tab bars would fight over a single layout tree.
func TestApplyWorkspaceStateSkipsTabClaimedByAnotherProject(t *testing.T) {
	var m Model
	m.applyWorkspaceState(WorkspaceStateMsg{
		Projects: []ProjectInfo{
			{ID: "proj-a", TabIDs: []string{"tab-1"}},
			{ID: "proj-b", TabIDs: []string{"tab-1"}},
		},
		Tabs: []TabInfo{{ID: "tab-1", Name: "One", ProjectID: "proj-b"}},
	}, "")

	if got := len(m.projects[0].tabs); got != 0 {
		t.Fatalf("proj-a tabs = %d, want 0 — the tab itself says it belongs to proj-b", got)
	}
	if got := m.projects[1].tabs; len(got) != 1 || got[0].ID != "tab-1" {
		t.Fatalf("proj-b tabs = %v, want tab-1", got)
	}
}

func TestApplyWorkspaceStateDropsProjectsRemovedOnThatDest(t *testing.T) {
	m := Model{projects: []*ProjectModel{
		{ID: "proj-a", Dest: "gpu01", tabs: []*TabModel{NewTabModel("tab-1", "One")}},
		{ID: "proj-b", Dest: "gpu01", tabs: []*TabModel{NewTabModel("tab-2", "Two")}},
	}}
	m.applyWorkspaceState(WorkspaceStateMsg{
		Dest:     "gpu01",
		Projects: []ProjectInfo{{ID: "proj-a", TabIDs: []string{"tab-1"}}},
		Tabs:     []TabInfo{{ID: "tab-1", Name: "One", ProjectID: "proj-a"}},
	}, "gpu01")

	if len(m.projects) != 1 || m.projects[0].ID != "proj-a" {
		t.Fatalf("projects = %+v, want only proj-a", m.projects)
	}
}
