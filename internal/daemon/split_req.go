package daemon

import (
	"fmt"
	"log"

	"github.com/artyomsv/quil/internal/claudesessions"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/layouttree"
)

// This file is split_pane_req (spec 5b §3.3): a create that also PLACES the
// pane, the tree built by the daemon under the same lock that publishes the
// pane. Every option is a NAME resolved through the create_pane_req code
// (buildCreatePayload), so a refusal the MCP path gives, this path gives.

// overlayInstanceArgs mirrors internal/tui/overlay.go's function of the same
// name: lazygit takes the repository as a flag; hunk reads its working
// directory and must get NO args, or its base `diff` subcommand is replaced.
func overlayInstanceArgs(kind, repo string) []string {
	if kind == "lazygit" {
		return []string{"--path", repo}
	}
	return nil
}

func (d *Daemon) handleSplitPaneReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.SplitPaneReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		respondTo(conn, msg.ID, ipc.MsgSplitPaneResp, ipc.SplitPaneRespPayload{Error: "malformed payload: " + err.Error()})
		return
	}
	respondTo(conn, msg.ID, ipc.MsgSplitPaneResp, d.splitPane(conn, req))
}

// splitPane runs the request and returns its answer. A worktree checkout is
// handed to a worker; everything else is synchronous on the requesting
// conn's goroutine, which holds no lock any other client needs.
func (d *Daemon) splitPane(conn *ipc.Conn, req ipc.SplitPaneReqPayload) ipc.SplitPaneRespPayload {
	fail := func(tabID, format string, args ...any) ipc.SplitPaneRespPayload {
		return ipc.SplitPaneRespPayload{TabID: tabID, Error: fmt.Sprintf(format, args...)}
	}
	switch req.Placement {
	case ipc.PlacementRight, ipc.PlacementBelow, ipc.PlacementReplace, ipc.PlacementNewTab, ipc.PlacementOverlay:
	default:
		return fail("", "unknown placement %q", req.Placement)
	}
	if req.Placement == ipc.PlacementNewTab {
		return d.splitIntoNewTab(conn, req)
	}

	// The tab comes from the TARGET when there is one; tab_id is never
	// trusted against it.
	tabID := req.TabID
	if req.TargetPaneID != "" {
		target := d.session.Pane(req.TargetPaneID)
		if target == nil {
			return fail("", "no such pane: %s", req.TargetPaneID)
		}
		tabID = target.CurrentTabID()
		target.PluginMu.Lock()
		isOverlay, preparing := target.Overlay, target.PreparingWorktree
		target.PluginMu.Unlock()
		if isOverlay {
			return fail(tabID, "the target pane is an overlay")
		}
		if preparing != "" && req.Placement == ipc.PlacementReplace {
			return fail(tabID, "the target pane is still preparing its worktree")
		}
	} else if req.Placement == ipc.PlacementReplace {
		return fail(tabID, "replace needs a target pane")
	}
	if tabID == "" || d.session.Tab(tabID) == nil {
		return fail(tabID, "no such tab: %s", tabID)
	}
	if req.Placement == ipc.PlacementOverlay {
		return d.splitOverlay(tabID, req)
	}

	// (1) Validate and resolve, outside every lock.
	creq, err := d.splitCreateReq(req.Pane)
	if err != nil {
		return fail(tabID, "%v", err)
	}
	payload, cwd, err := d.buildCreatePayload(creq, tabID, d.defaultCWD(conn))
	if err != nil {
		return fail(tabID, "%v", err)
	}
	if err := d.checkResumeRequest(payload, cwd); err != nil {
		return fail(tabID, "%v", err)
	}
	slot := paneSlot{TabID: tabID}
	switch req.Placement {
	case ipc.PlacementReplace:
		slot.ReplaceID = req.TargetPaneID
	case ipc.PlacementRight:
		slot.Split, slot.TargetID, slot.Dir = true, req.TargetPaneID, layouttree.Horizontal
	case ipc.PlacementBelow:
		slot.Split, slot.TargetID, slot.Dir = true, req.TargetPaneID, layouttree.Vertical
	}

	if payload.Worktree != nil {
		return d.splitIntoWorktree(payload, cwd, slot, req.Pane.Name)
	}

	// (2)–(3) Publish + tree, claim, spawn.
	pane, notice, err := d.buildPane(payload, cwd, payload.Type, buildOpts{Slot: slot, StrictResume: true})
	if pane == nil {
		return fail(tabID, "%v", err)
	}
	applyPaneName(pane, req.Pane.Name)
	// (4) One broadcast and snapshot.
	d.broadcastState()
	d.requestSnapshot()
	resp := ipc.SplitPaneRespPayload{PaneID: pane.ID, TabID: tabID, LayoutRev: d.tabLayoutRev(tabID), Error: spawnErrorOf(pane)}
	if resp.Error == "" {
		resp.Error = notice
	}
	return resp
}

