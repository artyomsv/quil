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

  getItem(k: string): string | null {
    try {
      const v = this.backing?.getItem(k);
      if (v !== undefined && v !== null) return v;
    } catch {
      // fall through to memory
    }
    return this.memory.get(k) ?? null;
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
