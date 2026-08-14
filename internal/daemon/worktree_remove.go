package daemon

import (
	"context"
	"log"
	"os"
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
// TWO fields, answering two different questions, and conflating them is a
// force-delete of somebody else's checkout. WorktreeOwned says WHETHER a
// directory may be removed — it is set exactly where this daemon ran `git
// worktree add` (createPaneInWorktree), so a pane that merely SITS in a linked
// worktree is never reported here. WorktreePath says WHICH directory, and it
// has to be the recorded one: CWD is a live cursor OSC 7 rewrites on every
// `cd`, so a pane created in feat-a whose shell walked into feat-b would
// otherwise have its close delete feat-b — a worktree Quil never made, with
// whatever uncommitted work was in it. A pane's own child can move that value
// with one escape sequence.
//
// A pane with ownership and NO recorded path (restored from a snapshot written
// before the field existed) is skipped rather than falling back to CWD. Losing
// the offer on an old pane costs a convenience; the fallback costs a checkout.
//
// Deduplicated because splitting a worktree pane gives two panes one directory:
// the second removal would fail against a tree the first already took, and be
// logged as an error for something that worked.
func ownedWorktreePaths(panes []*Pane) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range panes {
		if p == nil {
			continue
		}
		// PluginMu-protected, both of them (session.go). This runs on a conn's
		// dispatch goroutine, concurrent with the snapshot goroutine and with
		// spawnRestoredPane's own writes.
		p.PluginMu.Lock()
		owned, path := p.WorktreeOwned, p.WorktreePath
		p.PluginMu.Unlock()
		if !owned || path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
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
		// A FAILED attempt is not necessarily a whole failed removal. Measured
		// on Windows: git deletes the tree and the admin entry, then fails on
		// the now-empty directory because the pane's shell still has it as its
		// working directory — so it exits non-zero having already deregistered
		// the worktree. Every later attempt then answers "is not a working
		// tree", and the removal is reported as failed while an orphaned
		// directory `git worktree list` no longer mentions is left on disk.
		//
		// Asked of the LISTING rather than of the error text: the text is git's
		// to reword and locale-dependent, while "does git still own this path"
		// is the question that actually decides who finishes the job.
		if !worktreeRegistered(ctx, repo, path) {
			finishRemovedWorktree(path)
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

// worktreeRegistered reports whether git still knows path as a worktree of
// repo.
//
// An unreadable listing answers TRUE — "still git's" — deliberately: this
// decides whether Quil may delete a directory itself, and an unknown answer
// must never be the one that authorises that.
func worktreeRegistered(ctx context.Context, repo, path string) bool {
	list, err := worktreeListFn(ctx, repo)
	if err != nil {
		return true
	}
	for _, w := range list {
		if sameWorktreePath(w.Path, path) {
			return true
		}
	}
	return false
}

// finishRemovedWorktree deletes the directory git left behind after it
// deregistered the worktree but could not remove the folder itself.
//
// It RETRIES on the same budget the git call does, and shipping it without one
// left an empty directory behind on every real close: by the time git has
// deregistered the worktree and handed the folder back to us, the pane's shell
// is still holding it — DestroyPane closes the PTY asynchronously, so the child
// outlives the request by a second or two. Measured against a live daemon; one
// attempt fails for exactly the reason git's did.
func finishRemovedWorktree(path string) {
	var err error
	for attempt := 1; attempt <= worktreeRemoveAttempts; attempt++ {
		err = removeDirFn(path)
		if err == nil || os.IsNotExist(err) {
			// Not-there is the SUCCESS case: git managed the whole removal and
			// only its exit code misled us.
			log.Printf("worktree remove: %s removed (its branch was kept)", path)
			return
		}
		if attempt == worktreeRemoveAttempts {
			break
		}
		time.Sleep(worktreeRemoveBackoff * time.Duration(attempt))
	}
	log.Printf("worktree remove: %s was deregistered by git but the directory remains: %v", path, err)
}

// removeDirFn is the seam for the leftover-directory delete.
//
// os.Remove, NOT os.RemoveAll: it succeeds only on an EMPTY directory, which is
// exactly the residue this case produces (--force already deleted the tracked
// and untracked files). That bound is the point — Quil is finishing git's
// operation here, not performing a recursive delete of its own, and a non-empty
// directory means something happened that this code did not predict and must
// not act on.
var removeDirFn = os.Remove

// sameWorktreePath reports whether two paths name the same directory, with the
// same normalisation pathWithin uses. Named apart from the real-git tests' own
// samePath helper, which additionally resolves symlinks and folds case on every
// platform — appropriate for comparing what git printed against a built path in
// a test, and wrong for a decision that authorises a delete.
func sameWorktreePath(a, b string) bool {
	x, y := filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		x, y = strings.ToLower(x), strings.ToLower(y)
	}
	return x == y
}

// paneInWorktree reports the id of a live pane that lives in the worktree, or
// "" when none does.
//
// TWO tests, because either alone misses a real resident. A pane whose CWD is
// the directory or below it is obviously in it — a shell the user cd'd there, a
// split made in the same place. But a pane CREATED there whose shell has since
// walked out still lives there: a build running in the checkout, an agent that
// cd'd into a subrepo. Asking only where the shell last said it was would
// delete the tree out from under it, which is the same CWD-is-a-cursor mistake
// ownedWorktreePaths documents, one step later.
func (d *Daemon) paneInWorktree(path string) string {
	for _, p := range d.session.AllPanes() {
		p.PluginMu.Lock()
		cwd, home := p.CWD, p.WorktreePath
		p.PluginMu.Unlock()
		if home != "" && pathWithin(path, home) {
			return p.ID
		}
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
