import type { SerializedNode, WorkspaceState } from './protocol';

// The active pane is this browser tab's own choice (the daemon has none). It
// survives a state while its pane is still placed; otherwise the first placed
// pane takes over.
export function pickActive(current: string, placed: string[]): string {
  if (placed.includes(current)) return current;
  return placed[0] ?? '';
}

// unseenToClear is the pane to report update_pane{unseen:false} for: the
// active pane while the daemon still marks it unseen, at most once per mark
// (asked holds the panes already reported; the caller drops a pane from it
// when the state shows the mark cleared).
export function unseenToClear(s: WorkspaceState, active: string, asked: Set<string>): string | null {
  if (active === '' || asked.has(active)) return null;
  const p = s.panes.find((x) => x.id === active);
  return p?.unseen ? active : null;
}

// askedTabShown says whether a tab a rename or close dialog is about still
// exists; a dialog about a closed tab closes.
export function askedTabShown(s: WorkspaceState, tabId: string): boolean {
  return s.tabs.some((t) => t.id === tabId);
}

// successorOf is the pane that took gone's slot between two states: the leaf
// at gone's place in its tab's previous tree, in the same tab's new tree,
// when that leaf is a pane the previous state did not have. A preparing
// worktree's placeholder and a replaced pane are followed this way, so the
// page's active pane (and a focus waiting for it) moves to their successor.
// '' when gone is still there, or nothing new stands in its place.
export function successorOf(prev: WorkspaceState | null, next: WorkspaceState, gone: string): string {
  if (!prev || gone === '' || next.panes.some((p) => p.id === gone)) return '';
  const tabId = prev.panes.find((p) => p.id === gone)?.tab_id;
  const before = prev.tabs.find((t) => t.id === tabId)?.layout;
  const after = next.tabs.find((t) => t.id === tabId)?.layout;
  if (!before || !after) return '';
  const path = pathTo(before, gone);
  if (!path) return '';
  let n: SerializedNode | undefined = after;
  for (const side of path) n = side === 'l' ? n?.left : n?.right;
  const id = n?.pane_id ?? '';
  return id !== '' && !prev.panes.some((p) => p.id === id) ? id : '';
}

function pathTo(n: SerializedNode | undefined, id: string): ('l' | 'r')[] | null {
  if (!n) return null;
  if (n.pane_id !== undefined && n.pane_id !== '') return n.pane_id === id ? [] : null;
  const l = pathTo(n.left, id);
  if (l) return ['l', ...l];
  const r = pathTo(n.right, id);
  return r ? ['r', ...r] : null;
}
