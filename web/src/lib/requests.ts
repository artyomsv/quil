import type { Clock } from './connection';
import type { ErrorPayload, Message } from './protocol';
import { sanitizeRemoteText } from './sanitize';

export const REQUEST_TIMEOUT_MS = 10_000;
// A create can wait on a stalled docker probe and still succeed later.
export const STILL_WORKING = 'Still working — the pane appears when it is ready';

export interface RequestOpts {
  timeoutMs?: number;
  timeoutText?: string;
  // quietMs: a request the daemon answers only when it refuses it (an
  // accepted update_layout is confirmed by the next state, not by a reply).
  // After quietMs with no answer it resolves ok with quiet set.
  quietMs?: number;
}

export type Outcome =
  | { ok: true; reply?: Message; quiet?: boolean }
  | { ok: false; code: string; error: string; reply?: Message };

interface Pending {
  resolve: (o: Outcome) => void;
  timer: unknown;
}

// replyError reads the daemon's answer shapes that mean "not done": a
// non-empty error field, ok:false (tab/pane/project op), success:false
// (destroy/restart), delivered:false (pane_input).
function replyError(m: Message): string | null {
  const p = (m.payload ?? {}) as Record<string, unknown>;
  const err = typeof p.error === 'string' ? p.error : '';
  if (p.ok === false || p.success === false || p.delivered === false) return err || 'not done';
  return err !== '' ? err : null;
}

// Requests numbers every id-bearing request the page sends and ends each one
// on the message that carries its id, on a 10 s timeout, or when the
// connection is lost. No optimistic UI rests on it: callers show a banner on
// failure and wait for the daemon's state otherwise.
export class Requests {
  private seq = 0;
  private readonly pending = new Map<string, Pending>();

  constructor(
    private readonly send: (m: Message) => boolean,
    private readonly clock: Clock,
  ) {}

  request(type: string, payload: unknown, opts: RequestOpts = {}): Promise<Outcome> {
    const id = `req-${++this.seq}`;
    return new Promise((resolve) => {
      if (!this.send({ type, id, payload })) {
        resolve({ ok: false, code: 'offline', error: 'not connected' });
        return;
      }
      const quiet = opts.quietMs !== undefined;
      const ms = quiet ? (opts.quietMs as number) : (opts.timeoutMs ?? REQUEST_TIMEOUT_MS);
      const timer = this.clock.setTimeout(() => {
        this.pending.delete(id);
        resolve(
          quiet
            ? { ok: true, quiet: true }
            : { ok: false, code: 'timeout', error: opts.timeoutText ?? 'No answer from the daemon' },
        );
      }, ms);
      this.pending.set(id, { resolve, timer });
    });
  }

  // answer ends the request m answers; false when m is not one of ours.
  answer(m: Message): boolean {
    if (!m.id) return false;
    const p = this.pending.get(m.id);
    if (!p) return false;
    this.pending.delete(m.id);
    this.clock.clearTimeout(p.timer);
    if (m.type === 'error') {
      const e = (m.payload ?? {}) as Partial<ErrorPayload>;
      p.resolve({ ok: false, code: e.code ?? 'error', error: sanitizeRemoteText(e.message ?? 'refused'), reply: m });
      return true;
    }
    const err = replyError(m);
    if (err !== null) {
      p.resolve({ ok: false, code: 'failed', error: sanitizeRemoteText(err), reply: m });
      return true;
    }
    p.resolve({ ok: true, reply: m });
    return true;
  }

  // reconnecting fails every pending request: its answer, if any, would come
  // on a connection that no longer exists.
  reconnecting(): void {
    for (const p of this.pending.values()) {
      this.clock.clearTimeout(p.timer);
      p.resolve({ ok: false, code: 'offline', error: 'connection lost' });
    }
    this.pending.clear();
  }
}
