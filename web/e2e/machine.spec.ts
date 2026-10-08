import { expect, keymapLoaded, login, test } from './harness';

test('F1 → Processes lists the pane process', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('F1');
  await page.getByRole('menuitem', { name: 'Processes' }).click();
  const box = page.getByRole('dialog', { name: 'Processes' });
  await expect(box.locator('tbody tr').first()).toBeVisible({ timeout: 15_000 });
  await page.keyboard.press('Escape');
  await expect(box).toHaveCount(0);
});

test('Processes shows both tabs of one gateway (same PID) and refreshes', async ({ page, quil }) => {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(e.message));
  await login(page, quil);
  await keymapLoaded(page, 'default');
  const other = await page.context().newPage();
  await other.goto(quil.url);
  await expect(other.locator('.pane').first()).toBeVisible();
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('F1');
  await page.getByRole('menuitem', { name: 'Processes' }).click();
  const box = page.getByRole('dialog', { name: 'Processes' });
  const quilRows = box.locator('h3 + table tbody tr');
  await expect.poll(() => quilRows.count(), { timeout: 15_000 }).toBeGreaterThanOrEqual(2);
  // The next poll (5 s) re-renders the same rows without an error.
  await page.waitForTimeout(6_000);
  expect(await quilRows.count()).toBeGreaterThanOrEqual(2);
  expect(errors).toEqual([]);
  await other.close();
});

test('F1 → Plugins lists the daemon plugins', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('F1');
  await page.getByRole('menuitem', { name: 'Plugins' }).click();
  const box = page.getByRole('dialog', { name: 'Plugins' });
  await expect(box.getByText('terminal', { exact: true })).toBeVisible();
  await box.getByRole('button', { name: 'Reload' }).click();
  await expect(box.getByText('terminal', { exact: true })).toBeVisible();
});

test('F1 → Update never reads a missing update as up to date', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('F1');
  await page.getByRole('menuitem', { name: 'Update' }).click();
  const box = page.getByRole('dialog', { name: 'Update' });
  await expect(box.getByText(/no update reported by the daemon/)).toBeVisible();
  await expect(box.getByRole('button', { name: 'Download' })).toBeDisabled();
});
