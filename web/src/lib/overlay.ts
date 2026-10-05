import type { Message, WorkspaceState } from './protocol';
import { sanitizeRemoteText } from './sanitize';

export type OverlayKind = 'lazygit' | 'hunk';

export interface OverlayInfo {
  id: string;
  kind: string;
  cwd: string;
  // Why the overlay's tool did not start, sanitized for display; '' when it
  // did. The pane view shows it instead of an empty terminal.
  spawnError: string;
}

// overlayOf is the tab's one overlay pane (the daemon keeps one slot per
// tab), or null when it has none — including when it just left the state.
export function overlayOf(s: WorkspaceState | null, tabId: string): OverlayInfo | null {
  if (!s) return null;
  const p = s.panes.find((x) => x.tab_id === tabId && x.overlay === true);
  return p ? { id: p.id, kind: p.type ?? '', cwd: p.cwd, spawnError: sanitizeRemoteText(p.spawn_error ?? '') } : null;
}

// OverlayToggle is what Alt+G / Alt+D does in one tab (spec §5.4).
export type OverlayToggle =
  | { do: 'show'; id: string }
  | { do: 'hide'; id: string }
  // Ask the daemon which repositories hold the active pane's folder, then
  // decide with overlayRepoChoice. existing is the tab's overlay of this
  // kind (shown again when it already targets a candidate); hide is this
  // page's shown overlay of the OTHER tool, hidden only once a create goes
  // out. Both '' when there is none.
  | { do: 'discover'; existing: string; hide: string }
  | { do: 'refuse' };

// overlayToggle decides it, as the TUI does (internal/tui/overlay.go): a
// shown overlay of that kind hides. Anything else asks which repository the
// active pane is in, which an editable page may; a page that may not create
// still shows the tab's overlay of that kind.
export function overlayToggle(cur: OverlayInfo | null, shownId: string | undefined, kind: OverlayKind, canCreate: boolean): OverlayToggle {
  const shown = cur !== null && shownId === cur.id;
  const same = cur !== null && cur.kind === kind;
  if (same && shown) return { do: 'hide', id: cur.id };
  if (!canCreate) return same && cur ? { do: 'show', id: cur.id } : { do: 'refuse' };
  return { do: 'discover', existing: same && cur ? cur.id : '', hide: shown && !same && cur ? cur.id : '' };
}

// OverlayRepo is what the repositories found for the active pane decide.
export type OverlayRepo =
  | { do: 'show' }
  | { do: 'none' }
  | { do: 'pick'; repos: string[] }
  | { do: 'create'; repo: string };

// MAX_REPO_CHOICES caps the picker, as the TUI's maxRepoCandidates does.
export const MAX_REPO_CHOICES = 10;

// overlayRepoChoice is the TUI's resolveOverlay steps 3-7: no repository
// shows the tab's overlay of this kind if there is one; one that already
// runs on a candidate is shown; otherwise several candidates open a picker
// and one creates (the daemon replaces the tab's slot).
export function overlayRepoChoice(candidates: string[], existing: OverlayInfo | null): OverlayRepo {
  if (candidates.length === 0) return existing ? { do: 'show' } : { do: 'none' };
  if (existing && candidates.includes(existing.cwd)) return { do: 'show' };
  if (candidates.length > 1) return { do: 'pick', repos: candidates.slice(0, MAX_REPO_CHOICES) };
  return { do: 'create', repo: candidates[0]! };
}

// overlayVisibleMsg tells the daemon this page shows or hides an overlay
// (overlay_visible drives its idle reaper); a read-only page sends nothing.
export function overlayVisibleMsg(readOnly: boolean, paneId: string, visible: boolean): Message | null {
  return readOnly ? null : { type: 'update_pane', payload: { pane_id: paneId, overlay_visible: visible } };
}

// OverlayClaim keeps the daemon's picture of what this socket shows in step
// with the screen. overlay_visible is a CLAIM per connection, and an overlay
// no client claims is evicted after the idle timeout, so every path that
// changes what is on screen must report it (plugins.md): a toggle, a tab or
// project change (the old tab's overlay leaves the screen and must be
// withdrawn), and a new socket — the daemon dropped the old one's claims with
// its connection, so the overlay still on screen is claimed again.
export class OverlayClaim {
  private claimed = '';

  // reconcile returns the messages that move the claim to onScreen ('' for
  // none). A withdrawn overlay that is gone (live false) needs no message:
  // its claim went with the pane. A read-only page claims nothing.
  reconcile(onScreen: string, readOnly: boolean, live: (id: string) => boolean): Message[] {
    if (onScreen === this.claimed) return [];
    const out: Message[] = [];
    if (this.claimed !== '' && live(this.claimed)) {
      const m = overlayVisibleMsg(readOnly, this.claimed, false);
      if (m) out.push(m);
    }
    if (onScreen !== '') {
      const m = overlayVisibleMsg(readOnly, onScreen, true);
      if (m) out.push(m);
    }
    this.claimed = onScreen;
    return out;
  }

  // forget runs when the socket goes: the daemon holds nothing for the next.
  forget(): void {
    this.claimed = '';
  }
}
