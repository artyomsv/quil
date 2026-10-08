import type { Clock } from './connection';
import type { Panel } from './panels';
import type { PaneSearchHit, PaneSearchResp, WorkspaceState } from './protocol';
import type { Outcome } from './requests';
import type { MsgClass } from './rights';
import { sanitizeRemoteText } from './sanitize';

export const SEARCH_DEBOUNCE_MS = 150;
export const SEARCH_TIMEOUT_MS = 3000;

export type PaletteRun =
  | { action: string }
  | { goPane: string }
  | { switchTab: string }
  | { switchProject: string }
  | { panel: Panel };

export interface PaletteRow {
  header?: boolean;
  label: string;
  // The key that runs the same action here, or the hit count.
  detail?: string;
  keywords?: string[];
  run?: PaletteRun;
  // Why the row cannot run now; absent or '' = it can.
  disabled?: string;
  // A content-search hit's excerpt (already cleaned).
  excerpt?: string;
}

export interface PaletteCtx {
  state: WorkspaceState;
  activeProject: string;
  activePane: string;
  keyFor(id: string): string;
  refusal(c: MsgClass): string;
  // Rows later screens add at the end of Tabs, Projects, Pane and System.
  extra: PaletteRow[][];
}

function isSeparator(ch: string): boolean {
  return ' :-_./'.includes(ch) || /\s/u.test(ch);
}

// fuzzyScore is internal/tui/palette.go fuzzyScore: a case-insensitive greedy
// subsequence match; null when query is not a subsequence of target.
export function fuzzyScore(query: string, target: string): number | null {
  if (query === '') return 0;
  const q = Array.from(query.toLowerCase());
  const t = Array.from(target.toLowerCase());
  let score = 0;
  let qi = 0;
  let prevMatch = -2;
  for (let ti = 0; ti < t.length && qi < q.length; ti++) {
    if (t[ti] !== q[qi]) continue;
    let gain = 1;
    if (ti === 0) gain += 5;
    else if (isSeparator(t[ti - 1] ?? '')) gain += 3;
    if (ti === prevMatch + 1) gain += 4;
    gain -= Math.trunc(ti / 8);
    if (gain < 1) gain = 1;
    score += gain;
    prevMatch = ti;
    qi++;
  }
  return qi < q.length ? null : score;
}

// rowScore is commandScore: the best of the label and every keyword.
export function rowScore(query: string, r: PaletteRow): number | null {
  let best = fuzzyScore(query, r.label);
  for (const kw of r.keywords ?? []) {
    const s = fuzzyScore(query, kw);
    if (s !== null && (best === null || s > best)) best = s;
  }
  return best;
}

// filterPalette is the TUI's: an empty query browses every row with its
// headers; a query drops headers and sorts by score, stable on ties.
export function filterPalette(query: string, rows: PaletteRow[]): PaletteRow[] {
  if (query === '') return rows.slice();
  const scored: { r: PaletteRow; s: number; i: number }[] = [];
  rows.forEach((r, i) => {
    if (r.header) return;
    const s = rowScore(query, r);
    if (s !== null) scored.push({ r, s, i });
  });
  scored.sort((a, b) => b.s - a.s || a.i - b.i);
  return scored.map((x) => x.r);
}

function base(p: string): string {
  const t = p.replace(/[\\/]+$/, '');
  const i = Math.max(t.lastIndexOf('/'), t.lastIndexOf('\\'));
  return i >= 0 ? t.slice(i + 1) : t;
}

// paneLabel is formatPaneNav: "tab.pane · type[ · name][ · project]".
export function paneLabel(tabIdx: number, paneIdx: number, type: string, name: string, project: string): string {
  const parts = [`${tabIdx + 1}.${paneIdx + 1}`, type || 'terminal'];
  if (name) parts.push(sanitizeRemoteText(name));
  if (project) parts.push(sanitizeRemoteText(project));
  return parts.join(' · ');
}

