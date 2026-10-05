import { describe, expect, it } from 'vitest';
import { cwdForSplit, nextTabColor, projectRootOf, quickSplit, splitAnswer, TAB_COLORS, tabColorCss } from './actions';
import type { Clock } from './connection';
import type { WorkspaceState } from './protocol';
import { Requests } from './requests';

const s: WorkspaceState = {
  active_tab: 't1',
  active_project: 'p1',
  projects: [{ id: 'p1', name: 'P', root_dir: '/repo', tab_ids: ['t1'], active_tab: 't1' }],
  tabs: [{ id: 't1', name: '', color: '', panes: ['a'], project_id: 'p1', layout_rev: 3 }],
  panes: [{ id: 'a', tab_id: 't1', cwd: '/repo/sub' }],
};

describe('actions', () => {
  it("a quick split is a terminal at the target pane's folder", () => {
    expect(quickSplit(s, 'a', 'right')).toEqual({ target_pane_id: 'a', placement: 'right', pane: { type: 'terminal', cwd: '/repo/sub' } });
  });

  it('falls back to the project root, and always sends a cwd', () => {
    const t = { ...s, panes: [{ id: 'a', tab_id: 't1', cwd: '' }] };
    expect(cwdForSplit(t, 'a')).toBe('/repo');
    expect(projectRootOf(s, 't1')).toBe('/repo');
  });

  it('a pane that exists but did not start is an answer with a notice, not a refusal', () => {
    // The daemon puts a spawn failure in notice and leaves error empty, so
    // Requests resolves ok: the dialog closes instead of offering a retry.
    const clock: Clock = { setTimeout: () => 0, clearTimeout: () => {}, now: () => 0 };
    const r = new Requests(() => true, clock);
    const p = r.request('split_pane_req', {});
    r.answer({ type: 'split_pane_resp', id: 'req-1', payload: { pane_id: 'p9', tab_id: 't1', layout_rev: 4, notice: 'exec: not found' } });
    return p.then((out) => {
      expect(out.ok).toBe(true);
      expect(splitAnswer(out)).toEqual({ paneId: 'p9', preparing: false, notice: 'exec: not found' });
    });
  });

  it('a refused split has no answer, and a preparing one is followed', () => {
    expect(splitAnswer({ ok: false, code: 'failed', error: 'no such tab' })).toBeNull();
    const ok = { ok: true as const, reply: { type: 'split_pane_resp', payload: { pane_id: 'ph', preparing: true } } };
    expect(splitAnswer(ok)).toEqual({ paneId: 'ph', preparing: true, notice: '' });
  });

  it("offers the TUI's tab colours", () => {
    expect(TAB_COLORS.map((c) => c.value)).toEqual(['', '1', '2', '3', '4', '5', '6', '208']);
    expect(tabColorCss('208')).toBe('#ff8700');
    expect(tabColorCss('99')).toBe('transparent');
  });

  it('nextTabColor cycles through TAB_COLORS and wraps to none', () => {
    expect(nextTabColor('')).toBe('1');
    expect(nextTabColor('208')).toBe('');
    expect(nextTabColor('unknown')).toBe('');
  });
});
