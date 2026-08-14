package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// worktreePane builds a pane the daemon reported as owning a worktree, sitting
// in it — the ordinary case, where the recorded creation path and the shell's
// CWD still agree. Tests about them DISAGREEING override WorktreePath.
func worktreePane(id, cwd, branch string) *PaneModel {
	return &PaneModel{
		ID: id, CWD: cwd, WorktreeOwned: true, WorktreePath: cwd,
		GitBranch: branch, GitWorktree: true,
	}
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

// The status request leaves inside a tea.Batch (alongside ClearScreen), and
// calling a batched cmd does NOT run its children — it returns a BatchMsg the
// runtime dispatches. runCmd (overlay_test.go) unwraps that; calling the outer
// cmd alone observes no sends at all.

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

// The client has to price the SAME directory the daemon will delete, and a
// pane's CWD is not that directory: OSC 7 rewrites it on every `cd`, so a pane
// created in feat-a whose shell walked into feat-b would have the dialog name
// feat-b's branch, run `git status` against feat-b, and arm a toggle the daemon
// answers by deleting feat-a. Both sides read the recorded creation path.
func TestCollectConfirmWorktrees_UsesTheRecordedPathNotTheShellsCWD(t *testing.T) {
	p := worktreePane("pane-a", "/w/feat-b", "feat/b") // the shell wandered
	p.WorktreePath = "/w/feat-a"

	got := collectConfirmWorktrees([]*PaneModel{p})

	if len(got) != 1 {
		t.Fatalf("collectConfirmWorktrees = %+v, want one entry", got)
	}
	if got[0].path != "/w/feat-a" {
		t.Errorf("path = %q, want the recorded /w/feat-a", got[0].path)
	}
}

// A pane restored from a snapshot written before the path was recorded carries
// ownership and no path. It is not offered — the daemon will refuse it anyway,
// and a row that cannot be honoured is worse than no row.
func TestCollectConfirmWorktrees_SkipsAPaneWithNoRecordedPath(t *testing.T) {
	p := worktreePane("pane-a", "/w/feat-a", "feat/a")
	p.WorktreePath = ""

	if got := collectConfirmWorktrees([]*PaneModel{p}); len(got) != 0 {
		t.Errorf("collectConfirmWorktrees = %+v, want none without a recorded path", got)
	}
}

// The dialog box does not clip and lipgloss WRAPS, so an unbounded row list
// grows the box past the terminal and pushes the footer — and the Enter it
// documents — off screen. A tab can legitimately hold many worktree panes.
func TestRenderConfirmDialog_CapsTheWorktreeRowList(t *testing.T) {
	var panes []*PaneModel
	for i := range 40 {
		id := fmt.Sprintf("pane-%02d", i)
		p := worktreePane(id, fmt.Sprintf("/w/feat-%02d", i), fmt.Sprintf("feat/%02d", i))
		p.WorktreePath = p.CWD
		panes = append(panes, p)
	}
	m := modelWithPanes(panes...)
	m = applyModel(m.openCloseTabConfirm())

	out := stripANSI(m.renderConfirmDialog())

	if lines := strings.Count(out, "\n") + 1; lines > confirmWorktreeMaxRows*2+12 {
		t.Errorf("the dialog rendered %d lines for 40 worktrees; the row list is not capped:\n%s", lines, out)
	}
	if !strings.Contains(out, "more") {
		t.Errorf("the capped rows are not accounted for in the dialog:\n%s", out)
	}
	// The COUNT still describes every worktree — the cap is a display bound,
	// not a change to what the toggle does.
	if !strings.Contains(out, "40 worktrees") {
		t.Errorf("the header does not name all 40 worktrees:\n%s", out)
	}
}

// The label has four tiers and only the first was exercised. The row is how the
// user recognises WHICH piece of work they are deleting the checkout of, so a
// tier that silently produced an empty string would leave them confirming a
// blank line.
func TestConfirmWorktreeLabel_FallsBackThroughItsTiers(t *testing.T) {
	tests := []struct {
		name string
		pane *PaneModel
		want string
	}{
		{
			"the branch, when the pane is still in its own worktree",
			&PaneModel{CWD: "/w/feat-a", WorktreePath: "/w/feat-a", GitBranch: "feat/a", GitWorktreeName: "feat-a"},
			"feat/a",
		},
		{
			"the worktree name when git reports no branch (detached HEAD)",
			&PaneModel{CWD: "/w/feat-a", WorktreePath: "/w/feat-a", GitWorktreeName: "feat-a"},
			"feat-a",
		},
		{
			"the path when git has reported nothing yet",
			&PaneModel{CWD: "/w/feat-a", WorktreePath: "/w/feat-a"},
			"/w/feat-a",
		},
		{
			// The git fields describe wherever the shell went, so once they
			// disagree with the recorded path they name the WRONG worktree —
			// the row must not label this "feat/b" while the close deletes
			// feat-a.
			"the recorded path outright once the shell has wandered",
			&PaneModel{CWD: "/w/feat-b", WorktreePath: "/w/feat-a", GitBranch: "feat/b"},
			"/w/feat-a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := confirmWorktreeLabel(tt.pane); got != tt.want {
				t.Errorf("confirmWorktreeLabel = %q, want %q", got, tt.want)
			}
		})
	}
}

