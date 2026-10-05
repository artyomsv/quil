import { describe, expect, it } from 'vitest';
import { overlayOf } from './overlay';
import type { WorkspaceState } from './protocol';

const state = (panes: WorkspaceState['panes']): WorkspaceState => ({
  active_tab: 't1',
  active_project: '',
  projects: [],
  tabs: [{ id: 't1', name: 't', color: '', panes: panes.map((p) => p.id), project_id: '', layout_rev: 0 }],
  panes,
});

describe('overlayOf', () => {
  it('finds the tab overlay and its kind', () => {
    const s = state([
      { id: 'p1', tab_id: 't1', cwd: '/r' },
      { id: 'o1', tab_id: 't1', cwd: '/r', type: 'lazygit', overlay: true },
    ]);
    expect(overlayOf(s, 't1')).toEqual({ id: 'o1', kind: 'lazygit', cwd: '/r' });
  });
  it('answers null when the overlay left the state', () => {
    expect(overlayOf(state([{ id: 'p1', tab_id: 't1', cwd: '/r' }]), 't1')).toBeNull();
    expect(overlayOf(null, 't1')).toBeNull();
  });
  it("never answers another tab's overlay", () => {
    const s = state([{ id: 'o2', tab_id: 't2', cwd: '/r', type: 'hunk', overlay: true }]);
    expect(overlayOf(s, 't1')).toBeNull();
  });
});
