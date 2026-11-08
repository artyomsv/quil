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
		respondTo(conn, msg.ID, ipc.MsgWorktreeStatusResp, d.worktreeStatusResponse(req))
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

// worktreeStatusResponse (method) answers only about worktrees this daemon
// OWNS, and hands the rest back with a reason.
//
// This closes the one asymmetry with the destroy payload, which carries a bool
// precisely so no client can name a directory. The status request has to carry
// paths — it is asked before the close, about several worktrees at once — so the
// filter is where the same boundary is enforced. It costs nothing legitimate:
// the dialog only ever asks about worktrees it was offered, which are exactly
// the owned ones.
//
// It matters because `git status` is not as read-only as it looks: it runs in a
// directory the client named and honours that repository's core.fsmonitor,
// which can start a process configured by whoever owns that repository.
//
// An unowned path is refused with a REASON rather than a zero count, for the
// reason every other branch here is: a zero renders as "clean", the one answer
// that invites the removal toggle.
func (d *Daemon) worktreeStatusResponse(req ipc.WorktreeStatusReqPayload) ipc.WorktreeStatusRespPayload {
	// Capped against the REQUEST, before the filter: the response echoes every
	// path the client sent, so the frame bound has to be measured on that
	// rather than on the subset that survives.
	if len(req.Paths) > maxWorktreeStatusPaths {
		return ipc.WorktreeStatusRespPayload{
			Paths: req.Paths,
			Error: "too many worktrees in one status check",
		}
	}
	owned := d.ownedWorktreeTokens()
	tokens := map[string]string{}
	var ask ipc.WorktreeStatusReqPayload
	for _, p := range req.Paths {
		if tok, ok := owned[normalizeWorktreePath(p)]; ok {
			tokens[p] = tok
			ask.Paths = append(ask.Paths, p)
		}
	}
	inner := worktreeStatusResponseFor(ask, tokens)
	byPath := make(map[string]ipc.WorktreeStatus, len(inner.Statuses))
	for _, st := range inner.Statuses {
		byPath[st.Path] = st
	}
	// Paths echoes the REQUEST verbatim, unfiltered — it is the client's
	// staleness key, and a response listing fewer paths than were asked about
	// is one the client drops entirely.
	out := ipc.WorktreeStatusRespPayload{Paths: req.Paths, Error: inner.Error}
	for _, p := range req.Paths {
		if st, ok := byPath[p]; ok {
			out.Statuses = append(out.Statuses, st)
			continue
		}
		out.Statuses = append(out.Statuses, ipc.WorktreeStatus{
			Path:  p,
			Error: "not a worktree this daemon created",
		})
	}
	return out
}

// ownedWorktreeTokens maps every worktree directory this daemon created,
// normalised for comparison, to the identity token recorded for it ("" for a
// pane restored from before identities were recorded — the status check then
// refuses it, as the close does).
func (d *Daemon) ownedWorktreeTokens() map[string]string {
	out := map[string]string{}
	for _, p := range d.session.AllPanes() {
		p.PluginMu.Lock()
		owned, path, tok := p.WorktreeOwned, p.WorktreePath, p.WorktreeToken
		p.PluginMu.Unlock()
		if owned && path != "" {
			if prev, seen := out[normalizeWorktreePath(path)]; !seen || prev == "" {
				out[normalizeWorktreePath(path)] = tok
			}
		}
	}
	return out
}

// worktreeStatusResponse is the pure half.
//
// CONTRACT: Paths echoes req.Paths VERBATIM on every path, including the
// refusal — it is the client's staleness key, not a statement about what was
// read.
func worktreeStatusResponse(req ipc.WorktreeStatusReqPayload) ipc.WorktreeStatusRespPayload {
	return worktreeStatusResponseFor(req, nil)
}

