import { bufferText, createPane, createTab, expect, ipcSend, login, tabButton, test, typeInto } from './harness';

const TABS = 7;
const PANES_PER_TAB = 10;
const BUSY_PANES = 20;
const HEAP_LIMIT = 400 * 1024 * 1024;
const TASK_LIMIT_S = 2.5;
const IDLE_MS = 5_000;
// Logged for every window, so a failure says where the main thread went.
const DURATIONS = ['TaskDuration', 'ScriptDuration', 'LayoutDuration', 'RecalcStyleDuration'];

// A trace records screenshots and snapshots in the page, which is main-thread
// work of its own; this test measures the page alone.
test.use({ viewport: { width: 1600, height: 1000 }, trace: 'off' });

test('70 panes stay within the memory and main-thread budget', async ({ page, quil }) => {
  test.setTimeout(300_000);
  // Every workspace_state the page receives, for the log lines below.
  let states = 0;
  page.on('websocket', (ws) => {
    ws.on('framereceived', (f) => {
      if (typeof f.payload === 'string' && f.payload.includes('"type":"workspace_state"')) states++;
    });
  });
  await page.addInitScript(() => {
    const w = window as unknown as { __canvases: number };
    w.__canvases = 0;
    new MutationObserver((ms) => {
      for (const m of ms)
        for (const n of m.addedNodes) {
          if (n instanceof HTMLCanvasElement) w.__canvases++;
          else if (n instanceof Element) w.__canvases += n.querySelectorAll('canvas').length;
        }
    }).observe(document, { childList: true, subtree: true });
  });
  await login(page, quil);

  const tabs: { name: string; tabId: string; panes: string[] }[] = [];
  for (let t = 1; t <= TABS; t++) {
    const name = `scale-${t}`;
    const { tabId, paneId } = await createTab(quil.home, name);
    const panes = [paneId];
    for (let p = 1; p < PANES_PER_TAB; p++) panes.push(await createPane(quil.home, { tab_id: tabId }));
    tabs.push({ name, tabId, panes });
  }
  for (const t of tabs) for (const id of t.panes) await typeInto(quil.home, id, 'seq 1 200\r');

  for (const t of tabs) {
    await tabButton(page, t.name).click();
    await expect(page.locator('.pane')).toHaveCount(PANES_PER_TAB);
    for (const id of t.panes) await expect.poll(() => bufferText(page, id)).not.toBe('');
  }

  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Performance.enable');
  const snapshot = async (): Promise<Map<string, number>> => {
    const { metrics } = await cdp.send('Performance.getMetrics');
    return new Map(metrics.map((m) => [m.name, m.value]));
  };
  // idle waits IDLE_MS and returns how much each metric grew meanwhile, and
  // the values at its end.
  const idle = async (label: string) => {
    const before = await snapshot();
    const statesBefore = states;
    await page.waitForTimeout(IDLE_MS);
    const after = await snapshot();
    const delta = (n: string): number => (after.get(n) ?? Number.NaN) - (before.get(n) ?? Number.NaN);
    const parts = DURATIONS.map((n) => `${n} ${delta(n).toFixed(3)} s`);
    const heap = after.get('JSHeapUsedSize') ?? Number.NaN;
    console.log(
      `scale ${label}: JSHeapUsedSize ${(heap / 1024 / 1024).toFixed(1)} MB, over ${IDLE_MS} ms: ${parts.join(', ')}, workspace_state frames ${states - statesBefore}`,
    );
    return { heap, task: delta('TaskDuration') };
  };

  await idle('quiet');

  // DIAGNOSTIC (temporary): profile and count canvases.
  const canvases = (): Promise<number> =>
    page.evaluate(() => (window as unknown as { __canvases?: number }).__canvases ?? -1);
  const profile = async <T>(label: string, fn: () => Promise<T>): Promise<T> => {
    await cdp.send('Profiler.enable');
    await cdp.send('Profiler.setSamplingInterval', { interval: 1000 });
    await cdp.send('Profiler.start');
    const c0 = await canvases();
    const r = await fn();
    const c1 = await canvases();
    const { profile: prof } = await cdp.send('Profiler.stop');
    const selfMs = new Map<number, number>();
    const samples = prof.samples ?? [];
    const deltas = prof.timeDeltas ?? [];
    samples.forEach((id, i) => selfMs.set(id, (selfMs.get(id) ?? 0) + (deltas[i] ?? 0) / 1000));
    const byId = new Map(prof.nodes.map((n) => [n.id, n]));
    const keyOf = (id: number): string => {
      const f = byId.get(id)?.callFrame;
      return f ? `${f.functionName || '(anon)'} ${f.url.split('/').pop() ?? ''}:${f.lineNumber}:${f.columnNumber}` : '?';
    };
    const incl = new Map<number, number>();
    const total = (id: number): number => {
      const n = byId.get(id);
      let t = selfMs.get(id) ?? 0;
      for (const c of n?.children ?? []) t += total(c);
      incl.set(id, t);
      return t;
    };
    total(prof.nodes[0]?.id ?? 0);
    const self = new Map<string, number>();
    const inc = new Map<string, number>();
    for (const n of prof.nodes) {
      const k = keyOf(n.id);
      self.set(k, (self.get(k) ?? 0) + (selfMs.get(n.id) ?? 0));
      inc.set(k, Math.max(inc.get(k) ?? 0, incl.get(n.id) ?? 0));
    }
    const fmt = (m: Map<string, number>) =>
      [...m.entries()].sort((a, b) => b[1] - a[1]).slice(0, 40).map(([k, v]) => `${v.toFixed(1)} ms  ${k}`).join('\n');
    console.log(`scale profile ${label}: canvases added ${c1 - c0}\nSELF\n${fmt(self)}\nINCLUSIVE\n${fmt(inc)}`);
    return r;
  };
  await profile('renames', async () => {
    const before = await snapshot();
    const s0 = states;
    for (let i = 0; i < 10; i++) {
      await ipcSend(quil.home, 'update_tab', { tab_id: tabs[0]?.tabId, name: `scale-1-${i}` });
      await page.waitForTimeout(400);
    }
    await page.waitForTimeout(1000);
    const after = await snapshot();
    const n = states - s0;
    const d = (k: string) => (after.get(k) ?? 0) - (before.get(k) ?? 0);
    console.log(
      `scale renames: workspace_state frames ${n}, TaskDuration ${d('TaskDuration').toFixed(3)} s, Script ${d('ScriptDuration').toFixed(3)} s, per frame ${((d('TaskDuration') / Math.max(1, n)) * 1000).toFixed(1)} ms`,
    );
    console.log(`scale metric deltas: ${[...after.keys()].map((k) => `${k}=${d(k).toFixed(3)}`).join(' ')}`);
  });

  // The last tab stays in view; the busy panes are all in hidden tabs.
  const busy = tabs.slice(0, -1).flatMap((t) => t.panes).slice(0, BUSY_PANES);
  expect(busy).toHaveLength(BUSY_PANES);
  for (const id of busy) await typeInto(quil.home, id, 'while true; do echo x; sleep 0.5; done\r');
  // Logged only: the first window holds the loops starting up. The budget
  // applies to the steady state after it.
  await profile('busy start', () => idle('busy start'));

  // The page holds the busy output: count one hidden pane's lines of x.
  const xLines = async (): Promise<number> =>
    (await bufferText(page, busy[0] ?? '')).split('\n').filter((l) => l.trim() === 'x').length;
  await expect.poll(xLines).toBeGreaterThan(0);
  const xBefore = await xLines();
  const { heap, task } = await idle('busy');
  const xAfter = await xLines();
  console.log(`scale: busy pane x lines ${xBefore} -> ${xAfter}`);
  expect(xAfter - xBefore).toBeGreaterThanOrEqual(5);
  expect(heap).toBeLessThan(HEAP_LIMIT);
  expect(task).toBeLessThan(TASK_LIMIT_S);
});
