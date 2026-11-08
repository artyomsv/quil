package gitworktree

import (
	"context"
	"errors"
	"fmt"
)

// ErrBranchMoved means the branch no longer points at the commit the caller
// confirmed: nothing was deleted.
var ErrBranchMoved = errors.New("the branch moved after it was confirmed")

// DeleteLocalBranch deletes a local branch ONLY while it still points at tip,
// the commit the user confirmed against.
//
// The caller decided this with the unmerged-commit count on screen, and that
// count describes the branch at tip. `git branch -D` deletes by NAME, so a
// commit made after the count — an agent finishing its turn while the close
// retried — went with it unseen. `git update-ref -d <ref> <old>` is git's own
// compare-and-delete: it removes the ref only if it still holds old, as one
// locked step, so there is no window between the check and the delete.
//
// update-ref does not refuse a branch some worktree has checked out, which
// `branch -D` does; that check runs first, from git's own listing. A checkout
// switched onto the branch between the listing and the delete is the
// remaining gap, the same one `branch -D` has between its check and its
// delete. The ref is fully qualified, so a dash-led branch name never reaches
// git as an option.
func DeleteLocalBranch(ctx context.Context, repo, branch, tip string) error {
	if branch == "" {
		return fmt.Errorf("no branch to delete")
	}
	if tip == "" {
		return fmt.Errorf("no confirmed commit for branch %s", branch)
	}
	list, err := List(ctx, repo)
	if err != nil {
		return fmt.Errorf("git worktree list: %w", err)
	}
	if len(list) == 0 {
		return fmt.Errorf("git worktree list in %s: no answer", repo)
	}
	for _, w := range list {
		if !w.Detached && w.Branch == branch {
			return fmt.Errorf("branch %s is checked out in %s", branch, w.Path)
		}
	}
	ref := "refs/heads/" + branch
	if _, err := runGit(ctx, repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
		return fmt.Errorf("git rev-parse %s: %w", ref, err)
	}
	if _, err := runGit(ctx, repo, "update-ref", "-d", ref, tip); err != nil {
		return fmt.Errorf("%w: git update-ref -d %s: %v", ErrBranchMoved, ref, err)
	}
	return nil
}
