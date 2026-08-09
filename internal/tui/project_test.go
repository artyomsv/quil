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
	t.Setenv("QUIL_HOME", t.TempDir()) // cacheRemoteProjects writes here, never production
	// The BROADCASTING dest's project is first on purpose: with it last, an
	// append-based merge reproduces the original order by luck and the order
	// assertion below passes against the very bug it exists for.
	m := Model{projects: []*ProjectModel{
		{ID: "proj-gpu", Name: "api", Dest: "gpu01", tabs: []*TabModel{NewTabModel("tab-9", "Nine")}},
		{ID: "proj-local", Name: "quil", Dest: "", tabs: []*TabModel{NewTabModel("tab-1", "One")}},
	}}

	m.applyWorkspaceState(WorkspaceStateMsg{
		Dest:     "gpu01",
		Projects: []ProjectInfo{{ID: "proj-gpu", Name: "api-renamed", TabIDs: []string{"tab-9"}, ActiveTab: "tab-9"}},
		Tabs:     []TabInfo{{ID: "tab-9", Name: "Nine", ProjectID: "proj-gpu"}},
	}, "gpu01")

	if len(m.projects) != 2 {
		t.Fatalf("projects = %d, want 2 — the local project was clobbered", len(m.projects))
	}
	// ORDER, not just membership: the broadcasting dest's projects must keep
	// the slots they already occupied. Appending the rebuilt list instead
	// moved proj-gpu to the end, so a session holding local + remote reordered
	// the sidebar on every alternating broadcast.
	if m.projects[0].ID != "proj-gpu" || m.projects[1].ID != "proj-local" {
		t.Fatalf("order = [%s %s], want [proj-gpu proj-local] — a broadcast reordered "+
			"the project list", m.projects[0].ID, m.projects[1].ID)
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

// TestApplyWorkspaceStateKeepsProjectOrderAcrossAlternatingBroadcasts is the
// regression test for the merge: with a local daemon interleaved with a remote
// one in the list, NEITHER broadcast may move anything. The old `append(kept,
// rebuilt...)` put whichever dest broadcast last at the end, so the two
// daemons' projects swapped places on every tick of ordinary pane/tab traffic.
//
// The fixture interleaves dests (local, remote, local) deliberately: with the
// two dests contiguous, an append-based merge reproduces the original order for
// one of the two broadcasts by luck and the test passes half the time.
func TestApplyWorkspaceStateKeepsProjectOrderAcrossAlternatingBroadcasts(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir()) // cacheRemoteProjects writes here, never production
	m := Model{projects: []*ProjectModel{
		{ID: "proj-l1", Dest: "", tabs: []*TabModel{NewTabModel("tab-1", "One")}},
		{ID: "proj-r1", Dest: "gpu01", tabs: []*TabModel{NewTabModel("tab-2", "Two")}},
		{ID: "proj-l2", Dest: "", tabs: []*TabModel{NewTabModel("tab-3", "Three")}},
	}}
	want := []string{"proj-l1", "proj-r1", "proj-l2"}

	local := WorkspaceStateMsg{
		Dest: "",
		Projects: []ProjectInfo{
			{ID: "proj-l1", TabIDs: []string{"tab-1"}, ActiveTab: "tab-1"},
			{ID: "proj-l2", TabIDs: []string{"tab-3"}, ActiveTab: "tab-3"},
		},
		Tabs: []TabInfo{
			{ID: "tab-1", Name: "One", ProjectID: "proj-l1"},
			{ID: "tab-3", Name: "Three", ProjectID: "proj-l2"},
		},
	}
	remote := WorkspaceStateMsg{
		Dest:     "gpu01",
		Projects: []ProjectInfo{{ID: "proj-r1", TabIDs: []string{"tab-2"}, ActiveTab: "tab-2"}},
		Tabs:     []TabInfo{{ID: "tab-2", Name: "Two", ProjectID: "proj-r1"}},
	}

	for i := 0; i < 4; i++ {
		state := local
		if i%2 == 1 {
			state = remote
		}
		m.applyWorkspaceState(state, state.Dest)

		got := make([]string, len(m.projects))
		for j, p := range m.projects {
			got[j] = p.ID
		}
		if len(got) != len(want) {
			t.Fatalf("broadcast %d: projects = %v, want %v", i, got, want)
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("broadcast %d from dest %q: order = %v, want %v", i, state.Dest, got, want)
			}
		}
	}
}

