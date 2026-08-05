package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// mergeFormModel is the state a user meets on a host that predates the
// one-project-per-host rule: three projects, two of them indistinguishable.
// Mirrors a real reported host — a renamed workspace holding the work, plus two
// strays from creates that landed beside a bootstrap the client could not see.
func mergeFormModel(remote *fakeConn) Model {
	return Model{
		client: NewRouter(map[string]Client{"": newFakeConn(), "gpu01": remote}),
		projects: []*ProjectModel{
			{ID: "proj-keep", Name: "Default1", Dest: "gpu01", RootDir: "/home/a/.quil",
				tabs: []*TabModel{{ID: "tab-1"}, {ID: "tab-2"}}},
			{ID: "proj-dup-a", Name: "cluster-management", Dest: "gpu01",
				tabs: []*TabModel{{ID: "tab-3"}}},
			{ID: "proj-dup-b", Name: "cluster-management", Dest: "gpu01",
				tabs: []*TabModel{{ID: "tab-4"}}},
		},
		projectFormDest: "gpu01",
		dialog:          dialogProjectNew,
	}
}

// The confirmation gate is about what is ON SCREEN, not only about the plan.
//
// The armed plan and the displayed message are independent fields, and the Name
// row clears the message on a keystroke without clearing the plan while
// backspace restores neither. Two keystrokes therefore returned the form to a
// state where the plan matched, nothing sameAs compares had changed, and the
// warning line was blank — and the next Enter folded the host with no
// confirmation anywhere on screen.
func TestSubmitNewProject_DoesNotFoldWhileTheWarningIsOffScreen(t *testing.T) {
	remote := newFakeConn()
	m := mergeFormModel(remote)

	m.submitNewProject("cluster-management", "/srv") // arms and describes
	// Type a character and delete it again: the form is byte-for-byte back where
	// it was, except that the message line is now empty.
	m.projectFormName = "cluster-managementx"
	m.projectFormErr = ""
	m.projectFormName = "cluster-management"

	m.submitNewProject("cluster-management", "/srv")

	if got := len(sentOfType(t, remote, ipc.MsgMergeProjects)); got != 0 {
		t.Errorf("sent %d merges with a blank message line — the two-Enter design "+
			"exists to make the user read the consequence, and this fold was "+
			"confirmed against nothing", got)
	}
	if m.projectFormErr == "" {
		t.Error("re-arming did not put the warning back, so the next Enter has " +
			"nothing to confirm either")
	}
}

// Same hole reached without touching the fold's own fields: a dial started after
// arming replaces the warning with "connecting to …" while projectFormDest still
// names the OLD host, so every field sameAs compares is unchanged.
func TestSubmitNewProject_DoesNotFoldWhileAnotherMessageIsShowing(t *testing.T) {
	remote := newFakeConn()
	m := mergeFormModel(remote)

	m.submitNewProject("cluster-management", "/srv")
	m.setFormBusy("connecting to buildbox…")

	m.submitNewProject("cluster-management", "/srv")

	if got := len(sentOfType(t, remote, ipc.MsgMergeProjects)); got != 0 {
		t.Errorf("sent %d merges while the screen read \"connecting to buildbox…\" — "+
			"the user confirmed a sentence describing different work", got)
	}
}

// A conn that is PRESENT but refuses the write must not close the dialog
// either. Distinct from the host having gone away entirely, which the
// reachability guard catches earlier: here the router has the dest, the send is
// attempted, and ipc.Conn.Send returns an error — a queue overflow, or a conn
// closed under us.
func TestSubmitNewProject_AFailedSendKeepsTheDialogAndReports(t *testing.T) {
	failing := &failingConn{fakeConn: newFakeConn()}
	m := mergeFormModel(newFakeConn())
	m.client = NewRouter(map[string]Client{"": newFakeConn(), "gpu01": failing})

	m.submitNewProject("cluster-management", "/srv")
	m.submitNewProject("cluster-management", "/srv")

	if m.dialog == dialogNone {
		t.Error("the dialog closed on a fold whose write was refused, so the host " +
			"is presented as tidied while its duplicates are all still there")
	}
	if m.projectFormMsgKind != projectFormMsgError {
		t.Errorf("kind = %d, want error — the fold did not happen", m.projectFormMsgKind)
	}
	// The next Enter RE-ARMS rather than retrying: setFormError replaced the
	// warning, so foldIsConfirmed refuses until the consequence is back on
	// screen. Pinned because the message used to promise "Enter retries", which
	// was wrong by exactly one Enter.
	m.submitNewProject("cluster-management", "/srv")
	if m.projectFormMsgKind != projectFormMsgWarn {
		t.Errorf("kind = %d after the next Enter, want the warning back", m.projectFormMsgKind)
	}
	if strings.Contains(m.projectFormErr, "retries") {
		t.Errorf("message = %q promises a retry the next keystroke does not perform",
			m.projectFormErr)
	}
}

