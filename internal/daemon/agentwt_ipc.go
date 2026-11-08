package daemon

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
)

// The cleanup view's requests on the agent-worktree record.
//
// Every request names an entry by its daemon-minted id, and nothing is acted
// on from the record alone: before a removal the daemon asks git again whether
// the path is still a linked worktree of the recorded repository, and before
// a branch delete whether the branch still exists. A record can be days old;
// git is the truth at the moment of the delete.

// Seam for a branch entry's facts, beside branchFactsFn.
var branchFactsForFn = gitworktree.ReadBranchFactsFor

func (d *Daemon) handleAgentWorktreesReq(conn *ipc.Conn, msg *ipc.Message) {
	respondTo(conn, msg.ID, ipc.MsgAgentWorktreesResp, d.agentWorktreesResponse())
}

// agentWorktreesResponse lists the record with what is live about it now:
// whether its pane still exists, and which live pane sits in a worktree.
func (d *Daemon) agentWorktreesResponse() ipc.AgentWorktreesRespPayload {
	var out ipc.AgentWorktreesRespPayload
	for _, e := range d.agentWT.entriesSnapshot() {
		info := ipc.AgentWorktreeInfo{
			ID: e.ID, Kind: e.Kind, Repo: e.Repo, Path: e.Path, Branch: e.Branch,
			FirstSeen: e.FirstSeen.UnixMilli(), Gone: e.Gone,
			PaneID: e.PaneID, PaneName: e.PaneName, PaneLive: e.PaneID != "" && d.session.Pane(e.PaneID) != nil,
			Source: e.Source, Trigger: e.Trigger,
		}
		if e.Kind == agentWTKindWorktree && !e.Gone && e.Path != "" {
			info.InUseBy = d.paneInWorktree(e.Path, false)
		}
		out.Entries = append(out.Entries, info)
	}
	out.UncertainRepos = d.agentWT.uncertainRepos()
	return out
}

func (d *Daemon) handleAgentWorktreeFactsReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.AgentWorktreeFactsReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		respondTo(conn, msg.ID, ipc.MsgAgentWorktreeFactsResp, ipc.AgentWorktreeFactsRespPayload{Error: "malformed request"})
		return
	}
	// A worker and a single-flight slot: the facts are several git calls,
	// and this is the requesting client's dispatch goroutine.
	if !d.agentWTFacting.CompareAndSwap(false, true) {
		respondTo(conn, msg.ID, ipc.MsgAgentWorktreeFactsResp, ipc.AgentWorktreeFactsRespPayload{ID: req.ID, Error: "another check is already running"})
		return
	}
	id := msg.ID
	go func() {
		defer d.agentWTFacting.Store(false)
		respondTo(conn, id, ipc.MsgAgentWorktreeFactsResp, d.agentWorktreeFacts(req.ID))
	}()
}

// agentWorktreeFacts prices one entry: its files and branch for a worktree,
// its branch for a branch.
func (d *Daemon) agentWorktreeFacts(id string) ipc.AgentWorktreeFactsRespPayload {
	out := ipc.AgentWorktreeFactsRespPayload{ID: id}
	e, ok := d.agentWT.byID(id)
	if !ok {
		out.Error = "no such entry"
		return out
	}
	start := time.Now()
	if !claimBlockingFSCall() {
		out.Error = "too many filesystem calls in flight"
		return out
	}
	defer releaseBlockingFSCall()
	ctx, cancel := context.WithTimeout(context.Background(), worktreeStatusTimeout)
	defer cancel()
	// The branch half runs on the same budget as the close dialog's
	// (branchFactsBudget): the view waits as briefly, and a slow branch read
	// must never cost it the file list already read.
	factsCtx, cancelFacts := context.WithDeadline(ctx, start.Add(branchFactsBudget))
	defer cancelFacts()

	switch e.Kind {
	case agentWTKindWorktree:
		if err := verifyAgentWorktree(ctx, e); err != nil {
			out.Error = err.Error()
			return out
		}
		st := ipc.WorktreeStatus{Path: e.Path}
		if rep, err := worktreeStatusFn(ctx, e.Path); err != nil {
			st.Error = err.Error()
		} else {
			st.Changes, st.FilesTruncated = rep.Changes, rep.Truncated
			for _, f := range rep.Files {
				st.Files = append(st.Files, ipc.WorktreeFile{Code: f.Code, Path: f.Path})
			}
		}
		if factsCtx.Err() != nil {
			st.BranchReason = "not checked (out of time)"
		} else {
			fillBranchFacts(factsCtx, e.Path, map[string]string{}, &st)
		}
		out.Status = st
	case agentWTKindBranch:
		if err := verifyAgentBranch(factsCtx, e); err != nil {
			out.Error = err.Error()
			return out
		}
		f, err := branchFactsForFn(factsCtx, e.Repo, e.Branch, defaultBranchRefFn(factsCtx, e.Repo))
		st := ipc.WorktreeStatus{}
		applyBranchFacts(&st, f, err)
		out.Status = st
	default:
		out.Error = "unknown entry kind"
	}
	return out
}

func (d *Daemon) handleAgentWorktreeActReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.AgentWorktreeActReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		respondTo(conn, msg.ID, ipc.MsgAgentWorktreeActResp, ipc.AgentWorktreeActRespPayload{Error: "malformed request"})
		return
	}
	if reason := uncertainAckRefusal(req, d.hellos.roleOf(conn)); reason != "" {
		respondTo(conn, msg.ID, ipc.MsgAgentWorktreeActResp, ipc.AgentWorktreeActRespPayload{ID: req.ID, Error: reason})
		return
	}
	if !d.agentWTActing.CompareAndSwap(false, true) {
		respondTo(conn, msg.ID, ipc.MsgAgentWorktreeActResp, ipc.AgentWorktreeActRespPayload{ID: req.ID, Error: "another cleanup is already running"})
		return
	}
	id := msg.ID
	go func() {
		defer d.agentWTActing.Store(false)
		respondTo(conn, id, ipc.MsgAgentWorktreeActResp, d.agentWorktreeAct(req))
	}()
}

// agentWorktreeAct carries out one request on one entry, after re-checking
// it against git. Refusals change nothing and say why.
func (d *Daemon) agentWorktreeAct(req ipc.AgentWorktreeActReqPayload) ipc.AgentWorktreeActRespPayload {
	out := ipc.AgentWorktreeActRespPayload{ID: req.ID}
	e, ok := d.agentWT.byID(req.ID)
	if !ok {
		out.Error = "no such entry (it may have been forgotten)"
		return out
	}
	if req.Forget {
		d.agentWT.forget(e.ID)
		out.Notice = "Forgotten. Git is unchanged."
		return out
	}
	// A repository whose record may miss items needs an explicit
	// acknowledgment: enforced HERE, not only in the view, because any IPC
	// client can send this request.
	if d.agentWT.repoUncertain(e.Repo) && !req.AcceptUncertain {
		out.Error = "this repository's record may miss items: confirm with y in the view"
		return out
	}
	owner := paneRef{ID: e.PaneID, Name: e.PaneName}
	// The confirmation is filed under the ENTRY's own path: the client's copy
	// of a path is never trusted to say which worktree it is about.
	c := req.Confirmation
	c.WorktreePath = e.Path
	opt := branchDeleteOpts{Local: req.DeleteBranch,
		Owners:    map[string]paneRef{e.Path: owner},
		Confirmed: confirmations([]ipc.BranchConfirmation{c})}

	switch e.Kind {
	case agentWTKindWorktree:
		if !req.RemoveWorktree {
			out.Error = "a worktree's branch goes with the worktree: tick the worktree too"
			return out
		}
		if err := d.checkAgentWorktree(e); err != nil {
			out.Error = err.Error()
			return out
		}
		if used := d.paneInWorktree(e.Path, true); used != "" {
			out.Error = "pane " + used + " is still in this worktree"
			return out
		}
		log.Printf("agent worktrees: removing %s (entry %s)", e.Path, e.ID)
		res := d.removeWorktreeWithBranch(e.Path, opt)
		if !res.removed {
			// The worktree removal itself raises no sidebar card, so this
			// answer is the only place the user learns it did not happen.
			reason := res.reason
			if reason == "" {
				reason = "see quild.log"
			}
			out.Error = "the worktree was not removed: " + reason
			return out
		}
		out.Notice = "Worktree removed."
		if opt.wanted() {
			out.Notice += " Each branch step reports on a sidebar card."
		}
		d.agentWT.schedule(e.Repo, agentWTAttr{Trigger: "cleanup"})
		return out
	case agentWTKindBranch:
		if req.RemoveWorktree || !req.DeleteBranch {
			out.Error = "a branch entry takes only the branch delete"
			return out
		}
		if req.Confirmation.Branch != e.Branch {
			out.Error = "the branch shown is not the recorded one"
			return out
		}
		f, err := d.checkAgentBranch(e)
		if err != nil {
			out.Error = err.Error()
			return out
		}
		log.Printf("agent worktrees: deleting branch %s (entry %s)", e.Branch, e.ID)
		d.deleteBranches(e.Repo, f, opt, owner)
	default:
		out.Error = "unknown entry kind"
		return out
	}
	// The next compare marks what went as gone.
	d.agentWT.schedule(e.Repo, agentWTAttr{Trigger: "cleanup"})
	out.Notice = "Done. Each branch step reports on a sidebar card."
	return out
}

