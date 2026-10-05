import { describe, expect, it } from 'vitest';
import type { WebKeymap } from './engine';
import { keyFor, keyTarget } from './labels';

const km: WebKeymap = {
  preset: 'default',
  prefix: '',
  timeout_ms: 0,
  group_order: [],
  conflicts: [],
  actions: [
    { id: 'pane.split_h', label: 'Split', group: 'Panes', tier: 'late', keys: ['alt+shift+h'] },
    { id: 'pane.close', label: 'Close', group: 'Panes', tier: 'late', keys: ['alt+shift+c'], fallback: 'alt+shift+c' },
    { id: 'pane.rename', label: 'Rename', group: 'Panes', tier: 'late', keys: ['alt+f2', 'alt+shift+r'] },
    { id: 'tab.new', label: 'New tab', group: 'Tabs', tier: 'late', keys: [], fallback_unavailable: true },
  ],
  builtins: [{ id: 'new_pane', label: 'New pane', keys: ['alt+shift+o'], fallback: 'alt+shift+o' }],
};

describe('keyFor', () => {
  it('shows the key that works in this browser', () => {
    expect(keyFor(km, 'pane.split_h')).toBe('alt+shift+h');
    expect(keyFor(km, 'pane.close')).toBe('alt+shift+c');
    expect(keyFor(km, 'pane.rename')).toBe('alt+f2');
    expect(keyFor(km, 'builtin.new_pane')).toBe('alt+shift+o');
  });
  it('shows nothing when no key works here', () => {
    expect(keyFor(km, 'tab.new')).toBe('');
    expect(keyFor(km, 'no.such')).toBe('');
    expect(keyFor(km, 'builtin.help')).toBe('');
    expect(keyFor(null, 'pane.split_h')).toBe('');
  });
});

describe('keyTarget', () => {
  it('sends keys to a shown overlay, flagged so only overlay keys run', () => {
    expect(keyTarget('o1', 'p1')).toEqual({ pane: 'o1', overlay: true });
  });
  it('sends keys to the active pane otherwise', () => {
    expect(keyTarget(undefined, 'p1')).toEqual({ pane: 'p1', overlay: false });
  });
});
