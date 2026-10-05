import { describe, expect, it } from 'vitest';
import { askedTabShown, JUMP_TTL_MS, jumpStep, pickActive, resolveJump, successorOf, UnseenAsks, unseenToClear } from './activepane';
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

describe('UnseenAsks', () => {
  it('asks once per mark, and again after a refusal, a cleared mark or a reset', () => {
    const u = new UnseenAsks();
    expect(u.next(state(true), 'b')).toBe('b');
    expect(u.next(state(true), 'b')).toBeNull();
    u.answered('b', false);
    expect(u.next(state(true), 'b')).toBe('b');
    u.answered('b', true);
    expect(u.next(state(true), 'b')).toBeNull();
    u.reset();
    expect(u.next(state(true), 'b')).toBe('b');
    u.stateApplied(state(false));
    expect(u.has('b')).toBe(false);
  });
});

describe('jumpStep and resolveJump', () => {
  const two = (active: string): WorkspaceState => ({
    active_tab: active,
    active_project: '',
    projects: [],
    tabs: [
      { id: 't1', name: '', color: '', panes: ['a'], project_id: '', layout_rev: 1 },
      { id: 't2', name: '', color: '', panes: ['c'], project_id: '', layout_rev: 1 },
    ],
    panes: [
      { id: 'a', tab_id: 't1', cwd: '/' },
      { id: 'c', tab_id: 't2', cwd: '/' },
    ],
  });

  it('activates a placed pane of the active tab at once', () => {
    expect(jumpStep(two('t1'), 't1', ['a'], 't1', 'a', false, 0)).toEqual({ switch: '', activate: 'a', pending: null });
    // Not placed (an overlay, or gone): nothing.
    expect(jumpStep(two('t1'), 't1', ['a'], 't1', 'zz', false, 0)).toEqual({ switch: '', activate: '', pending: null });
  });

  it('switches and waits for the target tab before activating its pane', () => {
    const step = jumpStep(two('t1'), 't1', ['a'], 't2', 'c', false, 100);
    expect(step).toEqual({ switch: 't2', activate: '', pending: { from: 't1', tab: 't2', pane: 'c', at: 100 } });
    // A state from before the switch keeps the jump waiting.
    const early = resolveJump(step.pending, two('t1'), ['a'], 200);
    expect(early).toEqual({ activate: '', keep: step.pending });
    expect(resolveJump(early.keep, two('t2'), ['c'], 300)).toEqual({ activate: 'c', keep: null });
  });

  it('drops the jump when another tab shows up, or the pane is not placed', () => {
    const j = { from: 't1', tab: 't2', pane: 'c', at: 0 };
    expect(resolveJump(j, two('t3'), [], 10)).toEqual({ activate: '', keep: null });
    expect(resolveJump(j, two('t2'), [], 10)).toEqual({ activate: '', keep: null });
  });

  it('drops the jump when its tab leaves the state, or after JUMP_TTL_MS', () => {
    const j = { from: 't1', tab: 't2', pane: 'c', at: 0 };
    const gone = { ...two('t1'), tabs: two('t1').tabs.filter((t) => t.id !== 't2') };
    expect(resolveJump(j, gone, ['a'], 10)).toEqual({ activate: '', keep: null });
    // Still the old tab, but too late: a switch that never came.
    expect(resolveJump(j, two('t1'), ['a'], JUMP_TTL_MS + 1)).toEqual({ activate: '', keep: null });
    expect(resolveJump(j, two('t2'), ['c'], JUMP_TTL_MS + 1)).toEqual({ activate: '', keep: null });
  });

  it('a read-only page never jumps to another tab', () => {
    expect(jumpStep(two('t1'), 't1', ['a'], 't2', 'c', true, 0)).toEqual({ switch: '', activate: '', pending: null });
  });
});
