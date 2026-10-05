import { createHash } from 'node:crypto';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import {
  activePane,
  bufferText,
  createTab,
  expect,
  fakeTUI,
  ipcRequest,
  ipcSend,
  keymapLoaded,
  layoutIds,
  listPanes,
  login,
  paneMenu,
  paste,
  tabButton,
  stopDaemon,
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
  // Spec §5.5: the keyboard is back in the terminal once the dialog closes,
  // so what is typed next reaches the pane. $((40+2)) shows 42 only when
  // the shell runs the line.
  await page.keyboard.type('echo FOCUS-$((40+2))');
  await page.keyboard.press('Enter');
  await expect.poll(() => bufferText(page, first.id)).toContain('FOCUS-42');
  // The menu found by the new title mutes the pane; the header shows it.
  await paneMenu(page, 'renamed-pane');
  await page.getByRole('menuitem', { name: 'Mute', exact: true }).click();
  await expect(page.locator('.pane .title .mark', { hasText: 'muted' })).toBeVisible();

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

test('a lost daemon closes an open dialog, hides the controls, and a paste says it was not sent', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  const [first] = await listPanes(quil.home);
  if (!first) throw new Error('no first pane');
  await page.locator('.pane').first().getByRole('button', { name: 'Pane menu' }).click();
  await page.getByRole('menuitem', { name: 'Close…' }).click();
  await expect(page.getByRole('dialog', { name: 'Close pane' })).toBeVisible();
  // quil web starts the daemon once; with it gone the page stays offline.
  stopDaemon(quil.home);
  await expect(page.getByRole('dialog', { name: 'Close pane' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Pane menu' })).toHaveCount(0);
  // The new-pane key says why nothing opened, as every other action does.
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('Alt+Shift+O');
  await expect(page.locator('.notice')).toHaveText(/Not connected/);
  await paste(page, first.id, 'echo never-sent\r');
  await expect(page.locator('.notice')).toHaveText(/Paste was not sent/);
});

test('AC-11: a paste over 64 MiB arrives complete and in order through a full queue', async ({ page, quil }) => {
  test.setTimeout(240_000);
  await login(page, quil);
  const [pane] = await listPanes(quil.home);
  if (!pane) throw new Error('no pane');
  const size = 73_400_320; // 70 MiB
  const unit = '0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ-_';
  const sumFile = path.join(quil.home, 'paste.sum');
  const goFile = path.join(quil.home, 'paste.go');
  // Raw, no echo: the bytes reach head unchanged and nothing floods the page.
  // The shell reads nothing until the test creates goFile, which it does
  // once the daemon reports the pane's input queue (256 chunks of 256 KiB,
  // 64 MiB) full. READY-$((6*7)) prints READY-42 only when the shell RUNS the
  // line, so the echoed command itself cannot satisfy the wait.
  await typeInto(
    quil.home,
    pane.id,
    `stty raw -echo; echo READY-$((6*7)); while [ ! -e ${goFile} ]; do sleep 0.1; done; head -c ${size} | sha256sum > ${sumFile}; stty sane\r`,
  );
  await expect.poll(() => bufferText(page, pane.id), { timeout: 30_000 }).toContain('READY-42');
  await page.evaluate(
    ([id, u, n]) => (window as unknown as { __quilTest: { paste(i: string, t: string): void } }).__quilTest.paste(id, u.repeat(n)),
    [pane.id, unit, size / unit.length] as [string, string, number],
  );
  // The queue is forced full at least once: the daemon raises input_blocked.
  // Only then may the shell start reading.
  const blocked = async (): Promise<boolean> => {
    const ev = await ipcRequest(quil.home, 'get_notifications_req', {});
    const events = (ev.payload as { events?: { type: string; pane_id: string }[] | null }).events ?? [];
    return events.some((e) => e.type === 'input_blocked' && e.pane_id === pane.id);
  };
  await expect.poll(blocked, { timeout: 120_000 }).toBe(true);
  writeFileSync(goFile, '');
  await expect.poll(() => existsSync(sumFile) && readFileSync(sumFile, 'utf8').length > 0, { timeout: 200_000 }).toBe(true);
  const want = createHash('sha256').update(unit.repeat(size / unit.length)).digest('hex');
  expect(readFileSync(sumFile, 'utf8').split(' ')[0]).toBe(want);
  // A paste that waited for the pane ends without a notice.
  await expect(page.locator('.notice')).toHaveCount(0);
});

// A modal keeps Tab inside itself: Tab never reaches the terminal behind it,
// so keys typed while it is open never reach the pane.
test('Tab and Shift+Tab stay inside a dialog', async ({ page, quil }) => {
  await login(page, quil);
  await page.locator('.pane').first().getByRole('button', { name: 'Pane menu' }).click();
  await page.getByRole('menuitem', { name: 'Close…' }).click();
  const dialog = page.getByRole('dialog', { name: 'Close pane' });
  await expect(dialog).toBeVisible();
  const inside = (): Promise<boolean> => page.evaluate(() => document.activeElement?.closest('[role="dialog"]') != null);
  for (const key of ['Tab', 'Tab', 'Tab', 'Shift+Tab', 'Shift+Tab', 'Shift+Tab']) {
    await page.keyboard.press(key);
    expect(await inside(), `focus after ${key}`).toBe(true);
  }
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
});
