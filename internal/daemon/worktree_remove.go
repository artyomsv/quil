package daemon

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
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
	deleteLocalBranchFn = gitworktree.DeleteLocalBranch
)

// paneRef names the pane a close came from, captured BEFORE the destroy: the
// branch outcome is reported after the pane is gone, and a card with no pane
// id or name tells the user nothing about which close it belongs to.
type paneRef struct {
	ID, TabID, Name string
}

// branchDeleteOpts says what a close asked for beyond the worktree directory.
//
// Confirmed holds, per worktree, the branch the dialog SHOWED. It is
// check-only: the daemon deletes what it reads from git itself, and only when
// it matches the confirmation for THAT worktree — so a branch an agent
// switched the worktree to after the dialog opened is never deleted, one row
// cannot confirm another's, and no client can name a branch to delete. Quil
// never deletes a branch on a remote.
type branchDeleteOpts struct {
	Local bool
	// Confirmed is keyed by confirmKey(worktree path); "" for a branch with
	// no worktree.
	Confirmed map[string]ipc.BranchConfirmation
	// Tokens maps a worktree path to the identity token recorded when Quil
	// created it; a path with no token is never removed (removeOwnedWorktreesWith).
	Tokens map[string]string
	// Owners maps a worktree path (as ownedWorktreePaths returns it) to the
	// pane that owned it.
	Owners map[string]paneRef
}

func (o branchDeleteOpts) wanted() bool { return o.Local }

// confirmKey normalises a worktree path for the confirmation map.
func confirmKey(path string) string {
	if path == "" {
		return ""
	}
	return normalizeWorktreePath(path)
}

// confirmations indexes a payload's confirmations by worktree.
func confirmations(cs []ipc.BranchConfirmation) map[string]ipc.BranchConfirmation {
	out := map[string]ipc.BranchConfirmation{}
	for _, c := range cs {
		out[confirmKey(c.WorktreePath)] = c
	}
	return out
}

