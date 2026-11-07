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
// worktree has checked out before it may offer to delete that branch, locally
// or on its remote. Every value is read from LOCAL refs: no call here touches
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
	// Remote and RemoteBranch name where the branch is pushed. Both are ""
	// when there is no single answer, and RemoteReason then says why.
	Remote       string
	RemoteBranch string
	RemoteReason string
	// Unpushed counts commits on Branch that the remote-tracking ref lacks.
	Unpushed int
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
	if wt == nil {
		return BranchFacts{}, errors.New("not a worktree of this repository")
	}
	f := BranchFacts{DefaultRef: defaultRef}
	if wt.Detached || wt.Branch == "" {
		f.Detached = true
		return f, nil
	}
	f.Branch = wt.Branch
	f.IsDefault = defaultRef != "" && ShortBranchName(defaultRef) == f.Branch
	local := "refs/heads/" + f.Branch

	if defaultRef != "" && !f.IsDefault {
		if f.Unmerged, err = countCommits(ctx, repo, defaultRef, local); err != nil {
			return f, err
		}
	}

	if err := resolveRemote(ctx, repo, &f); err != nil {
		return f, err
	}
	if f.Remote != "" {
		tracking := "refs/remotes/" + f.Remote + "/" + f.RemoteBranch
		if f.Unpushed, err = countCommits(ctx, repo, tracking, local); err != nil {
			return f, err
		}
	}
	return f, nil
}

// resolveRemote fills Remote/RemoteBranch, or RemoteReason when there is no
// single answer. The configured upstream comes first — it is the branch's own
// record of where it is pushed. Without one, the remote-tracking refs decide,
// and a branch present on two remotes is ambiguous rather than guessed.
func resolveRemote(ctx context.Context, repo string, f *BranchFacts) error {
	remotes, err := remoteNames(ctx, repo)
	if err != nil {
		return err
	}
	out, err := runGit(ctx, repo, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "refs/heads/"+f.Branch+"@{upstream}")
	if up := strings.TrimSpace(out); err == nil && up != "" {
		// "origin/feat/x" is split on the remote NAMES, longest first, so a
		// slash in the branch cannot be read as part of the remote.
		best := ""
		for _, r := range remotes {
			if strings.HasPrefix(up, r+"/") && len(r) > len(best) {
				best = r
			}
		}
		if best != "" {
			rb := strings.TrimPrefix(up, best+"/")
			if refExists(ctx, repo, "refs/remotes/"+best+"/"+rb) {
				f.Remote, f.RemoteBranch = best, rb
				return nil
			}
		}
	}
	var found []string
	for _, r := range remotes {
		if refExists(ctx, repo, "refs/remotes/"+r+"/"+f.Branch) {
			found = append(found, r)
		}
	}
	switch len(found) {
	case 0:
		f.RemoteReason = "no remote branch"
	case 1:
		f.Remote, f.RemoteBranch = found[0], f.Branch
	default:
		f.RemoteReason = "branch exists on several remotes"
	}
	return nil
}

func remoteNames(ctx context.Context, repo string) ([]string, error) {
	out, err := runGit(ctx, repo, "remote")
	if err != nil {
		return nil, fmt.Errorf("git remote: %w", err)
	}
	var names []string
	for _, l := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names, nil
}

func refExists(ctx context.Context, repo, ref string) bool {
	out, err := runGit(ctx, repo, "rev-parse", "--verify", "--quiet", ref)
	return err == nil && strings.TrimSpace(out) != ""
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
