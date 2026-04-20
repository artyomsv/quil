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

func registerMCPTools(s *mcp.Server, bridge *mcpBridge, mcpLog *mcpLogger) {
	// Phase A (M10 core)
	registerListPanesTool(s, bridge, mcpLog)
	registerReadPaneOutputTool(s, bridge, mcpLog)
	registerSendToPaneTool(s, bridge, mcpLog)
	registerGetPaneStatusTool(s, bridge, mcpLog)
	registerCreatePaneTool(s, bridge, mcpLog)
	// Phase B (expansion)
	registerSendKeysTool(s, bridge, mcpLog)
	registerRestartPaneTool(s, bridge, mcpLog)
	registerScreenshotPaneTool(s, bridge, mcpLog)
	registerSwitchTabTool(s, bridge, mcpLog)
	registerListTabsTool(s, bridge, mcpLog)
	registerDestroyPaneTool(s, bridge, mcpLog)
	registerSetActivePaneTool(s, bridge, mcpLog)
	registerCloseTUITool(s, bridge, mcpLog)
	// Notification tools
	registerGetNotificationsTool(s, bridge, mcpLog)
	registerWatchNotificationsTool(s, bridge, mcpLog)
	// Memory reporting
	registerGetMemoryReportTool(s, bridge, mcpLog)
}

func registerListPanesTool(s *mcp.Server, bridge *mcpBridge, mcpLog *mcpLogger) {
	type Input struct{}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_panes",
		Description: "List all panes across all tabs with their IDs, types, names, working directories, and running status. Use this to discover pane IDs for other tools.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ Input) (*mcp.CallToolResult, any, error) {
		resp, err := bridge.request(ipc.MsgListPanesReq, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("list_panes: %w", err)
		}
		var payload ipc.ListPanesRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("list_panes decode: %w", err)
		}
		// PaneInfo fields are all primitives — json.MarshalIndent cannot fail
		text, _ := json.MarshalIndent(payload.Panes, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(text)}},
		}, nil, nil
	})
}

func registerReadPaneOutputTool(s *mcp.Server, bridge *mcpBridge, mcpLog *mcpLogger) {
	type Input struct {
		PaneID    string `json:"pane_id" jsonschema:"pane ID (use list_panes to discover IDs)"`
		LastLines int    `json:"last_lines,omitempty" jsonschema:"number of lines to return (default 50, max 1000)"`
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
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: payload.Text}},
		}, nil, nil
	})
}

func registerSendToPaneTool(s *mcp.Server, bridge *mcpBridge, mcpLog *mcpLogger) {
	type Input struct {
		PaneID     string `json:"pane_id" jsonschema:"target pane ID"`
		InputText  string `json:"input" jsonschema:"text to send to the pane"`
		PressEnter *bool  `json:"press_enter,omitempty" jsonschema:"append newline after input (default true)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name: "send_to_pane",
		Description: "Send keystrokes or commands to a pane's terminal. By default appends a newline (press_enter=true) to execute the command. " +
			"SECURITY: This executes arbitrary input in the target pane's shell. The MCP bridge has the same access as the terminal's owner.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		redactCount := countRedactMarkers(input.InputText)
		data := stripRedactMarkers(input.InputText)
		if input.PressEnter == nil || *input.PressEnter {
			data += "\n"
		}
		detail := fmt.Sprintf("bytes=%d", len(data))
		if redactCount > 0 {
			detail += fmt.Sprintf(" [%d redacted]", redactCount)
		}
		mcpLog.Log(input.PaneID, "send_to_pane", detail)
		msg, err := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{
			PaneID: input.PaneID,
			Data:   []byte(data),
		})
		if err != nil {
			return nil, nil, fmt.Errorf("send_to_pane: %w", err)
		}
		if err := bridge.sendRaw(msg); err != nil {
			return nil, nil, fmt.Errorf("send_to_pane send: %w", err)
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Sent %d bytes to %s", len(data), input.PaneID)}},
		}, nil, nil
	})
}

func registerGetPaneStatusTool(s *mcp.Server, bridge *mcpBridge, mcpLog *mcpLogger) {
	type Input struct {
		PaneID string `json:"pane_id" jsonschema:"pane ID to check"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_pane_status",
		Description: "Get the status of a pane's process — whether it's running or exited, exit code, type, and working directory.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		resp, err := bridge.request(ipc.MsgPaneStatusReq, ipc.PaneStatusReqPayload{
			PaneID: input.PaneID,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("get_pane_status: %w", err)
		}
		var payload ipc.PaneStatusRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("get_pane_status decode: %w", err)
		}
		// PaneStatusRespPayload fields are primitives — json.MarshalIndent cannot fail
		text, _ := json.MarshalIndent(payload, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(text)}},
		}, nil, nil
	})
}

