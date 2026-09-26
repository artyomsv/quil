package daemon

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// This file is the MCP create path: create_pane_req and create_tab_req carry
// the same options the Ctrl+N dialog collects, spelled as NAMES the daemon
// resolves rather than as arguments the client assembled.
//
// Everything lands on the SAME payload the TUI sends (ipc.CreatePanePayload)
// and goes down the same validated code — applySandboxSpec,
// applyResumeSessionID, worktreeAddAndCreate — so a refusal the dialog would
// get, an agent gets too. The only translation this file does is
// name → argument, and it refuses rather than guesses.

// resolveToggles turns toggle names into the ArgsWhenOn the plugin declares
// for them. Unknown names and two names from one mutual-exclusion group are
// errors: a toggle an agent misspelled would otherwise silently start the
// pane WITHOUT the permission mode it asked for, which for an unattended
// orchestrator means a pane stuck on a prompt nobody will answer.
func resolveToggles(p *plugin.PanePlugin, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if p == nil {
		return nil, fmt.Errorf("toggles given for a plugin that declares none")
	}
	byName := make(map[string]plugin.Toggle, len(p.Command.Toggles))
	for _, t := range p.Command.Toggles {
		byName[t.Name] = t
	}
	seenGroup := make(map[string]string)
	var args []string
	for _, name := range names {
		t, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("unknown toggle %q for plugin %s (see list_plugins)", name, p.Name)
		}
		if t.Group != "" {
			if prev, dup := seenGroup[t.Group]; dup {
				return nil, fmt.Errorf("toggles %q and %q are mutually exclusive (group %s)", prev, name, t.Group)
			}
			seenGroup[t.Group] = name
		}
		args = append(args, t.ArgsWhenOn...)
	}
	return args, nil
}

// resolveWorktreeRoot answers the repository root for a directory, the way
// the worktree-list request does — with the same permit and deadline, because
// the directory can be a dead mount. The main checkout's own path is the
// root; a bare one has no tree for a sibling to branch from.
func (d *Daemon) resolveWorktreeRoot(cwd string) (string, error) {
	if !claimBlockingFSCall() {
		return "", fmt.Errorf("too many filesystem calls in flight")
	}
	defer releaseBlockingFSCall()
	ctx, cancel := context.WithTimeout(context.Background(), worktreeListTimeout)
	defer cancel()
	list, err := worktreeListFn(ctx, cwd)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", fmt.Errorf("%s is not inside a git repository", cwd)
	}
	if list[0].Bare {
		return "", fmt.Errorf("repository at %s is bare; nothing to branch a worktree from", list[0].Path)
	}
	return list[0].Path, nil
}

// buildCreatePayload translates a request into the payload the TUI path
// consumes. The returned CWD is already resolved (or the fallback) so the
// worktree root is looked up against a directory that exists.
func (d *Daemon) buildCreatePayload(req ipc.CreatePaneReqPayload, tabID, fallbackCWD string) (ipc.CreatePanePayload, string, error) {
	paneType := req.Type
	if paneType == "" {
		paneType = "terminal"
	}
	p := d.registry.Get(paneType)
	if p == nil && paneType != "terminal" {
		return ipc.CreatePanePayload{}, "", fmt.Errorf("unknown plugin type %q (see list_plugins)", paneType)
	}
	// Raw arguments are for plugins with saved instances (ssh, stripe), where
	// the instance IS its argument list. For an AI plugin they REPLACE the
	// command's own arguments, so they bypass every named-toggle check above
	// and can start an agent with an invocation the plugin does not support —
	// the schema says "never for AI panes" and this is what makes that true.
	if len(req.InstanceArgs) > 0 && p != nil && p.Category == "ai" {
		return ipc.CreatePanePayload{}, "", fmt.Errorf("instance_args replace %s's own arguments — use toggles for an AI pane (see list_plugins)", paneType)
	}
	toggleArgs, err := resolveToggles(p, req.Toggles)
	if err != nil {
		return ipc.CreatePanePayload{}, "", err
	}
	// The order the dialog uses: the instance's own args first, then each
	// checked toggle in plugin order. Together they REPLACE the plugin's
	// Command.Args in resolveSpawnArgs, which is the contract the dialog
	// already lives with.
	var instanceArgs []string
	if len(req.InstanceArgs) > 0 || len(toggleArgs) > 0 {
		instanceArgs = append(append([]string(nil), req.InstanceArgs...), toggleArgs...)
	}
	cwd := d.resolveRequestedCWD(req.CWD, fallbackCWD)
	payload := ipc.CreatePanePayload{
		TabID:           tabID,
		CWD:             cwd,
		Type:            paneType,
		InstanceName:    req.InstanceName,
		InstanceArgs:    instanceArgs,
		ResumeSessionID: req.ResumeSessionID,
		Sandbox:         req.Sandbox,
	}
	if req.WorktreeBranch != "" {
		if err := gitworktree.ValidateBranch(req.WorktreeBranch); err != nil {
			return ipc.CreatePanePayload{}, "", err
		}
		root, err := d.resolveWorktreeRoot(cwd)
		if err != nil {
			return ipc.CreatePanePayload{}, "", err
		}
		payload.Worktree = &ipc.WorktreeSpec{RepoRoot: root, Branch: req.WorktreeBranch}
	}
	return payload, cwd, nil
}

