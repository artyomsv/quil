package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestSubmitNewProjectSendsCreateToActiveDest(t *testing.T) {
	fake := newFakeConn()
	m := Model{
		client: fake,
		// gpu01 holds NO project yet, which is the only state a create reaches
		// on a remote host now that one host holds one project: with a project
		// already there the create either adopts it or is refused. The routing
		// this test is about — a create belongs to the daemon whose filesystem
		// holds its root dir — is unchanged either way.
		projects:      []*ProjectModel{{ID: "proj-local"}},
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

	// Opening the dialog must replace the stale value SYNCHRONOUSLY, before the
	// (unresolved, in this test) browse response for B ever lands — with B's
	// own root, not with nothing: empty is not a safe placeholder here, since
	// UpdateProject has no unchanged-value guard and would erase the field.
	if m.cwdBrowseDir != "/src/project-b" {
		t.Fatalf("cwdBrowseDir = %q immediately after opening rename, want proj-b's own "+
			"root — project A's must not survive into B's dialog", m.cwdBrowseDir)
	}
	if !m.browse.pending {
		t.Fatal("browse.pending = false, want true — the root-dir request for B is in flight")
	}

	// The natural "open rename, fix the name, press Enter" flow: cursor
	// starts on the Name field (0), and Enter there submits directly —
	// before B's browse response has arrived.
	_, submitCmd := m.handleProjectDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if submitCmd != nil {
		submitCmd()
	}

	// The submit is NOT blocked — waiting made renaming a remote project
	// impossible, because its browse takes seconds and the button did nothing
	// visible until it answered. What matters is which root dir goes out, and
	// seeding the field from the project itself is what makes an early submit
	// correct rather than merely permitted.
	if len(fake.sent) != 1 {
		t.Fatalf("sent %d messages, want 1 — an early submit must go through", len(fake.sent))
	}
	var payload ipc.UpdateProjectPayload
	fake.sent[0].DecodePayload(&payload)
	if payload.ProjectID != "proj-b" {
		t.Fatalf("payload targets %q, want proj-b", payload.ProjectID)
	}
	if payload.RootDir == "/src/project-a" {
		t.Fatalf("submitted project A's root as B's — the scratch value survived into B's dialog")
	}
	if payload.RootDir != "/src/project-b" {
		t.Fatalf("RootDir = %q, want proj-b's own root: an empty one ERASES it, since "+
			"UpdateProject has no unchanged-value guard", payload.RootDir)
	}
}

// The Host field's whole point: create a project on a machine that is NOT the
// one currently on screen, without leaving the TUI. The root dir beside it was
// browsed on that host's filesystem, so sending the create anywhere else pairs
// a name with a path that does not exist there.
func TestSubmitNewProject_HostFieldOverridesTheActiveDest(t *testing.T) {
	fake := newFakeConn()
	m := Model{
		client:            fake,
		projects:          []*ProjectModel{{ID: "proj-local", Dest: ""}},
		activeProject:     0,
		projectFormRemote: true,
		projectFormHost:   "gpu01",
		projectFormDest:   "gpu01", // set once the dial landed
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
		client:            NewRouter(map[string]Client{"": newFakeConn(), "gpu01": newFakeConn()}),
		projectFormRemote: true,
		projectFormHost:   "gpu01",
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

// The Remote toggle gates the ssh rows, and the visible-row list is what the
// cursor, key dispatch and render all index into — so a mismatch here is the
// class of bug where the highlight sits on one field and typing lands in
// another.
func TestProjectFormVisibleRows_ToggleRevealsTheSSHFields(t *testing.T) {
	local := Model{}
	if got := local.projectFormVisibleRows(); len(got) != 4 {
		t.Errorf("local form has %d rows, want 4 (name, remote, root, submit)", len(got))
	}
	remote := Model{projectFormRemote: true}
	rows := remote.projectFormVisibleRows()
	if len(rows) != 6 {
		t.Fatalf("remote form has %d rows, want 6", len(rows))
	}
	if rows[2] != projectRowUser || rows[3] != projectRowHost {
		t.Errorf("ssh rows land at %v, want user then host between remote and root", rows[2:4])
	}
	// Submit is always last and root always immediately above it — the arm
	// that moves up from Submit relies on that positionally.
	if rows[len(rows)-1] != projectRowSubmit || rows[len(rows)-2] != projectRowRootDir {
		t.Error("root directory must sit directly above submit in both layouts")
	}
}

func TestProjectFormDest_ComposesUserAtHost(t *testing.T) {
	tests := []struct {
		name             string
		remote           bool
		user, host, want string
	}{
		{"local ignores the fields", false, "artyom", "gpu01", ""},
		{"host only", true, "", "gpu01", "gpu01"},
		{"user and host", true, "artyom", "gpu01", "artyom@gpu01"},
		{"blank host is not a destination", true, "artyom", "  ", ""},
		{"whitespace trimmed", true, " artyom ", " gpu01 ", "artyom@gpu01"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{projectFormRemote: tc.remote, projectFormUser: tc.user, projectFormHost: tc.host}
			if got := m.projectFormDestFromFields(); got != tc.want {
				t.Errorf("dest = %q, want %q", got, tc.want)
			}
		})
	}
}

// Turning Remote off must drop the destination too. A hidden field that still
// decided where the project landed would be worse than no toggle at all.
func TestProjectRemoteToggle_OffReturnsTheFormToLocal(t *testing.T) {
	m := Model{
		client:            NewRouter(map[string]Client{"": newFakeConn()}),
		projectFormRemote: true,
		projectFormUser:   "artyom",
		projectFormHost:   "gpu01",
		projectFormDest:   "artyom@gpu01",
	}
	m.projectFormCursor = 1 // the Remote row in the remote layout
	next, _ := m.handleProjectRemoteKey("space")
	got := next.(Model)

	if got.projectFormRemote {
		t.Fatal("toggle did not turn off")
	}
	if got.projectFormDest != "" || got.projectFormHost != "" || got.projectFormUser != "" {
		t.Errorf("dest=%q host=%q user=%q — turning Remote off must return the form to this machine",
			got.projectFormDest, got.projectFormHost, got.projectFormUser)
	}
}

// Rename pre-fills from a stored dest, so the split has to be the exact
// inverse of the compose — including a user part that itself contains "@".
func TestSplitSSHDest_RoundTrips(t *testing.T) {
	for _, dest := range []string{"gpu01", "artyom@gpu01", "artyom@corp.example@gpu01", ""} {
		user, host := splitSSHDest(dest)
		m := Model{projectFormRemote: dest != "", projectFormUser: user, projectFormHost: host}
		if got := m.projectFormDestFromFields(); got != dest {
			t.Errorf("split(%q) → user=%q host=%q → %q, want the original", dest, user, host, got)
		}
	}
}

// A dial that STILL reports the binary missing right after a successful
// install means something the install cannot fix — it landed outside the
// non-interactive PATH, or the recorded path never reached the dialer. Without
// a guard that is a loop: install, retry, 127, install. Observed running every
// five seconds against a host whose config record the dialer never saw.
func TestDestDialed_InstallsAtMostOncePerHost(t *testing.T) {
	installs := 0
	m := Model{
		client:             NewRouter(map[string]Client{"": newFakeConn()}),
		projectFormDialing: "gpu01",
		installDestFn: func(string) error {
			installs++
			return nil
		},
	}

	// First failure offers the install.
	next, cmd := m.Update(destDialedMsg{dest: "gpu01", err: ErrRemoteQuilMissing})
	m = next.(Model)
	if cmd != nil {
		cmd()
	}
	if installs != 1 {
		t.Fatalf("installs = %d after the first missing-binary dial, want 1", installs)
	}

	// The retry dial fails the same way. This must REPORT, not reinstall.
	m.projectFormDialing = "gpu01"
	next2, cmd2 := m.Update(destDialedMsg{dest: "gpu01", err: ErrRemoteQuilMissing})
	got := next2.(Model)
	if cmd2 != nil {
		cmd2()
	}
	if installs != 1 {
		t.Errorf("installs = %d after a second missing-binary dial, want 1 — this is the loop", installs)
	}
	if got.projectFormErr == "" {
		t.Error("the second failure must say something rather than silently retrying")
	}
}

// A remote daemon too old for this client to attach to must be UPGRADED from
// the dialog, not reported with a command to run somewhere else.
//
// The mismatch cannot reach here as ErrRemoteQuilMissing: quil ran over there,
// so the link delivered bytes and no exit code can classify it. Reported as
// "I expected the install to happen during remote project creation" — the user
// saw the raw gate message with `quil remote setup` in it.
func TestDestDialed_UpgradesADaemonThisClientCannotAttachTo(t *testing.T) {
	installs := 0
	m := Model{
		client:             NewRouter(map[string]Client{"": newFakeConn()}),
		projectFormDialing: "gpu01",
		installDestFn:      func(string) error { installs++; return nil },
	}
	dialErr := fmt.Errorf("%w: gpu01 runs 1.46.3, this client runs 1.47.0", ErrRemoteVersionMismatch)

	next, cmd := m.Update(destDialedMsg{dest: "gpu01", err: dialErr})
	got := next.(Model)
	if cmd != nil {
		cmd()
	}
	if installs != 1 {
		t.Fatalf("installs = %d, want 1 — a version mismatch is exactly what the remote setup fixes", installs)
	}
	if got.projectFormInstalling != "gpu01" {
		t.Errorf("projectFormInstalling = %q, want the host being upgraded", got.projectFormInstalling)
	}
	// An upgrade is not free the way a first install is: the daemon over there
	// stops and whatever was running in its shells dies, so the message owes
	// more than "installing".
	if !strings.Contains(got.projectFormErr, "daemon restarts") {
		t.Errorf("projectFormErr = %q, want the daemon restart named", got.projectFormErr)
	}

	// The retry reports the same mismatch: the daemon did not restart, and
	// pushing the same archive again cannot change that. Same guard as the
	// missing-binary loop, deliberately shared.
	got.projectFormDialing = "gpu01"
	next2, cmd2 := got.Update(destDialedMsg{dest: "gpu01", err: dialErr})
	if cmd2 != nil {
		cmd2()
	}
	if installs != 1 {
		t.Errorf("installs = %d after a second mismatched dial, want 1 — this is the loop", installs)
	}
	if next2.(Model).projectFormErr == "" {
		t.Error("the second failure must say something rather than silently retrying")
	}
}

// End-to-end through the REAL key handler: name, remote toggle, user, host,
// connect, then Create. Reported twice as "I gave a name but the project is
// still called Default" — where Default is the remote daemon's own bootstrap
// project, i.e. the create never arrived.
func TestProjectForm_RemoteFlowSendsTheCreate(t *testing.T) {
	conn := newFakeConn()
	remote := newFakeConn()
	m := Model{
		client:     NewRouter(map[string]Client{"": conn}),
		projects:   []*ProjectModel{{ID: "p-local", Name: "local"}},
		dialDestFn: func(string) (Client, error) { return remote, nil },
	}

	tm, _ := m.openNewProjectDialog()
	m = tm.(Model)
	m.projectFormName = "cluster-management"

	// Space on the Remote row.
	m.projectFormCursor = 1
	tm, _ = m.handleProjectDialogKey(tea.KeyPressMsg{Code: ' ', Text: " "})
	m = tm.(Model)
	if !m.projectFormRemote {
		t.Fatal("Space on the Remote row did not toggle it")
	}

	m.projectFormUser, m.projectFormHost = "build", "gpu01"
	// Enter on the Host row connects.
	m.projectFormCursor = 3
	tm, cmd := m.handleProjectDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = tm.(Model)
	if cmd == nil {
		t.Fatal("Enter on the Host row started no dial")
	}
	dialed := cmd()
	tm, _ = m.Update(dialed)
	m = tm.(Model)
	if m.projectFormDest != "build@gpu01" {
		t.Fatalf("projectFormDest = %q after a successful dial", m.projectFormDest)
	}

	// The browse the dial kicked off resolves, so the submit gate opens.
	m.applyBrowseDir(ipc.BrowseDirRespPayload{
		Path: "", Resolved: "/srv/cluster",
	}, m.browse.gen)

	// Tab to Create and press it.
	for m.projectFormRowKind() != projectRowSubmit {
		tm, _ = m.handleProjectDialogKey(tea.KeyPressMsg{Code: tea.KeyTab})
		m = tm.(Model)
	}
	_, submit := m.handleProjectDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if submit != nil {
		submit()
	}

	var created *ipc.Message
	for _, sent := range remote.sent {
		if sent.Type == ipc.MsgCreateProject {
			created = sent
		}
	}
	if created == nil {
		t.Fatalf("no create reached the remote daemon (err=%q) — the project the user "+
			"named is never made, so the only one they see is the daemon's own Default",
			m.projectFormErr)
	}
	var payload ipc.CreateProjectPayload
	created.DecodePayload(&payload)
	if payload.Name != "cluster-management" {
		t.Errorf("created name = %q, want the name the user typed", payload.Name)
	}
}
