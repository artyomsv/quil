package daemon

import "testing"

// mergeFixture builds the state a host carries after the duplicate bug: a
// project holding the user's real work, plus two same-named strays each holding
// a throwaway tab. Returns the project IDs in creation order.
func mergeFixture(t *testing.T) (*SessionManager, string, string, string) {
	t.Helper()
	sm := NewSessionManager(100)
	// Each tab carries a pane, so "nothing is closed" is a claim the tests can
	// actually check: a fold that fell through to DestroyProject would take them.
	tab := func(projectID, name string) {
		t.Helper()
		created := sm.CreateTabInProject(projectID, name)
		if _, err := sm.CreatePane(created.ID, "/home/build"); err != nil {
			t.Fatalf("create pane in %s: %v", name, err)
		}
	}
	keep := sm.CreateProject("Default1", "/home/build/.quil")
	dupA := sm.CreateProject("cluster-management", "/home/build/homelab")
	dupB := sm.CreateProject("cluster-management", "/home/build/homelab")
	// INTERLEAVED deliberately. The global tab order is creation order, so
	// building each project's tabs in a block makes it already equal to the
	// merged project's list and every ordering assertion below passes whether
	// the merge maintains it or not.
	tab(keep.ID, "shorli")
	tab(dupA.ID, "Shell")
	tab(keep.ID, "opa-opa")
	tab(dupB.ID, "Shell")
	return sm, keep.ID, dupA.ID, dupB.ID
}

// The point of the whole operation: tabs MOVE, they are not closed. Destroying
// a project takes its panes with it, which is why consolidating a host by hand
// cost the user the work that made them care which project survived.
func TestMergeProjects_MovesTabsAndDropsTheAbsorbedProjects(t *testing.T) {
	sm, keep, dupA, dupB := mergeFixture(t)

	if !sm.MergeProjects(keep, []string{dupA, dupB}, "cluster-management", "/home/build/homelab") {
		t.Fatal("merge refused")
	}

	projects := sm.Projects()
	if len(projects) != 1 {
		t.Fatalf("projects = %d, want the single survivor", len(projects))
	}
	if got := len(projects[0].TabIDs); got != 4 {
		t.Errorf("survivor holds %d tabs, want all 4 — a fold that loses tabs is a "+
			"destroy wearing a different name", got)
	}
	// Both halves of the link. The client rebuilds a project from TabIDs but
	// skips any tab whose own ProjectID disagrees, so updating one side alone
	// makes the tab vanish from the sidebar while still existing here.
	for _, tabID := range projects[0].TabIDs {
		if got := sm.tabs[tabID].ProjectID; got != keep {
			t.Errorf("tab %s still records project %s, want %s — the client would "+
				"skip it and the tab would disappear from the sidebar", tabID, got, keep)
		}
	}
	if got := len(sm.panes); got != 4 {
		t.Errorf("panes = %d, want all 4 — the fold must move tabs, not destroy them", got)
	}
	// The root directory is applied, not merely carried on the wire. Dropping
	// this half of the assignment left every other test in the package green
	// while the "renamed" project silently kept its old root — which is what new
	// panes spawn in and what the git subsystem probes.
	if got := projects[0].RootDir; got != "/home/build/homelab" {
		t.Errorf("RootDir = %q, want the value the fold carried", got)
	}
	// The absorbed IDs must leave projectOrder too. Projects() and the snapshot
	// both skip map-missing IDs defensively, so a stale entry is invisible until
	// DestroyProject's fallback — which has NO such check — promotes it into
	// activeProject and lands the client on a project that does not exist.
	for _, id := range []string{dupA, dupB} {
		if indexOfString(sm.projectOrder, id) >= 0 {
			t.Errorf("absorbed project %s is still in projectOrder %v", id, sm.projectOrder)
		}
	}
}

// Among projects nobody named, the FIRST wins — the fallback arm of the survivor
// choice, which no fixture with a named project can reach.
func TestMergeProjects_AllBootstrapHostsKeepTheFirst(t *testing.T) {
	sm := NewSessionManager(100)
	sm.CreateTab("Shell") // bootstraps one
	first := sm.Projects()[0].ID
	second := sm.CreateProject("Default", "/srv")
	second.Bootstrap = true

	sm.MergeProjects(first, []string{second.ID}, "infra", "/srv")

	if got := sm.Projects()[0].ID; got != first {
		t.Errorf("survivor = %q, want the first %q", got, first)
	}
}

