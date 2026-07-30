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

	// Pane input history
	MsgPaneHistoryReq       = "pane_history_req"
	MsgPaneHistoryResp      = "pane_history_resp"
	MsgPaneHistoryEntryReq  = "pane_history_entry_req"
	MsgPaneHistoryEntryResp = "pane_history_entry_resp"

	// Pane content search (M11 command palette)
	MsgPaneSearchReq  = "pane_search_req"
	MsgPaneSearchResp = "pane_search_resp"

	// Claude Code session discovery (pane setup dialog "resume" picker)
	MsgClaudeSessionsReq       = "claude_sessions_req"
	MsgClaudeSessionsResp      = "claude_sessions_resp"
	MsgClaudeSessionDetailReq  = "claude_session_detail_req"
	MsgClaudeSessionDetailResp = "claude_session_detail_resp"

	// Directory browsing (pane setup dialog CWD picker). The dialog used to
	// read the machine running the TUI, which in remote mode is the wrong disk.
	MsgBrowseDirReq  = "browse_dir_req"
	MsgBrowseDirResp = "browse_dir_resp"

	// Git repo discovery (Alt+G lazygit overlay, and the setup dialog's
	// discover = "git" pick list). Same reason as the browser: it used to stat
	// the TUI's own disk, so against a remote host it reported "no git repo
	// here" for a directory that is a repo on the machine that matters.
	MsgGitReposReq  = "git_repos_req"
	MsgGitReposResp = "git_repos_resp"

	// Auto-update (TUI ⇄ daemon)
	MsgStageUpdateReq  = "stage_update_req"  // TUI → daemon (empty payload)
	MsgStageUpdateResp = "stage_update_resp" // daemon → TUI (unicast)
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
	// ResumeSessionID resumes an existing Claude Code session instead of
	// starting a fresh one: the daemon spawns `claude --resume <id>` in place
	// of the preassign_id strategy's `--session-id <new-uuid>`. Empty (the
	// default) preserves the fresh-session behavior.
	//
	// Trust: like Overlay, any IPC client can set this. The daemon validates
	// it against the canonical UUID shape before it reaches argv, and also
	// refuses a session a live pane already holds — two claude processes on
	// one transcript overwrite each other's history. Either rejection falls
	// back to a fresh session rather than failing the spawn. The MCP bridge
	// deliberately does not expose this field.
	ResumeSessionID string `json:"resume_session_id,omitempty"`
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

// ContextTokensCompacting is the sentinel value for a pane's context-token
// count while a Claude compaction is in flight. The true post-compaction size
// is not knowable at PostCompact time — the compaction summary is written to
// the transcript as system/user entries with no assistant usage, so a read
// there would return the (now-stale) pre-compaction count. The daemon stores
// this sentinel on PostCompact and the TUI renders "<model> · compacting"
// until the next completed turn's Stop reports the real reduced size. It
// travels as the context_tokens value in both the hook-event data path and the
// workspace snapshot; the display convention lives in tui.modelStatusSegment.
const ContextTokensCompacting int64 = -1

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

// Pane input history payloads

// PaneHistoryReqPayload requests the input-history preview list for one pane.
type PaneHistoryReqPayload struct {
	PaneID string `json:"pane_id"`
}

// HistoryEntryMeta is one list row: a stable id (TsMs) and a single-line
// preview. The list renders exactly one row per entry, so the preview is
// flattened daemon-side (panehistory.PreviewLine) rather than shipped as the
// prompt's separate lines — the wire carries what is displayed, nothing more.
type HistoryEntryMeta struct {
	TsMs    int64  `json:"ts_ms"`
	Preview string `json:"preview"`
}

// PaneHistoryRespPayload carries the preview list, newest first.
type PaneHistoryRespPayload struct {
	PaneID  string             `json:"pane_id"`
	Entries []HistoryEntryMeta `json:"entries"`
}

// PaneHistoryEntryReqPayload requests one entry's full text by its TsMs id.
type PaneHistoryEntryReqPayload struct {
	PaneID string `json:"pane_id"`
	TsMs   int64  `json:"ts_ms"`
}

// PaneHistoryEntryRespPayload carries one entry's full text (Found=false if the
// id no longer exists, e.g. compacted away between list and fetch).
type PaneHistoryEntryRespPayload struct {
	PaneID string `json:"pane_id"`
	TsMs   int64  `json:"ts_ms"`
	Text   string `json:"text"`
	Found  bool   `json:"found"`
}

