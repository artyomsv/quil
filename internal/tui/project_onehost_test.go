package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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

// A host that is attached but has not reported its projects yet must WAIT, not
// create.
//
// This is the hole the one-per-host rule had where the user actually meets it:
// destDialedMsg batches the attach with the root-dir browse, so the listing can
// paint — and invite an Enter — before the first workspace_state arrives. The
// client then sees no projects for the host, creates, and lands beside the
// bootstrap Default it could not see. Worse, that Default is still adoptable,
// so the NEXT create renames it and the host ends with two named projects.
func TestSubmitNewProject_WaitsForAHostThatHasNotReportedYet(t *testing.T) {
	remote := newFakeConn()
	m := Model{
		client:          NewRouter(map[string]Client{"": newFakeConn(), "gpu01": remote}),
		attached:        map[string]bool{"gpu01": true},
		projectFormDest: "gpu01",
		dialog:          dialogProjectNew,
	}

	m.submitNewProject("cluster-management", "/srv/cluster")

	if len(remote.sent) != 0 {
		t.Errorf("sent %d messages before the host reported its projects — the "+
			"daemon already holds a bootstrap project this client cannot see, so "+
			"this create lands beside it", len(remote.sent))
	}
	if m.projectFormMsgKind != projectFormMsgBusy {
		t.Errorf("kind = %d, want busy — waiting for state is progress, not a "+
			"failure the user has to act on", m.projectFormMsgKind)
	}
	if m.dialog == dialogNone {
		t.Error("the dialog closed while waiting, so the create is lost")
	}
}

// Adopting closes the dialog, exactly as creating does. Asserted because the
// adopt path reaches the daemon through a different call and its dialog state
// is otherwise pinned nowhere.
func TestSubmitNewProject_AdoptClosesTheDialog(t *testing.T) {
	m := Model{
		client:          NewRouter(map[string]Client{"": newFakeConn(), "gpu01": newFakeConn()}),
		projects:        []*ProjectModel{{ID: "proj-boot", Name: "Default", Dest: "gpu01", Bootstrap: true}},
		projectFormDest: "gpu01",
		dialog:          dialogProjectNew,
	}

	m.submitNewProject("cluster-management", "/srv/cluster")

	if m.dialog != dialogNone {
		t.Errorf("dialog = %v after a successful adopt, want closed", m.dialog)
	}
}

// The adopt path marks its update conditional, so the daemon can refuse it when
// another client named the project first. Without the flag the loser silently
// renames the winner's project.
func TestSubmitNewProject_AdoptSendsAConditionalUpdate(t *testing.T) {
	remote := newFakeConn()
	m := Model{
		client:          NewRouter(map[string]Client{"": newFakeConn(), "gpu01": remote}),
		projects:        []*ProjectModel{{ID: "proj-boot", Name: "Default", Dest: "gpu01", Bootstrap: true}},
		projectFormDest: "gpu01",
		dialog:          dialogProjectNew,
	}

	m.submitNewProject("cluster-management", "/srv/cluster")

	updates := sentOfType(t, remote, ipc.MsgUpdateProject)
	if len(updates) != 1 {
		t.Fatalf("sent %d updates, want 1", len(updates))
	}
	var p ipc.UpdateProjectPayload
	if err := updates[0].DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	if !p.AdoptBootstrap {
		t.Error("the adopt was sent unconditionally — a second client adopting " +
			"the same host would rename this project out from under the user")
	}
}

// A plain rename must NOT carry the flag: the project it targets is, correctly,
// no longer a bootstrap, so a conditional update would be refused every time.
func TestSubmitRenameProject_IsUnconditional(t *testing.T) {
	remote := newFakeConn()
	m := Model{
		client:   NewRouter(map[string]Client{"": newFakeConn(), "gpu01": remote}),
		projects: []*ProjectModel{{ID: "proj-cluster", Name: "cluster-management", Dest: "gpu01"}},
	}

	m.submitRenameProject("proj-cluster", "infra", "/srv/infra")

	updates := sentOfType(t, remote, ipc.MsgUpdateProject)
	if len(updates) != 1 {
		t.Fatalf("sent %d updates, want 1", len(updates))
	}
	var p ipc.UpdateProjectPayload
	if err := updates[0].DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	if p.AdoptBootstrap {
		t.Error("a plain rename was sent conditionally, so the daemon refuses it")
	}
}

// The New Project form must not say "this machine" while aimed at a remote
// host. It seeded projectFormDest from the active project but left the ssh
// fields blank, so with a remote project active the form contradicted itself —
// and once naming a project there can RENAME the host's existing one, those
// keystrokes rename a project on a machine the form said was local.
func TestOpenNewProjectDialog_SeedsTheHostFieldsFromTheActiveDest(t *testing.T) {
	m := Model{
		client:        NewRouter(map[string]Client{"": newFakeConn(), "build@gpu01": newFakeConn()}),
		projects:      []*ProjectModel{{ID: "proj-remote", Name: "cluster", Dest: "build@gpu01"}},
		activeProject: 0,
	}

	next, _ := m.openNewProjectDialog()
	got := next.(Model)

	if got.projectFormDest != "build@gpu01" {
		t.Fatalf("projectFormDest = %q, want the active project's host", got.projectFormDest)
	}
	if !got.projectFormRemote {
		t.Error("the Remote toggle is off while the form targets a remote host, so " +
			"it reads as \"this machine\" and a submit acts on the far one")
	}
	if got.projectFormUser != "build" || got.projectFormHost != "gpu01" {
		t.Errorf("user=%q host=%q, want the dest split into the fields the user reads",
			got.projectFormUser, got.projectFormHost)
	}
}

// The refusal has to survive the REAL key path, not just a direct call. Every
// other refusal test calls submitNewProject on an addressable Model; the call
// site returns `m, m.submitNewProject(...)`, where the message is written
// through the implicit &m while m is also the result.
func TestProjectForm_RefusalSurvivesTheKeyHandler(t *testing.T) {
	m := Model{
		width: 100, height: 30,
		client:          NewRouter(map[string]Client{"": newFakeConn(), "gpu01": newFakeConn()}),
		projects:        []*ProjectModel{{ID: "proj-cluster", Name: "cluster-management", Dest: "gpu01"}},
		projectFormDest: "gpu01",
		projectFormName: "infra",
		dialog:          dialogProjectNew,
	}
	// Enter on the Name row submits.
	m.projectFormCursor = 0

	next, _ := m.handleProjectDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := next.(Model)

	if got.projectFormErr == "" {
		t.Fatal("the Model RETURNED by the key handler carries no message, so the " +
			"refusal is invisible however correct the direct call is")
	}
	if got.dialog != dialogProjectNew {
		t.Errorf("dialog = %v, want it still open on a refusal", got.dialog)
	}
}
