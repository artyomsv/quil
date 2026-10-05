import type { Message, WorkspaceState } from './protocol';

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

// OverlayToggle is what Alt+G / Alt+D does in one tab (spec §5.4).
export type OverlayToggle =
  | { do: 'show' | 'hide'; id: string }
  // hide names this page's shown overlay of the other tool, hidden first; ''
  // when none is shown.
  | { do: 'create'; hide: string }
  | { do: 'refuse' };

// overlayToggle decides it: the tab's overlay of that kind is shown or hidden
// on this page, which needs no rights; anything else asks the daemon for the
// slot, which only an editable page may.
export function overlayToggle(cur: OverlayInfo | null, shownId: string | undefined, kind: OverlayKind, canCreate: boolean): OverlayToggle {
  const shown = cur !== null && shownId === cur.id;
  if (cur && cur.kind === kind) return { do: shown ? 'hide' : 'show', id: cur.id };
  if (!canCreate) return { do: 'refuse' };
  return { do: 'create', hide: shown && cur ? cur.id : '' };
}

// overlayVisibleMsg tells the daemon this page shows or hides an overlay
// (overlay_visible drives its idle reaper); a read-only page sends nothing.
export function overlayVisibleMsg(readOnly: boolean, paneId: string, visible: boolean): Message | null {
  return readOnly ? null : { type: 'update_pane', payload: { pane_id: paneId, overlay_visible: visible } };
}
