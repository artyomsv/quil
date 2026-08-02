package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// tabIDsOf returns one project's tab order as the daemon holds it.
func tabIDsOf(t *testing.T, sm *SessionManager, projectID string) []string {
	t.Helper()
	for _, p := range sm.Projects() {
		if p.ID == projectID {
			return p.TabIDs
		}
	}
	t.Fatalf("project %q not found", projectID)
	return nil
}

// globalTabIDs returns sm.Tabs() order — what list_tabs and the snapshot's
// `tabs` array are built from.
func globalTabIDs(t *testing.T, sm *SessionManager) []string {
	t.Helper()
	out := make([]string, 0)
	for _, tab := range sm.Tabs() {
		out = append(out, tab.ID)
	}
	return out
}

// broadcastTabIDs runs the real snapshot builder and digs out one project's
// tab_ids — the field the client rebuilds its tab list from. This is the
// daemon half of the drag round trip: the client half
// (TestApplyWorkspaceStateOrdersTabsByProjectTabIDs, internal/tui) proves the
// client follows tab_ids and not the `tabs` array.
func broadcastTabIDs(t *testing.T, sm *SessionManager, projectID string) []string {
	t.Helper()
	d := New(config.Default())
	activeTab, tabs, panesByTab, projects, activeProject := sm.SnapshotState()
	state := d.workspaceStateFromSnapshot(activeTab, tabs, panesByTab, projects, activeProject, false)
	list, ok := state["projects"].([]any)
	if !ok {
		t.Fatalf("projects = %T, want []any", state["projects"])
	}
	for _, raw := range list {
		p, ok := raw.(map[string]any)
		if !ok || p["id"] != projectID {
			continue
		}
		ids, ok := p["tab_ids"].([]string)
		if !ok {
			t.Fatalf("tab_ids = %T, want []string", p["tab_ids"])
		}
		return ids
	}
	t.Fatalf("project %q missing from the broadcast", projectID)
	return nil
}

// A tab drag has to survive the broadcast it triggers. The client rebuilds
// each project's tab list from projects[].tab_ids, so the reorder must land
// THERE: moving only the global tabOrder left the broadcast carrying the new
// order in `tabs` and the OLD order in `tab_ids`, so the tab snapped back on
// the next tick — and was never persisted either, since the snapshot writes
// tab_ids too.
func TestReorderTabMovesTheOwningProjectsTabIDs(t *testing.T) {
	sm := NewSessionManager(1024)
	p := sm.CreateProject("alpha", "/tmp/a")
	a := sm.CreateTabInProject(p.ID, "A")
	b := sm.CreateTabInProject(p.ID, "B")
	c := sm.CreateTabInProject(p.ID, "C")

	if !sm.ReorderTab(c.ID, 0) {
		t.Fatal("ReorderTab reported no change")
	}

	want := []string{c.ID, a.ID, b.ID}
	if got := tabIDsOf(t, sm, p.ID); !reflect.DeepEqual(got, want) {
		t.Errorf("TabIDs = %v, want %v", got, want)
	}
	if got := broadcastTabIDs(t, sm, p.ID); !reflect.DeepEqual(got, want) {
		t.Errorf("broadcast tab_ids = %v, want %v — the client rebuilds from this", got, want)
	}
	// The global order follows, so list_tabs and the snapshot's `tabs` array
	// agree with what the user sees.
	if got := globalTabIDs(t, sm); !reflect.DeepEqual(got, want) {
		t.Errorf("global tab order = %v, want %v", got, want)
	}
}

