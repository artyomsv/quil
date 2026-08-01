package daemon

import (
	"testing"
	"time"
)

func TestCreateProjectAppendsAndActivatesFirst(t *testing.T) {
	sm := NewSessionManager(1024)
	a := sm.CreateProject("alpha", "/tmp/a")
	b := sm.CreateProject("beta", "/tmp/b")

	got := sm.Projects()
	if len(got) != 2 || got[0].ID != a.ID || got[1].ID != b.ID {
		t.Fatalf("order = %v, want [alpha beta]", got)
	}
	if sm.ActiveProject() != a.ID {
		t.Fatalf("first project should become active, got %q", sm.ActiveProject())
	}
}

func TestDestroyProjectReturnsPanesAndClearsActiveTab(t *testing.T) {
	sm := NewSessionManager(1024)
	p := sm.CreateProject("alpha", "/tmp/a")
	tab := sm.CreateTabInProject(p.ID, "Shell")
	pane, err := sm.CreatePane(tab.ID, "/tmp/a")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	panes := sm.DestroyProject(p.ID)

	if len(panes) != 1 || panes[0].ID != pane.ID {
		t.Fatalf("DestroyProject = %v, want the one pane", panes)
	}
	if len(sm.Projects()) != 0 {
		t.Fatal("project survived destroy")
	}
	if at := sm.ActiveTabID(); at == tab.ID {
		t.Fatalf("activeTab still points at a destroyed tab (%s)", at)
	}
}

func TestDestroyTabDeregistersFromItsProject(t *testing.T) {
	sm := NewSessionManager(1024)
	p := sm.CreateProject("alpha", "/tmp/a")
	keep := sm.CreateTabInProject(p.ID, "Keep")
	drop := sm.CreateTabInProject(p.ID, "Drop")

	sm.DestroyTab(drop.ID)

	projects := sm.Projects()
	if len(projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(projects))
	}
	ids := projects[0].TabIDs
	if len(ids) != 1 || ids[0] != keep.ID {
		t.Fatalf("TabIDs = %v, want only %s — a dangling ID gets persisted and broadcast",
			ids, keep.ID)
	}
}

func TestFreshSessionBootstrapsADefaultProject(t *testing.T) {
	// Fresh install: no snapshot, no migration, nobody has called
	// CreateProject. handleAttach creates the default Shell tab in exactly
	// this state.
	sm := NewSessionManager(1024)

	tab := sm.CreateTab("Shell")

	projects := sm.Projects()
	if len(projects) != 1 {
		t.Fatalf("projects = %d, want 1 — a tab with no project is invisible "+
			"to the client, which builds tabs only from project TabIDs", len(projects))
	}
	if tab.ProjectID != projects[0].ID {
		t.Fatalf("tab.ProjectID = %q, want %q", tab.ProjectID, projects[0].ID)
	}
	if len(projects[0].TabIDs) != 1 || projects[0].TabIDs[0] != tab.ID {
		t.Fatalf("TabIDs = %v, want [%s]", projects[0].TabIDs, tab.ID)
	}
}

func TestSwitchTabRecordsPerProjectActiveTab(t *testing.T) {
	sm := NewSessionManager(1024)
	p := sm.CreateProject("alpha", "/tmp/a")
	first := sm.CreateTabInProject(p.ID, "One")
	second := sm.CreateTabInProject(p.ID, "Two")

	sm.SwitchTab(second.ID)

	got := sm.Projects()[0]
	if got.ActiveTab != second.ID {
		t.Fatalf("Project.ActiveTab = %q, want %q — without this the client's "+
			"tab selection snaps back to tab 1 on every broadcast",
			got.ActiveTab, second.ID)
	}
	_ = first
}

func TestCreateTabInProjectSeedsActiveTabWhenEmpty(t *testing.T) {
	sm := NewSessionManager(1024)
	p := sm.CreateProject("alpha", "/tmp/a")
	tab := sm.CreateTabInProject(p.ID, "One")

	if sm.Projects()[0].ActiveTab != tab.ID {
		t.Fatal("the first tab in a project must become its active tab")
	}
}

func TestCreateTabDoesNotDeadlock(t *testing.T) {
	sm := NewSessionManager(1024)
	sm.CreateProject("alpha", "/tmp/a")

	done := make(chan struct{})
	go func() {
		sm.CreateTab("Shell") // delegates to the active project
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("CreateTab deadlocked: sm.mu is not reentrant")
	}
}
