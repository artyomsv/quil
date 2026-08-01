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
