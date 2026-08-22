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
//
// It bounds the WIRE PAYLOAD and what this function retains and allocates. It
// does NOT bound how much the daemon READS: runGit uses cmd.Output(), which
// buffers the whole of stdout with no limit, so a repository with a very large
// packed-refs is fully in memory before the scan below sees a byte. That is
// shared with List and is tracked in
// techdebt/3-2-gitworktree-rungit-reads-stdout-unbounded.md — fixing it inside
// runGit needs its own thought about partial records, which is why it is not
// smuggled in here.
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
	// lstrip=2 rather than %(refname:short): "short" is git's shortest
	// UNAMBIGUOUS form, so a repository holding a TAG of the same name gets
	// `heads/foo` instead of `foo` — and branchTaken compares with ==, so the
	// collision it exists to catch would be missed. lstrip=2 drops exactly
	// `refs/heads/` and is deterministic whatever else the ref store holds.
	out, err := runGit(ctx, dir, "for-each-ref", "--format=%(refname:lstrip=2)", "refs/heads")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || ctx.Err() != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	// Scanned line by line rather than via ReplaceAll + Split, and the reason is
	// the rule ValidateBranch states a few files away: "Bounded BEFORE the split
	// below ... splitting a megabyte string and then rejecting it for length
	// would allocate exactly what the bound exists to avoid."
	//
	// The same applies here and the cap was on the wrong side of it: ReplaceAll
	// copies the whole listing, Split adds a string header per line over that
	// copy, and only then does maxBranchList get a say — so a repository with a
	// very large packed-refs (a mirror clone, or one a pane's own child creates)
	// cost roughly three times the listing in one burst, on a daemon that hosts
	// every pane on the machine. Cutting as we go means the cap bounds the
	// allocation it was written to bound.
	//
	// NOT fixed here, and deliberately named rather than left implicit: runGit
	// uses cmd.Output(), which buffers the whole of stdout with no limit, so the
	// listing is still fully READ before this loop sees it. That is shared with
	// List (and Add/Remove/Status), and a truncated `worktree list --porcelain`
	// would parse into a confidently wrong answer rather than an error — so a
	// read limit belongs in a change scoped to that function, with its own
	// thought about partial records, not smuggled in here.
	var list []string
	rest := out
	for rest != "" && len(list) < maxBranchList {
		line, remainder, found := strings.Cut(rest, "\n")
		if found {
			rest = remainder
		} else {
			rest = ""
		}
		// TrimSpace also drops the CR of a CRLF listing, which is why there is
		// no ReplaceAll pass. git appends a trailing newline, so the last field
		// is always empty — and an empty entry would match the branch field's
		// own initial state and refuse it before a character has been typed.
		if name := strings.TrimSpace(line); name != "" {
			list = append(list, name)
		}
	}
	// Truncated only if something was actually left unread. The loop can also
	// exit on a listing that ends exactly at the cap, which is complete.
	return list, strings.TrimSpace(rest) != "", nil
}
