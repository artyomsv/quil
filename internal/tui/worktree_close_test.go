package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// worktreePane builds a pane the daemon reported as owning a worktree.
func worktreePane(id, cwd, branch string) *PaneModel {
	p := &PaneModel{ID: id, CWD: cwd, WorktreeOwned: true, GitBranch: branch, GitWorktree: true}
	return p
}

// The gate the daemon enforces, mirrored client-side so the row is only ever
// OFFERED for a worktree Quil made. A pane sitting in a worktree the user
// created by hand carries no WorktreeOwned, and the dialog must stay silent
// about it — the option existing at all is a claim that deleting is Quil's to
// offer.
func TestCollectConfirmWorktrees_OnlyPanesQuilMadeAWorktreeFor(t *testing.T) {
	got := collectConfirmWorktrees([]*PaneModel{
		worktreePane("pane-a", "/w/feat-a", "feat/a"),
		{ID: "pane-b", CWD: "/w/hand-made", GitWorktree: true},
		{ID: "pane-c", CWD: "/repo"},
	})

	if len(got) != 1 {
		t.Fatalf("collectConfirmWorktrees = %+v, want one entry", got)
	}
	if got[0].path != "/w/feat-a" {
		t.Errorf("path = %q, want /w/feat-a", got[0].path)
	}
}

// Two panes split inside one worktree describe ONE directory. Listing it twice
// would show the user "2 worktrees" for a single checkout and ask the daemon
// about it twice.
func TestCollectConfirmWorktrees_DeduplicatesOneWorktreesPanes(t *testing.T) {
	got := collectConfirmWorktrees([]*PaneModel{
		worktreePane("pane-a", "/w/feat-a", "feat/a"),
		worktreePane("pane-b", "/w/feat-a", "feat/a"),
	})
	if len(got) != 1 {
		t.Errorf("collectConfirmWorktrees = %+v, want one entry", got)
	}
}

// OFF on open, every time. This is the whole safety property of the feature:
// the destructive half must be something the user reached for, never something
// they inherited from the last dialog or confirmed by muscle memory.
func TestOpenClosePaneConfirm_ArmsNothingByDefault(t *testing.T) {
	m := modelWithWorktreePane()
	m.confirmRemoveWorktree = true // left over from a previous confirm

	got := applyModel(m.openClosePaneConfirm())

	if got.confirmRemoveWorktree {
		t.Error("the close confirm opened with the worktree toggle already armed")
	}
	if len(got.confirmWorktrees) != 1 {
		t.Fatalf("confirmWorktrees = %+v, want the pane's worktree", got.confirmWorktrees)
	}
	if got.confirmWorktrees[0].path != "/w/feat-a" {
		t.Errorf("path = %q, want /w/feat-a", got.confirmWorktrees[0].path)
	}
}

// An ordinary pane's close dialog is unchanged — no row, nothing to toggle, and
// no status request to the daemon.
func TestOpenClosePaneConfirm_OrdinaryPaneOffersNothing(t *testing.T) {
	m := modelWithPanes(&PaneModel{ID: "pane-plain", CWD: "/repo"})

	got := applyModel(m.openClosePaneConfirm())

	if len(got.confirmWorktrees) != 0 {
		t.Errorf("confirmWorktrees = %+v, want none for a pane with no worktree", got.confirmWorktrees)
	}
}

// A tab is closed as a unit and its worktrees go as one toggle, so the dialog
// has to collect every owned checkout in the tab rather than the active pane's.
func TestOpenCloseTabConfirm_CollectsEveryWorktreeInTheTab(t *testing.T) {
	m := modelWithPanes(
		worktreePane("pane-a", "/w/feat-a", "feat/a"),
		worktreePane("pane-b", "/w/feat-b", "feat/b"),
	)

	got := applyModel(m.openCloseTabConfirm())

	if len(got.confirmWorktrees) != 2 {
		t.Fatalf("confirmWorktrees = %+v, want both worktrees", got.confirmWorktrees)
	}
}

