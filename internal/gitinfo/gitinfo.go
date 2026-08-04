// Package gitinfo reads the small set of repository facts Quil shows beside a
// pane: which branch, whether the checkout is a linked worktree, and how far
// it has diverged from its upstream.
//
// Pure and stdlib-only, a sibling of gitdiscover and kubediscover. It shells
// out to git plumbing rather than parsing .git by hand — the formats it would
// have to parse (packed refs, worktree files, HEAD indirection) are exactly
// the ones git changes between releases, and the plumbing commands are the
// stable interface. Every call is a read; nothing here can modify a repository.
//
// `git status --porcelain` is deliberately absent. It is the one call that can
// take seconds on a large repository without fsmonitor, and it would need its
// own cadence, config gate and timeout budget.
package gitinfo

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Info is one checkout's state. The zero value means "nothing known", which is
// what a non-repository and a failed probe both produce — callers distinguish
// them by the ok/error return, not by inspecting fields.
type Info struct {
	// Branch is the checked-out branch name, or "" when HEAD is detached.
	// Detached is tracked separately because "" alone cannot distinguish a
	// detached HEAD from a probe that did not run.
	Branch   string
	Detached bool

	// LinkedWorktree reports a checkout created by `git worktree add`, where
	// the per-worktree git dir differs from the repository's common dir.
	LinkedWorktree bool

	// Ahead/Behind count commits relative to the tracking branch. Meaningful
	// only when HasUpstream; a branch with no upstream is not "0 ahead, 0
	// behind", it is unmeasured.
	HasUpstream bool
	Ahead       int
	Behind      int
}

// Empty reports whether the probe found nothing worth rendering.
func (i Info) Empty() bool {
	return i.Branch == "" && !i.Detached && !i.LinkedWorktree
}

// runGit is the seam every command goes through, so tests never need a real
// repository on disk and the daemon can wrap invocations in its own timeout
// and permit accounting.
//
// It returns raw stdout. Stderr is discarded on purpose: git writes advice
// there that varies by version and locale, and every caller here treats any
// failure identically — the fact is simply unknown.
var runGit = func(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Windows only, and load-bearing there: the daemon runs console-less, so
	// without this every probe allocates a console the user sees flash. See
	// proc_windows.go.
	hideWindow(cmd)
	// CommandContext kills git when the context expires, but Output() still
	// waits for every holder of the stdout pipe to exit — and git's own
	// children (a credential helper, fsmonitor) inherit it. One of those stuck
	// on a dead mount would keep this call parked long past its deadline,
	// holding the caller's blocking-FS permit with it. WaitDelay bounds the
	// wait after the kill; internal/transport/ssh.go bounds its child the same
	// way and for the same reason.
	cmd.WaitDelay = gitWaitDelay
	out, err := cmd.Output()
	return string(out), err
}

// gitWaitDelay caps how long a killed git may hold its output pipe open.
const gitWaitDelay = 2 * time.Second

// Dirs resolves a directory's repository identity: the absolute per-checkout
// git dir and the repository-wide common dir. They differ exactly when this
// checkout is a linked worktree.
//
// The PER-CHECKOUT dir is the cache key a caller wants, not the common one.
// Every linked worktree of a repository shares the common dir while sitting on
// its own branch — which is the entire reason someone creates one — so keying
// a branch cache on the common dir would report every worktree as being on
// whichever branch was probed first. Keying on the per-checkout dir still
// collapses the common case, N panes in different subdirectories of one
// checkout, to a single probe.
//
// ok is false for a directory that is not inside a repository at all. Callers
// are expected to remember that, because re-probing a non-repository costs the
// same as probing a real one.
func Dirs(ctx context.Context, dir string) (gitDir, commonDir string, ok bool) {
	out, err := runGit(ctx, dir, "rev-parse", "--git-dir", "--git-common-dir")
	if err != nil {
		return "", "", false
	}
	lines := splitLines(out)
	if len(lines) < 2 {
		return "", "", false
	}
	if lines[0] == "" || lines[1] == "" {
		return "", "", false
	}
	// git answers relatively when the command runs at the top level, so both
	// paths are resolved against dir before they are compared or used as a
	// key. Two panes in the same checkout would otherwise produce ".git" and
	// an absolute path and be cached apart.
	return absAgainst(dir, lines[0]), absAgainst(dir, lines[1]), true
}

// Probe reads a checkout's branch and divergence. The caller supplies the
// timeout via ctx; every command shares it, so a repository on a mount that
// stopped answering costs one budget rather than one per command.
func Probe(ctx context.Context, dir string) (Info, bool) {
	gitDir, commonDir, ok := Dirs(ctx, dir)
	if !ok {
		return Info{}, false
	}
	info := Info{LinkedWorktree: gitDir != commonDir}

	if out, err := runGit(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		if name := firstLine(out); name == "HEAD" {
			// Plumbing spells a detached HEAD as the literal "HEAD"; there is
			// no branch to name and no upstream to compare against.
			info.Detached = true
		} else {
			info.Branch = name
		}
	}

	// A branch with no upstream fails here, which is the answer — not an
	// error worth surfacing. Skipped for a detached HEAD, where @{u} cannot
	// resolve by construction.
	if !info.Detached {
		if out, err := runGit(ctx, dir, "rev-list", "--left-right", "--count", "@{u}...HEAD"); err == nil {
			if behind, ahead, ok := parseLeftRight(out); ok {
				info.HasUpstream = true
				info.Behind, info.Ahead = behind, ahead
			}
		}
	}
	return info, true
}

// parseLeftRight reads `rev-list --left-right --count @{u}...HEAD`, whose two
// fields are counted from the LEFT and RIGHT sides of the range in that order.
// Left is @{u}, so it is the commits upstream has that we do not — behind.
// Right is HEAD — ahead. Getting this pair backwards is silent and inverts
// what the user is told, so the mapping is named rather than positional at the
// call site.
func parseLeftRight(out string) (behind, ahead int, ok bool) {
	fields := strings.Fields(firstLine(out))
	if len(fields) != 2 {
		return 0, 0, false
	}
	l, err1 := strconv.Atoi(fields[0])
	r, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil || l < 0 || r < 0 {
		return 0, 0, false
	}
	return l, r, true
}

// absAgainst resolves a git-reported path against the directory the command
// ran in. Left alone when already absolute.
func absAgainst(dir, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(dir, p))
}

func splitLines(s string) []string {
	raw := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func firstLine(s string) string {
	if lines := splitLines(s); len(lines) > 0 {
		return lines[0]
	}
	return ""
}
