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
	// MsgSubscribe lets a client narrow what the daemon broadcasts to it.
	// Optional in both directions: a client that never sends it receives
	// everything, exactly as before this message existed.
	MsgSubscribe = "subscribe"

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

	// Project lifecycle (mirrors the tab message set).
	MsgCreateProject  = "create_project"
	MsgDestroyProject = "destroy_project"
	MsgUpdateProject  = "update_project"
	MsgMergeProjects  = "merge_projects"
	MsgSwitchProject  = "switch_project"
	MsgReorderProject = "reorder_project"

	// MsgLinkLost is synthesised CLIENT-SIDE by the router when a connection
	// fails. It is never written to a socket.
	MsgLinkLost = "link_lost"

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

	// Overlay lifecycle (Client -> Daemon)
	// MsgOverlayPolicy pushes the client's overlay retention settings. The
	// daemon starts from its own config, but F1 → Settings edits only reach
	// disk on TUI exit, so without this a setting would not apply until the
	// next daemon start. Last writer wins across clients.
	MsgOverlayPolicy = "overlay_policy"

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

	// Git worktree discovery (pane setup dialog with a repository path).
	// Asks the daemon for the list of worktrees in the repository containing Path.
	MsgWorktreeListReq  = "worktree_list_req"
	MsgWorktreeListResp = "worktree_list_resp"

	// Worktree change count (close confirm dialog). Asks the daemon how much
	// uncommitted work a worktree holds, so the dialog can say what the force
	// removal it is offering would destroy.
	//
	// On demand rather than on the git ticker, and that is a cost decision:
	// `git status` is the one plumbing call gitinfo deliberately never makes,
	// because it can take seconds on a large repository without fsmonitor.
	// Once per dialog against one worktree is affordable; every five seconds
	// against every pane's checkout is not.
	MsgWorktreeStatusReq  = "worktree_status_req"
	MsgWorktreeStatusResp = "worktree_status_resp"

	// Recent-directory existence check (pane setup dialog's quick pick). The
	// list was filtered with a local os.Stat, so against a remote host every
	// server path failed the test and the pick list rendered silently empty —
	// indistinguishable from a feature that had never been used, because
	// structurally nothing had failed.
	MsgDirsExistReq  = "dirs_exist_req"
	MsgDirsExistResp = "dirs_exist_resp"

	// Auto-update (TUI ⇄ daemon)
	MsgStageUpdateReq  = "stage_update_req"  // TUI → daemon (empty payload)
	MsgStageUpdateResp = "stage_update_resp" // daemon → TUI (unicast)
	// Check-only refresh, fired when the About dialog opens. Deliberately has
	// NO response type: the answer is the refreshed "update" key on the next
	// workspace_state broadcast, and a check that fails (offline laptop) is a
	// routine non-event the row must not report. Rate-limited daemon-side.
	MsgUpdateCheckReq = "update_check_req" // TUI → daemon (empty payload)

	// Kube-context discovery (pane setup dialog, discover = "kube"). Same
	// reason as the browser and git discovery: it used to parse the
	// kubeconfig on the machine drawing the UI, so against a remote host it
	// offered the laptop's clusters and launched with a --context the server
	// may not have.
	MsgKubeCtxReq  = "kube_ctx_req"
	MsgKubeCtxResp = "kube_ctx_resp"

	// Plugin availability (Ctrl+N and its consumers: context menu, palette,
	// Alt+G overlay). Availability used to be detected only on the machine
	// drawing the UI, which is the wrong machine whenever the daemon is
	// remote — a tool installed only on the server was greyed out, and one
	// installed only locally was offered and then spawned as a fallback
	// terminal.
	MsgPluginListReq  = "plugin_list_req"
	MsgPluginListResp = "plugin_list_resp"
)

