import type { WorkspaceState } from './protocol';

export type OverlayKind = 'lazygit' | 'hunk';

export interface OverlayInfo {
  id: string;
  kind: string;
  cwd: string;
}

// overlayOf is the tab's one overlay pane (the daemon keeps one slot per
// tab), or null when it has none — including when it just left the state.
export function overlayOf(s: WorkspaceState | null, tabId: string): OverlayInfo | null {
  if (!s) return null;
  const p = s.panes.find((x) => x.tab_id === tabId && x.overlay === true);
  return p ? { id: p.id, kind: p.type ?? '', cwd: p.cwd } : null;
}
