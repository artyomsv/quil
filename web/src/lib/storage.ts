export interface StorageLike {
  getItem(k: string): string | null;
  setItem(k: string, v: string): void;
  removeItem(k: string): void;
}

export const CLIENT_ID_KEY = 'quil.web.client_id';
export const LOGIN_KEY = 'quil.web.key';

// routedStorage keeps the client id per browser tab (session storage) and
// every other key, the login key among them, per origin (local storage). Two
// tabs sharing one stored client id would each present the other's id after a
// resync and lose their own lease. Reading window.localStorage itself can
// throw, so each store is fetched on every access; SafeStorage catches it. A
// store that is missing throws too, so SafeStorage treats it like a failure.
export function routedStorage(
  local: () => StorageLike | undefined,
  session: () => StorageLike | undefined,
): StorageLike {
  const pick = (k: string): StorageLike => {
    const s = k === CLIENT_ID_KEY ? session() : local();
    if (!s) throw new Error('storage unavailable');
    return s;
  };
  return {
    getItem: (k) => pick(k).getItem(k),
    setItem: (k, v) => pick(k).setItem(k, v),
    removeItem: (k) => pick(k).removeItem(k),
  };
}

// SafeStorage wraps a browser storage whose every access may throw (private
// windows, blocked site data). Code that must see the same values, such as
// the login form and the connection, shares one instance.
//
// The backing store is the truth: it is shared with the page's other tabs,
// and a value another tab wrote must be seen here. Memory is only a fallback.
// A key whose last write or remove failed is answered from memory, since the
// backing value is stale; a read that throws is answered from memory too.
// A write that succeeds hands the key back to the backing store.
export class SafeStorage implements StorageLike {
  private memory = new Map<string, string>();
  private failed = new Set<string>();

  constructor(private readonly backing: StorageLike | undefined) {}

  getItem(k: string): string | null {
    if (!this.failed.has(k)) {
      try {
        if (this.backing) return this.backing.getItem(k);
      } catch {
        // answered from memory below
      }
    }
    return this.memory.get(k) ?? null;
  }

  setItem(k: string, v: string): void {
    try {
      if (!this.backing) throw new Error('no storage');
      this.backing.setItem(k, v);
      this.failed.delete(k);
      this.memory.delete(k);
    } catch {
      this.failed.add(k);
      this.memory.set(k, v);
    }
  }

  removeItem(k: string): void {
    this.memory.delete(k);
    try {
      if (!this.backing) throw new Error('no storage');
      this.backing.removeItem(k);
      this.failed.delete(k);
    } catch {
      this.failed.add(k);
    }
  }
}
