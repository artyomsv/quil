import { describe, expect, it } from 'vitest';
import { CLIENT_ID_KEY, LOGIN_KEY, routedStorage, SafeStorage, type StorageLike } from './storage';

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

  it('sees what another tab wrote after its own write', () => {
    const shared = new MapStorage();
    const a = new SafeStorage(shared);
    const b = new SafeStorage(shared);
    a.setItem('k', 'k1');
    b.setItem('k', 'k2');
    expect(a.getItem('k')).toBe('k2');
    b.removeItem('k');
    expect(a.getItem('k')).toBeNull();
  });

  it('hands a key back to the backing store once a write succeeds', () => {
    const shared = new MapStorage();
    let fail = true;
    const flaky: StorageLike = {
      getItem: (k) => shared.getItem(k),
      setItem: (k, v) => {
        if (fail) throw new Error('quota');
        shared.setItem(k, v);
      },
      removeItem: (k) => shared.removeItem(k),
    };
    const a = new SafeStorage(flaky);
    a.setItem('k', 'mine');
    shared.data.set('k', 'other');
    expect(a.getItem('k')).toBe('mine');
    fail = false;
    a.setItem('k', 'mine2');
    shared.data.set('k', 'other2');
    expect(a.getItem('k')).toBe('other2');
  });

  it('answers from memory when a read throws', () => {
    const s = new SafeStorage({
      getItem: () => {
        throw new Error('SecurityError');
      },
      setItem: () => {
        throw new Error('SecurityError');
      },
      removeItem: () => {},
    });
    expect(s.getItem('k')).toBeNull();
    s.setItem('k', 'v');
    expect(s.getItem('k')).toBe('v');
  });
});

describe('routedStorage', () => {
  it('keeps the client id per tab and the login key per origin', () => {
    const local = new MapStorage();
    const session = new MapStorage();
    const s = new SafeStorage(routedStorage(() => local, () => session));
    s.setItem(CLIENT_ID_KEY, 'web-a-1');
    s.setItem(LOGIN_KEY, 'key1');
    expect([...session.data]).toEqual([[CLIENT_ID_KEY, 'web-a-1']]);
    expect([...local.data]).toEqual([[LOGIN_KEY, 'key1']]);
    s.removeItem(LOGIN_KEY);
    expect(local.data.size).toBe(0);
    expect(session.data.size).toBe(1);
  });

  it('survives a store whose access throws', () => {
    const s = new SafeStorage(
      routedStorage(
        () => {
          throw new Error('SecurityError');
        },
        () => undefined,
      ),
    );
    s.setItem(LOGIN_KEY, 'key1');
    expect(s.getItem(LOGIN_KEY)).toBe('key1');
    expect(s.getItem(CLIENT_ID_KEY)).toBeNull();
  });

  it('keeps a value in memory when the store is missing', () => {
    const s = new SafeStorage(routedStorage(() => undefined, () => undefined));
    expect(s.getItem(CLIENT_ID_KEY)).toBeNull();
    s.setItem(CLIENT_ID_KEY, 'web-a-1');
    expect(s.getItem(CLIENT_ID_KEY)).toBe('web-a-1');
  });
});
