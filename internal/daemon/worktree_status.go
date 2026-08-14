package daemon

import (
	"context"
	"log"
	"time"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
)

// worktreeStatusTimeout bounds ONE request, covering every path in it.
//
// One shared budget rather than one per path, the shape dirsExistResponse uses:
// every entry blocks on the same thing — a repository on an unresponsive mount
// — so a per-path budget lets a tab full of worktrees hold the slot for N times
// as long. Sharing it also makes the timeout self-propagating, since a path
// that spends the budget leaves every later one with a non-positive one.
//
// A var so a test can shorten it; production reads the value declared here.
var worktreeStatusTimeout = 10 * time.Second

// maxWorktreeStatusPaths caps one request.
//
// The client names the paths, so the bound has to hold BY CONSTRUCTION rather
// than because a tab usually holds one or two worktree panes — the same reason
// MaxBrowseEntries and maxDirsExistPaths exist. It bounds both the response
// size and how long the single-flight slot can be held.
const maxWorktreeStatusPaths = 32

// worktreeStatusFn is the seam tests drive, so no test needs a repository or a
// git binary. Same pattern as worktreeListFn beside it.
var worktreeStatusFn = gitworktree.Status

// handleWorktreeStatusReq answers "how much uncommitted work is in these
// worktrees".
//
// Worker goroutine + single-flight, matching handleWorktreeListReq. `git
// status` is the expensive plumbing call gitinfo's ticker deliberately avoids,
// so running it on the requesting connection's dispatch goroutine would freeze
// that client's input for as long as it takes.
func (d *Daemon) handleWorktreeStatusReq(conn *ipc.Conn, msg *ipc.Message) {
	req := worktreeStatusReq(msg)
	rejection, ok := d.beginWorktreeStatus(req)
	if !ok {
		respondTo(conn, msg.ID, ipc.MsgWorktreeStatusResp, rejection)
		return
	}
	go func() {
		defer d.worktreeStatusing.Store(false)
		respondTo(conn, msg.ID, ipc.MsgWorktreeStatusResp, worktreeStatusResponse(req))
	}()
}

// beginWorktreeStatus claims the single-flight slot, returning the rejection to
// send when it is already taken.
//
// Its OWN slot, not worktreeScanning: the setup dialog lists a repository's
// worktrees while the close dialog asks about the ones a tab holds, and a
// shared guard would fail each exactly when it followed the other — the same
// reasoning that gave dirsChecking a slot of its own.
//
// Split from the handler because ipc.Conn cannot be built outside its package,
// which is what lets the slot-independence test exercise the handler's OWN
// claim path rather than poking the atomic directly.
func (d *Daemon) beginWorktreeStatus(req ipc.WorktreeStatusReqPayload) (ipc.WorktreeStatusRespPayload, bool) {
	if d.worktreeStatusing.CompareAndSwap(false, true) {
		return ipc.WorktreeStatusRespPayload{}, true
	}
	return ipc.WorktreeStatusRespPayload{
		Paths: req.Paths,
		Error: "another worktree status check is already running",
	}, false
}

func worktreeStatusReq(msg *ipc.Message) ipc.WorktreeStatusReqPayload {
	var req ipc.WorktreeStatusReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		log.Printf("handleWorktreeStatusReq: decode: %v", err)
	}
	return req
}

// worktreeStatusResponse is the pure half.
//
// CONTRACT: Paths echoes req.Paths VERBATIM on every path, including the
// refusal — it is the client's staleness key, not a statement about what was
// read.
func worktreeStatusResponse(req ipc.WorktreeStatusReqPayload) ipc.WorktreeStatusRespPayload {
	out := ipc.WorktreeStatusRespPayload{Paths: req.Paths}
	if len(req.Paths) > maxWorktreeStatusPaths {
		out.Error = "too many worktrees in one status check"
		return out
	}
	if len(req.Paths) == 0 {
		return out
	}
	// ONE permit for the whole request rather than one per path: the shared
	// deadline already bounds how long it is held, and claiming per path would
	// let a single dialog take several of a pool sized for the whole daemon.
	if !claimBlockingFSCall() {
		out.Error = "too many filesystem calls in flight"
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), worktreeStatusTimeout)
	defer func() {
		cancel()
		releaseBlockingFSCall()
	}()

	for _, path := range req.Paths {
		st := ipc.WorktreeStatus{Path: path}
		changes, err := worktreeStatusFn(ctx, path)
		if err != nil {
			// Reported rather than folded into a 0. A count nobody obtained
			// must not render as "clean", which is the one answer that invites
			// the removal toggle.
			st.Error = err.Error()
		} else {
			st.Changes = changes
		}
		out.Statuses = append(out.Statuses, st)
	}
	return out
}
