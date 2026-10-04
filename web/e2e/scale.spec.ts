import { bufferText, createPane, createTab, expect, login, tabButton, test, typeInto } from './harness';

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
  await login(page, quil);

  const tabs: { name: string; panes: string[] }[] = [];
  for (let t = 1; t <= TABS; t++) {
    const name = `scale-${t}`;
    const { tabId, paneId } = await createTab(quil.home, name);
    const panes = [paneId];
    for (let p = 1; p < PANES_PER_TAB; p++) panes.push(await createPane(quil.home, { tab_id: tabId }));
    tabs.push({ name, panes });
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
    await page.waitForTimeout(IDLE_MS);
    const after = await snapshot();
    const delta = (n: string): number => (after.get(n) ?? Number.NaN) - (before.get(n) ?? Number.NaN);
    const parts = DURATIONS.map((n) => `${n} ${delta(n).toFixed(3)} s`);
    const heap = after.get('JSHeapUsedSize') ?? Number.NaN;
    console.log(`scale ${label}: JSHeapUsedSize ${(heap / 1024 / 1024).toFixed(1)} MB, over ${IDLE_MS} ms: ${parts.join(', ')}`);
    return { heap, task: delta('TaskDuration') };
  };

  await idle('quiet');

  // The last tab stays in view; the busy panes are all in hidden tabs.
  const busy = tabs.slice(0, -1).flatMap((t) => t.panes).slice(0, BUSY_PANES);
  expect(busy).toHaveLength(BUSY_PANES);
  for (const id of busy) await typeInto(quil.home, id, 'while true; do echo x; sleep 0.5; done\r');

  const { heap, task } = await idle('busy');
  expect(heap).toBeLessThan(HEAP_LIMIT);
  expect(task).toBeLessThan(TASK_LIMIT_S);
});
