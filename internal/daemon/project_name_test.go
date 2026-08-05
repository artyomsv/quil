package daemon

import (
	"strings"
	"testing"
)

// The client refuses a duplicate project name, but it checks its OWN snapshot —
// which is empty until the host's workspace_state arrives. Submitting in that
// window (Enter on the Name row submits immediately, so it is a keystroke away)
// reaches a daemon that appended whatever it was given, and the sidebar ended up
// with two rows carrying the same name and the same host. They are then
// indistinguishable: nothing tells the user which one holds their tabs, and
// removing the wrong one takes them with it.
//
// The daemon is the only place that can decide this without a race, so it is
// the one that guarantees the names differ.
func TestCreateProject_MakesADuplicateNameDistinguishable(t *testing.T) {
	sm := NewSessionManager(100)

	first := sm.CreateProject("cluster-management", "/srv/cluster")
	second := sm.CreateProject("cluster-management", "/srv/cluster")
	third := sm.CreateProject("cluster-management", "/srv/other")

	if first.Name != "cluster-management" {
		t.Errorf("the FIRST project was renamed to %q; only a collision may be "+
			"disambiguated", first.Name)
	}
	if second.Name == first.Name {
		t.Fatalf("both projects are called %q — this is the pair the user cannot "+
			"tell apart in the sidebar", second.Name)
	}
	if third.Name == first.Name || third.Name == second.Name {
		t.Errorf("third = %q, want it distinct from %q and %q",
			third.Name, first.Name, second.Name)
	}
	// The user's own words have to survive, or the row stops being findable.
	for _, p := range []string{second.Name, third.Name} {
		if !strings.HasPrefix(p, "cluster-management") {
			t.Errorf("name %q dropped what the user typed", p)
		}
	}
}

// Case and padding do not make two rows distinguishable on screen, so they must
// not count as a different name here either — the client's guard compares the
// same way.
func TestCreateProject_DuplicateCheckIgnoresCaseAndSpace(t *testing.T) {
	sm := NewSessionManager(100)

	first := sm.CreateProject("Cluster-Management", "")
	second := sm.CreateProject("  cluster-management  ", "")

	if strings.EqualFold(strings.TrimSpace(second.Name), strings.TrimSpace(first.Name)) {
		t.Errorf("second = %q reads identically to %q in the sidebar", second.Name, first.Name)
	}
}

// Distinct names are left exactly as typed. Without this the disambiguation
// could rename everything and the tests above would still pass.
func TestCreateProject_LeavesADistinctNameAlone(t *testing.T) {
	sm := NewSessionManager(100)

	sm.CreateProject("cluster-management", "")
	other := sm.CreateProject("infra", "")

	if other.Name != "infra" {
		t.Errorf("name = %q, want it untouched — nothing collided", other.Name)
	}
}

// A RENAME can produce the pair a create is prevented from producing. Intent
// does not make the result distinguishable: afterwards the two rows read the
// same, and removing the wrong one still takes its tabs.
func TestUpdateProject_MakesARenameCollisionDistinguishable(t *testing.T) {
	sm := NewSessionManager(100)
	first := sm.CreateProject("cluster-management", "/srv/cluster")
	second := sm.CreateProject("infra", "/srv/infra")

	if !sm.UpdateProject(second.ID, "cluster-management", "/srv/infra", false) {
		t.Fatal("the rename was refused outright")
	}

	names := map[string]string{}
	for _, p := range sm.Projects() {
		names[p.ID] = p.Name
	}
	if names[first.ID] != "cluster-management" {
		t.Errorf("the untouched project became %q", names[first.ID])
	}
	if names[second.ID] == names[first.ID] {
		t.Errorf("both projects are called %q after the rename", names[second.ID])
	}
}

// Renaming a project to the name it already has — which is what submitting the
// form after changing only the root directory does — must not treat it as
// colliding with itself.
func TestUpdateProject_KeepsItsOwnNameWhenOnlyTheRootChanges(t *testing.T) {
	sm := NewSessionManager(100)
	p := sm.CreateProject("cluster-management", "/srv/cluster")

	if !sm.UpdateProject(p.ID, "cluster-management", "/srv/elsewhere", false) {
		t.Fatal("update failed")
	}

	got := sm.Projects()[0]
	if got.Name != "cluster-management" {
		t.Errorf("name = %q — the project collided with itself", got.Name)
	}
	if got.RootDir != "/srv/elsewhere" {
		t.Errorf("root = %q, want the new one", got.RootDir)
	}
}

// A name that collides with nothing is stored trimmed, like one that does.
//
// The trim used to live on the collision path only, so "  infra  " kept its
// padding when it was the first of its name and lost it when it was the
// second — the stored value depended on whether a collision happened. The
// case-and-space test above cannot catch a regression here: it collides, so it
// goes through the disambiguation branch, which always trimmed.
func TestCreateProject_TrimsANameThatCollidesWithNothing(t *testing.T) {
	sm := NewSessionManager(100)

	p := sm.CreateProject("  infra  ", "")

	if p.Name != "infra" {
		t.Errorf("name = %q, want it trimmed — otherwise whether the padding "+
			"survives depends on whether some other project happens to share the name", p.Name)
	}
}
