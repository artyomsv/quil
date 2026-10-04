import { bufferText, createPane, createTab, expect, ipcSend, login, tabButton, test, typeInto } from './harness';

const TABS = 7;
const PANES_PER_TAB = 10;
const BUSY_PANES = 20;
const RENAMES = 10;
const RENAME_GAP_MS = 400;
const HEAP_LIMIT = 400 * 1024 * 1024;
const TASK_LIMIT_S = 2.5;
const IDLE_MS = 5_000;
const POLL_MS = 20_000;
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
    for (const id of t.panes) await expect.poll(() => bufferText(page, id), { timeout: POLL_MS }).not.toBe('');
  }

  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Performance.enable');
  const snapshot = async (): Promise<Map<string, number>> => {
    const { metrics } = await cdp.send('Performance.getMetrics');
    return new Map(metrics.map((m) => [m.name, m.value]));
  };
  // measure runs fn and returns how much the main-thread time grew
  // meanwhile, and the heap at its end, logging both with the number of
  // workspace_state frames and the main-thread time per frame.
  const measure = async (label: string, fn: () => Promise<void>) => {
    const before = await snapshot();
    const statesBefore = states;
    const start = Date.now();
    await fn();
    const ms = Date.now() - start;
    const after = await snapshot();
    const delta = (n: string): number => (after.get(n) ?? Number.NaN) - (before.get(n) ?? Number.NaN);
    const parts = DURATIONS.map((n) => `${n} ${delta(n).toFixed(3)} s`);
    const heap = after.get('JSHeapUsedSize') ?? Number.NaN;
    const frames = states - statesBefore;
    const perFrame = frames > 0 ? `, ${((delta('TaskDuration') * 1000) / frames).toFixed(1)} ms per frame` : '';
    console.log(
      `scale ${label}: JSHeapUsedSize ${(heap / 1024 / 1024).toFixed(1)} MB, over ${ms} ms: ${parts.join(', ')}, workspace_state frames ${frames}${perFrame}`,
    );
    return { heap, task: delta('TaskDuration') };
  };
  const idle = (label: string) => measure(label, () => page.waitForTimeout(IDLE_MS));

  await idle('quiet');

  // Each rename of a hidden tab makes the daemon send one workspace_state:
  // the page's cost per state frame, with the last tab's ten panes in view.
  const renames = await measure('state frames', async () => {
    for (let i = 0; i < RENAMES; i++) {
      await ipcSend(quil.home, 'update_tab', { tab_id: tabs[0]?.tabId, name: `scale-1-${i}` });
      await page.waitForTimeout(RENAME_GAP_MS);
    }
  });
  expect(renames.task).toBeLessThan(TASK_LIMIT_S);

  // The last tab stays in view; the busy panes are all in hidden tabs.
  const busy = tabs.slice(0, -1).flatMap((t) => t.panes).slice(0, BUSY_PANES);
  expect(busy).toHaveLength(BUSY_PANES);
  for (const id of busy) await typeInto(quil.home, id, 'while true; do echo x; sleep 0.5; done\r');
  // The first window holds the loops starting up, the second the steady
  // state after it; both stay in budget.
  const started = await idle('busy start');
  expect(started.task).toBeLessThan(TASK_LIMIT_S);

  // The page holds the busy output: count one hidden pane's lines of x.
  const xLines = async (): Promise<number> =>
    (await bufferText(page, busy[0] ?? '')).split('\n').filter((l) => l.trim() === 'x').length;
  await expect.poll(xLines, { timeout: POLL_MS }).toBeGreaterThan(0);
  const xBefore = await xLines();
  const { heap, task } = await idle('busy');
  const xAfter = await xLines();
  console.log(`scale: busy pane x lines ${xBefore} -> ${xAfter}`);
  expect(xAfter - xBefore).toBeGreaterThanOrEqual(5);
  expect(heap).toBeLessThan(HEAP_LIMIT);
  expect(task).toBeLessThan(TASK_LIMIT_S);
});
