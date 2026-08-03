package tui

import (
	"errors"
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
		// openNewProjectDialog seeds this from the active dest. The form field
		// is the source of truth rather than activeDest itself, because the
		// Host row can point one form at a different machine — see
		// TestSubmitNewProject_HostFieldOverridesTheActiveDest.
		projectFormDest: "gpu01",
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

// The Host field's whole point: create a project on a machine that is NOT the
// one currently on screen, without leaving the TUI. The root dir beside it was
// browsed on that host's filesystem, so sending the create anywhere else pairs
// a name with a path that does not exist there.
func TestSubmitNewProject_HostFieldOverridesTheActiveDest(t *testing.T) {
	fake := newFakeConn()
	m := Model{
		client:          fake,
		projects:        []*ProjectModel{{ID: "proj-local", Dest: ""}},
		activeProject:   0,
		projectFormHost: "gpu01",
		projectFormDest: "gpu01", // set once the dial landed
	}

	if cmd := m.submitNewProject("beta", "/srv/beta"); cmd != nil {
		cmd()
	}
	if len(fake.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(fake.sent))
	}
	if got := fake.sent[0].Origin; got != "gpu01" {
		t.Errorf("Origin = %q, want gpu01 — the active project is local, but the "+
			"form names another host and the root dir came from ITS filesystem", got)
	}
}

// A host that is already connected must not be dialled again: Router.Add
// refuses a live destination, so a second dial would spawn an ssh child that
// nothing ever closes.
func TestConnectProjectHost_AlreadyConnectedSkipsTheDial(t *testing.T) {
	dialed := 0
	m := Model{
		client:          NewRouter(map[string]Client{"": newFakeConn(), "gpu01": newFakeConn()}),
		projectFormHost: "gpu01",
		dialDestFn: func(string) (Client, error) {
			dialed++
			return newFakeConn(), nil
		},
	}
	next, _ := m.connectProjectHost()
	got := next.(Model)

	if dialed != 0 {
		t.Errorf("dialled %d times for an already-connected host, want 0", dialed)
	}
	if got.projectFormDest != "gpu01" {
		t.Errorf("projectFormDest = %q, want gpu01", got.projectFormDest)
	}
	if got.projectFormCursor != projectRowRootDir {
		t.Errorf("cursor = %d, want the root-dir row so the browser is next", got.projectFormCursor)
	}
}

// A dial result for a host the user has since retyped must be discarded. The
// dial takes seconds against a host that is down, and editing the field while
// one is in flight is ordinary use.
func TestDestDialed_StaleResultIsIgnored(t *testing.T) {
	m := Model{
		client:             NewRouter(map[string]Client{"": newFakeConn()}),
		projectFormDialing: "gpu02", // the user retyped
		projectFormDest:    "",
	}
	next, _ := m.Update(destDialedMsg{dest: "gpu01", client: newFakeConn()})
	got := next.(Model)

	if got.projectFormDest != "" {
		t.Errorf("projectFormDest = %q; a result for an abandoned host was applied", got.projectFormDest)
	}
	if got.projectFormDialing != "gpu02" {
		t.Errorf("projectFormDialing = %q, want the host still being awaited", got.projectFormDialing)
	}
}

// A failed dial reports why and leaves the form on the Host row, rather than
// silently pointing the root-dir browser at a machine that is not there.
func TestDestDialed_FailureSurfacesAndKeepsTheDest(t *testing.T) {
	m := Model{
		client:             NewRouter(map[string]Client{"": newFakeConn()}),
		projectFormDialing: "gpu01",
		projectFormCursor:  projectRowHost,
	}
	next, _ := m.Update(destDialedMsg{dest: "gpu01", err: errDialTest})
	got := next.(Model)

	if got.projectFormErr == "" {
		t.Error("a failed dial must say why")
	}
	if got.projectFormDest == "gpu01" {
		t.Error("a failed dial must not point the form at the host")
	}
	if got.projectFormDialing != "" {
		t.Error("the in-flight marker must clear so the field is editable again")
	}
}

var errDialTest = errors.New("ssh: connect to host gpu01 port 22: No route to host")
