import { describe, expect, it } from 'vitest';
import { pickActive, unseenToClear } from './activepane';
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