// PaneSearchReqPayload asks the daemon to scan every pane's scrollback for a
// literal, case-insensitive substring. Query is the palette query verbatim —
// content search runs inline with the command filter, so there is no sigil to
// strip; the daemon trims it only for matching and echoes it back unchanged.
type PaneSearchReqPayload struct {
	Query string `json:"query"`
}

// PaneSearchHit is one matching pane. The TUI resolves the display label itself
// from PaneID (it already holds tab/pane metadata), so the daemon returns only
// the id, the total match count, a single preview line, and whether THIS pane's
// count was capped (the per-hit flag is what the "capped" label renders from —
// the payload-level Truncated is only a "some pane was capped" summary).
type PaneSearchHit struct {
	PaneID    string `json:"pane_id"`
	Matches   int    `json:"matches"`
	Excerpt   string `json:"excerpt"`
	Truncated bool   `json:"truncated,omitempty"`
}

// PaneSearchRespPayload carries the hits for one search. Query echoes the
// request term VERBATIM (never trimmed — the TUI compares it against its own
// untrimmed term to drop responses that arrived after the user typed more).
// Truncated is set when any pane hit the per-pane match cap.
type PaneSearchRespPayload struct {
	Query     string          `json:"query"`
	Hits      []PaneSearchHit `json:"hits"`
	Truncated bool            `json:"truncated,omitempty"`
}

// ClaudeSessionsReqPayload asks the daemon to enumerate the Claude Code
// sessions recorded for CWD. CWD is the directory currently highlighted in the
// pane setup dialog — not yet committed, which is why the response echoes it
// back for staleness comparison.
type ClaudeSessionsReqPayload struct {
	CWD string `json:"cwd"`
}

// ClaudeSessionInfo is one resumable session. InUsePaneID identifies the live
// pane already attached to this session (empty when free) — two claude
// processes on one transcript would fight over it, so the TUI renders those
// rows blocked. Like PaneSearchHit, only the id travels: the TUI already holds
// tab/pane metadata and resolves the display label itself.
type ClaudeSessionInfo struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	ModifiedMs  int64  `json:"modified_ms"`
	InUsePaneID string `json:"in_use_pane_id,omitempty"`
}

// ClaudeSessionsRespPayload carries one directory's sessions, newest first.
// CWD echoes the request VERBATIM (never cleaned or resolved — the TUI compares
// it against its own value to drop responses that arrived after the user moved
// to a different directory; any daemon-side normalization would make a
// legitimate request look permanently stale). Truncated is set when the
// directory held more sessions than the discovery cap returns.
type ClaudeSessionsRespPayload struct {
	CWD       string              `json:"cwd"`
	Sessions  []ClaudeSessionInfo `json:"sessions"`
	Truncated bool                `json:"truncated,omitempty"`
	Error     string              `json:"error,omitempty"`
}

// BrowseDirReqPayload asks the daemon to list one directory. An empty Path
// means "wherever you would spawn a pane by default".
//
// Child descends: when set, the daemon lists the entry of that name inside
// Path. The client cannot do this join itself, and that is the point. Path
// separators are a property of the machine holding the filesystem, not of the
// one rendering the picker — a Windows TUI attached to a Linux daemon would
// build `C:\srv\work` shaped paths with filepath.Join and list nothing. The
// daemon joins with its own separator, so the client never has to know.
//
// Child is a single path element and is rejected if it contains a separator.
// Only the daemon can safely interpret one, so accepting it here would let a
// client smuggle traversal through a field documented as a leaf name.
type BrowseDirReqPayload struct {
	Path  string `json:"path"`
	Child string `json:"child,omitempty"`
}

// BrowseEntry is one child of a listed directory. Only the leaf name travels —
// the client already knows the parent it asked about, and repeating the full
// path on every entry would multiply the frame for nothing.
type BrowseEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
}