// splitCreateReq maps the dialog's spec onto create_pane_req's payload, so
// buildCreatePayload does the plugin, toggle, kube and worktree-root checks.
// An existing worktree is a CWD that must resolve — never a fallback
// directory (R3-c).
func (d *Daemon) splitCreateReq(s ipc.SplitPaneSpec) (ipc.CreatePaneReqPayload, error) {
	creq := ipc.CreatePaneReqPayload{
		CWD: s.CWD, Type: s.Type, InstanceName: s.InstanceName, InstanceArgs: s.InstanceArgs,
		Name: s.Name, Toggles: s.Toggles, ResumeSessionID: s.ResumeSessionID,
		Sandbox: s.Sandbox, KubeContext: s.KubeContext,
	}
	if w := s.Worktree; w != nil {
		switch {
		case w.Branch != "" && w.ExistingPath != "":
			return creq, fmt.Errorf("a worktree is either a new branch or an existing path, not both")
		case w.Branch != "":
			creq.WorktreeBranch = w.Branch
		case w.ExistingPath != "":
			dir := resolveSpawnDirWithin(w.ExistingPath, spawnDirProbeTimeout)
			if dir == "" {
				return creq, fmt.Errorf("the worktree %q does not exist or did not answer", w.ExistingPath)
			}
			creq.CWD = dir
		default:
			return creq, fmt.Errorf("an empty worktree choice")
		}
	}
	return creq, nil
}

// checkResumeRequest refuses a resume id whose format is wrong, whose
// transcript is not there, or which a live pane already holds — before
// anything is created. The claim itself is made after publish (buildPane).
// A sandbox pane's transcripts live in its container config, so the host
// transcript check is skipped for it.
func (d *Daemon) checkResumeRequest(p ipc.CreatePanePayload, cwd string) error {
	id := p.ResumeSessionID
	if id == "" {
		return nil
	}
	if !resumeSessionIDRe.MatchString(id) {
		return fmt.Errorf("resume_session_id is not a session id")
	}
	if p.Sandbox == nil {
		if exists, answered := transcriptExistsFn(claudesessions.TranscriptPath(cwd, id)); answered && !exists {
			return fmt.Errorf("no Claude session %s in %s", id, cwd)
		}
	}
	if holder, busy := d.claimedClaudeSessionIDs()[id]; busy {
		return fmt.Errorf("that Claude session is already open in pane %s", holder)
	}
	return nil
}

// splitOverlay creates or reuses the tab's overlay (one slot per tab; the
// rule is in PublishPane, so it binds the TUI's create_pane{overlay} too).
// The repository is the page's cwd — it came from git_repos_req — and must
// resolve. The overlay's args are built HERE, never taken from the page.
func (d *Daemon) splitOverlay(tabID string, req ipc.SplitPaneReqPayload) ipc.SplitPaneRespPayload {
	kind := req.OverlayKind
	if kind != "lazygit" && kind != "hunk" {
		return ipc.SplitPaneRespPayload{TabID: tabID, Error: fmt.Sprintf("unknown overlay kind %q", kind)}
	}
	repo := resolveSpawnDirWithin(req.Pane.CWD, spawnDirProbeTimeout)
	if repo == "" {
		return ipc.SplitPaneRespPayload{TabID: tabID, Error: "the overlay needs a repository folder that exists"}
	}
	payload := ipc.CreatePanePayload{TabID: tabID, CWD: repo, Type: kind, InstanceArgs: overlayInstanceArgs(kind, repo), Overlay: true}
	pane, _, err := d.buildPane(payload, repo, kind, buildOpts{Slot: paneSlot{TabID: tabID, OverlayKey: overlayKey(kind, repo)}})
	if pane == nil {
		return ipc.SplitPaneRespPayload{TabID: tabID, Error: err.Error()}
	}
	d.broadcastState()
	d.requestSnapshot()
	return ipc.SplitPaneRespPayload{PaneID: pane.ID, TabID: tabID, LayoutRev: d.tabLayoutRev(tabID), Error: spawnErrorOf(pane)}
}

