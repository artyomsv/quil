package daemon

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
)

// templateCreation is a request-local snapshot. No template is retained on a
// pane: all arguments, directories and prompts are resolved from this copy.
type templateCreation struct {
	req      ipc.CreateFromTemplateReqPayload
	tpl      config.Template
	cwd      string
	payloads []ipc.CreatePanePayload
	agents   []bool
}

func (d *Daemon) validateTemplateCreation(req ipc.CreateFromTemplateReqPayload) (*templateCreation, error) {
	if config.UnsafeTemplateText(req.Task) || config.UnsafeTemplateText(req.CWD) {
		return nil, fmt.Errorf("task or directory contains terminal control characters")
	}
	if len(req.Task) > 128*1024 {
		return nil, fmt.Errorf("task exceeds 128 KiB")
	}
	if req.ProjectID == "" {
		req.ProjectID = d.session.ActiveProject()
	}
	if req.ProjectID != "" && !d.projectExists(req.ProjectID) {
		return nil, fmt.Errorf("no such project: %s", req.ProjectID)
	}
	templates, err := config.LoadTemplates()
	if err != nil {
		return nil, fmt.Errorf("load templates: %w", err)
	}
	tpl, ok := templates.ByName(req.Template)
	if !ok {
		return nil, fmt.Errorf("unknown template %q", req.Template)
	}
	// Read the actual project root, not projectCWD: its fallback would turn
	// an unusable chosen directory into a successful create somewhere else.
	chosen := req.CWD
	if chosen == "" {
		for _, project := range d.session.Projects() {
			if project.ID == req.ProjectID {
				chosen = project.RootDir
				break
			}
		}
		if chosen == "" {
			chosen, err = os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("working directory: %w", err)
			}
		}
	}
	abs, err := filepath.Abs(chosen)
	if err != nil {
		return nil, fmt.Errorf("directory %q: %w", chosen, err)
	}
	cwd := resolveSpawnDirWithin(abs, spawnDirProbeTimeout)
	if cwd == "" {
		return nil, fmt.Errorf("directory %q is missing, not a directory, or did not answer", chosen)
	}
	plan := &templateCreation{req: req, tpl: tpl, cwd: cwd}
	for i, pane := range tpl.Panes {
		p := d.registry.Get(pane.Type)
		if p == nil || !p.Available {
			return nil, fmt.Errorf("template %q pane %d: plugin %q is unknown or unavailable", tpl.Name, i+1, pane.Type)
		}
		args, err := resolveToggles(p, pane.Toggles)
		if err != nil {
			return nil, fmt.Errorf("template %q pane %d: %w", tpl.Name, i+1, err)
		}
		permissionMode, selected := false, false
		for _, toggle := range p.Command.Toggles {
			if toggle.Group == "permission_mode" {
				permissionMode = true
				for _, name := range pane.Toggles {
					selected = selected || name == toggle.Name
				}
			}
		}
		if p.Category == "ai" && permissionMode && !selected {
			return nil, fmt.Errorf("template %q pane %d: %s requires a permission_mode toggle", tpl.Name, i+1, pane.Type)
		}
		if pane.QuilMCP && !mcpSupported(pane.Type) {
			return nil, fmt.Errorf("template %q pane %d: plugin %q has no per-spawn Quil MCP support", tpl.Name, i+1, pane.Type)
		}
		// Match resolveSpawnArgs' base-argument behavior, then freeze the model
		// together with the selected toggle arguments before allocating a pane.
		if len(args) == 0 {
			args = append([]string(nil), p.Command.Args...)
		}
		if p.Category == "ai" {
			args = mcpModelArgs(pane.Type, pane.Model, args)
		}
		dir := cwd
		if i == 0 && req.Branch != "" {
			// Subdir validates pane 0 against the actual new checkout. Only
			// lexical checks belong here; this tree may lack its directory.
			err = validateTemplatePaneRelative(cwd, pane.CWD)
		} else {
			dir, err = templatePaneDirectory(cwd, pane.CWD)
		}
		if err != nil {
			return nil, fmt.Errorf("template %q pane %d: %w", tpl.Name, i+1, err)
		}
		plan.payloads = append(plan.payloads, ipc.CreatePanePayload{
			Type: pane.Type, CWD: dir, InstanceArgs: args, QuilMCP: pane.QuilMCP,
		})
		plan.agents = append(plan.agents, p.Category == "ai")
	}
	if req.Branch != "" {
		if err := gitworktree.ValidateBranch(req.Branch); err != nil {
			return nil, err
		}
		root, err := d.resolveWorktreeRoot(cwd)
		if err != nil {
			return nil, err
		}
		if !filepath.IsAbs(root) {
			return nil, fmt.Errorf("repository path must be absolute")
		}
		plan.payloads[0].Worktree = &ipc.WorktreeSpec{RepoRoot: root, Branch: req.Branch, Subdir: tpl.Panes[0].CWD}
	}
	return plan, nil
}

