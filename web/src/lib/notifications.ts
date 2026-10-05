// The notification sidebar's store (spec §5.3). The daemon's event queue is
// the shared truth; this page keeps its own list of it, filed and filtered
// exactly as the TUI's sidebar does from the tables /api/client sends.

export interface PaneEvent {
  id: string;
  pane_id: string;
  tab_id: string;
  pane_name: string;
  type: string;
  title: string;
  message?: string;
  severity: string;
  timestamp: number;
  data?: Record<string, string>;
}

// NotifyInfo is /api/client's "notifications" (cmd/quil/web_client.go).
export interface NotifyInfo {
  shown: Record<string, boolean> | null;
  hook_groups: Record<string, string>;
  plain_groups: Record<string, string>;
  default_group: string;
  work_state_only: string[];
}

export interface SkipContext {
  muted(paneId: string): boolean;
  activePaneId: string;
}

// The TUI's sidebar cap (NewNotificationCenter(…, 200)).
export const MAX_EVENTS = 200;

const str = (v: unknown): string => (typeof v === 'string' ? v : '');

// stringMap keeps only the string values of a data object: a remote daemon's
// payload is not trusted to be the shape it claims.
function stringMap(v: unknown): Record<string, string> | undefined {
  if (typeof v !== 'object' || v === null || Array.isArray(v)) return undefined;
  const out: Record<string, string> = {};
  for (const [k, x] of Object.entries(v)) if (typeof x === 'string') out[k] = x;
  return out;
}

function boolMap(v: unknown): Record<string, boolean> | null {
  if (typeof v !== 'object' || v === null || Array.isArray(v)) return null;
  const out: Record<string, boolean> = {};
  for (const [k, x] of Object.entries(v)) if (typeof x === 'boolean') out[k] = x;
  return out;
}

// asNotifyInfo checks /api/client's notifications; null when there are none,
// and the store then shows everything.
export function asNotifyInfo(v: unknown): NotifyInfo | null {
  if (typeof v !== 'object' || v === null || Array.isArray(v)) return null;
  const o = v as Record<string, unknown>;
  return {
    shown: boolMap(o.shown),
    hook_groups: stringMap(o.hook_groups) ?? {},
    plain_groups: stringMap(o.plain_groups) ?? {},
    default_group: str(o.default_group) || 'system',
    work_state_only: Array.isArray(o.work_state_only) ? o.work_state_only.filter((s): s is string => typeof s === 'string') : [],
  };
}

export function parsePaneEvent(v: unknown): PaneEvent | null {
  if (typeof v !== 'object' || v === null) return null;
  const o = v as Record<string, unknown>;
  if (typeof o.id !== 'string' || o.id === '') return null;
  return {
    id: o.id,
    pane_id: str(o.pane_id),
    tab_id: str(o.tab_id),
    pane_name: str(o.pane_name),
    type: str(o.type),
    title: str(o.title),
    message: typeof o.message === 'string' ? o.message : undefined,
    severity: str(o.severity),
    timestamp: typeof o.timestamp === 'number' ? o.timestamp : 0,
    data: stringMap(o.data),
  };
}

// groupOf is internal/notifyclass.Group over the tables the server sent.
export function groupOf(type: string, info: NotifyInfo): string {
  if (type.startsWith('hook.')) {
    const rest = type.slice(5);
    const dot = rest.indexOf('.');
    if (dot >= 0) return info.hook_groups[rest.slice(dot + 1)] ?? info.default_group;
    return info.default_group;
  }
  return info.plain_groups[type] ?? info.default_group;
}

// NotificationStore is the sidebar's list, newest first, keyed by event id:
// the daemon aggregates a repeat (pane, title) under the old id with a higher
// count, so a known id replaces its entry and moves to the top.
//
// A rebuild races the live feed: the daemon takes its list, and an event or a
// dismissal can reach the page after that but before the list does. So every
// live change from beginRebuild on is recorded and replayed over the list.
export class NotificationStore {
  private events: PaneEvent[] = [];
  private gen = 0;
  // Live changes since beginRebuild, in arrival order; null when no rebuild
  // is waiting for its list.
  private live: ({ add: PaneEvent } | { dismiss: string })[] | null = null;

