import { describe, expect, it } from 'vitest';
import type { SerializedNode } from './protocol';
import type { Outcome } from './requests';
import { clampRatio, ratioFromPointer, SplitDrag, splitBars, withRatio } from './splitbars';

const tree: SerializedNode = {
  split: 0,
  ratio: 0.5,
  left: { pane_id: 'a' },
  right: { split: 1, ratio: 0.25, left: { pane_id: 'b' }, right: { pane_id: 'c' } },
};

describe('splitBars', () => {
  it('lists one bar per inner node with its parent rect and path', () => {
    const bars = splitBars(tree);
    expect(bars).toEqual([
      { path: '', dir: 0, ratio: 0.5, rect: { x: 0, y: 0, w: 1, h: 1 } },
      { path: 'R', dir: 1, ratio: 0.25, rect: { x: 0.5, y: 0, w: 0.5, h: 1 } },
    ]);
  });

  it('a single pane has no bar', () => {
    expect(splitBars({ pane_id: 'a' })).toEqual([]);
    expect(splitBars(undefined)).toEqual([]);
  });
});

describe('withRatio', () => {
  it('changes only the node at path, in a copy', () => {
    const out = withRatio(tree, 'R', 0.6);
    expect(out.right?.ratio).toBe(0.6);
    expect(tree.right?.ratio).toBe(0.25);
    expect(out.left).toEqual({ pane_id: 'a' });
  });
  it('clamps to 0.05..0.95', () => {
    expect(clampRatio(0)).toBe(0.05);
    expect(clampRatio(1)).toBe(0.95);
  });
  it('reads the pointer inside the bar node', () => {
    const [, inner] = splitBars(tree);
    if (!inner) throw new Error('no inner bar');
    expect(ratioFromPointer(inner, 0.9, 0.5)).toBe(0.5);
  });
});

class Clock {
  t = 0;
  n = 1;
  timers = new Map<number, { at: number; fn: () => void }>();
  setTimeout(fn: () => void, ms: number): unknown {
    const id = this.n++;
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

function drag() {
  const sent: { tabId: string; layout: SerializedNode; baseRev: number }[] = [];
  const answers: ((o: Outcome) => void)[] = [];
  const clock = new Clock();
  const d = new SplitDrag(clock, (tabId, layout, baseRev) => {
    sent.push({ tabId, layout, baseRev });
    return new Promise((r) => answers.push(r));
  });
  return { d, sent, clock, answer: (o: Outcome, i = answers.length - 1) => answers[i]?.(o) };
}

describe('SplitDrag', () => {
  it('pins the rev, previews, and sends ONE id-bearing write on release', () => {
    const { d, sent } = drag();
    expect(d.begin('t1', tree, 7, '')).toBe(true);
    d.move(0.6);
    d.move(0.7);
    expect(d.preview?.tree.ratio).toBe(0.7);
    expect(sent).toHaveLength(0);
    d.end();
    expect(sent).toEqual([{ tabId: 't1', layout: withRatio(tree, '', 0.7), baseRev: 7 }]);
  });

  it('refuses a path that is not an inner node', () => {
    const { d } = drag();
    expect(d.begin('t1', tree, 7, 'L')).toBe(false);
    expect(d.begin('t1', tree, 7, 'RRR')).toBe(false);
  });

  it('sends nothing when the ratio did not move', () => {
    const { d, sent } = drag();
    d.begin('t1', tree, 7, '');
    d.end();
    expect(sent).toHaveLength(0);
    expect(d.preview).toBeNull();
  });

  it('a newer rev during the drag cancels it', () => {
    const { d, sent } = drag();
    d.begin('t1', tree, 7, '');
    d.move(0.7);
    d.stateArrived('t2', 9);
    d.stateArrived('t1', 7);
    expect(d.preview).not.toBeNull();
    d.stateArrived('t1', 8);
    expect(d.preview).toBeNull();
    d.end();
    expect(sent).toHaveLength(0);
  });

  it('a higher rev after release confirms; a stale refusal reverts at once', async () => {
    const a = drag();
    a.d.begin('t1', tree, 7, '');
    a.d.move(0.7);
    a.d.end();
    a.d.stateArrived('t1', 8);
    expect(a.d.preview).toBeNull();

    const b = drag();
    b.d.begin('t1', tree, 7, '');
    b.d.move(0.7);
    b.d.end();
    b.answer({ ok: false, code: 'stale', error: 'layout changed' });
    await Promise.resolve();
    expect(b.d.preview).toBeNull();
  });

  it('a preview neither confirmed nor refused within 2 s reverts', () => {
    const { d, clock } = drag();
    d.begin('t1', tree, 7, '');
    d.move(0.7);
    d.end();
    clock.advance(1999);
    expect(d.preview).not.toBeNull();
    clock.advance(1);
    expect(d.preview).toBeNull();
  });

  it('a late refusal of an earlier drag does not end a later one', async () => {
    const { d, clock, answer } = drag();
    d.begin('t1', tree, 7, '');
    d.move(0.7);
    d.end();
    clock.advance(2000);
    d.begin('t1', tree, 7, '');
    d.move(0.3);
    d.end();
    answer({ ok: false, code: 'stale', error: 'x' }, 0);
    await Promise.resolve();
    expect(d.preview?.tree.ratio).toBe(0.3);
  });

  it('a second drag waits until the first is confirmed', () => {
    const { d } = drag();
    d.begin('t1', tree, 7, '');
    d.move(0.7);
    d.end();
    expect(d.begin('t1', tree, 7, '')).toBe(false);
    d.stateArrived('t1', 8);
    expect(d.begin('t1', tree, 8, '')).toBe(true);
  });

  it('a lost link ends a drag in any phase; cancel ends only an unreleased one', () => {
    const { d } = drag();
    d.begin('t1', tree, 7, '');
    d.move(0.7);
    d.end();
    d.cancel();
    expect(d.preview).not.toBeNull();
    d.linkLost();
    expect(d.preview).toBeNull();
    expect(d.begin('t1', tree, 7, '')).toBe(true);
  });
});
