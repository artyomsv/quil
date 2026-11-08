package gitworktree

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// BranchFacts is what the close dialog needs to know about the branch a
// worktree has checked out before it may offer to delete that LOCAL branch.
// Every value is read from local refs: no call here touches
// the network, because the dialog asks while the user waits.
type BranchFacts struct {
	// Branch is the short name checked out in the worktree ("" when Detached).
	Branch   string
	Detached bool
	// IsDefault reports that Branch is the repository's default branch, the
	// one branch the dialog never offers to delete.
	IsDefault bool
	// DefaultRef is the fully-qualified default ref Unmerged was counted
	// against ("" when the repository has none).
	DefaultRef string
	// Unmerged counts commits on Branch that DefaultRef does not contain.
	Unmerged int
	// Tip is the commit Branch points at when the facts were read. A delete
	// confirmed against these facts is refused once the tip has moved: the
	// count shown no longer describes what -D would discard.
	Tip string
}

// DefaultBranchRef is the repository's default branch as a FULLY-QUALIFIED
// ref ("refs/remotes/origin/main", "refs/heads/master"), or "" when none
// resolves. Fully qualified for the reason defaultBranch documents: a short
// name resolves refs/heads/<x> before refs/remotes/<x>.
func DefaultBranchRef(ctx context.Context, repo string) string {
	return defaultBranch(ctx, repo)
}

// ShortBranchName turns a fully-qualified ref into the branch name a user
// reads: refs/heads/<b> → <b>, refs/remotes/<remote>/<b> → <b>.
func ShortBranchName(ref string) string {
	if b, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return b
	}
	if rest, ok := strings.CutPrefix(ref, "refs/remotes/"); ok {
		if _, b, ok := strings.Cut(rest, "/"); ok {
			return b
		}
	}
	return ref
}

// ReadBranchFacts describes the branch checked out in the linked worktree at
// worktreePath of the repository repo. defaultRef is DefaultBranchRef's answer
// for that repository, passed in so a caller asking about several worktrees of
// one repository resolves it once.
//
// The branch is read from git's own listing, never from a caller: what the
// dialog offers to delete must be what the worktree actually has checked out.
func ReadBranchFacts(ctx context.Context, repo, worktreePath, defaultRef string) (BranchFacts, error) {
	list, err := List(ctx, repo)
	if err != nil {
		return BranchFacts{}, err
	}
	var wt *Worktree
	for i := range list {
		if samePath(list[i].Path, worktreePath) {
			wt = &list[i]
			break
		}
	}
	if wt == nil && len(list) > 1 {
		// git prints the path as it recorded it, often resolved (/private/tmp
		// for /tmp on macOS) or in another letter case: ask the disk before
		// calling the caller's spelling a stranger. Linked worktrees only —
		// the main checkout is never a branch-facts subject here.
		for i := range list[1:] {
			if same, err := sameFile(list[i+1].Path, worktreePath); err == nil && same {
				wt = &list[i+1]
				break
			}
		}
	}
	if wt == nil {
		return BranchFacts{}, errors.New("not a worktree of this repository")
	}
	if wt.Detached || wt.Branch == "" {
		return BranchFacts{DefaultRef: defaultRef, Detached: true}, nil
	}
	return ReadBranchFactsFor(ctx, repo, wt.Branch, defaultRef)
}

// ReadBranchFactsFor describes a local branch by name — for a branch that has
// no worktree. The caller must have read the name from git itself.
func ReadBranchFactsFor(ctx context.Context, repo, branch, defaultRef string) (BranchFacts, error) {
	var err error
	f := BranchFacts{DefaultRef: defaultRef, Branch: branch}
	f.IsDefault = defaultRef != "" && ShortBranchName(defaultRef) == f.Branch
	local := "refs/heads/" + f.Branch
	tip, err := runGit(ctx, repo, "rev-parse", "--verify", "--quiet", local+"^{commit}")
	if err != nil {
		return f, fmt.Errorf("git rev-parse %s: %w", local, err)
	}
	if f.Tip = strings.TrimSpace(tip); f.Tip == "" {
		return f, fmt.Errorf("git rev-parse %s: no commit", local)
	}

	if defaultRef != "" && !f.IsDefault {
		if f.Unmerged, err = countCommits(ctx, repo, defaultRef, local); err != nil {
			return f, err
		}
	}

	return f, nil
}

// countCommits counts commits reachable from to and not from from. Both are
// fully-qualified refs, so neither can be read as an option.
func countCommits(ctx context.Context, repo, from, to string) (int, error) {
	out, err := runGit(ctx, repo, "rev-list", "--count", from+".."+to)
	if err != nil {
		return 0, fmt.Errorf("git rev-list %s..%s: %w", from, to, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("git rev-list %s..%s: %q is not a count", from, to, strings.TrimSpace(out))
	}
	return n, nil
}

// samePath reports whether git's spelling of a worktree path and a caller's
// name the same directory: git prints forward slashes on Windows, and a pane's
// recorded path can differ in case there.
func samePath(a, b string) bool {
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(ca, cb)
	}
	return ca == cb
}
