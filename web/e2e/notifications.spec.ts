import { activePane, createPane, expect, ipcRequest, keymapLoaded, login, test, typeInto } from './harness';

// AC-9: an event reaches the sidebar; dismissing it in the browser removes it
// from the daemon's queue, which every client reads.
test('a notification shows and a dismissal reaches the daemon', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  const victim = await createPane(quil.home, { name: 'victim' });
  await expect(page.locator('.pane')).toHaveCount(2);
  await ipcRequest(quil.home, 'destroy_pane_req', { pane_id: victim });
  await expect(page.locator('.pane')).toHaveCount(1);
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('Alt+n');
  const card = page.locator('.notify .card', { hasText: 'Pane closed' });
  await expect(card).toBeVisible();

  await card.getByRole('button', { name: 'Dismiss' }).click();
  await expect(card).toHaveCount(0);
  const r = await ipcRequest(quil.home, 'get_notifications_req', {});
  const events = (r.payload as { events?: { pane_id: string }[] | null }).events ?? [];
  expect(events.some((e) => e.pane_id === victim)).toBe(false);
});

test('the list is rebuilt after a reload', async ({ page, quil }) => {
  await login(page, quil);
  const victim = await createPane(quil.home, { name: 'victim' });
  await ipcRequest(quil.home, 'destroy_pane_req', { pane_id: victim });
  await page.reload();
  await expect(page.locator('.pane').first()).toBeVisible();
  await keymapLoaded(page, 'default');
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('Alt+n');
  await expect(page.locator('.notify .card', { hasText: 'Pane closed' })).toBeVisible();
});

// A card jumps to its pane: clicking it makes that pane the active one.
test('clicking a notification makes its pane active', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  const target = await createPane(quil.home, { name: 'target' });
  await expect(page.locator('.pane')).toHaveCount(2);
  await page.locator('.pane').filter({ hasNotText: 'target' }).locator('.term').click();
  await expect.poll(() => activePane(page)).not.toBe(target);
  // Input from an agent connection is the daemon's "MCP agent typed here"
  // event for that pane.
  await typeInto(quil.home, target, 'true\r');
  await page.keyboard.press('Alt+n');
  const card = page.locator('.notify .card', { hasText: 'MCP agent typed here' });
  await expect(card).toBeVisible();
  await card.getByRole('button').first().click();
  await expect.poll(() => activePane(page)).toBe(target);
});
