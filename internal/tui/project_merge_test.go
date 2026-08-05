package tui

import (
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
	// The message adds fixed prose around the names; two caps' worth of slack
	// covers it without admitting an uncapped 4000-char name.
	limit := 2*formMsgNameCap + formMsgDetailCap

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
