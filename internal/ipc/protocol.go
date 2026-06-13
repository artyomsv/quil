package ipc

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Message type constants
const (
	// Lifecycle
	MsgAttach    = "attach"
	MsgDetach    = "detach"
	MsgShutdown  = "shutdown"
	MsgHeartbeat = "heartbeat"

	// Session control (Client -> Daemon)
	MsgCreatePane   = "create_pane"
	MsgDestroyPane  = "destroy_pane"
	MsgResizePane   = "resize_pane"
	MsgUpdatePane   = "update_pane"
	MsgUpdateLayout = "update_layout"

	// Tab control (Client -> Daemon)
	MsgCreateTab   = "create_tab"
	MsgDestroyTab  = "destroy_tab"
	MsgSwitchTab   = "switch_tab"
	MsgUpdateTab   = "update_tab"
	MsgReorderTab  = "reorder_tab"

	// I/O (bidirectional)
	MsgPaneInput  = "pane_input"
	MsgPaneOutput = "pane_output"

	// State sync (Daemon -> Client)
	MsgWorkspaceState = "workspace_state"
	MsgStateUpdate    = "state_update"

	// Plugin (Daemon -> Client)
	MsgPluginError = "plugin_error"

	// Plugin management (Client -> Daemon)
	MsgReloadPlugins = "reload_plugins"

	// MCP request-response (Client -> Daemon -> Client)
	MsgListPanesReq       = "list_panes_req"
	MsgListPanesResp      = "list_panes_resp"
	MsgReadPaneOutputReq  = "read_pane_output_req"
	MsgReadPaneOutputResp = "read_pane_output_resp"
	MsgPaneStatusReq      = "pane_status_req"
	MsgPaneStatusResp     = "pane_status_resp"
	MsgCreatePaneReq      = "create_pane_req"
	MsgCreatePaneResp     = "create_pane_resp"
	MsgRestartPaneReq     = "restart_pane_req"
	MsgRestartPaneResp    = "restart_pane_resp"
	MsgScreenshotPaneReq  = "screenshot_pane_req"
	MsgScreenshotPaneResp = "screenshot_pane_resp"
	MsgSwitchTabReq       = "switch_tab_req"
	MsgSwitchTabResp      = "switch_tab_resp"
	MsgListTabsReq        = "list_tabs_req"
	MsgListTabsResp       = "list_tabs_resp"
	MsgDestroyPaneReq     = "destroy_pane_req"
	MsgDestroyPaneResp    = "destroy_pane_resp"
	MsgSetActivePane      = "set_active_pane"  // broadcast to TUI
	MsgCloseTUI           = "close_tui"        // broadcast to TUI
	MsgHighlightPane      = "highlight_pane"   // broadcast to TUI (MCP interaction indicator)

	// Notification center (M12)
	MsgPaneEvent              = "pane_event"               // broadcast to TUI
	MsgDismissEvent           = "dismiss_event"            // client → daemon
	MsgGetNotificationsReq    = "get_notifications_req"    // MCP request
	MsgGetNotificationsResp   = "get_notifications_resp"   // MCP response
	MsgWatchNotificationsReq  = "watch_notifications_req"  // MCP request (blocking)
	MsgWatchNotificationsResp = "watch_notifications_resp" // MCP response

	// Version negotiation — TUI asks daemon for its version string before
	// attaching so mismatches can be surfaced as a blocking dialog or an
	// auto-restart prompt. A daemon built before this pair existed will
	// silently drop MsgVersionReq; the client handles the timeout.
	MsgVersionReq  = "version_req"  // client → daemon (empty payload)
	MsgVersionResp = "version_resp" // daemon → client (VersionRespPayload)

	// Memory reporting
	MsgMemoryReportReq  = "memory_report_req"
	MsgMemoryReportResp = "memory_report_resp"
)

// Message is the wire format for IPC communication.
type Message struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"` // request-response correlation (MCP bridge)
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Payload types

type AttachPayload struct {
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
	CWD  string `json:"cwd,omitempty"`
}

type CreatePanePayload struct {
	TabID         string   `json:"tab_id"`
	CWD           string   `json:"cwd"`
	Type          string   `json:"type,omitempty"`
	InstanceName  string   `json:"instance_name,omitempty"`
	InstanceArgs  []string `json:"instance_args,omitempty"`
	ReplacePaneID string   `json:"replace_pane_id,omitempty"`
	// Overlay marks the pane as a TUI overlay (lazygit toggle view): it
	// never enters the layout tree, is muted at creation, and is excluded
	// from disk snapshots (ephemeral — gone on daemon restart).
	// Trust: any IPC client can set this field; the daemon honors it under
	// the same socket trust model as every other field (the MCP bridge
	// deliberately does not expose it).
	Overlay bool `json:"overlay,omitempty"`
}

type DestroyPanePayload struct {
	PaneID string `json:"pane_id"`
}

type ResizePanePayload struct {
	PaneID string `json:"pane_id"`
	Rows   uint16 `json:"rows"`
	Cols   uint16 `json:"cols"`
}

