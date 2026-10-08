import { createTab, expect, keymapLoaded, login, tabButton, test, typeInto } from './harness';

test('the palette switches tab by name', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  await createTab(quil.home, 'zebra');
  await expect(tabButton(page, 'zebra')).toBeVisible();
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('Alt+Shift+P');
  const box = page.getByRole('dialog', { name: 'Command palette' });
  await box.getByRole('combobox').fill('zebra');
  await page.keyboard.press('Enter');
  await expect(box).toHaveCount(0);
  await expect(tabButton(page, 'zebra')).toHaveClass(/active/);
});

test('the selected row stays in view when the keys move past the edge', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  await page.setViewportSize({ width: 1000, height: 400 });
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('Alt+Shift+P');
  const box = page.getByRole('dialog', { name: 'Command palette' });
  // Up from the first row wraps to the last one, below the list's fold.
  await page.keyboard.press('ArrowUp');
  await expect(box.locator('button.cur')).toBeInViewport();
  await page.keyboard.press('PageUp');
  await expect(box.locator('button.cur')).toBeInViewport();
});

test('content search finds text echoed in a pane', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  const { paneId } = await createTab(quil.home, 'echo');
  await typeInto(quil.home, paneId, 'echo needle5c\r');
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('Alt+Shift+P');
  const box = page.getByRole('dialog', { name: 'Command palette' });
  await box.getByRole('combobox').fill('needle5c');
  await expect(box.locator('.hit', { hasText: '×' }).first()).toBeVisible({ timeout: 10_000 });
});