func TestConfirmKey_SpaceTogglesTheWorktreeRow(t *testing.T) {
	m := modelWithWorktreePane()
	m = applyModel(m.openClosePaneConfirm())

	m = applyModel(m.handleConfirmKey(keyPressFor(" ")))
	if !m.confirmRemoveWorktree {
		t.Fatal("space did not arm the worktree toggle")
	}
	m = applyModel(m.handleConfirmKey(keyPressFor(" ")))
	if m.confirmRemoveWorktree {
		t.Error("space did not disarm the worktree toggle")
	}
}

// The flag has to reach the wire, and it is the only thing on it that says
// "delete a directory".
func TestConfirmKey_EnterSendsTheRemoveFlagWhenArmed(t *testing.T) {
	m := modelWithWorktreePane()
	m = applyModel(m.openClosePaneConfirm())
	m = applyModel(m.handleConfirmKey(keyPressFor(" ")))

	fake := &fakeSender{}
	m.client = fake
	_, cmd := m.handleConfirmKey(keyPressFor("enter"))
	if cmd != nil {
		cmd()
	}

	var payload ipc.DestroyPanePayload
	if !decodeSent(t, fake, ipc.MsgDestroyPane, &payload) {
		t.Fatal("no destroy_pane was sent")
	}
	if !payload.RemoveWorktree {
		t.Error("the armed toggle did not reach the wire")
	}
}

// And the default close stays exactly what it has always been.
func TestConfirmKey_EnterSendsNoRemoveFlagByDefault(t *testing.T) {
	m := modelWithWorktreePane()
	m = applyModel(m.openClosePaneConfirm())

	fake := &fakeSender{}
	m.client = fake
	_, cmd := m.handleConfirmKey(keyPressFor("enter"))
	if cmd != nil {
		cmd()
	}

	var payload ipc.DestroyPanePayload
	if !decodeSent(t, fake, ipc.MsgDestroyPane, &payload) {
		t.Fatal("no destroy_pane was sent")
	}
	if payload.RemoveWorktree {
		t.Error("an untouched dialog asked the daemon to delete a worktree")
	}
}

// Cancelling must leave nothing armed behind it. The confirm fields are Model
// state that outlives the dialog, and an armed toggle inherited by the NEXT
// close is a deletion the user never saw offered.
func TestConfirmKey_EscapeClearsTheWorktreeState(t *testing.T) {
	m := modelWithWorktreePane()
	m = applyModel(m.openClosePaneConfirm())
	m = applyModel(m.handleConfirmKey(keyPressFor(" ")))

	m = applyModel(m.handleConfirmKey(keyPressFor("esc")))

	if m.confirmRemoveWorktree || len(m.confirmWorktrees) != 0 {
		t.Errorf("esc left worktree state behind: armed=%v rows=%+v",
			m.confirmRemoveWorktree, m.confirmWorktrees)
	}
}

// The count is what the user weighs the toggle against, so it has to reach the
// row it describes.
func TestApplyWorktreeStatus_LandsTheCountOnItsRow(t *testing.T) {
	m := modelWithWorktreePane()
	m = applyModel(m.openClosePaneConfirm())

	m.applyWorktreeStatus(worktreeStatusMsg{
		Gen: m.confirmWorktreeGen,
		Resp: ipc.WorktreeStatusRespPayload{
			Paths: []string{"/w/feat-a"},
			Statuses: []ipc.WorktreeStatus{
				{Path: "/w/feat-a", Changes: 3},
			},
		},
	})

	if !m.confirmWorktrees[0].loaded || m.confirmWorktrees[0].changes != 3 {
		t.Errorf("row = %+v, want 3 changes loaded", m.confirmWorktrees[0])
	}
}

// A response from a PREVIOUS dialog must not paint the current one. The user
// can close one pane and immediately open the confirm for another, and the two
// requests are indistinguishable on the wire but for the generation.
func TestApplyWorktreeStatus_DropsAStaleGeneration(t *testing.T) {
	m := modelWithWorktreePane()
	m = applyModel(m.openClosePaneConfirm())

	m.applyWorktreeStatus(worktreeStatusMsg{
		Gen: m.confirmWorktreeGen + "-old",
		Resp: ipc.WorktreeStatusRespPayload{
			Paths:    []string{"/w/feat-a"},
			Statuses: []ipc.WorktreeStatus{{Path: "/w/feat-a", Changes: 99}},
		},
	})

	if m.confirmWorktrees[0].loaded {
		t.Errorf("a stale response painted the row: %+v", m.confirmWorktrees[0])
	}
}