// failingConn is a conn the router HAS but which refuses every write.
type failingConn struct {
	*fakeConn
}

func (c *failingConn) Send(*ipc.Message) error { return errSendRefused }

var errSendRefused = errors.New("send refused")

// The second Enter is the one that acts, and it must send exactly the fold the
// message described.
func TestSubmitNewProject_ConfirmingSendsTheFold(t *testing.T) {
	remote := newFakeConn()
	m := mergeFormModel(remote)

	m.submitNewProject("cluster-management", "/home/a/homelab") // arms
	m.submitNewProject("cluster-management", "/home/a/homelab") // confirms

	sent := sentOfType(t, remote, ipc.MsgMergeProjects)
	if len(sent) != 1 {
		t.Fatalf("sent %d merges, want 1", len(sent))
	}
	var p ipc.MergeProjectsPayload
	if err := sent[0].DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	if p.ProjectID != "proj-keep" {
		t.Errorf("survivor = %q, want proj-keep", p.ProjectID)
	}
	if len(p.Absorb) != 2 {
		t.Errorf("absorb = %v, want both duplicates — folding one of three still "+
			"leaves the pair the user cannot tell apart", p.Absorb)
	}
	if p.Name != "cluster-management" || p.RootDir != "/home/a/homelab" {
		t.Errorf("name=%q root=%q, want both from the form", p.Name, p.RootDir)
	}
	if m.dialog != dialogNone {
		t.Error("the dialog stayed open after the fold was sent")
	}
}

// Naming the host after the duplicate being folded away is the ORDINARY way out
// of this state, so the create-path duplicate-name guard must not see it: every
// project carrying that name is one this message absorbs.
func TestSubmitNewProject_FoldAcceptsAnAbsorbedProjectsName(t *testing.T) {
	remote := newFakeConn()
	m := mergeFormModel(remote)

	m.submitNewProject("cluster-management", "/srv")
	m.submitNewProject("cluster-management", "/srv")

	if got := len(sentOfType(t, remote, ipc.MsgMergeProjects)); got != 1 {
		t.Fatalf("sent %d merges, want 1 — the name belongs to a project being "+
			"absorbed, so refusing it refuses the only way out of the duplicate", got)
	}
}

// The armed plan is re-derived and COMPARED, never simply consumed. Editing the
// name between the two Enters must re-arm with the new sentence rather than
// carry out the one the user stopped agreeing with.
func TestSubmitNewProject_ChangingTheNameReArmsInsteadOfFolding(t *testing.T) {
	remote := newFakeConn()
	m := mergeFormModel(remote)

	m.submitNewProject("cluster-management", "/srv")
	m.submitNewProject("something-else", "/srv")

	if got := len(remote.sent); got != 0 {
		t.Errorf("sent %d messages, want none — the second Enter carried out a plan "+
			"the user had already edited away from", got)
	}
	if m.projectFormMerge == nil || m.projectFormMerge.name != "something-else" {
		t.Errorf("plan = %+v, want one re-armed on the new name", m.projectFormMerge)
	}
	if !strings.Contains(m.projectFormErr, "something-else") {
		t.Errorf("message = %q, want it to quote the name now typed", m.projectFormErr)
	}
}

// A host whose SET of duplicates changed — one destroyed, another appearing,
// counts coincidentally equal — must re-arm on membership alone.
//
// This is the only thing pinning sameAs's per-element comparison; every other
// test differs in length, so dropping the loop passed the whole package. It is
// also the case with no visual signal whatsoever: the several-projects message
// names only counts, so a correct re-arm renders byte-identically and the user
// cannot tell the difference. The comparison is the entire defence.
func TestSubmitNewProject_ReArmsWhenTheSameNumberOfDifferentProjectsIsThere(t *testing.T) {
	remote := newFakeConn()
	m := mergeFormModel(remote)

	m.submitNewProject("cluster-management", "/srv")
	// One stray destroyed, a different one created: still three projects.
	m.projects[2] = &ProjectModel{
		ID: "proj-dup-c", Name: "cluster-management", Dest: "gpu01",
		tabs: []*TabModel{{ID: "tab-9"}},
	}
	m.submitNewProject("cluster-management", "/srv")

	sent := sentOfType(t, remote, ipc.MsgMergeProjects)
	if len(sent) != 0 {
		t.Fatalf("sent %d merges against a different set of projects than the one "+
			"the armed plan described", len(sent))
	}
	if m.projectFormMerge == nil || m.projectFormMerge.absorb[1] != "proj-dup-c" {
		t.Errorf("plan = %+v, want it re-armed on the projects now present",
			m.projectFormMerge)
	}
}

