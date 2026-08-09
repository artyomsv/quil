package tui

import "testing"

// The git ticker broadcasts every 5 seconds. An unconditional write is roughly
// 17 000 disk writes a day on a client left open, so the change check is the
// feature, not an optimisation.
func TestCacheRemoteProjects_WritesOnlyWhenTheSetChanges(t *testing.T) {
	writes := 0
	m := Model{saveRemoteProjectsFn: func(string, []CachedProject) error {
		writes++
		return nil
	}}
	m.projects = []*ProjectModel{
		{ID: "proj-1a2b3c4d", Name: "cluster-management", RootDir: "/w", Dest: "gpu01"},
	}

	for i := 0; i < 5; i++ {
		m.cacheRemoteProjects("gpu01")
	}
	if writes != 1 {
		t.Fatalf("writes = %d after 5 identical broadcasts, want 1", writes)
	}

	m.projects[0].Name = "renamed"
	m.cacheRemoteProjects("gpu01")
	if writes != 2 {
		t.Errorf("writes = %d after a rename, want 2", writes)
	}
}

// The local daemon needs no cache: it is never seeded offline, and a dead local
// daemon is fatal by design.
func TestCacheRemoteProjects_SkipsLocal(t *testing.T) {
	writes := 0
	m := Model{saveRemoteProjectsFn: func(string, []CachedProject) error { writes++; return nil }}
	m.projects = []*ProjectModel{{ID: "proj-1a2b3c4d", Name: "monorepo", Dest: ""}}
	m.cacheRemoteProjects("")
	if writes != 0 {
		t.Errorf("writes = %d, want 0 for the local daemon", writes)
	}
}

// A projects-unaware daemon yields proj-interim@<dest>; caching it would make
// destSupportsProjects answer from a stale observation on the next launch.
func TestCacheRemoteProjects_ExcludesSynthetic(t *testing.T) {
	var got []CachedProject
	m := Model{saveRemoteProjectsFn: func(_ string, list []CachedProject) error {
		got = list
		return nil
	}}
	m.projects = []*ProjectModel{
		{ID: interimProjectIDFor("gpu01"), Name: "(no projects)", Dest: "gpu01"},
		{ID: "proj-1a2b3c4d", Name: "real", Dest: "gpu01"},
	}
	m.cacheRemoteProjects("gpu01")

	if len(got) != 1 || got[0].ID != "proj-1a2b3c4d" {
		t.Errorf("cached %+v, want only the real project", got)
	}
}

// An offline row is this client's own invention replayed from cache. Writing it
// back would pin a stale list permanently: the host's real answer could never
// overwrite it, because the offline row is what produced it.
func TestCacheRemoteProjects_ExcludesOfflineRows(t *testing.T) {
	writes := 0
	m := Model{saveRemoteProjectsFn: func(string, []CachedProject) error { writes++; return nil }}
	m.projects = []*ProjectModel{
		{ID: "proj-1a2b3c4d", Name: "cluster", Dest: "gpu01", Offline: &OfflineState{}},
	}
	m.cacheRemoteProjects("gpu01")
	if writes != 0 {
		t.Errorf("writes = %d, want 0 — offline rows are not evidence", writes)
	}
}
