import { describe, expect, it } from 'vitest';
import { SafeStorage, type StorageLike } from './storage';

class MapStorage implements StorageLike {
  data = new Map<string, string>();
  getItem(k: string): string | null {
    return this.data.get(k) ?? null;
  }
  setItem(k: string, v: string): void {
    this.data.set(k, v);
  }
  removeItem(k: string): void {
    this.data.delete(k);
  }
}

class FailingWrites extends MapStorage {
  setItem(): void {
    throw new Error('quota');
  }
  removeItem(): void {
    throw new Error('blocked');
  }
}

describe('SafeStorage', () => {
  it('prefers the value this page wrote over a stale backing value', () => {
    const backing = new FailingWrites();
    backing.data.set('k', 'stale');
    const s = new SafeStorage(backing);
    expect(s.getItem('k')).toBe('stale');
    s.setItem('k', 'fresh');
    expect(s.getItem('k')).toBe('fresh');
  });

  it('forgets a removed key even when the backing remove fails', () => {
    const s = new SafeStorage(new FailingWrites());
    s.setItem('k', 'v');
    s.removeItem('k');
    expect(s.getItem('k')).toBeNull();
  });

  it('works with no backing storage', () => {
    const s = new SafeStorage(undefined);
    s.setItem('k', 'v');
    expect(s.getItem('k')).toBe('v');
  });
});
