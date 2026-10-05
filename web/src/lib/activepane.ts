import type { WorkspaceState } from './protocol';

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
