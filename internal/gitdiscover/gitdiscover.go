// Package gitdiscover finds git repositories near a directory: the enclosing
// repo (upward walk, same discovery rule as git itself) and immediate
// sub-repositories (one level down). Pure filesystem probes — no git binary,
// no I/O errors surfaced: every failure degrades to "no candidates" so
// discovery can never block pane creation.
package gitdiscover

import (
	"os"
	"path/filepath"
)

// maxWalkUp bounds EnclosingRepo's upward walk. The walk already terminates
// at the volume root (filepath.Dir fixpoint); the cap is a belt-and-suspenders
// guard against symlink-loop pathologies.
const maxWalkUp = 32

// hasGitEntry reports whether dir contains a .git entry. Both shapes count:
// a directory (normal repo) and a regular file (worktree / submodule).
func hasGitEntry(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		return false
	}
	return info.IsDir() || info.Mode().IsRegular()
}

// EnclosingRepo walks up from dir and returns the first ancestor (or dir
// itself) containing a .git entry. Symlinks are resolved first to match the
// daemon's defaultCWD discipline.
func EnclosingRepo(dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	cur := abs
	for i := 0; i < maxWalkUp; i++ {
		if hasGitEntry(cur) {
			return cur, true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		cur = parent
	}
	return "", false
}

// SubRepos returns the immediate (one level down) subdirectories of dir that
// contain a .git entry. Returns nil on any read error.
func SubRepos(dir string) []string {
	if dir == "" {
		return nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil
	}
	var repos []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(abs, e.Name())
		if hasGitEntry(sub) {
			repos = append(repos, sub)
		}
	}
	return repos
}

// Candidates returns the repos to offer for dir: the enclosing repo first
// (if any), then one-level sub-repos, deduplicated, absolute paths.
func Candidates(dir string) []string {
	if dir == "" {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	if root, ok := EnclosingRepo(dir); ok {
		out = append(out, root)
		seen[root] = true
	}
	for _, r := range SubRepos(dir) {
		if !seen[r] {
			out = append(out, r)
			seen[r] = true
		}
	}
	return out
}
