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
	return writeMarkIn(admin, token)
}

// ErrNotCreatedHere means the checkout at the path is not the one the create
// just made: its admin directory belongs to another repository, or git's own
// back-pointer names another path.
var ErrNotCreatedHere = errors.New("the checkout at the path is not the one this create made")

// MarkNewWorktree marks the linked worktree a create in repo JUST made at
// path, and refuses (ErrNotCreatedHere) when the checkout found there cannot
// be it. Every comparison is by FILE IDENTITY (os.SameFile), never by
// spelling, and anything that cannot be resolved is an error — no mark:
//   - the checkout's common git directory must be repo's own;
//   - repo's <common>/worktrees must be a real directory (not a link that
//     routes the metadata to another repository), and the admin directory a
//     real directory directly inside it;
//   - the admin directory's gitdir back-pointer (relative to the admin
//     directory when worktree.useRelativePaths wrote it so) must be path/.git.
//
// Call it straight after git returns.
//
// ACCEPTED RESIDUAL: git cannot make the add and the mark one step. A
// checkout of the SAME repository made at the SAME path between the add and
// this call — two rev-parse calls, a few stats and the write — is
// indistinguishable from the one the add made, and IS marked. Only a process
// of the same user can act there.
func MarkNewWorktree(ctx context.Context, repo, path, token string) error {
	if token == "" || len(token) > maxOwnerToken {
		return errors.New("invalid owner token")
	}
	admin, common, err := worktreeAdminAndCommon(ctx, path)
	if err != nil {
		return err
	}
	out, err := runGit(ctx, repo, "rev-parse", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("git rev-parse in %s: %w", repo, err)
	}
	repoCommon := strings.TrimSpace(out)
	if !filepath.IsAbs(repoCommon) {
		repoCommon = filepath.Join(repo, repoCommon)
	}
	if same, err := sameFile(common, repoCommon); err != nil {
		return err
	} else if !same {
		return ErrNotCreatedHere
	}
	worktrees, err := os.Lstat(filepath.Join(repoCommon, "worktrees"))
	if err != nil {
		return err
	}
	adminInfo, err := os.Lstat(admin)
	if err != nil {
		return err
	}
	if !worktrees.IsDir() || !adminInfo.IsDir() {
		return ErrNotCreatedHere // a link is not a directory to Lstat
	}
	parent, err := os.Stat(filepath.Dir(admin))
	if err != nil {
		return err
	}
	if !os.SameFile(parent, worktrees) {
		return ErrNotCreatedHere
	}
	back, err := readSmallFile(filepath.Join(admin, "gitdir"), 4096)
	if err != nil {
		return fmt.Errorf("reading the worktree's gitdir: %w", err)
	}
	backPath := strings.TrimSpace(back)
	if !filepath.IsAbs(backPath) {
		backPath = filepath.Join(admin, backPath)
	}
	if same, err := sameFile(backPath, filepath.Join(path, ".git")); err != nil {
		return err
	} else if !same {
		return ErrNotCreatedHere
	}
	return writeMarkIn(admin, token)
}

// sameFile reports whether a and b are the same file on disk. An error is a
// path that could not be examined: never treated as either answer.
func sameFile(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(ai, bi), nil
}

// readSmallFile reads at most limit bytes of a regular file, refusing
// anything larger or not regular.
func readSmallFile(file string, limit int64) (string, error) {
	f, err := os.OpenFile(file, os.O_RDONLY|markOpenFlags, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return "", err
	} else if !fi.Mode().IsRegular() || fi.Size() > limit {
		return "", errBadMark
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(b)) > limit {
		return "", errBadMark
	}
	return string(b), nil
}

// writeMarkIn writes token as the owner mark of the admin directory admin.
func writeMarkIn(admin, token string) error {
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
	admin, _, err := worktreeAdminAndCommon(ctx, path)
	return admin, err
}

// worktreeAdminAndCommon is worktreeAdminDir plus the common git directory
// the checkout at path belongs to.
func worktreeAdminAndCommon(ctx context.Context, path string) (admin, common string, err error) {
	out, err := runGit(ctx, path, "rev-parse", "--absolute-git-dir", "--git-common-dir")
	if err != nil {
		return "", "", fmt.Errorf("git rev-parse in %s: %w", path, err)
	}
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(out, "\r\n", "\n")), "\n")
	if len(lines) != 2 {
		return "", "", fmt.Errorf("git rev-parse in %s: unexpected answer %q", path, out)
	}
	gitDir, common := strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
	if !filepath.IsAbs(common) {
		common = filepath.Join(path, common)
	}
	if samePath(gitDir, common) {
		return "", "", ErrNotLinkedWorktree
	}
	return gitDir, common, nil
}