  constructor(private info: NotifyInfo | null) {}

  // beginRebuild runs as get_notifications_req goes out; its number goes to
  // rebuild or abortRebuild. A newer begin makes an older answer stale.
  beginRebuild(): number {
    this.live = [];
    return ++this.gen;
  }

  // abortRebuild ends a rebuild whose request failed.
  abortRebuild(gen: number): void {
    if (gen === this.gen) this.live = null;
  }

  // setInfo installs the tables; events that arrived before them and that
  // the TUI would have skipped are dropped now.
  setInfo(info: NotifyInfo | null): void {
    this.info = info;
    if (info) this.events = this.events.filter((e) => !info.work_state_only.includes(e.type));
  }

  size(): number {
    return this.events.length;
  }

  // add files a live event; false when a skip rule dropped it (the TUI's
  // paneEventMsg arm in internal/tui/model.go).
  add(e: PaneEvent, ctx: SkipContext): boolean {
    this.live?.push({ add: e });
    return this.file(e, ctx);
  }

  private file(e: PaneEvent, ctx: SkipContext): boolean {
    if (this.skipped(e, ctx)) return false;
    const i = this.events.findIndex((x) => x.id === e.id);
    if (i >= 0) this.events.splice(i, 1);
    this.events.unshift(e);
    this.evict();
    return true;
  }

  // rebuild replaces the list with get_notifications_resp's events, which the
  // daemon lists newest first, then replays the live changes that arrived
  // since beginRebuild, in order. A live event the list still holds at the
  // same or a newer timestamp is not replayed (that copy is as new); one a
  // replayed dismissal removed is filed again. False for a
  // stale answer: a newer rebuild has begun.
  rebuild(newestFirst: PaneEvent[], ctx: SkipContext, gen = this.gen): boolean {
    if (gen !== this.gen) return false;
    const live = this.live ?? [];
    this.live = null;
    this.events = [];
    for (let i = newestFirst.length - 1; i >= 0; i--) this.file(newestFirst[i]!, ctx);
    for (const op of live) {
      if ('dismiss' in op) {
        this.drop(op.dismiss);
        continue;
      }
      // Compared with the list AS REPLAYED SO FAR, not as it arrived: an
      // event added after a replayed dismissal postdates it, so it is filed
      // again even when the daemon's list already held the same copy.
      const t = this.events.find((e) => e.id === op.add.id)?.timestamp;
      if (t === undefined || op.add.timestamp > t) this.file(op.add, ctx);
    }
    return true;
  }

  // dismiss drops one event, or every event for an empty id (dismiss all).
  dismiss(id: string): void {
    this.live?.push({ dismiss: id });
    this.drop(id);
  }

  private drop(id: string): void {
    this.events = id === '' ? [] : this.events.filter((e) => e.id !== id);
  }

  visible(): PaneEvent[] {
    return this.events.filter((e) => this.shows(e));
  }

  private shows(e: PaneEvent): boolean {
    const info = this.info;
    if (!info || !info.shown) return true;
    return info.shown[groupOf(e.type, info)] === true;
  }

  private skipped(e: PaneEvent, ctx: SkipContext): boolean {
    if (this.info?.work_state_only.includes(e.type)) return true;
    if (ctx.muted(e.pane_id)) return true;
    return e.type === 'output_idle' && e.pane_id === ctx.activePaneId;
  }

  // Hidden events go first, oldest first, as the TUI's evictOverCap does.
  private evict(): void {
    while (this.events.length > MAX_EVENTS) {
      let drop = -1;
      for (let i = this.events.length - 1; i >= 0; i--) {
        if (!this.shows(this.events[i]!)) {
          drop = i;
          break;
        }
      }
      this.events.splice(drop >= 0 ? drop : this.events.length - 1, 1);
    }
  }
}