// checkAgentWorktree re-checks a worktree entry against git under a permit.
func (d *Daemon) checkAgentWorktree(e AgentWTEntry) error {
	if !claimBlockingFSCall() {
		return errors.New("too many filesystem calls in flight")
	}
	defer releaseBlockingFSCall()
	ctx, cancel := context.WithTimeout(context.Background(), worktreeStatusTimeout)
	defer cancel()
	return verifyAgentWorktree(ctx, e)
}

// checkAgentBranch re-checks a branch entry and reads its facts.
func (d *Daemon) checkAgentBranch(e AgentWTEntry) (gitworktree.BranchFacts, error) {
	if !claimBlockingFSCall() {
		return gitworktree.BranchFacts{}, errors.New("too many filesystem calls in flight")
	}
	defer releaseBlockingFSCall()
	ctx, cancel := context.WithTimeout(context.Background(), worktreeStatusTimeout)
	defer cancel()
	if err := verifyAgentBranch(ctx, e); err != nil {
		return gitworktree.BranchFacts{}, err
	}
	f, err := branchFactsForFn(ctx, e.Repo, e.Branch, defaultBranchRefFn(ctx, e.Repo))
	if err != nil {
		return f, errors.New("could not read the branch: " + err.Error())
	}
	if f.IsDefault {
		return f, errors.New("it is the default branch")
	}
	if f.DefaultRef == "" {
		return f, errors.New("no default branch found, so unmerged commits cannot be counted")
	}
	return f, nil
}

// verifyAgentWorktree asks git whether the recorded path is still a LINKED
// worktree of the recorded repository. A path git no longer lists, one that
// moved to another repository, or the main checkout never reaches a removal.
func verifyAgentWorktree(ctx context.Context, e AgentWTEntry) error {
	list, err := worktreeListFn(ctx, e.Path)
	if err != nil || len(list) == 0 {
		return errors.New("git does not list this worktree any more")
	}
	if !sameWorktreePath(list[0].Path, e.Repo) {
		return errors.New("this folder belongs to another repository now")
	}
	if sameWorktreePath(list[0].Path, e.Path) {
		return errors.New("this is the main checkout")
	}
	for _, w := range list[1:] {
		if sameWorktreePath(w.Path, e.Path) {
			return nil
		}
	}
	return errors.New("git does not list this worktree any more")
}

// verifyAgentBranch asks git whether the recorded branch still exists. A
// capped listing that does not show it is no answer, and refuses.
func verifyAgentBranch(ctx context.Context, e AgentWTEntry) error {
	branches, _, err := branchListFn(ctx, e.Repo)
	if err != nil {
		return errors.New("could not list the branches")
	}
	for _, b := range branches {
		if b.Name == e.Branch {
			return nil
		}
	}
	return errors.New("the branch no longer exists")
}

// Seam for the branch listing, beside worktreeListFn.
var branchListFn = gitworktree.Branches

// uncertainAckRefusal refuses an uncertain-record acknowledgment from
// anything but a Quil window (hello role "tui"): the acknowledgment stands
// for a person pressing y in the cleanup view, and an MCP bridge, the
// browser gateway or a script has no such view. The role is self-declared,
// so this keeps a cooperating agent from confirming on the user's behalf; a
// local process willing to lie already holds the user's own rights over the
// repository. A standard TCP token is refused earlier (clientauth).
func uncertainAckRefusal(req ipc.AgentWorktreeActReqPayload, role string) string {
	if req.AcceptUncertain && role != "tui" {
		return "only a Quil window can confirm a cleanup in an uncertain repository"
	}
	return ""
}
