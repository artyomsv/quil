import { describe, expect, it } from 'vitest';
import { MAX_REPO_CHOICES, overlayOf, overlayRepoChoice, overlayToggle, overlayVisibleMsg } from './overlay';
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
    expect(overlayOf(s, 't1')).toEqual({ id: 'o1', kind: 'lazygit', cwd: '/r', spawnError: '' });
  });
  it('carries a sanitized spawn error', () => {
    const s = state([{ id: 'o1', tab_id: 't1', cwd: '/r', type: 'lazygit', overlay: true, spawn_error: 'not\u001b found' }]);
    expect(overlayOf(s, 't1')?.spawnError).toBe('not found');
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
  const lg = { id: 'o1', kind: 'lazygit', cwd: '/r', spawnError: '' };

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

  it('hides a shown overlay of that kind without asking the daemon', () => {
    expect(overlayToggle(lg, 'o1', 'lazygit', true)).toEqual({ do: 'hide', id: 'o1' });
  });

  it('asks which repository first on an editable page, naming the overlay to reuse and the one to hide', () => {
    expect(overlayToggle(null, undefined, 'hunk', true)).toEqual({ do: 'discover', existing: '', hide: '' });
    expect(overlayToggle(lg, 'o1', 'hunk', true)).toEqual({ do: 'discover', existing: '', hide: 'o1' });
    // An overlay of this kind not shown here may target another repository.
    expect(overlayToggle(lg, undefined, 'lazygit', true)).toEqual({ do: 'discover', existing: 'o1', hide: '' });
    // An overlay another client opened, not shown here, is not "shown".
    expect(overlayToggle(lg, 'old', 'hunk', true)).toEqual({ do: 'discover', existing: '', hide: '' });
  });

  it('tells the daemon when an editable page shows or hides', () => {
    expect(overlayVisibleMsg(false, 'o1', true)).toEqual({ type: 'update_pane', payload: { pane_id: 'o1', overlay_visible: true } });
  });
});

// The TUI's resolveOverlay steps 3-7 (internal/tui/overlay.go).
describe('overlayRepoChoice', () => {
  const lg = { id: 'o1', kind: 'lazygit', cwd: '/a', spawnError: '' };

  it('with no repository shows the existing overlay, else says none', () => {
    expect(overlayRepoChoice([], lg)).toEqual({ do: 'show' });
    expect(overlayRepoChoice([], null)).toEqual({ do: 'none' });
  });

  it('shows an overlay already on one of the repositories', () => {
    expect(overlayRepoChoice(['/b', '/a'], lg)).toEqual({ do: 'show' });
  });

  it('replaces an overlay on another repository', () => {
    expect(overlayRepoChoice(['/b'], lg)).toEqual({ do: 'create', repo: '/b' });
    expect(overlayRepoChoice(['/b'], null)).toEqual({ do: 'create', repo: '/b' });
  });

  it('offers a picker for several repositories, capped', () => {
    expect(overlayRepoChoice(['/b', '/c'], lg)).toEqual({ do: 'pick', repos: ['/b', '/c'] });
    const many = Array.from({ length: MAX_REPO_CHOICES + 5 }, (_, i) => `/r${i}`);
    const got = overlayRepoChoice(many, null);
    expect(got.do === 'pick' && got.repos.length).toBe(MAX_REPO_CHOICES);
  });
});
