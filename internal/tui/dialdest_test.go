package tui

import (
	"os"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// Disconnecting a host answers a different question from destroying a project.
// A daemon cannot be left with no project, so destroying the last one on a
// machine bootstraps a fresh "Default" and looks like nothing happened; this
// removes the MACHINE and leaves everything on it running.
func TestDisconnectDest_RemovesTheHostAndItsProjects(t *testing.T) {
	local, gpu := newFakeConn(), newFakeConn()
	var closed []Client
	m := Model{
		client: NewRouter(map[string]Client{"": local, "gpu01": gpu}),
		cfg:    config.Config{Destinations: []config.Destination{{Dest: "gpu01"}, {Dest: "other"}}},
		projects: []*ProjectModel{
			{ID: "p-local", Name: "local"},
			{ID: "p-gpu", Name: "remote", Dest: "gpu01"},
		},
		activeProject: 1,
		attached:      map[string]bool{"": true, "gpu01": true},
		redialFns:     map[string]RedialFunc{"gpu01": func(Client) (Client, error) { return nil, nil }},
		links:         map[string]*reconnectState{"gpu01": {}},
		closeClientFn: func(c Client) { closed = append(closed, c) },
	}

	m.disconnectDest("gpu01")

	if len(m.projects) != 1 || m.projects[0].ID != "p-local" {
		t.Fatalf("projects = %v, want only the local one", m.projects)
	}
	if m.activeProject != 0 {
		t.Errorf("activeProject = %d, want 0 — the active project was on the host that left", m.activeProject)
	}
	for _, d := range m.knownDests() {
		if d == "gpu01" {
			t.Error("the router still routes to a disconnected host")
		}
	}
	// A leftover dialer would have the reconnect ladder redial a host the user
	// just dismissed: canReconnect is `redialFns[dest] != nil`.
	if m.canReconnect("gpu01") {
		t.Error("a disconnected host must not stay reconnectable")
	}
	if len(closed) != 1 || closed[0] != gpu {
		t.Errorf("closed = %v, want exactly the disconnected host's conn — an ssh child leaks otherwise", closed)
	}
	// Removed from config so it is not dialled again at launch, without
	// disturbing the other entry.
	if len(m.cfg.Destinations) != 1 || m.cfg.Destinations[0].Dest != "other" {
		t.Errorf("destinations = %v, want only \"other\"", m.cfg.Destinations)
	}
}

// disconnectDest must clear the two per-destination memories the offline
// feature added, and the on-disk half of one of them, or "forget this host"
// leaves three things behind that resurrect it: a leftover offlineWoken skips
// re-arming the ladder if the host is ever added back, a leftover cachedRemote
// answers a future SeedOfflineDest with a list belonging to the forgotten
// host, and a leftover cache FILE brings both back the moment the process
// restarts.
func TestDisconnectDest_ClearsOfflineWokenAndCachedRemoteAndTheirCacheFile(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	local, gpu := newFakeConn(), newFakeConn()
	cachePath := config.RemoteProjectsPath("gpu01")
	if err := SaveRemoteProjects(cachePath, []CachedProject{{ID: "p-gpu", Name: "remote"}}); err != nil {
		t.Fatalf("seed cache file: %v", err)
	}
	m := Model{
		client:        NewRouter(map[string]Client{"": local, "gpu01": gpu}),
		projects:      []*ProjectModel{{ID: "p-local", Name: "local"}, {ID: "p-gpu", Name: "remote", Dest: "gpu01"}},
		activeProject: 0,
		offlineWoken:  map[string]bool{"gpu01": true},
		cachedRemote:  map[string][]CachedProject{"gpu01": {{ID: "p-gpu", Name: "remote"}}},
	}

	m.disconnectDest("gpu01")

	if m.offlineWoken["gpu01"] {
		t.Error("offlineWoken not cleared — a re-added host would never re-arm its reconnect ladder")
	}
	if _, ok := m.cachedRemote["gpu01"]; ok {
		t.Error("cachedRemote not cleared — a future SeedOfflineDest would answer with the forgotten host's stale list")
	}
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Errorf("cache file still exists after disconnect (err = %v), want removed", err)
	}
}

// The local daemon is not disconnectable — its panes died with it, and
// dropping the "" route would leave the client with nowhere to send.
func TestDisconnectDest_RefusesTheLocalDaemon(t *testing.T) {
	local := newFakeConn()
	m := Model{
		client:   NewRouter(map[string]Client{"": local}),
		projects: []*ProjectModel{{ID: "p-local", Name: "local"}},
	}
	m.disconnectDest("")
	if len(m.projects) != 1 {
		t.Error("disconnecting the local daemon must be a no-op")
	}
	if len(m.knownDests()) != 1 {
		t.Error("the local route must survive")
	}
}

// A daemon with no project support accepts every project message and does
// nothing with it. The client can tell, without a version number: such a
// daemon reports no projects, so the placeholder standing in for it IS the
// observation. Reported as "the name I gave was not respected" against a host
// running a release build while the same actions worked locally.
func TestDestSupportsProjects(t *testing.T) {
	m := Model{projects: []*ProjectModel{
		{ID: "proj-real", Dest: ""},
		{ID: interimProjectIDFor("old01"), Dest: "old01"},
		{ID: "proj-real2", Dest: "new01"},
	}}

	if !m.destSupportsProjects("") {
		t.Error("a daemon reporting a real project supports projects")
	}
	if m.destSupportsProjects("old01") {
		t.Error("a placeholder means that daemon reported none — it cannot hold a project")
	}
	if !m.destSupportsProjects("new01") {
		t.Error("a remote reporting a real project supports projects")
	}
	// Unknown hosts answer true: one still connecting has no projects either,
	// and refusing there blocks the ordinary case to catch the rare one.
	if !m.destSupportsProjects("never-seen") {
		t.Error("an unknown destination must not be assumed incapable")
	}
}

// The refusal has to be visible. Closing the dialog on a project that never
// appears is what made this cost an evening to recognise.
func TestSubmitNewProject_RefusesAHostThatCannotHoldOne(t *testing.T) {
	conn := newFakeConn()
	m := Model{
		client:          conn,
		projects:        []*ProjectModel{{ID: interimProjectIDFor("old01"), Dest: "old01"}},
		projectFormDest: "old01",
		dialog:          dialogProjectNew,
	}
	if cmd := m.submitNewProject("api", "/srv/api"); cmd != nil {
		cmd()
	}
	if len(conn.sent) != 0 {
		t.Error("a create was sent to a daemon that cannot act on it")
	}
	if m.projectFormErr == "" {
		t.Error("the dialog must say why rather than closing on nothing")
	}
	if m.dialog == dialogNone {
		t.Error("a refused submit must leave the form open")
	}
}
