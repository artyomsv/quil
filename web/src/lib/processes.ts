import type { Clock } from './connection';
import type { ProcNode, ResourceReportResp } from './protocol';
import type { Outcome } from './requests';

export const POLL_MS = 5000;
export const REPORT_TIMEOUT_MS = 8000;

export interface ProcRow {
  paneId: string;
  paneLabel: string;
  pid: number;
  name: string;
  depth: number;
  rss: number;
  cpu: number;
  startMs: number;
  // '' = this row may be killed; else why not.
  killable: string;
}

// flatten lists each pane's process tree depth-first. Only a process below
// the pane's own child may be killed (that one is restarted —
// internal/tui/processes.go appendProcNodeRows), and only with a start time
// the daemon can check against a reused PID.
export function flatten(r: ResourceReportResp, label: (paneId: string) => string, canKill: string): ProcRow[] {
  const out: ProcRow[] = [];
  const walk = (paneId: string, n: ProcNode): void => {
    let killable = canKill;
    if (!killable && n.depth < 2) killable = 'restart the pane instead';
    if (!killable && !n.start_ms) killable = 'start time unknown';
    out.push({
      paneId,
      paneLabel: label(paneId),
      pid: n.pid,
      name: n.name,
      depth: n.depth,
      rss: n.rss_bytes,
      cpu: n.cpu_pct,
      startMs: n.start_ms ?? 0,
      killable,
    });
    for (const c of n.children ?? []) walk(paneId, c);
  };
  for (const p of r.panes ?? []) if (p.tree) walk(p.pane_id, p.tree);
  return out;
}

// Poller asks for a report while the page is open and the browser tab is
// shown: the daemon's process collector runs only while such requests keep
// coming. One request at a time; the next goes 5 s after the answer.
export class Poller {
  onReport: (r: ResourceReportResp) => void = () => {};
  onError: (text: string) => void = () => {};
  private timer: unknown = null;
  private running = false;
  private waiting = false;

  constructor(
    private readonly clock: Clock,
    private readonly ask: () => Promise<Outcome>,
    private readonly hidden: () => boolean,
  ) {}

  start(): void {
    this.running = true;
    this.tick();
  }

  stop(): void {
    this.running = false;
    if (this.timer !== null) this.clock.clearTimeout(this.timer);
    this.timer = null;
  }

  // visible is the page's visibilitychange: a shown tab asks at once.
  visible(): void {
    if (this.running && this.timer === null && !this.waiting) this.tick();
  }

  private tick(): void {
    this.timer = null;
    if (!this.running || this.hidden()) return;
    this.waiting = true;
    void this.ask().then((o) => {
      this.waiting = false;
      if (!this.running) return;
      const p = (o.reply?.payload ?? null) as ResourceReportResp | null;
      if (o.ok && p) this.onReport(p);
      else this.onError(o.ok ? 'bad answer' : o.error);
      this.timer = this.clock.setTimeout(() => this.tick(), POLL_MS);
    });
  }
}
