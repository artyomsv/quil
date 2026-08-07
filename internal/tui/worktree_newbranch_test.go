package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

func newBranchModel(t *testing.T) Model {
	t.Helper()
	// Built on the split-drag fixture because handleCreatePaneSplit needs a
	// real active pane to split: a tab with none returns nil before it ever
	// reaches the payload, so a bare model would make every wire assertion
	// below vacuously "pass" by never running.
	// A worktree create arms a give-up tick alongside its send, and runCmd
	// executes the whole batch — so at the production value every wire test
	// here would block for two and a half minutes.
	prevTimeout := createPaneTimeout
	createPaneTimeout = time.Millisecond
	t.Cleanup(func() { createPaneTimeout = prevTimeout })

	m := *newSplitDragTestModel(t)
	m.width, m.height = 120, 44
	// setupDialogWidth reads the registry to size the box against toggle
	// labels; an empty one answers nil and takes the floor, which is what
	// every fixture here wants.
	m.pluginRegistry = plugin.NewRegistry()
	// The browsed directory is a SUBDIRECTORY of the repository, which is the
	// ordinary case (the CWD browser starts from the active pane's cwd) and
	// the one that distinguishes the two values. `git worktree list` succeeds
	// from any subdirectory, so the field is offered here — and deriving the
	// worktree path from this would nest a second checkout inside the repo.
	m.cwdBrowseDir = "/repo/internal/tui"
	m.worktrees = worktreeState{
		loaded: true,
		repo:   true,
		root:   "/repo",
		list: []ipc.WorktreeInfo{
			{Path: "/repo", Branch: "master", Main: true},
			{Path: "/repo-worktrees/feat-a", Branch: "feat-a"},
		},
	}
	return m
}

// worktreeRowIndex finds a row by PATH rather than by position.
//
// Every fixture below used a literal index, so moving one row broke eight
// unrelated tests at once — and the row moved because it was placed last to
// spare those fixtures in the first place, which is the tail wagging the dog.
// Looking the row up means the order is free to change and only the test that
// asserts the order has to care.
func worktreeRowIndex(t *testing.T, m Model, path string) int {
	t.Helper()
	for i, r := range m.worktreeRows() {
		if r.path == path {
			return i
		}
	}
	t.Fatalf("no worktree row with path %q", path)
	return -1
}

// "+ new branch…" is the FIRST actionable row, directly under "off". A
// repository with a dozen worktrees would otherwise bury the row reached for
// most; "off" stays above it because it is the default rather than a choice.
func TestWorktreeRows_NewBranchIsFirstAction(t *testing.T) {
	m := newBranchModel(t)
	rows := m.worktreeRows()
	if len(rows) < 2 {
		t.Fatal("no worktree rows")
	}
	if rows[0].path != "" {
		t.Error("the off row is no longer first")
	}
	if !strings.Contains(rows[1].label, "new branch") {
		t.Errorf("row 1 is %q, want the new-branch row", rows[1].label)
	}
	if rows[1].path != worktreeNewRowPath {
		t.Errorf("row 1 path = %q, want the sentinel", rows[1].path)
	}
	// The existing worktrees follow it, in listing order.
	if rows[2].path != "/repo-worktrees/feat-a" {
		t.Errorf("row 2 = %q, want the first existing worktree", rows[2].path)
	}
}

// Enter on the row opens the NAME field rather than committing: the branch is
// the identity of the work, so the row means nothing until it is typed.
func TestWorktreeNewBranch_EnterOpensTheNameField(t *testing.T) {
	m := newBranchModel(t)
	p := &plugin.PanePlugin{Name: "terminal"}
	m.worktreeCursor = worktreeRowIndex(t, m, worktreeNewRowPath)

	updated, _ := m.handleSetupWorktreeKey(p, "enter")
	got := updated.(Model)
	if !got.worktreeNaming {
		t.Fatal("Enter on the new-branch row did not open the name field")
	}
	if got.selectedWorktree != "" {
		t.Errorf("selectedWorktree = %q, want it cleared", got.selectedWorktree)
	}
}

