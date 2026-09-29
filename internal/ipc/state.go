package ipc

import "encoding/json"

// WorkspaceState is the typed form of the workspace_state frame AND of
// workspace.json (the disk snapshot leaves the broadcast-only fields unset).
//
// The JSON keys and their presence rules are the wire contract that predates
// this type: every key the old map builder always wrote has no omitempty;
// every key it wrote conditionally has omitempty, or a pointer when it can be
// present at its zero value. Do not change a tag without changing every
// client — an older TUI decodes this with type assertions on these names.
type WorkspaceState struct {
	ActiveTab     string         `json:"active_tab"`
	Tabs          []TabState     `json:"tabs"`
	Panes         []PaneState    `json:"panes"`
	Projects      []ProjectState `json:"projects"`
	ActiveProject string         `json:"active_project"`

	// SizeMaster is on the wire always (broadcast: "" = no master) and on
	// disk only when non-empty (the restart reserve). A pointer lets each
	// path choose.
	SizeMaster *string `json:"size_master,omitempty"`

	// Broadcast-only. The disk path never sets them.
	Update        *UpdateInfo `json:"update,omitempty"`
	Clients       *int        `json:"clients,omitempty"`
	DaemonLimited bool        `json:"daemon_limited,omitempty"`
	// Rev numbers every state frame of one daemon run, starting at 1; RunID
	// is new per daemon process. Zero/empty means an older daemon.
	Rev   uint64 `json:"rev,omitempty"`
	RunID string `json:"run_id,omitempty"`

	// SharedData is broadcast-only and always true from a 3b daemon: this
	// daemon owns groups, recent folders and notes for its projects and panes
	// (spec 4.1). Never written to workspace.json.
	SharedData bool `json:"shared_data,omitempty"`
	// Groups is this daemon's group-name list, creation order (no display
	// meaning — order is per client). RecentCWDs is its recent-folder list,
	// most recent first, at most MaxRecentCWDs. Both persist.
	Groups     []string `json:"groups,omitempty"`
	RecentCWDs []string `json:"recent_cwds,omitempty"`
}

// TabState is one tab. Panes is never nil on the wire.
type TabState struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Color          string          `json:"color"`
	Panes          []string        `json:"panes"`
	ProjectID      string          `json:"project_id"`
	LayoutRev      uint64          `json:"layout_rev"`
	Layout         json.RawMessage `json:"layout,omitempty"`
	TemplateLayout string          `json:"template_layout,omitempty"`
	TemplateMain   string          `json:"template_main,omitempty"`
}

// ProjectState is one project; every key is always present. TabIDs is passed
// through as the daemon holds it, so a nil list stays JSON null as before.
type ProjectState struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	RootDir   string   `json:"root_dir"`
	TabIDs    []string `json:"tab_ids"`
	ActiveTab string   `json:"active_tab"`
	Bootstrap bool     `json:"bootstrap"`
	// Group is the group name this project is filed under; "" = ungrouped.
	Group string `json:"group,omitempty"`
}

// PaneState is one pane. Numeric fields are wide on purpose: a client
// range-checks them rather than failing the whole frame on one odd value.
type PaneState struct {
	ID    string `json:"id"`
	TabID string `json:"tab_id"`
	CWD   string `json:"cwd"`

	Name              string            `json:"name,omitempty"`
	Type              string            `json:"type,omitempty"`
	PluginState       map[string]string `json:"plugin_state,omitempty"`
	Muted             bool              `json:"muted,omitempty"`
	Eager             bool              `json:"eager,omitempty"`
	ConvertedFrom     string            `json:"converted_from,omitempty"`
	Adopted           bool              `json:"adopted,omitempty"`
	PinnedAttention   bool              `json:"pinned_attention,omitempty"`
	MarkedForDeletion bool              `json:"marked_for_deletion,omitempty"`
	Unseen            bool              `json:"unseen,omitempty"`
	WorktreeOwned     bool              `json:"worktree_owned,omitempty"`
	QuilMCP           bool              `json:"quil_mcp,omitempty"`
	WorktreePath      string            `json:"worktree_path,omitempty"`
	SandboxImage      string            `json:"sandbox_image,omitempty"`
	// SandboxAuth is present (possibly "") whenever SandboxImage is set.
	SandboxAuth         *string  `json:"sandbox_auth,omitempty"`
	ContainerCWD        string   `json:"container_cwd,omitempty"`
	WorktreeInterrupted bool     `json:"worktree_interrupted,omitempty"`
	InstanceName        string   `json:"instance_name,omitempty"`
	InstanceArgs        []string `json:"instance_args,omitempty"`
	Cols                int      `json:"cols,omitempty"`
	Rows                int      `json:"rows,omitempty"`
	Overlay             bool     `json:"overlay,omitempty"`
	// NoteRev is the pane's note version; 0 = no note. Persisted and broadcast; the note text never rides the frame.
	NoteRev uint64 `json:"note_rev,omitempty"`

	// Broadcast-only (includeOverlays == true).
	SizeSeq           uint64 `json:"size_seq,omitempty"`
	Pending           bool   `json:"pending,omitempty"`
	SessionID         string `json:"session_id,omitempty"`
	HistoryLines      int    `json:"history_lines,omitempty"`
	MouseTracking     bool   `json:"mouse_tracking,omitempty"`
	MouseSGR          bool   `json:"mouse_sgr,omitempty"`
	BracketedPaste    bool   `json:"bracketed_paste,omitempty"`
	SpawnError        string `json:"spawn_error,omitempty"`
	PreparingWorktree string `json:"preparing_worktree,omitempty"`
	Model             string `json:"model,omitempty"`
	// ContextTokens is present (possibly 0) whenever Model is set.
	ContextTokens   *int64 `json:"context_tokens,omitempty"`
	GitBranch       string `json:"git_branch,omitempty"`
	GitDetached     bool   `json:"git_detached,omitempty"`
	GitWorktree     bool   `json:"git_worktree,omitempty"`
	GitWorktreeName string `json:"git_worktree_name,omitempty"`
	GitUpstream     bool   `json:"git_upstream,omitempty"`
	// GitAhead/GitBehind are present (possibly 0) whenever GitUpstream is.
	GitAhead  *int `json:"git_ahead,omitempty"`
	GitBehind *int `json:"git_behind,omitempty"`
	GitStale  bool `json:"git_stale,omitempty"`
}
