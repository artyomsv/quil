import { describe, expect, it } from 'vitest';
import { FakeClock } from './fakeclock';
import { flatten, POLL_MS, Poller } from './processes';
import type { ResourceReportResp } from './protocol';
import type { Outcome } from './requests';

const report: ResourceReportResp = {
  snapshot_at: 1,
  total: 0,
  panes: [
    {
      pane_id: 'p1',
      tab_id: 't',
      go_heap_bytes: 0,
      pty_rss_bytes: 0,
      total_bytes: 0,
      tree: {
        pid: 10,
        name: 'bash',
        rss_bytes: 1,
        cpu_pct: 0,
        depth: 1,
        start_ms: 5,
        children: [
          { pid: 11, name: 'node', rss_bytes: 2, cpu_pct: 1, depth: 2, start_ms: 6 },
          { pid: 12, name: 'odd', rss_bytes: 2, cpu_pct: -1, depth: 2 },
        ],
      },
    },
    { pane_id: 'p2', tab_id: 't', go_heap_bytes: 0, pty_rss_bytes: 0, total_bytes: 0 },
  ],
};

describe('flatten', () => {
  const rows = flatten(report, () => 'P', '');
  it('lists the tree depth-first, skipping a pane with no tree', () => {
    expect(rows.map((r) => r.pid)).toEqual([10, 11, 12]);
  });
  it('never offers to kill the pane child', () => {
    expect(rows[0]?.killable).toBe('restart the pane instead');
  });
  it('offers a depth-2 process with a start time', () => {
    expect(rows[1]?.killable).toBe('');
    expect(rows[1]?.startMs).toBe(6);
  });
  it('refuses a process with no start time', () => {
    expect(rows[2]?.killable).toBe('start time unknown');
  });
  it('carries the rights reason on every row', () => {
    expect(flatten(report, () => 'P', 'needs full rights').every((r) => r.killable === 'needs full rights')).toBe(true);
  });
});

describe('Poller', () => {
  function setup(answer: Outcome = { ok: true, reply: { type: 'resource_report_resp', payload: report } }) {
    const clock = new FakeClock();
    let asks = 0;
    let h = false;
    const reports: ResourceReportResp[] = [];
    const errors: string[] = [];
    const p = new Poller(
      clock,
      () => {
        asks++;
        return Promise.resolve(answer);
      },
      () => h,
    );
    p.onReport = (r) => reports.push(r);
    p.onError = (e) => errors.push(e);
    return { clock, p, asks: () => asks, hide: (v: boolean) => (h = v), reports, errors };
  }
  const settle = async (): Promise<void> => {
    for (let i = 0; i < 3; i++) await Promise.resolve();
  };

  it('asks at once and every 5 s', async () => {
    const s = setup();
    s.p.start();
    await settle();
    expect(s.asks()).toBe(1);
    expect(s.reports.length).toBe(1);
    s.clock.advance(POLL_MS);
    await settle();
    expect(s.asks()).toBe(2);
  });
  it('does not ask while the tab is hidden, and asks when it is shown', async () => {
    const s = setup();
    s.p.start();
    await settle();
    s.hide(true);
    s.clock.advance(POLL_MS * 3);
    await settle();
    expect(s.asks()).toBe(1);
    s.hide(false);
    s.p.visible();
    await settle();
    expect(s.asks()).toBe(2);
  });
  it('stops', async () => {
    const s = setup();
    s.p.start();
    await settle();
    s.p.stop();
    s.clock.advance(POLL_MS * 3);
    await settle();
    expect(s.asks()).toBe(1);
  });
  it('reports a failed ask and keeps asking', async () => {
    const s = setup({ ok: false, code: 'refused', error: 'needs standard rights' });
    s.p.start();
    await settle();
    expect(s.errors).toEqual(['needs standard rights']);
    s.clock.advance(POLL_MS);
    await settle();
    expect(s.asks()).toBe(2);
  });
});