type PaneInputPayload struct {
	PaneID string `json:"pane_id"`
	Data   []byte `json:"data"`
}

type PaneOutputPayload struct {
	PaneID string `json:"pane_id"`
	Data   []byte `json:"data"`
	Ghost  bool   `json:"ghost,omitempty"`
}

type CreateTabPayload struct {
	Name string `json:"name"`
}

type DestroyTabPayload struct {
	TabID string `json:"tab_id"`
}

type SwitchTabPayload struct {
	TabID string `json:"tab_id"`
}

type UpdateTabPayload struct {
	TabID string `json:"tab_id"`
	Name  string `json:"name,omitempty"`
	Color string `json:"color,omitempty"`
	// ClearColor disambiguates an empty Color: "" alone means "no change"
	// (e.g. a rename of an uncolored tab), ClearColor=true means "reset to
	// the default color" (the tab-color cycle wrapping past the last color).
	ClearColor bool `json:"clear_color,omitempty"`
}

// ReorderTabPayload moves an existing tab to a new ordinal position. NewIndex
// is clamped to the daemon-side tab list bounds, so a stale TUI does not have
// to track creation/destruction races to send a safe value.
type ReorderTabPayload struct {
	TabID    string `json:"tab_id"`
	NewIndex int    `json:"new_index"`
}

type UpdatePanePayload struct {
	PaneID string `json:"pane_id"`
	Name   string `json:"name,omitempty"`
	CWD    string `json:"cwd,omitempty"`
	// Muted is a pointer so an unset field (nil) is distinguishable from an
	// explicit false. Callers updating only Name or CWD pass nil and the
	// daemon leaves the pane's mute state untouched.
	Muted *bool `json:"muted,omitempty"`
	// Eager is a pointer for the same nil-vs-false tri-state reason as Muted.
	Eager *bool `json:"eager,omitempty"`
}

type UpdateLayoutPayload struct {
	TabID  string          `json:"tab_id"`
	Layout json.RawMessage `json:"layout"`
}

type PluginErrorPayload struct {
	PaneID  string `json:"pane_id"`
	Title   string `json:"title"`
	Message string `json:"message"`
}

// MCP request-response payloads

type PaneInfo struct {
	ID           string `json:"id"`
	TabID        string `json:"tab_id"`
	TabName      string `json:"tab_name"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	CWD          string `json:"cwd"`
	Running      bool   `json:"running"`
	Pending      bool   `json:"pending,omitempty"`
	InstanceName string `json:"instance_name,omitempty"`
}

type ListPanesRespPayload struct {
	Panes []PaneInfo `json:"panes"`
}

type ReadPaneOutputReqPayload struct {
	PaneID    string `json:"pane_id"`
	LastLines int    `json:"last_lines"`
}

type ReadPaneOutputRespPayload struct {
	PaneID string `json:"pane_id"`
	Text   string `json:"text"`
	Lines  int    `json:"lines"`
}

type PaneStatusReqPayload struct {
	PaneID string `json:"pane_id"`
}

type PaneStatusRespPayload struct {
	PaneID   string `json:"pane_id"`
	Running  bool   `json:"running"`
	Pending  bool   `json:"pending,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Type     string `json:"type"`
	CWD      string `json:"cwd"`
	Name     string `json:"name"`
}

type CreatePaneReqPayload struct {
	TabID        string   `json:"tab_id,omitempty"`
	CWD          string   `json:"cwd,omitempty"`
	Type         string   `json:"type,omitempty"`
	InstanceName string   `json:"instance_name,omitempty"`
	InstanceArgs []string `json:"instance_args,omitempty"`
}

type CreatePaneRespPayload struct {
	PaneID string `json:"pane_id"`
	TabID  string `json:"tab_id"`
}

// Phase B MCP payloads

type RestartPaneReqPayload struct {
	PaneID string `json:"pane_id"`
}

type RestartPaneRespPayload struct {
	PaneID  string `json:"pane_id"`
	Success bool   `json:"success"`
}

type ScreenshotPaneReqPayload struct {
	PaneID string `json:"pane_id"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

type ScreenshotPaneRespPayload struct {
	PaneID  string `json:"pane_id"`
	Text    string `json:"text"`
	CursorX int    `json:"cursor_x"`
	CursorY int    `json:"cursor_y"`
}

type SwitchTabReqPayload struct {
	TabID string `json:"tab_id"`
}

type SwitchTabRespPayload struct {
	TabID string `json:"tab_id"`
}

type TabInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color,omitempty"`
	PaneCount int    `json:"pane_count"`
	Active    bool   `json:"active"`
}

type ListTabsRespPayload struct {
	Tabs []TabInfo `json:"tabs"`
}

type DestroyPaneReqPayload struct {
	PaneID string `json:"pane_id"`
}

type DestroyPaneRespPayload struct {
	Success bool `json:"success"`
}

type SetActivePanePayload struct {
	PaneID string `json:"pane_id"`
}

type HighlightPanePayload struct {
	PaneID string `json:"pane_id"`
}