// NewIndex is an ordinal within the dragged tab's OWN project — the tab bar
// never shows another project's tabs, so it cannot mean anything else. Applied
// to the global order it would land the tab at an unrelated slot and shove
// other projects' tabs aside; the re-anchoring keeps them put.
func TestReorderTabLeavesOtherProjectsUntouched(t *testing.T) {
	sm := NewSessionManager(1024)
	alpha := sm.CreateProject("alpha", "/tmp/a")
	beta := sm.CreateProject("beta", "/tmp/b")
	a1 := sm.CreateTabInProject(alpha.ID, "A1")
	b1 := sm.CreateTabInProject(beta.ID, "B1")
	a2 := sm.CreateTabInProject(alpha.ID, "A2")
	b2 := sm.CreateTabInProject(beta.ID, "B2")
	// Global creation order: A1 B1 A2 B2.

	// Drag beta's second tab to beta's FIRST slot — a project-relative 0.
	if !sm.ReorderTab(b2.ID, 0) {
		t.Fatal("ReorderTab reported no change")
	}

	if got, want := tabIDsOf(t, sm, beta.ID), []string{b2.ID, b1.ID}; !reflect.DeepEqual(got, want) {
		t.Errorf("beta TabIDs = %v, want %v", got, want)
	}
	if got, want := tabIDsOf(t, sm, alpha.ID), []string{a1.ID, a2.ID}; !reflect.DeepEqual(got, want) {
		t.Errorf("alpha TabIDs = %v, want %v — a foreign drag must not touch them", got, want)
	}
	// b2 is re-anchored next to its new project neighbour; alpha's tabs keep
	// both their relative order and their surroundings.
	if got, want := globalTabIDs(t, sm), []string{a1.ID, b2.ID, b1.ID, a2.ID}; !reflect.DeepEqual(got, want) {
		t.Errorf("global tab order = %v, want %v", got, want)
	}
}

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
	if _, ok := sm.SwitchProject(c.ID); !ok {
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

func TestHandleProjectMessagesMutateState(t *testing.T) {
	d := newTestDaemon(t)
	p := d.session.CreateProject("alpha", t.TempDir())

	rename, _ := ipc.NewMessage(ipc.MsgUpdateProject, ipc.UpdateProjectPayload{
		ProjectID: p.ID, Name: "renamed", RootDir: "/tmp",
	})
	d.handleMessage(nil, rename)

	got := d.session.Projects()
	if len(got) != 1 || got[0].Name != "renamed" {
		t.Fatalf("projects = %v, want one named renamed", got)
	}

	destroy, _ := ipc.NewMessage(ipc.MsgDestroyProject, ipc.DestroyProjectPayload{ProjectID: p.ID})
	d.handleMessage(nil, destroy)

	for _, pr := range d.session.Projects() {
		if pr.ID == p.ID {
			t.Fatal("project survived destroy")
		}
	}
	// Destroying the LAST project leaves a workspace with nothing to render
	// and no tab bar to act on, so the handler bootstraps a replacement — the
	// same always-on invariant that auto-creates a Shell tab when the last one
	// is closed. (This expectation changed with that fix; before it, the
	// client was left on a blank screen.)
	if len(d.session.Projects()) != 1 {
		t.Fatalf("projects = %v, want exactly one bootstrapped replacement", d.session.Projects())
	}
	if len(d.session.Tabs()) != 1 {
		t.Fatalf("tabs = %d, want the replacement project to ship one", len(d.session.Tabs()))
	}
}

// TestHandleSwitchProjectSpawnsTheIncomingProjectsTab: after a lazy restore
// only sm.activeTab's panes are running, and switching PROJECT never reached
// ensureTabSpawned — so a background project's panes stayed Pending
// indefinitely, showing the restore indicator with no process behind them.
// resizeAllPanes cannot rescue that: there is no PTY to resize.
func TestHandleSwitchProjectSpawnsTheIncomingProjectsTab(t *testing.T) {
	d := newTestDaemon(t)
	a := d.session.CreateProject("A", t.TempDir())
	b := d.session.CreateProject("B", t.TempDir())
	tabA := d.session.CreateTabInProject(a.ID, "A1")
	tabB := d.session.CreateTabInProject(b.ID, "B1")

	paneA, err := d.session.CreatePane(tabA.ID, t.TempDir())
	if err != nil {
		t.Fatalf("create pane A: %v", err)
	}
	paneB, err := d.session.CreatePane(tabB.ID, t.TempDir())
	if err != nil {
		t.Fatalf("create pane B: %v", err)
	}
	// The shape a lazy restore leaves behind: everything outside the active
	// tab is deferred.
	paneA.Type, paneB.Type = "terminal", "terminal"
	paneA.Pending, paneB.Pending = true, true

	if _, ok := d.session.SwitchProject(a.ID); !ok {
		t.Fatal("SwitchProject(a) = false")
	}
	d.ensureTabSpawned(tabA.ID)
	if paneB.PTY != nil {
		t.Fatal("setup invariant broken: B's pane must still be Pending before the switch")
	}

	msg, _ := ipc.NewMessage(ipc.MsgSwitchProject, ipc.SwitchProjectPayload{ProjectID: b.ID})
	d.handleMessage(nil, msg)

	if paneB.PTY == nil || paneB.Pending {
		t.Error("switching project left the incoming tab's panes unspawned — the user " +
			"lands on a restore indicator with no process behind it")
	}
	// The secondary half: sm.activeTab is what the NEXT daemon start eagerly
	// restores, so leaving it on the outgoing project warms the wrong one.
	if got := d.session.ActiveTabID(); got != tabB.ID {
		t.Errorf("ActiveTabID() = %q, want %q (the incoming project's own tab)", got, tabB.ID)
	}
}

// TestHandleDestroyTabRecoversThePerProjectLastTab: the auto-recovery guard
// used to be workspace-wide (len(Tabs()) == 0), so closing the last tab of
// project A while B still had tabs left A empty — and an empty ACTIVE project
// renders as a blank screen with no tab bar to act on.
func TestHandleDestroyTabRecoversThePerProjectLastTab(t *testing.T) {
	d := newTestDaemon(t)
	a := d.session.CreateProject("A", t.TempDir())
	b := d.session.CreateProject("B", t.TempDir())
	tabA := d.session.CreateTabInProject(a.ID, "A1")
	d.session.CreateTabInProject(b.ID, "B1")
	if _, ok := d.session.SwitchProject(a.ID); !ok {
		t.Fatal("SwitchProject(a) = false")
	}

	msg, _ := ipc.NewMessage(ipc.MsgDestroyTab, ipc.DestroyTabPayload{TabID: tabA.ID})
	d.handleMessage(nil, msg)

	ids := tabIDsOf(t, d.session, a.ID)
	if len(ids) != 1 {
		t.Fatalf("project A tabs = %v, want one replacement — B still having tabs "+
			"must not stop A from recovering its own last one", ids)
	}
	if ids[0] == tabA.ID {
		t.Fatal("the destroyed tab is still registered to the project")
	}
	// DestroyTab moves the global active tab to tabOrder[0], which here belongs
	// to project B — a highlighted tab absent from the tab list the client
	// renders for the active project.
	if got := d.session.ActiveTabID(); got != ids[0] {
		t.Errorf("ActiveTabID() = %q, want the replacement %q", got, ids[0])
	}
}

// TestHandleCreateProjectShipsATabRootedAtTheProjectDir covers both halves of
// the New Project flow: a project with no tab renders as a blank screen the
// moment the user switches to it (and Ctrl+T files its tab against the ACTIVE
// project, so there is no way out from inside it), and the RootDir the dialog
// collects has to actually be where the shell starts.
func TestHandleCreateProjectShipsATabRootedAtTheProjectDir(t *testing.T) {
	d := newTestDaemon(t)
	root := t.TempDir()
	want := root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		want = resolved
	}

	msg, _ := ipc.NewMessage(ipc.MsgCreateProject, ipc.CreateProjectPayload{Name: "alpha", RootDir: root})
	d.handleMessage(nil, msg)

	var proj Project
	var found bool
	for _, p := range d.session.Projects() {
		if p.Name == "alpha" {
			proj, found = p, true
		}
	}
	if !found {
		t.Fatalf("project alpha missing from %v", d.session.Projects())
	}
	if len(proj.TabIDs) != 1 {
		t.Fatalf("project TabIDs = %v, want exactly one shipped tab", proj.TabIDs)
	}
	panes := d.session.Panes(proj.TabIDs[0])
	if len(panes) != 1 {
		t.Fatalf("panes = %d, want one shell", len(panes))
	}
	if panes[0].CWD != want {
		t.Errorf("pane CWD = %q, want the project's root %q — the New Project dialog's "+
			"root directory is collected, validated and persisted but never used",
			panes[0].CWD, want)
	}
}

// TestProjectCWDFallsBackForAnUnusableRoot: a snapshot outlives the directory
// it names, and can be restored on a machine where that path never existed.
// Neither may fail the spawn.
func TestProjectCWDFallsBackForAnUnusableRoot(t *testing.T) {
	d := newTestDaemon(t)
	fallback := d.defaultCWD()

	gone := d.session.CreateProject("gone", filepath.Join(t.TempDir(), "never-existed"))
	if got := d.projectCWD(gone.ID); got != fallback {
		t.Errorf("projectCWD(stale root) = %q, want the daemon default %q", got, fallback)
	}

	blank := d.session.CreateProject("blank", "")
	if got := d.projectCWD(blank.ID); got != fallback {
		t.Errorf("projectCWD(no root) = %q, want the daemon default %q", got, fallback)
	}

	if got := d.projectCWD("proj-does-not-exist"); got != fallback {
		t.Errorf("projectCWD(unknown project) = %q, want the daemon default %q", got, fallback)
	}

	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	notADir := d.session.CreateProject("file", file)
	if got := d.projectCWD(notADir.ID); got != fallback {
		t.Errorf("projectCWD(file as root) = %q, want the daemon default %q", got, fallback)
	}
}
