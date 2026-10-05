import { activePane, createPane, expect, fakeTUI, ipcRequest, keymapLoaded, login, stopDaemon, test, typeInto } from './harness';

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

// A card dismissed by ANOTHER client (a TUI) leaves this page too: the daemon
// broadcasts event_dismissed to every client.
test('a dismissal in another client removes the card here', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  const tui = await fakeTUI(quil.home, 'tui-dismiss');
  try {
    const victim = await createPane(quil.home, { name: 'victim' });
    await ipcRequest(quil.home, 'destroy_pane_req', { pane_id: victim });
    await page.locator('.pane .term').first().click();
    await page.keyboard.press('Alt+n');
    const card = page.locator('.notify .card', { hasText: 'Pane closed' });
    await expect(card).toBeVisible();
    const r = await ipcRequest(quil.home, 'get_notifications_req', {});
    const events = (r.payload as { events?: { id: string; pane_id: string }[] | null }).events ?? [];
    const ev = events.find((e) => e.pane_id === victim);
    if (!ev) throw new Error('no event for the closed pane');
    tui.send({ type: 'dismiss_event', payload: { event_id: ev.id } });
    await expect(card).toHaveCount(0);
  } finally {
    tui.close();
  }
});

// Dismissing needs a live link: with the daemon gone the controls go, so a
// dismissal that could not reach the daemon is never offered.
test('the dismiss controls hide while the link is down', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  const victim = await createPane(quil.home, { name: 'victim' });
  await ipcRequest(quil.home, 'destroy_pane_req', { pane_id: victim });
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('Alt+n');
  const card = page.locator('.notify .card', { hasText: 'Pane closed' });
  await expect(card.getByRole('button', { name: 'Dismiss' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Dismiss all' })).toBeVisible();
  stopDaemon(quil.home);
  await expect(card).toBeVisible();
  await expect(card.getByRole('button', { name: 'Dismiss' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Dismiss all' })).toHaveCount(0);
});
