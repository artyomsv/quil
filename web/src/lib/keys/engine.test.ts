import { describe, expect, it } from 'vitest';
import type { Clock } from '../connection';
import { buildTables, KeyEngine, type KeyContext, type WebKeymap } from './engine';

class FakeClock implements Clock {
  t = 0;
  private next = 1;
  private timers = new Map<number, { at: number; fn: () => void }>();
  setTimeout(fn: () => void, ms: number): unknown {
    const id = this.next++;
    this.timers.set(id, { at: this.t + ms, fn });
    return id;
  }
  clearTimeout(h: unknown): void {
    this.timers.delete(h as number);
  }
  now(): number {
    return this.t;
  }
  advance(ms: number): void {
    this.t += ms;
    for (const [id, tm] of [...this.timers]) {
      if (tm.at <= this.t) {
        this.timers.delete(id);
        tm.fn();
      }
    }
  }
}

const km = (over: Partial<WebKeymap> = {}): WebKeymap => ({
  preset: 'tmux',
  prefix: 'ctrl+b',
  timeout_ms: 0,
  group_order: [],
  conflicts: [],
  builtins: [
    { id: 'help', label: 'Key list', keys: ['f1'] },
    { id: 'new_pane', label: 'New pane', keys: ['alt+shift+o'], fallback: 'alt+shift+o' },
  ],
  actions: [
    { id: 'pane.split_h', label: 'Split', group: 'Panes', tier: 'late', keys: ['ctrl+b %'] },
    { id: 'pane.close', label: 'Close', group: 'Panes', tier: 'late', keys: ['ctrl+b x'] },
    { id: 'notification.toggle', label: 'Notes', group: 'Notifications', tier: 'early', keys: ['alt+n'] },
    { id: 'pane.restart', label: 'Restart', group: 'Panes', tier: 'late', keys: ['alt+r'] },
    { id: 'pane.toggle_lazygit', label: 'Lazygit', group: 'Panes', tier: 'early', keys: ['alt+g'] },
  ],
  ...over,
});

const k = (key: string, code: string, m: Partial<Record<'ctrlKey' | 'altKey' | 'shiftKey' | 'metaKey', boolean>> = {}) => ({
  key,
  code,
  ctrlKey: false,
  altKey: false,
  shiftKey: false,
  metaKey: false,
  ...m,
});
const ctrlB = k('b', 'KeyB', { ctrlKey: true });
const ctx = (over: Partial<KeyContext> = {}): KeyContext => ({ modalOpen: false, activePaneId: 'p1', rawKeys: new Set(), ...over });

function rig(over: Partial<WebKeymap> = {}) {
  const clock = new FakeClock();
  let changes = 0;
  const m = km(over);
  const e = new KeyEngine(buildTables(m), m.timeout_ms, clock, () => changes++);
  return { e, clock, changes: () => changes };
}

