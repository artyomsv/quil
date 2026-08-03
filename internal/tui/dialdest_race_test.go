package tui

import (
	"testing"
)

// TestDisconnectedDestCannotResurrectItsProjects covers the gap between
// Router.Remove and the messages already in flight when it runs.
//
// Remove closes the pump's stop channel, and every publish point checks it
// first — but that only covers a pump PARKED in Receive. A broadcast already
// sitting in the 64-slot buffer is still delivered, and one that passed the
// stopped check moments before Remove wins the select about half the time
// (select chooses uniformly among ready cases). A remote pane producing output
// keeps that buffer genuinely non-empty, so this is an ordinary race, not a
// narrow one.
//
// applyWorkspaceState treats a broadcast as authoritative for its destination,
// so a late one re-appends the projects the user just dismissed. They come back
// unusable: knownDests no longer lists the dest, so nothing re-attaches it and
// every send for its panes is dropped. The zombie sits in the sidebar for the
// rest of the session.
func TestDisconnectedDestCannotResurrectItsProjects(t *testing.T) {
	r := NewRouter(map[string]Client{"": newFakeConn(), "gpu01": newFakeConn()})
	m := Model{
		width: 100, height: 30,
		client: r,
		projects: []*ProjectModel{
			{ID: "proj-local", Name: "local"},
			{ID: "proj-gpu", Name: "remote", Dest: "gpu01"},
		},
	}

	m.disconnectDest("gpu01")
	if len(m.projects) != 1 {
		t.Fatalf("after disconnect, projects = %d, want 1 — the test needs the "+
			"disconnect itself to work before it can test the race", len(m.projects))
	}

	// The broadcast that was already in flight when the user pressed
	// Disconnect. Same shape the pump would have delivered.
	late := WorkspaceStateMsg{
		Dest:          "gpu01",
		ActiveProject: "proj-gpu",
		Projects:      []ProjectInfo{{ID: "proj-gpu", Name: "remote"}},
	}
	next, _ := m.Update(late)
	got := next.(Model)

	for _, p := range got.projects {
		if p.Dest == "gpu01" {
			t.Fatalf("a broadcast that was in flight when gpu01 was disconnected "+
				"put project %q (dest %q) back in the sidebar. It is unreachable "+
				"there: the router no longer knows the dest, so it is never "+
				"re-attached and every send for its panes is dropped.",
				p.Name, p.Dest)
		}
	}
}