// A host that disconnects while the dialog is open must not have the fold turn
// into something else.
//
// Disconnect drops the host's projects client-side, so the recompute finds none
// and falls through to the generic create at the bottom of submitNewProject —
// the user confirms a FOLD and the client attempts a CREATE, which Router.Send
// then drops silently for a dest it no longer has a conn for, closing the dialog
// as though the fold had happened.
func TestSubmitNewProject_RefusesWhenTheHostWentAwayMidConfirmation(t *testing.T) {
	remote := newFakeConn()
	m := mergeFormModel(remote)
	m.submitNewProject("cluster-management", "/srv") // arm

	// The host goes: its conn is gone and its projects with it.
	m.client = NewRouter(map[string]Client{"": newFakeConn()})
	m.projects = nil

	m.submitNewProject("cluster-management", "/srv")

	if got := len(remote.sent); got != 0 {
		t.Errorf("sent %d messages to a host that is no longer connected", got)
	}
	if m.dialog == dialogNone {
		t.Error("the dialog closed, presenting the fold as done")
	}
	if m.projectFormMsgKind != projectFormMsgError {
		t.Errorf("kind = %d, want error — the host is unreachable", m.projectFormMsgKind)
	}
	if m.projectFormMerge != nil {
		t.Error("the plan is still armed for a host that is gone")
	}
}

// A host whose projects changed under the user — another client renamed one,
// or a broadcast added one — must re-arm too. The plan carries the survivor's
// name for exactly this reason: it appears in the message.
func TestSubmitNewProject_AHostThatChangedUnderTheUserReArms(t *testing.T) {
	remote := newFakeConn()
	m := mergeFormModel(remote)

	m.submitNewProject("infra", "/srv")
	// A broadcast lands between the two keystrokes.
	m.projects[0].Name = "renamed-by-someone-else"
	m.submitNewProject("infra", "/srv")

	if got := len(remote.sent); got != 0 {
		t.Errorf("sent %d messages, want none — the message named a project that "+
			"had since been renamed, so the user confirmed a stale sentence", got)
	}
}

// An empty root directory keeps the survivor's own. submitProjectForm does not
// wait for the browse, and Enter on the Name row submits at once, so empty is
// reachable — and the fold ends in a rename with no unchanged-value guard on the
// far side, exactly as the adopt path does.
func TestSubmitNewProject_FoldKeepsTheSurvivorsRootWhenTheBrowseHasNotAnswered(t *testing.T) {
	remote := newFakeConn()
	m := mergeFormModel(remote)

	m.submitNewProject("cluster-management", "")
	m.submitNewProject("cluster-management", "")

	sent := sentOfType(t, remote, ipc.MsgMergeProjects)
	if len(sent) != 1 {
		t.Fatalf("sent %d merges, want 1", len(sent))
	}
	var p ipc.MergeProjectsPayload
	if err := sent[0].DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	if p.RootDir != "/home/a/.quil" {
		t.Errorf("RootDir = %q, want the survivor's own — an empty one ERASES it, "+
			"and new panes and the git subsystem both read it", p.RootDir)
	}
}

// The message states the consequence, because that is what the user is being
// asked to agree to: how many projects there are, what the result is called,
// how many tabs move, and that nothing is closed.
func TestProjectMergePlan_MessageStatesTheConsequence(t *testing.T) {
	m := mergeFormModel(newFakeConn())

	m.submitNewProject("cluster-management", "/srv")

	for _, want := range []string{"gpu01", "3 projects", "cluster-management", "2 tabs", "nothing closes"} {
		if !strings.Contains(m.projectFormErr, want) {
			t.Errorf("message = %q, missing %q", m.projectFormErr, want)
		}
	}
}

