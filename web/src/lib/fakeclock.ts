import type { Clock } from './connection';

// FakeClock runs timers when advance() passes their time. Tests only.
export class FakeClock implements Clock {
  t = 0;
  private next = 1;
  timers = new Map<number, { at: number; fn: () => void }>();
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
