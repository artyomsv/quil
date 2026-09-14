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
	MsgCreateTab  = "create_tab"
	MsgDestroyTab = "destroy_tab"
	MsgSwitchTab  = "switch_tab"
	MsgUpdateTab  = "update_tab"
	MsgReorderTab = "reorder_tab"

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
	MsgPaneInput = "pane_input"
	// MsgPaneInputResp answers a pane_input that carried an ID.
	//
	// ONLY id-bearing requests get one. The TUI sends pane_input for every
	// keystroke and sets no ID, so it is unaffected — which matters, because a
	// response per keystroke would be a frame per keystroke on that client's
	// 64-slot must-deliver queue.
	MsgPaneInputResp = "pane_input_resp"
	MsgPaneOutput    = "pane_output"

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
	MsgSetActivePane      = "set_active_pane" // broadcast to TUI
	MsgCloseTUI           = "close_tui"       // broadcast to TUI
	MsgHighlightPane      = "highlight_pane"  // broadcast to TUI (MCP interaction indicator)

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
	//
	// Retained permanently, not deprecated. Three consumers ride this pair —
	// MCP's get_memory_report and get_pane_memory, plus `quil status` — and
	// migrating an MCP tool's stable output shape for tidiness is not worth
	// it. The resource pair below is a superset for the TUI's own use.
	MsgMemoryReportReq  = "memory_report_req"
	MsgMemoryReportResp = "memory_report_resp"

	// Resource reporting — the process dialog and the status-bar total.
	//
	// Separate from the memory pair because it carries per-pane process TREES,
	// which the status bar does not need and must not pay for. See
	// ResourceReportReqPayload.WithTrees.
	MsgResourceReportReq  = "resource_report_req"
	MsgResourceReportResp = "resource_report_resp"

	// ClientHello — a durable client stating its own identity.
	//
	// Fire and forget, no response. Sent only by clients that intend to be
	// listed as running quil processes (the TUI and MCP bridges), never from
	// ipc.NewClient itself: most dial sites are short-lived probes that dial,
	// ask one thing and close, and registering those would populate the dialog
	// with processes that no longer exist. Same distinction the daemon already
	// draws between an ATTACHED client and a CONNECTED conn.
	MsgClientHello = "client_hello"

	// MsgClientStat is a durable client reporting its OWN cpu and rss, pushed
	// on a tick rather than sent once like the hello.
	//
	// It is a push, not a request-response, for the same reason the hello is
	// fire-and-forget: the daemon must never block a report on a client that
	// has stopped reading, and the client whose numbers matter most is
	// precisely the one that might be wedged. A missed push ages out and
	// renders as unknown, which is the honest answer.
	//
	// Self-reported rather than read from the OS process table, matching
	// MsgClientHello — the daemon cannot see a remote client's process at all,
	// and inferring quil's own processes from the local table is the mistake
	// the previous version of this section made.
	MsgClientStat = "client_stat"

	// Process kill — the dialog asking the daemon to stop a pane descendant.
	//
	// The daemon owns this decision entirely: it re-enumerates, re-derives the
	// pane's tree and re-checks the target before signalling anything. The
	// request is a proposal, never an instruction.
	MsgKillProcessReq  = "kill_process_req"
	MsgKillProcessResp = "kill_process_resp"

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

	// Docker sandbox capability (pane setup dialog). Asks the daemon whether
	// IT can run a sandbox pane, because the container runs on the daemon's
	// machine and the client may be a laptop attached to a remote host.
	//
	// Its own single-flight slot, like every other pair here: the dialog
	// asks this while it is also browsing and listing worktrees, so a shared
	// guard would make each step fail exactly when it followed another.
	MsgSandboxCapReq  = "sandbox_cap_req"
	MsgSandboxCapResp = "sandbox_cap_resp"

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

	// MCP project and tab management. The six project mutations above are
	// fire-and-forget for the TUI; when a request carries an ID (only the MCP
	// bridge sets one) the daemon answers with MsgProjectOpResp / MsgTabOpResp
	// / MsgPaneOpResp so an agent learns whether the operation applied. The
	// TUI never sets an ID, so nothing changes for it.
	MsgListProjectsReq   = "list_projects_req"
	MsgListProjectsResp  = "list_projects_resp"
	MsgCreateProjectReq  = "create_project_req"
	MsgCreateProjectResp = "create_project_resp"
	MsgProjectOpResp     = "project_op_resp"
	MsgTabOpResp         = "tab_op_resp"
	MsgPaneOpResp        = "pane_op_resp"
	MsgCreateTabReq      = "create_tab_req"
	MsgCreateTabResp     = "create_tab_resp"
	// MsgPluginCatalogReq lists every plugin with the options its setup dialog
	// offers (toggles, cwd prompt, resume support), so an MCP agent can learn
	// what create_pane accepts. Separate from MsgPluginListReq, whose contract
	// is availability-only and remote-mode scoped.
	MsgPluginCatalogReq  = "plugin_catalog_req"
	MsgPluginCatalogResp = "plugin_catalog_resp"

	// Agent tasking: one pane asks another for work and hears when it is done.
	MsgDelegateTaskReq  = "delegate_task_req"
	MsgDelegateTaskResp = "delegate_task_resp"
	MsgGetTaskReq       = "get_task_req"
	MsgGetTaskResp      = "get_task_resp"
	MsgWaitTaskReq      = "wait_task_req"
	MsgWaitTaskResp     = "wait_task_resp"
	MsgListTasksReq     = "list_tasks_req"
	MsgListTasksResp    = "list_tasks_resp"
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
	QuilMCP       bool     `json:"quil_mcp,omitempty"`
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
	// Sandbox asks the daemon to run this pane's process inside a Docker
	// container with its checkout bind-mounted, instead of on the host.
	//
	// A POINTER for the same reason Worktree is: nil keeps every existing
	// producer — MCP create_pane, restore, the plugin dialog — on the
	// unchanged path with no branch anywhere in the daemon, and it says "this
	// create is different" structurally rather than by a zero value someone
	// can forget.
	//
	// Trust: like Overlay, ResumeSessionID and Worktree, any IPC client can
	// set this. Image is the only thing the user controls and the daemon
	// validates it against a conservative grammar before it reaches argv;
	// nothing else about the container — mounts, flags, environment — is
	// reachable from the wire. The MCP bridge deliberately does not expose
	// the field.
	Sandbox *SandboxSpec `json:"sandbox,omitempty"`
}