func registerCreatePaneTool(s *mcp.Server, bridge *mcpBridge, mcpLog *mcpLogger) {
	type Input struct {
		TabID string `json:"tab_id,omitempty" jsonschema:"tab to create pane in (default: active tab)"`
		CWD   string `json:"cwd,omitempty" jsonschema:"working directory for the new pane"`
		Type  string `json:"type,omitempty" jsonschema:"plugin type: terminal (default), claude-code, ssh, stripe"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_pane",
		Description: "Create a new pane in a tab. Returns the new pane ID. Defaults to a terminal pane in the active tab.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		resp, err := bridge.request(ipc.MsgCreatePaneReq, ipc.CreatePaneReqPayload{
			TabID: input.TabID,
			CWD:   input.CWD,
			Type:  input.Type,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("create_pane: %w", err)
		}
		var payload ipc.CreatePaneRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("create_pane decode: %w", err)
		}
		// CreatePaneRespPayload has only string fields — json.MarshalIndent cannot fail
		text, _ := json.MarshalIndent(payload, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(text)}},
		}, nil, nil
	})
}

// Phase B tools

func registerSendKeysTool(s *mcp.Server, bridge *mcpBridge, mcpLog *mcpLogger) {
	type Input struct {
		PaneID string   `json:"pane_id" jsonschema:"target pane ID"`
		Keys   []string `json:"keys" jsonschema:"key names or text: enter, tab, escape, up, down, left, right, ctrl+c, f1-f12, or literal text"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name: "send_keys",
		Description: "Send named key sequences to a pane. Each element can be a key name (enter, tab, escape, up, down, left, right, " +
			"home, end, page_up, page_down, f1-f12, ctrl+a through ctrl+z, backspace, delete, space) or literal text to type.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		if len(input.Keys) > maxSendKeys {
			return nil, nil, fmt.Errorf("send_keys: too many keys (%d, max %d)", len(input.Keys), maxSendKeys)
		}
		// Send keys individually with delays between escape sequences.
		// Batching escape sequences (e.g., down+down+enter) causes TUI apps
		// to only process the first one before enter arrives.
		var textBuf strings.Builder
		sendBuf := func() error {
			if textBuf.Len() == 0 {
				return nil
			}
			msg, err := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{
				PaneID: input.PaneID,
				Data:   []byte(textBuf.String()),
			})
			if err != nil {
				return err
			}
			textBuf.Reset()
			return bridge.sendRaw(msg)
		}

		for _, key := range input.Keys {
			seq, isEscape := keyMap[strings.ToLower(key)]
			if isEscape {
				if err := sendBuf(); err != nil {
					return nil, nil, fmt.Errorf("send_keys: %w", err)
				}
				msg, err := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{
					PaneID: input.PaneID,
					Data:   []byte(seq),
				})
				if err != nil {
					return nil, nil, fmt.Errorf("send_keys: %w", err)
				}
				if err := bridge.sendRaw(msg); err != nil {
					return nil, nil, fmt.Errorf("send_keys send: %w", err)
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
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Sent %d keys to %s", len(input.Keys), input.PaneID)}},
		}, nil, nil
	})
}

func registerRestartPaneTool(s *mcp.Server, bridge *mcpBridge, mcpLog *mcpLogger) {
	type Input struct {
		PaneID string `json:"pane_id" jsonschema:"pane to restart"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "restart_pane",
		Description: "Kill a pane's process and respawn it with the same plugin type, working directory, and instance config. Useful for fixing stuck or crashed panes.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		resp, err := bridge.request(ipc.MsgRestartPaneReq, ipc.RestartPaneReqPayload{
			PaneID: input.PaneID,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("restart_pane: %w", err)
		}
		var payload ipc.RestartPaneRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("restart_pane decode: %w", err)
		}
		mcpLog.Log(input.PaneID, "restart_pane", "")
		// RestartPaneRespPayload has primitive fields — json.MarshalIndent cannot fail
		text, _ := json.MarshalIndent(payload, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(text)}},
		}, nil, nil
	})
}

func registerScreenshotPaneTool(s *mcp.Server, bridge *mcpBridge, mcpLog *mcpLogger) {
	type Input struct {
		PaneID string `json:"pane_id" jsonschema:"pane to screenshot"`
		Width  int    `json:"width,omitempty" jsonschema:"terminal width in columns (default 80)"`
		Height int    `json:"height,omitempty" jsonschema:"terminal height in rows (default 24)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name: "screenshot_pane",
		Description: "Take a VT-emulated text screenshot of a pane. Unlike read_pane_output which returns raw output lines, " +
			"this shows the actual terminal screen state — what the user sees right now. Essential for reading interactive TUI apps (vim, htop, etc.).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
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
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: result}},
		}, nil, nil
	})
}

