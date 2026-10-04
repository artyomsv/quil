import { describe, expect, it } from 'vitest';
import { cwdForSplit, nextTabColor, projectRootOf, quickSplit, TAB_COLORS, tabColorCss } from './actions';
import type { WorkspaceState } from './protocol';

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