// SandboxSpec asks the daemon to run a pane inside a container.
//
// One field, deliberately. Every other property of the container is derived
// by the daemon from the pane's own checkout, and a wire field for any of them
// — a mount, a flag, a user — would be a way to reach past the sandbox from
// the outside, which is the one thing this feature exists to prevent.
type SandboxSpec struct {
	// Image is the container image, supplied by the user.
	//
	// Quil publishes no image and ships no default: running Claude Code
	// inside a vendor's own image triggers the Commercial Terms conditions
	// for "preinstalling or running Claude Code in your products or
	// services", while a user-supplied image means Quil pre-installs nothing
	// and that section never applies.
	Image string `json:"image"`

	// Auth is the sign-in mode for THIS pane: "token" or "browser". Empty
	// follows [sandbox] auth, so an older client and every restore path keep
	// the configured behaviour.
	//
	// Per-pane because the trade is per-pane, and both halves were measured: a
	// token pane needs no sign-in at all but authenticates as "Claude API",
	// where Fable is absent from /model and Remote Control reports the login
	// expired; a browser pane signs in inside the container and gets the full
	// subscription. Neither is the right answer for every pane.
	//
	// The daemon validates it. An unknown value is refused rather than
	// guessed: the two modes hand the container different credentials, and
	// guessing wrong is either a pane that cannot authenticate or one that
	// silently loses the model the user picked it for.
	Auth string `json:"auth,omitempty"`
}