// TestApplyWorkspaceStateAppendsOnlyGenuinelyNewProjects: a project the daemon
// has just created has no slot to keep, so it goes to the end — and a project
// the daemon has dropped disappears rather than lingering.
func TestApplyWorkspaceStateAppendsOnlyGenuinelyNewProjects(t *testing.T) {
	m := Model{projects: []*ProjectModel{
		{ID: "proj-a", Dest: "", tabs: []*TabModel{NewTabModel("tab-1", "One")}},
		{ID: "proj-b", Dest: "", tabs: []*TabModel{NewTabModel("tab-2", "Two")}},
	}}

	m.applyWorkspaceState(WorkspaceStateMsg{
		Dest: "",
		Projects: []ProjectInfo{
			// Daemon order deliberately differs from the client's, and proj-a
			// is gone: the client's own slot order wins for survivors.
			{ID: "proj-c", TabIDs: []string{"tab-3"}, ActiveTab: "tab-3"},
			{ID: "proj-b", TabIDs: []string{"tab-2"}, ActiveTab: "tab-2"},
		},
		Tabs: []TabInfo{
			{ID: "tab-2", Name: "Two", ProjectID: "proj-b"},
			{ID: "tab-3", Name: "Three", ProjectID: "proj-c"},
		},
	}, "")

	if len(m.projects) != 2 {
		t.Fatalf("projects = %d, want 2 (proj-a dropped, proj-c added)", len(m.projects))
	}
	if m.projects[0].ID != "proj-b" || m.projects[1].ID != "proj-c" {
		t.Fatalf("order = [%s %s], want [proj-b proj-c]", m.projects[0].ID, m.projects[1].ID)
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

// The client half of the tab-drag round trip (the daemon half is
// TestReorderTabMovesTheOwningProjectsTabIDs in internal/daemon): tab ORDER
// comes from the project's tab_ids, never from the broadcast's flat `tabs`
// array. The two are given DIFFERENT orders here so the assertion can only
// pass for one of them — the daemon's reorder lands in tab_ids, and a client
// that ordered by `tabs` would revert every drag on the next broadcast.
func TestApplyWorkspaceStateOrdersTabsByProjectTabIDs(t *testing.T) {
	var m Model
	m.applyWorkspaceState(WorkspaceStateMsg{
		Projects: []ProjectInfo{{ID: "proj-a", TabIDs: []string{"tab-3", "tab-1", "tab-2"}}},
		Tabs: []TabInfo{
			{ID: "tab-1", Name: "One", ProjectID: "proj-a"},
			{ID: "tab-2", Name: "Two", ProjectID: "proj-a"},
			{ID: "tab-3", Name: "Three", ProjectID: "proj-a"},
		},
	}, "")

	var got []string
	for _, tab := range m.curTabs() {
		got = append(got, tab.ID)
	}
	want := []string{"tab-3", "tab-1", "tab-2"}
	if len(got) != len(want) {
		t.Fatalf("tabs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tab order = %v, want %v (tab_ids order, not the `tabs` array's)", got, want)
		}
	}
}

// Two project-less destinations must not collide on one synthetic ID.
// indexOfProject resolves by ID alone, so a shared proj-interim would put the
// active pointer on whichever came first: focus the remote's tabs and the next
// local broadcast drags you back to the local ones.
func TestBroadcastProjectsQualifiesTheSyntheticIDPerDest(t *testing.T) {
	state := WorkspaceStateMsg{Tabs: []TabInfo{{ID: "tab-1", Name: "One"}}}

	local := broadcastProjects(state, "")
	if len(local) != 1 || local[0].ID != interimProjectID {
		t.Fatalf("local synthetic project = %+v, want the bare %q — existing clients hold that ID",
			local, interimProjectID)
	}
	remote := broadcastProjects(state, "gpu01")
	if len(remote) != 1 || remote[0].ID == interimProjectID {
		t.Fatalf("remote synthetic project = %+v, want an ID distinct from %q", remote, interimProjectID)
	}

	// The end the collision is actually felt at: two project-less dests, both
	// present, and focus stays where the user put it.
	m := Model{}
	m.applyWorkspaceState(state, "")
	m.applyWorkspaceState(WorkspaceStateMsg{Tabs: []TabInfo{{ID: "tab-9", Name: "Nine"}}}, "gpu01")
	if len(m.projects) != 2 {
		t.Fatalf("projects = %d, want 2 — the two dests collapsed onto one synthetic ID", len(m.projects))
	}
	m.activeProject = 1
	focused := m.projects[1].ID
	m.applyWorkspaceState(state, "")
	if p := m.cur(); p == nil || p.ID != focused {
		t.Fatalf("active project = %v, want %q — a local broadcast stole focus from the remote", p, focused)
	}
}

// resolveActiveProjectIndex must behave exactly like indexOfProject on a
// genuine match, offline or not — landing on an offline row by NAME is how
// the repair panel is reached, and this must not get in the way of that.
func TestResolveActiveProjectIndex_GenuineMatchWinsEvenIfOffline(t *testing.T) {
	projects := []*ProjectModel{
		{ID: "proj-live"},
		{ID: "proj-offline", Offline: &OfflineState{}},
	}
	if got := resolveActiveProjectIndex(projects, "proj-offline"); got != 1 {
		t.Errorf("index = %d, want 1 — a requested offline project must still resolve", got)
	}
}

// The miss case is the fix: index 0 is exactly where a seeded offline row
// parks (SeedOfflineDest runs before any broadcast, so it occupies the low
// indices), so falling back to indexOfProject's plain 0-on-a-miss silently
// lands an unresolved active id on a project with no tabs and no panes.
func TestResolveActiveProjectIndex_MissPrefersLiveOverOffline(t *testing.T) {
	projects := []*ProjectModel{
		{ID: "proj-offline", Offline: &OfflineState{}},
		{ID: "proj-live"},
	}
	if got := resolveActiveProjectIndex(projects, "does-not-exist"); got != 1 {
		t.Errorf("index = %d, want 1 (the live project), not the offline stand-in at 0", got)
	}
}

// When every project really is offline there is no live one to prefer, so
// this must degrade to the same always-somewhere-valid fallback as
// indexOfProject rather than panicking or returning an out-of-range index.
func TestResolveActiveProjectIndex_EveryProjectOfflineFallsBackToZero(t *testing.T) {
	projects := []*ProjectModel{
		{ID: "proj-a", Offline: &OfflineState{}},
		{ID: "proj-b", Offline: &OfflineState{}},
	}
	if got := resolveActiveProjectIndex(projects, "missing"); got != 0 {
		t.Errorf("index = %d, want 0", got)
	}
}

// TestApplyWorkspaceState_MissedActiveIDPrefersLiveOverOfflineProject is the
// end-to-end version of TestResolveActiveProjectIndex_MissPrefersLiveOverOffline:
// a launch that seeded an offline row for an unreachable remote host, followed
// by the local daemon's first broadcast (which the daemon has never recorded an
// active project for). Before the fix this landed m.activeProject on the
// offline stand-in — combined with freezeInput's per-project gate, selecting it
// was how the whole session could read as input-frozen.
func TestApplyWorkspaceState_MissedActiveIDPrefersLiveOverOfflineProject(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir()) // cacheRemoteProjects writes here, never production
	m := Model{projects: []*ProjectModel{
		{ID: "proj-offline@gpu01", Dest: "gpu01", Offline: &OfflineState{Kind: offlineRetrying}},
	}}

	m.applyWorkspaceState(WorkspaceStateMsg{
		Dest:     "",
		Projects: []ProjectInfo{{ID: "proj-local", TabIDs: []string{"tab-1"}, ActiveTab: "tab-1"}},
		Tabs:     []TabInfo{{ID: "tab-1", Name: "One", ProjectID: "proj-local"}},
	}, "")

	if got := m.cur(); got == nil || got.ID != "proj-local" {
		t.Fatalf("active project = %+v, want proj-local", got)
	}
}

