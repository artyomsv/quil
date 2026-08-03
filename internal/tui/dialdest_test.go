package tui

import (
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
