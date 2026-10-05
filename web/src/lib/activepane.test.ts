import { describe, expect, it } from 'vitest';
import { askedTabShown, pickActive, successorOf, unseenToClear } from './activepane';
import type { WorkspaceState } from './protocol';

const state = (unseen: boolean): WorkspaceState => ({
  active_tab: 't1',
  active_project: '',
  projects: [],
  tabs: [{ id: 't1', name: '', color: '', panes: ['a', 'b'], project_id: '', layout_rev: 1 }],
  panes: [
    { id: 'a', tab_id: 't1', cwd: '/' },
    { id: 'b', tab_id: 't1', cwd: '/', unseen },
  ],
});

describe('pickActive', () => {
  it('keeps a still-placed active pane, else takes the first placed', () => {
    expect(pickActive('b', ['a', 'b'])).toBe('b');
    expect(pickActive('gone', ['a', 'b'])).toBe('a');
    expect(pickActive('x', [])).toBe('');
  });
});

describe('unseenToClear', () => {
  it('names the active pane once while it is marked unseen', () => {
    const asked = new Set<string>();
    expect(unseenToClear(state(true), 'b', asked)).toBe('b');
    asked.add('b');
    expect(unseenToClear(state(true), 'b', asked)).toBeNull();
    expect(unseenToClear(state(false), 'b', new Set())).toBeNull();
    expect(unseenToClear(state(true), 'a', new Set())).toBeNull();
    expect(unseenToClear(state(true), '', new Set())).toBeNull();
  });
});

describe('askedTabShown', () => {
  it('is true only while the tab exists', () => {
    expect(askedTabShown(state(false), 't1')).toBe(true);
    expect(askedTabShown(state(false), 'gone')).toBe(false);
  });
});

describe('successorOf', () => {
  const tree = (right: string) => ({ split: 0, ratio: 0.5, left: { pane_id: 'a' }, right: { split: 1, ratio: 0.3, left: { pane_id: right }, right: { pane_id: 'c' } } });
  const ws = (right: string, panes: string[]): WorkspaceState => ({
    active_tab: 't1',
    active_project: '',
    projects: [],
    tabs: [{ id: 't1', name: '', color: '', panes, project_id: '', layout_rev: 1, layout: tree(right) }],
    panes: panes.map((id) => ({ id, tab_id: 't1', cwd: '/' })),
  });

  it('is the new pane in the slot a replaced (or placeholder) pane left', () => {
    expect(successorOf(ws('ph', ['a', 'ph', 'c']), ws('new', ['a', 'new', 'c']), 'ph')).toBe('new');
  });

  it('is nothing when the pane is still there, the slot holds an old pane, or there was no state', () => {
    expect(successorOf(ws('ph', ['a', 'ph', 'c']), ws('ph', ['a', 'ph', 'c']), 'ph')).toBe('');
    const pruned = ws('ph', ['a', 'c']);
    pruned.tabs[0]!.layout = { split: 0, ratio: 0.5, left: { pane_id: 'a' }, right: { pane_id: 'c' } };
    expect(successorOf(ws('ph', ['a', 'ph', 'c']), pruned, 'ph')).toBe('');
    expect(successorOf(null, ws('new', ['a', 'new', 'c']), 'ph')).toBe('');
  });
});
