package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/gitworktree"
)

// The whole worktree-create path had never run end to end. Every daemon test
// above stubs addWorktreeFn, and in production the branch name was dropped
// client-side before the daemon ever saw a spec — so the sequence
// "validate → real git worktree add → pane whose CWD is the new tree" was
// asserted nowhere and observed never.
//
// These tests remove the stub. They need a real git and a real filesystem, so
// they skip where git is absent: the build container has none, and they are run
// natively on Windows, which is also the platform whose separator handling
// DerivePath's other tests cannot reach.

func realGitRepoForDaemon(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	// Nested one level down so the derived SIBLING lands inside the temp tree
	// and is removed with it.
	root := filepath.Join(t.TempDir(), "proj", "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-b", "master")
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run("add", "f.txt")
	run("commit", "-m", "init")
	return root
}

// The pane's CWD is the entire feature. A pane at the repository root is an
// agent running on master while the user believes it is isolated — the exact
// outcome the reported bug produced, and the one nothing had ever verified the
// absence of against real git.
func TestWorktreeAddAndCreate_RealGit_PaneLandsInTheNewWorktree(t *testing.T) {
	repo := realGitRepoForDaemon(t)
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")

	resp := d.worktreeAddAndCreate(worktreeCreate(tab.ID, repo, "feat/login"))

	if resp.Error != "" {
		t.Fatalf("create failed: %s", resp.Error)
	}
	if resp.PaneID == "" {
		t.Fatal("create reported success but no pane")
	}
	pane := d.session.Pane(resp.PaneID)
	if pane == nil {
		t.Fatalf("pane %s is not in the session", resp.PaneID)
	}

	want := gitworktree.DerivePath(repo, "feat/login")
	if !samePath(pane.CWD, want) {
		t.Errorf("pane CWD = %q, want the worktree %q", pane.CWD, want)
	}
	// Stated separately from the equality above: if DerivePath itself ever
	// returned the repo root, the check above would pass and this one would
	// still catch the pane running on master.
	if samePath(pane.CWD, repo) {
		t.Error("pane CWD is the REPOSITORY ROOT — the isolation the feature promises is absent")
	}
	if !pane.WorktreeOwned {
		t.Error("pane is not marked WorktreeOwned, so a missing worktree would silently relocate on restore")
	}
	if info, err := os.Stat(pane.CWD); err != nil || !info.IsDir() {
		t.Errorf("the pane's CWD does not exist on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pane.CWD, "f.txt")); err != nil {
		t.Errorf("the worktree has no checked-out content: %v", err)
	}
}

// Replace + new worktree against real git, end to end: the pane being replaced
// goes away, the new one lands in the worktree git actually created, and the
// worktree is a sibling of the repository. This is the combination that was
// refused outright until it was pointed out to be a legitimate scenario.
func TestWorktreeAddAndCreate_RealGit_ReplaceLandsInTheNewWorktree(t *testing.T) {
	repo := realGitRepoForDaemon(t)
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	old, err := d.session.CreatePane(tab.ID, repo)
	if err != nil {
		t.Fatalf("seed pane: %v", err)
	}

	p := worktreeCreate(tab.ID, repo, "feat/swap")
	p.ReplacePaneID = old.ID
	resp := d.worktreeAddAndCreate(p)

	if resp.Error != "" {
		t.Fatalf("replace failed: %s", resp.Error)
	}
	if d.session.Pane(old.ID) != nil {
		t.Error("the replaced pane is still in the session")
	}
	pane := d.session.Pane(resp.PaneID)
	if pane == nil {
		t.Fatalf("new pane %s is not in the session", resp.PaneID)
	}
	want := gitworktree.DerivePath(repo, "feat/swap")
	if !samePath(pane.CWD, want) {
		t.Errorf("pane CWD = %q, want the worktree %q", pane.CWD, want)
	}
	if samePath(pane.CWD, repo) {
		t.Error("the replacement pane is at the REPOSITORY ROOT, not in a worktree")
	}
	if _, err := os.Stat(filepath.Join(pane.CWD, "f.txt")); err != nil {
		t.Errorf("the worktree has no checked-out content: %v", err)
	}
}

// The safety property, against real git: a branch git refuses must leave the
// pane being replaced untouched. This is what the original refusal was
// protecting, and the reason the add runs before the swap.
func TestWorktreeAddAndCreate_RealGit_FailedReplaceKeepsTheOldPane(t *testing.T) {
	repo := realGitRepoForDaemon(t)
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	old, err := d.session.CreatePane(tab.ID, repo)
	if err != nil {
		t.Fatalf("seed pane: %v", err)
	}
	// Take the branch first so the replace's own add is the one git refuses.
	if resp := d.worktreeAddAndCreate(worktreeCreate(tab.ID, repo, "taken")); resp.Error != "" {
		t.Fatalf("seed worktree: %s", resp.Error)
	}

	p := worktreeCreate(tab.ID, repo, "taken")
	p.ReplacePaneID = old.ID
	resp := d.worktreeAddAndCreate(p)

	if resp.Error == "" {
		t.Fatal("a duplicate branch was accepted on the replace path")
	}
	if resp.PaneID != "" {
		t.Errorf("a failed replace created pane %q", resp.PaneID)
	}
	if d.session.Pane(old.ID) == nil {
		t.Fatal("a failed git worktree add DESTROYED the pane it was going to replace")
	}
}

// A real git failure must still produce no pane. The stubbed test above asserts
// this against an invented error; this one asserts it against the error git
// actually returns, which is what the daemon puts in front of the user.
func TestWorktreeAddAndCreate_RealGit_DuplicateBranchCreatesNoPane(t *testing.T) {
	repo := realGitRepoForDaemon(t)
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")

	if resp := d.worktreeAddAndCreate(worktreeCreate(tab.ID, repo, "dup")); resp.Error != "" {
		t.Fatalf("first create failed: %s", resp.Error)
	}
	before := len(d.session.Panes(tab.ID))

	resp := d.worktreeAddAndCreate(worktreeCreate(tab.ID, repo, "dup"))

	if resp.Error == "" {
		t.Fatal("a duplicate branch was accepted")
	}
	if resp.PaneID != "" {
		t.Errorf("a failed create produced pane %q", resp.PaneID)
	}
	if got := len(d.session.Panes(tab.ID)); got != before {
		t.Errorf("pane count %d, want %d", got, before)
	}
	if !strings.Contains(resp.Error, "dup") {
		t.Errorf("the error does not name the branch, so the flash cannot point anywhere: %q", resp.Error)
	}
}

// samePath compares two filesystem paths for equality, tolerating the
// separator and case differences Windows introduces between a path git printed
// and one filepath built.
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = filepath.Clean(ra)
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = filepath.Clean(rb)
	}
	return strings.EqualFold(a, b)
}
