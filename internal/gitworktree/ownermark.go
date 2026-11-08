package gitworktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ownerMarkFile is the file Quil writes into a linked worktree's own git
// admin directory (<common>/worktrees/<name>/) when it creates the worktree.
// git ignores files it does not know there, and removes the directory — mark
// included — with the worktree.
const ownerMarkFile = "quil-owner"

// ErrNotLinkedWorktree means path's git directory is the repository's own
// (a main checkout), so it has no per-worktree admin directory to mark.
var ErrNotLinkedWorktree = errors.New("not a linked worktree")

// WriteOwnerMark records token in the admin directory of the linked worktree
// at path. The admin directory is unique to ONE `git worktree add`: a checkout
// later created at the same path gets a fresh one, without the mark — which
// is what lets a close tell the worktree Quil made from a replacement.
func WriteOwnerMark(ctx context.Context, path, token string) error {
	if token == "" {
		return errors.New("empty owner token")
	}
	admin, err := worktreeAdminDir(ctx, path)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(admin, ownerMarkFile), []byte(token+"\n"), 0o600)
}

// OwnerMarkMatches reports whether the linked worktree at path is the one
// that was marked with token. Any doubt — no admin directory, no mark, a read
// error, another token — is false.
func OwnerMarkMatches(ctx context.Context, path, token string) (bool, error) {
	if token == "" {
		return false, errors.New("no owner token recorded")
	}
	admin, err := worktreeAdminDir(ctx, path)
	if err != nil {
		return false, err
	}
	b, err := os.ReadFile(filepath.Join(admin, ownerMarkFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(string(b)) == token, nil
}

// worktreeAdminDir is the per-worktree git directory of the linked worktree
// at path, refusing a main checkout (whose git dir IS the common dir).
func worktreeAdminDir(ctx context.Context, path string) (string, error) {
	out, err := runGit(ctx, path, "rev-parse", "--absolute-git-dir", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("git rev-parse in %s: %w", path, err)
	}
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(out, "\r\n", "\n")), "\n")
	if len(lines) != 2 {
		return "", fmt.Errorf("git rev-parse in %s: unexpected answer %q", path, out)
	}
	gitDir, common := strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
	if !filepath.IsAbs(common) {
		common = filepath.Join(path, common)
	}
	if samePath(gitDir, common) {
		return "", ErrNotLinkedWorktree
	}
	return gitDir, nil
}