func registerSwitchTabTool(s *mcp.Server, bridge *mcpBridge, mcpLog *mcpLogger) {
	type Input struct {
		TabID string `json:"tab_id" jsonschema:"tab ID to switch to (use list_tabs to discover)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "switch_tab",
		Description: "Switch the active tab in the TUI.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		resp, err := bridge.request(ipc.MsgSwitchTabReq, ipc.SwitchTabReqPayload{
			TabID: input.TabID,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("switch_tab: %w", err)
		}
		var payload ipc.SwitchTabRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("switch_tab decode: %w", err)
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Switched to tab %s", payload.TabID)}},
		}, nil, nil
	})
}

func registerListTabsTool(s *mcp.Server, bridge *mcpBridge, mcpLog *mcpLogger) {
	type Input struct{}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_tabs",
		Description: "List all tabs with their IDs, names, pane counts, and active status.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ Input) (*mcp.CallToolResult, any, error) {
		resp, err := bridge.request(ipc.MsgListTabsReq, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("list_tabs: %w", err)
		}
		var payload ipc.ListTabsRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("list_tabs decode: %w", err)
		}
		// TabInfo fields are all primitives — json.MarshalIndent cannot fail
		text, _ := json.MarshalIndent(payload.Tabs, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(text)}},
		}, nil, nil
	})
}

func registerDestroyPaneTool(s *mcp.Server, bridge *mcpBridge, mcpLog *mcpLogger) {
	type Input struct {
		PaneID string `json:"pane_id" jsonschema:"pane to destroy"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "destroy_pane",
		Description: "Destroy a pane and close its process. If it's the last pane in a tab, a new terminal pane is auto-created.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		resp, err := bridge.request(ipc.MsgDestroyPaneReq, ipc.DestroyPaneReqPayload{
			PaneID: input.PaneID,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("destroy_pane: %w", err)
		}
		var payload ipc.DestroyPaneRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("destroy_pane decode: %w", err)
		}
		if payload.Success {
			mcpLog.Log(input.PaneID, "destroy_pane", "")
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Destroyed pane %s", input.PaneID)}},
			}, nil, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Failed to destroy pane %s", input.PaneID)}},
		}, nil, nil
	})
}

func registerSetActivePaneTool(s *mcp.Server, bridge *mcpBridge, mcpLog *mcpLogger) {
	type Input struct {
		PaneID string `json:"pane_id" jsonschema:"pane to focus (switches tab if needed)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_active_pane",
		Description: "Set the active/focused pane in the TUI. Automatically switches to the correct tab if the pane is on a different tab.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		msg, err := ipc.NewMessage(ipc.MsgSetActivePane, ipc.SetActivePanePayload{
			PaneID: input.PaneID,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("set_active_pane: %w", err)
		}
		if err := bridge.sendRaw(msg); err != nil {
			return nil, nil, fmt.Errorf("set_active_pane send: %w", err)
		}
		mcpLog.Log(input.PaneID, "set_active_pane", "")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Set active pane to %s", input.PaneID)}},
		}, nil, nil
	})
}

func registerCloseTUITool(s *mcp.Server, bridge *mcpBridge, mcpLog *mcpLogger) {
	type Input struct{}

	mcp.AddTool(s, &mcp.Tool{
		Name: "close_tui",
		Description: "Close the Quil TUI window. The daemon stays running and all pane processes continue. " +
			"Reconnect by running quil in any terminal.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ Input) (*mcp.CallToolResult, any, error) {
		msg, err := ipc.NewMessage(ipc.MsgCloseTUI, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("close_tui: %w", err)
		}
		if err := bridge.sendRaw(msg); err != nil {
			return nil, nil, fmt.Errorf("close_tui send: %w", err)
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "TUI close signal sent. Daemon continues running."}},
		}, nil, nil
	})
}

// Notification tools

func registerGetNotificationsTool(s *mcp.Server, bridge *mcpBridge, mcpLog *mcpLogger) {
	type Input struct{}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_notifications",
		Description: "Get all pending notification events from the Notification Center without blocking. Returns process exits, output pattern matches, and other pane events.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ Input) (*mcp.CallToolResult, any, error) {
		resp, err := bridge.request(ipc.MsgGetNotificationsReq, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("get_notifications: %w", err)
		}
		var payload ipc.GetNotificationsRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("get_notifications decode: %w", err)
		}
		mcpLog.Log("", "get_notifications", fmt.Sprintf("events=%d", len(payload.Events)))
		// GetNotificationsRespPayload has primitive fields — json.MarshalIndent cannot fail
		text, _ := json.MarshalIndent(payload.Events, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(text)}},
		}, nil, nil
	})
}

func registerWatchNotificationsTool(s *mcp.Server, bridge *mcpBridge, mcpLog *mcpLogger) {
	type Input struct {
		PaneIDs []string `json:"pane_ids,omitempty" jsonschema:"pane IDs to watch (empty = all panes)"`
		Timeout int      `json:"timeout,omitempty" jsonschema:"timeout in seconds (default 60, max 300)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name: "watch_notifications",
		Description: "Block until a notification event fires for the specified panes. Returns event details when a process exits, " +
			"output matches a notification pattern, or timeout. Use this instead of polling with sleep + screenshot.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		timeout := input.Timeout
		if timeout <= 0 {
			timeout = 60
		}
		if timeout > 300 {
			timeout = 300
		}

		mcpLog.Log("", "watch_notifications", fmt.Sprintf("panes=%d timeout=%ds", len(input.PaneIDs), timeout))

		resp, err := bridge.requestWithTimeout(
			ipc.MsgWatchNotificationsReq,
			ipc.WatchNotificationsReqPayload{
				PaneIDs:   input.PaneIDs,
				TimeoutMs: timeout * 1000,
			},
			time.Duration(timeout+5)*time.Second,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("watch_notifications: %w", err)
		}
		var payload ipc.WatchNotificationsRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("watch_notifications decode: %w", err)
		}
		// WatchNotificationsRespPayload has primitive fields — json.MarshalIndent cannot fail
		text, _ := json.MarshalIndent(payload, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(text)}},
		}, nil, nil
	})
}