// While naming, j/k are LETTERS. They must reach the name, not the cursor —
// which is why naming swallows the list keys rather than sharing a handler.
func TestWorktreeNewBranch_TypingDoesNotMoveTheCursor(t *testing.T) {
	m := newBranchModel(t)
	p := &plugin.PanePlugin{Name: "terminal"}
	m.worktreeNaming = true
	before := m.worktreeCursor

	for _, k := range []string{"j", "k", "f", "i", "x"} {
		updated, _ := m.handleSetupWorktreeKey(p, k)
		m = updated.(Model)
	}
	if m.worktreeNewBranch != "jkfix" {
		t.Errorf("worktreeNewBranch = %q, want \"jkfix\"", m.worktreeNewBranch)
	}
	if m.worktreeCursor != before {
		t.Errorf("cursor moved to %d while typing a name", m.worktreeCursor)
	}
}

// Validated in the dialog as well as daemon-side. The daemon is the authority,
// but a round trip to learn about a typo is a bad dialog, and here the message
// lands beside the field the user typed into.
func TestWorktreeNewBranch_EnterRefusesAnInvalidName(t *testing.T) {
	m := newBranchModel(t)
	p := &plugin.PanePlugin{Name: "terminal"}
	m.worktreeNaming = true
	m.worktreeNewBranch = "-b"

	updated, _ := m.handleSetupWorktreeKey(p, "enter")
	got := updated.(Model)
	if got.worktreeErr == "" {
		t.Error("a flag-shaped branch name was accepted with no message")
	}
	if !got.worktreeNaming {
		t.Error("the name field closed on an invalid name")
	}
}

func TestWorktreeNewBranch_EnterAcceptsAValidName(t *testing.T) {
	m := newBranchModel(t)
	p := &plugin.PanePlugin{Name: "terminal"}
	m.worktreeNaming = true
	m.worktreeNewBranch = "feat/x"

	updated, _ := m.handleSetupWorktreeKey(p, "enter")
	got := updated.(Model)
	if got.worktreeErr != "" {
		t.Errorf("worktreeErr = %q for a valid name", got.worktreeErr)
	}
	if got.worktreeNaming {
		t.Error("the name field stayed open after accepting")
	}
	if got.worktreeNewBranch != "feat/x" {
		t.Errorf("worktreeNewBranch = %q, want it kept", got.worktreeNewBranch)
	}
}

// Esc abandons the row entirely rather than merely closing the input: a
// half-typed name left behind would be committed by the next Enter on
// Continue, spawning a pane on a branch the user backed out of.
func TestWorktreeNewBranch_EscAbandonsTheName(t *testing.T) {
	m := newBranchModel(t)
	p := &plugin.PanePlugin{Name: "terminal"}
	m.worktreeNaming = true
	m.worktreeNewBranch = "feat/x"

	updated, _ := m.handleSetupWorktreeKey(p, "esc")
	got := updated.(Model)
	if got.worktreeNaming {
		t.Error("Esc left the name field open")
	}
	if got.worktreeNewBranch != "" {
		t.Errorf("worktreeNewBranch = %q after Esc, want it cleared", got.worktreeNewBranch)
	}
}

// Choosing an EXISTING worktree must clear a previously typed new branch: the
// two are mutually exclusive, and a leftover name would win at submit.
func TestWorktreeNewBranch_ChoosingAnExistingWorktreeClearsIt(t *testing.T) {
	m := newBranchModel(t)
	p := &plugin.PanePlugin{Name: "terminal"}
	m.worktreeNewBranch = "feat/x"
	m.worktreeCursor = worktreeRowIndex(t, m, "/repo-worktrees/feat-a")

	updated, _ := m.handleSetupWorktreeKey(p, "enter")
	got := updated.(Model)
	if got.worktreeNewBranch != "" {
		t.Errorf("worktreeNewBranch = %q after picking an existing worktree", got.worktreeNewBranch)
	}
	if got.selectedWorktree != "/repo-worktrees/feat-a" {
		t.Errorf("selectedWorktree = %q, want the picked worktree", got.selectedWorktree)
	}
}

