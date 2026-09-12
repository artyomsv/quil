package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/memreport"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// hostDoc is the shared description of the `host` input every addressed tool
// carries. Kept in one place so the tools agree on what it means.
const hostDoc = "daemon host from list_hosts (empty = the host the id was discovered on, else local)"

func registerMCPTools(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	// Phase A (M10 core)
	registerListPanesTool(s, r, mcpLog)
	registerReadPaneOutputTool(s, r, mcpLog)
	registerSendToPaneTool(s, r, mcpLog)
	registerGetPaneStatusTool(s, r, mcpLog)
	registerCreatePaneTool(s, r, mcpLog)
	// Phase B (expansion)
	registerSendKeysTool(s, r, mcpLog)
	registerRestartPaneTool(s, r, mcpLog)
	registerScreenshotPaneTool(s, r, mcpLog)
	registerSwitchTabTool(s, r, mcpLog)
	registerListTabsTool(s, r, mcpLog)
	registerDestroyPaneTool(s, r, mcpLog)
	registerSetActivePaneTool(s, r, mcpLog)
	registerCloseTUITool(s, r, mcpLog)
	// Notification tools
	registerGetNotificationsTool(s, r, mcpLog)
	registerWatchNotificationsTool(s, r, mcpLog)
	registerDismissNotificationsTool(s, r, mcpLog)
	// Memory reporting
	registerGetMemoryReportTool(s, r, mcpLog)
	registerGetPaneMemoryTool(s, r, mcpLog)
	// Projects, tabs, hosts, plugin catalog
	registerProjectTools(s, r, mcpLog)
	// Agent tasking
	registerTaskTools(s, r, mcpLog)
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func jsonResult(v any) *mcp.CallToolResult {
	text, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return textResult(fmt.Sprintf("(unencodable result: %v)", err))
	}
	return textResult(string(text))
}

// hostedPane is PaneInfo plus where it lives and whether it is the caller.
type hostedPane struct {
	ipc.PaneInfo
	Host string `json:"host,omitempty"`
	// Self marks the pane this bridge runs inside — the caller's own pane —
	// so an orchestrator can tell itself apart from its workers.
	Self bool `json:"self,omitempty"`
}

func registerListPanesTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		Host string `json:"host,omitempty" jsonschema:"limit to one host (default: every connected host)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_panes",
		Description: "List all panes across all tabs (and every connected host) with their IDs, types, names, working directories, " +
			"project, running status and agent_state (working / blocked / idle; empty = unknown, not idle). " +
			"The pane this bridge runs inside is marked self. Use this to discover pane IDs for other tools.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		var out []hostedPane
		err := r.forEachHost(input.Host, func(hb hostBridge) error {
			resp, err := hb.bridge.request(ipc.MsgListPanesReq, nil)
			if err != nil {
				return fmt.Errorf("list_panes%s: %w", hostSuffix(hb.host), err)
			}
			var payload ipc.ListPanesRespPayload
			if err := resp.DecodePayload(&payload); err != nil {
				return fmt.Errorf("list_panes decode: %w", err)
			}
			for _, p := range payload.Panes {
				r.remember(hb.host, p.ID, p.TabID, p.ProjectID)
				out = append(out, hostedPane{PaneInfo: p, Host: hb.host, Self: hb.host == "" && p.ID == r.selfPane})
			}
			return nil
		})
		if err != nil {
			return nil, nil, fmt.Errorf("list_panes: %w", err)
		}
		if out == nil {
			out = []hostedPane{}
		}
		return jsonResult(out), nil, nil
	})
}

func hostSuffix(host string) string {
	if host == "" {
		return ""
	}
	return " on " + host
}

func registerReadPaneOutputTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		PaneID    string `json:"pane_id" jsonschema:"pane ID (use list_panes to discover IDs)"`
		LastLines int    `json:"last_lines,omitempty" jsonschema:"number of lines to return (default 50, max 1000)"`
		Host      string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the id was discovered on, else local)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "read_pane_output",
		Description: "Read recent terminal output from a pane. Returns ANSI-stripped plain text. Use this to check build output, test results, logs, or any terminal content.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		lastLines := input.LastLines
		if lastLines <= 0 {
			lastLines = 50
		}
		if lastLines > 1000 {
			lastLines = 1000
		}
		bridge, _, err := r.bridgeFor(input.Host, input.PaneID)
		if err != nil {
			return nil, nil, fmt.Errorf("read_pane_output: %w", err)
		}
		resp, err := bridge.request(ipc.MsgReadPaneOutputReq, ipc.ReadPaneOutputReqPayload{
			PaneID:    input.PaneID,
			LastLines: lastLines,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("read_pane_output: %w", err)
		}
		var payload ipc.ReadPaneOutputRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("read_pane_output decode: %w", err)
		}
		mcpLog.Log(input.PaneID, "read_pane_output", fmt.Sprintf("lines=%d", payload.Lines))
		return textResult(payload.Text), nil, nil
	})
}

// wrapPaste wraps text in bracketed-paste markers so an interactive program
// (Claude Code, codex, a shell with bracketed paste on) takes embedded
// newlines as part of the text rather than as submits.
func wrapPaste(text string) string {
	return "\x1b[200~" + text + "\x1b[201~"
}

func registerSendToPaneTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		PaneID     string `json:"pane_id" jsonschema:"target pane ID"`
		InputText  string `json:"input" jsonschema:"text to send to the pane"`
		PressEnter *bool  `json:"press_enter,omitempty" jsonschema:"append newline after input (default true)"`
		Paste      bool   `json:"paste,omitempty" jsonschema:"deliver as a bracketed paste, then Enter after a short pause — use for multi-line prompts to an AI pane so newlines do not submit early"`
		Host       string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the id was discovered on, else local)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name: "send_to_pane",
		Description: "Send keystrokes or commands to a pane's terminal. By default appends a newline (press_enter=true) to execute the command. " +
			"For a prompt to another AI pane prefer delegate_task, which also reports when the work is done. " +
			"SECURITY: This executes arbitrary input in the target pane's shell. The MCP bridge has the same access as the terminal's owner.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, _, err := r.bridgeFor(input.Host, input.PaneID)
		if err != nil {
			return nil, nil, fmt.Errorf("send_to_pane: %w", err)
		}
		redactCount := countRedactMarkers(input.InputText)
		data := stripRedactMarkers(input.InputText)
		enter := input.PressEnter == nil || *input.PressEnter
		detail := fmt.Sprintf("bytes=%d", len(data))
		if redactCount > 0 {
			detail += fmt.Sprintf(" [%d redacted]", redactCount)
		}
		if input.Paste {
			detail += " paste"
		}
		mcpLog.Log(input.PaneID, "send_to_pane", detail)
		// A REQUEST, not a fire-and-forget send: the daemon drops input aimed at
		// a pane with no process (a worktree placeholder, or one whose spawn
		// failed) and this tool used to answer "Sent N bytes" anyway — an agent
		// then waits forever for output from a command that was never run.
		if input.Paste {
			if err := sendPaneInput(bridge, input.PaneID, []byte(wrapPaste(data))); err != nil {
				return nil, nil, fmt.Errorf("send_to_pane: %w", err)
			}
			if enter {
				time.Sleep(pasteEnterDelay)
				if err := sendPaneInput(bridge, input.PaneID, []byte("\r")); err != nil {
					return nil, nil, fmt.Errorf("send_to_pane: %w", err)
				}
			}
			return textResult(fmt.Sprintf("Pasted %d bytes to %s", len(data), input.PaneID)), nil, nil
		}
		if enter {
			// CR, the byte Enter produces, not LF. LF executed in a Unix
			// shell only because the tty maps it too; in PowerShell under
			// ConPTY it is echoed and sits at the prompt, so every
			// send_to_pane on Windows typed the command and never ran it
			// (measured 2026-09-10). send_keys "enter" has always sent CR.
			data += "\r"
		}
		if err := sendPaneInput(bridge, input.PaneID, []byte(data)); err != nil {
			return nil, nil, fmt.Errorf("send_to_pane: %w", err)
		}
		return textResult(fmt.Sprintf("Sent %d bytes to %s", len(data), input.PaneID)), nil, nil
	})
}

// pasteEnterDelay is the pause between a bracketed paste and the Enter that
// submits it. The daemon's delegate_task uses the same gap.
const pasteEnterDelay = 100 * time.Millisecond

// sendPaneInput delivers bytes to a pane and FAILS when the daemon could not
// hand them to a process.
//
// The daemon drops input aimed at a pane with no PTY — a worktree placeholder,
// or a pane whose spawn failed — and both MCP input tools used to report success
// regardless, because the send was fire-and-forget. An agent then waits for
// output from a command that was never run, which is indistinguishable from a
// slow command and resolves only by timing out.
//
// One round trip per call is affordable here and nowhere else: these are agent
// tools issuing a handful of sends, not the TUI's per-keystroke path, which
// deliberately still sets no request ID and still gets no answer.
func sendPaneInput(bridge *mcpBridge, paneID string, data []byte) error {
	resp, err := bridge.request(ipc.MsgPaneInput, ipc.PaneInputPayload{PaneID: paneID, Data: data})
	if err != nil {
		return err
	}
	var payload ipc.PaneInputRespPayload
	if err := resp.DecodePayload(&payload); err != nil {
		return fmt.Errorf("decode pane_input_resp: %w", err)
	}
	if !payload.Delivered {
		if payload.Error != "" {
			return fmt.Errorf("%s: %s", paneID, payload.Error)
		}
		return fmt.Errorf("%s: input was not delivered", paneID)
	}
	return nil
}

func registerGetPaneStatusTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		PaneID string `json:"pane_id" jsonschema:"pane ID to check"`
		Host   string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the id was discovered on, else local)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_pane_status",
		Description: "Get the status of a pane's process — whether it's running or exited, exit code, type, working directory, project, " +
			"and agent_state (working / blocked / idle; empty = unknown) with blocked_reason and last_idle_at for AI panes.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, _, err := r.bridgeFor(input.Host, input.PaneID)
		if err != nil {
			return nil, nil, fmt.Errorf("get_pane_status: %w", err)
		}
		resp, err := bridge.request(ipc.MsgPaneStatusReq, ipc.PaneStatusReqPayload{PaneID: input.PaneID})
		if err != nil {
			return nil, nil, fmt.Errorf("get_pane_status: %w", err)
		}
		var payload ipc.PaneStatusRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("get_pane_status decode: %w", err)
		}
		return jsonResult(payload), nil, nil
	})
}

// createPaneInput is shared by create_pane and create_tab's first_pane.
type createPaneInput struct {
	CWD             string   `json:"cwd,omitempty" jsonschema:"working directory for the new pane (on the daemon's filesystem)"`
	Type            string   `json:"type,omitempty" jsonschema:"plugin type from list_plugins: terminal (default), claude-code, opencode, codex, ssh, stripe, ..."`
	Name            string   `json:"name,omitempty" jsonschema:"pane label"`
	Toggles         []string `json:"toggles,omitempty" jsonschema:"plugin toggle names from list_plugins, e.g. dangerously_skip_permissions, enable_auto_mode, chrome, search"`
	ResumeSessionID string   `json:"resume_session_id,omitempty" jsonschema:"Claude session id from list_sessions to resume instead of starting fresh"`
	WorktreeBranch  string   `json:"worktree_branch,omitempty" jsonschema:"create a NEW git worktree on this branch (off the repo containing cwd) and open the pane inside it"`
	SandboxImage    string   `json:"sandbox_image,omitempty" jsonschema:"run the pane inside a Docker container from this image (requires sandbox_available from list_plugins)"`
	SandboxAuth     string   `json:"sandbox_auth,omitempty" jsonschema:"sandbox sign-in mode for claude-code: token or browser (empty = config default)"`
	InstanceName    string   `json:"instance_name,omitempty" jsonschema:"saved instance name for plugins with instances (ssh, stripe)"`
	InstanceArgs    []string `json:"instance_args,omitempty" jsonschema:"instance arguments for plugins with instances (ssh, stripe); they REPLACE the plugin's own args and are REFUSED for AI panes — use toggles there"`
}

// usesDialogOptions reports whether the request carries any field a daemon
// older than mcpDaemonMinVersion would silently drop.
func (in createPaneInput) usesDialogOptions() bool {
	return in.Name != "" || len(in.Toggles) > 0 || in.ResumeSessionID != "" ||
		in.WorktreeBranch != "" || in.SandboxImage != "" || in.SandboxAuth != ""
}

func (in createPaneInput) toReq(tabID string) ipc.CreatePaneReqPayload {
	req := ipc.CreatePaneReqPayload{
		TabID:           tabID,
		CWD:             in.CWD,
		Type:            in.Type,
		InstanceName:    in.InstanceName,
		InstanceArgs:    in.InstanceArgs,
		Name:            in.Name,
		Toggles:         in.Toggles,
		ResumeSessionID: in.ResumeSessionID,
		WorktreeBranch:  in.WorktreeBranch,
	}
	if in.SandboxImage != "" {
		req.Sandbox = &ipc.SandboxSpec{Image: in.SandboxImage, Auth: in.SandboxAuth}
	}
	return req
}

// createTimeout is how long the bridge waits for a create. A worktree create
// checks out a tree first, which the daemon bounds at worktreeAddTimeout
// (120 s); the ordinary create answers at once.
func createTimeout(worktree bool) time.Duration {
	if worktree {
		return 130 * time.Second
	}
	return mcpRequestTimeout
}

func registerCreatePaneTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		TabID string `json:"tab_id,omitempty" jsonschema:"tab to create pane in (default: active tab)"`
		createPaneInput
		Host string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the tab id was discovered on, else local)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name: "create_pane",
		Description: "Create a new pane in a tab with the same options the create-pane dialog offers: plugin type, working directory, " +
			"name, plugin toggles (call list_plugins for the names), a Claude session to resume (list_sessions), a new git worktree, " +
			"and a Docker sandbox. Returns the new pane ID, or an error naming the refusal. Defaults to a terminal pane in the active tab.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, host, err := r.bridgeFor(input.Host, input.TabID)
		if err != nil {
			return nil, nil, fmt.Errorf("create_pane: %w", err)
		}
		// The bare create (tab, cwd, type) is the request every daemon has
		// answered since M10; the dialog options are not, and an older daemon
		// IGNORES unknown fields — it would start the pane without the
		// permission mode or worktree that was asked for, silently.
		if input.usesDialogOptions() {
			if err := bridge.requireDaemon("create_pane with name/toggles/resume/worktree/sandbox"); err != nil {
				return nil, nil, fmt.Errorf("create_pane: %w", err)
			}
		}
		resp, err := bridge.requestWithTimeout(ipc.MsgCreatePaneReq, input.toReq(input.TabID), createTimeout(input.WorktreeBranch != ""))
		if err != nil {
			return nil, nil, fmt.Errorf("create_pane: %w", err)
		}
		var payload ipc.CreatePaneRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("create_pane decode: %w", err)
		}
		if payload.PaneID == "" {
			if payload.Error == "" {
				payload.Error = "daemon created no pane"
			}
			return nil, nil, fmt.Errorf("create_pane: %s", payload.Error)
		}
		r.remember(host, payload.PaneID, payload.TabID)
		mcpLog.Log(payload.PaneID, "create_pane", fmt.Sprintf("type=%s toggles=%v worktree=%q sandbox=%v", input.Type, input.Toggles, input.WorktreeBranch, input.SandboxImage != ""))
		out := struct {
			PaneID string `json:"pane_id"`
			TabID  string `json:"tab_id"`
			Host   string `json:"host,omitempty"`
			// Error here means the pane EXISTS but has no process (its spawn
			// failed); the reason is on the pane too, for restart_pane.
			Error string `json:"error,omitempty"`
		}{payload.PaneID, payload.TabID, host, payload.Error}
		return jsonResult(out), nil, nil
	})
}

// Phase B tools

func registerSendKeysTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		PaneID string   `json:"pane_id" jsonschema:"target pane ID"`
		Keys   []string `json:"keys" jsonschema:"key names or text: enter, tab, escape, up, down, left, right, ctrl+c, f1-f12, or literal text"`
		Host   string   `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the id was discovered on, else local)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name: "send_keys",
		Description: "Send named key sequences to a pane. Each element can be a key name (enter, tab, escape, up, down, left, right, " +
			"home, end, page_up, page_down, f1-f12, ctrl+a through ctrl+z, backspace, delete, space) or literal text to type.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		if len(input.Keys) > maxSendKeys {
			return nil, nil, fmt.Errorf("send_keys: too many keys (%d, max %d)", len(input.Keys), maxSendKeys)
		}
		bridge, _, err := r.bridgeFor(input.Host, input.PaneID)
		if err != nil {
			return nil, nil, fmt.Errorf("send_keys: %w", err)
		}
		// Send keys individually with delays between escape sequences.
		// Batching escape sequences (e.g., down+down+enter) causes TUI apps
		// to only process the first one before enter arrives.
		var textBuf strings.Builder
		sendBuf := func() error {
			if textBuf.Len() == 0 {
				return nil
			}
			data := textBuf.String()
			textBuf.Reset()
			return sendPaneInput(bridge, input.PaneID, []byte(data))
		}

		for _, key := range input.Keys {
			seq, isEscape := keyMap[strings.ToLower(key)]
			if isEscape {
				if err := sendBuf(); err != nil {
					return nil, nil, fmt.Errorf("send_keys: %w", err)
				}
				// Confirmed like the text above it: a key sequence dropped into
				// a pane with no process is the same silent failure, and this
				// tool is the one used to drive interactive menus, where a
				// dropped keypress reads as the menu ignoring you.
				if err := sendPaneInput(bridge, input.PaneID, []byte(seq)); err != nil {
					return nil, nil, fmt.Errorf("send_keys: %w", err)
				}
				time.Sleep(50 * time.Millisecond)
			} else {
				textBuf.WriteString(key) // batch plain text
			}
		}
		if err := sendBuf(); err != nil {
			return nil, nil, fmt.Errorf("send_keys: %w", err)
		}

		mcpLog.Log(input.PaneID, "send_keys", fmt.Sprintf("count=%d", len(input.Keys)))
		return textResult(fmt.Sprintf("Sent %d keys to %s", len(input.Keys), input.PaneID)), nil, nil
	})
}

func registerRestartPaneTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		PaneID string `json:"pane_id" jsonschema:"pane to restart"`
		Host   string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the id was discovered on, else local)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "restart_pane",
		Description: "Kill a pane's process and respawn it with the same plugin type, working directory, and instance config. Useful for fixing stuck or crashed panes.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, _, err := r.bridgeFor(input.Host, input.PaneID)
		if err != nil {
			return nil, nil, fmt.Errorf("restart_pane: %w", err)
		}
		resp, err := bridge.request(ipc.MsgRestartPaneReq, ipc.RestartPaneReqPayload{PaneID: input.PaneID})
		if err != nil {
			return nil, nil, fmt.Errorf("restart_pane: %w", err)
		}
		var payload ipc.RestartPaneRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("restart_pane decode: %w", err)
		}
		mcpLog.Log(input.PaneID, "restart_pane", "")
		return jsonResult(payload), nil, nil
	})
}

func registerScreenshotPaneTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		PaneID string `json:"pane_id" jsonschema:"pane to screenshot"`
		Width  int    `json:"width,omitempty" jsonschema:"terminal width in columns (default 80)"`
		Height int    `json:"height,omitempty" jsonschema:"terminal height in rows (default 24)"`
		Host   string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the id was discovered on, else local)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name: "screenshot_pane",
		Description: "Take a VT-emulated text screenshot of a pane. Unlike read_pane_output which returns raw output lines, " +
			"this shows the actual terminal screen state — what the user sees right now. Essential for reading interactive TUI apps (vim, htop, etc.).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, _, err := r.bridgeFor(input.Host, input.PaneID)
		if err != nil {
			return nil, nil, fmt.Errorf("screenshot_pane: %w", err)
		}
		resp, err := bridge.request(ipc.MsgScreenshotPaneReq, ipc.ScreenshotPaneReqPayload{
			PaneID: input.PaneID,
			Width:  input.Width,
			Height: input.Height,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("screenshot_pane: %w", err)
		}
		var payload ipc.ScreenshotPaneRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("screenshot_pane decode: %w", err)
		}
		mcpLog.Log(input.PaneID, "screenshot_pane", fmt.Sprintf("cursor=%d,%d", payload.CursorX, payload.CursorY))
		result := payload.Text
		if result == "" {
			result = "(empty screen)"
		}
		return textResult(result), nil, nil
	})
}

func registerSwitchTabTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		TabID string `json:"tab_id" jsonschema:"tab ID to switch to (use list_tabs to discover)"`
		Host  string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the id was discovered on, else local)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "switch_tab",
		Description: "Switch the active tab in the TUI.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, _, err := r.bridgeFor(input.Host, input.TabID)
		if err != nil {
			return nil, nil, fmt.Errorf("switch_tab: %w", err)
		}
		resp, err := bridge.request(ipc.MsgSwitchTabReq, ipc.SwitchTabReqPayload{TabID: input.TabID})
		if err != nil {
			return nil, nil, fmt.Errorf("switch_tab: %w", err)
		}
		var payload ipc.SwitchTabRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("switch_tab decode: %w", err)
		}
		return textResult(fmt.Sprintf("Switched to tab %s", payload.TabID)), nil, nil
	})
}

type hostedTab struct {
	ipc.TabInfo
	Host string `json:"host,omitempty"`
}

func registerListTabsTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		ProjectID string `json:"project_id,omitempty" jsonschema:"only tabs of this project"`
		Host      string `json:"host,omitempty" jsonschema:"limit to one host (default: every connected host)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_tabs",
		Description: "List all tabs (across every connected host) with their IDs, names, project, pane counts, and active status.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		host := input.Host
		if host == "" && input.ProjectID != "" {
			if _, h, err := r.bridgeFor("", input.ProjectID); err == nil {
				host = h
			}
		}
		var out []hostedTab
		err := r.forEachHost(host, func(hb hostBridge) error {
			resp, err := hb.bridge.request(ipc.MsgListTabsReq, ipc.ListTabsReqPayload{ProjectID: input.ProjectID})
			if err != nil {
				return fmt.Errorf("list_tabs%s: %w", hostSuffix(hb.host), err)
			}
			var payload ipc.ListTabsRespPayload
			if err := resp.DecodePayload(&payload); err != nil {
				return fmt.Errorf("list_tabs decode: %w", err)
			}
			for _, tb := range payload.Tabs {
				// A daemon older than the filter ignores it and answers with
				// every tab; drop the strays here so the answer is the same
				// whichever side filtered.
				if input.ProjectID != "" && tb.ProjectID != "" && tb.ProjectID != input.ProjectID {
					continue
				}
				r.remember(hb.host, tb.ID, tb.ProjectID)
				out = append(out, hostedTab{TabInfo: tb, Host: hb.host})
			}
			return nil
		})
		if err != nil {
			return nil, nil, fmt.Errorf("list_tabs: %w", err)
		}
		if out == nil {
			out = []hostedTab{}
		}
		return jsonResult(out), nil, nil
	})
}

func registerDestroyPaneTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		PaneID string `json:"pane_id" jsonschema:"pane to destroy"`
		Host   string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the id was discovered on, else local)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "destroy_pane",
		Description: "Destroy a pane and close its process. If it's the last pane in a tab, a new terminal pane is auto-created.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, _, err := r.bridgeFor(input.Host, input.PaneID)
		if err != nil {
			return nil, nil, fmt.Errorf("destroy_pane: %w", err)
		}
		resp, err := bridge.request(ipc.MsgDestroyPaneReq, ipc.DestroyPaneReqPayload{PaneID: input.PaneID})
		if err != nil {
			return nil, nil, fmt.Errorf("destroy_pane: %w", err)
		}
		var payload ipc.DestroyPaneRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("destroy_pane decode: %w", err)
		}
		if payload.Success {
			mcpLog.Log(input.PaneID, "destroy_pane", "")
			return textResult(fmt.Sprintf("Destroyed pane %s", input.PaneID)), nil, nil
		}
		return textResult(fmt.Sprintf("Failed to destroy pane %s", input.PaneID)), nil, nil
	})
}

func registerSetActivePaneTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		PaneID string `json:"pane_id" jsonschema:"pane to focus (switches tab if needed)"`
		Host   string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the id was discovered on, else local)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_active_pane",
		Description: "Set the active/focused pane in the TUI. Automatically switches to the correct tab if the pane is on a different tab.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, _, err := r.bridgeFor(input.Host, input.PaneID)
		if err != nil {
			return nil, nil, fmt.Errorf("set_active_pane: %w", err)
		}
		msg, err := ipc.NewMessage(ipc.MsgSetActivePane, ipc.SetActivePanePayload{PaneID: input.PaneID})
		if err != nil {
			return nil, nil, fmt.Errorf("set_active_pane: %w", err)
		}
		if err := bridge.sendRaw(msg); err != nil {
			return nil, nil, fmt.Errorf("set_active_pane send: %w", err)
		}
		mcpLog.Log(input.PaneID, "set_active_pane", "")
		return textResult(fmt.Sprintf("Set active pane to %s", input.PaneID)), nil, nil
	})
}

func registerCloseTUITool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		Host string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (default: local)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name: "close_tui",
		Description: "Close the Quil TUI window. The daemon stays running and all pane processes continue. " +
			"Reconnect by running quil in any terminal.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, _, err := r.bridgeFor(input.Host)
		if err != nil {
			return nil, nil, fmt.Errorf("close_tui: %w", err)
		}
		msg, err := ipc.NewMessage(ipc.MsgCloseTUI, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("close_tui: %w", err)
		}
		if err := bridge.sendRaw(msg); err != nil {
			return nil, nil, fmt.Errorf("close_tui send: %w", err)
		}
		return textResult("TUI close signal sent. Daemon continues running."), nil, nil
	})
}

// Notification tools

// hostedEvent is a PaneEventPayload plus the host it came from, which
// dismiss_notifications needs to aim the dismissal.
type hostedEvent struct {
	ipc.PaneEventPayload
	Host string `json:"host,omitempty"`
}

func registerGetNotificationsTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		Host string `json:"host,omitempty" jsonschema:"limit to one host (default: every connected host)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_notifications",
		Description: "Get all pending notification events from the Notification Center without blocking. Returns process exits, " +
			"output pattern matches, agent turn boundaries (agent_idle), task completions (task_done) and other pane events, across every connected host.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		var out []hostedEvent
		err := r.forEachHost(input.Host, func(hb hostBridge) error {
			resp, err := hb.bridge.request(ipc.MsgGetNotificationsReq, nil)
			if err != nil {
				return fmt.Errorf("get_notifications%s: %w", hostSuffix(hb.host), err)
			}
			var payload ipc.GetNotificationsRespPayload
			if err := resp.DecodePayload(&payload); err != nil {
				return fmt.Errorf("get_notifications decode: %w", err)
			}
			for _, e := range payload.Events {
				out = append(out, hostedEvent{PaneEventPayload: e, Host: hb.host})
			}
			return nil
		})
		if err != nil {
			return nil, nil, fmt.Errorf("get_notifications: %w", err)
		}
		if out == nil {
			out = []hostedEvent{}
		}
		mcpLog.Log("", "get_notifications", fmt.Sprintf("events=%d", len(out)))
		return jsonResult(out), nil, nil
	})
}

