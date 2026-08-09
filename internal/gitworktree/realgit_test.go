package gitworktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every other Add test stubs git, so the pair that actually decides where a
// checkout lands — DerivePath composing the path, Add handing it to git — had
// never run against a real repository in the suite. It had never run in
// production either: the branch name was dropped client-side before the daemon
// saw it, so `git worktree add` was reached for the first time only after that
// was fixed. These tests exercise the real binary.

func realGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	// The repo sits in a subdirectory of TempDir, so DerivePath's sibling
	// lands inside the temp tree and is cleaned up with it.
	root := filepath.Join(t.TempDir(), "proj", "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	runGitIn(t, root, "init", "-b", "master")
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGitIn(t, root, "add", "f.txt")
	runGitIn(t, root, "commit", "-m", "init")
	return root
}

// runGitIn runs one git command in dir and fails the test if it does not
// succeed. Extracted from realGitRepo's closure so a test can drive the
// repository further — putting HEAD on a branch other than the default one is
// the whole setup for the base-branch tests below.
func runGitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// A worktree add reads user.name/user.email when it creates the
	// branch's first ref; a container with no global config fails without
	// these, which would look like a bug in Add.
	// os.DevNull, not "/dev/null": these run natively on Windows, where the
	// build container has no git at all and the separator handling in
	// DerivePath differs from the Linux CI leg.
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// The headline guarantee: the new checkout is a SIBLING of the repository, not
// nested inside it. A nested worktree is what makes `git clean -xfd` in the
// main checkout delete another pane's uncommitted work, and what makes every
// tree-walking tool traverse two copies of the project.
func TestAdd_RealGit_CreatesASiblingWorktreeOnTheBranch(t *testing.T) {
	repo := realGitRepo(t)
	path := DerivePath(repo, "feat/login")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := Add(ctx, repo, path, "feat/login"); err != nil {
		t.Fatalf("Add against real git: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		t.Fatalf("worktree is not at the derived path %q: %v", path, err)
	}
	// Nested is the failure this layout exists to prevent, so assert the
	// containment directly rather than trusting the string shape.
	if rel, err := filepath.Rel(repo, path); err == nil && !strings.HasPrefix(rel, "..") {
		t.Errorf("worktree %q is INSIDE the repository %q (rel %q)", path, repo, rel)
	}
	if got, want := filepath.Dir(path), repo+worktreesSuffix; got != want {
		t.Errorf("worktree parent = %q, want %q", got, want)
	}
	// The checked-out file proves this is a real working tree, not just a
	// directory git recorded.
	if _, err := os.Stat(filepath.Join(path, "f.txt")); err != nil {
		t.Errorf("the worktree has no checked-out content: %v", err)
	}

	// And the listing the dialog reads must now show it, on its own branch —
	// the per-checkout key is what keeps it from reporting master.
	list, err := List(ctx, repo)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found bool
	for _, w := range list {
		if filepath.Clean(w.Path) != filepath.Clean(path) {
			continue
		}
		found = true
		if w.Branch != "feat/login" {
			t.Errorf("listed branch = %q, want %q", w.Branch, "feat/login")
		}
		if w.Main {
			t.Error("the new worktree is listed as the main checkout")
		}
	}
	if !found {
		t.Errorf("the new worktree is not in the listing: %+v", list)
	}
}

// A branch that already exists must FAIL rather than silently reusing or
// relocating. The daemon turns this error into "no pane at all", which is the
// contract the whole feature rests on.
func TestAdd_RealGit_RefusesAnExistingBranch(t *testing.T) {
	repo := realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := Add(ctx, repo, DerivePath(repo, "dup"), "dup"); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	// A different path, the same branch — the shape a user retrying a name
	// produces.
	second := DerivePath(repo, "dup") + "-2"
	_, err := Add(ctx, repo, second, "dup")
	if err == nil {
		t.Fatal("Add reused an existing branch instead of failing")
	}
	// git's own stderr names the worktree already holding it, which is what
	// makes the flash actionable.
	if !strings.Contains(err.Error(), "dup") {
		t.Errorf("error does not name the branch: %v", err)
	}
	if _, statErr := os.Stat(second); statErr == nil {
		t.Error("a failed Add left a directory behind")
	}
}

// A branch name with a slash is flattened for the directory but must reach git
// UNFLATTENED, or the pane sits on a branch nobody asked for.
func TestAdd_RealGit_KeepsTheSlashInTheBranchName(t *testing.T) {
	repo := realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	path := DerivePath(repo, "feat/nested/deep")
	if _, err := Add(ctx, repo, path, "feat/nested/deep"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if base := filepath.Base(path); strings.ContainsAny(base, `/\`) {
		t.Errorf("derived directory %q still contains a separator", base)
	}
	list, err := List(ctx, repo)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, w := range list {
		if filepath.Clean(w.Path) == filepath.Clean(path) && w.Branch != "feat/nested/deep" {
			t.Errorf("branch = %q, want the unflattened %q", w.Branch, "feat/nested/deep")
		}
	}
}

// Remove is new production code and uses --force plus a branch delete, so it
// gets real-git coverage rather than a stub: the ordering (worktree first, then
// branch) is a git constraint, not a preference, and a stub cannot show it.
func TestRemove_RealGit_UndoesAnAddCompletely(t *testing.T) {
	repo := realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	path := DerivePath(repo, "feat/undo")
	if _, err := Add(ctx, repo, path, "feat/undo"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := Remove(ctx, repo, path, "feat/undo"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the worktree directory survived Remove: %v", err)
	}
	list, err := List(ctx, repo)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, w := range list {
		if filepath.Clean(w.Path) == filepath.Clean(path) {
			t.Errorf("git still lists the removed worktree: %+v", w)
		}
	}
	// The branch must go too, or the whole point is lost: the next attempt at
	// the same name would fail with "already exists" against a branch the user
	// never made.
	if _, err := Add(ctx, repo, DerivePath(repo, "feat/undo"), "feat/undo"); err != nil {
		t.Errorf("the branch outlived Remove, so the name cannot be reused: %v", err)
	}
}

// A dirty worktree must still be removable. It was created by the daemon
// seconds ago and never handed to anyone, so there is no user work to protect —
// and git refuses a plain remove on a checkout it considers dirty, which a
// fresh one can be on a repository with line-ending differences.
func TestRemove_RealGit_RemovesADirtyWorktree(t *testing.T) {
	repo := realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	path := DerivePath(repo, "feat/dirty")
	if _, err := Add(ctx, repo, path, "feat/dirty"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "f.txt"), []byte("modified\n"), 0o644); err != nil {
		t.Fatalf("dirty the worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("add an untracked file: %v", err)
	}

	if err := Remove(ctx, repo, path, "feat/dirty"); err != nil {
		t.Fatalf("Remove refused a dirty worktree: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the dirty worktree survived Remove: %v", err)
	}
}

// The bug this pair of tests exists for: a worktree created while the main
// checkout sat on a feature branch was branched off THAT branch, so a "fix"
// worktree carried an unmerged feature's commits and its PR diff was the
// feature. `git worktree add -b <new> <path>` with no start-point branches from
// the HEAD of the repository the command runs in, and the main checkout's HEAD
// is whatever the user last worked on — ambient state the operation must not
// read.
//
// Real git rather than a stub, because the defect is entirely in what git does
// with an argv the stub tests already agreed was correct.
func TestAdd_RealGit_BranchesFromTheDefaultBranchNotCurrentHEAD(t *testing.T) {
	repo := realGitRepo(t)
	// Put HEAD somewhere other than master, with a commit master does not
	// have. This is the ordinary state of a checkout someone is working in.
	runGitIn(t, repo, "checkout", "-b", "feat/unrelated")
	if err := os.WriteFile(filepath.Join(repo, "g.txt"), []byte("unrelated\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGitIn(t, repo, "add", "g.txt")
	runGitIn(t, repo, "commit", "-m", "unrelated work")

	masterTip := runGitIn(t, repo, "rev-parse", "master")
	headTip := runGitIn(t, repo, "rev-parse", "HEAD")
	if masterTip == headTip {
		t.Fatal("setup failed: HEAD and master are the same commit, so the test cannot tell them apart")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	path := DerivePath(repo, "fix/thing")
	if _, err := Add(ctx, repo, path, "fix/thing"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got := runGitIn(t, repo, "rev-parse", "fix/thing")
	if got == headTip {
		t.Errorf("the new branch was created off the current HEAD (%s), want the default branch master (%s)", headTip, masterTip)
	}
	if got != masterTip {
		t.Errorf("fix/thing = %s, want master's tip %s", got, masterTip)
	}
	// The checkout must match the branch, or the pane comes up on the right
	// ref with the wrong files on disk.
	if _, err := os.Stat(filepath.Join(path, "g.txt")); !os.IsNotExist(err) {
		t.Errorf("the worktree contains g.txt, a file that exists only on feat/unrelated: %v", err)
	}
}

// A repository whose HEAD IS the default branch must be unaffected — this is
// the common case, and it is the one that made the defect invisible until the
// main checkout happened to be parked on a feature branch.
func TestAdd_RealGit_BranchesFromDefaultWhenHEADAlreadyIsIt(t *testing.T) {
	repo := realGitRepo(t)
	masterTip := runGitIn(t, repo, "rev-parse", "master")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	path := DerivePath(repo, "feat/ordinary")
	if _, err := Add(ctx, repo, path, "feat/ordinary"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := runGitIn(t, repo, "rev-parse", "feat/ordinary"); got != masterTip {
		t.Errorf("feat/ordinary = %s, want master's tip %s", got, masterTip)
	}
}

// A repository with no branch named main or master and no origin/HEAD still has
// to work: git init -b trunk is a legitimate repository, and refusing to create
// a worktree there would be a regression in service of a convention that repo
// does not follow. Falling back to HEAD is git's own behaviour.
func TestAdd_RealGit_FallsBackToHEADWhenThereIsNoDefaultBranch(t *testing.T) {
	repo := realGitRepo(t)
	runGitIn(t, repo, "branch", "-m", "master", "trunk")
	trunkTip := runGitIn(t, repo, "rev-parse", "HEAD")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	path := DerivePath(repo, "feat/x")
	if _, err := Add(ctx, repo, path, "feat/x"); err != nil {
		t.Fatalf("Add on a repository with no conventional default branch: %v", err)
	}
	if got := runGitIn(t, repo, "rev-parse", "feat/x"); got != trunkTip {
		t.Errorf("feat/x = %s, want HEAD %s", got, trunkTip)
	}
}

// realGitClone builds an origin repository and a clone of it, returning the
// clone. A clone is the ONLY way to get a real refs/remotes/origin/HEAD — git
// writes that symref from the server's advertised HEAD at clone time and never
// again — and origin/HEAD is the branch defaultBranch tries FIRST for every
// real user, so the tests that build a remote-less repo exercise everything
// except the common path.
func realGitClone(t *testing.T) string {
	t.Helper()
	origin := realGitRepo(t)
	parent := filepath.Join(t.TempDir(), "cloned")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	runGitIn(t, parent, "clone", "--quiet", origin, "repo")
	return filepath.Join(parent, "repo")
}

// runGitAllowFail runs git and reports the exit status instead of failing the
// test, for the assertions whose POINT is that a command fails — asking for a
// branch's upstream when it must not have one.
func runGitAllowFail(t *testing.T, dir string, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err == nil
}

// A DANGLING origin/HEAD must not break worktree creation.
//
// `git symbolic-ref` reads the symref without resolving its target, so it exits
// 0 and prints a ref that no longer exists — the ordinary state of any clone
// made before its remote renamed the default branch, because a later `fetch
// --prune` removes the branch and leaves the symref pointing at it. Adopting
// that answer unverified hands `worktree add` a reference it refuses, and since
// the daemon creates NO pane on failure, worktree-backed panes stop working in
// that repository permanently — for a branch name the user never typed.
//
// git itself treats this state as "no answer" ("warning: ignoring dangling
// symref"), which is what defaultBranch has to mirror.
func TestAdd_RealGit_FallsThroughWhenOriginHEADIsDangling(t *testing.T) {
	repo := realGitClone(t)
	if _, ok := runGitAllowFail(t, repo, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); !ok {
		t.Skip("this git did not record origin/HEAD on clone; nothing to dangle")
	}
	// Strand the symref exactly as `fetch --prune` does after an upstream
	// rename: the branch goes, the symref naming it stays.
	runGitIn(t, repo, "update-ref", "-d", "refs/remotes/origin/master")
	if _, ok := runGitAllowFail(t, repo, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/master^{commit}"); ok {
		t.Fatal("setup failed: refs/remotes/origin/master still resolves, so origin/HEAD is not dangling")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	path := DerivePath(repo, "fix/dangling")
	if _, err := Add(ctx, repo, path, "fix/dangling"); err != nil {
		t.Fatalf("Add failed on a repository with a dangling origin/HEAD: %v", err)
	}
	// It must land on the local master that clone created, not nowhere.
	want := runGitIn(t, repo, "rev-parse", "refs/heads/master")
	if got := runGitIn(t, repo, "rev-parse", "fix/dangling"); got != want {
		t.Errorf("fix/dangling = %s, want local master %s", got, want)
	}
}

// The new branch must have NO upstream.
//
// A remote-tracking start-point silently configures tracking (branch.autoSetupMerge
// defaults to true), and the consequences are all bad here: `git push` in the
// new worktree fails, and the remedy git prints — `git push origin HEAD:master`
// — pushes the feature work straight onto the default branch if it is pasted.
// `git pull` merges the base into the feature branch. And the sidebar's git row
// starts rendering ahead/behind counts against the base on a branch whose push
// does not work.
//
// Branching off HEAD never set an upstream, so tracking would be a behaviour
// change smuggled in by the base-selection fix rather than anything asked for.
func TestAdd_RealGit_DoesNotSetUpstreamOnTheNewBranch(t *testing.T) {
	repo := realGitClone(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := Add(ctx, repo, DerivePath(repo, "feat/untracked"), "feat/untracked"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if out, ok := runGitAllowFail(t, repo, "config", "--get", "branch.feat/untracked.merge"); ok {
		t.Errorf("the new branch tracks %q; it must have no upstream", out)
	}
	if out, ok := runGitAllowFail(t, repo, "config", "--get", "branch.feat/untracked.remote"); ok {
		t.Errorf("the new branch has remote %q configured; it must have none", out)
	}
}

// The clone case, asserted end to end: origin/HEAD is what a real repository
// answers with, and every other real-git test here builds a repo that has none.
func TestAdd_RealGit_UsesOriginHEADOnAClone(t *testing.T) {
	repo := realGitClone(t)
	runGitIn(t, repo, "checkout", "-q", "-b", "feat/unrelated")
	if err := os.WriteFile(filepath.Join(repo, "g.txt"), []byte("unrelated\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGitIn(t, repo, "add", "g.txt")
	runGitIn(t, repo, "commit", "-qm", "unrelated work")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := Add(ctx, repo, DerivePath(repo, "fix/from-origin"), "fix/from-origin"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	want := runGitIn(t, repo, "rev-parse", "refs/remotes/origin/master")
	if got := runGitIn(t, repo, "rev-parse", "fix/from-origin"); got != want {
		t.Errorf("fix/from-origin = %s, want origin/master %s", got, want)
	}
}
