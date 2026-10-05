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

// UnseenAsks is the set of panes this tab has asked the daemon to clear the
// unseen mark of, at most once per mark. A pane leaves it when a state shows
// the mark gone, when the ask was refused or got no answer, and when the
// link drops: a pane left in it would never be asked about again.
export class UnseenAsks {
  private readonly asked = new Set<string>();

  // next is the pane to ask about now, recorded as asked; null for none.
  next(s: WorkspaceState, active: string): string | null {
    const id = unseenToClear(s, active, this.asked);
    if (id) this.asked.add(id);
    return id;
  }

  // answered files the outcome of the ask about id.
  answered(id: string, ok: boolean): void {
    if (!ok) this.asked.delete(id);
  }

  // stateApplied drops every pane whose mark this state shows cleared.
  stateApplied(s: WorkspaceState): void {
    for (const id of [...this.asked]) {
      if (!s.panes.find((p) => p.id === id)?.unseen) this.asked.delete(id);
    }
  }

  reset(): void {
    this.asked.clear();
  }

  has(id: string): boolean {
    return this.asked.has(id);
  }
}

// PendingJump is a notification jump to a pane in another tab, waiting for
// the state that shows that tab active. from is the tab active when it was
// asked for.
export interface PendingJump {
  from: string;
  tab: string;
  pane: string;
}

// JumpStep is what a jump does now: switch is the tab to ask the daemon
// for, activate the pane to make active at once, pending the jump to finish
// later. Each may be empty or null.
export interface JumpStep {
  switch: string;
  activate: string;
  pending: PendingJump | null;
}

// jumpStep plans a jump to a card's pane. A pane in the active tab is made
// active only while it is placed there. A pane in another tab waits for the
// state that shows that tab (resolveJump): the states that arrive before the
// switch still show the old tab and would undo an early choice. A read-only
// page switches nothing, so it never jumps to another tab's pane.
export function jumpStep(s: WorkspaceState | null, activeTab: string, placed: string[], tab: string, pane: string, readOnly: boolean): JumpStep {
  const none: JumpStep = { switch: '', activate: '', pending: null };
  if (!s) return none;
  if (tab && tab !== activeTab) {
    if (readOnly) return none;
    const live = pane !== '' && s.panes.some((p) => p.id === pane);
    return { switch: tab, activate: '', pending: live ? { from: activeTab, tab, pane } : null };
  }
  return { ...none, activate: pane !== '' && placed.includes(pane) ? pane : '' };
}

// resolveJump settles a pending jump against a newly applied state: the pane
// to make active ('' for none) and the jump to keep waiting (null when it is
// over). It waits while the old tab is still shown, finishes on the target
// tab — with its pane only if that state places it — and is dropped when
// any other tab shows up.
export function resolveJump(j: PendingJump | null, s: WorkspaceState, placed: string[]): { activate: string; keep: PendingJump | null } {
  if (!j) return { activate: '', keep: null };
  if (s.active_tab === j.from) return { activate: '', keep: j };
  if (s.active_tab !== j.tab) return { activate: '', keep: null };
  return { activate: placed.includes(j.pane) ? j.pane : '', keep: null };
}
