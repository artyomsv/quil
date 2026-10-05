import { displayLayout, paneRects, type Rect } from './layout';
import type { PaneState, ProjectState, SerializedNode, TabState, WorkspaceState } from './protocol';
import { sanitizeRemoteText } from './sanitize';

export type AgentDot = 'working' | 'blocked' | 'idle' | 'unknown';

export interface PaneDot {
  id: string;
  name: string;
  state: AgentDot;
}

export interface TabItem {
  id: string;
  name: string;
  // The daemon's colour value (TAB_COLORS in actions.ts); '' is the default.
  color: string;
  active: boolean;
  dots: PaneDot[];
}

export interface ProjectItem {
  id: string;
  name: string;
  active: boolean;
  tabs: TabItem[];
}

export interface PlacedPane {
  id: string;
  name: string;
  rect: Rect;
  spawnError: string;
  muted: boolean;
  worktreeOwned: boolean;
  // The pane's agent state, as the sidebar dots show it.
  agent: AgentDot;
}

// A border drag's tree, drawn for its tab in place of the stored one until
// the daemon confirms or refuses it.
export interface LayoutPreview {
  tabId: string;
  tree: SerializedNode;
}

const isObject = (v: unknown): v is Record<string, unknown> =>
  typeof v === 'object' && v !== null && !Array.isArray(v);

// A list field: null (Go's nil slice) reads as empty; anything else that is
// not an array makes the frame malformed. Entries without a string id are
// dropped.
function list<T>(v: unknown): T[] | null {
  if (v === undefined || v === null) return [];
  if (!Array.isArray(v)) return null;
  return v.filter((e) => isObject(e) && typeof e.id === 'string') as T[];
}

const strings = (v: unknown): string[] => (Array.isArray(v) ? v.filter((s) => typeof s === 'string') : []);
const str = (v: unknown): string => (typeof v === 'string' ? v : '');

// parseWorkspaceState checks a workspace_state payload's shape and normalizes
// the daemon's null lists. It returns null for a frame that must be dropped,
// never an empty workspace in its place.
export function parseWorkspaceState(p: unknown): WorkspaceState | null {
  if (!isObject(p)) return null;
  const tabs = list<TabState>(p.tabs);
  const panes = list<PaneState>(p.panes);
  const projects = list<ProjectState>(p.projects);
  if (!tabs || !panes || !projects) return null;
  const out: WorkspaceState = {
    active_tab: str(p.active_tab),
    active_project: str(p.active_project),
    tabs: tabs.map((t) => ({
      ...t,
      name: str(t.name),
      color: str(t.color),
      panes: strings(t.panes),
      project_id: str(t.project_id),
      layout: isObject(t.layout) ? (t.layout as SerializedNode) : undefined,
    })),
    panes: panes.map((x) => ({ ...x, tab_id: str(x.tab_id), cwd: str(x.cwd) })),
    projects: projects.map((x) => ({ ...x, name: str(x.name), tab_ids: strings(x.tab_ids), active_tab: str(x.active_tab) })),
  };
  if (typeof p.size_master === 'string') out.size_master = p.size_master;
  if (typeof p.rev === 'number') out.rev = p.rev;
  if (typeof p.run_id === 'string') out.run_id = p.run_id;
  return out;
}

// The page always shows the daemon's active tab; it never switches locally.
export function activeTabOf(s: WorkspaceState | null): string {
  return s?.active_tab ?? '';
}

// The active tab's project, or the daemon's active project when the tab names
// none.
export function activeProjectOf(s: WorkspaceState | null): string {
  if (!s) return '';
  const tab = s.tabs.find((t) => t.id === s.active_tab);
  return tab?.project_id || s.active_project;
}

export function paneName(p: PaneState): string {
  return sanitizeRemoteText(p.name || p.type || 'pane');
}

function dotOf(state: string | undefined): AgentDot {
  return state === 'working' || state === 'blocked' || state === 'idle' ? state : 'unknown';
}

