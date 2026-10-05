import type { Clock } from '../connection';
import { chordOf, type KeyEventLike } from './chord';

export type Tier = 'early' | 'late';

export interface WebAction {
  id: string;
  label: string;
  group: string;
  tier: Tier;
  keys: string[];
  fallback?: string;
  fallback_unavailable?: boolean;
}

export interface WebBuiltin {
  id: string;
  label: string;
  keys: string[];
  fallback?: string;
  fallback_unavailable?: boolean;
}

// WebKeymap is /api/client's "keymap" (internal/keymap/web.go WebKeymap).
export interface WebKeymap {
  preset: string;
  prefix: string;
  timeout_ms: number;
  group_order: string[];
  actions: WebAction[];
  builtins: WebBuiltin[];
  conflicts: string[];
}

const isObj = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);
const strList = (v: unknown): string[] => (Array.isArray(v) ? v.filter((s): s is string => typeof s === 'string') : []);
const optStr = (v: unknown): string | undefined => (typeof v === 'string' && v !== '' ? v : undefined);

// asWebKeymap checks /api/client's keymap and drops entries it cannot use, so
// a half-read answer can never put a binding into the tables; null when the
// answer has no keymap at all.
export function asWebKeymap(v: unknown): WebKeymap | null {
  if (!isObj(v) || !Array.isArray(v.actions) || !Array.isArray(v.builtins)) return null;
  const actions: WebAction[] = [];
  for (const a of v.actions) {
    if (!isObj(a) || typeof a.id !== 'string') continue;
    actions.push({
      id: a.id,
      label: typeof a.label === 'string' ? a.label : a.id,
      group: typeof a.group === 'string' ? a.group : '',
      tier: a.tier === 'early' ? 'early' : 'late',
      keys: strList(a.keys),
      fallback: optStr(a.fallback),
      fallback_unavailable: a.fallback_unavailable === true || undefined,
    });
  }
  const builtins: WebBuiltin[] = [];
  for (const b of v.builtins) {
    if (!isObj(b) || typeof b.id !== 'string') continue;
    builtins.push({
      id: b.id,
      label: typeof b.label === 'string' ? b.label : b.id,
      keys: strList(b.keys),
      fallback: optStr(b.fallback),
      fallback_unavailable: b.fallback_unavailable === true || undefined,
    });
  }
  return {
    preset: typeof v.preset === 'string' ? v.preset : '',
    prefix: typeof v.prefix === 'string' ? v.prefix : '',
    timeout_ms: typeof v.timeout_ms === 'number' && v.timeout_ms > 0 ? v.timeout_ms : 0,
    group_order: strList(v.group_order),
    actions,
    builtins,
    conflicts: strList(v.conflicts),
  };
}

export interface KeyTables {
  early: Map<string, string>;
  late: Map<string, string>;
  seqs: Map<string, string>;
  partial: Set<string>;
  builtins: Map<string, string>;
}

// buildTables mirrors internal/keymap's tables: single chords per tier,
// multi-step sequences, and every proper prefix of a sequence. The server
// already left out every binding that loses dispatch, so first writer wins
// here only as a guard.
export function buildTables(km: WebKeymap): KeyTables {
  const t: KeyTables = { early: new Map(), late: new Map(), seqs: new Map(), partial: new Set(), builtins: new Map() };
  for (const a of km.actions) {
    for (const key of a.keys) {
      const steps = key.split(' ');
      if (steps.length > 1) {
        if (!t.seqs.has(key)) t.seqs.set(key, a.id);
        for (let i = 1; i < steps.length; i++) t.partial.add(steps.slice(0, i).join(' '));
        continue;
      }
      const tier = a.tier === 'early' ? t.early : t.late;
      if (!tier.has(key)) tier.set(key, a.id);
    }
  }
  for (const b of km.builtins) for (const key of b.keys) if (!t.builtins.has(key)) t.builtins.set(key, b.id);
  return t;
}