// worktreeStatusResponseFor is worktreeStatusResponse with the identity check:
// when tokens is non-nil, every path must carry a token whose mark still
// matches, or it is refused with a reason before any file is read — the
// files shown must be the files of the checkout Quil made, not of a
// replacement at the same path.
func worktreeStatusResponseFor(req ipc.WorktreeStatusReqPayload, tokens map[string]string) ipc.WorktreeStatusRespPayload {
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

	// The default branch is resolved ONCE per repository per request: a tab
	// holding several worktrees of one repository would otherwise pay up to
	// six git spawns per worktree for the same answer.
	start := time.Now()
	for _, path := range req.Paths {
		st := ipc.WorktreeStatus{Path: path}
		if tokens != nil {
			if reason := ownerMarkRefusal(ctx, path, tokens[path]); reason != "" {
				st.Error, st.BranchReason = keptMessage(reason), keptMessage(reason)
				out.Statuses = append(out.Statuses, st)
				continue
			}
		}
		rep, err := worktreeStatusFn(ctx, path)
		if err != nil {
			// Reported rather than folded into a 0. A count nobody obtained
			// must not render as "clean", which is the one answer that invites
			// the removal toggle.
			st.Error = err.Error()
		} else {
			st.Changes = rep.Changes
			st.FilesTruncated = rep.Truncated
			for _, f := range rep.Files {
				st.Files = append(st.Files, ipc.WorktreeFile{Code: f.Code, Path: f.Path})
			}
		}
		out.Statuses = append(out.Statuses, st)
	}

	// The branch half runs AFTER every file count, on a budget that ends
	// before the dialog gives up waiting (worktreeStatusTimeout client side,
	// 6 s): slow branch checks must never cost the user the file counts they
	// were waiting for. A worktree whose facts did not fit says so.
	factsCtx, cancelFacts := context.WithDeadline(ctx, start.Add(branchFactsBudget))
	defer cancelFacts()
	// The default branch is resolved ONCE per repository per request.
	defaults := map[string]string{}
	for i := range out.Statuses {
		st := &out.Statuses[i]
		switch {
		case st.BranchReason != "":
			// Already refused: not the checkout Quil made.
		case i >= maxBranchFactsPaths:
			st.BranchReason = "not checked (too many worktrees)"
		case factsCtx.Err() != nil:
			st.BranchReason = "not checked (out of time)"
		default:
			fillBranchFacts(factsCtx, st.Path, defaults, st)
		}
	}
	return out
}

// branchFactsBudget is how long one status request may spend, counted from
// its start, before the branch half stops: inside the close dialog's 6 s wait,
// so the file counts always arrive. A var so a test can shorten it.
var branchFactsBudget = 4 * time.Second

// maxBranchFactsPaths bounds how many worktrees of one request get branch
// facts. It matches the close dialog's own row cap (confirmWorktreeMaxRows):
// facts are several git spawns each, all on the request's one deadline, and a
// row the dialog does not draw needs none.
const maxBranchFactsPaths = 6

// Seams for the branch half, so no test needs a repository.
var (
	branchFactsFn      = gitworktree.ReadBranchFacts
	defaultBranchRefFn = gitworktree.DefaultBranchRef
)

// fillBranchFacts adds the branch facts for one worktree to st. Any failure
// leaves the branch NOT deletable with the reason — the dialog offers a local
// delete only on facts it actually read.
func fillBranchFacts(ctx context.Context, path string, defaults map[string]string, st *ipc.WorktreeStatus) {
	list, err := worktreeListFn(ctx, path)
	if err != nil || len(list) == 0 || list[0].Path == "" {
		st.BranchReason = "could not find the repository"
		return
	}
	repo := list[0].Path
	key := normalizeWorktreePath(repo)
	def, ok := defaults[key]
	if !ok {
		def = defaultBranchRefFn(ctx, repo)
		defaults[key] = def
	}
	f, err := branchFactsFn(ctx, repo, path, def)
	applyBranchFacts(st, f, err)
}

// applyBranchFacts copies branch facts onto a status answer. Any failure
// leaves the branch NOT deletable with the reason.
func applyBranchFacts(st *ipc.WorktreeStatus, f gitworktree.BranchFacts, err error) {
	st.Branch = f.Branch
	st.DefaultBranch = gitworktree.ShortBranchName(f.DefaultRef)
	st.Unmerged = f.Unmerged
	st.BranchTip = f.Tip
	switch {
	case err != nil:
		st.BranchReason = "could not read the branch: " + err.Error()
	case f.Detached:
		st.BranchReason = "detached HEAD, no branch"
	case f.IsDefault:
		st.BranchReason = "default branch"
	case f.DefaultRef == "":
		// Without a default branch nothing can say which commits exist only
		// on this one, nor guard the default itself: fail closed.
		st.BranchReason = "no default branch found, so unmerged commits cannot be counted"
	default:
		st.BranchDeletable = true
	}
}
