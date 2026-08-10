package tui

import (
	"fmt"
	"strings"
	"testing"
)

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

// The mixed case above (one synthetic, one real) never drives a dest whose
// ENTIRE project set is synthetic, so it never proves the len(list) == 0
// early return actually fires from the synthetic-filter branch rather than
// from m.projects being empty to begin with.
func TestCacheRemoteProjects_ExcludesSynthetic_AllSynthetic(t *testing.T) {
	writes := 0
	m := Model{saveRemoteProjectsFn: func(string, []CachedProject) error { writes++; return nil }}
	m.projects = []*ProjectModel{
		{ID: interimProjectIDFor("gpu01"), Name: "(no projects)", Dest: "gpu01"},
	}
	m.cacheRemoteProjects("gpu01")

	if writes != 0 {
		t.Errorf("writes = %d, want 0 — every project on this dest was synthetic", writes)
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

// ID/Name/RootDir all trace back to the remote daemon's own broadcast, with
// no cap of their own on that path. Round 2 of this feature's review found
// the write side uncapped — a single oversized broadcast grew this process's
// memory and its on-disk cache to match, before the load-side caps ever got
// a chance to trim it back down on the NEXT launch.
func TestCacheRemoteProjects_TruncatesOversizedFields(t *testing.T) {
	var got []CachedProject
	m := Model{saveRemoteProjectsFn: func(_ string, list []CachedProject) error {
		got = list
		return nil
	}}
	long := strings.Repeat("x", maxCachedFieldLen+100)
	m.projects = []*ProjectModel{
		{ID: long, Name: long, RootDir: long, Dest: "gpu01"},
	}
	m.cacheRemoteProjects("gpu01")

	if len(got) != 1 {
		t.Fatalf("cached %d projects, want 1", len(got))
	}
	if n := len(got[0].ID); n > maxCachedFieldLen {
		t.Errorf("ID length = %d, want <= %d", n, maxCachedFieldLen)
	}
	if n := len(got[0].Name); n > maxCachedFieldLen {
		t.Errorf("Name length = %d, want <= %d", n, maxCachedFieldLen)
	}
	if n := len(got[0].RootDir); n > maxCachedFieldLen {
		t.Errorf("RootDir length = %d, want <= %d", n, maxCachedFieldLen)
	}
}

// The count cap must apply BEFORE the change-detection compare, or a
// destination reporting more than maxCachedProjects real projects would
// write an uncapped file on its very first broadcast and then never write
// again — sameCachedProjects would keep comparing the SAME uncapped slice
// against itself and never see a change.
func TestCacheRemoteProjects_CapsCountBeforeCompare(t *testing.T) {
	writes := 0
	var lastWritten []CachedProject
	m := Model{saveRemoteProjectsFn: func(_ string, list []CachedProject) error {
		writes++
		lastWritten = list
		return nil
	}}
	m.projects = make([]*ProjectModel, 0, maxCachedProjects+10)
	for i := 0; i < maxCachedProjects+10; i++ {
		m.projects = append(m.projects, &ProjectModel{
			ID:   fmt.Sprintf("proj-%03d", i),
			Name: "p",
			Dest: "gpu01",
		})
	}

	m.cacheRemoteProjects("gpu01")
	if writes != 1 {
		t.Fatalf("writes = %d, want 1", writes)
	}
	if len(lastWritten) != maxCachedProjects {
		t.Fatalf("wrote %d entries, want capped at %d", len(lastWritten), maxCachedProjects)
	}

	// A second broadcast with the identical (capped) content must not write
	// again — proof the cap was applied before the stored comparison value
	// was captured, not just before the file write.
	m.cacheRemoteProjects("gpu01")
	if writes != 1 {
		t.Errorf("writes = %d after an identical broadcast, want still 1", writes)
	}
}