export interface KeyContext {
  // A dialog, menu or editable field owns the keyboard.
  modalOpen: boolean;
  // The pane keys go to: the shown overlay, else the active pane.
  activePaneId: string;
  // That pane's plugin raw keys, as canonical chords.
  rawKeys: ReadonlySet<string>;
}

export type KeyDecision = { kind: 'pass' } | { kind: 'consume' } | { kind: 'action'; id: string } | { kind: 'builtin'; id: string };

const PASS: KeyDecision = { kind: 'pass' };
const CONSUME: KeyDecision = { kind: 'consume' };

// KeyEngine is the TUI's handleKey order for the browser (spec §5.5):
// dialog → sequence → early tier → plugin raw keys → late tier → builtins →
// terminal. The sequence machine follows the TUI's stepSequence: it is
// tier-agnostic, Esc cancels, prefix-prefix sends the prefix, an unknown step
// drops the sequence with a notice that never names the typed key, and a
// completed sequence runs even over a plugin's raw key.
export class KeyEngine {
  pending: string[] = [];
  hint = '';
  private pendingPane = '';
  private gen = 0;
  private timer: unknown;

  constructor(
    private tables: KeyTables,
    private timeoutMs: number,
    private readonly clock: Clock,
    private readonly onChange: () => void,
  ) {}

  setKeymap(km: WebKeymap): void {
    this.cancel();
    this.tables = buildTables(km);
    this.timeoutMs = km.timeout_ms;
  }

  cancel(): void {
    if (this.timer !== undefined) this.clock.clearTimeout(this.timer);
    this.timer = undefined;
    if (this.pending.length === 0) return;
    this.pending = [];
    this.gen++;
    this.onChange();
  }

  handle(e: KeyEventLike, ctx: KeyContext): KeyDecision {
    if (e.isComposing) return PASS;
    const c = chordOf(e);
    // A bare modifier is no key: it neither runs nor cancels anything.
    if (c === null) return PASS;
    if (ctx.modalOpen) {
      this.cancel();
      return PASS;
    }
    if (this.hint !== '') {
      this.hint = '';
      this.onChange();
    }
    if (this.pending.length > 0 && ctx.activePaneId !== this.pendingPane) this.cancel();
    if (c === 'esc' && this.pending.length > 0) {
      this.cancel();
      return CONSUME;
    }
    // The prefix twice sends the prefix itself to the pane.
    if (this.pending.length === 1 && this.pending[0] === c) {
      this.cancel();
      return PASS;
    }
    const cand = [...this.pending, c];
    const key = cand.join(' ');
    if (cand.length > 1) {
      const id = this.tables.seqs.get(key);
      if (id) {
        this.cancel();
        return { kind: 'action', id };
      }
    }
    const single = cand.length === 1 && (this.tables.early.has(c) || this.tables.late.has(c));
    if (!single && this.tables.partial.has(key)) {
      this.arm(cand, ctx.activePaneId);
      return CONSUME;
    }
    if (this.pending.length > 0) {
      this.hint = `${this.pending.join(' ')} — no such binding`;
      this.cancel();
      this.onChange();
      return CONSUME;
    }
    const early = this.tables.early.get(c);
    if (early) return { kind: 'action', id: early };
    if (ctx.rawKeys.has(c)) return PASS;
    const late = this.tables.late.get(c);
    if (late) return { kind: 'action', id: late };
    const builtin = this.tables.builtins.get(c);
    if (builtin) return { kind: 'builtin', id: builtin };
    return PASS;
  }

  private arm(cand: string[], pane: string): void {
    if (this.timer !== undefined) this.clock.clearTimeout(this.timer);
    this.timer = undefined;
    this.pending = cand;
    this.pendingPane = pane;
    this.gen++;
    const gen = this.gen;
    if (this.timeoutMs > 0) {
      this.timer = this.clock.setTimeout(() => {
        if (gen === this.gen) this.cancel();
      }, this.timeoutMs);
    }
    this.onChange();
  }
}
