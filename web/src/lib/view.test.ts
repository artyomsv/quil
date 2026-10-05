import { describe, expect, it } from 'vitest';
import type { WorkspaceState } from './protocol';
import { activeProjectOf, activeTree, parseWorkspaceState, placedPanes, sidebarModel, tabBarModel } from './view';

const ESC = String.fromCodePoint(0x1b);
const RLO = String.fromCodePoint(0x202e);

function ws(): WorkspaceState {
  return {
    active_tab: 't2',
    active_project: 'p1',
    projects: [
      { id: 'p1', name: 'one', root_dir: '/r1', tab_ids: ['t1', 't2'], active_tab: 't2' },
      { id: 'p2', name: `tw${RLO}o`, root_dir: '/r2', tab_ids: ['t3'], active_tab: 't3' },
    ],
    tabs: [
      { id: 't1', name: 'a', color: '', panes: ['x1'], project_id: 'p1', layout_rev: 0 },
      {
        id: 't2',
        name: `b${ESC}[2J`,
        color: '',
        panes: ['x2', 'x3', 'ov'],
        project_id: 'p1',
        layout_rev: 1,
        layout: { split: 0, ratio: 0.25, left: { pane_id: 'x2' }, right: { pane_id: 'x3' } },
      },
      { id: 't3', name: 'c', color: '', panes: ['x4'], project_id: 'p2', layout_rev: 0 },
      { id: 't4', name: 'lost', color: '', panes: [], project_id: 'gone', layout_rev: 0 },
    ],
    panes: [
      { id: 'x1', tab_id: 't1', cwd: '/', name: 'shell' },
      { id: 'x2', tab_id: 't2', cwd: '/', type: 'claude-code' },
      { id: 'x3', tab_id: 't2', cwd: '/', spawn_error: `no${ESC} such file` },
      { id: 'ov', tab_id: 't2', cwd: '/', overlay: true },
      { id: 'x4', tab_id: 't3', cwd: '/' },
    ],
  };
}

describe('parseWorkspaceState', () => {
  it('refuses a payload that is not an object or has a non-list field', () => {
    for (const p of [null, undefined, 'x', 3, [], { tabs: {} }, { panes: 'x' }, { projects: 1 }]) {
      expect(parseWorkspaceState(p)).toBeNull();
    }
  });

  it('keeps the recent folders, strings only', () => {
    const s = parseWorkspaceState({ active_tab: 't', tabs: [], panes: [], projects: [], recent_cwds: ['/a', 3, '/b'] });
    expect(s?.recent_cwds).toEqual(['/a', '/b']);
    expect(parseWorkspaceState({ active_tab: 't', tabs: [], panes: [], projects: [] })?.recent_cwds).toBeUndefined();
  });

  it('reads null lists as empty', () => {
    const s = parseWorkspaceState({ active_tab: 't', tabs: null, panes: null, projects: null });
    expect(s).toEqual({ active_tab: 't', active_project: '', tabs: [], panes: [], projects: [] });
  });

  it('normalizes nested null lists and a null layout, and keeps the numbering', () => {
    const s = parseWorkspaceState({
      active_tab: 't',
      tabs: [{ id: 't', name: 'n', panes: null, project_id: 'p', layout: null }],
      panes: [{ id: 'x' }],
      projects: [{ id: 'p', name: 'p', tab_ids: null }],
      size_master: 'web-1',
      rev: 4,
      run_id: 'r',
    });
    expect(s?.tabs[0]?.panes).toEqual([]);
    expect(s?.tabs[0]?.layout).toBeUndefined();
    expect(s?.projects[0]?.tab_ids).toEqual([]);
    expect(s?.panes[0]?.tab_id).toBe('');
    expect([s?.size_master, s?.rev, s?.run_id]).toEqual(['web-1', 4, 'r']);
  });

  it('drops list entries without a string id', () => {
    const s = parseWorkspaceState({ tabs: [{ id: 't' }, { name: 'x' }, 7, null], panes: [], projects: [] });
    expect(s?.tabs.map((t) => t.id)).toEqual(['t']);
  });
});