// applyPaneName labels a pane the way handleUpdatePane does: bounded, under
// PluginMu, and only when a name was given.
func applyPaneName(pane *Pane, name string) {
	if pane == nil || name == "" {
		return
	}
	pane.PluginMu.Lock()
	pane.Name = truncateField(name, maxPaneNameField)
	pane.PluginMu.Unlock()
}

// spawnErrorOf reads the reason a freshly constructed pane has no process.
func spawnErrorOf(pane *Pane) string {
	if pane == nil {
		return ""
	}
	pane.PluginMu.Lock()
	defer pane.PluginMu.Unlock()
	return pane.SpawnError
}

// createPaneFromReq is the synchronous half of create_pane_req. A worktree
// request is NOT handled here — it blocks on git and belongs on a worker; the
// handler branches before calling this.
func (d *Daemon) createPaneFromReq(payload ipc.CreatePanePayload, cwd string) ipc.CreatePaneRespPayload {
	pane, err := d.createPaneAt(payload, cwd, payload.Type)
	if err != nil && pane == nil {
		return ipc.CreatePaneRespPayload{TabID: payload.TabID, Error: err.Error()}
	}
	d.highlightPane(pane.ID)
	return ipc.CreatePaneRespPayload{PaneID: pane.ID, TabID: payload.TabID, Error: spawnErrorOf(pane)}
}

func (d *Daemon) handleCreatePaneReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.CreatePaneReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		log.Printf("handleCreatePaneReq: decode: %v", err)
		respondTo(conn, msg.ID, ipc.MsgCreatePaneResp, ipc.CreatePaneRespPayload{Error: "malformed payload: " + err.Error()})
		return
	}
	tabID := req.TabID
	if tabID == "" {
		tabID = d.session.ActiveTabID()
	}
	if tabID == "" {
		respondTo(conn, msg.ID, ipc.MsgCreatePaneResp, ipc.CreatePaneRespPayload{Error: "no active tab"})
		return
	}
	if d.session.Tab(tabID) == nil {
		respondTo(conn, msg.ID, ipc.MsgCreatePaneResp, ipc.CreatePaneRespPayload{TabID: tabID, Error: "no such tab: " + tabID})
		return
	}
	payload, cwd, err := d.buildCreatePayload(req, tabID, d.defaultCWD(conn))
	if err != nil {
		respondTo(conn, msg.ID, ipc.MsgCreatePaneResp, ipc.CreatePaneRespPayload{TabID: tabID, Error: err.Error()})
		return
	}
	if payload.Worktree != nil {
		// Same reason handleCreatePane moves this off the dispatch goroutine:
		// a checkout is seconds on a large repository and this goroutine
		// carries every message from the requesting client. The bridge waits
		// with a longer timeout for a worktree create.
		go func() {
			resp := d.worktreeAddAndCreate(payload)
			applyPaneName(d.session.Pane(resp.PaneID), req.Name)
			respondTo(conn, msg.ID, ipc.MsgCreatePaneResp, resp)
		}()
		return
	}
	resp := d.createPaneFromReq(payload, cwd)
	if resp.PaneID != "" {
		applyPaneName(d.session.Pane(resp.PaneID), req.Name)
		if req.Name != "" {
			d.broadcastState()
		}
	}
	respondTo(conn, msg.ID, ipc.MsgCreatePaneResp, resp)
}

