package main

import (
	"context"
	"fmt"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Project, tab, host and plugin-catalog tools.

func registerProjectTools(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	registerListHostsTool(s, r)
	registerListProjectsTool(s, r)
	registerCreateProjectTool(s, r, mcpLog)
	registerUpdateProjectTool(s, r, mcpLog)
	registerDestroyProjectTool(s, r, mcpLog)
	registerSwitchProjectTool(s, r, mcpLog)
	registerCreateTabTool(s, r, mcpLog)
	registerCreateFromTemplateTool(s, r, mcpLog)
	registerRenameTabTool(s, r, mcpLog)
	registerDestroyTabTool(s, r, mcpLog)
	registerRenamePaneTool(s, r, mcpLog)
	registerListPluginsTool(s, r)
	registerListSessionsTool(s, r)
}

// opResult turns an OpResp into a tool result or a tool error.
func opResult(tool string, resp *ipc.Message, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", tool, err)
	}
	var op ipc.OpRespPayload
	if err := resp.DecodePayload(&op); err != nil {
		return nil, nil, fmt.Errorf("%s decode: %w", tool, err)
	}
	if !op.OK {
		if op.Error == "" {
			op.Error = "not applied"
		}
		return nil, nil, fmt.Errorf("%s: %s", tool, op.Error)
	}
	return jsonResult(op), nil, nil
}

func registerListHostsTool(s *mcp.Server, r *mcpRouter) {
	type Input struct{}
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_hosts",
		Description: "List the remote daemons this bridge can reach — the [[destinations]] of the machine running quil mcp — with their " +
			"connection state. The local daemon is always available and is addressed with host empty or \"local\". " +
			"Every other tool takes an optional host; ids discovered through list_panes / list_tabs / list_projects route to their host automatically.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ Input) (*mcp.CallToolResult, any, error) {
		out := struct {
			Local bool         `json:"local"`
			Hosts []hostStatus `json:"hosts"`
		}{true, r.statuses()}
		if out.Hosts == nil {
			out.Hosts = []hostStatus{}
		}
		return jsonResult(out), nil, nil
	})
}

type hostedProject struct {
	ipc.ProjectInfo
	Host string `json:"host,omitempty"`
}

