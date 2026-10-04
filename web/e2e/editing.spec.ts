import { createHash } from 'node:crypto';
import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import {
  activePane,
  createTab,
  expect,
  fakeTUI,
  ipcRequest,
  ipcSend,
  layoutIds,
  listPanes,
  login,
  tabButton,
  tabOf,
  test,
  typeInto,
} from './harness';

test('AC-1 outbound: a browser split lands where it asked, in a TUI too', async ({ page, quil }) => {
  await login(page, quil);
  const tui = await fakeTUI(quil.home, 'tui-ac1');
  try {
    const [first] = await listPanes(quil.home);
    if (!first) throw new Error('no first pane');
    await page.locator('.pane').first().getByRole('button', { name: 'Pane menu' }).click();
    await page.getByRole('menuitem', { name: 'Split right' }).click();
    await expect(page.locator('.pane')).toHaveCount(2);
    await expect.poll(() => layoutIds(tabOf(tui.state(), first.tab_id)?.layout)).toHaveLength(2);
    const layout = tabOf(tui.state(), first.tab_id)?.layout as {
      split?: number;
      left?: { pane_id?: string };
      right?: { pane_id?: string };
    };
    const added = (await listPanes(quil.home)).find((p) => p.id !== first.id);
    expect(layout.split ?? 0).toBe(0);
    expect(layout.left?.pane_id).toBe(first.id);
    expect(layout.right?.pane_id).toBe(added?.id);
    // The new pane is the page's active pane.
    await expect.poll(() => activePane(page)).toBe(added?.id);
  } finally {
    tui.close();
  }
});

test('AC-6: a dragged border is stored and every client gets the ratio', async ({ page, quil }) => {
  await login(page, quil);
  const tui = await fakeTUI(quil.home, 'tui-ac6');
  try {
    const [first] = await listPanes(quil.home);
    if (!first) throw new Error('no first pane');
    await page.locator('.pane').first().getByRole('button', { name: 'Pane menu' }).click();
    await page.getByRole('menuitem', { name: 'Split right' }).click();
    await expect(page.locator('.split-bar')).toHaveCount(1);
    const area = await page.locator('.area').boundingBox();
    const bar = await page.locator('.split-bar').boundingBox();
    if (!area || !bar) throw new Error('no geometry');
    const y = bar.y + bar.height / 2;
    await page.mouse.move(bar.x + bar.width / 2, y);
    await page.mouse.down();
    await page.mouse.move(area.x + area.width * 0.7, y, { steps: 8 });
    await page.mouse.up();
    await expect.poll(() => tabOf(tui.state(), first.tab_id)?.layout?.ratio ?? 0).toBeGreaterThan(0.65);
    expect(tabOf(tui.state(), first.tab_id)?.layout?.ratio ?? 0).toBeLessThan(0.75);
    const slot = await page.locator('.slot').first().boundingBox();
    expect((slot?.width ?? 0) / area.width).toBeGreaterThan(0.65);
  } finally {
    tui.close();
  }
});

test('AC-7: a closed pane never comes back in a stored or broadcast tree', async ({ page, quil }) => {
  await login(page, quil);
  const tui = await fakeTUI(quil.home, 'tui-ac7');
  try {
    const [first] = await listPanes(quil.home);
    if (!first) throw new Error('no first pane');
    await page.locator('.pane').first().getByRole('button', { name: 'Pane menu' }).click();
    await page.getByRole('menuitem', { name: 'Split right' }).click();
    await expect(page.locator('.pane')).toHaveCount(2);
    const closed = (await listPanes(quil.home)).find((p) => p.id !== first.id);
    if (!closed) throw new Error('no second pane');
    await page.locator('.pane').nth(1).getByRole('button', { name: 'Pane menu' }).click();
    await page.getByRole('menuitem', { name: 'Close…' }).click();
    await page.getByRole('button', { name: 'Close', exact: true }).click();
    await expect(page.locator('.pane')).toHaveCount(1);
    const seenAfterClose = tui.states().length;
    // An old client's unversioned write that still names the closed pane.
    await ipcSend(quil.home, 'update_layout', {
      tab_id: first.tab_id,
      layout: { split: 0, ratio: 0.5, left: { pane_id: first.id }, right: { pane_id: closed.id } },
    });
    const stored = await ipcRequest(quil.home, 'state_req', {});
    const tabs = (stored.payload as { tabs?: { id: string; layout?: unknown }[] }).tabs ?? [];
    expect(layoutIds(tabs.find((t) => t.id === first.tab_id)?.layout)).not.toContain(closed.id);
    await page.waitForTimeout(500);
    for (const s of tui.states().slice(seenAfterClose)) {
      expect(layoutIds(tabOf(s, first.tab_id)?.layout)).not.toContain(closed.id);
    }
  } finally {
    tui.close();
  }
});

