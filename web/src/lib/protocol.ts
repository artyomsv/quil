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
