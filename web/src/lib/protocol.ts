export interface Message {
  type: string;
  id?: string;
  payload?: unknown;
}

export interface ErrorPayload {
  code: string;
  message: string;
  type: string;
}

export interface WebWelcome {
  client_id: string;
  rights: 'full' | 'standard' | 'read-only' | string;
  version: string;
}

export interface SerializedNode {
  pane_id?: string;
  split?: number;
  ratio?: number;
  left?: SerializedNode;
  right?: SerializedNode;
}

export interface TabState {
  id: string;
  name: string;
  color: string;
  panes: string[];
  project_id: string;
  layout_rev: number;
  layout?: SerializedNode;
  template_layout?: string;
  template_main?: string;
}

export interface PaneState {
  id: string;
  tab_id: string;
  cwd: string;
  name?: string;
  type?: string;
  cols?: number;
  rows?: number;
  size_seq?: number;
  overlay?: boolean;
  pending?: boolean;
  spawn_error?: string;
  muted?: boolean;
  unseen?: boolean;
  worktree_owned?: boolean;
  worktree_path?: string;
  plugin_state?: Record<string, string>;
  instance_name?: string;
  instance_args?: string[];
  sandbox_image?: string;
  sandbox_auth?: string;
  sandbox_claude_config?: string;
}

export interface ProjectState {
  id: string;
  name: string;
  root_dir: string;
  tab_ids: string[];
  active_tab: string;
}

export interface WorkspaceState {
  active_tab: string;
  tabs: TabState[];
  panes: PaneState[];
  projects: ProjectState[];
  active_project: string;
  size_master?: string;
  rev?: number;
  run_id?: string;
  // The daemon's recent folders, most recent first (the dialog's Recent row).
  recent_cwds?: string[];
}

export interface PaneInfo {
  id: string;
  tab_id: string;
  agent_state?: string;
  blocked_reason?: string;
}

export interface PaneSize {
  pane_id: string;
  cols: number;
  rows: number;
  size_seq?: number;
}

// One decoded binary pane_output frame. data is a view into the received
// buffer, not a copy.
export interface PaneOutputFrame {
  paneId: string;
  ghost: boolean;
  generation: bigint;
  data: Uint8Array;
}

export const CLOSE = {
  resync: 4001,
  tooSlow: 4002,
  daemonUnavailable: 4003,
  tokenRefused: 4004,
  versionMismatch: 4005,
  byAgent: 4006,
  goingAway: 1001,
} as const;

// The reason of the 1008 the gateway closes a socket with when a newer socket
// of the same login took its place over the tab limit (closeReplaced in
// internal/webgw/server.go; keep the two equal). Unlike other 1008s it is not
// final: the page retries with the normal back-off.
export const CLOSE_REPLACED_REASON = 'replaced by a newer connection';

export type Placement = 'right' | 'below' | 'replace' | 'new_tab' | 'overlay';

// The pane half of split_pane_req: the same field names as the daemon's
// create_pane payload. instance_args never comes from the page (the gateway
// fills it from instance_id, see internal/webgw); a worktree is {branch} for
// a new branch (the daemon resolves the repository from cwd) or
// {existing_path} for an existing worktree — exactly one (R-A, ipc.SplitWorktree).
export interface PaneSpec {
  type?: string;
  name?: string;
  cwd: string;
  toggles?: string[];
  instance_id?: string;
  kube_context?: string;
  resume_session_id?: string;
  worktree?: { branch: string } | { existing_path: string };
  sandbox?: { image: string; auth?: string; claude_config?: string };
}

export interface SplitPaneReq {
  target_pane_id?: string;
  tab_id?: string;
  placement: Placement;
  new_tab?: { name: string; project_id: string };
  overlay_kind?: 'lazygit' | 'hunk';
  pane: PaneSpec;
}

export interface SplitPaneResp {
  pane_id: string;
  tab_id: string;
  layout_rev: number;
  preparing?: boolean;
  error?: string;
}