// The rename must happen AFTER the absorbed records are gone, so their names are
// free. Naming a host after the duplicate being folded away is the ordinary way
// out of this state — the user types "cluster-management" precisely because that
// is what the strays are called. Computing the name first hands them
// "cluster-management (2)", which is the shape the fold exists to remove.
func TestMergeProjects_FreesTheAbsorbedNamesBeforeRenaming(t *testing.T) {
	sm, keep, dupA, dupB := mergeFixture(t)

	sm.MergeProjects(keep, []string{dupA, dupB}, "cluster-management", "/home/build/homelab")

	if got := sm.Projects()[0].Name; got != "cluster-management" {
		t.Errorf("name = %q, want %q — the absorbed projects still held the name "+
			"when it was disambiguated", got, "cluster-management")
	}
}

// A survivor listed in its own absorb list would have its tabs appended to
// itself, giving each of them two entries in TabIDs — which the client renders
// as the same tab twice, both copies fighting over one layout tree.
func TestMergeProjects_SkipsItselfInAbsorb(t *testing.T) {
	sm, keep, dupA, _ := mergeFixture(t)

	sm.MergeProjects(keep, []string{keep, dupA}, "infra", "/srv")

	// Resolved BY ID rather than taken as Projects()[0]: absorbing the survivor
	// also deletes its record, so an index-based lookup silently reads whichever
	// project happens to be left and reports no duplicates for the wrong one.
	var survivor *Project
	for _, p := range sm.Projects() {
		if p.ID == keep {
			cp := p
			survivor = &cp
		}
	}
	if survivor == nil {
		t.Fatal("the survivor absorbed itself and its own record was deleted")
	}
	seen := map[string]bool{}
	for _, tabID := range survivor.TabIDs {
		if seen[tabID] {
			t.Fatalf("tab %s appears twice in the survivor's TabIDs — the client "+
				"renders it as two tabs fighting over one layout tree", tabID)
		}
		seen[tabID] = true
	}
}

// A client acts on its own snapshot, which can name a project the daemon no
// longer has. Folding what it can see and skipping the rest converges; failing
// whole would leave the host in exactly the state the fold was asked to fix.
func TestMergeProjects_SkipsUnknownAbsorbIDs(t *testing.T) {
	sm, keep, dupA, _ := mergeFixture(t)

	if !sm.MergeProjects(keep, []string{dupA, "proj-gone"}, "infra", "/srv") {
		t.Fatal("merge refused because one absorb ID was stale")
	}

	if got := len(sm.Projects()); got != 2 {
		t.Errorf("projects = %d, want 2 — the live absorb applied and the stale one "+
			"was skipped", got)
	}
}

// An unknown survivor has nothing to fold into. Reported rather than silently
// ignored, because from the client it looks like a dialog that accepted a name
// and closed on nothing changing.
func TestMergeProjects_UnknownSurvivorIsRefused(t *testing.T) {
	sm, _, dupA, _ := mergeFixture(t)

	if sm.MergeProjects("proj-gone", []string{dupA}, "infra", "/srv") {
		t.Error("merge into an unknown project reported success")
	}
	if got := len(sm.Projects()); got != 3 {
		t.Errorf("projects = %d, want the original 3 untouched", got)
	}
}

// Naming it makes it the user's, exactly as a rename does. A survivor left
// Bootstrap would be adopted by the next create and silently renamed.
func TestMergeProjects_ClearsBootstrapOnTheSurvivor(t *testing.T) {
	sm := NewSessionManager(100)
	sm.CreateTab("Shell") // bootstraps a Default
	boot := sm.Projects()[0].ID
	stray := sm.CreateProject("stray", "/srv")

	sm.MergeProjects(boot, []string{stray.ID}, "cluster-management", "/srv")

	if sm.Projects()[0].Bootstrap {
		t.Error("survivor is still Bootstrap, so the next create on this host would " +
			"adopt it and rename the project the user just made")
	}
}

