import { describe, expect, it } from 'vitest';
import { FakeClock } from './fakeclock';
import {
  buildPalette,
  filterPalette,
  fuzzyScore,
  type PaletteCtx,
  type PaletteRow,
  PaneSearch,
  rowScore,
  SEARCH_TIMEOUT_MS,
} from './palette';
import type { WorkspaceState } from './protocol';
import type { Outcome } from './requests';

const cmd = (label: string, ...keywords: string[]): PaletteRow => ({ label, keywords, run: { action: 'x' } });

// Ported from internal/tui/palette_test.go TestFuzzyScore_Subsequence.
describe('fuzzyScore (ported)', () => {
  it.each([
    ['empty query matches', '', 'anything', true],
    ['exact substring', 'split', 'Split horizontal', true],
    ['scattered subsequence', 'sph', 'Split horizontal', true],
    ['case insensitive', 'SPLIT', 'split horizontal', true],
    ['not a subsequence', 'xyz', 'Split horizontal', false],
    ['subsequence across words', 'sh', 'Split horizontal', true],
    ['reverse order fails', 'hs', 'Split horizontal', false],
    ['reverse order fails short', 'ts', 'st', false],
  ])('%s', (_n, q, t, want) => {
    expect(fuzzyScore(q, t) !== null).toBe(want);
  });

  // TestFuzzyScore_Ranking.
  it('ranks a consecutive prefix above a scattered match', () => {
    expect(fuzzyScore('spl', 'Split pane')!).toBeGreaterThan(fuzzyScore('spl', 'special loop')!);
  });
  it('scores a word-boundary match positively', () => {
    expect(fuzzyScore('h', 'Split horizontal')!).toBeGreaterThan(0);
  });
  // Exact numbers pin the port to the Go arithmetic (gain -= ti/8, floor 1).
  it('matches the Go scores', () => {
    expect(fuzzyScore('', 'x')).toBe(0);
    // s@0: 1+5 (start); p@1: 1+4 (consecutive); l@2: 1+4 → 6+5+5
    expect(fuzzyScore('spl', 'Split pane')).toBe(16);
    // h@6 after a space: 1+3 = 4
    expect(fuzzyScore('h', 'Split horizontal')).toBe(4);
    // z@16: 1 - 2 → floored at 1
    expect(fuzzyScore('z', 'aaaaaaaaaaaaaaaaz')).toBe(1);
  });
});

// TestCommandScore_BestOfLabelAndKeywords.
describe('rowScore', () => {
  it('matches a keyword the label lacks', () => {
    expect(rowScore('hsplit', cmd('Split horizontal', 'hsplit', 'wide'))).not.toBeNull();
    expect(rowScore('zzz', cmd('Split horizontal', 'hsplit', 'wide'))).toBeNull();
  });
});

describe('filterPalette (ported)', () => {
  // TestFilterPalette_EmptyReturnsAllInOrder.
  it('returns every row, headers included, for an empty query', () => {
    const rows = [{ header: true, label: 'Tabs' }, cmd('Alpha'), cmd('Beta')];
    expect(filterPalette('', rows)).toEqual(rows);
  });
  // TestFilterPalette_RanksAndStableTies.
  it('drops headers and keeps registry order on equal scores', () => {
    const got = filterPalette('close', [{ header: true, label: 'Pane' }, cmd('Close pane'), cmd('Close tab'), cmd('Split horizontal')]);
    expect(got.map((r) => r.label)).toEqual(['Close pane', 'Close tab']);
  });
  it('ranks a better match first', () => {
    const got = filterPalette('spl', [cmd('special loop'), cmd('Split pane')]);
    expect(got.map((r) => r.label)).toEqual(['Split pane', 'special loop']);
  });
});

