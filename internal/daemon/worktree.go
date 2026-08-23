package daemon

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
)

// worktreeListTimeout bounds one listing. `git worktree list` reads the
// repository's admin directory, so it is a readdir-class call like browse —
// not the checkout-class budget an Add would need (stage B).
var worktreeListTimeout = 10 * time.Second

// worktreeListFn is the seam tests drive, so no test needs a repository or a
// git binary. Same pattern as gitProbeFn/gitDirsFn in gitcache.go.
var worktreeListFn = gitworktree.List

// worktreeBranchesFn is the same seam for the branch listing that rides along
// with it.
var worktreeBranchesFn = gitworktree.Branches

// handleWorktreeListReq answers "which worktrees does this repository have".
//
// Worker goroutine + single-flight, matching handleGitReposReq. Its OWN slot
// rather than sharing browseScanning: the setup dialog resolves a directory
// and then lists its worktrees, so one shared guard would make each step fail
// exactly when it followed the other — the same reasoning that gave
// gitDiscovering, kubeDiscovering and dirsChecking slots of their own.
func (d *Daemon) handleWorktreeListReq(conn *ipc.Conn, msg *ipc.Message) {
	rejection, ok := d.beginWorktreeList(msg)
	if !ok {
		respondTo(conn, msg.ID, ipc.MsgWorktreeListResp, rejection)
		return
	}
	fallback := d.defaultCWD()
	go func() {
		defer d.worktreeScanning.Store(false)
		respondTo(conn, msg.ID, ipc.MsgWorktreeListResp,
			worktreeListResponse(worktreeListReq(msg), fallback))
	}()
}

// beginWorktreeList claims the single-flight slot, returning the rejection to
// send when it is already taken. Split from the handler because ipc.Conn
// cannot be built outside its package — same reason as beginGitDiscover, and
// the reason the slot-independence test can exercise the handler's OWN claim
// path rather than poking the atomic directly.
func (d *Daemon) beginWorktreeList(msg *ipc.Message) (ipc.WorktreeListRespPayload, bool) {
	if d.worktreeScanning.CompareAndSwap(false, true) {
		return ipc.WorktreeListRespPayload{}, true
	}
	return ipc.WorktreeListRespPayload{
		Path:  worktreeListReq(msg).Path,
		Error: "another worktree scan is already running",
	}, false
}

func worktreeListReq(msg *ipc.Message) ipc.WorktreeListReqPayload {
	var req ipc.WorktreeListReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		log.Printf("handleWorktreeListReq: decode: %v", err)
	}
	return req
}

// worktreeListResponse is the pure half.
//
// CONTRACT: Path echoes req.Path VERBATIM on every path, including the error
// ones — it is the client's staleness key.
func worktreeListResponse(req ipc.WorktreeListReqPayload, fallback string) ipc.WorktreeListRespPayload {
	out := ipc.WorktreeListRespPayload{Path: req.Path}

	dir := req.Path
	if dir == "" {
		dir = fallback
	}
	if dir == "" {
		out.Error = "no directory to scan and no default available"
		return out
	}

	if !claimBlockingFSCall() {
		out.Error = "too many filesystem calls in flight"
		return out
	}
	// ONE deadline and ONE permit for BOTH git invocations, the arrangement
	// dirsExistResponse and worktreeStatusResponse already use. Giving the
	// branch listing its own budget would double the worst case a hung mount can
	// hold the single-flight slot for, and the shared deadline is
	// self-propagating in the right direction: a listing that spent the whole
	// budget leaves the branch listing to fail immediately, which degrades to
	// "no opinion about branch names" rather than to a second stall.
	//
	// Released by defer rather than inline: what follows the git calls is pure
	// slice building, and two exit paths that each had to remember both calls is
	// how a permit leaks.
	defer releaseBlockingFSCall()
	ctx, cancel := context.WithTimeout(context.Background(), worktreeListTimeout)
	defer cancel()
	list, err := worktreeListFn(ctx, dir)

	if err != nil {
		out.Error = err.Error()
		return out
	}
	if len(list) == 0 {
		// Not a repository. A real answer, deliberately not an Error — the two
		// produce different UI and only one may say "there is no repository".
		//
		// Returning here is also what keeps the branch listing off the browser's
		// hot path: the setup dialog asks about every directory the user walks
		// through and most are not repositories, so a second subprocess for each
		// would be a per-keystroke cost for an answer that is always empty.
		return out
	}

	// AFTER the repository test, and its failure is deliberately not fatal: the
	// worktree list is what the dialog needs to function, while the branch names
	// are an extra check whose absence degrades to the behaviour that shipped
	// before it existed — git's own refusal at create time.
	branches, truncated, bErr := worktreeBranchesFn(ctx, dir)
	if bErr != nil {
		// %q, not %s: dir comes straight off the wire unvalidated and may carry
		// ESC/CSI/OSC bytes and newlines, and quild.log is a plain file people
		// read with cat and tail — which interpret them. Same hazard
		// sanitizeRemoteText polices on the render side (CWE-117).
		log.Printf("worktree list: branches for %q: %v", dir, bErr)
	} else {
		out.Branches = branches
		out.BranchesTruncated = truncated
	}

	out.Repo = true
	out.Worktrees = make([]ipc.WorktreeInfo, 0, len(list))
	for _, w := range list {
		out.Worktrees = append(out.Worktrees, ipc.WorktreeInfo{
			Path:     w.Path,
			Branch:   w.Branch,
			Detached: w.Detached,
			Main:     w.Main,
			Locked:   w.Locked,
			Prunable: w.Prunable,
			Bare:     w.Bare,
		})
	}

	main := list[0]
	out.Root = main.Path
	// A bare main checkout has no working tree, so nothing can be its sibling.
	// Left empty rather than guessed; the field renders the reason.
	if !main.Bare {
		out.WorktreeRoot = filepath.Join(filepath.Dir(main.Path),
			filepath.Base(main.Path)+"-worktrees")
	}
	return out
}
