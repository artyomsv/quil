package tui

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// Creating a project whose name a daemon already has is refused.
//
// Scoped to the LOCAL daemon, which is the only one that holds several
// projects: a remote host holds exactly one, so a duplicate name there is
// refused earlier and for a different reason (see project_onehost_test.go).
// Locally the hazard is the sidebar — it shows name and host and nothing else,
// so two rows with one name on one daemon are indistinguishable: nothing says
// which holds the user's tabs, and removing the wrong one takes them with it.
func TestSubmitNewProject_RefusesADuplicateNameOnTheSameHost(t *testing.T) {
	conn := newFakeConn()
	m := Model{
		client: NewRouter(map[string]Client{"": conn}),
		projects: []*ProjectModel{
			{ID: "proj-cluster", Name: "cluster-management"},
		},
		projectFormDest: "",
		// Open, so "the dialog stays open on a refusal" is a real assertion
		// rather than one the zero value satisfies for free.
		dialog: dialogProjectNew,
	}

	if cmd := m.submitNewProject("cluster-management", "/srv/cluster"); cmd != nil {
		t.Error("a refused create returned a command")
	}
	if m.projectFormErr == "" {
		t.Fatal("the duplicate was accepted silently")
	}
	if !strings.Contains(m.projectFormErr, "already exists") {
		t.Errorf("message = %q, want it to say the name is taken", m.projectFormErr)
	}
	if m.dialog == dialogNone {
		t.Error("the dialog closed on a create it refused to send, which is how the " +
			"user learns the project was made when it was not")
	}
	for _, sent := range conn.sent {
		if sent.Type == ipc.MsgCreateProject {
			t.Fatal("the create was sent anyway — the guard only changed the message")
		}
	}
}

// Case and surrounding space do not make two rows distinguishable, so they do
// not make the name free either.
func TestSubmitNewProject_DuplicateCheckIgnoresCaseAndSpace(t *testing.T) {
	m := Model{
		client:          NewRouter(map[string]Client{"": newFakeConn()}),
		projects:        []*ProjectModel{{ID: "p1", Name: "Cluster-Management"}},
		projectFormDest: "",
	}
	if m.submitNewProject("  cluster-management  ", ""); m.projectFormErr == "" {
		t.Error("a name differing only by case and padding was accepted")
	}
}

// The SAME name on a DIFFERENT host is ordinary and must still work: the
// sidebar row carries the host, so those two are told apart on sight — which is
// exactly what two on one host are not.
func TestSubmitNewProject_AllowsTheSameNameOnAnotherHost(t *testing.T) {
	remote := newFakeConn()
	m := Model{
		client:          NewRouter(map[string]Client{"": newFakeConn(), "gpu01": remote}),
		projects:        []*ProjectModel{{ID: "p-local", Name: "cluster-management"}},
		projectFormDest: "gpu01",
	}

	m.submitNewProject("cluster-management", "/srv/cluster")
	if m.projectFormErr != "" {
		t.Fatalf("refused a name that only the LOCAL daemon has: %q", m.projectFormErr)
	}
	found := false
	for _, sent := range remote.sent {
		if sent.Type == ipc.MsgCreateProject {
			found = true
		}
	}
	if !found {
		t.Error("no create reached gpu01")
	}
}

// The client refuses a rename onto a name the destination already has, the way
// it refuses a create. Scoped to the local daemon for the same reason: a remote
// host holds one project, so it has nothing to collide with.
func TestSubmitRenameProject_RefusesADuplicateName(t *testing.T) {
	conn := newFakeConn()
	m := Model{
		client: NewRouter(map[string]Client{"": conn}),
		projects: []*ProjectModel{
			{ID: "proj-cluster", Name: "cluster-management"},
			{ID: "proj-infra", Name: "infra"},
		},
		dialog: dialogProjectRename,
	}

	m.submitRenameProject("proj-infra", "cluster-management", "/srv/infra")

	if len(conn.sent) != 0 {
		t.Errorf("sent %d messages for a refused rename", len(conn.sent))
	}
	if m.projectFormErr == "" {
		t.Error("the duplicate rename was accepted silently")
	}
	if m.dialog == dialogNone {
		t.Error("the dialog closed on a rename it refused to send")
	}
}

// Renaming a project to the name it already holds is how the form submits a
// root-directory change. It must not read as a collision with itself.
func TestSubmitRenameProject_AllowsItsOwnName(t *testing.T) {
	conn := newFakeConn()
	m := Model{
		client:   NewRouter(map[string]Client{"": conn}),
		projects: []*ProjectModel{{ID: "proj-cluster", Name: "cluster-management"}},
		dialog:   dialogProjectRename,
	}

	m.submitRenameProject("proj-cluster", "cluster-management", "/srv/elsewhere")

	if len(sentOfType(t, conn, ipc.MsgUpdateProject)) != 1 {
		t.Fatalf("the rename was refused: %q", m.projectFormErr)
	}
}
