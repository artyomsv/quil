package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

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

// TestBeginProjectRenameDoesNotSubmitStaleRootDir is the regression case for
// the round-trip race: beginProjectRename's browse request is async, and
// cwdBrowseDir is shared scratch state with the pane-setup dialog and every
// OTHER project's rename session. Opening Rename on project B while
// cwdBrowseDir still holds a stale value (project A's root, left over from a
// previous rename that never completed a browse) and immediately pressing
// Enter from the Name field — where the cursor starts — must neither submit
// A's root as B's, nor submit at all until the daemon's answer for B lands.
func TestBeginProjectRenameDoesNotSubmitStaleRootDir(t *testing.T) {
	fake := newFakeConn()
	m := Model{
		client: fake,
		projects: []*ProjectModel{
			{ID: "proj-a", Name: "alpha", RootDir: "/src/project-a"},
			{ID: "proj-b", Name: "beta", RootDir: "/src/project-b"},
		},
	}
	// Stale scratch state from a previous dialog session — project A's root.
	m.cwdBrowseDir = "/src/project-a"
	m.cwdBrowseEntries = []string{"stale-entry"}

	tm, _ := m.beginProjectRename("proj-b")
	m = tm.(Model)

	// Opening the dialog must clear the stale value SYNCHRONOUSLY, before the
	// (unresolved, in this test) browse response for B ever lands.
	if m.cwdBrowseDir != "" {
		t.Fatalf("cwdBrowseDir = %q immediately after opening rename, want cleared — "+
			"project A's root must not survive into B's dialog", m.cwdBrowseDir)
	}
	if !m.browse.pending {
		t.Fatal("browse.pending = false, want true — the root-dir request for B is in flight")
	}

	// The natural "open rename, fix the name, press Enter" flow: cursor
	// starts on the Name field (0), and Enter there submits directly —
	// before B's browse response has arrived.
	tm2, submitCmd := m.handleProjectDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m2 := tm2.(Model)
	if submitCmd != nil {
		submitCmd()
	}

	if len(fake.sent) != 0 {
		t.Fatalf("sent %d messages while the root-dir browse was still pending — "+
			"a submit must not race an outstanding response", len(fake.sent))
	}
	if m2.dialog != dialogProjectRename {
		t.Fatalf("dialog = %v, want dialogProjectRename — a blocked submit must leave the form open", m2.dialog)
	}

	// Once B's own browse answer lands, the field holds B's real root and
	// submitting proceeds normally — the gate is a wait, not a dead end.
	resp := ipc.BrowseDirRespPayload{
		Path: "/src/project-b", Resolved: "/src/project-b",
	}
	m2.applyBrowseDir(resp, m2.browse.gen)

	_, submitCmd2 := m2.handleProjectDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if submitCmd2 != nil {
		submitCmd2()
	}

	if len(fake.sent) != 1 {
		t.Fatalf("sent %d messages after the browse resolved, want 1", len(fake.sent))
	}
	var payload ipc.UpdateProjectPayload
	fake.sent[0].DecodePayload(&payload)
	if payload.ProjectID != "proj-b" || payload.RootDir != "/src/project-b" {
		t.Fatalf("payload = %+v, want proj-b's own root, not A's stale one", payload)
	}
}