// The state belongs to the repository that was on screen when it was typed.
// Leaving it behind would branch from a repository the dialog no longer shows
// — the third recurrence of this class in one feature, which is why the reset
// lives in ONE function every directory-committing path routes through.
func TestOnSetupCWDChanged_ClearsTheNewBranch(t *testing.T) {
	m := newBranchModel(t)
	m.worktreeNewBranch = "feat/x"
	m.worktreeNaming = true
	m.worktreeErr = "stale"
	m.selectedWorktree = "/repo-worktrees/feat-a"

	m.onSetupCWDChanged("/other-repo")

	if m.worktreeNewBranch != "" || m.worktreeNaming || m.worktreeErr != "" {
		t.Errorf("new-branch state survived a directory change: branch=%q naming=%v err=%q",
			m.worktreeNewBranch, m.worktreeNaming, m.worktreeErr)
	}
	if m.selectedWorktree != "" {
		t.Errorf("selectedWorktree = %q after a directory change", m.selectedWorktree)
	}
}

// The invariant restated as a check anyone can run: no setup-dialog path may
// assign cwdBrowseDir directly, because that is how the reset gets skipped.
func TestSetupDialog_NoBareCWDBrowseDirAssignment(t *testing.T) {
	// Enforced by grep in review rather than here — this test exists to carry
	// the rule where a reader of the worktree code will see it:
	//
	//   grep -rn 'm\.cwdBrowseDir = ' internal/tui/ | grep -v onSetupCWDChanged
	//
	// must return nothing outside onSetupCWDChanged's own body.
	t.Skip("documented invariant; see the grep in the comment")
}

// The name field REPLACES the list rather than sitting under it, so focusing
// the field cannot grow the dialog past the terminal — lipgloss.Place does not
// clip, and an over-tall dialog pushes [Continue] off screen.
// Swept across heights rather than sampled: worktreeVisibleRows floors at 1,
// so the failing band is at the SHORT end, which a single comfortable fixture
// height steps straight over. Stage A shipped exactly that mistake once.
func TestRenderSetupWorktreeField_NamingDoesNotGrowTheField(t *testing.T) {
	for h := 20; h <= 60; h++ {
		m := newBranchModel(t)
		m.height = h
		listRows := strings.Count(m.renderSetupWorktreeField(true), "\n")

		m.worktreeNaming = true
		m.worktreeErr = "branch name may not start with \"-\""
		nameRows := strings.Count(m.renderSetupWorktreeField(true), "\n")

		if nameRows > listRows {
			t.Errorf("h=%d: naming renders %d rows, more than the list's %d", h, nameRows, listRows)
		}
	}
}

// The collapsed summary must say WHICH branch will be created, or the user
// tabs away and loses sight of what they asked for.
func TestRenderSetupWorktreeField_SummaryNamesTheNewBranch(t *testing.T) {
	m := newBranchModel(t)
	m.worktreeNewBranch = "feat/x"

	out := stripANSI(m.renderSetupWorktreeField(false))
	if !strings.Contains(out, "feat/x") {
		t.Errorf("collapsed summary %q does not name the new branch", out)
	}
}

// The spec must actually REACH the wire. handleCreatePaneSplit tears the setup
// dialog down before it builds the payload, so reading the branch name after
// that reset yields "" — and every "new branch" would silently spawn an
// ordinary pane in the repository root, which is the exact relocation this
// feature exists to prevent. Caught by driving the real handler, not by
// inspecting state.
func TestHandleCreatePaneSplit_SendsTheWorktreeSpec(t *testing.T) {
	m := newBranchModel(t)
	fake := &fakeSender{}
	m.client = fake
	m.selectedPlugin = "terminal"
	m.selectedCWD = "/repo"
	m.worktreeNewBranch = "feat/x"
	m.dialogCursor = 0 // split horizontal

	_, cmd := m.handleCreatePaneSplit()
	if cmd == nil {
		t.Fatal("no command returned — nothing was sent")
	}
	runCmd(cmd)

	if len(fake.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(fake.sent))
	}
	var p ipc.CreatePanePayload
	if err := fake.sent[0].DecodePayload(&p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Worktree == nil {
		t.Fatal("the create carried no worktree spec — the teardown cleared it first")
	}
	if p.Worktree.Branch != "feat/x" {
		t.Errorf("branch = %q, want feat/x", p.Worktree.Branch)
	}
	if p.Worktree.RepoRoot != "/repo" {
		t.Errorf("repo root = %q, want /repo", p.Worktree.RepoRoot)
	}
}