// registerDismissNotificationsTool exposes the same dismiss path the TUI uses
// (MsgDismissEvent). The previous MCP surface was read-only — get_notifications
// returned the queue but no agent could drain it, so MCP-only sessions
// accumulated events until the daemon's bounded queue evicted them. Now an
// agent can ack events explicitly: dismiss a single event by ID, or pass an
// empty event_id to clear everything.
func registerDismissNotificationsTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		EventID string `json:"event_id,omitempty" jsonschema:"event id to dismiss (empty = dismiss all)"`
		Host    string `json:"host,omitempty" jsonschema:"host the event came from (default: local)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name: "dismiss_notifications",
		Description: "Dismiss notification events from the daemon's queue. Pass event_id to drop a single event " +
			"(matching one returned by get_notifications, with its host), or omit it to clear all pending events on that host. Use after " +
			"acting on an event so it does not show up again on the next get_notifications call.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, _, err := r.bridgeFor(input.Host)
		if err != nil {
			return nil, nil, fmt.Errorf("dismiss_notifications: %w", err)
		}
		mcpLog.Log("", "dismiss_notifications", fmt.Sprintf("event_id=%q", input.EventID))

		msg, err := ipc.NewMessage(ipc.MsgDismissEvent, ipc.DismissEventPayload{EventID: input.EventID})
		if err != nil {
			return nil, nil, fmt.Errorf("dismiss_notifications build: %w", err)
		}
		// MsgDismissEvent is fire-and-forget on the daemon side — no response
		// IPC message exists. sendRaw is thread-safe; raw Send through the
		// shared bridge.client could race with the response read loop's
		// internal locking.
		if err := bridge.sendRaw(msg); err != nil {
			return nil, nil, fmt.Errorf("dismiss_notifications send: %w", err)
		}

		summary := "Dismissed all notifications."
		if input.EventID != "" {
			summary = fmt.Sprintf("Dismissed notification %s.", input.EventID)
		}
		return textResult(summary), nil, nil
	})
}

func registerWatchNotificationsTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		PaneIDs        []string `json:"pane_ids,omitempty" jsonschema:"pane IDs to watch (empty = all panes)"`
		Timeout        int      `json:"timeout,omitempty" jsonschema:"timeout in seconds (default 60, max 300)"`
		SinceTimestamp int64    `json:"since_timestamp,omitempty" jsonschema:"if set (Unix ms), return immediately with the oldest queued event newer than this — closes the race between kicking off a task and starting to watch. Pass the timestamp of the last event you handled."`
		Host           string   `json:"host,omitempty" jsonschema:"limit to one host (default: the hosts the pane ids were discovered on, else every connected host)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name: "watch_notifications",
		Description: "Block until a notification event fires for the specified panes. Returns event details when a process exits, " +
			"an agent's turn settles (agent_idle), a delegated task ends (task_done), output matches a notification pattern, or timeout. " +
			"Use this instead of polling with sleep + screenshot. " +
			"Pass since_timestamp (the Unix ms of the last event you saw) to recover events that may have fired between " +
			"your previous action and this call.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		timeout := input.Timeout
		if timeout <= 0 {
			timeout = 60
		}
		if timeout > 300 {
			timeout = 300
		}

		mcpLog.Log("", "watch_notifications", fmt.Sprintf("panes=%d timeout=%ds since=%d", len(input.PaneIDs), timeout, input.SinceTimestamp))

		targets, err := r.watchTargets(input.Host, input.PaneIDs)
		if err != nil {
			return nil, nil, fmt.Errorf("watch_notifications: %w", err)
		}
		if len(targets) == 0 {
			return nil, nil, fmt.Errorf("watch_notifications: no connected host to watch")
		}
		type answer struct {
			host    string
			payload ipc.WatchNotificationsRespPayload
			err     error
		}
		results := make(chan answer, len(targets))
		for _, hb := range targets {
			go func(hb hostBridge) {
				resp, err := hb.bridge.requestWithTimeout(
					ipc.MsgWatchNotificationsReq,
					ipc.WatchNotificationsReqPayload{
						PaneIDs:        input.PaneIDs,
						TimeoutMs:      timeout * 1000,
						SinceTimestamp: input.SinceTimestamp,
					},
					time.Duration(timeout+5)*time.Second,
				)
				a := answer{host: hb.host, err: err}
				if err == nil {
					a.err = resp.DecodePayload(&a.payload)
				}
				results <- a
			}(hb)
		}
		// The first real event wins. Timeouts from other hosts are collected
		// so a single dead host cannot mask an event elsewhere; their
		// daemon-side watchers expire on their own timeout.
		var firstErr error
		for range targets {
			a := <-results
			if a.err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("watch_notifications%s: %w", hostSuffix(a.host), a.err)
				}
				continue
			}
			if a.payload.Event != nil {
				return jsonResult(struct {
					Event   hostedEvent `json:"event"`
					Timeout bool        `json:"timeout"`
				}{hostedEvent{PaneEventPayload: *a.payload.Event, Host: a.host}, false}), nil, nil
			}
		}
		if firstErr != nil {
			return nil, nil, firstErr
		}
		return jsonResult(ipc.WatchNotificationsRespPayload{Timeout: true}), nil, nil
	})
}