const state: WorkspaceState = {
  active_tab: 't1',
  active_project: 'p1',
  tabs: [
    { id: 't1', name: 'main', color: '', panes: ['a', 'b'], project_id: 'p1', layout_rev: 1 },
    { id: 't2', name: 'logs', color: '', panes: ['c'], project_id: 'p2', layout_rev: 1 },
  ],
  panes: [
    { id: 'a', tab_id: 't1', cwd: '/w/app', type: 'claude-code', name: 'agent' },
    { id: 'b', tab_id: 't1', cwd: '/w/app' },
    { id: 'c', tab_id: 't2', cwd: '/w/ops', type: 'terminal' },
  ],
  projects: [
    { id: 'p1', name: 'App', root_dir: '/w/app', tab_ids: ['t1'], active_tab: 't1' },
    { id: 'p2', name: 'Ops', root_dir: '/w/ops', tab_ids: ['t2'], active_tab: 't2' },
  ],
};

function ctx(over: Partial<PaletteCtx> = {}): PaletteCtx {
  return {
    state,
    activeProject: 'p1',
    activePane: 'a',
    keyFor: (id) => (id === 'pane.split_h' ? 'Alt+H' : ''),
    refusal: () => '',
    extra: [[], [], [], []],
    ...over,
  };
}

describe('buildPalette', () => {
  it('lists the sections in TUI order', () => {
    const heads = buildPalette(ctx())
      .filter((r) => r.header)
      .map((r) => r.label);
    expect(heads).toEqual(['Go to pane', 'Tabs', 'Projects', 'Pane', 'System']);
  });
  it('labels panes as the TUI does, with the project only outside the active one', () => {
    const go = buildPalette(ctx())
      .filter((r) => r.run && 'goPane' in r.run)
      .map((r) => r.label);
    expect(go).toEqual(['1.1 · claude-code · agent', '1.2 · terminal', '1.1 · terminal · Ops']);
  });
  it('skips overlay panes', () => {
    const s: WorkspaceState = { ...state, panes: state.panes.map((p) => (p.id === 'b' ? { ...p, overlay: true } : p)) };
    const go = buildPalette(ctx({ state: s })).filter((r) => r.run && 'goPane' in r.run);
    expect(go.map((r) => r.label)).toEqual(['1.1 · claude-code · agent', '1.1 · terminal · Ops']);
  });
  it('lists the active project tabs and the other projects', () => {
    const rows = buildPalette(ctx());
    expect(rows.some((r) => r.label === 'Switch to 1:main')).toBe(true);
    expect(rows.some((r) => r.label === 'Switch to 1:logs')).toBe(false);
    expect(rows.some((r) => r.label === 'Switch to Ops')).toBe(true);
    expect(rows.some((r) => r.label === 'Switch to App')).toBe(false);
  });
  it('shows the key of each action row', () => {
    expect(buildPalette(ctx()).find((r) => r.label === 'Split horizontal')?.detail).toBe('Alt+H');
  });
  it('greys every mutating row on a read-only page', () => {
    const rows = buildPalette(ctx({ refusal: (c) => (c === 'view' ? '' : 'read-only connection') }));
    expect(rows.find((r) => r.label === 'Close pane…')?.disabled).toBe('read-only connection');
    expect(rows.find((r) => r.label === 'New tab')?.disabled).toBe('read-only connection');
    expect(rows.find((r) => r.label === 'Keyboard shortcuts')?.disabled ?? '').toBe('');
    // A pane of the shown tab is only focused; one elsewhere needs a switch.
    const go = rows.filter((r) => r.run && 'goPane' in r.run).map((r) => [r.label, r.disabled ?? '']);
    expect(go).toEqual([
      ['1.1 · claude-code · agent', ''],
      ['1.2 · terminal', ''],
      ['1.1 · terminal · Ops', 'read-only connection'],
    ]);
    expect(rows.find((r) => r.label === 'Switch to 1:main')?.disabled).toBe('read-only connection');
    expect(rows.find((r) => r.label === 'Switch to Ops')?.disabled).toBe('read-only connection');
  });
  it('greys pane rows when no pane is active', () => {
    const rows = buildPalette(ctx({ activePane: '' }));
    expect(rows.find((r) => r.label === 'Split vertical')?.disabled).toBe('no active pane');
    expect(rows.find((r) => r.label === 'New tab')?.disabled ?? '').toBe('');
  });
  it('names the mute row from the active pane', () => {
    const s: WorkspaceState = { ...state, panes: state.panes.map((p) => (p.id === 'a' ? { ...p, muted: true } : p)) };
    expect(buildPalette(ctx({ state: s })).some((r) => r.label === 'Unmute notifications')).toBe(true);
  });
  it('adds the later tasks rows at the end of their section', () => {
    const rows = buildPalette(ctx({ extra: [[], [], [cmd('Toggle notes')], []] }));
    const i = rows.findIndex((r) => r.label === 'Toggle notes');
    expect(rows[i + 1]?.label).toBe('System');
  });
  it('puts New from template before New tab, as the TUI does', () => {
    const rows = buildPalette(ctx({ extra: [[cmd('New from template'), cmd('Move tab to project…')], [], [], []] }));
    const labels = rows.map((r) => r.label);
    expect(labels.indexOf('New from template')).toBe(labels.indexOf('New tab') - 1);
    expect(labels.indexOf('Move tab to project…')).toBe(labels.indexOf('Cycle tab color') + 1);
  });
});

