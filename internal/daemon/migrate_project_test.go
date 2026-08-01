package daemon

import "testing"

func TestMigrateCreatesDefaultProjectForLegacyState(t *testing.T) {
	state := map[string]any{
		"tabs": []any{
			map[string]any{"id": "tab-aaa", "name": "Shell", "panes": []any{"pane-1"}},
			map[string]any{"id": "tab-bbb", "name": "Logs", "panes": []any{"pane-2"}},
		},
		"active_tab": "tab-aaa",
	}

	migrateToDefaultProject(state)

	projects, _ := state["projects"].([]any)
	if len(projects) != 1 {
		t.Fatalf("projects = %d, want 1 (Default)", len(projects))
	}
	def, _ := projects[0].(map[string]any)
	if def["name"] != "Default" {
		t.Fatalf("name = %v, want Default", def["name"])
	}
	tabIDs, _ := def["tab_ids"].([]any)
	if len(tabIDs) != 2 || tabIDs[0] != "tab-aaa" || tabIDs[1] != "tab-bbb" {
		t.Fatalf("tab_ids = %v, want both tabs in original order", tabIDs)
	}
	if def["active_tab"] != "tab-aaa" {
		t.Fatalf("active_tab = %v", def["active_tab"])
	}
	if state["active_project"] != def["id"] {
		t.Fatalf("active_project = %v, want %v", state["active_project"], def["id"])
	}
	for _, raw := range state["tabs"].([]any) {
		tab, _ := raw.(map[string]any)
		if tab["project_id"] != def["id"] {
			t.Fatalf("tab %v not stamped", tab["id"])
		}
	}
}

func TestMigrateIsNoopWhenProjectsExist(t *testing.T) {
	state := map[string]any{
		"projects": []any{map[string]any{"id": "proj-x", "name": "quil", "tab_ids": []any{"tab-aaa"}}},
		"tabs":     []any{map[string]any{"id": "tab-aaa", "project_id": "proj-x"}},
	}
	migrateToDefaultProject(state)
	projects, _ := state["projects"].([]any)
	first, _ := projects[0].(map[string]any)
	if len(projects) != 1 || first["name"] != "quil" {
		t.Fatalf("migration ran on state that already had projects: %v", projects)
	}
}

func TestMigrateToleratesEmptyState(t *testing.T) {
	state := map[string]any{}
	migrateToDefaultProject(state)
	if _, ok := state["projects"]; ok {
		t.Fatal("a workspace with no tabs needs no Default project")
	}
}

func TestBroadcastStateCarriesProjects(t *testing.T) {
	d := newTestDaemon(t)
	p := d.session.CreateProject("alpha", t.TempDir())
	d.session.CreateTabInProject(p.ID, "Shell")

	state := d.buildWorkspaceState()

	projects, _ := state["projects"].([]any)
	if len(projects) != 1 {
		t.Fatal("the LIVE broadcast must carry projects, not just the disk snapshot")
	}
}

// TestRestoreWorkspace_MigratesLegacyStateAndStampsTabs is a round-trip
// regression guard for the restore-time migration path: a workspace snapshot
// written before projects existed (RestoreTab bypasses CreateProject, so the
// resulting snapshot carries no project data) must come back from
// restoreWorkspace with exactly one "Default" project owning the tab, and the
// rebuilt in-memory *Tab must carry that project's ID. A restored tab with an
// empty ProjectID would make DestroyTab's project de-registration a silent
// no-op — see project.go.
func TestRestoreWorkspace_MigratesLegacyStateAndStampsTabs(t *testing.T) {
	quilHome := t.TempDir()

	d1 := newTestDaemonInDir(t, quilHome)
	d1.session.RestoreTab(
		&Tab{ID: "tab-00000030", Name: "Legacy", Panes: []string{"pane-00000030"}},
		[]*Pane{{ID: "pane-00000030", TabID: "tab-00000030", Type: "terminal"}},
	)
	d1.snapshot()

	d2 := newTestDaemonInDir(t, quilHome)
	if err := d2.restoreWorkspace(); err != nil {
		t.Fatalf("restoreWorkspace: %v", err)
	}

	projects := d2.session.Projects()
	if len(projects) != 1 {
		t.Fatalf("projects after restore = %d, want 1 (Default)", len(projects))
	}
	def := projects[0]
	if def.Name != "Default" {
		t.Errorf("migrated project name = %q, want Default", def.Name)
	}
	if len(def.TabIDs) != 1 || def.TabIDs[0] != "tab-00000030" {
		t.Errorf("migrated project TabIDs = %v, want [tab-00000030]", def.TabIDs)
	}
	if d2.session.ActiveProject() != def.ID {
		t.Errorf("ActiveProject() = %q, want %q", d2.session.ActiveProject(), def.ID)
	}

	tab := d2.session.Tab("tab-00000030")
	if tab == nil {
		t.Fatalf("restored tab not found")
	}
	if tab.ProjectID != def.ID {
		t.Errorf("restored Tab.ProjectID = %q, want %q", tab.ProjectID, def.ID)
	}
}