describe('KeyEngine', () => {
  it('runs a prefix sequence and consumes both keys', () => {
    const { e } = rig();
    expect(e.handle(ctrlB, ctx())).toEqual({ kind: 'consume' });
    expect(e.pending).toEqual(['ctrl+b']);
    expect(e.handle(k('%', 'Digit5', { shiftKey: true }), ctx())).toEqual({ kind: 'action', id: 'pane.split_h' });
    expect(e.pending).toEqual([]);
  });

  it('runs a completed sequence even when the plugin claims its last chord', () => {
    const { e } = rig();
    e.handle(ctrlB, ctx({ rawKeys: new Set(['x']) }));
    expect(e.handle(k('x', 'KeyX'), ctx({ rawKeys: new Set(['x']) }))).toEqual({ kind: 'action', id: 'pane.close' });
  });

  it('checks early actions before plugin raw keys, and late ones after', () => {
    const { e } = rig();
    const raw = ctx({ rawKeys: new Set(['alt+n', 'alt+r']) });
    expect(e.handle(k('n', 'KeyN', { altKey: true }), raw)).toEqual({ kind: 'action', id: 'notification.toggle' });
    expect(e.handle(k('r', 'KeyR', { altKey: true }), raw)).toEqual({ kind: 'pass' });
    expect(e.handle(k('r', 'KeyR', { altKey: true }), ctx())).toEqual({ kind: 'action', id: 'pane.restart' });
  });

  it('runs builtins after both tiers', () => {
    const { e } = rig();
    expect(e.handle(k('F1', 'F1'), ctx())).toEqual({ kind: 'builtin', id: 'help' });
    expect(e.handle(k('O', 'KeyO', { altKey: true, shiftKey: true }), ctx())).toEqual({ kind: 'builtin', id: 'new_pane' });
  });

  it('passes an unbound key to the terminal', () => {
    const { e } = rig();
    expect(e.handle(k('a', 'KeyA'), ctx())).toEqual({ kind: 'pass' });
  });

  it('lets a dialog own every key and cancels a pending prefix', () => {
    const { e } = rig();
    e.handle(ctrlB, ctx());
    expect(e.handle(k('%', 'Digit5', { shiftKey: true }), ctx({ modalOpen: true }))).toEqual({ kind: 'pass' });
    expect(e.pending).toEqual([]);
  });

  it('ignores a modifier pressed alone, without cancelling', () => {
    const { e } = rig();
    e.handle(ctrlB, ctx());
    expect(e.handle(k('Shift', 'ShiftLeft', { shiftKey: true }), ctx())).toEqual({ kind: 'pass' });
    expect(e.pending).toEqual(['ctrl+b']);
  });

  it('cancels on Esc and consumes it', () => {
    const { e } = rig();
    e.handle(ctrlB, ctx());
    expect(e.handle(k('Escape', 'Escape'), ctx())).toEqual({ kind: 'consume' });
    expect(e.pending).toEqual([]);
  });

  it('sends the prefix itself on prefix prefix', () => {
    const { e } = rig();
    e.handle(ctrlB, ctx());
    expect(e.handle(ctrlB, ctx())).toEqual({ kind: 'pass' });
    expect(e.pending).toEqual([]);
  });

  it('drops an unknown second step with a notice that never shows the typed key', () => {
    const { e } = rig();
    e.handle(ctrlB, ctx());
    expect(e.handle(k('q', 'KeyQ'), ctx())).toEqual({ kind: 'consume' });
    expect(e.hint).toBe('ctrl+b — no such binding');
    expect(e.hint).not.toContain('q');
  });

  it('clears the notice at the next key', () => {
    const { e } = rig();
    e.handle(ctrlB, ctx());
    e.handle(k('q', 'KeyQ'), ctx());
    e.handle(k('a', 'KeyA'), ctx());
    expect(e.hint).toBe('');
  });

  it('cancels when the active pane changed under the prefix', () => {
    const { e } = rig();
    e.handle(ctrlB, ctx({ activePaneId: 'p1' }));
    expect(e.handle(k('%', 'Digit5', { shiftKey: true }), ctx({ activePaneId: 'p2' }))).toEqual({ kind: 'pass' });
  });

  it('cancel() clears and a stale timeout tick is ignored', () => {
    const { e, clock } = rig({ timeout_ms: 500 });
    e.handle(ctrlB, ctx());
    clock.advance(200);
    e.cancel();
    e.handle(ctrlB, ctx());
    clock.advance(400); // the first tick would have fired at 500
    expect(e.pending).toEqual(['ctrl+b']);
    clock.advance(200);
    expect(e.pending).toEqual([]);
  });

  it('never times out with timeout_ms 0', () => {
    const { e, clock } = rig();
    e.handle(ctrlB, ctx());
    clock.advance(60_000);
    expect(e.pending).toEqual(['ctrl+b']);
  });

  it('keeps form fields their keys', () => {
    const { e } = rig();
    expect(e.handle(k('n', 'KeyN', { altKey: true }), ctx({ modalOpen: true }))).toEqual({ kind: 'pass' });
  });

  it('a fallback chord dispatches like any binding', () => {
    const { e } = rig({ actions: [{ id: 'pane.close', label: 'Close', group: 'Panes', tier: 'late', keys: ['alt+shift+c'], fallback: 'alt+shift+c' }] });
    expect(e.handle(k('C', 'KeyC', { altKey: true, shiftKey: true }), ctx())).toEqual({ kind: 'action', id: 'pane.close' });
  });

  it('setKeymap swaps the tables and drops a pending prefix', () => {
    const { e } = rig();
    e.handle(ctrlB, ctx());
    e.setKeymap(km({ actions: [{ id: 'pane.split_h', label: 'Split', group: 'Panes', tier: 'late', keys: ['alt+shift+h'] }] }));
    expect(e.pending).toEqual([]);
    expect(e.handle(ctrlB, ctx())).toEqual({ kind: 'pass' });
    expect(e.handle(k('H', 'KeyH', { altKey: true, shiftKey: true }), ctx())).toEqual({ kind: 'action', id: 'pane.split_h' });
  });

  it('gives a shown overlay every key but its toggles, the panels and alt+1..9', () => {
    const { e } = rig();
    const ov = ctx({ overlay: true, activePaneId: 'o1' });
    expect(e.handle(k('g', 'KeyG', { altKey: true }), ov)).toEqual({ kind: 'action', id: 'pane.toggle_lazygit' });
    expect(e.handle(k('n', 'KeyN', { altKey: true }), ov)).toEqual({ kind: 'action', id: 'notification.toggle' });
    expect(e.handle(k('3', 'Digit3', { altKey: true }), ov)).toEqual({ kind: 'action', id: 'tab.switch_3' });
    expect(e.handle(k('r', 'KeyR', { altKey: true }), ov)).toEqual({ kind: 'pass' });
    expect(e.handle(k('F1', 'F1'), ov)).toEqual({ kind: 'pass' });
    expect(e.handle(k('Escape', 'Escape'), ov)).toEqual({ kind: 'pass' });
  });

  it('keeps the sequence machine inert under an overlay', () => {
    const { e } = rig();
    expect(e.handle(ctrlB, ctx({ overlay: true }))).toEqual({ kind: 'pass' });
    expect(e.pending).toEqual([]);
    e.handle(ctrlB, ctx());
    expect(e.pending).toEqual(['ctrl+b']);
    expect(e.handle(k('%', 'Digit5', { shiftKey: true }), ctx({ overlay: true }))).toEqual({ kind: 'pass' });
    expect(e.pending).toEqual([]);
  });

  it('passes a composing key untouched', () => {
    const { e } = rig();
    e.handle(ctrlB, ctx());
    expect(e.handle({ ...k('x', 'KeyX'), isComposing: true }, ctx())).toEqual({ kind: 'pass' });
    expect(e.pending).toEqual(['ctrl+b']);
  });
});
