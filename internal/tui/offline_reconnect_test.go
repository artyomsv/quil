package tui

import "testing"

// The whole point of caching the daemon's own project ID: the row stops being
// orange rather than being deleted and replaced.
func TestApplyWorkspaceState_ClearsOfflineInPlace(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	m := Model{}
	m.SeedOfflineDest("gpu01", "gpu01", offlineNeedsUpgrade, "drift", []CachedProject{
		{ID: "proj-1a2b3c4d", Name: "cluster-management"},
	})
	before := m.projects[0]

	m.applyWorkspaceState(WorkspaceStateMsg{
		Projects: []ProjectInfo{{ID: "proj-1a2b3c4d", Name: "cluster-management"}},
	}, "gpu01")

	if len(m.projects) != 1 {
		t.Fatalf("projects = %d, want 1 — the row is filled, not duplicated", len(m.projects))
	}
	if m.projects[0] != before {
		t.Error("a new *ProjectModel was allocated; the offline row must be reused in place")
	}
	if m.projects[0].Offline != nil {
		t.Error("Offline survived the broadcast; the row would stay guard-refused forever")
	}
}

// A cache written before the project was renamed or destroyed on the host must
// not leave a ghost row behind.
func TestApplyWorkspaceState_StaleCachedRowDisappears(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	m := Model{}
	m.SeedOfflineDest("gpu01", "gpu01", offlineRetrying, "", []CachedProject{
		{ID: "proj-deadbeef", Name: "since-deleted"},
	})

	m.applyWorkspaceState(WorkspaceStateMsg{
		Projects: []ProjectInfo{{ID: "proj-1a2b3c4d", Name: "the-real-one"}},
	}, "gpu01")

	for _, p := range m.projects {
		if p.ID == "proj-deadbeef" {
			t.Fatal("the stale cached row survived the broadcast")
		}
	}
}

// Seeded rows make cur() non-nil, so without this the local daemon's remembered
// active project is never adopted and every launch with a dead remote opens
// focused on a stand-in.
func TestApplyWorkspaceState_SeededRowsDoNotStealLaunchFocus(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	m := Model{}
	m.SeedOfflineDest("gpu01", "gpu01", offlineRetrying, "", nil)

	m.applyWorkspaceState(WorkspaceStateMsg{
		Projects:      []ProjectInfo{{ID: "proj-1a2b3c4d", Name: "monorepo"}},
		ActiveProject: "proj-1a2b3c4d",
	}, "")

	got := m.cur()
	if got == nil || got.ID != "proj-1a2b3c4d" {
		t.Fatalf("active project = %v, want the local daemon's remembered one", got)
	}
}
