import { describe, expect, it } from 'vitest';
import { TrapStack, trapIndex } from './focustrap';

describe('trapIndex', () => {
  it('wraps at either end and leaves the middle to the browser', () => {
    expect(trapIndex(3, 2, false)).toBe(0);
    expect(trapIndex(3, 0, true)).toBe(2);
    expect(trapIndex(3, 1, false)).toBeNull();
    expect(trapIndex(3, 1, true)).toBeNull();
  });

  it('pulls a focus that is outside back in', () => {
    expect(trapIndex(3, -1, false)).toBe(0);
    expect(trapIndex(3, -1, true)).toBe(2);
  });

  it('has nowhere to go with nothing focusable', () => {
    expect(trapIndex(0, -1, false)).toBeNull();
  });

  it('a single element keeps the focus', () => {
    expect(trapIndex(1, 0, false)).toBe(0);
    expect(trapIndex(1, 0, true)).toBe(0);
  });
});

describe('TrapStack', () => {
  it('puts the last opened modal on top, and the one below back on top when it closes', () => {
    const s = new TrapStack<string>();
    s.push('list');
    s.push('confirm');
    expect(s.top()).toBe('confirm');
    s.remove('confirm');
    expect(s.top()).toBe('list');
  });
  it('keeps the top when a covered modal closes first', () => {
    const s = new TrapStack<string>();
    s.push('create');
    s.push('details');
    s.remove('create');
    expect(s.top()).toBe('details');
    s.remove('details');
    expect(s.top()).toBeUndefined();
  });
});