// Message is the wire format for IPC communication.
type Message struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"` // request-response correlation (MCP bridge)
	Payload json.RawMessage `json:"payload,omitempty"`
	// Origin names the daemon a message came from (set by the router on receive)
	// or is destined for (set by the Model on send). Client-side routing state
	// only: `json:"-"` keeps it off the wire, so adding it needs no protocol
	// version bump. Empty on receive means the local daemon; empty on send means
	// "resolve it" — see router.Send.
	Origin string `json:"-"`
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
	// Worktree asks the daemon to CREATE a linked worktree and spawn the pane
	// inside it, ignoring CWD. Nil (the default) is the ordinary synchronous
	// create every existing client makes.
	//
	// A POINTER rather than a value: nil is what keeps every other create —
	// MCP create_pane, the plugin dialog, restore — on the unchanged path with
	// no branch anywhere in the daemon, and it says "this create is different"
	// structurally rather than by a zero-value convention someone can forget.
	//
	// Trust: like Overlay and ResumeSessionID, any IPC client can set this.
	// The daemon validates the branch name against both the ref and the path
	// grammar before it reaches argv, and never passes --force. The MCP bridge
	// deliberately does not expose this field.
	Worktree *WorktreeSpec `json:"worktree,omitempty"`
}

// WorktreeSpec asks the daemon to create a linked worktree for a new pane.
// Create-time only — an instruction, not stored pane state; what persists is
// the resulting CWD, plus a flag saying the pane owns a worktree.
type WorktreeSpec struct {
	// RepoRoot is the repository the worktree branches from, as the DAEMON's
	// filesystem spells it. The client sends back the directory the daemon's
	// own browse answered with, so no path built on the client is involved.
	RepoRoot string `json:"repo_root"`
	// Branch is the NEW branch, off the repository's DEFAULT branch —
	// origin/HEAD where it is set, else the conventional names, else HEAD.
	// Deliberately not the repository's current HEAD, which is whatever the
	// main checkout was last left on: a worktree created while it sat on a
	// feature branch inherited that feature's unmerged commits, so the pane
	// was isolated in its directory and not in its history.
	//
	// The base is resolved DAEMON-side (gitworktree.defaultBranch) and is not
	// on the wire. Nothing chooses it yet — when something does, it belongs
	// here as a field, not as a second thing the client infers about a
	// repository living on the daemon's disk.
	//
	// Existing branches are deliberately not offered: one already checked out
	// in another worktree fails at the git level and needs its own error path,
	// and attaching to that worktree — which stage A ships — covers the real
	// case anyway.
	Branch string `json:"branch"`
}