// The message must name EVERYTHING the plan can change, or recompute-and-compare
// is defeated by a silent field.
//
// The root directory is the case that proved it. The dialog's own opening browse
// resolves an EMPTY path, so the daemon answers with its default CWD and
// applyBrowseListing writes that into cwdBrowseDir — a directory nobody picked.
// A plan armed before that lands carries the survivor's root and one armed after
// carries the daemon's default, so the second Enter re-armed correctly behind a
// message whose text had not changed by one character: three Enters, two
// identical sentences, and a real project's root replaced.
func TestProjectMergePlan_MessageNamesARootThatMoves(t *testing.T) {
	t.Run("silent when the root stays put", func(t *testing.T) {
		m := mergeFormModel(newFakeConn())

		// Empty root falls back to the survivor's own, so nothing moves.
		m.submitNewProject("cluster-management", "")

		if strings.Contains(m.projectFormErr, "root directory") {
			t.Errorf("message = %q, want no mention of a root that is not moving — "+
				"the value is already on the Root directory row above", m.projectFormErr)
		}
	})

	t.Run("named when the browse lands on the daemon's default", func(t *testing.T) {
		m := mergeFormModel(newFakeConn())

		// What the opening browse delivers: a resolved path nobody chose.
		m.submitNewProject("cluster-management", "/home/build")

		if !strings.Contains(m.projectFormErr, "/home/build") {
			t.Errorf("message = %q, want it to say the root becomes /home/build — "+
				"otherwise the re-armed sentence is identical to the one before it "+
				"and the user confirms a change they were never shown",
				m.projectFormErr)
		}
	})
}

// A remote daemon chooses its own project names, and this is the one
// value-bearing row in the dialog with no truncation of its own — lipgloss WRAPS
// at the box width, so an unbounded name becomes thousands of rendered lines in
// every frame.
//
// BOTH interpolations are covered, because they appear on different branches and
// only one of them is remote-chosen on the obvious reading. The survivor's name
// comes straight off the wire and is quoted only when a host holds ONE project —
// so a test written against the three-project fixture (the interesting case) is
// the one that never touches it, and passed with the cap removed.
func TestProjectMergePlan_BoundsEveryInterpolatedName(t *testing.T) {
	long := strings.Repeat("x", 4000)
	// The message adds fixed prose around the names and may name a moving root;
	// this much slack covers all of it without admitting an uncapped 4000-char
	// value. The host is deliberately NOT bounded and cannot be: Message.Origin
	// is json:"-" and Router stamps ProjectModel.Dest from its own map key, so a
	// dest is always the user's own ssh destination, never remote-supplied.
	limit := 2*formMsgNameCap + formMsgPathCap + formMsgDetailCap

	t.Run("the survivor's name, chosen by the remote daemon", func(t *testing.T) {
		m := Model{
			client:          NewRouter(map[string]Client{"": newFakeConn(), "gpu01": newFakeConn()}),
			projects:        []*ProjectModel{{ID: "proj-one", Name: long, Dest: "gpu01"}},
			projectFormDest: "gpu01",
			dialog:          dialogProjectNew,
		}

		m.submitNewProject("infra", "/srv")

		if len(m.projectFormErr) > limit {
			t.Errorf("message is %d chars for a %d-char remote name — it wraps the "+
				"box into thousands of lines", len(m.projectFormErr), len(long))
		}
	})

	t.Run("the typed name, which a paste can make just as long", func(t *testing.T) {
		m := mergeFormModel(newFakeConn())

		m.submitNewProject(long, "/srv")

		if len(m.projectFormErr) > limit {
			t.Errorf("message is %d chars for a %d-char typed name", len(m.projectFormErr), len(long))
		}
	})
}

// Reopening the form must disarm. The confirming Enter fires when the recomputed
// plan MATCHES the armed one, and reopening against the same host and typing the
// same name reproduces an identical plan — so a plan that outlived its dialog
// would fold on the first Enter of the next session, having never shown the user
// the sentence it was confirming.
func TestOpenNewProjectDialog_DisarmsAPendingFold(t *testing.T) {
	remote := newFakeConn()
	m := mergeFormModel(remote)
	m.submitNewProject("cluster-management", "/srv")
	if m.projectFormMerge == nil {
		t.Fatal("nothing was armed, so this test proves nothing")
	}

	next, _ := m.openNewProjectDialog()
	reopened := next.(Model)
	reopened.submitNewProject("cluster-management", "/srv")

	if got := len(sentOfType(t, remote, ipc.MsgMergeProjects)); got != 0 {
		t.Errorf("sent %d merges on the FIRST Enter of a reopened form — the plan "+
			"survived the close and was confirmed by a keystroke that should have "+
			"armed it", got)
	}
}