// BrowseDirRespPayload carries one directory listing, directories first.
//
// Path and Resolved are separate ON PURPOSE and must not be merged. Path echoes
// the request VERBATIM and is the client's staleness key — the browser fires a
// request per keystroke of navigation, so answers routinely arrive after the
// user has moved on, and the client drops any whose echo does not match where
// it is now. Resolved is the cleaned absolute path: the usable answer, what the
// dialog displays and ultimately commits. Collapsing the two would break the
// echo the first time the daemon cleaned a trailing separator, and the field
// would hang on its pending state until the timeout fired.
//
// Child echoes the request's Child for the same reason, so the staleness key is
// the whole request rather than half of it — two descents from one directory
// differ only in this field.
//
// Parent is the daemon's own answer for "one level up", never computed by the
// client, for the separator reason described on the request.
//
// Roots lists the filesystem roots, and is populated only when Resolved IS a
// root. On Unix a root has nothing above it, so it stays empty; on Windows it
// carries the available drive letters, which is what "up" from `C:\` offers.
//
// It exists because the client cannot enumerate them: the old browser walked
// A:\ to Z:\ with os.Stat under a runtime.GOOS check, and both halves describe
// the machine DRAWING the picker rather than the one holding the disk. Against
// a Linux daemon there are no drives at all, and against a Windows daemon the
// letters are the server's.
//
// Truncated reports that the directory held more than the listing cap.
type BrowseDirRespPayload struct {
	Path      string        `json:"path"`
	Child     string        `json:"child,omitempty"`
	Resolved  string        `json:"resolved,omitempty"`
	Parent    string        `json:"parent,omitempty"`
	Entries   []BrowseEntry `json:"entries,omitempty"`
	Roots     []string      `json:"roots,omitempty"`
	Truncated bool          `json:"truncated,omitempty"`
	Error     string        `json:"error,omitempty"`
}

// GitReposReqPayload asks the daemon which git repositories are near CWD —
// the enclosing repo plus one level of sub-repos. An empty CWD means the
// daemon's default.
type GitReposReqPayload struct {
	CWD string `json:"cwd"`
}

// GitReposRespPayload carries the discovered repositories, enclosing repo
// first.
//
// CWD echoes the request VERBATIM, the same staleness contract the browse and
// session listings use: the answer is only meaningful for the directory that
// was asked about, and the user may have moved on by the time it lands.
//
// An empty Repos with an empty Error is a real answer — "there is no repo
// here" — and is deliberately distinguishable from a failure, because the two
// produce different UI: the first flashes a finding, the second must not claim
// one.
type GitReposRespPayload struct {
	CWD   string   `json:"cwd"`
	Repos []string `json:"repos,omitempty"`
	Error string   `json:"error,omitempty"`
}

// ClaudeSessionDetailReqPayload asks for the deep read of ONE session — the
// listing head-reads every transcript in a directory, so this is issued per
// user request (the picker's info key), never per listing.
type ClaudeSessionDetailReqPayload struct {
	CWD       string `json:"cwd"`
	SessionID string `json:"session_id"`
}

// ClaudeSessionDetailRespPayload answers with one session's summary. CWD and
// SessionID echo the request VERBATIM for the same staleness contract
// ClaudeSessionsRespPayload documents — here the pair is what identifies which
// highlighted row the answer belongs to, since the user can keep moving the
// cursor while the read is in flight.
//
// StartedMs is 0 when no opening entry carried a timestamp. Prompts are
// multi-line: they render as paragraphs, not rows. UserPrompts counts only what
// the user typed — see claudesessions.Detail for why no assistant-side count is
// reported.
type ClaudeSessionDetailRespPayload struct {
	CWD         string `json:"cwd"`
	SessionID   string `json:"session_id"`
	FirstPrompt string `json:"first_prompt,omitempty"`
	LastPrompt  string `json:"last_prompt,omitempty"`
	UserPrompts int    `json:"user_prompts,omitempty"`
	StartedMs   int64  `json:"started_ms,omitempty"`
	ModifiedMs  int64  `json:"modified_ms,omitempty"`
	SizeBytes   int64  `json:"size_bytes,omitempty"`
	Error       string `json:"error,omitempty"`
}

// UpdateInfo rides the workspace_state broadcast under the "update" key
// when a newer release than the running daemon's version is known. Omitted
// entirely when up to date; old clients ignore the extra key.
type UpdateInfo struct {
	LatestVersion   string `json:"latest_version"`
	ReleaseURL      string `json:"release_url,omitempty"`
	StagedVersion   string `json:"staged_version,omitempty"` // set once fully staged
	InstallWritable bool   `json:"install_writable"`
}

// StageUpdateRespPayload answers MsgStageUpdateReq (About → Update now with
// nothing staged yet).
type StageUpdateRespPayload struct {
	Success bool   `json:"success"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
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