// handleCreateTabReq creates a tab in a chosen project with a chosen first
// pane and answers with both ids. Unlike the TUI's create_tab it does NOT
// switch the active tab: an orchestrator opening tabs for workers must not
// yank the user's focus each time; switch_tab exists for the agent that
// wants it.
func (d *Daemon) handleCreateTabReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.CreateTabReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		respondTo(conn, msg.ID, ipc.MsgCreateTabResp, ipc.CreateTabRespPayload{Error: "malformed payload: " + err.Error()})
		return
	}
	if req.ProjectID != "" && !d.projectExists(req.ProjectID) {
		respondTo(conn, msg.ID, ipc.MsgCreateTabResp, ipc.CreateTabRespPayload{Error: "no such project: " + req.ProjectID})
		return
	}
	first := ipc.CreatePaneReqPayload{}
	if req.FirstPane != nil {
		first = *req.FirstPane
	}
	name := req.Name
	if name == "" {
		name = "New Tab"
	}
	// EVERYTHING is validated before the tab exists, so a refused request
	// creates nothing at all — unlike the TUI path, which has a tab on screen
	// to fall back to. Validating after the create left an unknown plugin, a
	// conflicting toggle pair or an unresolvable worktree root with a tab
	// holding a fallback shell nobody asked for, reported through a response
	// that still carried tab_id and therefore read as success at the tool.
	//
	// The fallback CWD is the project root, and it has to be resolved from the
	// project id rather than from the tab: there is no tab yet. An empty id
	// means the active project, which is where CreateTabInProject files it.
	projectID := req.ProjectID
	if projectID == "" {
		projectID = d.session.ActiveProject()
	}
	payload, cwd, err := d.buildCreatePayload(first, "", d.projectCWD(conn, projectID))
	if err != nil {
		respondTo(conn, msg.ID, ipc.MsgCreateTabResp, ipc.CreateTabRespPayload{Error: err.Error()})
		return
	}
	tab := d.session.CreateTabInProject(req.ProjectID, name)
	payload.TabID = tab.ID
	log.Printf("tab created over IPC request: %s %q project=%s", tab.ID, tab.Name, tab.ProjectID)

	if payload.Worktree != nil {
		// The placeholder path handleCreateTab uses: a visible, PTY-less pane
		// carrying the branch, replaced by the requested pane when the add
		// finishes. The requester is answered NOW with the placeholder id;
		// worktree_ready names the pane that replaces it.
		spec := ipc.FirstPaneSpec{Worktree: payload.Worktree}
		pane, err := d.constructPreparingPane(tab.ID, cwd, "terminal", spec)
		if err != nil {
			d.ensureTabNotEmpty(tab.ID)
			d.broadcastState()
			d.requestSnapshot()
			respondTo(conn, msg.ID, ipc.MsgCreateTabResp, ipc.CreateTabRespPayload{TabID: tab.ID, Error: err.Error()})
			return
		}
		applyPaneName(pane, first.Name)
		d.broadcastState()
		d.requestSnapshot()
		respondTo(conn, msg.ID, ipc.MsgCreateTabResp, ipc.CreateTabRespPayload{
			TabID:             tab.ID,
			PaneID:            pane.ID,
			PreparingWorktree: payload.Worktree.Branch,
		})
		payload.ReplacePaneID = pane.ID
		placeholderID := pane.ID
		go func() {
			resp := d.worktreeAddAndCreate(payload)
			if resp.Error != "" && !resp.Swapped {
				d.failPreparingPane(placeholderID, "worktree not created: "+resp.Error)
				return
			}
			applyPaneName(d.session.Pane(resp.PaneID), first.Name)
		}()
		return
	}

	// constructPaneAt, not createPaneAt: tab and pane reach clients as ONE
	// frame, the discipline handleCreateTab documents.
	pane, err := d.constructPaneAt(payload, cwd, payload.Type)
	resp := ipc.CreateTabRespPayload{TabID: tab.ID}
	if err != nil && pane == nil {
		resp.Error = err.Error()
		d.ensureTabNotEmpty(tab.ID)
	} else {
		applyPaneName(pane, first.Name)
		resp.PaneID = pane.ID
		resp.Error = spawnErrorOf(pane)
	}
	d.broadcastState()
	d.requestSnapshot()
	respondTo(conn, msg.ID, ipc.MsgCreateTabResp, resp)
}

// handlePluginCatalogReq answers what create_pane can be asked for, per
// plugin, so an agent discovers toggle names instead of guessing them.
func (d *Daemon) handlePluginCatalogReq(conn *ipc.Conn, msg *ipc.Message) {
	avail := d.registry.Availability()
	plugins := d.registry.All()
	entries := make([]ipc.PluginCatalogEntry, 0, len(plugins))
	for _, p := range plugins {
		e := ipc.PluginCatalogEntry{
			Name:        p.Name,
			DisplayName: p.DisplayName,
			Category:    p.Category,
			Available:   avail[p.Name],
			PromptsCWD:  p.Command.PromptsCWD,
			Sessions:    p.Command.Sessions != "",
		}
		for _, t := range p.Command.Toggles {
			e.Toggles = append(e.Toggles, ipc.PluginToggleInfo{
				Name: t.Name, Label: t.Label, Group: t.Group, Default: t.Default,
			})
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sandboxOK, _ := d.sandboxAvailable(ctx)
	respondTo(conn, msg.ID, ipc.MsgPluginCatalogResp, ipc.PluginCatalogRespPayload{
		Plugins:          entries,
		SandboxAvailable: sandboxOK,
	})
}
