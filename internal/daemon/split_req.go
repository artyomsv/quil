package daemon

import (
	"fmt"
	"log"

	"github.com/artyomsv/quil/internal/claudesessions"
	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/layouttree"
)

// This file is split_pane_req (spec 5b §3.3): a create that also PLACES the
// pane, the tree built by the daemon under the same lock that publishes the
// pane. Every option is a NAME resolved through the create_pane_req code
// (buildCreatePayload), so a refusal the MCP path gives, this path gives.
//
// Every refusal is decided before anything exists: no tab, no placeholder, no
// `git worktree add`. A step that has to run after the answer (a worktree
// checkout) is returned as a start func the handler calls once the answer is
// sent, so the requester always hears about its pane before anything that
// pane's worker broadcasts.

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
	resp, start := d.splitPane(conn, req)
	respondTo(conn, msg.ID, ipc.MsgSplitPaneResp, resp)
	if start != nil {
		start()
	}
}

// splitPane runs the request and returns its answer, plus the work that must
// begin only once the answer is sent (nil for none). A worktree checkout is
// handed to a worker; everything else is synchronous on the requesting conn's
// goroutine, which holds no lock any other client needs.
func (d *Daemon) splitPane(conn *ipc.Conn, req ipc.SplitPaneReqPayload) (ipc.SplitPaneRespPayload, func()) {
	fail := func(tabID, format string, args ...any) (ipc.SplitPaneRespPayload, func()) {
		return ipc.SplitPaneRespPayload{TabID: tabID, Error: fmt.Sprintf(format, args...)}, nil
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
		return d.splitOverlay(tabID, req), nil
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
	// Here and not only in buildPane: the worktree arm publishes a
	// placeholder and starts a checkout long before buildPane runs.
	if err := d.checkSandboxRequest(payload.Type, cwd, payload.Sandbox); err != nil {
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
	pane, built, err := d.buildPane(payload, cwd, payload.Type, buildOpts{Slot: slot, StrictResume: true})
	if pane == nil {
		return fail(tabID, "%v", err)
	}
	applyPaneName(pane, req.Pane.Name)
	// (4) One broadcast and snapshot.
	d.broadcastState()
	d.requestSnapshot()
	resp := ipc.SplitPaneRespPayload{PaneID: pane.ID, TabID: tabID, LayoutRev: built.LayoutRev, Error: spawnErrorOf(pane)}
	if resp.Error == "" {
		resp.Error = built.Notice
	}
	return resp, nil
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
//
// Claude files a transcript under the directory it RUNS in, so the lookup uses
// the pane's run directory (paneRunDir): cwd as resolved, or — for a new
// worktree — the checkout, which is not where the request pointed.
func (d *Daemon) checkResumeRequest(p ipc.CreatePanePayload, cwd string) error {
	id := p.ResumeSessionID
	if id == "" {
		return nil
	}
	if !resumeSessionIDRe.MatchString(id) {
		return fmt.Errorf("resume_session_id is not a session id")
	}
	if p.Sandbox == nil {
		dir := paneRunDir(p, cwd)
		if exists, answered := transcriptExistsFn(claudesessions.TranscriptPath(dir, id)); answered && !exists {
			return fmt.Errorf("no Claude session %s in %s", id, dir)
		}
	}
	if holder, busy := d.claimedClaudeSessionIDs()[id]; busy {
		return fmt.Errorf("that Claude session is already open in pane %s", holder)
	}
	return nil
}

// paneRunDir is the directory a create's pane will run in. For a new worktree
// that is the checkout: worktreeAddAndCreate creates it at
// gitworktree.DerivePath(RepoRoot, Branch) and spawns the pane there
// (createPaneInWorktree), so the path is known before `git worktree add` runs.
// The root came from git's own worktree list, so it is already the resolved
// path. split_pane_req sets no Subdir; a template's Subdir is not resolved here.
func paneRunDir(p ipc.CreatePanePayload, cwd string) string {
	if w := p.Worktree; w != nil && w.RepoRoot != "" && w.Branch != "" {
		return gitworktree.DerivePath(w.RepoRoot, w.Branch)
	}
	return cwd
}

// checkSandboxRequest validates a sandbox spec the way buildPane will, on a
// scratch pane nobody can see, so a spec that would be refused at build time
// is refused before a tab, a placeholder or a checkout exists.
func (d *Daemon) checkSandboxRequest(paneType, cwd string, spec *ipc.SandboxSpec) error {
	if spec == nil {
		return nil
	}
	if paneType == "" {
		paneType = "terminal"
	}
	scratch := &Pane{ID: "(new pane)", Type: paneType, CWD: cwd}
	return d.applySandboxSpecFor(scratch, spec)
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
	pane, built, err := d.buildPane(payload, repo, kind, buildOpts{Slot: paneSlot{TabID: tabID, OverlayKey: overlayKey(kind, repo)}})
	if pane == nil {
		return ipc.SplitPaneRespPayload{TabID: tabID, Error: err.Error()}
	}
	d.broadcastState()
	d.requestSnapshot()
	return ipc.SplitPaneRespPayload{PaneID: pane.ID, TabID: tabID, LayoutRev: built.LayoutRev, Error: spawnErrorOf(pane)}
}

// splitIntoWorktree is the worktree arm. A split publishes a PTY-less
// placeholder (PreparingWorktree set, so every client shows the spinner) in
// the requested slot now, and the ordinary worktree REPLACE swaps the
// finished pane into that slot (ReplacePane substitutes the tree). A REPLACE
// does not: the target stays until git succeeds (R3-a). Either way the
// checkout starts only after the answer is sent (the returned func).
func (d *Daemon) splitIntoWorktree(payload ipc.CreatePanePayload, cwd string, slot paneSlot, name string) (ipc.SplitPaneRespPayload, func()) {
	tabID := slot.TabID
	branch := payload.Worktree.Branch
	if slot.ReplaceID != "" {
		target := slot.ReplaceID
		payload.ReplacePaneID = target
		start := func() {
			go func() {
				resp := d.worktreeAddAndCreate(payload)
				if resp.Error != "" {
					log.Printf("split replace: worktree %s for pane %s not created: %s", branch, target, resp.Error)
				}
				// The answer said preparing and went, so a failure is told the
				// way a finished worktree is: in the sidebar, on the pane the
				// request named. Except when a pane already carries it — a swap
				// whose new pane failed to start in a tab it left empty gets a
				// recovery pane showing the reason (recoverEmptyTab). A swap in
				// a tab with other panes leaves no pane to carry it, so it is
				// reported like a failed add.
				if resp.Error != "" && !(resp.Swapped && resp.RecoveredTab) {
					d.notifyWorktreeFailed(target, tabID, branch, resp.Error)
				}
				applyPaneName(d.session.Pane(resp.PaneID), name)
			}()
		}
		return ipc.SplitPaneRespPayload{PaneID: target, TabID: tabID, LayoutRev: d.tabLayoutRev(tabID), Preparing: true}, start
	}
	placeholder := d.session.NewPane(cwd)
	// Unpublished: set without PluginMu, before PublishPane makes it visible.
	placeholder.Type = "terminal"
	placeholder.PreparingWorktree = branch
	res, err := d.session.PublishPane(placeholder, slot)
	if err != nil {
		return ipc.SplitPaneRespPayload{TabID: tabID, Error: err.Error()}, nil
	}
	log.Printf("pane created: %s (placeholder, tab=%s, awaiting worktree %s)", placeholder.ID, tabID, branch)
	d.broadcastState()
	d.requestSnapshot()
	payload.ReplacePaneID = placeholder.ID
	start := func() {
		go func() {
			resp := d.worktreeAddAndCreate(payload)
			if resp.Error != "" && !resp.Swapped {
				d.failPreparingPane(placeholder.ID, "worktree not created: "+resp.Error)
				return
			}
			applyPaneName(d.session.Pane(resp.PaneID), name)
		}()
	}
	return ipc.SplitPaneRespPayload{PaneID: placeholder.ID, TabID: tabID, LayoutRev: res.LayoutRev, Preparing: true}, start
}

// splitIntoNewTab is the "new tab" placement: create_tab_req's code, with the
// strict resume check createTabIn runs before its tab exists (the first
// pane's own claim is lenient). The check needs the resolved payload — an
// empty cwd is the project root, and a worktree runs in its checkout — which
// is why it runs inside createTabIn and not here.
func (d *Daemon) splitIntoNewTab(conn *ipc.Conn, req ipc.SplitPaneReqPayload) (ipc.SplitPaneRespPayload, func()) {
	fail := func(err error) (ipc.SplitPaneRespPayload, func()) {
		return ipc.SplitPaneRespPayload{Error: err.Error()}, nil
	}
	creq, err := d.splitCreateReq(req.Pane)
	if err != nil {
		return fail(err)
	}
	treq := ipc.CreateTabReqPayload{FirstPane: &creq}
	if req.NewTab != nil {
		treq.Name, treq.ProjectID = req.NewTab.Name, req.NewTab.ProjectID
	}
	if treq.ProjectID != "" && !d.projectExists(treq.ProjectID) {
		return fail(fmt.Errorf("no such project: %s", treq.ProjectID))
	}
	// Resolved once, here, and handed on; createTabIn does not probe it again.
	cwd, picked := d.newTabCWD(conn, treq)
	// The sandbox and the resume session are checked inside createTabIn,
	// before its tab exists.
	resp, start := d.createTabIn(conn, treq, cwd, picked, true)
	return ipc.SplitPaneRespPayload{PaneID: resp.PaneID, TabID: resp.TabID, LayoutRev: d.tabLayoutRev(resp.TabID),
		Preparing: resp.PreparingWorktree != "", Error: resp.Error}, start
}

// tabLayoutRev reads a tab's revision under the session lock. Used where no
// publish of this request produced one: a replace still waiting on git, and a
// new tab, whose first pane takes no place in a tree.
func (d *Daemon) tabLayoutRev(tabID string) uint64 {
	_, tabs, _, _, _ := d.session.SnapshotState()
	for _, t := range tabs {
		if t.ID == tabID {
			return t.LayoutRev
		}
	}
	return 0
}
