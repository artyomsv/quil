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
  // The note's revision on the daemon (0 = no note); the text never rides here.
  note_rev?: number;
}

export interface ProjectState {
  id: string;
  name: string;
  root_dir: string;
  tab_ids: string[];
  active_tab: string;
  // The group this project is filed under; '' = ungrouped.
  group?: string;
  // The daemon's own first project, made before any client named one.
  bootstrap?: boolean;
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
  // The daemon's group names, creation order.
  groups?: string[];
  // A newer release the daemon found; absent does NOT mean up to date.
  update?: UpdateInfo;
}

// ipc.UpdateInfo
export interface UpdateInfo {
  latest_version: string;
  release_url?: string;
  staged_version?: string;
  install_writable: boolean;
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
  // The pane exists but has a problem (ipc.SplitPaneRespPayload): not a refusal.
  notice?: string;
  error?: string;
}

// ---- Step 5c payloads, field for field from internal/ipc ----

// ipc.PaneSearchHit
export interface PaneSearchHit {
  pane_id: string;
  matches: number;
  excerpt: string;
  truncated?: boolean;
}

// ipc.PaneSearchRespPayload
export interface PaneSearchResp {
  query: string;
  hits: PaneSearchHit[] | null;
  truncated?: boolean;
}

// ipc.NoteRespPayload
export interface NoteResp {
  pane_id: string;
  text: string;
  rev: number;
  error?: string;
}

// ipc.NoteSetRespPayload
export interface NoteSetResp {
  pane_id: string;
  ok: boolean;
  rev?: number;
  conflict?: boolean;
  current_rev?: number;
  error?: string;
}

// ipc.HistoryEntryMeta
export interface HistoryEntryMeta {
  ts_ms: number;
  preview: string;
}

// ipc.PaneHistoryRespPayload
export interface PaneHistoryResp {
  pane_id: string;
  entries: HistoryEntryMeta[] | null;
}

// ipc.PaneHistoryEntryRespPayload
export interface PaneHistoryEntryResp {
  pane_id: string;
  ts_ms: number;
  text: string;
  found: boolean;
}

// ipc.CreateProjectRespPayload
export interface CreateProjectResp {
  project_id?: string;
  name?: string;
  error?: string;
}

// ipc.OpRespPayload (project_op_resp, tab_op_resp, group_op_resp)
export interface OpResp {
  id?: string;
  ok: boolean;
  error?: string;
}

// ipc.CreateFromTemplateReqPayload
export interface CreateFromTemplateReq {
  template: string;
  task?: string;
  cwd?: string;
  branch?: string;
  project_id?: string;
}

// ipc.CreateFromTemplateRespPayload
export interface CreateFromTemplateResp {
  tab_id?: string;
  pane_ids?: string[];
  preparing_worktree?: string;
  error?: string;
}

// ipc.ClaudeSessionDetailRespPayload
export interface ClaudeSessionDetailResp {
  cwd: string;
  session_id: string;
  first_prompt?: string;
  last_prompt?: string;
  user_prompts?: number;
  started_ms?: number;
  modified_ms?: number;
  size_bytes?: number;
  error?: string;
}

// ipc.ProcNode
export interface ProcNode {
  pid: number;
  name: string;
  rss_bytes: number;
  cpu_pct: number;
  depth: number;
  children?: ProcNode[];
  start_ms?: number;
}

// ipc.PaneResourceInfo
export interface PaneResourceInfo {
  pane_id: string;
  tab_id: string;
  go_heap_bytes: number;
  pty_rss_bytes: number;
  total_bytes: number;
  tree?: ProcNode;
  in_container?: boolean;
}

// ipc.QuilProcInfo
export interface QuilProcInfo {
  role: string;
  pid: number;
  version: string;
  exe_name: string;
  uptime_ms: number;
  stale?: boolean;
  cpu_pct: number;
  rss_bytes?: number;
  stat_age_ms?: number;
}

// ipc.ResourceReportRespPayload
export interface ResourceReportResp {
  snapshot_at: number;
  panes: PaneResourceInfo[] | null;
  total: number;
  quil?: QuilProcInfo[];
  trees_at?: number;
}

// ipc.KillProcessRespPayload
export interface KillProcessResp {
  signalled: number;
  escalated: number;
  refused?: string;
}

// ipc.StageUpdateRespPayload
export interface StageUpdateResp {
  success: boolean;
  already_staged?: boolean;
  check_failed?: boolean;
  version?: string;
  error?: string;
}

// ipc.VersionRespPayload
export interface VersionResp {
  version: string;
  requests?: string[];
}

// ipc.PluginInfo
export interface PluginAvail {
  name: string;
  available: boolean;
}