// Both lexical traversal and symlink escape are refused. The config validator
// checks the former too; checking the resolved path here enforces the boundary
// on this machine, with the same bounded filesystem probe as ordinary creates.
func templatePaneDirectory(root, relative string) (string, error) {
	if err := validateTemplatePaneRelative(root, relative); err != nil {
		return "", err
	}
	path := filepath.Join(root, relative)
	dir := probeSpawnDirWithin(path, spawnDirProbeTimeout, true)
	if dir == "" {
		return "", fmt.Errorf("directory %q is missing, not a directory, or did not answer", path)
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("directory %q leaves the chosen directory %q", path, root)
	}
	return dir, nil
}

func validateTemplatePaneRelative(root, relative string) error {
	// WorktreeSpec also arrives directly over IPC, without config validation.
	// Reject absolute and traversing paths in either platform's spelling.
	if filepath.IsAbs(relative) || strings.HasPrefix(relative, `\`) || len(relative) > 1 && relative[1] == ':' {
		return fmt.Errorf("directory %q must be relative to %q", relative, root)
	}
	for _, segment := range strings.FieldsFunc(relative, func(r rune) bool { return r == '/' || r == '\\' }) {
		if segment == ".." {
			return fmt.Errorf("directory %q leaves the chosen directory %q", relative, root)
		}
	}
	return nil
}

func templateTabName(task, fallback string) string {
	var b strings.Builder
	separator := false
	for _, r := range strings.ToLower(task) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if separator && b.Len() > 0 && b.Len() < 63 {
				b.WriteByte('-')
			}
			separator = false
			if b.Len() >= 64 {
				break
			}
			b.WriteRune(r)
		} else {
			separator = true
		}
	}
	if b.Len() == 0 {
		return fallback
	}
	return b.String()
}

func (d *Daemon) handleCreateFromTemplateReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.CreateFromTemplateReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		respondTo(conn, msg.ID, ipc.MsgCreateFromTemplateResp, ipc.CreateFromTemplateRespPayload{Error: "malformed payload: " + err.Error()})
		return
	}
	// Directory and repository probes belong off the client's dispatch loop.
	go func() {
		plan, err := d.validateTemplateCreation(req)
		if err != nil {
			respondTo(conn, msg.ID, ipc.MsgCreateFromTemplateResp, ipc.CreateFromTemplateRespPayload{Error: err.Error()})
			return
		}
		select {
		case <-d.shutdown:
			respondTo(conn, msg.ID, ipc.MsgCreateFromTemplateResp, ipc.CreateFromTemplateRespPayload{Error: "daemon is shutting down"})
			return
		default:
		}
		tab := d.session.CreateTabInProject(plan.req.ProjectID, templateTabName(req.Task, plan.tpl.Name))
		for i := range plan.payloads {
			plan.payloads[i].TabID = tab.ID
		}
		d.session.mu.Lock()
		tab.TemplateLayout = plan.tpl.LayoutKeyword()
		d.session.mu.Unlock()
		if req.Branch == "" {
			resp := d.completeTemplateCreation(plan, tab.ID, nil, plan.cwd)
			respondTo(conn, msg.ID, ipc.MsgCreateFromTemplateResp, resp)
			return
		}
		first := plan.payloads[0]
		placeholder, err := d.constructPreparingPane(tab.ID, plan.cwd, "terminal", ipc.FirstPaneSpec{Worktree: first.Worktree})
		if err != nil {
			if cleanupErr := d.session.DestroyTab(tab.ID); cleanupErr != nil {
				log.Printf("template tab cleanup: %v", cleanupErr)
			}
			respondTo(conn, msg.ID, ipc.MsgCreateFromTemplateResp, ipc.CreateFromTemplateRespPayload{Error: err.Error()})
			return
		}
		applyTemplatePane(placeholder, plan.tpl.Panes[0])
		d.broadcastState()
		d.requestSnapshot()
		respondTo(conn, msg.ID, ipc.MsgCreateFromTemplateResp, ipc.CreateFromTemplateRespPayload{
			TabID: tab.ID, PaneIDs: []string{placeholder.ID}, PreparingWorktree: req.Branch,
		})
		first.ReplacePaneID = placeholder.ID
		// This goroutine is already a worker. Answer before entering git, and
		// retain the existing swap's own state frame and failure handling.
		resp := d.worktreeAddAndCreate(first)
		if resp.Error != "" {
			if resp.InvalidSubdir {
				placeholder.PluginMu.Lock()
				name := placeholder.Name
				placeholder.PluginMu.Unlock()
				// Git has already been undone by worktreeAddAndCreate. This is
				// a refused directory, not a failed process to leave on screen.
				if err := d.session.DestroyTab(tab.ID); err != nil {
					log.Printf("template %s tab cleanup: %v", plan.tpl.Name, err)
				}
				d.broadcastState()
				d.requestSnapshot()
				// The caller already received its preparing response. Report the
				// asynchronous refusal through the ordinary notification channel.
				d.emitEvent(PaneEvent{PaneID: placeholder.ID, TabID: tab.ID, PaneName: name,
					Type: "template_create_failed", Title: "Template could not be created", Severity: "error",
					Message: "Template " + plan.tpl.Name + ": " + resp.Error})
				return
			}
			if !resp.Swapped {
				d.failPreparingPane(placeholder.ID, "worktree not created: "+resp.Error)
			}
			return
		}
		pane := d.session.Pane(resp.PaneID)
		if pane == nil {
			return // The user closed the pane during preparation.
		}
		pane.PluginMu.Lock()
		root := pane.WorktreePath
		pane.PluginMu.Unlock()
		d.completeTemplateCreation(plan, tab.ID, pane, root)
	}()
}

func applyTemplatePane(pane *Pane, spec config.TemplatePane) {
	applyPaneName(pane, spec.Name)
	pane.PluginMu.Lock()
	pane.Muted = spec.Muted
	pane.PluginMu.Unlock()
}

func (d *Daemon) completeTemplateCreation(plan *templateCreation, tabID string, first *Pane, root string) ipc.CreateFromTemplateRespPayload {
	resp := ipc.CreateFromTemplateRespPayload{TabID: tabID}
	panes := make([]*Pane, len(plan.payloads))
	for i, payload := range plan.payloads {
		var pane *Pane
		if i == 0 {
			pane = first
		}
		if pane == nil {
			cwd := payload.CWD
			if plan.req.Branch != "" {
				var err error
				cwd, err = templatePaneDirectory(root, plan.tpl.Panes[i].CWD)
				if err != nil {
					if resp.Error == "" {
						resp.Error = err.Error()
					}
					// The new checkout can differ from the source directory. Keep
					// a visible failed pane instead of silently spawning at root.
					pane, err = d.session.CreatePane(tabID, filepath.Join(root, plan.tpl.Panes[i].CWD))
					if err == nil {
						pane.PluginMu.Lock()
						pane.Type = payload.Type
						pane.InstanceArgs = payload.InstanceArgs
						pane.QuilMCP = payload.QuilMCP
						pane.SpawnError = "template directory is unusable in the new worktree"
						pane.PluginMu.Unlock()
					}
					if pane == nil {
						continue
					}
				}
			}
			if pane == nil {
				var err error
				pane, err = d.constructPaneAt(payload, cwd, payload.Type)
				if err != nil && resp.Error == "" {
					resp.Error = err.Error()
				}
			}
		}
		if pane == nil {
			continue // A concurrent tab close can make construction fail.
		}
		applyTemplatePane(pane, plan.tpl.Panes[i])
		panes[i] = pane
		resp.PaneIDs = append(resp.PaneIDs, pane.ID)
	}
	d.session.mu.Lock()
	if tab := d.session.tabs[tabID]; tab != nil {
		if main := panes[plan.tpl.MainPane()]; main != nil {
			tab.TemplateMain = main.ID
		}
	}
	d.session.mu.Unlock()
	// One frame for all constructed panes. On the branch path this follows
	// the preparing and swap frames, regardless of the number of panes.
	d.broadcastState()
	d.requestSnapshot()
	// A starting prompt is a statement about the tab that was ASKED for, so
	// none is delivered once a pane is missing or dead. agent-team is the
	// shape that makes this concrete: {{panes}} is built from the panes that
	// EXIST, so a failed developer leaves the orchestrator briefed with a
	// two-line roster where three were requested — and it starts delegating
	// without ever learning that a teammate it was told to use is not there.
	//
	// The panes that did come up are kept rather than rolled back. A visible
	// SpawnError beside working panes is more useful than destroying a
	// checkout that took minutes, and the failure is not silent: the pane
	// carries it, resp.Error carries it to the caller, and the create dialog
	// keeps itself open on it. What is removed is only the briefing of a team
	// that is not the one the template describes.
	if resp.Error != "" {
		return resp
	}
	d.deliverTemplatePrompts(plan, panes, root)
	return resp
}

func (d *Daemon) deliverTemplatePrompts(plan *templateCreation, panes []*Pane, root string) {
	var roster []string
	for _, pane := range panes {
		if pane == nil {
			continue
		}
		pane.PluginMu.Lock()
		name, typ := pane.Name, pane.Type
		pane.PluginMu.Unlock()
		if name == "" {
			name = typ
		}
		roster = append(roster, fmt.Sprintf("%s  %s  %s", name, pane.ID, typ))
	}
	vars := map[string]string{"task": plan.req.Task, "dir": root, "branch": plan.req.Branch, "panes": strings.Join(roster, "\n")}
	for i, pane := range panes {
		if pane == nil || plan.tpl.Panes[i].Prompt == "" {
			continue
		}
		prompt := config.RenderPrompt(plan.tpl.Panes[i].Prompt, vars)
		pane.PluginMu.Lock()
		live, name := pane.PTY != nil && pane.ExitCode == nil && !pane.inputStopped, pane.Name
		pane.PluginMu.Unlock()
		// Never hold sm.mu or PluginMu across prompt delivery. Delivery means
		// queued, as with delegate_task; children consume their queues separately.
		if !live || !d.deliverPrompt(pane, prompt, plan.agents[i]) {
			log.Printf("template %s pane %s (%s): starting prompt could not be queued", plan.tpl.Name, name, pane.ID)
			d.emitEvent(PaneEvent{PaneID: pane.ID, TabID: pane.CurrentTabID(), PaneName: name,
				Type: "template_prompt_failed", Title: "Starting prompt was not delivered", Severity: "error",
				Message: "Template " + plan.tpl.Name + ": no process or input queue full"})
		}
	}
}
