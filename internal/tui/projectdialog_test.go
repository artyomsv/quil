package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestSubmitNewProjectSendsCreateToActiveDest(t *testing.T) {
	fake := newFakeConn()
	m := Model{
		client:        fake,
		projects:      []*ProjectModel{{ID: "proj-a", Dest: "gpu01"}},
		activeProject: 0,
	}

	if cmd := m.submitNewProject("beta", "/src/beta"); cmd != nil {
		cmd()
	}

	if len(fake.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(fake.sent))
	}
	got := fake.sent[0]
	if got.Type != ipc.MsgCreateProject {
		t.Fatalf("type = %s, want %s", got.Type, ipc.MsgCreateProject)
	}
	if got.Origin != "gpu01" {
		t.Fatalf("Origin = %q, want gpu01 — a new project belongs to the daemon "+
			"whose filesystem holds its root dir", got.Origin)
	}
	var payload ipc.CreateProjectPayload
	got.DecodePayload(&payload)
	if payload.Name != "beta" || payload.RootDir != "/src/beta" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestSubmitNewProjectRejectsEmptyName(t *testing.T) {
	fake := newFakeConn()
	m := Model{client: fake, projects: []*ProjectModel{{ID: "proj-a"}}}

	if cmd := m.submitNewProject("  ", "/src"); cmd != nil {
		cmd()
	}
	if len(fake.sent) != 0 {
		t.Fatal("an unnamed project must not be created")
	}
}

func TestDestroyProjectIsConfirmedNotImmediate(t *testing.T) {
	fake := newFakeConn()
	m := Model{client: fake, projects: []*ProjectModel{{ID: "proj-a", Name: "alpha"}}}

	m.confirmDestroyProject("proj-a")

	if len(fake.sent) != 0 {
		t.Fatal("destroy takes every tab and pane with it — it must confirm first")
	}
	if m.dialog != dialogConfirm {
		t.Fatalf("dialog = %v, want a confirm dialog", m.dialog)
	}
	if m.confirmID != "proj-a" || m.confirmKind != confirmKindDestroyProject {
		t.Fatalf("confirm = (%q, %q), want the project kind and its ID",
			m.confirmKind, m.confirmID)
	}
}
