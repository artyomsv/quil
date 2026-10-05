import { describe, expect, it } from 'vitest';
import { overlayOf, overlayToggle, overlayVisibleMsg } from './overlay';
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

describe('overlayToggle and overlayVisibleMsg', () => {
  const lg = { id: 'o1', kind: 'lazygit', cwd: '/r' };

  it('lets a read-only page show and hide an existing overlay, sending nothing', () => {
    expect(overlayToggle(lg, undefined, 'lazygit', false)).toEqual({ do: 'show', id: 'o1' });
    expect(overlayToggle(lg, 'o1', 'lazygit', false)).toEqual({ do: 'hide', id: 'o1' });
    expect(overlayVisibleMsg(true, 'o1', true)).toBeNull();
    expect(overlayVisibleMsg(true, 'o1', false)).toBeNull();
  });

  it('never creates from a read-only page', () => {
    expect(overlayToggle(null, undefined, 'lazygit', false)).toEqual({ do: 'refuse' });
    expect(overlayToggle(lg, 'o1', 'hunk', false)).toEqual({ do: 'refuse' });
  });

  it('creates on an editable page, hiding the other tool shown here first', () => {
    expect(overlayToggle(null, undefined, 'hunk', true)).toEqual({ do: 'create', hide: '' });
    expect(overlayToggle(lg, 'o1', 'hunk', true)).toEqual({ do: 'create', hide: 'o1' });
    // An overlay another client opened, not shown here, is not "shown".
    expect(overlayToggle(lg, 'old', 'hunk', true)).toEqual({ do: 'create', hide: '' });
  });

  it('tells the daemon when an editable page shows or hides', () => {
    expect(overlayVisibleMsg(false, 'o1', true)).toEqual({ type: 'update_pane', payload: { pane_id: 'o1', overlay_visible: true } });
  });
});
