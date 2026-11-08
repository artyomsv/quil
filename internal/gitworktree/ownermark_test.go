package gitworktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The mark identifies ONE `git worktree add`: it matches the worktree it was
// written into, and a checkout created later at the same path — after the
// first was removed — has a fresh admin directory without it.
//
// Fails if the identity is the path alone.
func TestOwnerMark_RealGit_ReplacedCheckoutDoesNotMatch(t *testing.T) {
	repo := realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	path := DerivePath(repo, "feat")
	if _, err := Add(ctx, repo, path, "feat"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := WriteOwnerMark(ctx, path, "tok-1"); err != nil {
		t.Fatalf("WriteOwnerMark: %v", err)
	}
	if ok, err := OwnerMarkMatches(ctx, path, "tok-1"); !ok || err != nil {
		t.Fatalf("match = %v, %v; want the marked worktree to match", ok, err)
	}
	if ok, _ := OwnerMarkMatches(ctx, path, "tok-2"); ok {
		t.Fatal("another token matched")
	}

	// Replaced at the same path.
	if err := RemoveWorktree(ctx, repo, path); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	runGitIn(t, repo, "worktree", "add", "-q", path, "-b", "other")
	if ok, err := OwnerMarkMatches(ctx, path, "tok-1"); ok || err != nil {
		t.Errorf("replacement: match = %v, %v; want no match and no error", ok, err)
	}

	// The main checkout has no admin directory of its own.
	if err := WriteOwnerMark(ctx, repo, "tok-3"); !errors.Is(err, ErrNotLinkedWorktree) {
		t.Errorf("main checkout: err = %v, want ErrNotLinkedWorktree", err)
	}
}

// A mark that is not a small regular file is refused WITHOUT reading it: a
// symlink (to an endless source, say) or an oversized file.
//
// Fails if readOwnerMark follows a link or reads without a cap.
func TestReadOwnerMark_RefusesLinksAndOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readOwnerMark(big); !errors.Is(err, errBadMark) {
		t.Errorf("oversized mark: err = %v, want errBadMark", err)
	}
	good := filepath.Join(dir, "good")
	if err := os.WriteFile(good, []byte("tok-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readOwnerMark(good); err != nil || got != "tok-1" {
		t.Errorf("regular mark: %q, %v", got, err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(good, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readOwnerMark(link); !errors.Is(err, errBadMark) {
		t.Errorf("symlink mark: err = %v, want errBadMark", err)
	}
}

// MarkNewWorktree marks the checkout a create just made.
func TestMarkNewWorktree_RealGit_MarksTheCreatedCheckout(t *testing.T) {
	repo := realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	path := DerivePath(repo, "feat")
	if _, err := Add(ctx, repo, path, "feat"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := MarkNewWorktree(ctx, repo, path, "tok-1"); err != nil {
		t.Fatalf("MarkNewWorktree: %v", err)
	}
	if ok, err := OwnerMarkMatches(ctx, path, "tok-1"); !ok || err != nil {
		t.Errorf("match = %v, %v; want the created checkout marked", ok, err)
	}
}

// The path now holds a checkout of ANOTHER repository: it is not the one this
// create made, and it is never marked.
//
// Fails if the mark is written to whatever checkout the path names.
func TestMarkNewWorktree_RealGit_RefusesACheckoutOfAnotherRepository(t *testing.T) {
	repo, other := realGitRepo(t), realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	path := DerivePath(repo, "feat")
	if _, err := Add(ctx, repo, path, "feat"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := RemoveWorktree(ctx, repo, path); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	runGitIn(t, other, "worktree", "add", "-q", path, "-b", "intruder")

	if err := MarkNewWorktree(ctx, repo, path, "tok-1"); !errors.Is(err, ErrNotCreatedHere) {
		t.Fatalf("err = %v, want ErrNotCreatedHere", err)
	}
	if ok, _ := OwnerMarkMatches(ctx, path, "tok-1"); ok {
		t.Error("the other repository's checkout was marked")
	}
}

// The path holds a folder whose .git names a worktree registered at ANOTHER
// path (a copied or moved checkout): git's own back-pointer disagrees, and it
// is never marked.
//
// Fails if the admin directory's gitdir back-pointer is not checked.
func TestMarkNewWorktree_RealGit_RefusesACheckoutRegisteredElsewhere(t *testing.T) {
	repo := realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	real := DerivePath(repo, "feat")
	if _, err := Add(ctx, repo, real, "feat"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	copyDir := filepath.Join(t.TempDir(), "copy")
	if err := os.MkdirAll(copyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dotGit, err := os.ReadFile(filepath.Join(real, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(copyDir, ".git"), dotGit, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := MarkNewWorktree(ctx, repo, copyDir, "tok-1"); !errors.Is(err, ErrNotCreatedHere) {
		t.Fatalf("err = %v, want ErrNotCreatedHere", err)
	}
	if ok, _ := OwnerMarkMatches(ctx, real, "tok-1"); ok {
		t.Error("the worktree registered at the other path was marked")
	}
}

// ACCEPTED RESIDUAL, pinned so it is a decision and not an accident: a
// checkout of the SAME repository made at the SAME path between the add and
// the mark is indistinguishable from the one the add made, and is marked.
// git offers no way to make the add and the mark one step. Quil writes the
// mark straight after git returns, so the window is one rev-parse wide, and
// only a process of the same user can act in it.
func TestMarkNewWorktree_RealGit_SameRepositoryReplacementIsTheAcceptedResidual(t *testing.T) {
	repo := realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	path := DerivePath(repo, "feat")
	if _, err := Add(ctx, repo, path, "feat"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := RemoveWorktree(ctx, repo, path); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	runGitIn(t, repo, "worktree", "add", "-q", path, "-b", "swapped")

	if err := MarkNewWorktree(ctx, repo, path, "tok-1"); err != nil {
		t.Fatalf("err = %v; the accepted residual is that this is marked", err)
	}
	if ok, err := OwnerMarkMatches(ctx, path, "tok-1"); !ok || err != nil {
		t.Errorf("match = %v, %v; the accepted residual is that the replacement carries the token", ok, err)
	}
}

// A's worktree metadata is ROUTED to B's through a link: A's .git/worktrees
// is a symlink to B's. A checkout B registered at the path passes a
// spelling check against A's metadata root; it is not A's, and B gets no mark.
//
// Fails if the repository or the metadata root is compared by spelling, or if
// a linked metadata root is accepted.
func TestMarkNewWorktree_RealGit_RefusesMetadataRoutedToAnotherRepository(t *testing.T) {
	repo, other := realGitRepo(t), realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	path := DerivePath(repo, "feat")
	runGitIn(t, other, "worktree", "add", "-q", path, "-b", "intruder")
	if err := os.Remove(filepath.Join(repo, ".git", "worktrees")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, ".git", "worktrees"), filepath.Join(repo, ".git", "worktrees")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := MarkNewWorktree(ctx, repo, path, "tok-1"); !errors.Is(err, ErrNotCreatedHere) {
		t.Fatalf("err = %v, want ErrNotCreatedHere", err)
	}
	if ok, _ := OwnerMarkMatches(ctx, path, "tok-1"); ok {
		t.Error("the other repository's checkout was marked")
	}
}

// With worktree.useRelativePaths git writes the gitdir back-pointer relative
// to the admin directory. A fresh, valid worktree must still be marked.
//
// Fails if a relative back-pointer is resolved against the process's cwd.
func TestMarkNewWorktree_RealGit_RelativeBackPointerIsMarked(t *testing.T) {
	repo := realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runGitIn(t, repo, "config", "worktree.useRelativePaths", "true")
	path := DerivePath(repo, "feat")
	if _, err := Add(ctx, repo, path, "feat"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	admin, err := worktreeAdminDir(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	back, err := os.ReadFile(filepath.Join(admin, "gitdir"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(strings.TrimSpace(string(back))) {
		t.Skip("this git writes absolute back-pointers (worktree.useRelativePaths needs git 2.48+)")
	}

	if err := MarkNewWorktree(ctx, repo, path, "tok-1"); err != nil {
		t.Fatalf("MarkNewWorktree: %v", err)
	}
	if ok, err := OwnerMarkMatches(ctx, path, "tok-1"); !ok || err != nil {
		t.Errorf("match = %v, %v; want the worktree marked", ok, err)
	}
}

// Two checkouts of one repository, P and Q. P/.git is replaced by a link to
// Q/.git, so git resolves P to Q's admin directory and Q's back-pointer
// follows to the same file. P is not Q: it is refused, and Q's own mark is
// untouched.
//
// Fails if the .git entries are compared by followed identity, or a linked
// .git is accepted.
func TestMarkNewWorktree_RealGit_RefusesALinkedDotGit(t *testing.T) {
	repo := realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	p, q := DerivePath(repo, "feat-p"), DerivePath(repo, "feat-q")
	for _, wt := range []struct{ path, branch string }{{p, "feat-p"}, {q, "feat-q"}} {
		if _, err := Add(ctx, repo, wt.path, wt.branch); err != nil {
			t.Fatalf("Add %s: %v", wt.branch, err)
		}
	}
	if err := MarkNewWorktree(ctx, repo, q, "tok-q"); err != nil {
		t.Fatalf("MarkNewWorktree q: %v", err)
	}
	if err := os.Remove(filepath.Join(p, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(q, ".git"), filepath.Join(p, ".git")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := MarkNewWorktree(ctx, repo, p, "tok-p"); !errors.Is(err, ErrNotCreatedHere) {
		t.Fatalf("err = %v, want ErrNotCreatedHere", err)
	}
	if ok, err := OwnerMarkMatches(ctx, q, "tok-q"); !ok || err != nil {
		t.Errorf("Q's mark = %v, %v; want it unchanged", ok, err)
	}
}