// "Could not check" and "clean" are different answers and only one of them
// invites the toggle. A timeout must produce the first.
func TestApplyWorktreeStatusTimeout_DoesNotClaimClean(t *testing.T) {
	m := modelWithWorktreePane()
	m = applyModel(m.openClosePaneConfirm())

	m.applyWorktreeStatusTimeout(worktreeStatusTimeoutMsg{gen: m.confirmWorktreeGen})

	row := m.confirmWorktrees[0]
	if !row.loaded || row.err == "" {
		t.Errorf("row = %+v, want a loaded row carrying a reason", row)
	}
	if row.changes != 0 {
		t.Errorf("row = %+v, want no fabricated count", row)
	}
}

func TestRenderConfirmDialog_ShowsTheWorktreeRowAndItsWarning(t *testing.T) {
	m := modelWithWorktreePane()
	m = applyModel(m.openClosePaneConfirm())
	m.confirmWorktrees[0].loaded = true
	m.confirmWorktrees[0].changes = 3

	out := stripANSI(m.renderConfirmDialog())

	if !strings.Contains(out, "[ ]") {
		t.Errorf("no unticked checkbox in the dialog:\n%s", out)
	}
	if !strings.Contains(out, "feat/a") {
		t.Errorf("the worktree is not named in the dialog:\n%s", out)
	}
	if !strings.Contains(out, "3") || !strings.Contains(strings.ToLower(out), "uncommitted") {
		t.Errorf("the dialog does not say what would be lost:\n%s", out)
	}
}

func TestRenderConfirmDialog_ShowsTheArmedCheckbox(t *testing.T) {
	m := modelWithWorktreePane()
	m = applyModel(m.openClosePaneConfirm())
	m = applyModel(m.handleConfirmKey(keyPressFor(" ")))

	out := stripANSI(m.renderConfirmDialog())

	if !strings.Contains(out, "[x]") {
		t.Errorf("the armed toggle does not render as ticked:\n%s", out)
	}
}

// An ordinary close dialog is untouched: no checkbox, and no footer hint for a
// key that would do nothing.
func TestRenderConfirmDialog_OrdinaryCloseIsUnchanged(t *testing.T) {
	m := modelWithPanes(&PaneModel{ID: "pane-plain"})
	m.dialog = dialogConfirm
	m.confirmKind = "pane"
	m.confirmName = "shell"

	out := stripANSI(m.renderConfirmDialog())

	if strings.Contains(out, "[ ]") || strings.Contains(strings.ToLower(out), "worktree") {
		t.Errorf("a pane with no worktree got the worktree row:\n%s", out)
	}
}

// modelWithPanes builds a one-tab model holding the given panes, the first of
// them active.
func modelWithPanes(panes ...*PaneModel) Model {
	return Model{
		projects:      []*ProjectModel{{ID: "proj-a", tabs: []*TabModel{tabWith(panes...)}}},
		activeProject: 0,
		termFocused:   true,
	}
}

// modelWithWorktreePane builds a model whose active pane owns /w/feat-a.
func modelWithWorktreePane() Model {
	return modelWithPanes(worktreePane("pane-a", "/w/feat-a", "feat/a"))
}

// applyModel runs the (tea.Model, tea.Cmd) pair every handler here returns and
// gives back the concrete Model. The Cmd is deliberately dropped: these tests
// assert on state, and the send paths take a fakeSender where they matter.
func applyModel(mdl tea.Model, _ tea.Cmd) Model {
	m, _ := mdl.(Model)
	return m
}

// decodeSent finds the last message of the given type on the fake and decodes
// its payload. Reports whether one was found.
func decodeSent(t *testing.T, fake *fakeSender, msgType string, out any) bool {
	t.Helper()
	for i := len(fake.sent) - 1; i >= 0; i-- {
		if fake.sent[i].Type != msgType {
			continue
		}
		if err := fake.sent[i].DecodePayload(out); err != nil {
			t.Fatalf("decode %s: %v", msgType, err)
		}
		return true
	}
	return false
}