// buildPalette is buildPaletteCommands reduced to what the browser runs.
export function buildPalette(c: PaletteCtx): PaletteRow[] {
  const s = c.state;
  const rows: PaletteRow[] = [];
  const noPane = c.activePane === '' ? 'no active pane' : '';
  const action = (label: string, id: string, keywords: string[] = [], cls: MsgClass = 'act', paneScoped = false): PaletteRow => ({
    label,
    detail: c.keyFor(id),
    keywords,
    run: { action: id },
    disabled: (cls === 'view' ? '' : c.refusal(cls)) || (paneScoped ? noPane : ''),
  });
  const panes = new Map(s.panes.map((p) => [p.id, p]));
  const tabs = new Map(s.tabs.map((t) => [t.id, t]));

  rows.push({ header: true, label: 'Go to pane' });
  for (const proj of s.projects) {
    const projName = proj.id === c.activeProject ? '' : proj.name;
    proj.tab_ids.forEach((tid, i) => {
      const t = tabs.get(tid);
      if (!t) return;
      let j = 0;
      for (const pid of t.panes) {
        const p = panes.get(pid);
        if (!p || p.overlay) continue;
        const type = p.type || 'terminal';
        rows.push({
          label: paneLabel(i, j, type, p.name ?? '', projName),
          keywords: ['go to', 'goto', 'pane', 'focus', p.name ?? '', base(p.cwd), type, proj.name],
          run: { goPane: p.id },
          // Showing another tab is a switch_tab (act class); this tab's panes
          // are only focused here.
          disabled: t.id === s.active_tab ? '' : c.refusal('act'),
        });
        j++;
      }
    });
  }

  rows.push({ header: true, label: 'Tabs' });
  const active = s.projects.find((p) => p.id === c.activeProject);
  (active?.tab_ids ?? []).forEach((tid, i) => {
    const t = tabs.get(tid);
    if (t) {
      rows.push({
        label: `Switch to ${i + 1}:${sanitizeRemoteText(t.name)}`,
        keywords: ['tab', 'go to', 'goto', 'switch'],
        run: { switchTab: t.id },
        disabled: c.refusal('act'),
      });
    }
  });
  rows.push(
    ...(c.extra[0] ?? []).filter((r) => r.label === 'New from template'),
    action('New tab', 'tab.new', ['tab', 'create']),
    action('Close tab…', 'tab.close', ['tab', 'close']),
    action('Rename tab', 'tab.rename', ['tab', 'rename']),
    action('Cycle tab color', 'tab.cycle_color', ['tab', 'color']),
    ...(c.extra[0] ?? []).filter((r) => r.label !== 'New from template'),
  );

  rows.push({ header: true, label: 'Projects' });
  for (const p of s.projects) {
    if (p.id === c.activeProject) continue;
    rows.push({
      label: `Switch to ${sanitizeRemoteText(p.name)}`,
      keywords: ['project', 'switch', 'go to', 'goto', p.name],
      run: { switchProject: p.id },
      disabled: c.refusal('act'),
    });
  }
  rows.push(...(c.extra[1] ?? []));

  rows.push({ header: true, label: 'Pane' });
  const muted = panes.get(c.activePane)?.muted === true;
  rows.push(
    action('Split horizontal', 'pane.split_h', ['hsplit', 'horizontal'], 'act', true),
    action('Split vertical', 'pane.split_v', ['vsplit', 'vertical'], 'act', true),
    action('Rename pane', 'pane.rename', [], 'act', true),
    action(muted ? 'Unmute notifications' : 'Mute notifications', 'pane.mute', ['mute', 'silence', 'notification'], 'act', true),
    action('Open lazygit', 'pane.toggle_lazygit', ['git', 'lazygit'], 'act', true),
    action('Open hunk', 'pane.toggle_hunk', ['git', 'hunk', 'diff', 'review'], 'act', true),
    action('New pane…', 'builtin.new_pane', ['create', 'plugin', 'claude', 'terminal']),
    action('Restart pane…', 'pane.restart', ['restart', 'respawn'], 'act', true),
    action('Close pane…', 'pane.close', ['close', 'kill'], 'act', true),
    ...(c.extra[2] ?? []),
  );

  rows.push({ header: true, label: 'System' });
  rows.push(
    action('Keyboard shortcuts', 'system.shortcuts', ['keys', 'bindings', 'help'], 'view'),
    action('Take control (size master)', 'client.take_control', ['master', 'control', 'resize', 'follower']),
    ...(c.extra[3] ?? []),
  );
  return rows;
}

// PaneSearch runs the palette's search in pane output: 150 ms after the last
// keystroke, one pane_search_req, 3 s to answer; an answer for an older
// query is dropped (the TUI's applyPaneSearch rule). onChange tells the
// component to read hits, status and truncated again.
export class PaneSearch {
  hits: PaneSearchHit[] = [];
  status: '' | 'searching' | 'timed_out' | 'failed' = '';
  truncated = false;
  onChange: () => void = () => {};
  private current = '';
  private debounce: unknown = null;
  private timeout: unknown = null;

  constructor(
    private readonly clock: Clock,
    private readonly send: (q: string) => Promise<Outcome>,
  ) {}

  query(q: string): void {
    const changed = q !== this.current;
    this.current = q;
    if (this.debounce !== null) this.clock.clearTimeout(this.debounce);
    this.debounce = null;
    // The old query's hits, status and cap go at once, as the TUI's
    // afterPaletteQueryChange clears them: they are not this query's.
    if (changed || q.trim() === '') this.reset();
    if (q.trim() === '') return;
    this.debounce = this.clock.setTimeout(() => {
      this.debounce = null;
      this.issue(q);
    }, SEARCH_DEBOUNCE_MS);
  }

  stop(): void {
    this.current = '';
    if (this.debounce !== null) this.clock.clearTimeout(this.debounce);
    this.debounce = null;
    this.reset();
  }

  private reset(): void {
    if (this.timeout !== null) this.clock.clearTimeout(this.timeout);
    this.timeout = null;
    this.hits = [];
    this.status = '';
    this.truncated = false;
    this.onChange();
  }

  private issue(q: string): void {
    this.status = 'searching';
    this.onChange();
    if (this.timeout !== null) this.clock.clearTimeout(this.timeout);
    this.timeout = this.clock.setTimeout(() => {
      this.timeout = null;
      if (this.current === q && this.status === 'searching') {
        this.status = 'timed_out';
        this.onChange();
      }
    }, SEARCH_TIMEOUT_MS);
    void this.send(q).then((o) => {
      // The echoed query is the staleness key (palette_search.go): an answer
      // for anything but the current query is dropped.
      if (this.current !== q) return;
      const p = (o.reply?.payload ?? null) as PaneSearchResp | null;
      if (!o.ok || !p || p.query !== q) {
        if (!o.ok && o.code !== 'timeout') {
          if (this.timeout !== null) this.clock.clearTimeout(this.timeout);
          this.timeout = null;
          this.status = 'failed';
          this.onChange();
        }
        return;
      }
      if (this.timeout !== null) this.clock.clearTimeout(this.timeout);
      this.timeout = null;
      this.hits = p.hits ?? [];
      this.truncated = p.truncated === true;
      this.status = '';
      this.onChange();
    });
  }
}