// TabMemSummary is the per-tab aggregation emitted by get_memory_report.
type TabMemSummary struct {
	TabID      string `json:"tab_id"`
	TabName    string `json:"tab_name"`
	PaneCount  int    `json:"pane_count"`
	TotalBytes uint64 `json:"total_bytes"`
	TotalHuman string `json:"total_human"`
}

// buildTabMemSummaries is used by registerGetMemoryReportTool to build the tabs[]
// array in the tool output. The tab-aggregation logic lives here so the
// tool handler stays short and focused on the request/response flow.
func buildTabMemSummaries(mem ipc.MemoryReportRespPayload, tabs []ipc.TabInfo) (goHeap, ptyRSS uint64, summaries []TabMemSummary) {
	tabNames := make(map[string]string, len(tabs))
	tabOrder := make([]string, 0, len(tabs))
	for _, t := range tabs {
		tabNames[t.ID] = t.Name
		tabOrder = append(tabOrder, t.ID)
	}

	type agg struct {
		name  string
		count int
		total uint64
	}
	tabAgg := make(map[string]*agg, len(tabOrder))
	for _, id := range tabOrder {
		tabAgg[id] = &agg{name: tabNames[id]}
	}

	for _, p := range mem.Panes {
		goHeap += p.GoHeapBytes
		ptyRSS += p.PTYRSSBytes
		a, ok := tabAgg[p.TabID]
		if !ok {
			a = &agg{name: p.TabID}
			tabAgg[p.TabID] = a
			tabOrder = append(tabOrder, p.TabID)
		}
		a.count++
		a.total += p.TotalBytes
	}

	summaries = make([]TabMemSummary, 0, len(tabOrder))
	for _, id := range tabOrder {
		a := tabAgg[id]
		summaries = append(summaries, TabMemSummary{
			TabID:      id,
			TabName:    a.name,
			PaneCount:  a.count,
			TotalBytes: a.total,
			TotalHuman: memreport.HumanBytes(a.total),
		})
	}
	return
}

func registerGetMemoryReportTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		Host string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (default: local)"`
	}

	type Output struct {
		SnapshotAt  string          `json:"snapshot_at"`
		Host        string          `json:"host,omitempty"`
		TotalBytes  uint64          `json:"total_bytes"`
		TotalHuman  string          `json:"total_human"`
		GoHeapBytes uint64          `json:"go_heap_bytes"`
		PTYRSSBytes uint64          `json:"pty_rss_bytes"`
		Tabs        []TabMemSummary `json:"tabs"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_memory_report",
		Description: "Return a snapshot of daemon-side memory usage: per-tab totals plus grand total. Layers reported: Go-heap (ring buffers + ghost snapshots + plugin state) and PTY child resident memory (OS-reported; not comparable across platforms). TUI-side memory is intentionally omitted because MCP may be invoked when the TUI is disconnected.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, host, err := r.bridgeFor(input.Host)
		if err != nil {
			return nil, nil, fmt.Errorf("get_memory_report: %w", err)
		}
		memResp, err := bridge.request(ipc.MsgMemoryReportReq, ipc.MemoryReportReqPayload{})
		if err != nil {
			return nil, nil, fmt.Errorf("get_memory_report: %w", err)
		}
		var memPayload ipc.MemoryReportRespPayload
		if err := memResp.DecodePayload(&memPayload); err != nil {
			return nil, nil, fmt.Errorf("get_memory_report decode: %w", err)
		}

		// Daemon embeds the tab list in the same response (since v1.10.x);
		// no second IPC round-trip is needed for newer daemons. Against a
		// pre-1.10 daemon memPayload.Tabs will be nil — buildTabMemSummaries
		// degrades gracefully and uses bare tab IDs as the display name.
		goHeap, ptyRSS, summaries := buildTabMemSummaries(memPayload, memPayload.Tabs)

		out := Output{
			SnapshotAt:  time.Unix(0, memPayload.SnapshotAt).UTC().Format(time.RFC3339),
			Host:        host,
			TotalBytes:  memPayload.Total,
			TotalHuman:  memreport.HumanBytes(memPayload.Total),
			GoHeapBytes: goHeap,
			PTYRSSBytes: ptyRSS,
			Tabs:        summaries,
		}

		mcpLog.Log("", "get_memory_report", fmt.Sprintf("panes=%d total=%s", len(memPayload.Panes), out.TotalHuman))
		return jsonResult(out), nil, nil
	})
}

// fetchPaneMeta best-effort fetches a pane's name + type via MsgPaneStatusReq
// for use by registerGetPaneMemoryTool. On failure returns empty strings —
// the memory numbers are the point, metadata is nice-to-have.
func fetchPaneMeta(bridge *mcpBridge, paneID string) (name, paneType string) {
	resp, err := bridge.request(ipc.MsgPaneStatusReq, ipc.PaneStatusReqPayload{PaneID: paneID})
	if err != nil {
		return "", ""
	}
	var status ipc.PaneStatusRespPayload
	if err := resp.DecodePayload(&status); err != nil {
		return "", ""
	}
	return status.Name, status.Type
}

func registerGetPaneMemoryTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		PaneID string `json:"pane_id" jsonschema:"pane ID (use list_panes or get_memory_report to discover)"`
		Host   string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the id was discovered on, else local)"`
	}

	type Output struct {
		SnapshotAt  string `json:"snapshot_at"` // RFC3339 (UTC)
		PaneID      string `json:"pane_id"`
		TabID       string `json:"tab_id"`
		Host        string `json:"host,omitempty"`
		PaneName    string `json:"pane_name,omitempty"`
		Type        string `json:"type,omitempty"`
		GoHeapBytes uint64 `json:"go_heap_bytes"`
		PTYRSSBytes uint64 `json:"pty_rss_bytes"`
		TotalBytes  uint64 `json:"total_bytes"`
		TotalHuman  string `json:"total_human"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_pane_memory",
		Description: "Return daemon-side memory usage for a single pane: Go-heap (ring buffer + ghost snapshot + plugin state), PTY child resident memory, and combined total. Call get_memory_report or list_panes first to discover pane IDs. PTY RSS is OS-reported and not comparable across platforms.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		if input.PaneID == "" {
			return nil, nil, fmt.Errorf("get_pane_memory: pane_id is required")
		}
		bridge, host, err := r.bridgeFor(input.Host, input.PaneID)
		if err != nil {
			return nil, nil, fmt.Errorf("get_pane_memory: %w", err)
		}

		memResp, err := bridge.request(ipc.MsgMemoryReportReq, ipc.MemoryReportReqPayload{})
		if err != nil {
			return nil, nil, fmt.Errorf("get_pane_memory: %w", err)
		}
		var memPayload ipc.MemoryReportRespPayload
		if err := memResp.DecodePayload(&memPayload); err != nil {
			return nil, nil, fmt.Errorf("get_pane_memory decode: %w", err)
		}

		var found *ipc.PaneMemInfo
		for i := range memPayload.Panes {
			if memPayload.Panes[i].PaneID == input.PaneID {
				found = &memPayload.Panes[i]
				break
			}
		}
		if found == nil {
			return nil, nil, fmt.Errorf("get_pane_memory: pane not found: %s", input.PaneID)
		}

		paneName, paneType := fetchPaneMeta(bridge, input.PaneID)

		out := Output{
			SnapshotAt:  time.Unix(0, memPayload.SnapshotAt).UTC().Format(time.RFC3339),
			PaneID:      found.PaneID,
			TabID:       found.TabID,
			Host:        host,
			PaneName:    paneName,
			Type:        paneType,
			GoHeapBytes: found.GoHeapBytes,
			PTYRSSBytes: found.PTYRSSBytes,
			TotalBytes:  found.TotalBytes,
			TotalHuman:  memreport.HumanBytes(found.TotalBytes),
		}

		mcpLog.Log(input.PaneID, "get_pane_memory", out.TotalHuman)
		return jsonResult(out), nil, nil
	})
}