type DestroyPanePayload struct {
	PaneID string `json:"pane_id"`
	// RemoveWorktree asks the daemon to delete the linked worktree this pane
	// was created into, once the pane itself is gone.
	//
	// A BOOL, never a path, and that is the security boundary rather than a
	// convenience: the daemon re-derives which directory may go from its own
	// Pane.WorktreeOwned record, so the only directories reachable through this
	// field are ones this daemon created itself. A path on the wire would be a
	// recursive-delete primitive any IPC client could aim anywhere.
	//
	// Absent means false means today's behaviour, which is what keeps every
	// existing producer — the MCP destroy_pane tool, the overlay teardown, an
	// older client — non-destructive without knowing this field exists.
	RemoveWorktree bool `json:"remove_worktree,omitempty"`
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

// SubscribePayload narrows what a client is sent.
//
// PaneOutput is a POINTER so "field absent" and "explicitly false" are
// distinguishable: an omitted field leaves the current setting alone, which
// keeps the message extensible without every future sender having to restate
// every flag. Nothing is opted out by default.
type SubscribePayload struct {
	PaneOutput *bool `json:"pane_output,omitempty"`
}

type CreateTabPayload struct {
	Name string `json:"name"`
	// FirstPane names the pane the new tab opens with. Nil (the default) keeps
	// the historical behavior — a `terminal` pane rooted at the owning project's
	// directory — which is what every non-interactive producer of this message
	// needs: an older client, the attach bootstrap's shape, and any future
	// caller with no opinion. The TUI sets it from the create-pane dialog so a
	// tab and its first pane are ONE atomic step, with no create-then-replace
	// and no window where the wrong pane type is on screen.
	FirstPane *FirstPaneSpec `json:"first_pane,omitempty"`
}

// FirstPaneSpec is the subset of a pane request that makes sense for a tab that
// does not exist yet.
//
// Deliberately NOT a *CreatePanePayload, though it carries a subset of the same
// fields. That type also has TabID (meaningless — the daemon owns the id it is
// about to mint), ReplacePaneID (there is nothing to replace, and honoring one
// would destroy an arbitrary pane as a side effect of "create tab") and Overlay
// (a tab whose only pane is a muted overlay is a state ensureTabNotEmpty reads
// as empty and no create path repairs). Any IPC client can set these fields, so
// the guarantee is structural rather than a sanitizing branch someone can drop
// later — the same reason MergeProjectsPayload has no RootDir.
type FirstPaneSpec struct {
	Type         string   `json:"type,omitempty"`
	CWD          string   `json:"cwd,omitempty"`
	InstanceName string   `json:"instance_name,omitempty"`
	InstanceArgs []string `json:"instance_args,omitempty"`
	// ResumeSessionID and Worktree carry the same meaning, and the same trust
	// model, as their CreatePanePayload counterparts — the daemon validates both
	// before either reaches argv.
	ResumeSessionID string        `json:"resume_session_id,omitempty"`
	Worktree        *WorktreeSpec `json:"worktree,omitempty"`
}

type DestroyTabPayload struct {
	TabID string `json:"tab_id"`
	// RemoveWorktree deletes the linked worktrees of every pane in the tab that
	// owns one. A tab is closed as a unit, so its worktrees are too — see
	// DestroyPanePayload.RemoveWorktree for why this is a bool rather than a
	// list of paths.
	RemoveWorktree bool `json:"remove_worktree,omitempty"`
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

type CreateProjectPayload struct {
	Name    string `json:"name"`
	RootDir string `json:"root_dir"`
}

type DestroyProjectPayload struct {
	ProjectID string `json:"project_id"`
}

type UpdateProjectPayload struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	RootDir   string `json:"root_dir"`
	// AdoptBootstrap makes the update conditional: apply it only while the
	// project is still one the daemon invented. Set by the client's adopt path,
	// where naming a project on a host RENAMES the host's unnamed one — two
	// clients adopting the same host would otherwise each rename the other's
	// freshly named project, since the Bootstrap check lives in each client's
	// own snapshot. Omitted (false) means an ordinary rename, which always
	// applies. omitempty so an older daemon sees the same wire shape it did.
	AdoptBootstrap bool `json:"adopt_bootstrap,omitempty"`
}

// MergeProjectsPayload folds the Absorb projects' tabs into ProjectID and
// drops the emptied records, then renames the survivor to Name. Tabs and panes
// are never destroyed — that is the whole difference from DestroyProject, and
// the reason a user could not consolidate a host by hand.
//
// Absorb is an explicit list rather than "every other project on that daemon":
// the one-project-per-host rule is the CLIENT's (Project has no Dest field), so
// a daemon-side "fold everything" would be wrong on the local machine, where
// several projects are expected.
//
// There is deliberately NO RootDir. A fold renames and absorbs; it does not
// relocate. The survivor already has a root somebody chose, while the form field
// that would supply one holds — in the ordinary case — whatever the dialog's own
// opening browse resolved, since that request carries an empty path and the
// daemon answers with its default CWD. Carrying it would overwrite a deliberate
// value with an artifact on nearly every fold. Changing a project's root is what
// MsgUpdateProject is for, from a dialog seeded with the project's own.
type MergeProjectsPayload struct {
	ProjectID string   `json:"project_id"`
	Absorb    []string `json:"absorb"`
	Name      string   `json:"name"`
}

type SwitchProjectPayload struct {
	ProjectID string `json:"project_id"`
}

type ReorderProjectPayload struct {
	ProjectID string `json:"project_id"`
	NewIndex  int    `json:"new_index"`
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
	// PinnedAttention is a pointer for the same reason, and here the tri-state
	// is what makes UNPINNING expressible at all: the pin is a toggle whose
	// off-state is the one the user asks for explicitly, so a plain bool would
	// make "unmark attention" indistinguishable from "rename this pane" and
	// every OSC 7 CWD update would silently clear the mark.
	PinnedAttention *bool `json:"pinned_attention,omitempty"`
	// OverlayVisible reports whether the TUI is currently SHOWING this overlay
	// pane. Pointer for the same tri-state reason as Muted: this is a partial
	// update handler, so a plain bool would report every rename and every OSC 7
	// CWD change as "hidden" and hand the idle sweep a pane the user is looking
	// at. Visibility is client state the daemon cannot observe, and the daemon
	// needs it because an idle lazygit emits nothing whether shown or not.
	OverlayVisible *bool `json:"overlay_visible,omitempty"`
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

// OverlayPolicyPayload carries the overlay retention settings. Both fields use
// 0 for "disabled", matching config.OverlayConfig.
type OverlayPolicyPayload struct {
	IdleTimeoutMinutes int `json:"idle_timeout_minutes"`
	MaxLive            int `json:"max_live"`
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
	// Error explains a create that produced NO pane. Only a create carrying a
	// WorktreeSpec can fail this way — an ordinary create is synchronous and
	// its result arrives in the next workspace broadcast, as it always has.
	//
	// It carries git's own stderr where there is any: "already used by
	// worktree '/x/feat-y'" names the pane to go look at, and no message Quil
	// could invent would.
	Error string `json:"error,omitempty"`
	// Swapped reports whether a REPLACE actually removed the pane named by
	// ReplacePaneID. It is a statement about what happened, unlike Worktree
	// below, and it exists because the two are not implied by Error.
	//
	// A worktree-backed replace creates the worktree BEFORE the swap, so an add
	// that fails leaves the pane alive and the client must put it back. But the
	// swap itself happens before the new pane's PTY is spawned — so a spawn
	// failure reports an error with the old pane already destroyed, and a
	// client that inferred "error means untouched" would restore a pane the
	// daemon no longer has. Keystrokes then route to a pane id that does not
	// exist until the next broadcast prunes the leaf.
	//
	// omitempty: absent means false, which is the correct reading for every
	// non-replace response and for an older daemon that does not send it.
	Swapped bool `json:"swapped,omitempty"`
	// Worktree echoes the request's spec VERBATIM on every path, including the
	// error one. It is the client's staleness key, not a statement about what
	// was created — the client armed a layout placeholder before the send and
	// nothing else will unwind it.
	Worktree *WorktreeSpec `json:"worktree,omitempty"`
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
//
// RootsTruncated is the same statement about Roots, and is deliberately a
// SECOND flag rather than a reuse of Truncated. The two are independent: the
// drive sweep can give up on unresponsive mappings while the directory read
// that follows it succeeds completely, and the client shows the roots AS the
// listing once the user navigates up — so one flag would either claim the file
// list was capped when it was not, or let a short drive list pass for a
// complete one.
type BrowseDirRespPayload struct {
	Path           string        `json:"path"`
	Child          string        `json:"child,omitempty"`
	Resolved       string        `json:"resolved,omitempty"`
	Parent         string        `json:"parent,omitempty"`
	Entries        []BrowseEntry `json:"entries,omitempty"`
	Roots          []string      `json:"roots,omitempty"`
	Truncated      bool          `json:"truncated,omitempty"`
	RootsTruncated bool          `json:"roots_truncated,omitempty"`
	Error          string        `json:"error,omitempty"`
}

// DirsExistReqPayload asks the daemon which of Paths still resolve to
// directories on ITS filesystem.
type DirsExistReqPayload struct {
	Paths []string `json:"paths"`
}

// DirsExistRespPayload carries the surviving directories.
//
// Paths is the subset of the request that resolved to a directory, in the
// request's order. It is deliberately NOT an echo of the request, so it cannot
// serve as a staleness key the way BrowseDirRespPayload.Path does — correlation
// is by the per-request generation in Message.ID instead, because a path LIST is
// a poor key: two requests differing only in order would compare equal under any
// cheap comparison, and comparing them properly costs more than the generation.
//
// An empty Paths with an empty Error is a real answer — "none of these exist any
// more" — and must stay distinguishable from a failure, because only one of the
// two justifies telling the user their remembered directories are gone.
type DirsExistRespPayload struct {
	Paths []string `json:"paths,omitempty"`
	Error string   `json:"error,omitempty"`
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

// WorktreeListReqPayload asks which git worktrees belong to the repository
// containing Path. An empty Path means the daemon's default directory.
type WorktreeListReqPayload struct {
	Path string `json:"path"`
}

// WorktreeInfo is one entry of the repository's worktree list, as the daemon
// sees it. A mirror of gitworktree.Worktree rather than a reuse of it: this is
// a wire type, and the internal one is free to change shape.
type WorktreeInfo struct {
	Path     string `json:"path"`
	Branch   string `json:"branch,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	Main     bool   `json:"main,omitempty"`
	Locked   bool   `json:"locked,omitempty"`
	Prunable bool   `json:"prunable,omitempty"`
	Bare     bool   `json:"bare,omitempty"`
}

// WorktreeListRespPayload carries the repository's worktrees, main checkout
// first.
//
// CONTRACT: Path echoes the request VERBATIM on every path, including the
// error and single-flight-rejection ones. It is the client's staleness key,
// not a statement about what was read — normalising it daemon-side would make
// a live request look permanently stale.
//
// Repo false with an empty Error is a real answer ("this is not a repository")
// and must stay distinguishable from a failure: only one of the two justifies
// telling the user there is no repository here.
//
// WorktreeRoot is the directory NEW worktrees would go in, already joined by
// the daemon with the daemon's own separators. The client must never compute
// it: doing so means running filepath.Dir/Join with the CLIENT's separators
// over a path that lives on the daemon's machine. Unused by stage A beyond
// display, and present now so the contract does not change under stage B.
type WorktreeListRespPayload struct {
	Path         string         `json:"path"`
	Repo         bool           `json:"repo,omitempty"`
	Root         string         `json:"root,omitempty"`
	WorktreeRoot string         `json:"worktree_root,omitempty"`
	Worktrees    []WorktreeInfo `json:"worktrees,omitempty"`
	Error        string         `json:"error,omitempty"`
}

// WorktreeStatusReqPayload asks how much uncommitted work each of these
// worktrees holds. The close confirm dialog sends one request covering every
// worktree the close would delete, so a tab with several is one round trip.
type WorktreeStatusReqPayload struct {
	Paths []string `json:"paths"`
}

// WorktreeStatus is one worktree's answer.
//
// Changes counts what `git status --porcelain` reports — modified tracked files
// and untracked entries alike, because a force removal destroys both. An
// untracked DIRECTORY counts once rather than once per file, which is why the
// dialog says "uncommitted changes" rather than naming a file count.
//
// Changes == 0 with an empty Error is the only thing that means CLEAN. A path
// that could not be read carries Error and must never be rendered as clean: a
// zero there would invite the toggle on the strength of a number nobody
// obtained.
type WorktreeStatus struct {
	Path    string `json:"path"`
	Changes int    `json:"changes,omitempty"`
	Error   string `json:"error,omitempty"`
}

// WorktreeStatusRespPayload answers a status request.
//
// CONTRACT: Paths echoes the request VERBATIM on every path, including the
// error and single-flight-rejection ones — the client's staleness key, matching
// WorktreeListRespPayload.Path and every other pair in this protocol.
//
// Error is the whole-request failure (an oversized request); a per-worktree
// failure rides its own WorktreeStatus, because one unreadable checkout must
// not take the other rows' answers with it.
type WorktreeStatusRespPayload struct {
	Paths    []string         `json:"paths"`
	Statuses []WorktreeStatus `json:"statuses,omitempty"`
	Error    string           `json:"error,omitempty"`
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

// StageUpdateRespPayload answers MsgStageUpdateReq (About → Update now).
//
// AlreadyStaged distinguishes "the latest release is on disk, nothing was
// downloaded" from "it was downloaded just now". The request re-checks GitHub
// on EVERY press — including when the client believes a version is already
// staged, because that belief comes from a broadcast the daemon refreshes
// daily — so without this flag the answer to "is my stage still the latest?"
// would cost a redundant ~15 MB download every time it was yes.
//
// Success is true in both cases (the latest IS staged when the call returns),
// so a client that predates the flag still reads the outcome correctly.
//
// CheckFailed narrows what a failure licenses. A client holding an apply intent
// may fall back to installing what is already staged ONLY when the release
// check could not be MADE — GitHub unreachable — because then "is this still
// the newest?" is unanswered rather than answered no. Every other error
// (staging failed, install dir not writable, a check already running) leaves
// the question answered or the request unperformed, and must not be read as
// permission to install an older stage: doing so re-creates the very
// apply-the-intermediate-version loop this pair exists to end.
type StageUpdateRespPayload struct {
	Success       bool   `json:"success"`
	AlreadyStaged bool   `json:"already_staged,omitempty"`
	CheckFailed   bool   `json:"check_failed,omitempty"`
	Version       string `json:"version,omitempty"`
	Error         string `json:"error,omitempty"`
}

// KubeCtxReqPayload is deliberately empty: kube-context discovery is
// CWD-independent, so there is no content key that could go stale. The
// per-request generation in Message.ID is the whole correlator.
type KubeCtxReqPayload struct{}

// KubeContextInfo is one context enumerated from the daemon's kubeconfig.
// Current is carried per entry rather than as a top-level name, matching
// kubediscover.Context — the setup dialog draws ● from this field directly.
type KubeContextInfo struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Current   bool   `json:"current,omitempty"`
}

// KubeCtxRespPayload carries the discovered kube contexts.
//
// An empty Contexts with an empty Error is a real answer — "no kubeconfig
// here" — deliberately distinguishable from a failure: only one of the two
// justifies telling the user there are no contexts. Truncated is set when the
// daemon capped the list at maxKubeContexts.
type KubeCtxRespPayload struct {
	Contexts  []KubeContextInfo `json:"contexts,omitempty"`
	Truncated bool              `json:"truncated,omitempty"`
	Error     string            `json:"error,omitempty"`
}

// PluginListReqPayload is deliberately empty: the answer is "the daemon's
// whole registry", not scoped to any request-supplied key.
type PluginListReqPayload struct{}

// PluginInfo is one plugin's availability as the daemon sees it.
//
// No Homepage field: a greyed row already links out via the LOCAL plugin
// definition's own Homepage, which points at the same URL either machine
// would give. The field would only matter for a plugin the TUI does not
// define, which it cannot render at all — so it is dropped rather than
// carried unused.
type PluginInfo struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
}

// PluginListRespPayload carries the daemon's own registry. Deliberately no
// generation field: every response describes the same daemon and applying it
// is idempotent, so a late answer says exactly what a fresh one would.
type PluginListRespPayload struct {
	Plugins []PluginInfo `json:"plugins,omitempty"`
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
// Tries appendEnvelope first, which builds the same bytes by concatenation and
// skips encoding/json's redundant pass over the already-encoded payload. Any
// shape it declines falls through to EncodeFrameSlow, which remains the sole
// definition of correct output — the fast path is measured against it in tests.
func EncodeFrame(msg *Message) ([]byte, error) {
	if frame, ok := appendEnvelope(msg); ok {
		return frame, nil
	}
	return EncodeFrameSlow(msg)
}

// EncodeFrameSlow is the reference encoder: plain json.Marshal plus the length
// prefix. Exported so the fast path can be differentially tested against it,
// and kept as the fallback for every shape appendEnvelope declines.
func EncodeFrameSlow(msg *Message) ([]byte, error) {
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

	// data is freshly allocated above and never reused. That is load-bearing:
	// parseEnvelope's Payload ALIASES it rather than copying, so a pooled or
	// reused read buffer here would become a use-after-reuse bug surfacing as
	// intermittently corrupted payloads.
	var msg Message
	if parseEnvelope(data, &msg) {
		return &msg, nil
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}
	return &msg, nil
}

// DecodePayload unmarshals the message payload into the given target.
//
// pane_output is special-cased because it is the only high-frequency type: at
// up to 500 frames/s/pane, running the JSON scanner over ~11 KB of base64 to
// reach one []byte field cost ~90 us a frame. The fast path lives inside this
// method rather than in a new exported one so that no call site changes, and it
// declines to the same json.Unmarshal below for every shape it does not
// recognise — including the error cases, so callers keep seeing exactly the
// errors encoding/json produces.
func (m *Message) DecodePayload(target any) error {
	if out, ok := target.(*PaneOutputPayload); ok && decodePaneOutput(m.Payload, out) {
		return nil
	}
	return json.Unmarshal(m.Payload, target)
}