// Notification center payloads (M12)

type PaneEventPayload struct {
	ID        string            `json:"id"`
	PaneID    string            `json:"pane_id"`
	TabID     string            `json:"tab_id"`
	PaneName  string            `json:"pane_name"`
	Type      string            `json:"type"`
	Title     string            `json:"title"`
	Message   string            `json:"message,omitempty"`
	Severity  string            `json:"severity"`
	Timestamp int64             `json:"timestamp"`
	Data      map[string]string `json:"data,omitempty"`
}

type DismissEventPayload struct {
	EventID string `json:"event_id"` // empty = dismiss all
}

type GetNotificationsRespPayload struct {
	Events []PaneEventPayload `json:"events"`
}

type WatchNotificationsReqPayload struct {
	PaneIDs   []string `json:"pane_ids,omitempty"`
	TimeoutMs int      `json:"timeout_ms"`
	// SinceTimestamp closes the race between "kick off a task" and "start
	// watching" — events fired during that window would otherwise be lost.
	// When set (Unix ms), the daemon first scans the existing event queue
	// for any matching event whose timestamp is strictly greater, returning
	// the oldest such event immediately. Only if the queue holds no
	// qualifying event does it register a blocking watcher. Agents should
	// pass the timestamp of the last event they handled.
	SinceTimestamp int64 `json:"since_timestamp,omitempty"`
}

type WatchNotificationsRespPayload struct {
	Event   *PaneEventPayload `json:"event,omitempty"`
	Timeout bool              `json:"timeout"`
}

// VersionRespPayload carries the daemon's version string. MsgVersionReq
// has no payload — the request is just "what version are you running?".
type VersionRespPayload struct {
	Version string `json:"version"`
}

// Memory reporting payloads

type MemoryReportReqPayload struct{}

// PaneMemInfo is the wire form of a single pane's daemon-side memory.
// TUI-local memory is not part of the wire format — the TUI merges its own
// values at render time.
type PaneMemInfo struct {
	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id"`
	GoHeapBytes uint64 `json:"go_heap_bytes"`
	PTYRSSBytes uint64 `json:"pty_rss_bytes"`
	TotalBytes  uint64 `json:"total_bytes"`
}

type MemoryReportRespPayload struct {
	SnapshotAt int64         `json:"snapshot_at"` // Unix nanoseconds
	Panes      []PaneMemInfo `json:"panes"`
	Total      uint64        `json:"total"`
	// Tabs is the same view that MsgListTabsResp would return at the moment
	// the daemon assembled this response. Embedded here so MCP
	// `get_memory_report` does not need a second round-trip to enrich tab
	// IDs with names. Note: the per-pane memory numbers come from the
	// memreport collector's last tick (up to 5 s old), while Tabs is taken
	// fresh — the two halves are captured close-in-time on the daemon side
	// but are not guaranteed to be drawn from the exact same instant.
	Tabs []TabInfo `json:"tabs,omitempty"`
}

// NewMessage creates a Message with a typed payload.
func NewMessage(typ string, payload any) (*Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Message{Type: typ, Payload: data}, nil
}

// maxFrameSize bounds a single wire frame (length prefix excluded), shared by
// both directions: ReadMessage rejects oversized incoming frames, and
// EncodeFrame refuses to produce one — failing fast at the producer with an
// attributable error instead of poisoning the stream and surfacing as an
// opaque "message too large" disconnect on the peer. The guard also bounds
// the size arithmetic in EncodeFrame's allocation.
const maxFrameSize = 10 * 1024 * 1024

// EncodeFrame marshals msg into a single length-prefixed wire frame in one
// allocation. Shared by WriteMessage and the per-conn send queues — replaces
// the marshal → bytes.Buffer → clone chain that copied every broadcast frame
// up to four times.
func EncodeFrame(msg *Message) ([]byte, error) {
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal message: %w", err)
	}
	if len(data) > maxFrameSize {
		return nil, fmt.Errorf("frame too large: %d bytes (max %d)", len(data), maxFrameSize)
	}
	frame := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(data)))
	copy(frame[4:], data)
	return frame, nil
}

// WriteMessage writes a length-prefixed JSON message to w.
// Format: [4 bytes uint32 big-endian length][JSON payload]
func WriteMessage(w io.Writer, msg *Message) error {
	frame, err := EncodeFrame(msg)
	if err != nil {
		return err
	}
	if _, err := w.Write(frame); err != nil {
		return fmt.Errorf("write payload: %w", err)
	}
	return nil
}

// ReadMessage reads a length-prefixed JSON message from r.
func ReadMessage(r io.Reader) (*Message, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("read length: %w", err)
	}
	length := binary.BigEndian.Uint32(lenBuf[:])

	if length > maxFrameSize {
		return nil, fmt.Errorf("message too large: %d bytes", length)
	}

	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}

	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}
	return &msg, nil
}

// DecodePayload unmarshals the message payload into the given target.
func (m *Message) DecodePayload(target any) error {
	return json.Unmarshal(m.Payload, target)
}
