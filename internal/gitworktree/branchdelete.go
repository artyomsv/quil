package gitworktree

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrBranchMoved means the branch no longer points at the commit the caller
// confirmed: nothing was deleted.
var ErrBranchMoved = errors.New("the branch moved after it was confirmed")

// DeleteLocalBranch deletes a local branch, but only while it still points at
// tip, the commit the user confirmed against.
//
// The caller decided this with the unmerged-commit count on screen, and that
// count describes the branch at tip. The tip is read again here, right
// before the delete, so a commit made after the count — an agent finishing
// its turn while the close retried the worktree removal — keeps the branch
// (ErrBranchMoved).
//
// The delete itself stays `git branch -D -- <b>`: git's own refusals (a branch
// checked out in any worktree, one being rebased or bisected) and its cleanup
// of the branch's configuration come with it. Between the tip read and the
// delete no worktree holds the branch (branch -D refuses one that does), so
// an ordinary commit cannot land there; only an explicit ref update can, and
// that is the remaining gap. -D, not -d: the user chose to drop the commits
// the dialog counted. `--` ends option parsing: a branch name may legally
// begin with a dash.
func DeleteLocalBranch(ctx context.Context, repo, branch, tip string) error {
	if branch == "" {
		return fmt.Errorf("no branch to delete")
	}
	if tip == "" {
		return fmt.Errorf("no confirmed commit for branch %s", branch)
	}
	ref := "refs/heads/" + branch
	cur, err := runGit(ctx, repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return fmt.Errorf("git rev-parse %s: %w", ref, err)
	}
	if strings.TrimSpace(cur) != tip {
		return ErrBranchMoved
	}
	if _, err := runGit(ctx, repo, "branch", "-D", "--", branch); err != nil {
		return fmt.Errorf("git branch -D %s: %w", branch, err)
	}
	return nil
}
