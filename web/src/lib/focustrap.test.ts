import { describe, expect, it } from 'vitest';
import { trapIndex } from './focustrap';

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