// splitIntoWorktree is the worktree arm. A split publishes a PTY-less
// placeholder (PreparingWorktree set, so every client shows the spinner) in
// the requested slot now, and the ordinary worktree REPLACE swaps the
// finished pane into that slot (ReplacePane substitutes the tree). A REPLACE
// does not: the target stays until git succeeds (R3-a).
func (d *Daemon) splitIntoWorktree(payload ipc.CreatePanePayload, cwd string, slot paneSlot, name string) ipc.SplitPaneRespPayload {
	tabID := slot.TabID
	if slot.ReplaceID != "" {
		payload.ReplacePaneID = slot.ReplaceID
		go func() {
			resp := d.worktreeAddAndCreate(payload)
			applyPaneName(d.session.Pane(resp.PaneID), name)
		}()
		return ipc.SplitPaneRespPayload{PaneID: slot.ReplaceID, TabID: tabID, LayoutRev: d.tabLayoutRev(tabID), Preparing: true}
	}
	placeholder := d.session.NewPane(cwd)
	// Unpublished: set without PluginMu, before PublishPane makes it visible.
	placeholder.Type = "terminal"
	placeholder.PreparingWorktree = payload.Worktree.Branch
	res, err := d.session.PublishPane(placeholder, slot)
	if err != nil {
		return ipc.SplitPaneRespPayload{TabID: tabID, Error: err.Error()}
	}
	log.Printf("pane created: %s (placeholder, tab=%s, awaiting worktree %s)", placeholder.ID, tabID, payload.Worktree.Branch)
	d.broadcastState()
	d.requestSnapshot()
	payload.ReplacePaneID = placeholder.ID
	go func() {
		resp := d.worktreeAddAndCreate(payload)
		if resp.Error != "" && !resp.Swapped {
			d.failPreparingPane(placeholder.ID, "worktree not created: "+resp.Error)
			return
		}
		applyPaneName(d.session.Pane(resp.PaneID), name)
	}()
	return ipc.SplitPaneRespPayload{PaneID: placeholder.ID, TabID: tabID, LayoutRev: res.LayoutRev, Preparing: true}
}

// splitIntoNewTab is the "new tab" placement: create_tab_req's code. Its
// first pane goes through constructPaneAt, whose resume claim is lenient, so
// a session already held is refused here first.
func (d *Daemon) splitIntoNewTab(conn *ipc.Conn, req ipc.SplitPaneReqPayload) ipc.SplitPaneRespPayload {
	creq, err := d.splitCreateReq(req.Pane)
	if err != nil {
		return ipc.SplitPaneRespPayload{Error: err.Error()}
	}
	if creq.ResumeSessionID != "" {
		if err := d.checkResumeRequest(ipc.CreatePanePayload{ResumeSessionID: creq.ResumeSessionID, Sandbox: creq.Sandbox}, creq.CWD); err != nil {
			return ipc.SplitPaneRespPayload{Error: err.Error()}
		}
	}
	treq := ipc.CreateTabReqPayload{FirstPane: &creq}
	if req.NewTab != nil {
		treq.Name, treq.ProjectID = req.NewTab.Name, req.NewTab.ProjectID
	}
	resp := d.createTabFromReq(conn, treq)
	return ipc.SplitPaneRespPayload{PaneID: resp.PaneID, TabID: resp.TabID, LayoutRev: d.tabLayoutRev(resp.TabID),
		Preparing: resp.PreparingWorktree != "", Error: resp.Error}
}

// tabLayoutRev reads a tab's revision under the session lock.
func (d *Daemon) tabLayoutRev(tabID string) uint64 {
	_, tabs, _, _, _ := d.session.SnapshotState()
	for _, t := range tabs {
		if t.ID == tabID {
			return t.LayoutRev
		}
	}
	return 0
}
