package daemon

import (
	"context"
	"errors"
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

// removeOwnedWorktreesFn is the test seam for the close-time removal.
var removeOwnedWorktreesFn = func(d *Daemon, paths []string, opt branchDeleteOpts) {
	d.removeOwnedWorktreesWith(paths, opt)
}

// Seams for the branch deletes that may follow a removal.
var (
	deleteLocalBranchFn  = gitworktree.DeleteLocalBranch
	deleteRemoteBranchFn = gitworktree.DeleteRemoteBranch
)

// remoteBranchDeleteTimeout bounds one remote push. It runs with no FS
// permit held (it is network, not disk), but it is still a goroutine and a
// git process; a push that has not finished in 30 s is waiting on something
// that will not answer, and the user learns that from the sidebar card.
var remoteBranchDeleteTimeout = 30 * time.Second

// paneRef names the pane a close came from, captured BEFORE the destroy: the
// branch outcome is reported after the pane is gone, and a card with no pane
// id or name tells the user nothing about which close it belongs to.
type paneRef struct {
	ID, TabID, Name string
}

// branchDeleteOpts says what a close asked for beyond the worktree directory.
//
// Confirmed holds the branch names the dialog SHOWED. It is check-only: the
// daemon deletes the branch it reads from git itself, and only when that name
// is in this list — so a branch an agent switched the worktree to after the
// dialog opened is never deleted, and no client can name a branch to delete.
type branchDeleteOpts struct {
	Local, Remote bool
	Confirmed     []string
	// Owners maps a worktree path (as ownedWorktreePaths returns it) to the
	// pane that owned it.
	Owners map[string]paneRef
}

func (o branchDeleteOpts) wanted() bool { return o.Local || o.Remote }

func (o branchDeleteOpts) confirmed(branch string) bool {
	for _, b := range o.Confirmed {
		if b == branch {
			return true
		}
	}
	return false
}

// worktreeOwners maps each owned worktree path to the first pane that owns it,
// read under PluginMu like ownedWorktreePaths.
func worktreeOwners(panes []*Pane) map[string]paneRef {
	out := map[string]paneRef{}
	for _, p := range panes {
		if p == nil {
			continue
		}
		p.PluginMu.Lock()
		owned, path, name := p.WorktreeOwned, p.WorktreePath, p.Name
		p.PluginMu.Unlock()
		if !owned || path == "" {
			continue
		}
		if _, ok := out[path]; !ok {
			out[path] = paneRef{ID: p.ID, TabID: p.CurrentTabID(), Name: name}
		}
	}
	return out
}

// branchOpts builds the options a destroy payload asked for.
func branchOpts(local, remote bool, confirmed []string, panes []*Pane) branchDeleteOpts {
	opt := branchDeleteOpts{Local: local, Remote: remote, Confirmed: confirmed}
	if opt.wanted() {
		opt.Owners = worktreeOwners(panes)
	}
	return opt
}

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
	d.removeOwnedWorktreesWith(paths, branchDeleteOpts{})
}

// removeOwnedWorktreesWith is removeOwnedWorktrees plus the branch deletes a
// close may ask for.
func (d *Daemon) removeOwnedWorktreesWith(paths []string, opt branchDeleteOpts) {
	for _, path := range paths {
		// The pane that owned it is gone, but a worktree can host panes in
		// OTHER tabs — a split made in the same directory, a second agent on
		// the same branch. Removing it would delete the working directory out
		// from under processes that are still running in it, which is a
		// data-loss bug wearing this feature's clothes.
		if used := d.paneInWorktree(path); used != "" {
			log.Printf("worktree remove: %s kept, pane %s is still in it", path, used)
			if opt.wanted() {
				d.notifyBranchKept(opt.Owners[path], "", "the worktree is still in use by pane "+used)
			}
			continue
		}
		d.removeWorktreeWithBranch(path, opt)
	}
}

