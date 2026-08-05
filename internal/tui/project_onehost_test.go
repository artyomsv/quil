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

// Once a host's project has a name, naming a second OFFERS to rename the first
// rather than refusing.
//
// The refusal this replaced was a dead end: "rename it instead" named a remedy
// the dialog had no route to, and the user had to leave, find the row in the
// sidebar and use its context menu. Nothing is sent on the first Enter — the
// offer has to be read before it is taken.
func TestSubmitNewProject_OffersToRenameALoneNamedProject(t *testing.T) {
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
		t.Errorf("sent %d messages before the user confirmed", len(remote.sent))
	}
	if !strings.Contains(m.projectFormErr, "gpu01") ||
		!strings.Contains(m.projectFormErr, "cluster-management") {
		t.Errorf("message = %q, want it to name the host AND the project already "+
			"there — otherwise the user cannot tell what is about to be renamed",
			m.projectFormErr)
	}
	if m.projectFormMsgKind != projectFormMsgWarn {
		t.Errorf("kind = %d, want warn — a consequence awaiting confirmation is not "+
			"the failure red says it is", m.projectFormMsgKind)
	}
	if m.dialog == dialogNone {
		t.Error("the dialog closed before the user could confirm")
	}
	if m.projectFormMerge == nil || len(m.projectFormMerge.absorb) != 0 {
		t.Errorf("plan = %+v, want one that absorbs nothing — there is only one "+
			"project on this host, so the fold degenerates to a rename", m.projectFormMerge)
	}
}

// A host holding several folds them into one. This is the state the create-time
// guard leaves behind on a host that predates it: renaming one of three still
// leaves three, so the old refusal described work that could not fix it.
func TestSubmitNewProject_OffersToFoldAHostHoldingSeveral(t *testing.T) {
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
		t.Errorf("sent %d messages before the user confirmed", len(remote.sent))
	}
	plan := m.projectFormMerge
	if plan == nil {
		t.Fatal("no fold was planned for a host holding two projects")
	}
	// The NAMED project survives, not the first in order. Its root directory is
	// one the user chose; a bootstrap project's is whatever CWD the daemon
	// started in, and an empty form root falls back to the survivor's.
	if plan.into != "proj-cluster" {
		t.Errorf("survivor = %q, want the project the user named", plan.into)
	}
	if len(plan.absorb) != 1 || plan.absorb[0] != "proj-boot" {
		t.Errorf("absorb = %v, want the unnamed project", plan.absorb)
	}
}

// The LOCAL daemon keeps many projects. The one-per-host rule is about remote
// hosts, and applying it locally would FOLD every project on the machine the
// user is sitting at into one — the exact operation the rule now performs on a
// remote host, and the exact wrong answer here.
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

// The escape hatch: a named destination this client has never attached.
//
// Read the fixture carefully — `attached` is empty. That is what separates this
// from TestSubmitNewProject_WaitsForAHostThatHasNotReportedYet, which is the
// case the user actually reaches: attached, and its state still in flight.
//
// Whether an UNATTACHED non-empty dest is reachable through the UI is NOT
// established. `adoptDest` sets the ledger entry before `destDialedMsg` assigns
// projectFormDest, and a project only exists for a dest whose state has
// arrived, so both routes into the form look attached — but every entry point
// has not been traced, and a create is the safer answer for a destination the
// client knows nothing about than a wait that nothing will ever end. This pins
// the fallback; it does not claim a user can get here.
func TestSubmitNewProject_CreatesOnAHostThisClientNeverAttached(t *testing.T) {
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

// The offer has to survive the REAL key path, not just a direct call. Every
// other test here calls submitNewProject on an addressable Model; the call
// site returns `m, m.submitNewProject(...)`, where the message is written
// through the implicit &m while m is also the result.
func TestProjectForm_OfferSurvivesTheKeyHandler(t *testing.T) {
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
			"offer is invisible however correct the direct call is")
	}
	if got.projectFormMerge == nil {
		t.Error("the returned Model carries no armed plan, so the confirming Enter " +
			"recomputes from nothing and only ever re-arms — the fold is unreachable")
	}
	if got.dialog != dialogProjectNew {
		t.Errorf("dialog = %v, want it still open until the user confirms", got.dialog)
	}
}

// Adopting with no root directory keeps the project's own.
//
// The regression this pins: submitProjectForm deliberately does not wait for
// the browse, so on a create the root is "" until a round trip to a host
// connected seconds ago answers — and Enter on the Name row submits at once.
// Routing that create into a rename made empty reachable on a path whose
// documented invariant says it is not, and UpdateProject has no
// unchanged-value guard, so the adopted project's root was erased.
func TestSubmitNewProject_AdoptKeepsTheProjectsRootWhenTheBrowseHasNotAnswered(t *testing.T) {
	remote := newFakeConn()
	m := Model{
		client: NewRouter(map[string]Client{"": newFakeConn(), "gpu01": remote}),
		projects: []*ProjectModel{
			{ID: "proj-boot", Name: "Default", Dest: "gpu01", Bootstrap: true,
				RootDir: "/home/build"},
		},
		projectFormDest: "gpu01",
		dialog:          dialogProjectNew,
	}

	// The browse has not answered, so the form's root is still empty.
	m.submitNewProject("cluster-management", "")

	updates := sentOfType(t, remote, ipc.MsgUpdateProject)
	if len(updates) != 1 {
		t.Fatalf("sent %d updates, want 1", len(updates))
	}
	var p ipc.UpdateProjectPayload
	if err := updates[0].DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	if p.RootDir != "/home/build" {
		t.Errorf("RootDir = %q, want the adopted project's own — an empty one "+
			"ERASES it, and new panes and the git subsystem both read it", p.RootDir)
	}
}
