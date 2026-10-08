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
