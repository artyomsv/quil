import { writeFileSync } from 'node:fs';
import path from 'node:path';
import { expect, keymapLoaded, login, test } from './harness';

const tmux = 'preset = "tmux"\n';

// AC-10 (browser half): under the tmux preset, Ctrl+B % splits the active
// pane. The keys are typed with the terminal focused, so this also proves
// xterm's own textarea is not taken for a form field.
test('ctrl+b % splits under the tmux preset', async ({ page, quil }) => {
  writeFileSync(path.join(quil.home, 'bindings.toml'), tmux);
  await login(page, quil);
  await keymapLoaded(page, 'tmux');
  await expect(page.locator('.pane')).toHaveCount(1);
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('Control+b');
  await page.keyboard.press('%');
  await expect(page.locator('.pane')).toHaveCount(2);
});

// AC-12 (browser half): a preset saved while the page is open applies after
// a reload, because /api/client reads bindings.toml per request.
test('a preset switched on disk applies at the next page load', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('Control+b');
  await page.keyboard.press('%');
  await page.waitForTimeout(1_000);
  await expect(page.locator('.pane')).toHaveCount(1);

  writeFileSync(path.join(quil.home, 'bindings.toml'), tmux);
  await page.reload();
  await expect(page.locator('.pane').first()).toBeVisible();
  await keymapLoaded(page, 'tmux');
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('Control+b');
  await page.keyboard.press('%');
  await expect(page.locator('.pane')).toHaveCount(2);
});

test('F1 → Shortcuts lists the keys and Esc returns to the terminal', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('F1');
  await page.getByRole('menuitem', { name: 'Shortcuts' }).click();
  const keys = page.getByRole('dialog', { name: 'Keys' });
  await expect(keys).toBeVisible();
  // pane.close's default Ctrl+W is the browser's; the list shows its stand-in.
  await expect(keys.getByText('alt+shift+c', { exact: true })).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(keys).toHaveCount(0);
});

test('a TUI-only key is consumed with a notice', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('Alt+Shift+P'); // command palette
  await expect(page.getByText('available in the TUI')).toBeVisible();
});

// The browser stand-in for a reserved chord dispatches like any binding.
test('the browser key for pane.close asks to close the pane', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('Alt+Shift+C');
  await expect(page.getByRole('dialog', { name: 'Close pane' })).toBeVisible();
  // The dialog owns the keyboard now: Alt+N does not open the list behind it.
  await page.keyboard.press('Alt+n');
  await expect(page.locator('.notify')).toHaveCount(0);
});