// removeWorktreeWithBranch removes one worktree and then, when asked, its
// branch: locally first, then on its remote. The branch facts are read BEFORE
// the removal, because the worktree's entry in git's listing — the only
// record of which branch it had checked out — goes with it.
//
// Each refusal and each failure becomes a sidebar card: the pane and the
// dialog are gone by now, and a branch that silently survived a delete the
// user asked for is the outcome this must never hide.
func (d *Daemon) removeWorktreeWithBranch(path string, opt branchDeleteOpts) {
	res := d.removeOneWorktreeFull(path, opt.wanted())
	if !opt.wanted() {
		return
	}
	owner := opt.Owners[path]
	f := res.facts
	switch {
	case !res.removed:
		d.notifyBranchKept(owner, f.Branch, "the worktree was not removed")
		return
	case res.factsErr != nil:
		d.notifyBranchKept(owner, "", "could not read the branch: "+res.factsErr.Error())
		return
	case f.Detached || f.Branch == "":
		d.notifyBranchKept(owner, "", "the worktree had no branch checked out")
		return
	case f.IsDefault:
		d.notifyBranchKept(owner, f.Branch, "it is the default branch")
		return
	case !opt.confirmed(f.Branch):
		d.notifyBranchKept(owner, f.Branch, "the worktree switched branch after the dialog opened")
		return
	}

	if opt.Local {
		if err := d.deleteLocalBranch(res.repo, f.Branch); err != nil {
			d.notifyBranchKept(owner, f.Branch, err.Error())
		} else {
			d.notifyBranchDeleted(owner, f.Branch, "")
		}
	}
	if opt.Remote {
		switch {
		case !d.remoteBranchDelete.Load():
			d.notifyBranchKept(owner, f.Branch, "remote delete is off in F1 → Settings")
		case f.Remote == "":
			d.notifyBranchKept(owner, f.Branch, "no remote branch: "+f.RemoteReason)
		default:
			ctx, cancel := context.WithTimeout(context.Background(), remoteBranchDeleteTimeout)
			err := deleteRemoteBranchFn(ctx, res.repo, f.Remote, f.RemoteBranch)
			cancel()
			target := f.Remote + "/" + f.RemoteBranch
			if err != nil {
				log.Printf("worktree remove: remote branch %s kept: %v", target, err)
				d.notifyBranchKept(owner, target, err.Error())
			} else {
				log.Printf("worktree remove: remote branch %s deleted", target)
				d.notifyBranchDeleted(owner, target, f.Remote)
			}
		}
	}
}

// deleteLocalBranch runs the local delete under its own FS permit and
// deadline, after the removal released its own.
func (d *Daemon) deleteLocalBranch(repo, branch string) error {
	if !claimBlockingFSCall() {
		return errors.New("too many filesystem calls in flight")
	}
	defer releaseBlockingFSCall()
	ctx, cancel := context.WithTimeout(context.Background(), worktreeRemoveTimeout)
	defer cancel()
	if err := deleteLocalBranchFn(ctx, repo, branch); err != nil {
		log.Printf("worktree remove: branch %s kept: %v", branch, err)
		return err
	}
	log.Printf("worktree remove: branch %s deleted", branch)
	return nil
}

// removeResult is what one worktree removal learned.
type removeResult struct {
	repo     string
	removed  bool
	facts    gitworktree.BranchFacts
	factsErr error
}

// removeOneWorktree resolves the repository and runs the removal, retrying
// while the directory is still held.
func (d *Daemon) removeOneWorktree(path string) {
	d.removeOneWorktreeFull(path, false)
}

// removeOneWorktreeFull is removeOneWorktree, also reading the branch facts
// before the removal when wantFacts, and reporting whether git no longer
// holds the worktree afterwards.
func (d *Daemon) removeOneWorktreeFull(path string, wantFacts bool) (res removeResult) {
	if !claimBlockingFSCall() {
		log.Printf("worktree remove: %s kept, too many filesystem calls in flight", path)
		return res
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
	// The two failures are logged APART because List folds "not a repository"
	// into (nil, nil) deliberately — so a shared branch printed "cannot find
	// its repository: <nil>", which reads as a bug in the logging rather than
	// as the answer it is.
	list, err := worktreeListFn(ctx, path)
	if err != nil {
		log.Printf("worktree remove: %s kept, its repository could not be read: %v", path, err)
		return
	}
	if len(list) == 0 {
		log.Printf("worktree remove: %s kept, it is not inside a git repository", path)
		return
	}
	repo := list[0].Path
	if repo == "" {
		log.Printf("worktree remove: %s kept, its repository has no main checkout", path)
		return
	}
	res.repo = repo
	if wantFacts {
		// Before the removal: the listing entry naming the branch goes with it.
		res.facts, res.factsErr = branchFactsFn(ctx, repo, path, defaultBranchRefFn(ctx, repo))
	}

	for attempt := 1; attempt <= worktreeRemoveAttempts; attempt++ {
		err = removeWorktreeKeepBranchFn(ctx, repo, path)
		if err == nil {
			res.removed = true
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
			// git has let go of it, so its branch is no longer checked out
			// there: a branch delete may follow even if the empty folder stays.
			res.removed = true
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
	return normalizeWorktreePath(a) == normalizeWorktreePath(b)
}

// normalizeWorktreePath is the one normalisation every worktree-path comparison
// in this package uses — pathWithin, sameWorktreePath and the ownership set —
// so a path cannot be considered equal by one of them and not by another.
func normalizeWorktreePath(p string) string {
	c := filepath.Clean(p)
	if runtime.GOOS == "windows" {
		c = strings.ToLower(c)
	}
	return c
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
	p, c := normalizeWorktreePath(parent), normalizeWorktreePath(child)
	return p == c || strings.HasPrefix(c, p+string(filepath.Separator))
}