// The dispose sweep must run AFTER m.projects is rebuilt, and must span every
// project. Both halves are asserted here because each fails a different way
// and neither is visible elsewhere in the suite:
//
//   - Sweeping before the assignment leaves m.projects still holding the
//     project this broadcast dropped, so its panes look alive and leak their
//     VT emulators for the session's lifetime. (A rebuilt project's own pane
//     churn does NOT show this: proj.tabs is assigned on the reused pointer,
//     so the old slice already reflects the new tabs.)
//   - Sweeping only the rebuilt projects disposes another dest's live panes.
func TestApplyWorkspaceStateDisposesOnlyTheDroppedProjectsPanes(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir()) // cacheRemoteProjects writes here, never production
	livePane := NewPaneModel("pane-local", 4096)
	liveTab := NewTabModel("tab-1", "One")
	liveTab.Root = NewLeaf(livePane)

	orphanPane := NewPaneModel("pane-b", 4096)
	orphanTab := NewTabModel("tab-2", "Two")
	orphanTab.Root = NewLeaf(orphanPane)

	m := Model{projects: []*ProjectModel{
		{ID: "proj-local", Dest: "", tabs: []*TabModel{liveTab}},
		{ID: "proj-a", Dest: "gpu01", tabs: []*TabModel{NewTabModel("tab-9", "Nine")}},
		{ID: "proj-b", Dest: "gpu01", tabs: []*TabModel{orphanTab}},
	}}

	// gpu01 no longer has proj-b.
	m.applyWorkspaceState(WorkspaceStateMsg{
		Dest:     "gpu01",
		Projects: []ProjectInfo{{ID: "proj-a", TabIDs: []string{"tab-9"}}},
		Tabs:     []TabInfo{{ID: "tab-9", Name: "Nine", ProjectID: "proj-a"}},
	}, "gpu01")

	// Dispose nils the emulator; a live pane still has one.
	if livePane.vt == nil {
		t.Error("a pane of another dest's project was disposed by this dest's broadcast")
	}
	if orphanPane.vt != nil {
		t.Error("a dropped project's pane was not disposed — the sweep ran before " +
			"m.projects was rebuilt, so the dropped project still looked alive")
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
	t.Setenv("QUIL_HOME", t.TempDir()) // cacheRemoteProjects writes here, never production
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
