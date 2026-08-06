package gitworktree

import (
	"fmt"
	"path/filepath"
	"strings"
)

// branchRejected lists the bytes git refuses in a ref name, plus the ones that
// are legal in a ref and dangerous in a path. DEL is included with the C0
// range handled separately below.
const branchRejected = " ~^:?*[\\\x7f"

// worktreesSuffix names the sibling directory linked worktrees live in.
const worktreesSuffix = "-worktrees"

// maxBranchLen bounds the name. It becomes a path SEGMENT, and most
// filesystems refuse a component past 255 bytes — while the IPC frame allows
// megabytes, so without this a huge name is derived into a huge path and
// handed to git.
const maxBranchLen = 255

// ValidateBranch rejects a name that cannot safely be BOTH a git ref and a
// path segment.
//
// Two grammars, deliberately, and neither subsumes the other. The name reaches
// `git worktree add -b <branch>` as argv — so a leading dash reads as a flag,
// and git's own ref rules bar the rest — and it also becomes a path segment
// via DerivePath, so it must not be able to reach outside its parent
// directory. A name legal as one can be dangerous as the other.
//
// Slashes are ACCEPTED: feat/x is an ordinary branch name. DerivePath flattens
// them rather than this function rejecting them.
//
// git itself would refuse most of these, and that is not a reason to skip the
// check: the refusal would arrive as a failed subprocess seconds later, after
// a permit and a single-flight slot have been spent, rather than as a message
// beside the field the user typed into.
func ValidateBranch(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("branch name is empty")
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("branch name may not start with %q — it would read as a flag", "-")
	}
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return fmt.Errorf("branch name may not start or end with %q", "/")
	}
	if strings.Contains(name, "//") {
		return fmt.Errorf("branch name may not contain %q", "//")
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("branch name may not contain %q", "..")
	}
	if strings.Contains(name, "@{") {
		return fmt.Errorf("branch name may not contain %q", "@{")
	}
	if name == "@" {
		return fmt.Errorf("branch name may not be %q", "@")
	}
	if strings.HasSuffix(name, ".") {
		return fmt.Errorf("branch name may not end with %q", ".")
	}
	// PER COMPONENT, not just on the whole name: git applies these rules to
	// every slash-separated part, so "feat/.hidden" and "feat/x.lock" are both
	// refused by git. Checking only the whole name let them through to cost a
	// permit, a single-flight slot and a subprocess before failing — which is
	// exactly the fail-fast property this function exists to provide.
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") {
			return fmt.Errorf("no part of a branch name may start with %q", ".")
		}
		if strings.HasSuffix(part, ".lock") {
			return fmt.Errorf("no part of a branch name may end in %q", ".lock")
		}
	}
	// Bounded. The name becomes a path segment, and most filesystems refuse a
	// component past 255 bytes — but the IPC frame allows megabytes, so
	// without this a huge name is derived into a huge path and handed to git.
	if len(name) > maxBranchLen {
		return fmt.Errorf("branch name is longer than %d characters", maxBranchLen)
	}
	// filepath.IsAbs is platform-dependent, so the colon is checked outright
	// as well: a Linux daemon must still reject "C:/evil", which IsAbs there
	// reports as relative while the colon is a git-illegal ref character
	// anyway.
	if filepath.IsAbs(name) {
		return fmt.Errorf("branch name may not be an absolute path")
	}
	for _, r := range name {
		if r < 0x20 || strings.ContainsRune(branchRejected, r) {
			return fmt.Errorf("branch name may not contain %q", r)
		}
	}
	return nil
}

// DerivePath returns where a worktree for branch belongs: a SIBLING of the
// repository, at <parent>/<repo><worktreesSuffix>/<flattened-branch>.
//
// Sibling rather than nested so the repo tree stays clean — no .gitignore
// entry, and no tool that walks the tree (ripgrep, file watchers, the agent
// itself) finds a second full checkout inside the first.
//
// Separators are flattened rather than preserved: feat/x is an ordinary branch
// name, and honouring it as a path would nest directories under the worktrees
// parent, so two branches could collide on an intermediate segment (feat/x and
// feat would want the same name to be both a file and a directory).
//
// Callers must run ValidateBranch first — this function assumes the name
// cannot escape, and does not re-check.
func DerivePath(repoRoot, branch string) string {
	parent := filepath.Dir(repoRoot)
	name := filepath.Base(repoRoot) + worktreesSuffix
	return filepath.Join(parent, name, strings.ReplaceAll(branch, "/", "-"))
}