// WorktreeSpec asks the daemon to create a linked worktree for a new pane.
// Create-time only — an instruction, not stored pane state; what persists is
// the resulting CWD, plus a flag saying the pane owns a worktree.
type WorktreeSpec struct {
	// Subdir is the pane's relative working directory within the new checkout.
	// Empty keeps the worktree-root spawn used by existing callers.
	Subdir string `json:"subdir,omitempty"`
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

// PaneInputRespPayload says whether input actually reached a pane's process.
//
// It exists because "the daemon accepted the message" and "the child received
// the bytes" are different facts, and the MCP send_to_pane tool was reporting
// the first as the second: a pane with no PTY — a worktree placeholder, or one
// whose spawn failed — dropped the input silently while the tool answered
// "Sent N bytes".
//
// Delivered means the bytes are on the pane's input queue, which is as far as
// any caller can be told synchronously: the per-pane writer goroutine owns the
// PTY write, precisely so a child that has stopped reading its stdin cannot
// block the dispatch goroutine. Queue overflow is reported as NOT delivered.
type PaneInputRespPayload struct {
	PaneID    string `json:"pane_id"`
	Delivered bool   `json:"delivered"`
	Error     string `json:"error,omitempty"`
}

type PaneOutputPayload struct {
	PaneID string `json:"pane_id"`
	Data   []byte `json:"data"`
	Ghost  bool   `json:"ghost,omitempty"`
	// Generation identifies the PTY run. Repeated on every live chunk so a
	// dropped output frame cannot lose the reset between two child processes.
	Generation uint64 `json:"generation,omitempty"`
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
	// ProjectID files the tab under a project other than the active one.
	// Empty keeps the historical behaviour (the active project), which is
	// what every existing producer sends.
	ProjectID string `json:"project_id,omitempty"`
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
	// Sandbox is here for the same reason Worktree is, and its absence was a
	// silent isolation failure: the setup dialog's own designed flow — new
	// tab, worktree chosen, sandbox on — reaches the daemon through THIS
	// type, and handleCreateTab hand-copies a fixed field list into the
	// create it builds. A spec that stops at CreatePanePayload therefore
	// produces a tab whose agent runs on the HOST, in a fresh worktree, with
	// no container and no error. Any field added here must also be added to
	// that copy in createFirstPaneWorktree.
	Sandbox *SandboxSpec `json:"sandbox,omitempty"`
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
	// MarkedForDeletion is a pointer for the same reason as PinnedAttention,
	// and the tri-state is load-bearing twice over here. Unmarking is the
	// off-state the user asks for explicitly, so a plain bool could not express
	// it; and the panes this mark lands on are the ones still emitting OSC 7
	// CWD updates from a background job, so a plain bool would clear the mark
	// on the next `cd` the shell reports.
	MarkedForDeletion *bool `json:"marked_for_deletion,omitempty"`
	// Unseen is the TUI's "work finished while you were not looking" mark,
	// reported here so it outlives the TUI process. The TUI derives it (only
	// the client knows what the user was looking at) and reports every set
	// and clear; the daemon keeps a copy for the snapshot and hands it back on
	// attach. Pointer for the same tri-state reason as the others: the clear
	// is the direction that has to be expressible, and the handler is partial.
	Unseen *bool `json:"unseen,omitempty"`
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
	// PreparingWorktree names the branch a `git worktree add` is checking out
	// for the pane that will REPLACE this one. Non-empty means the pane has no
	// process ON PURPOSE — and without it, "running: false, pending: false,
	// exit_code: null" is indistinguishable from a pane whose process died, so
	// an agent reads a placeholder as a corpse and calls restart_pane on it.
	PreparingWorktree string `json:"preparing_worktree,omitempty"`
	// ProjectID names the project the pane's tab belongs to.
	ProjectID string `json:"project_id,omitempty"`
	// AgentState is the daemon's replay of the pane's hook edges: "working",
	// "blocked", "idle", or empty when no hook edge has been seen (a terminal
	// pane, or an AI pane whose hooks never loaded). Empty is NOT idle: an
	// agent deciding whether another pane is free must treat it as unknown.
	AgentState string `json:"agent_state,omitempty"`
	// BlockedReason names the tool a permission prompt was raised for, when
	// the producer said. Only meaningful while AgentState is "blocked".
	BlockedReason string `json:"blocked_reason,omitempty"`
	// LastIdleAt is when the pane last fell idle, Unix ms; 0 if never.
	LastIdleAt int64 `json:"last_idle_at,omitempty"`
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
	// PreparingWorktree: see PaneInfo. A pane waiting on a checkout is not a
	// dead pane, and this is the only field that says so.
	PreparingWorktree string `json:"preparing_worktree,omitempty"`
	// ProjectID, AgentState, BlockedReason, LastIdleAt: see PaneInfo.
	ProjectID     string `json:"project_id,omitempty"`
	AgentState    string `json:"agent_state,omitempty"`
	BlockedReason string `json:"blocked_reason,omitempty"`
	LastIdleAt    int64  `json:"last_idle_at,omitempty"`
}

type CreatePaneReqPayload struct {
	TabID        string   `json:"tab_id,omitempty"`
	CWD          string   `json:"cwd,omitempty"`
	Type         string   `json:"type,omitempty"`
	InstanceName string   `json:"instance_name,omitempty"`
	InstanceArgs []string `json:"instance_args,omitempty"`
	// Name labels the pane, as Alt+F2 would.
	Name string `json:"name,omitempty"`
	// Toggles are plugin toggle NAMES (see PluginToggleInfo), resolved to
	// their ArgsWhenOn by the daemon. Never raw args: InstanceArgs REPLACE
	// the plugin's command args, so a free-form list would let any IPC
	// client run any program under the plugin's name.
	Toggles []string `json:"toggles,omitempty"`
	// ResumeSessionID: see CreatePanePayload. Validated daemon-side.
	ResumeSessionID string `json:"resume_session_id,omitempty"`
	// WorktreeBranch asks for a NEW linked worktree on this branch, off the
	// repository containing CWD, with the pane spawned inside it. The repo
	// root is resolved by the daemon — never sent — because a client-built
	// path is how a worktree ends up nested inside a checkout.
	WorktreeBranch string `json:"worktree_branch,omitempty"`
	// Sandbox: see CreatePanePayload. Validated daemon-side.
	Sandbox *SandboxSpec `json:"sandbox,omitempty"`
}

type CreatePaneRespPayload struct {
	// InvalidSubdir is worker-local failure classification, never sent over IPC.
	// Template creation uses it to discard its provisional tab after checkout.
	InvalidSubdir bool   `json:"-"`
	PaneID        string `json:"pane_id"`
	TabID         string `json:"tab_id"`
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
	// ProjectID names the owning project.
	ProjectID string `json:"project_id,omitempty"`
}

// ListTabsReqPayload is optional: a request with no payload lists every tab.
type ListTabsReqPayload struct {
	ProjectID string `json:"project_id,omitempty"`
}

type ListTabsRespPayload struct {
	Tabs []TabInfo `json:"tabs"`
}

// ProjectInfo is the MCP view of a project.
type ProjectInfo struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	RootDir   string   `json:"root_dir"`
	Active    bool     `json:"active"`
	Bootstrap bool     `json:"bootstrap,omitempty"`
	TabIDs    []string `json:"tab_ids"`
	ActiveTab string   `json:"active_tab,omitempty"`
}