test('renaming a pane and a tab, and closing a tab, go through the daemon', async ({ page, quil }) => {
  await login(page, quil);
  const [first] = await listPanes(quil.home);
  if (!first) throw new Error('no first pane');
  await page.locator('.pane').first().getByRole('button', { name: 'Pane menu' }).click();
  await page.getByRole('menuitem', { name: 'Rename…' }).click();
  await page.getByRole('textbox', { name: 'Rename pane' }).fill('renamed-pane');
  await page.getByRole('button', { name: 'Rename', exact: true }).click();
  await expect(page.locator('.pane .title', { hasText: 'renamed-pane' })).toBeVisible();
  await expect.poll(async () => (await listPanes(quil.home)).find((p) => p.id === first.id)?.name).toBe('renamed-pane');

  await createTab(quil.home, 'second');
  await expect(tabButton(page, 'second')).toBeVisible();
  await tabButton(page, 'second').dblclick();
  await page.getByRole('textbox', { name: 'Rename tab' }).fill('third');
  await page.getByRole('button', { name: 'Rename', exact: true }).click();
  await expect(tabButton(page, 'third')).toBeVisible();

  await page.getByRole('button', { name: 'Close tab third' }).click();
  await page.getByRole('button', { name: 'Close', exact: true }).click();
  await expect(tabButton(page, 'third')).toHaveCount(0);
});

test('AC-11: a paste over 64 MiB arrives complete and in order through a full queue', async ({ page, quil }) => {
  test.setTimeout(240_000);
  await login(page, quil);
  const [pane] = await listPanes(quil.home);
  if (!pane) throw new Error('no pane');
  const size = 73_400_320; // 70 MiB
  const unit = '0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ-_';
  const sumFile = path.join(quil.home, 'paste.sum');
  // Raw, no echo: the bytes reach head unchanged and nothing floods the page.
  // The sleep stops reading long enough for the pane's input queue (256
  // chunks of 256 KiB, 64 MiB) to fill.
  await typeInto(quil.home, pane.id, `stty raw -echo; sleep 8; head -c ${size} | sha256sum > ${sumFile}; stty sane\r`);
  await page.waitForTimeout(500);
  await page.evaluate(
    ([id, u, n]) => (window as unknown as { __quilTest: { paste(i: string, t: string): void } }).__quilTest.paste(id, u.repeat(n)),
    [pane.id, unit, size / unit.length] as [string, string, number],
  );
  await expect.poll(() => existsSync(sumFile) && readFileSync(sumFile, 'utf8').length > 0, { timeout: 200_000 }).toBe(true);
  const want = createHash('sha256').update(unit.repeat(size / unit.length)).digest('hex');
  expect(readFileSync(sumFile, 'utf8').split(' ')[0]).toBe(want);
  // The queue was forced full at least once: the daemon raised input_blocked.
  const ev = await ipcRequest(quil.home, 'get_notifications_req', {});
  const events = (ev.payload as { events?: { type: string; pane_id: string }[] }).events ?? [];
  expect(events.some((e) => e.type === 'input_blocked' && e.pane_id === pane.id)).toBe(true);
  // A paste that waited for the pane ends without a notice.
  await expect(page.locator('.notice')).toHaveCount(0);
});
