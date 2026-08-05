package daemon

import "testing"

// The Bootstrap flag is what lets a client tell "a project the user named"
// from "a project that exists because a tab needed a home". The client adopts
// the second kind — naming a project on a host renames it in place, so the
// host's tabs end up under that name instead of beside it as a Default nobody
// asked for. Get the flag wrong and the client either adopts a real project or
// leaves a stray one.
func TestCreateTab_BootstrapProjectIsMarked(t *testing.T) {
	sm := NewSessionManager(100)

	sm.CreateTab("Shell")

	projects := sm.Projects()
	if len(projects) != 1 {
		t.Fatalf("projects = %d, want the one bootstrapped for the tab", len(projects))
	}
	if !projects[0].Bootstrap {
		t.Error("the project invented for a homeless tab is not marked Bootstrap, " +
			"so a client cannot tell it from one the user named and will leave it " +
			"beside the project they create")
	}
}

// A project the user asks for is theirs from the start.
func TestCreateProject_IsNotBootstrap(t *testing.T) {
	sm := NewSessionManager(100)

	p := sm.CreateProject("cluster-management", "/srv/cluster")

	if p.Bootstrap {
		t.Error("a project created by name is marked Bootstrap, so the next create " +
			"would rename it out from under the user")
	}
}

// Naming it is what makes it the user's. Without this a bootstrap project
// adopted once stays adoptable, and the NEXT create renames the work the user
// just named.
func TestUpdateProject_ClearsBootstrap(t *testing.T) {
	sm := NewSessionManager(100)
	sm.CreateTab("Shell")
	projects := sm.Projects()
	id := projects[0].ID

	if !sm.UpdateProject(id, "cluster-management", "/srv/cluster", false) {
		t.Fatal("update failed")
	}

	after := sm.Projects()
	if after[0].Bootstrap {
		t.Error("still Bootstrap after being named — the next create on this host " +
			"would adopt it again and rename the user's project")
	}
}

// The flag has to survive a daemon restart, or an un-adopted Default becomes a
// real project the moment the daemon bounces — and the host is then permanently
// occupied by a row nobody created.
func TestParseRestoredProjects_KeepsBootstrap(t *testing.T) {
	raw := []any{
		map[string]any{
			"id": "proj-boot", "name": "Default", "root_dir": "/home/a",
			"tab_ids": []any{"tab-1"}, "active_tab": "tab-1", "bootstrap": true,
		},
		map[string]any{
			"id": "proj-named", "name": "cluster-management", "root_dir": "/srv",
			"tab_ids": []any{"tab-2"}, "active_tab": "tab-2",
		},
	}

	got := parseRestoredProjects(raw)
	if len(got) != 2 {
		t.Fatalf("parsed %d projects, want 2", len(got))
	}
	if !got[0].Bootstrap {
		t.Error("the bootstrap flag was dropped on restore")
	}
	if got[1].Bootstrap {
		t.Error("a project with no bootstrap key came back marked; absence must " +
			"mean the user's, or every pre-flag project becomes adoptable")
	}
}

// A pre-projects workspace is wrapped in a project so its tabs stay reachable —
// the user never named that either, so the first project they DO name on that
// host should take those tabs over.
func TestMigrateToDefaultProject_MarksItBootstrap(t *testing.T) {
	state := map[string]any{
		"tabs":       []any{map[string]any{"id": "tab-1"}},
		"active_tab": "tab-1",
	}

	migrateToDefaultProject(state)

	projects, ok := state["projects"].([]any)
	if !ok || len(projects) != 1 {
		t.Fatalf("projects = %v, want one migrated project", state["projects"])
	}
	p := projects[0].(map[string]any)
	if b, _ := p["bootstrap"].(bool); !b {
		t.Error("the migrated project is not marked Bootstrap, so a host upgraded " +
			"from a pre-projects daemon keeps a Default beside whatever the user names")
	}
}

// Adopting is compare-and-swap on the far side.
//
// The client decides to adopt from its OWN snapshot, so two clients driving the
// same host both see the bootstrap project and both send a rename. Without the
// condition the second silently renames the project the first just named —
// nobody is told, and the host ends up called whatever arrived last.
func TestUpdateProject_AdoptRefusesAProjectSomebodyNamedFirst(t *testing.T) {
	sm := NewSessionManager(100)
	sm.CreateTab("Shell")
	id := sm.Projects()[0].ID

	// Client A adopts.
	if !sm.UpdateProject(id, "cluster-management", "/srv/cluster", true) {
		t.Fatal("the first adopt was refused")
	}
	// Client B adopts the same project, from a snapshot taken before A's.
	if sm.UpdateProject(id, "infra", "/srv/infra", true) {
		t.Error("the second adopt applied — it renamed the project the first " +
			"client had just named, and neither user was told")
	}
	if got := sm.Projects()[0].Name; got != "cluster-management" {
		t.Errorf("name = %q, want the first client's", got)
	}
}

// An ordinary rename is unconditional. The guard is for adoption, and applying
// it to every update would break renaming a project that is — correctly — no
// longer a bootstrap.
func TestUpdateProject_PlainRenameIsUnconditional(t *testing.T) {
	sm := NewSessionManager(100)
	p := sm.CreateProject("cluster-management", "/srv/cluster")

	if !sm.UpdateProject(p.ID, "infra", "/srv/infra", false) {
		t.Fatal("a plain rename of a named project was refused")
	}
	if got := sm.Projects()[0].Name; got != "infra" {
		t.Errorf("name = %q, want infra", got)
	}
}
