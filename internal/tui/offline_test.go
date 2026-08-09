package tui

import "testing"

func TestSeedOfflineDest_UsesCachedNames(t *testing.T) {
	m := Model{}
	m.SeedOfflineDest("gpu01", "gpu box", offlineNeedsUpgrade, "runs 1.53.0", []CachedProject{
		{ID: "proj-1a2b3c4d", Name: "cluster-management", RootDir: "/home/u/work"},
	})

	if len(m.projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(m.projects))
	}
	p := m.projects[0]
	if p.ID != "proj-1a2b3c4d" {
		t.Errorf("ID = %q, want the cached daemon ID (reconnect fills it in place)", p.ID)
	}
	if p.Name != "cluster-management" {
		t.Errorf("Name = %q, want the cached name", p.Name)
	}
	if p.Dest != "gpu01" {
		t.Errorf("Dest = %q, want gpu01", p.Dest)
	}
	if p.Offline == nil || p.Offline.Kind != offlineNeedsUpgrade {
		t.Fatalf("Offline = %+v, want kind offlineNeedsUpgrade", p.Offline)
	}
	if p.Offline.Detail != "runs 1.53.0" {
		t.Errorf("Detail = %q, want the classifier's detail", p.Offline.Detail)
	}
}

// A host this client has never reached has no cache, and the row must still
// exist — it is the only thing that says the host is configured at all.
func TestSeedOfflineDest_NoCacheFallsBackToLabel(t *testing.T) {
	m := Model{}
	m.SeedOfflineDest("gpu01", "gpu box", offlineRetrying, "no route to host", nil)

	if len(m.projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(m.projects))
	}
	if got := m.projects[0].Name; got != "gpu box" {
		t.Errorf("Name = %q, want the destination label", got)
	}
	if m.projects[0].ID == "" {
		t.Error("ID is empty; the row needs a stable identity for the sidebar")
	}
}

// Seeding twice for one dest must not double the rows: the launch path and a
// later reclassification both call it.
func TestSeedOfflineDest_ReplacesThatDestsRows(t *testing.T) {
	m := Model{}
	cached := []CachedProject{{ID: "proj-1a2b3c4d", Name: "cluster-management"}}
	m.SeedOfflineDest("gpu01", "gpu01", offlineRetrying, "", cached)
	m.SeedOfflineDest("gpu01", "gpu01", offlineNeedsUpgrade, "version drift", cached)

	if len(m.projects) != 1 {
		t.Fatalf("projects = %d, want 1 after reseeding the same dest", len(m.projects))
	}
	if m.projects[0].Offline.Kind != offlineNeedsUpgrade {
		t.Error("reseed did not update the kind")
	}
}

// A projects-unaware daemon's placeholder must never be resurrected from cache:
// its ID is invented per-process and collides with the fresh one.
func TestSeedOfflineDest_SkipsSyntheticCachedProjects(t *testing.T) {
	m := Model{}
	m.SeedOfflineDest("gpu01", "gpu01", offlineRetrying, "", []CachedProject{
		{ID: interimProjectIDFor("gpu01"), Name: "(no projects)"},
	})

	if len(m.projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(m.projects))
	}
	if got := m.projects[0].Name; got != "gpu01" {
		t.Errorf("Name = %q, want the label — the synthetic cache entry must be ignored", got)
	}
}
