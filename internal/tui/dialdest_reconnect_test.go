package tui

import (
	"testing"
)

// TestReconnectDest_DoesNotDuplicateItsProjects walks the exact sequence a user
// reported as "every reconnect adds another copy of the same project": connect a
// host, see its projects, disconnect it, connect it again.
//
// Disconnect is client-side only — the remote daemon keeps every project — so
// the second connect replays the SAME project IDs the first one delivered. If
// anything on that path appended rather than merged by ID, the sidebar would
// grow by one full set per connect, and each row would name the same host.
func TestReconnectDest_DoesNotDuplicateItsProjects(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir()) // persistDestination writes config

	r := NewRouter(map[string]Client{"": newFakeConn()})
	m := Model{
		width: 100, height: 30,
		client:   r,
		projects: []*ProjectModel{{ID: "proj-local", Name: "local"}},
		attached: map[string]bool{},
	}

	// What the daemon on that host holds: the project its own workspace
	// migrated into, plus the one created from the dialog on the first visit.
	remoteState := WorkspaceStateMsg{
		Dest:          "gpu01",
		ActiveProject: "proj-cluster",
		Projects: []ProjectInfo{
			{ID: "proj-default", Name: "Default"},
			{ID: "proj-cluster", Name: "cluster-management"},
		},
	}

	connect := func(m Model) Model {
		m.adoptDest("gpu01", newFakeConn())
		next, _ := m.Update(remoteState)
		return next.(Model)
	}

	m = connect(m)
	first := countDestProjects(m, "gpu01")
	if first != 2 {
		t.Fatalf("after the first connect gpu01 has %d projects, want 2 — the test "+
			"needs the connect itself to work before it can test the reconnect", first)
	}

	m.disconnectDest("gpu01")
	if n := countDestProjects(m, "gpu01"); n != 0 {
		t.Fatalf("disconnect left %d gpu01 projects behind", n)
	}

	m = connect(m)
	if n := countDestProjects(m, "gpu01"); n != 2 {
		t.Errorf("after disconnecting and connecting gpu01 again it has %d projects, "+
			"want 2 — the daemon still holds the same two, so a larger number means "+
			"the client appended a second copy of every one of them", n)
	}
	if n := countDestProjects(m, ""); n != 1 {
		t.Errorf("the local daemon's projects changed to %d during a remote "+
			"reconnect, want 1", n)
	}
}

// TestConnectDest_IsIdempotentForOneHost covers the other half of the same
// report — connecting a host that is ALREADY connected, which is what a second
// Enter on the dialog's Host row does.
func TestConnectDest_IsIdempotentForOneHost(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	r := NewRouter(map[string]Client{"": newFakeConn()})
	m := Model{
		width: 100, height: 30,
		client:   r,
		attached: map[string]bool{},
	}
	state := WorkspaceStateMsg{
		Dest:     "gpu01",
		Projects: []ProjectInfo{{ID: "proj-cluster", Name: "cluster-management"}},
	}

	m.adoptDest("gpu01", newFakeConn())
	next, _ := m.Update(state)
	m = next.(Model)

	// Same host again, and its daemon broadcasts the same state.
	m.adoptDest("gpu01", newFakeConn())
	next2, _ := m.Update(state)
	m = next2.(Model)

	if n := countDestProjects(m, "gpu01"); n != 1 {
		t.Errorf("connecting gpu01 twice left %d copies of its one project, want 1", n)
	}
}

func countDestProjects(m Model, dest string) int {
	n := 0
	for _, p := range m.projects {
		if p.Dest == dest {
			n++
		}
	}
	return n
}
