package daemon

import (
	"context"
	"log"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/artyomsv/quil/internal/gitworktree"
)

// worktreeRemoveTimeout bounds one `git worktree remove`.
//
// Shorter than worktreeAddTimeout, and the asymmetry is real rather than
// arbitrary: an add CHECKS OUT A TREE, which on a large repository legitimately
// takes minutes, while a remove deletes a directory and rewrites one admin
// file. A removal that has not finished in a minute is stuck on something —
// a dead mount, a file another process will not release — and waiting out an
// add-sized budget for it holds a blocking-FS permit for two more minutes.
const worktreeRemoveTimeout = 60 * time.Second

// worktreeRemoveAttempts and worktreeRemoveBackoff cover the window in which
// the pane's own child is still exiting.
//
// DestroyPane detaches the pane and closes its PTY OFF-LOCK (releasePanes),
// because PTY.Close blocks until the child is reaped and doing that under sm.mu
// starves every reader. So the removal routinely starts while the shell is
// still on its way out, and neither platform lets a directory go while a
// process holds it — Windows fails with a sharing violation, Linux with EBUSY
// on the mount case. One attempt would report a failure for something that
// succeeds a quarter of a second later.
//
// Three attempts rather than a long single wait: if the directory is held by
// something that is NOT exiting — an editor, a file watcher, a shell the user
// has open outside Quil — no amount of waiting helps, and the log line saying
// so is more useful than a minute of silence.
const (
	worktreeRemoveAttempts = 3
	worktreeRemoveBackoff  = 250 * time.Millisecond
)

// removeWorktreeKeepBranchFn is the removal seam. Deliberately NOT
// removeWorktreeFn, which is the abandon-cleanup path and deletes the branch
// with the directory: these two are one keystroke apart in a diff and their
// difference is whether the user's commits survive.
var removeWorktreeKeepBranchFn = gitworktree.RemoveWorktree

// ownedWorktreePaths reports the worktree directories the given panes own, with
// duplicates collapsed.
//
// WorktreeOwned is the ONLY gate, and it is what makes this safe to run from a
// close dialog: it is set exactly where this daemon ran `git worktree add`
// (createPaneInWorktree), so a pane that merely SITS in a linked worktree — one
// the user made by hand, or attached to through the setup dialog — is never
// reported here. Quil deletes what Quil created.
//
// Deduplicated because splitting a worktree pane gives two panes one directory:
// the second removal would fail against a tree the first already took, and be
// logged as an error for something that worked.
func ownedWorktreePaths(panes []*Pane) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range panes {
		if p == nil || !p.WorktreeOwned || p.CWD == "" {
			continue
		}
		if seen[p.CWD] {
			continue
		}
		seen[p.CWD] = true
		out = append(out, p.CWD)
	}
	return out
}

// removeOwnedWorktrees deletes each worktree directory, leaving its branch.
//
// ALWAYS off the requesting connection's dispatch goroutine (the callers `go`
// it): a removal shells out to git against a directory that may be on a network
// mount, and the dispatch goroutine is where that client's input arrives. Same
// rule the add path follows, for the same reason.
//
// Best-effort by contract. Every outcome is logged and nothing is reported back
// to the client: the pane is already gone by the time this runs, so there is no
// dialog left to answer, and a failure leaves the worktree exactly where the
// user can still see it and deal with it.
func (d *Daemon) removeOwnedWorktrees(paths []string) {
	for _, path := range paths {
		// The pane that owned it is gone, but a worktree can host panes in
		// OTHER tabs — a split made in the same directory, a second agent on
		// the same branch. Removing it would delete the working directory out
		// from under processes that are still running in it, which is a
		// data-loss bug wearing this feature's clothes.
		if used := d.paneInWorktree(path); used != "" {
			log.Printf("worktree remove: %s kept, pane %s is still in it", path, used)
			continue
		}
		d.removeOneWorktree(path)
	}
}

// removeOneWorktree resolves the repository and runs the removal, retrying
// while the directory is still held.
func (d *Daemon) removeOneWorktree(path string) {
	if !claimBlockingFSCall() {
		log.Printf("worktree remove: %s kept, too many filesystem calls in flight", path)
		return
	}
	defer releaseBlockingFSCall()

	ctx, cancel := context.WithTimeout(context.Background(), worktreeRemoveTimeout)
	defer cancel()

	// The repository the removal runs in comes from the listing's FIRST entry,
	// which git prints for the main checkout. Running git inside the worktree
	// being deleted is the shape that fails on Windows, where a process cannot
	// remove the directory it is sitting in — and the daemon has no record of
	// the repository a pane's worktree came from: it stores the worktree path
	// and nothing else, deliberately, so this is where the repo is recovered.
	list, err := worktreeListFn(ctx, path)
	if err != nil || len(list) == 0 {
		log.Printf("worktree remove: %s kept, cannot find its repository: %v", path, err)
		return
	}
	repo := list[0].Path
	if repo == "" {
		log.Printf("worktree remove: %s kept, its repository has no main checkout", path)
		return
	}

	for attempt := 1; attempt <= worktreeRemoveAttempts; attempt++ {
		err = removeWorktreeKeepBranchFn(ctx, repo, path)
		if err == nil {
			// Logged on SUCCESS as well, because this is the only record that a
			// directory was deleted: the pane it belonged to is gone, and so is
			// the dialog that asked. The branch is named as surviving because
			// that is the question the user asks next.
			log.Printf("worktree remove: %s removed (its branch was kept)", path)
			return
		}
		if attempt == worktreeRemoveAttempts {
			break
		}
		timer := time.NewTimer(worktreeRemoveBackoff * time.Duration(attempt))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			log.Printf("worktree remove: %s gave up waiting for the directory: %v", path, err)
			return
		}
	}
	log.Printf("worktree remove: %s failed after %d attempts: %v", path, worktreeRemoveAttempts, err)
}

// paneInWorktree reports the id of a live pane whose working directory is the
// worktree or below it, or "" when none is.
func (d *Daemon) paneInWorktree(path string) string {
	for _, p := range d.session.AllPanes() {
		p.PluginMu.Lock()
		cwd := p.CWD
		p.PluginMu.Unlock()
		if cwd != "" && pathWithin(path, cwd) {
			return p.ID
		}
	}
	return ""
}

// pathWithin reports whether child is parent or sits below it.
//
// The separator in the prefix test is load-bearing: a bare strings.HasPrefix
// treats `/w/feat-a2` as inside `/w/feat-a`, so closing one pane would delete a
// sibling worktree's checkout. Case is folded on Windows because a pane's CWD
// is rewritten from OSC 7 — whatever case the shell reports — while the
// worktree path is the one git created.
func pathWithin(parent, child string) bool {
	p := filepath.Clean(parent)
	c := filepath.Clean(child)
	if runtime.GOOS == "windows" {
		p, c = strings.ToLower(p), strings.ToLower(c)
	}
	return p == c || strings.HasPrefix(c, p+string(filepath.Separator))
}