// localConfirmed reports that the dialog showed this worktree on branch f.
func (o branchDeleteOpts) localConfirmed(path string, f gitworktree.BranchFacts) bool {
	c, ok := o.Confirmed[confirmKey(path)]
	return ok && f.Branch != "" && c.Branch == f.Branch
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

// branchOpts builds the options a close asked for: the owners (for the cards)
// and the identity tokens are captured for every owned worktree, whether or
// not a branch delete was asked, because the removal itself needs both.
func branchOpts(local bool, confirmed []ipc.BranchConfirmation, panes []*Pane) branchDeleteOpts {
	return branchDeleteOpts{Local: local, Confirmed: confirmations(confirmed),
		Owners: worktreeOwners(panes), Tokens: worktreeTokens(panes)}
}

// worktreeTokens maps each owned worktree path to its identity token ("" when
// none was recorded), read under PluginMu like ownedWorktreePaths.
func worktreeTokens(panes []*Pane) map[string]string {
	out := map[string]string{}
	for _, p := range panes {
		if p == nil {
			continue
		}
		p.PluginMu.Lock()
		owned, path, tok := p.WorktreeOwned, p.WorktreePath, p.WorktreeToken
		p.PluginMu.Unlock()
		if owned && path != "" && out[path] == "" {
			out[path] = tok
		}
	}
	return out
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

// removeOwnedWorktreesWith deletes each worktree directory a close asked to
// remove, plus the local branch deletes opt asks for.
//
// ALWAYS off the requesting connection's dispatch goroutine (the callers `go`
// it): a removal shells out to git against a directory that may be on a
// network mount, and the dispatch goroutine is where that client's input
// arrives. The pane is already gone by the time this runs, so every worktree
// that is KEPT gets a sidebar card saying why (notifyWorktreeKept), and every
// outcome is logged.
func (d *Daemon) removeOwnedWorktreesWith(paths []string, opt branchDeleteOpts) {
	for _, path := range paths {
		owner := opt.Owners[path]
		// IDENTITY first: the close removes only the checkout Quil made. A
		// path with no recorded token (a pane from before identities, or a
		// mark that could not be written) is never removed; the token is
		// checked against the worktree's own git admin directory again just
		// before the removal (removeOneWorktreeFull).
		token := opt.Tokens[path]
		if token == "" {
			log.Printf("worktree remove: %s kept, no identity was recorded for it", path)
			d.notifyWorktreeKept(owner, path, keptNoIdentity)
			continue
		}
		// The pane that owned it is gone, but a worktree can host panes in
		// OTHER tabs — a split made in the same directory, a second agent on
		// the same branch. Removing it would delete the working directory out
		// from under processes that are still running in it, which is a
		// data-loss bug wearing this feature's clothes.
		if used := d.paneInWorktree(path, true); used != "" {
			log.Printf("worktree remove: %s kept, pane %s is still in it", path, used)
			d.notifyWorktreeKept(owner, path, keptInUse)
			continue
		}
		if res := d.removeWorktreeWithBranch(path, opt); !res.removed {
			reason := res.reason
			if reason == "" {
				reason = keptRemoveFailed
			}
			d.notifyWorktreeKept(owner, path, reason)
		}
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
func (d *Daemon) removeWorktreeWithBranch(path string, opt branchDeleteOpts) removeResult {
	res := d.removeOneWorktreeFull(path, opt.Tokens[path], opt.wanted())
	if !opt.wanted() {
		return res
	}
	owner := opt.Owners[path]
	f := res.facts
	switch {
	case !res.removed:
		// The worktree-kept card (close) or the act's answer (cleanup view)
		// already says why; a second branch card would repeat it.
		return res
	case res.factsErr != nil:
		d.notifyBranchKept(owner, "", "could not read the branch: "+res.factsErr.Error())
		return res
	case f.Detached || f.Branch == "":
		d.notifyBranchKept(owner, "", "the worktree had no branch checked out")
		return res
	case f.IsDefault:
		d.notifyBranchKept(owner, f.Branch, "it is the default branch")
		return res
	case f.DefaultRef == "":
		d.notifyBranchKept(owner, f.Branch, "no default branch found, so unmerged commits cannot be counted")
		return res
	case !opt.localConfirmed(path, f):
		d.notifyBranchKept(owner, f.Branch, "the worktree switched branch after the dialog opened")
		return res
	}
	d.deleteBranches(res.repo, f, opt, owner)
	return res
}

// deleteBranches runs the local delete opt asks for, for a branch
// whose facts the caller has already checked (read from git, not the default,
// confirmed).
// Each outcome is a sidebar card for owner.
func (d *Daemon) deleteBranches(repo string, f gitworktree.BranchFacts, opt branchDeleteOpts, owner paneRef) {
	if opt.Local {
		if err := d.deleteLocalBranch(repo, f.Branch); err != nil {
			d.notifyBranchKept(owner, f.Branch, err.Error())
		} else {
			d.notifyBranchDeleted(owner, f.Branch)
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
	// reason says why removed is false, for a caller that reports it.
	reason string
}

// removeOneWorktreeFull is removeOneWorktree, also reading the branch facts
// before the removal when wantFacts, and reporting whether git no longer
// holds the worktree afterwards.
func (d *Daemon) removeOneWorktreeFull(path, token string, wantFacts bool) (res removeResult) {
	if !claimBlockingFSCall() {
		log.Printf("worktree remove: %s kept, too many filesystem calls in flight", path)
		res.reason = keptBusy
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
		res.reason = keptRepoUnreadable
		return
	}
	if len(list) == 0 {
		log.Printf("worktree remove: %s kept, it is not inside a git repository", path)
		res.reason = keptNotRepo
		return
	}
	repo := list[0].Path
	if repo == "" {
		log.Printf("worktree remove: %s kept, its repository has no main checkout", path)
		res.reason = keptNotRepo
		return
	}
	res.repo = repo
	// The checkout's IDENTITY: a token the close captured from the pane,
	// matched against the mark in the worktree's own git admin directory. A
	// checkout replaced at this path since Quil made it has a fresh admin
	// directory and no mark. Checked before the facts read AND before every
	// removal attempt, because the facts read and the retry backoff are
	// windows in which the folder can be replaced. Only the close path passes
	// a token; the cleanup view verifies its agent entry against git's
	// listing instead (verifyAgentWorktree).
	stillOurs := func() bool {
		if token == "" {
			return true
		}
		if reason := ownerMarkRefusal(ctx, path, token); reason != "" {
			log.Printf("worktree remove: %s kept, %s", path, reason)
			res.reason = reason
			return false
		}
		return true
	}
	if !stillOurs() {
		return
	}
	if wantFacts {
		// Before the removal: the listing entry naming the branch goes with it.
		res.facts, res.factsErr = branchFactsFn(ctx, repo, path, defaultBranchRefFn(ctx, repo))
	}

	for attempt := 1; attempt <= worktreeRemoveAttempts; attempt++ {
		if !stillOurs() {
			return
		}
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
			res.reason = keptRemoveFailed
			return
		}
	}
	log.Printf("worktree remove: %s failed after %d attempts: %v", path, worktreeRemoveAttempts, err)
	res.reason = keptRemoveFailed
	return res
}

// worktreeRegistered reports whether git still knows path as a worktree of
// repo.
//
// An unreadable listing answers TRUE — "still git's" — deliberately: this
// decides whether Quil may delete a directory itself, and an unknown answer
// must never be the one that authorises that.
func worktreeRegistered(ctx context.Context, repo, path string) bool {
	list, err := worktreeListStrictFn(ctx, repo)
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
//
// resolve says the answer authorises a DELETE: then every path is also
// compared by the real folder behind it (symlinks resolved), because an agent
// or a shell can sit in the worktree through a link while git lists the real
// path. The listing's "in use" hint passes false: it runs on a dispatch
// goroutine, where a filesystem call is not allowed to wait.
func (d *Daemon) paneInWorktree(path string, resolve bool) string {
	within := pathWithin
	if resolve {
		within = resolvedWithin(path)
	}
	for _, p := range d.session.AllPanes() {
		p.PluginMu.Lock()
		cwd, home := p.CWD, p.WorktreePath
		p.PluginMu.Unlock()
		if home != "" && within(path, home) {
			return p.ID
		}
		if cwd != "" && within(path, cwd) {
			return p.ID
		}
	}
	// A THIRD resident the pane fields cannot show: an agent that entered
	// the worktree (Claude's EnterWorktree) while its pane's CWD stayed put.
	return d.agentWT.paneInDir(path, within)
}

// resolveTimeout bounds ALL symlink resolution of one delete check: a pane
// sitting on a dead network share must not hold the check — and with it the
// cleanup slot — forever. A var so a test can shorten it.
var resolveTimeout = 3 * time.Second

// evalSymlinksFn is the resolution seam, so a test can make one path hang.
var evalSymlinksFn = filepath.EvalSymlinks

// resolvedWithin returns a pathWithin for parent that ALSO matches when the
// two paths name the same real folder: parent and each child are compared
// lexically and then with symlinks resolved, on ONE shared deadline and under
// the process-wide filesystem-call limit. A child that does not exist is
// compared lexically only (a folder that is gone holds nobody). Anything that
// cannot be ANSWERED — a timeout, no permit, an unreadable component — counts
// as a match: the worktree is kept rather than deleted on an unknown.
func resolvedWithin(parent string) func(parent, child string) bool {
	deadline := time.Now().Add(resolveTimeout)
	physParent, parentOK := physicalPathBy(parent, deadline)
	return func(p, child string) bool {
		if foldedWithin(p, child) {
			return true
		}
		if !parentOK {
			return true
		}
		pc, ok := physicalPathBy(child, deadline)
		if !ok {
			return true
		}
		if physParent != "" && pc != "" && foldedWithin(physParent, pc) {
			return true
		}
		// The strings disagree, but the disk may not: a case or Unicode
		// (NFC/NFD) alias on a macOS volume is one folder under two spellings,
		// and only the folder's identity says so.
		same, ok := sameFolderAncestorBy(physParent, pc, deadline)
		return !ok || same
	}
}

// statFn is the identity seam beside evalSymlinksFn.
var statFn = os.Stat

// sameFolderAncestorBy reports whether child, or a folder above it, IS the
// folder parent names on disk (os.SameFile), before deadline. ok is false when
// that cannot be answered (timeout, no permit, a stat error other than "does
// not exist"). An empty or missing parent answers (false, true): a folder that
// is not there holds nobody.
func sameFolderAncestorBy(parent, child string, deadline time.Time) (same, ok bool) {
	if parent == "" || child == "" {
		return false, true
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	stat := statFn // read here: the worker can outlive this call
	type answer struct{ same, ok bool }
	r, err := boundedFSCall(ctx, func() (answer, error) {
		pi, err := stat(parent)
		if errors.Is(err, fs.ErrNotExist) {
			return answer{false, true}, nil
		}
		if err != nil {
			return answer{false, false}, nil
		}
		for d := child; ; {
			ci, err := stat(d)
			switch {
			case err == nil:
				if os.SameFile(pi, ci) {
					return answer{true, true}, nil
				}
			case !errors.Is(err, fs.ErrNotExist):
				return answer{false, false}, nil
			}
			up := filepath.Dir(d)
			if up == d {
				return answer{false, true}, nil
			}
			d = up
		}
	})
	if err != nil {
		return false, false
	}
	return r.same, r.ok
}

// physicalPathBy resolves path before deadline. ok is false when there was no
// answer (timeout, no permit, an error other than "does not exist"); a path
// that does not exist answers ("", true).
func physicalPathBy(path string, deadline time.Time) (string, bool) {
	d := time.Until(deadline)
	if d <= 0 || !claimBlockingFSCall() {
		return "", false
	}
	type result struct {
		path string
		ok   bool
	}
	// Read on the caller's goroutine: the worker can outlive the call (a
	// hung resolution is abandoned), and must not read the seam later.
	eval := evalSymlinksFn
	ch := make(chan result, 1)
	go func() {
		defer releaseBlockingFSCall()
		p, err := eval(path)
		switch {
		case err == nil:
			if abs, aerr := filepath.Abs(p); aerr == nil {
				p = abs
			}
			ch <- result{p, true}
		case errors.Is(err, fs.ErrNotExist):
			// A missing tail does not prove nobody is there: a process keeps
			// its cwd after a rename. Resolve the nearest EXISTING ancestor and
			// re-append the rest, so /link/sub (sub renamed) still names the
			// real worktree it was under.
			if p, ok := resolveNearestAncestor(eval, path); ok {
				ch <- result{p, true}
			} else {
				ch <- result{"", false}
			}
		default:
			ch <- result{"", false}
		}
	}()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.path, r.ok
	case <-timer.C:
		return "", false
	}
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

// resolveNearestAncestor resolves the deepest existing ancestor of path with
// eval and joins the missing remainder back on. ok is false when no ancestor
// answers (an error other than "does not exist" on the way up).
func resolveNearestAncestor(eval func(string) (string, error), path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	rest := ""
	for d := abs; ; {
		p, err := eval(d)
		switch {
		case err == nil:
			if a, aerr := filepath.Abs(p); aerr == nil {
				p = a
			}
			return filepath.Join(p, rest), true
		case !errors.Is(err, fs.ErrNotExist):
			return "", false
		}
		// ENOENT from resolution can also be a DANGLING LINK: the entry is
		// there, its target is not. Its target is where the process went, and
		// the link alone no longer says where that is — unknown, never an
		// ordinary missing component to re-append.
		if _, lerr := lstatFn(d); lerr == nil || !errors.Is(lerr, fs.ErrNotExist) {
			return "", false
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
		rest = filepath.Join(filepath.Base(d), rest)
		d = parent
	}
}

// ownerMarkMatchesFn is the identity-check seam, so tests need no real git.
var ownerMarkMatchesFn = gitworktree.OwnerMarkMatches

// lstatFn is the link-inspection seam beside evalSymlinksFn.
var lstatFn = os.Lstat

// errFSCallUnanswered is boundedFSCall's answer when the call did not finish
// in time, or no permit was free.
var errFSCallUnanswered = errors.New("the filesystem did not answer in time")

// boundedFSCall runs fn on a worker that holds its OWN filesystem-call permit
// until fn really returns — a hung read or write keeps counting against the
// process-wide limit — and gives up waiting when ctx ends. The identity mark is
// plain file I/O after a bounded git call, and on a dead mount the file read
// or write alone could otherwise wait forever while its caller holds a
// single-flight slot (status, worktree add) every later request needs.
func boundedFSCall[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	if !claimBlockingFSCall() {
		return zero, errFSCallUnanswered
	}
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		defer releaseBlockingFSCall()
		v, err := fn()
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		return zero, errFSCallUnanswered
	}
}

// ownerMarkRefusal is "" when the worktree at path is still the checkout Quil
// made and marked with token, else the reason CODE (keptNoIdentity,
// keptIdentityUnknown, keptNotOurs). No token, no answer, a missing or
// different mark all refuse: a replacement at the same path is never shown or
// removed as Quil's. The detail of a failed read goes to quild.log only.
func ownerMarkRefusal(ctx context.Context, path, token string) string {
	if token == "" {
		return keptNoIdentity
	}
	match := ownerMarkMatchesFn // read here: the worker can outlive this call
	ok, err := boundedFSCall(ctx, func() (bool, error) { return match(ctx, path, token) })
	switch {
	case err != nil:
		log.Printf("worktree identity: %s: %v", path, err)
		return keptIdentityUnknown
	case !ok:
		return keptNotOurs
	}
	return ""
}

// Why a worktree a close (or the cleanup view) asked to remove was kept: a
// FIXED set of codes, each with a fixed message for the sidebar card. Git's
// own error text and absolute paths go to quild.log, never to a card.
const (
	keptNoIdentity      = "no_identity"
	keptIdentityUnknown = "identity_unknown"
	keptNotOurs         = "not_ours"
	keptInUse           = "in_use"
	keptRepoUnreadable  = "repo_unreadable"
	keptNotRepo         = "not_in_repository"
	keptBusy            = "busy"
	keptRemoveFailed    = "remove_failed"
	keptHasChanges      = "has_changes"
	keptHasCommits      = "has_commits"
	keptNoDefaultBranch = "no_default_branch"
	keptBranchChanged   = "branch_changed"
)

var keptMessages = map[string]string{
	keptNoIdentity:      "Quil has no record of making this worktree (it predates checkout identities)",
	keptIdentityUnknown: "could not confirm this is the worktree Quil made",
	keptNotOurs:         "this folder is no longer the worktree Quil made for the pane",
	keptInUse:           "another pane is still in it",
	keptRepoUnreadable:  "its repository could not be read",
	keptNotRepo:         "it is not inside a git repository",
	keptBusy:            "too many filesystem calls in flight",
	keptRemoveFailed:    "git could not remove it (see quild.log)",
	keptHasChanges:      "it holds changes that are not committed",
	keptHasCommits:      "its branch has commits the default branch does not",
	keptNoDefaultBranch: "its branch could not be checked for commits (no default branch found)",
	keptBranchChanged:   "it is no longer on the branch Quil created",
}

// keptMessage is the card text for a reason code.
func keptMessage(code string) string {
	if m, ok := keptMessages[code]; ok {
		return m
	}
	return keptMessages[keptRemoveFailed]
}

// foldPathCase says the delete checks compare paths ignoring letter case.
// True on macOS as well as Windows: APFS and HFS+ are case-insensitive by
// default, and EvalSymlinks there keeps the spelling it was given, so /a/WT
// and /a/wt resolve to two strings naming one folder. Folding on a
// case-sensitive volume can only make two folders look like one — the
// worktree is KEPT, never removed by mistake. A var so a test can turn it on.
var foldPathCase = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// foldedWithin is pathWithin, ignoring case when foldPathCase.
func foldedWithin(parent, child string) bool {
	if foldPathCase {
		return pathWithin(strings.ToLower(parent), strings.ToLower(child))
	}
	return pathWithin(parent, child)
}
