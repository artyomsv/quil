export interface StorageLike {
  getItem(k: string): string | null;
  setItem(k: string, v: string): void;
  removeItem(k: string): void;
}

export const CLIENT_ID_KEY = 'quil.web.client_id';
export const LOGIN_KEY = 'quil.web.key';

// SafeStorage wraps a browser storage whose every access may throw (private
// windows, blocked site data). A failed write keeps the value in memory, so
// the page still works for its own life; a failed read answers from memory.
// Code that must see the same values, such as the login form and the
// connection, shares one instance.
export class SafeStorage implements StorageLike {
  private memory = new Map<string, string>();

  constructor(private readonly backing: StorageLike | undefined) {}

  // Memory is read first: it holds the latest value this page wrote, which
  // is newer than a backing value left stale by a failed write.
  getItem(k: string): string | null {
    const m = this.memory.get(k);
    if (m !== undefined) return m;
    try {
      return this.backing?.getItem(k) ?? null;
    } catch {
      return null;
    }
  }

  setItem(k: string, v: string): void {
    this.memory.set(k, v);
    try {
      this.backing?.setItem(k, v);
    } catch {
      // memory holds it
    }
  }

  removeItem(k: string): void {
    this.memory.delete(k);
    try {
      this.backing?.removeItem(k);
    } catch {
      // nothing more to do
    }
  }
}