type ListProjectsRespPayload struct {
	Projects      []ProjectInfo `json:"projects"`
	ActiveProject string        `json:"active_project,omitempty"`
}

// CreateProjectReqPayload mirrors CreateProjectPayload; the separate type is
// the request-response contract, not a different shape.
type CreateProjectReqPayload struct {
	Name    string `json:"name"`
	RootDir string `json:"root_dir"`
}

type CreateProjectRespPayload struct {
	ProjectID string `json:"project_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Error     string `json:"error,omitempty"`
}

// OpRespPayload answers an ID-bearing mutation that has no richer response:
// ID echoes the object the request named, OK says whether it applied, Error
// says why not.
type OpRespPayload struct {
	ID    string `json:"id,omitempty"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// CreateTabReqPayload creates a tab in a chosen project with a chosen first
// pane, and ANSWERS with both ids. CreateTabPayload (the TUI's) is a
// broadcast-answered message and files the tab under the active project.
type CreateTabReqPayload struct {
	Name      string `json:"name,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	// FirstPane names the pane the tab opens with; nil is a terminal in the
	// project root. TabID inside it is ignored (the daemon mints the tab).
	FirstPane *CreatePaneReqPayload `json:"first_pane,omitempty"`
}

type CreateTabRespPayload struct {
	TabID  string `json:"tab_id,omitempty"`
	PaneID string `json:"pane_id,omitempty"`
	// PreparingWorktree is set when the first pane is a worktree placeholder:
	// PaneID names the placeholder, and the pane that replaces it carries a
	// NEW id, announced by the worktree_ready event.
	PreparingWorktree string `json:"preparing_worktree,omitempty"`
	Error             string `json:"error,omitempty"`
}

// PluginToggleInfo is one setup-dialog checkbox as create_pane accepts it by
// name. Group non-empty means the toggles sharing it are mutually exclusive.
type PluginToggleInfo struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Group   string `json:"group,omitempty"`
	Default bool   `json:"default,omitempty"`
}

type PluginCatalogEntry struct {
	Name        string             `json:"name"`
	DisplayName string             `json:"display_name"`
	Category    string             `json:"category"`
	Available   bool               `json:"available"`
	PromptsCWD  bool               `json:"prompts_cwd"`
	Sessions    bool               `json:"sessions"`
	Toggles     []PluginToggleInfo `json:"toggles,omitempty"`
}

type PluginCatalogRespPayload struct {
	Plugins []PluginCatalogEntry `json:"plugins"`
	// SandboxAvailable reports whether this daemon can run sandbox panes at
	// all (docker present), so an agent does not offer an image to a host
	// that will refuse it.
	SandboxAvailable bool `json:"sandbox_available"`
}

// DelegateTaskReqPayload hands a prompt to ToPane and records a task the
// daemon completes from ToPane's work ledger.
type DelegateTaskReqPayload struct {
	ToPane string `json:"to_pane"`
	Prompt string `json:"prompt"`
	// FromPane is the requester (the bridge's own QUIL_PANE_ID). Optional:
	// a bridge outside any pane has none, and then Notify is ignored.
	FromPane string `json:"from_pane,omitempty"`
	// Notify types a one-line completion notice into FromPane when the task
	// ends, delivered only while FromPane is not mid-turn.
	Notify bool `json:"notify,omitempty"`
	// TimeoutMs ends the task as "timeout" if it has not finished by then.
	// 0 means no timeout.
	TimeoutMs int `json:"timeout_ms,omitempty"`
}

// TaskInfo is the wire view of a task. State is one of sent, working, done,
// failed, timeout. Times are Unix ms; zero when not reached.
type TaskInfo struct {
	ID         string `json:"id"`
	FromPane   string `json:"from_pane,omitempty"`
	ToPane     string `json:"to_pane"`
	ToPaneName string `json:"to_pane_name,omitempty"`
	State      string `json:"state"`
	Prompt     string `json:"prompt"`
	// Result is the last lines of ToPane's output at completion, ANSI-stripped.
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
	CreatedAt int64  `json:"created_at"`
	StartedAt int64  `json:"started_at,omitempty"`
	EndedAt   int64  `json:"ended_at,omitempty"`
	// Notified reports that the completion notice reached FromPane.
	Notified bool `json:"notified,omitempty"`
}

type DelegateTaskRespPayload struct {
	Task  TaskInfo `json:"task"`
	Error string   `json:"error,omitempty"`
}

type GetTaskReqPayload struct {
	TaskID string `json:"task_id"`
}

type GetTaskRespPayload struct {
	Task  TaskInfo `json:"task"`
	Error string   `json:"error,omitempty"`
}

type WaitTaskReqPayload struct {
	TaskID    string `json:"task_id"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

type WaitTaskRespPayload struct {
	Task    TaskInfo `json:"task"`
	Timeout bool     `json:"timeout,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type ListTasksReqPayload struct {
	// PaneID filters to tasks where the pane is requester OR target.
	PaneID string `json:"pane_id,omitempty"`
}

type ListTasksRespPayload struct {
	Tasks []TaskInfo `json:"tasks"`
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
	// Requests names the gated request types this daemon actually handles.
	//
	// It exists because a VERSION NUMBER cannot tell a feature-branch build
	// from the release that wears the same number: scripts/dev.sh stamps the
	// tree's VERSION into every binary, so a client built beside a daemon
	// that has a new request type and a released daemon that does not can
	// report the identical string. A floor compared against that string is
	// therefore either too strict (it refuses the daemon the client was built
	// beside, making the feature unusable in the builds used to test it) or
	// too loose (it accepts a released daemon that drops the request in
	// silence). This answers the question the floor was approximating.
	//
	// ABSENT from every daemon built before this field existed, which is the
	// discriminator: an empty list means "cannot say", and the caller falls
	// back to the version floor. Only the gated types are listed — this is
	// not a catalogue of everything the daemon handles, and nothing should
	// read it as one.
	Requests []string `json:"requests,omitempty"`
}

// GatedRequests are the request types a daemon advertises in
// VersionRespPayload.Requests. Add a type here when it is new enough that an
// older daemon would drop it silently.
var GatedRequests = []string{MsgCreateFromTemplateReq}

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

// Resource reporting payloads

// ResourceReportReqPayload asks for the workspace's resource state.
type ResourceReportReqPayload struct {
	// WithTrees asks for per-pane process trees as well as totals.
	//
	// This flag is what keeps the fat frame off the wire. The status bar polls
	// this message every 5 s for the life of the session and needs only
	// totals; trees for forty panes are tens of kilobytes per report, and
	// sending them on that tick would put an oversized frame on the 64-slot
	// must-deliver queue continuously. A client's own critical queue
	// overflowing is a documented force-disconnect shape in this codebase.
	//
	// It also gates the daemon's process collector, which runs only while
	// requests with this flag keep arriving.
	WithTrees bool `json:"with_trees,omitempty"`
}

// ProcNode is the wire form of one process in a pane's tree.
//
// Carries an image name and never a command line: command lines are unbounded
// and routinely contain secrets in argv, and in remote mode they come from a
// machine the user may not control.
type ProcNode struct {
	PID      int        `json:"pid"`
	Name     string     `json:"name"`
	RSSBytes uint64     `json:"rss_bytes"`
	CPUPct   float64    `json:"cpu_pct"` // negative means unknown
	Depth    int        `json:"depth"`   // 1 = the pane's direct child
	Children []ProcNode `json:"children,omitempty"`
	// StartMS is the process start time in Unix milliseconds, echoed back on a
	// kill request so the daemon can confirm the target is still the same
	// process. Zero means the platform could not read it, which the daemon
	// treats as grounds to refuse a kill.
	StartMS int64 `json:"start_ms,omitempty"`
}

// PaneResourceInfo is one pane's resource state.
type PaneResourceInfo struct {
	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id"`
	GoHeapBytes uint64 `json:"go_heap_bytes"`
	PTYRSSBytes uint64 `json:"pty_rss_bytes"`
	TotalBytes  uint64 `json:"total_bytes"`
	// Tree is present only when WithTrees was set.
	Tree *ProcNode `json:"tree,omitempty"`
	// InContainer marks a pane whose process runs inside a container, so the
	// dialog says "not measured" rather than a confidently wrong number.
	//
	// The collector walks the HOST process tree from the pane's PID, and a
	// sandbox pane's PID is the docker CLI — the agent lives in the Docker VM
	// on Windows or a containerd cgroup on Linux, never beneath it. The
	// figure would therefore be the CLI's own footprint, which is neither the
	// pane's nor visibly wrong. An em dash is the honest answer, the same
	// rule this dialog already applies to an unsampled CPU reading.
	InContainer bool `json:"in_container,omitempty"`
}

// QuilProcInfo is one of quil's own processes, as it described itself.
//
// Every field here is SELF-REPORTED over the socket by the process it
// describes. Nothing is inferred from the OS process table — that is what the
// previous attempt at this feature did, and it was wrong on both platforms.
type QuilProcInfo struct {
	Role     string `json:"role"` // "tui" | "bridge" | "daemon"
	PID      int    `json:"pid"`
	Version  string `json:"version"`
	ExeName  string `json:"exe_name"`
	UptimeMS int64  `json:"uptime_ms"`
	// Stale marks a process whose version differs from the daemon's.
	Stale bool `json:"stale,omitempty"`

	// CPUPct and RSSBytes are the last values this process reported about
	// itself via MsgClientStat. CPUPct is negative when unknown, which is what
	// a process that has not reported yet gets — NOT zero, which renders as
	// "0%" and claims the process is idle.
	CPUPct   float64 `json:"cpu_pct"`
	RSSBytes uint64  `json:"rss_bytes,omitempty"`
	// StatAgeMS is how long ago that report ARRIVED, on the daemon's clock.
	// Zero means nothing was ever reported, which the dialog renders the same
	// as unknown but which is a distinct state from a report that went stale.
	//
	// Measured daemon-side rather than carried by the client for the reason
	// UptimeMS is a duration: in remote mode the two clocks are unrelated.
	StatAgeMS int64 `json:"stat_age_ms,omitempty"`
}

type ResourceReportRespPayload struct {
	SnapshotAt int64              `json:"snapshot_at"` // Unix nanoseconds
	Panes      []PaneResourceInfo `json:"panes"`
	Total      uint64             `json:"total"`
	Tabs       []TabInfo          `json:"tabs,omitempty"`
	// Quil lists quil's own processes. Present only when WithTrees was set —
	// the status bar has no use for it.
	Quil []QuilProcInfo `json:"quil,omitempty"`
	// Unidentified counts connections that never sent MsgClientHello AND have
	// been open long enough that they cannot be a short-lived probe. At
	// rollout these are bridges from a build predating the feature, which is
	// precisely the population the stale marker exists to expose.
	Unidentified int `json:"unidentified,omitempty"`
	// TreesAt is when the process trees were collected, which can lag
	// SnapshotAt: the collector is gated and its ticks can be skipped. The
	// dialog surfaces staleness rather than presenting old numbers as current.
	TreesAt int64 `json:"trees_at,omitempty"`
	// CPUSampled is false where the platform reports a kernel-computed average
	// instead of usage over our own sample window (Darwin). The dialog
	// footnotes it, because a column that looks uniform while meaning
	// different things per platform is a confidently wrong answer.
	CPUSampled bool `json:"cpu_sampled,omitempty"`
	// WithTrees echoes the request flag.
	//
	// The status bar and the dialog share this message, so a status-bar poll's
	// response can arrive while the dialog is open. Without an explicit echo the
	// client cannot tell a treeless answer from a tree-bearing one whose trees
	// happen to be empty, and adopting the former blanks the dialog for a round
	// trip. Echoed rather than inferred from a populated field for the same
	// reason the browse dialogs echo their request key verbatim.
	WithTrees bool `json:"with_trees,omitempty"`
	// CPUSupported is false where the platform has no CPU source at all.
	CPUSupported bool `json:"cpu_supported,omitempty"`
}

// ClientHelloPayload is a durable client's self-description.
type ClientHelloPayload struct {
	Role    string `json:"role"` // "tui" | "bridge"
	PID     int    `json:"pid"`
	Version string `json:"version"`
	// ExeName is the basename of the running binary, so a bridge still
	// executing quil.exe.old.3 after an in-place update swap is visible as
	// such — an observed production state, not a hypothetical.
	ExeName string `json:"exe_name"`
	// UptimeMS is a DURATION, deliberately not a start timestamp. In remote
	// mode the client and daemon are on different machines with unsynchronised
	// clocks, and a daemon computing now-minus-start would report skewed and
	// sometimes negative uptimes. It is also not redundant with the daemon's
	// own connection age: a re-dial resets the connection but not the process,
	// so after a daemon restart the conn is seconds old while a stale bridge
	// has been alive for days.
	UptimeMS int64 `json:"uptime_ms"`
}

// ClientStatPayload is a durable client's periodic report about ITSELF.
//
// Both fields carry an explicit unknown, because "could not measure" and
// "measured zero" are different claims and only one of them is safe to render
// as a number. A client on a platform with no cumulative CPU counter sends a
// negative CPUPct forever, and the dialog shows an em dash forever.
type ClientStatPayload struct {
	// CPUPct is percent of ONE core since this client's previous report.
	// Negative means unknown — the same convention ProcNode.CPUPct uses.
	CPUPct float64 `json:"cpu_pct"`
	// RSSBytes is the process's resident set size. Zero means unknown; a live
	// process cannot genuinely occupy zero bytes, so the ambiguity is free.
	RSSBytes uint64 `json:"rss_bytes"`
}

// Process kill payloads

// KillProcessReqPayload proposes killing one pane descendant.
type KillProcessReqPayload struct {
	// PaneID scopes the kill: the target must be a descendant of THIS pane's
	// process, re-derived daemon-side rather than taken on trust.
	PaneID string `json:"pane_id"`
	PID    int    `json:"pid"`
	// StartMS is the target's start time as the client saw it. The daemon
	// requires a match before signalling; a PID recycled between the snapshot
	// and the accepted confirm is a different process wearing the same number,
	// and the start time is the only thing that distinguishes them.
	StartMS int64 `json:"start_ms"`
}

type KillProcessRespPayload struct {
	// Signalled and Escalated count what the sweep actually did.
	Signalled int `json:"signalled"`
	Escalated int `json:"escalated"`
	// Refused carries the reason when nothing was killed. A refusal is a
	// normal outcome here, not an error.
	Refused string `json:"refused,omitempty"`
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

// SandboxCapReqPayload asks whether the daemon's machine can run a sandbox
// pane. No fields: the answer describes the daemon, not any particular
// request.
type SandboxCapReqPayload struct{}

// SandboxCapRespPayload answers a capability request.
//
// Every field describes the DAEMON's Docker engine, which is why the client
// files the answer against the destination that sent it rather than adopting
// it globally. One registry serving every destination is what made a remote
// host without `claude` grey out Claude Code in the local project.
type SandboxCapRespPayload struct {
	// Available is the one field a caller should gate on. It requires an
	// engine that is reachable AND running linux containers: Docker Desktop
	// in Windows-containers mode answers an info probe perfectly well and
	// then fails every linux image at `run`, so reachability alone would
	// offer the user a pane that cannot start.
	Available bool `json:"available"`

	ServerVersion string `json:"server_version,omitempty"`
	OSType        string `json:"os_type,omitempty"`
	// Arch is normalised to Go's spelling (amd64, arm64). Docker reports the
	// machine form — x86_64, aarch64 — and the only consumer is a release
	// asset name, which is in Go's.
	Arch string `json:"arch,omitempty"`
	// Error explains an unavailable engine in the user's terms. Rendered
	// through sanitizeRemoteText like every other daemon-supplied string: on
	// a remote daemon this is text from a machine the user may not control.
	Error string `json:"error,omitempty"`
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
//
// CommitTime is the committer date (unix seconds) of the checked-out branch's
// tip, joined daemon-side from the branch listing, so the setup dialog can
// order worktrees by recency. Zero when unknown — a detached checkout, a branch
// the listing did not carry, or a daemon too old to send it — and a client
// ORDERS by it only; nothing is hidden or refused for lacking one, and a
// listing of all zeros keeps git's own order.
type WorktreeInfo struct {
	Path       string `json:"path"`
	Branch     string `json:"branch,omitempty"`
	Detached   bool   `json:"detached,omitempty"`
	Main       bool   `json:"main,omitempty"`
	Locked     bool   `json:"locked,omitempty"`
	Prunable   bool   `json:"prunable,omitempty"`
	Bare       bool   `json:"bare,omitempty"`
	CommitTime int64  `json:"commit_time,omitempty"`
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
// Branches lists the repository's LOCAL branch names, short, so the setup
// dialog can refuse a name `git worktree add -b` would refuse. Worktrees cannot
// answer that question — it reports only branches that HAVE a checkout, and the
// ordinary way to collide is with a branch whose worktree was removed.
//
// BranchesTruncated says the list was clipped at the daemon's cap. It is not
// cosmetic: a client that cannot see the whole list must not conclude a name is
// FREE, so absence from a truncated list means "no opinion", never "available".
// A branch listing that FAILED is reported the same way, as an empty list — the
// worktree listing is what the dialog needs to function and must not be lost
// with it.
type WorktreeListRespPayload struct {
	Path              string         `json:"path"`
	Repo              bool           `json:"repo,omitempty"`
	Root              string         `json:"root,omitempty"`
	WorktreeRoot      string         `json:"worktree_root,omitempty"`
	Worktrees         []WorktreeInfo `json:"worktrees,omitempty"`
	Branches          []string       `json:"branches,omitempty"`
	BranchesTruncated bool           `json:"branches_truncated,omitempty"`
	Error             string         `json:"error,omitempty"`
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
