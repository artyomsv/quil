package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
)

// stubAdd swaps the worktree-creation seam for the test's duration.
func stubAdd(t *testing.T, fn func(ctx context.Context, repo, path, branch string) error) *[][]string {
	t.Helper()
	var calls [][]string
	prev := addWorktreeFn
	addWorktreeFn = func(ctx context.Context, repo, path, branch string) error {
		calls = append(calls, []string{repo, path, branch})
		return fn(ctx, repo, path, branch)
	}
	t.Cleanup(func() { addWorktreeFn = prev })
	return &calls
}

func worktreeCreate(tabID, repo, branch string) ipc.CreatePanePayload {
	return ipc.CreatePanePayload{
		TabID:    tabID,
		Type:     "terminal",
		Worktree: &ipc.WorktreeSpec{RepoRoot: repo, Branch: branch},
	}
}

// The whole point of the feature: an add that fails must produce NO pane and
// must never relocate to the repository root. A pane on master that the user
// believes is isolated is the confidently-wrong answer this design exists to
// remove.
func TestWorktreeAdd_FailureCreatesNoPaneAndDoesNotRelocate(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	before := len(d.session.Panes(tab.ID))

	stubAdd(t, func(context.Context, string, string, string) error {
		return errors.New("fatal: '/x/feat-y' already exists")
	})

	resp := d.worktreeAddAndCreate(worktreeCreate(tab.ID, "/repo", "feat/x"))

	if resp.Error == "" {
		t.Error("a failed add reported no error")
	}
	if resp.PaneID != "" {
		t.Errorf("a failed add created pane %q", resp.PaneID)
	}
	if got := len(d.session.Panes(tab.ID)); got != before {
		t.Errorf("pane count %d, want %d — a failed add created a pane anyway", got, before)
	}
}

// git's own stderr must reach the user: "already used by worktree '/x/feat-y'"
// names the pane to go look at, and no message Quil could invent would.
func TestWorktreeAdd_CarriesGitsOwnMessage(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	stubAdd(t, func(context.Context, string, string, string) error {
		return errors.New("fatal: 'feat/x' is already used by worktree at '/x/feat-y'")
	})

	resp := d.worktreeAddAndCreate(worktreeCreate(tab.ID, "/repo", "feat/x"))
	if !strings.Contains(resp.Error, "/x/feat-y") {
		t.Errorf("error %q drops git's own message", resp.Error)
	}
}

// The spec is echoed verbatim on the failure path: the client matches its
// armed layout placeholder on it, and a dropped echo leaves that placeholder
// armed forever, swallowing the next pane created in the tab.
func TestWorktreeAdd_EchoesTheSpecOnEveryPath(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	stubAdd(t, func(context.Context, string, string, string) error { return errors.New("nope") })

	resp := d.worktreeAddAndCreate(worktreeCreate(tab.ID, "/repo", "feat/x"))
	if resp.Worktree == nil {
		t.Fatal("no spec echoed on the failure path")
	}
	if resp.Worktree.Branch != "feat/x" || resp.Worktree.RepoRoot != "/repo" {
		t.Errorf("spec = %+v, want {/repo feat/x}", resp.Worktree)
	}
}

// Validation runs BEFORE any repository write, so a bad name costs no git
// invocation, no permit, and no single-flight slot — and never reaches argv.
func TestWorktreeAdd_RejectsABadBranchWithoutRunningGit(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	calls := stubAdd(t, func(context.Context, string, string, string) error { return nil })

	resp := d.worktreeAddAndCreate(worktreeCreate(tab.ID, "/repo", "-b"))
	if resp.Error == "" {
		t.Error("a flag-shaped branch name was accepted")
	}
	if len(*calls) != 0 {
		t.Errorf("git ran %d times for a name that failed validation", len(*calls))
	}
}

// A repo root the client never filled in must not reach DerivePath, which
// would resolve it against the daemon's own working directory.
func TestWorktreeAdd_RejectsAnEmptyRepoRoot(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	calls := stubAdd(t, func(context.Context, string, string, string) error { return nil })

	resp := d.worktreeAddAndCreate(worktreeCreate(tab.ID, "", "feat/x"))
	if resp.Error == "" {
		t.Error("an empty repo root was accepted")
	}
	if len(*calls) != 0 {
		t.Errorf("git ran %d times with no repository named", len(*calls))
	}
}

