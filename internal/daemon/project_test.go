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

// Review finding 1: DestroyProject used to repair sm.activeTab from the
// global tabOrder independently of sm.activeProject, so the two could name
// tabs from different projects. Destroying the active project must leave
// ActiveTabID() naming a tab that actually belongs to ActiveProject().
//
// This needs THREE projects, not two: with only two, the sole survivor of a
// destroy necessarily owns every remaining tab (there is nowhere else for
// them to belong), so global tabOrder[0] and the survivor coincide by
// construction and the bug can't surface. The mismatch needs projectOrder[0]
// (project-creation order) and tabOrder[0] (tab-creation order, across all
// projects) to name DIFFERENT surviving projects — which needs a project B
// whose tab was created before survivor A's tab, while A still sorts first
// in projectOrder.
func TestDestroyProjectActiveTabStaysWithinActiveProject(t *testing.T) {
	sm := NewSessionManager(1024)
	a := sm.CreateProject("A", "/tmp/a") // projectOrder[0]
	b := sm.CreateProject("B", "/tmp/b")
	tabB1 := sm.CreateTabInProject(b.ID, "B1") // tabOrder[0] — created before A's tab
	tabA1 := sm.CreateTabInProject(a.ID, "A1")
	c := sm.CreateProject("C", "/tmp/c")
	tabC1 := sm.CreateTabInProject(c.ID, "C1")

	// Make C both the active project and the holder of the active tab, so
	// destroying it forces the repair path on both activeProject and
	// activeTab.
	if !sm.SwitchProject(c.ID) {
		t.Fatal("SwitchProject(c) = false")
	}
	sm.SwitchTab(tabC1.ID)

	sm.DestroyProject(c.ID)

	active := sm.ActiveProject()
	if active != a.ID {
		t.Fatalf("ActiveProject() = %q, want %q (first remaining project)", active, a.ID)
	}
	got := sm.ActiveTabID()
	if got != tabA1.ID {
		t.Fatalf("ActiveTabID() = %q, want %q (A's own active tab) — "+
			"got tabOrder[0]'s owner instead of ActiveProject()'s own tab", got, tabA1.ID)
	}

	var belongs bool
	for _, pr := range sm.Projects() {
		if pr.ID != active {
			continue
		}
		for _, tid := range pr.TabIDs {
			if tid == got {
				belongs = true
			}
		}
	}
	if !belongs {
		t.Fatalf("ActiveTabID() %q does not belong to ActiveProject() %q", got, active)
	}
	_ = tabB1
}

// Review finding 2: createTabLocked bootstrapped correctly for an empty
// projectID but, for a non-empty ID absent from sm.projects, registered the
// tab to nothing — invisible to a client that builds tabs only from project
// TabIDs. Reachable when a client races CreateTabInProject against another
// connection's DestroyProject. An unknown projectID must resolve the same
// way an empty one does.
func TestCreateTabInProjectFallsBackForUnknownProjectID(t *testing.T) {
	sm := NewSessionManager(1024)
	sm.CreateProject("alpha", "/tmp/a")

	tab := sm.CreateTabInProject("proj-does-not-exist", "Shell")

	projects := sm.Projects()
	idx := -1
	for i, p := range projects {
		if p.ID == tab.ProjectID {
			idx = i
			break
		}
	}
	if idx == -1 {
		t.Fatalf("tab.ProjectID = %q names no project in Projects() = %v", tab.ProjectID, projects)
	}
	owner := projects[idx]
	found := false
	for _, tid := range owner.TabIDs {
		if tid == tab.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("project %q TabIDs = %v, want to contain %s", owner.ID, owner.TabIDs, tab.ID)
	}
}
