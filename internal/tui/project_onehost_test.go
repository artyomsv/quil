package tui

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// sentOfType returns the payloads of every message of one type a fake conn saw.
func sentOfType(t *testing.T, c *fakeConn, typ string) []*ipc.Message {
	t.Helper()
	var out []*ipc.Message
	for _, m := range c.sent {
		if m.Type == typ {
			out = append(out, m)
		}
	}
	return out
}

// A remote host holds exactly one project, and naming it is how you get it.
//
// A daemon must have at least one tab, a tab must belong to a project, so a
// host arrives already holding a project nobody asked for — "Default", either
// bootstrapped on attach or migrated from tabs that predate projects. Creating
// a project beside it left the user with a row they never named, holding the
// tabs they actually cared about.
func TestSubmitNewProject_AdoptsTheHostsUnnamedProject(t *testing.T) {
	remote := newFakeConn()
	m := Model{
		client: NewRouter(map[string]Client{"": newFakeConn(), "gpu01": remote}),
		projects: []*ProjectModel{
			{ID: "proj-boot", Name: "Default", Dest: "gpu01", Bootstrap: true},
		},
		projectFormDest: "gpu01",
		dialog:          dialogProjectNew,
	}

	m.submitNewProject("cluster-management", "/home/artyom/homelab")

	if got := sentOfType(t, remote, ipc.MsgCreateProject); len(got) != 0 {
		t.Error("a create was sent — that adds a SECOND project beside the one " +
			"the host already has, which is the row the user never asked for")
	}
	updates := sentOfType(t, remote, ipc.MsgUpdateProject)
	if len(updates) != 1 {
		t.Fatalf("sent %d updates, want 1 renaming the host's own project", len(updates))
	}
	var p ipc.UpdateProjectPayload
	if err := updates[0].DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	if p.ProjectID != "proj-boot" {
		t.Errorf("renamed %q, want the host's existing project — its tabs are "+
			"already inside it, which is what puts them under the chosen name", p.ProjectID)
	}
	if p.Name != "cluster-management" || p.RootDir != "/home/artyom/homelab" {
		t.Errorf("name=%q root=%q, want both from the form", p.Name, p.RootDir)
	}
	if m.projectFormErr != "" {
		t.Errorf("adopting reported %q", m.projectFormErr)
	}
}

// Once a host's project has a name, that host is taken: it may hold only one.
func TestSubmitNewProject_RefusesASecondProjectOnAHost(t *testing.T) {
	remote := newFakeConn()
	m := Model{
		client: NewRouter(map[string]Client{"": newFakeConn(), "gpu01": remote}),
		projects: []*ProjectModel{
			{ID: "proj-cluster", Name: "cluster-management", Dest: "gpu01"},
		},
		projectFormDest: "gpu01",
		dialog:          dialogProjectNew,
	}

	m.submitNewProject("infra", "/srv/infra")

	if len(remote.sent) != 0 {
		t.Errorf("sent %d messages for a refused create", len(remote.sent))
	}
	if !strings.Contains(m.projectFormErr, "gpu01") ||
		!strings.Contains(m.projectFormErr, "cluster-management") {
		t.Errorf("message = %q, want it to name the host AND the project already "+
			"there — otherwise the user cannot tell what to rename", m.projectFormErr)
	}
	if m.dialog == dialogNone {
		t.Error("the dialog closed on a create it refused to send")
	}
}

// A named project makes the host taken even when an unnamed one is also
// present, which is reachable: a project created before this rule existed sits
// beside the bootstrap one. Adopting there would rename a project the user is
// not looking at.
func TestSubmitNewProject_NamedProjectWinsOverAnUnnamedOne(t *testing.T) {
	remote := newFakeConn()
	m := Model{
		client: NewRouter(map[string]Client{"": newFakeConn(), "gpu01": remote}),
		projects: []*ProjectModel{
			{ID: "proj-boot", Name: "Default", Dest: "gpu01", Bootstrap: true},
			{ID: "proj-cluster", Name: "cluster-management", Dest: "gpu01"},
		},
		projectFormDest: "gpu01",
		dialog:          dialogProjectNew,
	}

	m.submitNewProject("infra", "")

	if len(remote.sent) != 0 {
		t.Errorf("sent %d messages, want none — the host already has a named project", len(remote.sent))
	}
	if m.projectFormErr == "" {
		t.Error("adopted or created silently instead of refusing")
	}
}

// The LOCAL daemon keeps many projects. The one-per-host rule is about remote
// hosts, and applying it locally would refuse the second project on the machine
// the user is sitting at.
func TestSubmitNewProject_LocalDaemonStillTakesManyProjects(t *testing.T) {
	local := newFakeConn()
	m := Model{
		client: NewRouter(map[string]Client{"": local}),
		projects: []*ProjectModel{
			{ID: "proj-boot", Name: "Default", Bootstrap: true},
		},
		projectFormDest: "",
		dialog:          dialogProjectNew,
	}

	m.submitNewProject("infra", "/srv/infra")

	if got := sentOfType(t, local, ipc.MsgCreateProject); len(got) != 1 {
		t.Fatalf("sent %d creates locally, want 1 — the local daemon is not "+
			"one-project-per-host, and must not adopt its Default either", len(got))
	}
	if got := sentOfType(t, local, ipc.MsgUpdateProject); len(got) != 0 {
		t.Error("adopted the local Default; a local create must add a project " +
			"beside the user's existing work, not rename it")
	}
}

// A host whose first workspace_state has not landed presents no projects at
// all. A create is the right answer there — refusing would make the dialog
// depend on a broadcast the user cannot see.
func TestSubmitNewProject_CreatesOnAHostThatHasReportedNothingYet(t *testing.T) {
	remote := newFakeConn()
	m := Model{
		client:          NewRouter(map[string]Client{"": newFakeConn(), "gpu01": remote}),
		projectFormDest: "gpu01",
		dialog:          dialogProjectNew,
	}

	m.submitNewProject("cluster-management", "/srv/cluster")

	if got := sentOfType(t, remote, ipc.MsgCreateProject); len(got) != 1 {
		t.Errorf("sent %d creates, want 1", len(got))
	}
}