// A tab destroyed while the add ran must not receive a pane. The window is
// SECONDS wide here — a checkout, not a fork — where an ordinary create's is
// microseconds.
func TestWorktreeAdd_RefusesAVanishedTab(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	stubAdd(t, func(context.Context, string, string, string) error {
		d.session.DestroyTab(tab.ID) // the tab goes while the checkout runs
		return nil
	})

	resp := d.worktreeAddAndCreate(worktreeCreate(tab.ID, "/repo", "feat/x"))
	if resp.Error == "" {
		t.Error("a create into a destroyed tab was accepted")
	}
	if resp.PaneID != "" {
		t.Errorf("pane %q created in a destroyed tab", resp.PaneID)
	}
}

// Replace mode is REFUSED. The client destroys its model of the old pane
// before the send (leaf.Pane = nil + Dispose), so a failed add there costs a
// live pane — and unlike a dangling placeholder, PrunePlaceholders cannot undo
// a Dispose(). The dialog omits the field; this is the daemon half of that,
// because any IPC client can send the pair.
// Replacing a pane with one in a NEW worktree is supported: swapping a scratch
// shell for an agent in a fresh branch is an ordinary thing to want. It was
// refused at first because the client destroyed the old pane before the send,
// which is a property of WHEN the client disposed rather than of the operation.
func TestWorktreeAdd_ReplaceModeIsAccepted(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	// A real repo root under TempDir, because createPaneInWorktree STATS the
	// derived path — the pane must land in a directory that exists, which is
	// the guarantee it enforces. The stub stands in for git by creating it.
	repo := filepath.Join(t.TempDir(), "proj", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	old, err := d.session.CreatePane(tab.ID, repo)
	if err != nil {
		t.Fatalf("seed pane: %v", err)
	}
	calls := stubAdd(t, func(_ context.Context, _, path, _ string) error {
		return os.MkdirAll(path, 0o755)
	})

	p := worktreeCreate(tab.ID, repo, "feat/x")
	p.ReplacePaneID = old.ID
	resp := d.worktreeAddAndCreate(p)

	if resp.Error != "" {
		t.Fatalf("a worktree replace was refused: %s", resp.Error)
	}
	if len(*calls) != 1 {
		t.Fatalf("git ran %d times, want 1", len(*calls))
	}
	if resp.PaneID == "" {
		t.Fatal("no pane was reported")
	}
	if resp.PaneID == old.ID {
		t.Error("the reported pane is the one being replaced")
	}
	if d.session.Pane(old.ID) != nil {
		t.Error("the replaced pane is still in the session")
	}
	if got := d.session.Pane(resp.PaneID); got == nil {
		t.Error("the new pane is not in the session")
	} else if !got.WorktreeOwned {
		t.Error("the new pane is not marked WorktreeOwned")
	}
}

// The ordering that makes the above safe: git runs BEFORE the pane is touched,
// so an add that fails must leave the pane being replaced exactly where it was.
// This is the property the original refusal was protecting, kept as a test now
// that the operation is allowed.
func TestWorktreeAdd_ReplaceFailureLeavesTheOldPaneAlone(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	repo := filepath.Join(t.TempDir(), "proj", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	old, err := d.session.CreatePane(tab.ID, repo)
	if err != nil {
		t.Fatalf("seed pane: %v", err)
	}
	before := len(d.session.Panes(tab.ID))
	stubAdd(t, func(context.Context, string, string, string) error {
		return errors.New("fatal: a branch named 'feat/x' already exists")
	})

	p := worktreeCreate(tab.ID, repo, "feat/x")
	p.ReplacePaneID = old.ID
	resp := d.worktreeAddAndCreate(p)

	if resp.Error == "" {
		t.Fatal("a failed add reported success")
	}
	if resp.PaneID != "" {
		t.Errorf("a failed add created pane %q", resp.PaneID)
	}
	if d.session.Pane(old.ID) == nil {
		t.Error("a failed worktree add DESTROYED the pane it was going to replace")
	}
	if got := len(d.session.Panes(tab.ID)); got != before {
		t.Errorf("pane count %d, want %d", got, before)
	}
}

// worktreeAdding is its OWN slot. The dialog LISTS a directory's worktrees and
// then CREATES one, so a shared guard would reject each step exactly when it
// followed the other — the same reason dirsChecking is not browseScanning.
//
// Claimed through beginWorktreeAdd rather than by setting the atomic directly,
// so the handler's own claim path is what is under test: a test that sets the
// field stays green when the handler is pointed at the wrong slot.
func TestWorktreeAdd_SlotIsIndependentOfTheListingSlot(t *testing.T) {
	d := newTestDaemon(t)
	d.worktreeScanning.Store(true) // a listing is in flight
	if !d.beginWorktreeAdd() {
		t.Error("an add was refused while only a LISTING held its slot")
	}
	d.endWorktreeAdd()
}

// Two adds at once are refused rather than queued: the second would hold a
// blocking-FS permit for the add's long timeout, and that serialisation IS the
// permit budget for this path.
func TestWorktreeAdd_SecondConcurrentAddIsRefused(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	if !d.beginWorktreeAdd() {
		t.Fatal("could not claim the add slot")
	}
	t.Cleanup(d.endWorktreeAdd)

	resp := d.worktreeAddAndCreate(worktreeCreate(tab.ID, "/repo", "feat/x"))
	if resp.Error == "" {
		t.Error("a second concurrent add was accepted")
	}
	if resp.Worktree == nil {
		t.Error("the rejection dropped the spec echo — the client would never unwind")
	}
}

// The add must target the path DerivePath chose, not the browsed directory:
// the sibling location is the whole point of the layout decision.
func TestWorktreeAdd_UsesTheDerivedSiblingPath(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	calls := stubAdd(t, func(context.Context, string, string, string) error {
		return errors.New("stop before the pane is created")
	})

	d.worktreeAddAndCreate(worktreeCreate(tab.ID, "/home/u/repo", "feat/x"))

	if len(*calls) != 1 {
		t.Fatalf("got %d git invocations, want 1", len(*calls))
	}
	want := gitworktree.DerivePath("/home/u/repo", "feat/x")
	if (*calls)[0][1] != want {
		t.Errorf("add path = %q, want %q", (*calls)[0][1], want)
	}
	if (*calls)[0][0] != "/home/u/repo" {
		t.Errorf("add ran in %q, want the repository root", (*calls)[0][0])
	}
}

// The guarantee, at the point it can actually be broken: the add REPORTS
// success but the directory is not there — a slow network mount, a drive that
// unmounted between the checkout and the spawn. The surrounding create path
// answers that by substituting the daemon's own directory, which here would
// spawn the agent on master while the user believes it is isolated. So: no
// pane, no relocation, an error the client can show.
func TestWorktreeAdd_MissingWorktreeAfterAddIsAFailureNotAFallback(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	before := len(d.session.Panes(tab.ID))

	// Reports success and creates nothing.
	stubAdd(t, func(context.Context, string, string, string) error { return nil })

	resp := d.worktreeAddAndCreate(worktreeCreate(tab.ID, filepath.Join(t.TempDir(), "repo"), "feat/x"))

	if resp.Error == "" {
		t.Error("a worktree that is not on disk after the add was accepted")
	}
	if resp.PaneID != "" {
		t.Errorf("pane %q created for a worktree that does not exist", resp.PaneID)
	}
	if got := len(d.session.Panes(tab.ID)); got != before {
		t.Errorf("pane count %d, want %d — the create relocated instead of failing", got, before)
	}
}

// The success path: the pane lands IN the worktree and is marked as owning
// one, which is what lets restore tell a missing worktree from a stale browsed
// directory later.
func TestWorktreeAdd_SuccessPutsThePaneInTheWorktree(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	repo := filepath.Join(t.TempDir(), "repo")

	stubAdd(t, func(_ context.Context, _, path, _ string) error {
		return os.MkdirAll(path, 0o755)
	})

	resp := d.worktreeAddAndCreate(worktreeCreate(tab.ID, repo, "feat/x"))
	if resp.Error != "" {
		t.Fatalf("create failed: %s", resp.Error)
	}
	if resp.PaneID == "" {
		t.Fatal("no pane id returned on success")
	}

	pane := d.session.Pane(resp.PaneID)
	if pane == nil {
		t.Fatal("the returned pane id resolves to no pane")
	}
	want := gitworktree.DerivePath(repo, "feat/x")
	// EvalSymlinks may canonicalise the temp dir (macOS /var → /private/var),
	// so compare what the spawn actually resolved rather than the raw string.
	if !strings.HasSuffix(pane.CWD, filepath.Join("repo-worktrees", "feat-x")) {
		t.Errorf("pane CWD = %q, want it inside %q", pane.CWD, want)
	}
	if !pane.WorktreeOwned {
		t.Error("the pane is not marked WorktreeOwned — restore could not tell a missing worktree from a stale cwd")
	}
}