describe('sidebarModel', () => {
  it('lists projects and their tabs in daemon order, names sanitized', () => {
    const m = sidebarModel(ws(), {});
    expect(m.map((p) => [p.id, p.name, p.active])).toEqual([
      ['p1', 'one', true],
      ['p2', 'two', false],
      ['', 'Other tabs', false],
    ]);
    expect(m[0]?.tabs.map((t) => [t.id, t.name, t.active])).toEqual([
      ['t1', 'a', false],
      ['t2', 'b[2J', true],
    ]);
    expect(m[2]?.tabs.map((t) => t.id)).toEqual(['t4']);
  });

  it('gives each non-overlay pane a dot with its agent state', () => {
    const m = sidebarModel(ws(), { x2: 'working', x3: 'weird' });
    expect(m[0]?.tabs[1]?.dots).toEqual([
      { id: 'x2', name: 'claude-code', state: 'working' },
      { id: 'x3', name: 'pane', state: 'unknown' },
    ]);
  });

  it('is empty before any state', () => {
    expect(sidebarModel(null, {})).toEqual([]);
  });
});

describe('tabBarModel and activeProjectOf', () => {
  it('follows the project of the daemon active tab', () => {
    const s = ws();
    s.active_tab = 't3';
    expect(activeProjectOf(s)).toBe('p2');
    expect(tabBarModel(s, {}).map((t) => t.id)).toEqual(['t3']);
  });
});

describe('placedPanes', () => {
  it('places the active tab panes by the stored tree, overlays left out', () => {
    const got = placedPanes(ws());
    expect(got.map((p) => p.id)).toEqual(['x2', 'x3']);
    expect(got[0]?.rect).toEqual({ x: 0, y: 0, w: 0.25, h: 1 });
    expect(got[1]?.rect).toEqual({ x: 0.25, y: 0, w: 0.75, h: 1 });
    expect(got[1]?.spawnError).toBe('no such file');
  });

  it('carries the mute and worktree marks', () => {
    const s = ws();
    s.panes = s.panes.map((p) => (p.id === 'x2' ? { ...p, muted: true, worktree_owned: true } : p));
    const got = placedPanes(s);
    expect(got.map((p) => [p.muted, p.worktreeOwned])).toEqual([
      [true, true],
      [false, false],
    ]);
  });

  it('gives each placed pane its agent dot, unknown for anything else', () => {
    expect(placedPanes(ws(), null, { x2: 'blocked', x3: 'weird' }).map((p) => p.agent)).toEqual(['blocked', 'unknown']);
    expect(placedPanes(ws(), null, { x2: 'working', x3: 'idle' }).map((p) => p.agent)).toEqual(['working', 'idle']);
    expect(placedPanes(ws()).map((p) => p.agent)).toEqual(['unknown', 'unknown']);
  });

  it('draws a preview for the active tab in place of its tree; one for another tab changes nothing', () => {
    const tree = { split: 0, ratio: 0.6, left: { pane_id: 'x2' }, right: { pane_id: 'x3' } };
    const moved = placedPanes(ws(), { tabId: 't2', tree });
    expect(moved[0]?.rect.w).toBeCloseTo(0.6);
    expect(activeTree(ws(), { tabId: 't2', tree })?.ratio).toBe(0.6);
    const other = placedPanes(ws(), { tabId: 't1', tree });
    expect(other[0]?.rect).toEqual({ x: 0, y: 0, w: 0.25, h: 1 });
    expect(activeTree(ws(), null)?.ratio).toBe(0.25);
    expect(activeTree(null)).toBeUndefined();
  });

  it('is empty when the active tab is unknown or has no panes', () => {
    const s = ws();
    s.active_tab = 't4';
    expect(placedPanes(s)).toEqual([]);
    s.active_tab = 'nope';
    expect(placedPanes(s)).toEqual([]);
  });
});
