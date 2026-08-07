// Package gitworktree performs the repository WRITES Quil needs to give a pane
// its own linked worktree, plus the listing that offers the existing ones.
//
// It is deliberately a separate package from gitinfo rather than a few more
// functions in it. gitinfo's package documentation states that every call is a
// read and nothing there can modify a repository — and that is load-bearing
// rather than descriptive: the daemon runs gitinfo.Probe on a ticker against
// every pane's checkout, so a package that gains the ability to write is one
// careless refactor away from a ticker that writes. Keeping the two apart makes
// that mistake need an import to happen, which is a thing a reviewer can see.
//
// Pure and stdlib-only, a sibling of gitinfo, gitdiscover and kubediscover. It
// shells out to git plumbing rather than manipulating .git by hand — creating a
// worktree means writing a gitdir file, an admin directory under
// worktrees/<name>, and a checked-out tree, and getting any of it subtly wrong
// produces a repository git itself cannot repair.
package gitworktree

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Worktree is one entry of `git worktree list --porcelain`.
//
// Locked and Prunable are carried rather than filtered because both describe an
// entry that LOOKS attachable and is not: a prunable worktree's directory is
// gone, and a locked one refuses operations. Dropping them would hand the
// caller a shorter list with no way to explain the absence, so a pane could be
// offered a directory that cannot host it.
type Worktree struct {
	// Path is the working directory, absolute, as git reports it.
	Path string
	// Branch is the checked-out branch with the refs/heads/ prefix stripped.
	// Empty when Detached or Bare.
	Branch   string
	Detached bool
	// Main marks the repository's primary checkout — the first block git
	// prints. It is where a NEW worktree's path is derived from, and it is the
	// one entry that must never be offered as somewhere to attach.
	Main bool
	// Locked and Prunable mark entries git will refuse to operate on.
	Locked   bool
	Prunable bool
	// Bare marks a repository with no working tree. Only ever true on the main
	// entry, and it cannot host a pane.
	Bare bool
}

// runGit is the seam every command goes through, so tests never need a real
// repository on disk. Mirrors gitinfo.runGit, including its two non-obvious
// pieces: hideWindow (a console-less daemon otherwise allocates a visible
// console per child on Windows) and WaitDelay (CommandContext kills git on
// expiry, but Output still waits for every holder of the stdout pipe, and
// git's own children — a credential helper, fsmonitor — inherit it).
//
// Stderr is discarded for List, whose every failure means the same thing.
// Add needs the opposite and captures it; see there.
var runGit = func(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	hideWindow(cmd)
	cmd.WaitDelay = gitWaitDelay
	out, err := cmd.Output()
	if err != nil {
		// git writes the actionable part of a worktree failure to stderr —
		// "already exists", "already used by worktree", "invalid reference".
		// The caller shows it to the user, so unlike gitinfo this cannot
		// discard it. ExitError carries it; every other error is the exec
		// itself failing and speaks for itself.
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return string(out), fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
	}
	return string(out), err
}

// gitWaitDelay caps how long a killed git may hold its output pipe open.
const gitWaitDelay = 2 * time.Second

// List reports the worktrees of the repository containing dir, main checkout
// first. The caller supplies the timeout via ctx.
//
// A directory outside any repository is NOT an error: the setup dialog asks
// about every directory the user browses to, and most are not repositories, so
// treating that as a failure would put an error on screen for the ordinary
// case. It returns no entries, which is the same shape the caller renders for
// a repository with nothing to attach to.
//
// That collapse is deliberately narrow: a plain non-zero exit from `git
// worktree list` — the shape "not a repository" actually takes — is the only
// case folded into (nil, nil). A missing git binary (exec.ErrNotFound) and a
// call that ran out of time (ctx.Err() != nil) are returned as errors
// instead, or a missing binary, a corrupt repository and a permissions error
// would all render the same confidently-wrong "not a git repository" the
// caller shows for the genuine case.
func List(ctx context.Context, dir string) ([]Worktree, error) {
	out, err := runGit(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || ctx.Err() != nil {
			return nil, err
		}
		return nil, nil
	}
	return parsePorcelain(out), nil
}