// An ordinary create must stay wire-identical: no spec, so the daemon takes
// its unchanged synchronous path.
func TestHandleCreatePaneSplit_NoSpecWithoutANewBranch(t *testing.T) {
	m := newBranchModel(t)
	fake := &fakeSender{}
	m.client = fake
	m.selectedPlugin = "terminal"
	m.selectedCWD = "/repo"
	m.dialogCursor = 0

	_, cmd := m.handleCreatePaneSplit()
	if cmd == nil {
		t.Fatal("no command returned")
	}
	runCmd(cmd)

	var p ipc.CreatePanePayload
	if err := fake.sent[0].DecodePayload(&p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Worktree != nil {
		t.Errorf("an ordinary create carried a worktree spec: %+v", p.Worktree)
	}
}

// The teardown must clear the worktree state, or the NEXT Ctrl+N inherits the
// branch name and spawns in a repository the dialog is no longer showing.
func TestHandleCreatePaneSplit_ClearsTheWorktreeStateAfterwards(t *testing.T) {
	m := newBranchModel(t)
	m.client = &fakeSender{}
	m.selectedPlugin = "terminal"
	m.selectedCWD = "/repo"
	m.worktreeNewBranch = "feat/x"
	m.selectedWorktree = "/repo-worktrees/feat-a"
	m.dialogCursor = 0

	updated, _ := m.handleCreatePaneSplit()
	got := updated.(Model)

	if got.worktreeNewBranch != "" || got.selectedWorktree != "" {
		t.Errorf("worktree state survived the teardown: branch=%q selected=%q",
			got.worktreeNewBranch, got.selectedWorktree)
	}
}

// Replace mode with a new worktree used to be refused here. It is supported
// now — swapping a scratch shell for an agent in a fresh branch is an ordinary
// thing to want, and the hazard the refusal named (a failed add costing a live
// pane) was about WHEN the client disposed the old pane, not about the
// operation. See worktree_replace_test.go, which pins the send, the restore on
// failure and on timeout, and the dispose on success.

// The spec must carry the REPOSITORY ROOT the daemon reported, never the
// browsed directory. `git worktree list` succeeds from any subdirectory, so
// the field is offered while browsing <repo>/internal/tui — and deriving from
// that puts a full second checkout at <repo>/internal/tui-worktrees/<branch>,
// NESTED inside the first. A `git clean -xfd` in the main checkout then
// deletes another pane's live work, and every tree-walking tool traverses it.
//
// protocol.go states the contract: the client must never compute this value.
func TestHandleCreatePaneSplit_SendsTheRepoRootNotTheBrowsedDir(t *testing.T) {
	m := newBranchModel(t)
	fake := &fakeSender{}
	m.client = fake
	m.selectedPlugin = "terminal"
	m.selectedCWD = m.cwdBrowseDir
	m.worktreeNewBranch = "feat/x"
	m.dialogCursor = 0

	_, cmd := m.handleCreatePaneSplit()
	runCmd(cmd)

	if len(fake.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(fake.sent))
	}
	var p ipc.CreatePanePayload
	if err := fake.sent[0].DecodePayload(&p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Worktree == nil {
		t.Fatal("no worktree spec sent")
	}
	if p.Worktree.RepoRoot != "/repo" {
		t.Errorf("RepoRoot = %q, want the repository root %q — the browsed dir would nest the worktree",
			p.Worktree.RepoRoot, "/repo")
	}
}

// With no listing answered yet the repository root is unknown, and falling
// back to the browsed directory is exactly the nesting bug. Refuse instead.
func TestHandleCreatePaneSplit_RefusesWhenTheRepoRootIsUnknown(t *testing.T) {
	m := newBranchModel(t)
	fake := &fakeSender{}
	m.client = fake
	m.selectedPlugin = "terminal"
	m.selectedCWD = "/repo/internal/tui"
	m.worktreeNewBranch = "feat/x"
	m.worktrees.root = "" // listing never answered
	m.dialogCursor = 0

	updated, cmd := m.handleCreatePaneSplit()
	runCmd(cmd)
	got := updated.(Model)

	for _, sent := range fake.sent {
		var p ipc.CreatePanePayload
		if err := sent.DecodePayload(&p); err == nil && p.Worktree != nil {
			t.Errorf("a create was sent with an unknown repo root: %+v", p.Worktree)
		}
	}
	if got.flashText == "" {
		t.Error("the refusal was silent")
	}
}
