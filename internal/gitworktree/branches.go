package gitworktree

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// maxBranchList bounds how many branch names one listing may carry.
//
// The list rides the worktree-listing RESPONSE, which is a must-deliver frame,
// so an unbounded one is a repository-sized payload on a 64-slot queue. The
// user's own monorepo measures 161 local branches / 5.2 KB, so this leaves two
// orders of magnitude of headroom before anything is clipped.
//
// The cap is only safe because Branches reports that it applied one. A caller
// that cannot see the whole list must not conclude a name is FREE — that is the
// false-negative direction, where a branch git will refuse looks available — so
// the flag is what lets it fall back to "no opinion" instead.
const maxBranchList = 2000

// Branches reports the local branch names of the repository containing dir,
// short (no refs/heads/ prefix), plus whether the list was clipped at
// maxBranchList.
//
// It exists so the setup dialog can refuse a branch name git would refuse. The
// worktree LISTING cannot answer that question: it reports branches that have a
// checkout, and the ordinary way to collide is with a branch whose worktree was
// removed — which is invisible there and is exactly the failure this was written
// for (a create that produced no pane, no dialog error, and a three-second
// status-bar flash).
//
// refs/heads ONLY, deliberately. `git worktree add -b <name>` refuses a name
// that exists as a LOCAL branch; a remote-tracking ref of the same name is not a
// collision, so including refs/remotes would refuse names the user may
// legitimately create. Refusing a valid name is the worse direction here — it
// blocks work with a message that is simply wrong — while missing a collision
// only degrades to the daemon's own error, which the pane now shows.
//
// A directory outside any repository is NOT an error, matching List and for the
// same reason: the setup dialog asks about every directory the user browses to,
// and most are not repositories. The collapse is equally narrow — a missing git
// binary (exec.ErrNotFound) and a call that ran out of time are returned as
// errors, or all three would render as "this repository has no branches", which
// reads as "every name is free".
func Branches(ctx context.Context, dir string) ([]string, bool, error) {
	out, err := runGit(ctx, dir, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || ctx.Err() != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	var list []string
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		name := strings.TrimSpace(line)
		// git appends a trailing newline, so the last field is always empty.
		// An empty entry would match the branch field's own initial state and
		// refuse it before a character has been typed.
		if name == "" {
			continue
		}
		if len(list) == maxBranchList {
			return list, true, nil
		}
		list = append(list, name)
	}
	return list, false, nil
}
