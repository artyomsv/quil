package gitinfo

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MaxChildDirs bounds the one-level walk ListChildRepos makes under a folder
// that is not itself a repository.
//
// It is a refusal threshold, not a prefix cap: a folder holding MORE visible
// subdirectories than this is not walked at all, rather than having its first
// N examined. The walk exists for a folder that GROUPS a few checkouts — a
// worktree set, a ticket's sibling repositories — and a home directory or a
// `~/Projects` root with fifty entries is not that. Examining a prefix of such
// a folder would report "3 repos" for one that holds thirty, which is a wrong
// answer presented as a right one, and it would cost up to N git launches per
// negative-TTL re-resolution for the privilege.
const MaxChildDirs = 16

// ChildRepo is one direct child directory of a non-repository folder that is
// itself inside a repository — in practice its root, since git resolves a
// directory upward and the parent resolved to nothing.
type ChildRepo struct {
	// Dir is the child's path, the directory a probe runs in.
	Dir string
	// GitDir and CommonDir are what Dirs reports for it: the cache key and
	// the linked-worktree discriminator, exactly as for a pane's own CWD.
	GitDir, CommonDir string
}

// ListChildRepos lists the direct child directories of dir that are inside a
// repository, in name order. It is meant to run only after Dirs reported dir
// itself as not a repository; a caller that runs it on a checkout gets that
// checkout's subdirectories back as "repositories", which is true and useless.
//
// One git call per child, every one under the caller's ctx — the whole walk
// shares one deadline, the budget Probe already documents for a mount that
// stopped answering. Hidden entries are skipped (`.git` cannot be one, and a
// `.cache` full of clones is nobody's workspace), and so are symbolic links:
// os.ReadDir reports a link as not-a-directory, which is the bound wanted —
// a link out of the folder is somebody else's checkout, and following links
// is how a one-level walk stops being one level.
//
// Nil when dir cannot be read, has no visible subdirectory, has more than
// MaxChildDirs of them (see the constant), or none of them is a repository.
// The four are the same answer to the caller: nothing to summarise.
func ListChildRepos(ctx context.Context, dir string) []ChildRepo {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		names = append(names, e.Name())
		if len(names) > MaxChildDirs {
			return nil
		}
	}
	sort.Strings(names)

	var out []ChildRepo
	for _, name := range names {
		if ctx.Err() != nil {
			// The deadline is shared: once it has passed, every remaining
			// child would fail the same way, and a partial list is the
			// prefix-cap answer the constant above refuses.
			return nil
		}
		child := filepath.Join(dir, name)
		gitDir, commonDir, ok := Dirs(ctx, child)
		if ctx.Err() != nil {
			// Checked AFTER the probe as well: a deadline spent inside the
			// last child's probe has no next iteration to notice it, and
			// the list so far is the same partial answer (PR #279 review,
			// M-3).
			return nil
		}
		if !ok {
			continue
		}
		out = append(out, ChildRepo{Dir: child, GitDir: gitDir, CommonDir: commonDir})
	}
	return out
}

// Combine folds the probed state of several child repositories into the one
// Info a parent folder reports. The rules, each chosen so the row can never
// claim more than every child agrees on:
//
//   - Branch and Detached are taken only when EVERY child reports the same
//     pair; otherwise both are cleared and ChildRepos alone says what the
//     folder holds, which a client renders as "N repos". A folder whose
//     worktrees sit on one ticket branch — the case this exists for — reads
//     as that branch; one whose repositories have drifted apart says so
//     rather than naming whichever child came first.
//   - LinkedWorktree is set when every child is one, and WorktreeName when
//     every child's name is the same; a mix clears the name and the flag, for
//     the same reason.
//   - Ahead and Behind are SUMMED over the children that have an upstream,
//     and HasUpstream is set when any does. The counts answer "how much is
//     unpushed / unpulled here", and for a folder of repositories that is the
//     total — a maximum would hide a second repository's commits behind the
//     first's, and a per-child breakdown has nowhere to render.
//   - ChildRepos is len(infos), including children whose probe has not
//     answered yet (a zero Info): the count describes the folder, the rest
//     describes what is known about it so far.
//
// The zero Info for no children, so a caller can test Empty on the result
// without special-casing the count.
func Combine(infos []Info) Info {
	if len(infos) == 0 {
		return Info{}
	}
	out := Info{ChildRepos: len(infos)}
	first := infos[0]
	sameBranch, sameWorktree, allLinked := true, true, true
	for _, in := range infos {
		if in.Branch != first.Branch || in.Detached != first.Detached {
			sameBranch = false
		}
		if in.WorktreeName != first.WorktreeName {
			sameWorktree = false
		}
		if !in.LinkedWorktree {
			allLinked = false
		}
		if in.HasUpstream {
			out.HasUpstream = true
			out.Ahead += in.Ahead
			out.Behind += in.Behind
		}
	}
	if sameBranch {
		out.Branch, out.Detached = first.Branch, first.Detached
	}
	if allLinked {
		out.LinkedWorktree = true
		if sameWorktree {
			out.WorktreeName = first.WorktreeName
		}
	}
	return out
}
