import { describe, expect, it, vi } from 'vitest';
import { menuGone, menuOpened } from './menuopen';

describe('menuOpened and menuGone', () => {
  it('closes the menu that was open when another opens', () => {
    const a = vi.fn();
    const b = vi.fn();
    menuOpened(a);
    menuOpened(b);
    expect(a).toHaveBeenCalledTimes(1);
    expect(b).not.toHaveBeenCalled();
    menuGone(b);
  });

  it('never calls the close of a destroyed menu', () => {
    const dead = vi.fn();
    const next = vi.fn();
    menuOpened(dead);
    menuGone(dead);
    menuOpened(next);
    expect(dead).not.toHaveBeenCalled();
    menuGone(next);
  });

  it('a menu that is not the recorded one leaves the record alone', () => {
    const open = vi.fn();
    const other = vi.fn();
    menuOpened(open);
    menuGone(other);
    menuOpened(vi.fn());
    expect(open).toHaveBeenCalledTimes(1);
  });
});