// parsePorcelain reads `git worktree list --porcelain`: blank-line-separated
// blocks of "<key> <value>" or bare "<key>" lines, main checkout first.
//
// Split on the "worktree" key rather than on blank lines, because the trailing
// newlines vary — a bare repo's block is two lines, and git appends a final
// blank — so a blank-line split has to special-case its own output shape.
//
// Neither split is robust against a LOCK REASON, which is free text git does
// not escape: a blank line inside one would split an entry in two, and a line
// beginning "worktree " would invent one. Accepted rather than solved, because
// the consequence is bounded — a phantom entry is a row that fails to attach,
// and the reason is written by whoever locked the worktree, i.e. the user. If
// that ever needs to be airtight, the fix is `-z` (NUL-delimited records),
// not more parsing.
func parsePorcelain(out string) []Worktree {
	var list []Worktree
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		key, val, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch key {
		case "worktree":
			if val == "" {
				continue
			}
			list = append(list, Worktree{Path: val, Main: len(list) == 0})
		case "branch":
			if len(list) > 0 {
				list[len(list)-1].Branch = strings.TrimPrefix(val, "refs/heads/")
			}
		case "detached":
			if len(list) > 0 {
				list[len(list)-1].Detached = true
			}
		case "bare":
			if len(list) > 0 {
				list[len(list)-1].Bare = true
			}
		case "locked":
			if len(list) > 0 {
				list[len(list)-1].Locked = true
			}
		case "prunable":
			if len(list) > 0 {
				list[len(list)-1].Prunable = true
			}
		}
	}
	return list
}

// Add creates a linked worktree at path, checking out a NEW branch off the
// repository's current HEAD. repo is the directory the command runs in.
//
// There is deliberately no force option and no --force in the argv. Every
// refusal git can raise here is a fact the user needs — the path is occupied,
// the branch already exists, the branch is checked out in another worktree, the
// repository has no commits to branch from — and forcing past any of them lands
// a pane on top of a checkout something else is using. The error is returned
// with git's own stderr attached, because "already used by worktree
// '/x/feat-y'" tells the user which pane to go look at and no message this
// package could invent would.
func Add(ctx context.Context, repo, path, branch string) error {
	if _, err := runGit(ctx, repo, "worktree", "add", "-b", branch, path); err != nil {
		return fmt.Errorf("git worktree add %s (branch %s): %w", path, branch, err)
	}
	return nil
}

// Remove undoes an Add whose pane could not be created, and deletes the branch
// that Add made along with it.
//
// This is the ONE place --force is right, and for a reason that does not
// generalise to Add: the worktree being removed was created by this daemon
// seconds ago and has never been handed to anyone, so there is no user work to
// protect — while without it git refuses to remove a worktree it considers
// dirty, which a fresh checkout can be on a repository with line-ending or
// filemode differences. Leaving it instead strands a full checkout on disk plus
// a branch pointing at it, and the next attempt at the same name then fails
// with "already exists" against a directory the user never made.
//
// Best-effort by contract: the caller is already reporting a failure and this
// is cleanup, so the error is for the log, not for the user.
func Remove(ctx context.Context, repo, path string, branch string) error {
	if _, err := runGit(ctx, repo, "worktree", "remove", "--force", path); err != nil {
		return fmt.Errorf("git worktree remove %s: %w", path, err)
	}
	// Only after the worktree is gone: git refuses to delete a branch that a
	// worktree still has checked out, so the order is load-bearing.
	if _, err := runGit(ctx, repo, "branch", "-D", branch); err != nil {
		return fmt.Errorf("git branch -D %s: %w", branch, err)
	}
	return nil
}
