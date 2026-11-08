package gitworktree

import (
	"context"
	"fmt"
)

// DeleteLocalBranch deletes a local branch, whatever it holds.
//
// -D, not -d: the caller decided this with the unmerged-commit count on
// screen, and -d would refuse exactly the branch the user chose to drop. git
// itself still refuses a branch that is checked out in ANY worktree, which is
// what closes the race with an agent switching a sibling worktree onto it.
// `--` ends option parsing: a branch name may legally begin with a dash.
func DeleteLocalBranch(ctx context.Context, repo, branch string) error {
	if branch == "" {
		return fmt.Errorf("no branch to delete")
	}
	if _, err := runGit(ctx, repo, "branch", "-D", "--", branch); err != nil {
		return fmt.Errorf("git branch -D %s: %w", branch, err)
	}
	return nil
}