// The REQUEST itself, not just the state it leaves behind. Every other test
// here asserts on m.confirmWorktrees and never runs the returned command, so a
// regression that asked the daemon about the wrong directories — every pane's
// CWD rather than the owned worktrees — would pass all of them, and the
// daemon-side dispatch test assumes a well-formed request it never sees built.
func TestOpenClosePaneConfirm_AsksTheDaemonAboutTheRecordedWorktree(t *testing.T) {
	p := worktreePane("pane-a", "/w/feat-b", "feat/b") // the shell wandered
	p.WorktreePath = "/w/feat-a"
	m := modelWithPanes(p)
	fake := &fakeSender{}
	m.client = fake

	mdl, cmd := m.openClosePaneConfirm()
	got := applyModel(mdl, cmd)
	if cmd == nil {
		t.Fatal("no command returned, so no status request was ever sent")
	}
	runCmd(cmd)

	var req ipc.WorktreeStatusReqPayload
	if !decodeSent(t, fake, ipc.MsgWorktreeStatusReq, &req) {
		t.Fatal("no worktree_status_req was sent")
	}
	if len(req.Paths) != 1 || req.Paths[0] != "/w/feat-a" {
		t.Errorf("Paths = %v, want the recorded worktree /w/feat-a", req.Paths)
	}
	// The generation is what applyWorktreeStatus matches the answer on; a
	// request that carries a different one can never be landed.
	var stamped string
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgWorktreeStatusReq {
			stamped = msg.ID
		}
	}
	if stamped == "" || stamped != got.confirmWorktreeGen {
		t.Errorf("request ID = %q, want the dialog's generation %q", stamped, got.confirmWorktreeGen)
	}
}

// Every line the confirm dialog draws must fit its content width. lipgloss pads
// but never clips, so an over-wide line WRAPS — stranding half the footer on a
// row the box never budgeted, on top of the worktree list it now carries. The
// footer this feature added was 59 cells against a 54-cell budget.
func TestRenderConfirmDialog_EveryLineFitsTheBox(t *testing.T) {
	p := worktreePane("pane-a", "/w/feat-a", "feat/a")
	m := modelWithPanes(p)
	m = applyModel(m.openClosePaneConfirm())
	m.confirmWorktrees[0].loaded = true
	m.confirmWorktrees[0].changes = 3

	budget := dialogWidth - dialogBoxChrome
	for _, line := range strings.Split(stripANSI(m.renderConfirmDialog()), "\n") {
		if w := lipgloss.Width(line); w > budget {
			t.Errorf("line is %d cells, over the %d-cell budget — lipgloss will wrap it:\n%q", w, budget, line)
		}
	}
}

// A drifted pane labels its row with the PATH, and a path is identified by its
// TAIL — truncateCells cuts the tail, so a plain truncation leaves every
// worktree under one parent looking identical (`E:\Projects\…\quil-worktrees\…`).
func TestRenderConfirmDialog_KeepsTheIdentifyingEndOfALongPath(t *testing.T) {
	p := worktreePane("pane-a", "/elsewhere", "feat/b")
	p.WorktreePath = `E:\Projects\Stukans\quil-worktrees\feat-the-actual-branch`
	m := modelWithPanes(p)
	m = applyModel(m.openClosePaneConfirm())

	out := stripANSI(m.renderConfirmDialog())

	if !strings.Contains(out, "feat-the-actual-branch") {
		t.Errorf("the row does not show the part of the path that identifies the worktree:\n%s", out)
	}
}

// Every confirm opener clears the worktree state, not just the close ones.
// Today the terminal branches of handleConfirmKey reset it and the invariant
// holds globally — but "instance" renders through the same default arm, so an
// exit path added later that skips the reset would paint worktree rows on an
// unrelated confirm. Clearing at each opener makes it local instead.
func TestOpenRestartPaneConfirm_ClearsAnyWorktreeStateFromABeforeDialog(t *testing.T) {
	m := modelWithWorktreePane()
	m = applyModel(m.openClosePaneConfirm())
	m = applyModel(m.handleConfirmKey(keyPressFor(" ")))

	got := applyModel(m.openRestartPaneConfirm())

	if len(got.confirmWorktrees) != 0 || got.confirmRemoveWorktree {
		t.Errorf("the restart confirm inherited worktree state: rows=%+v armed=%v",
			got.confirmWorktrees, got.confirmRemoveWorktree)
	}
}