describe('PaneSearch', () => {
  function setup(answer: (q: string) => Outcome | null) {
    const clock = new FakeClock();
    const sent: string[] = [];
    const pending: ((o: Outcome) => void)[] = [];
    const s = new PaneSearch(clock, (q) => {
      sent.push(q);
      const a = answer(q);
      return a ? Promise.resolve(a) : new Promise((r) => pending.push(r));
    });
    return { clock, sent, s, release: (i: number, o: Outcome) => pending[i]?.(o) };
  }
  const reply = (query: string, hits: unknown[]): Outcome => ({
    ok: true,
    reply: { type: 'pane_search_resp', payload: { query, hits } },
  });
  const flush = async (): Promise<void> => {
    for (let i = 0; i < 4; i++) await Promise.resolve();
  };

  it('waits 150 ms after the last keystroke', () => {
    const { clock, sent, s } = setup(() => null);
    s.query('a');
    clock.advance(100);
    s.query('ab');
    clock.advance(149);
    expect(sent).toEqual([]);
    clock.advance(1);
    expect(sent).toEqual(['ab']);
  });
  it('searches nothing for a blank query', () => {
    const { clock, sent, s } = setup(() => null);
    s.query('   ');
    clock.advance(500);
    expect(sent).toEqual([]);
  });
  it('drops an answer for an older query', async () => {
    const { clock, s, release } = setup(() => null);
    s.query('old');
    clock.advance(150);
    s.query('new');
    release(0, reply('old', [{ pane_id: 'a', matches: 2, excerpt: 'x' }]));
    await flush();
    expect(s.hits).toEqual([]);
  });
  it('shows the hits of the current query', async () => {
    const { clock, s } = setup((q) => reply(q, [{ pane_id: 'a', matches: 2, excerpt: 'x' }]));
    s.query('q');
    clock.advance(150);
    await flush();
    expect(s.hits).toEqual([{ pane_id: 'a', matches: 2, excerpt: 'x' }]);
    expect(s.status).toBe('');
  });
  it('times out after 3 s', () => {
    const { clock, s } = setup(() => null);
    s.query('q');
    clock.advance(150);
    expect(s.status).toBe('searching');
    clock.advance(SEARCH_TIMEOUT_MS);
    expect(s.status).toBe('timed_out');
  });
  it('clears the hits when the query is emptied', async () => {
    const { clock, s } = setup((q) => reply(q, [{ pane_id: 'a', matches: 1, excerpt: 'x' }]));
    s.query('q');
    clock.advance(150);
    await flush();
    s.query('');
    expect(s.hits).toEqual([]);
    expect(s.status).toBe('');
  });
});
