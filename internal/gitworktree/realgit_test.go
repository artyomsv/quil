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
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
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

// The headline guarantee: the new checkout is a SIBLING of the repository, not
// nested inside it. A nested worktree is what makes `git clean -xfd` in the
// main checkout delete another pane's uncommitted work, and what makes every
// tree-walking tool traverse two copies of the project.
func TestAdd_RealGit_CreatesASiblingWorktreeOnTheBranch(t *testing.T) {
	repo := realGitRepo(t)
	path := DerivePath(repo, "feat/login")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := Add(ctx, repo, path, "feat/login"); err != nil {
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

	if err := Add(ctx, repo, DerivePath(repo, "dup"), "dup"); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	// A different path, the same branch — the shape a user retrying a name
	// produces.
	second := DerivePath(repo, "dup") + "-2"
	err := Add(ctx, repo, second, "dup")
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
	if err := Add(ctx, repo, path, "feat/nested/deep"); err != nil {
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
