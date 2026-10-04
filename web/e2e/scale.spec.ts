import { bufferText, createPane, createTab, expect, login, tabButton, test, typeInto } from './harness';

const TABS = 7;
const PANES_PER_TAB = 10;
const BUSY_PANES = 20;
const HEAP_LIMIT = 400 * 1024 * 1024;
const TASK_LIMIT_S = 2.5;
const IDLE_MS = 5_000;

test.use({ viewport: { width: 1600, height: 1000 } });

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

  // The last tab stays in view; the busy panes are all in hidden tabs.
  const busy = tabs.slice(0, -1).flatMap((t) => t.panes).slice(0, BUSY_PANES);
  expect(busy).toHaveLength(BUSY_PANES);
  for (const id of busy) await typeInto(quil.home, id, 'while true; do echo x; sleep 0.5; done\r');

  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Performance.enable');
  const metric = async (name: string): Promise<number> => {
    const { metrics } = await cdp.send('Performance.getMetrics');
    return metrics.find((m) => m.name === name)?.value ?? Number.NaN;
  };
  const taskBefore = await metric('TaskDuration');
  await page.waitForTimeout(IDLE_MS);
  const taskDelta = (await metric('TaskDuration')) - taskBefore;
  const heap = await metric('JSHeapUsedSize');
  console.log(`scale: JSHeapUsedSize ${(heap / 1024 / 1024).toFixed(1)} MB, TaskDuration over ${IDLE_MS} ms ${taskDelta.toFixed(3)} s`);
  expect(heap).toBeLessThan(HEAP_LIMIT);
  expect(taskDelta).toBeLessThan(TASK_LIMIT_S);
});