func registerGetMemoryReportTool(s *mcp.Server, bridge *mcpBridge, mcpLog *mcpLogger) {
	type Input struct{}

	type TabMemSummary struct {
		TabID      string `json:"tab_id"`
		TabName    string `json:"tab_name"`
		PaneCount  int    `json:"pane_count"`
		TotalBytes uint64 `json:"total_bytes"`
		TotalHuman string `json:"total_human"`
	}

	type Output struct {
		SnapshotAt  string          `json:"snapshot_at"` // RFC3339
		TotalBytes  uint64          `json:"total_bytes"`
		TotalHuman  string          `json:"total_human"`
		GoHeapBytes uint64          `json:"go_heap_bytes"`
		PTYRSSBytes uint64          `json:"pty_rss_bytes"`
		Tabs        []TabMemSummary `json:"tabs"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_memory_report",
		Description: "Return a snapshot of daemon-side memory usage: per-tab totals plus grand total. Layers reported: Go-heap (ring buffers + ghost snapshots + plugin state) and PTY child resident memory (OS-reported; not comparable across platforms). TUI-side memory is intentionally omitted because MCP may be invoked when the TUI is disconnected.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ Input) (*mcp.CallToolResult, any, error) {
		memResp, err := bridge.request(ipc.MsgMemoryReportReq, ipc.MemoryReportReqPayload{})
		if err != nil {
			return nil, nil, fmt.Errorf("get_memory_report: %w", err)
		}
		var memPayload ipc.MemoryReportRespPayload
		if err := memResp.DecodePayload(&memPayload); err != nil {
			return nil, nil, fmt.Errorf("get_memory_report decode: %w", err)
		}

		tabsResp, err := bridge.request(ipc.MsgListTabsReq, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("get_memory_report tabs: %w", err)
		}
		var tabsPayload ipc.ListTabsRespPayload
		if err := tabsResp.DecodePayload(&tabsPayload); err != nil {
			return nil, nil, fmt.Errorf("get_memory_report tabs decode: %w", err)
		}

		tabNames := make(map[string]string, len(tabsPayload.Tabs))
		tabOrder := make([]string, 0, len(tabsPayload.Tabs))
		for _, t := range tabsPayload.Tabs {
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

		var goHeap, ptyRSS uint64
		for _, p := range memPayload.Panes {
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

		out := Output{
			SnapshotAt:  time.Unix(0, memPayload.SnapshotAt).UTC().Format(time.RFC3339),
			TotalBytes:  memPayload.Total,
			TotalHuman:  memreport.HumanBytes(memPayload.Total),
			GoHeapBytes: goHeap,
			PTYRSSBytes: ptyRSS,
		}
		for _, id := range tabOrder {
			a := tabAgg[id]
			out.Tabs = append(out.Tabs, TabMemSummary{
				TabID:      id,
				TabName:    a.name,
				PaneCount:  a.count,
				TotalBytes: a.total,
				TotalHuman: memreport.HumanBytes(a.total),
			})
		}

		mcpLog.Log("", "get_memory_report", fmt.Sprintf("panes=%d total=%s", len(memPayload.Panes), out.TotalHuman))
		text, _ := json.MarshalIndent(out, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(text)}},
		}, nil, nil
	})
}
