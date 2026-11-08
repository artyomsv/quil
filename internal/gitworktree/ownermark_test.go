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
