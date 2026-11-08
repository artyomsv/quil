package gitworktree

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	if token == "" || len(token) > maxOwnerToken {
		return errors.New("invalid owner token")
	}
	admin, err := worktreeAdminDir(ctx, path)
	if err != nil {
		return err
	}
	// Written ATOMICALLY: a fresh temp file (O_EXCL, 0600) renamed over the
	// mark. A rename replaces whatever entry is there — a symlink included —
	// rather than writing through it, which os.WriteFile would.
	tmp, err := os.CreateTemp(admin, ownerMarkFile+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.WriteString(token + "\n")
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(name, 0o600)
	}
	if werr == nil {
		werr = os.Rename(name, filepath.Join(admin, ownerMarkFile))
	}
	if werr != nil {
		_ = os.Remove(name) // best-effort: the temp is ours and empty of meaning
		return werr
	}
	return nil
}

// maxOwnerToken bounds a token (a UUID is 36 bytes); a mark longer than this
// plus its newline is invalid.
const maxOwnerToken = 64

// OwnerMarkMatches reports whether the linked worktree at path is the one
// that was marked with token. Any doubt — no admin directory, no mark, a mark
// that is not a small regular file, a read error, another token — is false.
func OwnerMarkMatches(ctx context.Context, path, token string) (bool, error) {
	if token == "" {
		return false, errors.New("no owner token recorded")
	}
	admin, err := worktreeAdminDir(ctx, path)
	if err != nil {
		return false, err
	}
	got, err := readOwnerMark(filepath.Join(admin, ownerMarkFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return got == token, nil
}

// errBadMark is a mark that is not a small regular file.
var errBadMark = errors.New("the owner mark is not a small regular file")

// readOwnerMark reads the mark only if it is a small REGULAR file: a symlink,
// a FIFO, a device or an oversized file is refused without reading it. The
// open neither follows a link nor blocks on a FIFO (markOpenFlags), the
// opened descriptor is checked to be the very file that was examined, and the
// read is capped.
func readOwnerMark(file string) (string, error) {
	before, err := os.Lstat(file)
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() || before.Size() > maxOwnerToken+1 {
		return "", errBadMark
	}
	f, err := os.OpenFile(file, os.O_RDONLY|markOpenFlags, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return "", errBadMark
	}
	buf := make([]byte, maxOwnerToken+2)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", err
	}
	if n > maxOwnerToken+1 {
		return "", errBadMark
	}
	return strings.TrimSpace(string(buf[:n])), nil
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