// The absorbed project can be the active one, and its remembered tab belongs to
// the survivor afterwards. SwitchProject reads Project.ActiveTab, so leaving it
// on the survivor's OWN original tab lands the next switch-away-and-back
// somewhere the user never was.
func TestMergeProjects_PromotesTheActiveProjectAndItsTab(t *testing.T) {
	sm, keep, dupA, dupB := mergeFixture(t)
	// Make the stray active, on its own tab, as it would be after the user
	// clicked it in the sidebar.
	activeTab, ok := sm.SwitchProject(dupA)
	if !ok || activeTab == "" {
		t.Fatalf("switch to %s returned %q, %v", dupA, activeTab, ok)
	}

	sm.MergeProjects(keep, []string{dupA, dupB}, "cluster-management", "/srv")

	if got := sm.ActiveProject(); got != keep {
		t.Errorf("activeProject = %q, want the survivor %q — it named a project that "+
			"no longer exists", got, keep)
	}
	if got := sm.Projects()[0].ActiveTab; got != activeTab {
		t.Errorf("survivor's ActiveTab = %q, want the tab that was active %q", got, activeTab)
	}
}

// A tab already in the survivor's list must not be appended a second time.
//
// The self-merge guard covers only one shape of this. restoreProjects copies
// `tab_ids` verbatim from workspace.json with no uniqueness or cross-project
// check, so a snapshot listing one tab under two projects reaches the identical
// state — and the client then builds one TabModel twice, both copies fighting
// over a single layout tree.
func TestMergeProjects_RefusesToListOneTabTwice(t *testing.T) {
	sm, keep, dupA, _ := mergeFixture(t)
	// The shape a corrupted snapshot restores: the stray claims a tab that
	// already belongs to the survivor.
	shared := sm.projects[keep].TabIDs[0]
	sm.projects[dupA].TabIDs = append(sm.projects[dupA].TabIDs, shared)

	sm.MergeProjects(keep, []string{dupA}, "infra", "/srv")

	seen := map[string]bool{}
	for _, tabID := range sm.Projects()[0].TabIDs {
		if seen[tabID] {
			t.Fatalf("tab %s is listed twice in the survivor", tabID)
		}
		seen[tabID] = true
	}
}

// An absorbed project whose remembered tab is gone must not leave activeTab
// pointing outside the survivor. The client scopes the visible tab list to the
// active project's own tabs, so that renders as a highlighted tab absent from
// the list — the state DestroyProject re-derives against for the same reason.
func TestMergeProjects_ReDerivesAStrandedActiveTab(t *testing.T) {
	sm, keep, dupA, dupB := mergeFixture(t)
	if _, ok := sm.SwitchProject(dupA); !ok {
		t.Fatal("switch failed")
	}
	// The remembered tab vanished — a destroy that raced the fold.
	sm.activeTab = "tab-gone"
	sm.projects[dupA].ActiveTab = "tab-gone"

	sm.MergeProjects(keep, []string{dupA, dupB}, "infra", "/srv")

	survivor := sm.Projects()[0]
	if indexOfString(survivor.TabIDs, sm.activeTab) < 0 {
		t.Errorf("activeTab = %q names no tab in the survivor %v — the client "+
			"highlights a tab that is not in the list it renders",
			sm.activeTab, survivor.TabIDs)
	}
	if indexOfString(survivor.TabIDs, survivor.ActiveTab) < 0 {
		t.Errorf("survivor ActiveTab = %q names no tab it holds", survivor.ActiveTab)
	}
}

// The global order must keep matching the project-relative one — the invariant
// ReorderTab maintains through this same reanchor helper. With every tab folded
// into one project the two lists collapse to the same list, so this asserts them
// equal element for element; the fixture interleaves creation precisely so they
// start out disagreeing.
func TestMergeProjects_ReanchorsTheGlobalTabOrder(t *testing.T) {
	sm, keep, dupA, dupB := mergeFixture(t)

	sm.MergeProjects(keep, []string{dupA, dupB}, "cluster-management", "/srv")

	tabIDs := sm.Projects()[0].TabIDs
	if len(sm.tabOrder) != len(tabIDs) {
		t.Fatalf("tabOrder holds %d tabs, the project holds %d", len(sm.tabOrder), len(tabIDs))
	}
	for i, tabID := range tabIDs {
		if sm.tabOrder[i] != tabID {
			t.Fatalf("tabOrder = %v, project = %v — the two disagree about grouping, "+
				"which is what reanchorTab exists to prevent", sm.tabOrder, tabIDs)
		}
	}
}