func registerListProjectsTool(s *mcp.Server, r *mcpRouter) {
	type Input struct {
		Host string `json:"host,omitempty" jsonschema:"limit to one host (default: every connected host)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_projects",
		Description: "List projects (the grouping above tabs) on every connected host: id, name, root directory, whether it is the active project, and its tab ids.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		var out []hostedProject
		err := r.forEachHost(input.Host, func(hb hostBridge) error {
			if err := hb.bridge.requireDaemon("list_projects"); err != nil {
				return err
			}
			resp, err := hb.bridge.request(ipc.MsgListProjectsReq, nil)
			if err != nil {
				return fmt.Errorf("list_projects%s: %w", hostSuffix(hb.host), err)
			}
			var payload ipc.ListProjectsRespPayload
			if err := resp.DecodePayload(&payload); err != nil {
				return fmt.Errorf("list_projects decode: %w", err)
			}
			for _, p := range payload.Projects {
				r.remember(hb.host, p.ID)
				r.remember(hb.host, p.TabIDs...)
				out = append(out, hostedProject{ProjectInfo: p, Host: hb.host})
			}
			return nil
		})
		if err != nil {
			return nil, nil, fmt.Errorf("list_projects: %w", err)
		}
		if out == nil {
			out = []hostedProject{}
		}
		return jsonResult(out), nil, nil
	})
}

func registerCreateProjectTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		Name    string `json:"name" jsonschema:"project name (made unique on the daemon if taken)"`
		RootDir string `json:"root_dir,omitempty" jsonschema:"root directory on the daemon's filesystem; new tabs open here (default: daemon's working directory)"`
		Host    string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (default: local)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_project",
		Description: "Create a project with a root directory. The project opens with one shell tab. Returns the project id.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, host, err := r.bridgeFor(input.Host)
		if err != nil {
			return nil, nil, fmt.Errorf("create_project: %w", err)
		}
		if err := bridge.requireDaemon("create_project"); err != nil {
			return nil, nil, fmt.Errorf("create_project: %w", err)
		}
		resp, err := bridge.request(ipc.MsgCreateProjectReq, ipc.CreateProjectReqPayload{Name: input.Name, RootDir: input.RootDir})
		if err != nil {
			return nil, nil, fmt.Errorf("create_project: %w", err)
		}
		var payload ipc.CreateProjectRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("create_project decode: %w", err)
		}
		if payload.Error != "" {
			return nil, nil, fmt.Errorf("create_project: %s", payload.Error)
		}
		r.remember(host, payload.ProjectID)
		mcpLog.Log("", "create_project", fmt.Sprintf("id=%s name=%q", payload.ProjectID, payload.Name))
		return jsonResult(struct {
			ProjectID string `json:"project_id"`
			Name      string `json:"name"`
			Host      string `json:"host,omitempty"`
		}{payload.ProjectID, payload.Name, host}), nil, nil
	})
}

func registerUpdateProjectTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		ProjectID string `json:"project_id" jsonschema:"project to update"`
		Name      string `json:"name" jsonschema:"new name"`
		RootDir   string `json:"root_dir,omitempty" jsonschema:"new root directory (default: unchanged)"`
		Host      string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the id was discovered on, else local)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "update_project",
		Description: "Rename a project and/or change its root directory.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, _, err := r.bridgeFor(input.Host, input.ProjectID)
		if err != nil {
			return nil, nil, fmt.Errorf("update_project: %w", err)
		}
		if err := bridge.requireDaemon("update_project"); err != nil {
			return nil, nil, fmt.Errorf("update_project: %w", err)
		}
		rootDir := input.RootDir
		if rootDir == "" {
			// The daemon's UpdateProject takes the root as given; fetch the
			// current one so "rename only" does not blank it.
			rootDir = currentProjectRoot(bridge, input.ProjectID)
		}
		mcpLog.Log("", "update_project", fmt.Sprintf("id=%s name=%q", input.ProjectID, input.Name))
		resp, err := bridge.request(ipc.MsgUpdateProject, ipc.UpdateProjectPayload{ProjectID: input.ProjectID, Name: input.Name, RootDir: rootDir})
		return opResult("update_project", resp, err)
	})
}

func currentProjectRoot(bridge *mcpBridge, projectID string) string {
	resp, err := bridge.request(ipc.MsgListProjectsReq, nil)
	if err != nil {
		return ""
	}
	var payload ipc.ListProjectsRespPayload
	if err := resp.DecodePayload(&payload); err != nil {
		return ""
	}
	for _, p := range payload.Projects {
		if p.ID == projectID {
			return p.RootDir
		}
	}
	return ""
}

func registerDestroyProjectTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		ProjectID string `json:"project_id" jsonschema:"project to destroy"`
		Host      string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the id was discovered on, else local)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "destroy_project",
		Description: "Destroy a project WITH every tab and pane under it. Destructive: confirm with the user before calling.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, _, err := r.bridgeFor(input.Host, input.ProjectID)
		if err != nil {
			return nil, nil, fmt.Errorf("destroy_project: %w", err)
		}
		if err := bridge.requireDaemon("destroy_project"); err != nil {
			return nil, nil, fmt.Errorf("destroy_project: %w", err)
		}
		mcpLog.Log("", "destroy_project", "id="+input.ProjectID)
		resp, err := bridge.request(ipc.MsgDestroyProject, ipc.DestroyProjectPayload{ProjectID: input.ProjectID})
		return opResult("destroy_project", resp, err)
	})
}

func registerSwitchProjectTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		ProjectID string `json:"project_id" jsonschema:"project to make active"`
		Host      string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the id was discovered on, else local)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "switch_project",
		Description: "Make a project the active one in the TUI (its last active tab comes into view).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, _, err := r.bridgeFor(input.Host, input.ProjectID)
		if err != nil {
			return nil, nil, fmt.Errorf("switch_project: %w", err)
		}
		if err := bridge.requireDaemon("switch_project"); err != nil {
			return nil, nil, fmt.Errorf("switch_project: %w", err)
		}
		resp, err := bridge.request(ipc.MsgSwitchProject, ipc.SwitchProjectPayload{ProjectID: input.ProjectID})
		return opResult("switch_project", resp, err)
	})
}

func registerCreateTabTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		Name      string           `json:"name,omitempty" jsonschema:"tab name (default: New Tab)"`
		ProjectID string           `json:"project_id,omitempty" jsonschema:"project to file the tab under (default: the active project)"`
		FirstPane *createPaneInput `json:"first_pane,omitempty" jsonschema:"the pane the tab opens with; same options as create_pane (default: a terminal in the project root)"`
		Host      string           `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the project id was discovered on, else local)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "create_tab",
		Description: "Create a tab in a project with a chosen first pane (any create_pane option). Returns tab_id and pane_id. " +
			"Does not switch the TUI's focus; call switch_tab for that. With a worktree_branch the returned pane is a placeholder " +
			"(preparing_worktree set) that the real pane replaces — watch for the worktree_ready event, which names the new pane.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, host, err := r.bridgeFor(input.Host, input.ProjectID)
		if err != nil {
			return nil, nil, fmt.Errorf("create_tab: %w", err)
		}
		if err := bridge.requireDaemon("create_tab"); err != nil {
			return nil, nil, fmt.Errorf("create_tab: %w", err)
		}
		req := ipc.CreateTabReqPayload{Name: input.Name, ProjectID: input.ProjectID}
		if input.FirstPane != nil {
			fp := input.FirstPane.toReq("")
			req.FirstPane = &fp
		}
		resp, err := bridge.request(ipc.MsgCreateTabReq, req)
		if err != nil {
			return nil, nil, fmt.Errorf("create_tab: %w", err)
		}
		var payload ipc.CreateTabRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("create_tab decode: %w", err)
		}
		if payload.TabID == "" {
			return nil, nil, fmt.Errorf("create_tab: %s", payload.Error)
		}
		r.remember(host, payload.TabID, payload.PaneID)
		mcpLog.Log(payload.PaneID, "create_tab", fmt.Sprintf("tab=%s project=%s", payload.TabID, input.ProjectID))
		return jsonResult(struct {
			ipc.CreateTabRespPayload
			Host string `json:"host,omitempty"`
		}{payload, host}), nil, nil
	})
}

func registerCreateFromTemplateTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		Template  string `json:"template" jsonschema:"name in templates.toml, such as pair, agent-team, or review"`
		Task      string `json:"task,omitempty" jsonschema:"optional task text for the tab name and starting prompts"`
		CWD       string `json:"cwd,omitempty" jsonschema:"existing working directory on the daemon; default is the project's root; unusable paths are refused"`
		Branch    string `json:"branch,omitempty" jsonschema:"optional new branch to create in a linked worktree"`
		ProjectID string `json:"project_id,omitempty" jsonschema:"project to file the tab under; defaults to the active project"`
		Host      string `json:"host,omitempty" jsonschema:"daemon host from list_hosts; default is the project id's host, otherwise local"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "create_from_template",
		Description: "Create a tab from a named workspace template, with its ordered panes, frozen arguments and starting prompts. " +
			"Returns tab_id and pane_ids without switching focus. With branch, returns immediately with a preparing_worktree " +
			"placeholder; the completed pane IDs arrive in workspace state (use list_panes). Invalid requests create nothing.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, host, err := r.bridgeFor(input.Host, input.ProjectID)
		if err != nil {
			return nil, nil, fmt.Errorf("create_from_template: %w", err)
		}
		if err := bridge.requireDaemonAtLeast("create_from_template", createFromTemplateMinVersion); err != nil {
			return nil, nil, err
		}
		resp, err := bridge.request(ipc.MsgCreateFromTemplateReq, ipc.CreateFromTemplateReqPayload{
			Template: input.Template, Task: input.Task, CWD: input.CWD, Branch: input.Branch, ProjectID: input.ProjectID,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("create_from_template: %w", err)
		}
		var payload ipc.CreateFromTemplateRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("create_from_template decode: %w", err)
		}
		if payload.TabID == "" {
			return nil, nil, fmt.Errorf("create_from_template: %s", payload.Error)
		}
		r.remember(host, payload.TabID)
		r.remember(host, payload.PaneIDs...)
		mcpLog.Log("", "create_from_template", fmt.Sprintf("tab=%s template=%s project=%s", payload.TabID, input.Template, input.ProjectID))
		return jsonResult(struct {
			ipc.CreateFromTemplateRespPayload
			Host string `json:"host,omitempty"`
		}{payload, host}), nil, nil
	})
}

func registerRenameTabTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		TabID string `json:"tab_id" jsonschema:"tab to rename"`
		Name  string `json:"name" jsonschema:"new name"`
		Host  string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the id was discovered on, else local)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "rename_tab",
		Description: "Rename a tab.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		if input.Name == "" {
			return nil, nil, fmt.Errorf("rename_tab: name is required")
		}
		bridge, _, err := r.bridgeFor(input.Host, input.TabID)
		if err != nil {
			return nil, nil, fmt.Errorf("rename_tab: %w", err)
		}
		if err := bridge.requireDaemon("rename_tab"); err != nil {
			return nil, nil, fmt.Errorf("rename_tab: %w", err)
		}
		resp, err := bridge.request(ipc.MsgUpdateTab, ipc.UpdateTabPayload{TabID: input.TabID, Name: input.Name})
		return opResult("rename_tab", resp, err)
	})
}

func registerDestroyTabTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		TabID string `json:"tab_id" jsonschema:"tab to destroy"`
		Host  string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the id was discovered on, else local)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "destroy_tab",
		Description: "Destroy a tab and every pane in it. If it was the project's last tab, a shell tab is auto-created. Destructive: confirm with the user before calling.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, _, err := r.bridgeFor(input.Host, input.TabID)
		if err != nil {
			return nil, nil, fmt.Errorf("destroy_tab: %w", err)
		}
		if err := bridge.requireDaemon("destroy_tab"); err != nil {
			return nil, nil, fmt.Errorf("destroy_tab: %w", err)
		}
		mcpLog.Log("", "destroy_tab", "tab="+input.TabID)
		resp, err := bridge.request(ipc.MsgDestroyTab, ipc.DestroyTabPayload{TabID: input.TabID})
		return opResult("destroy_tab", resp, err)
	})
}

func registerRenamePaneTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		PaneID string `json:"pane_id" jsonschema:"pane to rename"`
		Name   string `json:"name" jsonschema:"new label"`
		Host   string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the id was discovered on, else local)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "rename_pane",
		Description: "Set a pane's label (what Alt+F2 does in the TUI).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		if input.Name == "" {
			return nil, nil, fmt.Errorf("rename_pane: name is required")
		}
		bridge, _, err := r.bridgeFor(input.Host, input.PaneID)
		if err != nil {
			return nil, nil, fmt.Errorf("rename_pane: %w", err)
		}
		if err := bridge.requireDaemon("rename_pane"); err != nil {
			return nil, nil, fmt.Errorf("rename_pane: %w", err)
		}
		mcpLog.Log(input.PaneID, "rename_pane", fmt.Sprintf("name=%q", input.Name))
		resp, err := bridge.request(ipc.MsgUpdatePane, ipc.UpdatePanePayload{PaneID: input.PaneID, Name: input.Name})
		return opResult("rename_pane", resp, err)
	})
}

func registerListPluginsTool(s *mcp.Server, r *mcpRouter) {
	type Input struct {
		Host string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (default: local)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_plugins",
		Description: "List the pane plugins a host knows and the options create_pane accepts for each: whether the binary is available, " +
			"whether it takes a working directory, whether Claude sessions can be resumed, and its toggles by name (toggles sharing a group are mutually exclusive). " +
			"Also reports whether Docker sandbox panes are possible on that host. Call this before create_pane with an AI type.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, host, err := r.bridgeFor(input.Host)
		if err != nil {
			return nil, nil, fmt.Errorf("list_plugins: %w", err)
		}
		if err := bridge.requireDaemon("list_plugins"); err != nil {
			return nil, nil, fmt.Errorf("list_plugins: %w", err)
		}
		resp, err := bridge.request(ipc.MsgPluginCatalogReq, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("list_plugins: %w", err)
		}
		var payload ipc.PluginCatalogRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("list_plugins decode: %w", err)
		}
		return jsonResult(struct {
			ipc.PluginCatalogRespPayload
			Host string `json:"host,omitempty"`
		}{payload, host}), nil, nil
	})
}

func registerListSessionsTool(s *mcp.Server, r *mcpRouter) {
	type Input struct {
		CWD  string `json:"cwd" jsonschema:"directory whose Claude Code sessions to list (on the daemon's filesystem)"`
		Host string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (default: local)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_sessions",
		Description: "List the Claude Code sessions recorded for a directory — id, title, last modified, and which pane (if any) already holds it — " +
			"for create_pane's resume_session_id. A session a live pane holds cannot be resumed twice.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, host, err := r.bridgeFor(input.Host)
		if err != nil {
			return nil, nil, fmt.Errorf("list_sessions: %w", err)
		}
		resp, err := bridge.request(ipc.MsgClaudeSessionsReq, ipc.ClaudeSessionsReqPayload{CWD: input.CWD})
		if err != nil {
			return nil, nil, fmt.Errorf("list_sessions: %w", err)
		}
		var payload ipc.ClaudeSessionsRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("list_sessions decode: %w", err)
		}
		if payload.Error != "" {
			return nil, nil, fmt.Errorf("list_sessions: %s", payload.Error)
		}
		if payload.Sessions == nil {
			payload.Sessions = []ipc.ClaudeSessionInfo{}
		}
		return jsonResult(struct {
			ipc.ClaudeSessionsRespPayload
			Host string `json:"host,omitempty"`
		}{payload, host}), nil, nil
	})
}
