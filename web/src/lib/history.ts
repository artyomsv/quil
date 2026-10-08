import type { PluginDef } from './client';
import type { HistoryEntryMeta, PaneHistoryEntryResp, PaneHistoryResp } from './protocol';
import type { Outcome } from './requests';
import { sanitizeBlock } from './sanitize';

export const HISTORY_TIMEOUT_MS = 3000;

// historySupported: the TUI asks only for a pane whose plugin records input
// history (Command.RecordHistory), and says so for any other.
export function historySupported(type: string, plugins: PluginDef[]): boolean {
  return plugins.some((p) => p.name === (type || 'terminal') && p.record_history === true);
}

const two = (n: number): string => String(n).padStart(2, '0');

// entryTitle is the TUI viewer's "Input @ 2006-01-02 15:04:05" (local time).
export function entryTitle(tsMs: number): string {
  const d = new Date(tsMs);
  return `Input @ ${d.getFullYear()}-${two(d.getMonth() + 1)}-${two(d.getDate())} ${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}`;
}

export type HistoryView =
  | { state: 'unsupported' }
  | { state: 'loading' }
  | { state: 'error'; text: string }
  | { state: 'list'; entries: HistoryEntryMeta[] }
  | { state: 'entry'; title: string; text: string; entries: HistoryEntryMeta[] };

export interface HistoryIO {
  list(): Promise<Outcome>;
  entry(tsMs: number): Promise<Outcome>;
}

// HistoryFlow is the input-history dialog's logic: an unsupported pane type
// asks the daemon nothing; an entry the daemon no longer has reloads the
// list, as the TUI does (model.go historyEntryMsg). The component renders
// view and calls the methods.
export class HistoryFlow {
  view: HistoryView = { state: 'loading' };
  onChange: () => void = () => {};
  // Every request numbers itself; an answer that is not the newest is late.
  private seq = 0;

  constructor(
    private readonly io: HistoryIO,
    private readonly supported: boolean,
  ) {}

  open(): void {
    if (!this.supported) {
      this.set({ state: 'unsupported' });
      return;
    }
    void this.list();
  }

  async list(): Promise<void> {
    const seq = ++this.seq;
    this.set({ state: 'loading' });
    const o = await this.io.list();
    if (seq !== this.seq) return;
    const p = (o.reply?.payload ?? null) as PaneHistoryResp | null;
    this.set(!o.ok || !p ? { state: 'error', text: o.ok ? 'bad answer' : o.error } : { state: 'list', entries: p.entries ?? [] });
  }

  async pick(tsMs: number): Promise<void> {
    const keep = this.view.state === 'list' || this.view.state === 'entry' ? this.view.entries : [];
    const seq = ++this.seq;
    const o = await this.io.entry(tsMs);
    if (seq !== this.seq) return;
    const p = (o.reply?.payload ?? null) as PaneHistoryEntryResp | null;
    if (!o.ok || !p) {
      this.set({ state: 'error', text: o.ok ? 'bad answer' : o.error });
      return;
    }
    if (!p.found) {
      await this.list();
      return;
    }
    this.set({ state: 'entry', title: entryTitle(p.ts_ms), text: sanitizeBlock(p.text), entries: keep });
  }

  // back leaves an open entry for the list; false when there is none open.
  back(): boolean {
    if (this.view.state !== 'entry') return false;
    this.set({ state: 'list', entries: this.view.entries });
    return true;
  }

  private set(v: HistoryView): void {
    this.view = v;
    this.onChange();
  }
}