function tabItem(s: WorkspaceState, tab: TabState, panes: Map<string, PaneState>, agents: Record<string, string>): TabItem {
  const dots: PaneDot[] = [];
  for (const id of tab.panes) {
    const p = panes.get(id);
    if (!p || p.overlay) continue;
    dots.push({ id, name: paneName(p), state: dotOf(agents[id]) });
  }
  return { id: tab.id, name: sanitizeRemoteText(tab.name), color: tab.color, active: tab.id === s.active_tab, dots };
}

// sidebarModel lists the projects in the daemon's order, each with its tabs
// in its own order. A tab whose project is not listed goes under a last
// group with no id, so it is never hidden.
export function sidebarModel(s: WorkspaceState | null, agents: Record<string, string>): ProjectItem[] {
  if (!s) return [];
  const panes = new Map(s.panes.map((p) => [p.id, p]));
  const tabs = new Map(s.tabs.map((t) => [t.id, t]));
  const activeProject = activeProjectOf(s);
  const placed = new Set<string>();
  const out: ProjectItem[] = [];
  for (const proj of s.projects) {
    const items: TabItem[] = [];
    for (const id of proj.tab_ids) {
      const t = tabs.get(id);
      if (!t || placed.has(id)) continue;
      placed.add(id);
      items.push(tabItem(s, t, panes, agents));
    }
    out.push({ id: proj.id, name: sanitizeRemoteText(proj.name), active: proj.id === activeProject, tabs: items });
  }
  const rest = s.tabs.filter((t) => !placed.has(t.id)).map((t) => tabItem(s, t, panes, agents));
  if (rest.length > 0) out.push({ id: '', name: 'Other tabs', active: false, tabs: rest });
  return out;
}

// tabBarModel is the active project's tabs.
export function tabBarModel(s: WorkspaceState | null, agents: Record<string, string>): TabItem[] {
  const pid = activeProjectOf(s);
  return sidebarModel(s, agents).find((p) => p.id === pid)?.tabs ?? [];
}

// activeTab is the active tab with its non-overlay panes, and the tree to
// draw for it: preview's tree when preview is for that tab, else the stored
// tree as displayLayout completes it.
function activeTab(
  s: WorkspaceState,
  preview: LayoutPreview | null | undefined,
): { panes: Map<string, PaneState>; ids: string[]; tree: SerializedNode | undefined } | null {
  const tab = s.tabs.find((t) => t.id === s.active_tab);
  if (!tab) return null;
  const panes = new Map(s.panes.map((p) => [p.id, p]));
  const ids = tab.panes.filter((id) => {
    const p = panes.get(id);
    return p !== undefined && !p.overlay;
  });
  const stored = preview?.tabId === tab.id ? preview.tree : tab.layout;
  return { panes, ids, tree: displayLayout(stored, ids, tab.template_layout ?? '', tab.template_main ?? '') };
}

// activeTree is the tree drawn for the active tab (the split bars sit on it).
export function activeTree(s: WorkspaceState | null, preview?: LayoutPreview | null): SerializedNode | undefined {
  return s ? activeTab(s, preview)?.tree : undefined;
}

// placedPanes is the active tab's panes where the layout puts them, overlay
// panes left out. A preview for the active tab is drawn in place of its
// stored tree.
export function placedPanes(
  s: WorkspaceState | null,
  preview?: LayoutPreview | null,
  agents: Record<string, string> = {},
): PlacedPane[] {
  if (!s) return [];
  const at = activeTab(s, preview);
  if (!at) return [];
  const rects = paneRects(at.tree);
  const out: PlacedPane[] = [];
  for (const id of at.ids) {
    const rect = rects.get(id);
    const p = at.panes.get(id);
    if (!rect || !p) continue;
    out.push({
      id,
      name: paneName(p),
      rect,
      spawnError: sanitizeRemoteText(p.spawn_error ?? ''),
      muted: p.muted === true,
      worktreeOwned: p.worktree_owned === true,
      agent: dotOf(agents[id]),
    });
  }
  return out;
}
