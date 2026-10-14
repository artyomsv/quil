import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import type { Page } from '@playwright/test';
import { expect, keymapLoaded, login, test, testWithPlugins } from './harness';

async function openF1(page: Page, item: string): Promise<void> {
  await page.keyboard.press('F1');
  await page.getByRole('menuitem', { name: item }).click();
}

test('Settings: a value persists across a reload; a bad one shows its error', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  await openF1(page, 'Settings');
  const box = page.getByRole('dialog', { name: 'Settings' });
  const field = box.getByLabel('Scrollback lines');
  await field.fill('4321');
  await field.press('Enter');
  await expect(field).toHaveValue('4321');
  await expect.poll(() => readFileSync(path.join(quil.home, 'config.toml'), 'utf8')).toContain('scrollback_lines = 4321');
  await page.reload();
  await keymapLoaded(page, 'default');
  await openF1(page, 'Settings');
  const f2 = page.getByRole('dialog', { name: 'Settings' }).getByLabel('Scrollback lines');
  await expect(f2).toHaveValue('4321');
  await f2.fill('0');
  await f2.press('Enter');
  await expect(page.getByRole('dialog', { name: 'Settings' }).getByText('must be a whole number above 0')).toBeVisible();
  await expect(f2).toHaveValue('4321');
  // Refused on the page, before any request: the stored value comes back too.
  await f2.fill('abc');
  await f2.press('Enter');
  await expect(page.getByRole('dialog', { name: 'Settings' }).getByText('must be a whole number', { exact: true })).toBeVisible();
  await expect(f2).toHaveValue('4321');
});

// stalePlugin is a valid plugin file with no schema_version: behind every
// shipped default that has one.
const stalePlugin = (name: string): string => `[plugin]\nname = "${name}"\ndisplay_name = "${name}"\ncategory = "tools"\n\n[command]\ncmd = "sh"\n`;
const staleClaude = stalePlugin('claude-code');

testWithPlugins({ 'claude-code.toml': staleClaude })(
  'Plugin update: Escape closes it at once; Save mine with the version raised clears the notice',
  async ({ page, quil }) => {
    await login(page, quil);
    await keymapLoaded(page, 'default');
    const item = '1 plugin file needs an update';
    await openF1(page, item);
    const box = page.getByRole('dialog', { name: 'Plugin update' });
    await expect(box).toBeVisible();
    await page.keyboard.press('Escape'); // the dialog holds focus from the start
    await expect(box).toBeHidden();
    await openF1(page, item);
    const need = /schema_version (\d+)/.exec(await box.locator('.note').first().innerText())?.[1];
    expect(need).toBeTruthy();
    await box.getByLabel('Your file').fill(staleClaude.replace('name = "claude-code"\n', `name = "claude-code"\nschema_version = ${need}\n`));
    await box.getByRole('button', { name: 'Save mine' }).click();
    await expect(box.getByText('Every plugin file is up to date.')).toBeVisible();
    expect(readFileSync(path.join(quil.home, 'plugins', 'claude-code.toml'), 'utf8')).toContain(`schema_version = ${need}`);
  },
);

// Three stale files, listed claude-code, codex, hunk. A draft for codex is
// kept, visible and copyable, after another client updates codex first and
// while hunk still waits.
testWithPlugins({ 'claude-code.toml': staleClaude, 'codex.toml': stalePlugin('codex'), 'hunk.toml': stalePlugin('hunk') })(
  'Plugin update: a draft another client overtook stays readable while other files remain',
  async ({ page, quil }) => {
    await login(page, quil);
    await keymapLoaded(page, 'default');
    await openF1(page, '3 plugin files need an update');
    const box = page.getByRole('dialog', { name: 'Plugin update' });
    await expect(box.getByRole('heading', { name: /claude-code \(1 of 3\)/ })).toBeVisible();
    await box.getByRole('button', { name: 'Next' }).click();
    await expect(box.getByRole('heading', { name: /codex \(2 of 3\)/ })).toBeVisible();
    await box.getByLabel('Your file').fill(`${stalePlugin('codex')}# my codex draft\n`);
    await box.getByRole('button', { name: 'Previous' }).click();
    await expect(box.getByRole('heading', { name: /claude-code \(1 of 3\)/ })).toBeVisible();
    // Another client updates codex past the shipped version: it leaves the stale list.
    writeFileSync(path.join(quil.home, 'plugins', 'codex.toml'), stalePlugin('codex').replace('name = "codex"\n', 'name = "codex"\nschema_version = 99\n'));
    await box.getByRole('button', { name: 'Use default' }).click();
    await expect(box.getByRole('heading', { name: /hunk \(1 of 1\)/ })).toBeVisible();
    await expect(box.getByLabel('Unsaved text for codex')).toHaveValue(/# my codex draft/);
    // Close asks first: the draft is unsaved text.
    await box.getByRole('button', { name: 'Close' }).click();
    await expect(box.getByRole('button', { name: 'Discard and close' })).toBeVisible();
  },
);

test('Templates: a valid save reaches the template list; an invalid one is refused', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  await openF1(page, 'Settings');
  await page.getByRole('dialog', { name: 'Settings' }).getByRole('button', { name: 'Templates…' }).click();
  const ed = page.getByRole('dialog', { name: 'Templates' });
  const area = ed.getByLabel('Templates text');
  await expect(area).toBeVisible();
  const text = await area.inputValue();
  await area.fill(`${text}\n[[templates]]\nname = "e2e-one"\n[[templates.panes]]\ntype = "terminal"\n`);
  await ed.getByRole('button', { name: 'Save' }).click();
  await expect(ed.getByRole('status')).toHaveText('Saved');
  expect(readFileSync(path.join(quil.home, 'templates.toml'), 'utf8')).toContain('e2e-one');
  await area.fill('[[templates]\nname=');
  await ed.getByRole('button', { name: 'Save' }).click();
  await expect(ed.getByRole('status')).toContainText('Not saved');
  expect(readFileSync(path.join(quil.home, 'templates.toml'), 'utf8')).toContain('e2e-one');
  await ed.getByRole('button', { name: 'Close' }).click();
  await ed.getByRole('button', { name: 'Discard and close' }).click();
  await page.keyboard.press('Alt+Shift+P');
  await page.keyboard.type('New from template');
  await page.keyboard.press('Enter');
  // An <option> inside a <select> is never "visible" to Playwright: check it exists.
  await expect(page.locator('option', { hasText: 'e2e-one' })).toHaveCount(1);
});

const tool = '[plugin]\nname = "e2e-tool"\ndisplay_name = "E2E Tool"\ncategory = "tools"\n\n[command]\ncmd = "sh"\n';

testWithPlugins({ 'e2e-tool.toml': tool })('Plugin editor: edit, save, reopen', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  await openF1(page, 'Plugins');
  const box = page.getByRole('dialog', { name: 'Plugins' });
  await box.locator('li', { hasText: 'e2e-tool' }).getByRole('button', { name: 'Edit' }).click();
  const ed = page.getByRole('dialog', { name: 'Plugin: e2e-tool' });
  const area = ed.getByLabel('Plugin: e2e-tool text');
  await area.fill(tool.replace('E2E Tool', 'E2E Tool Two'));
  await ed.getByRole('button', { name: 'Save' }).click();
  await expect(ed.getByRole('status')).toHaveText('Saved');
  await ed.getByRole('button', { name: 'Close' }).click();
  await openF1(page, 'Plugins');
  await page.getByRole('dialog', { name: 'Plugins' }).locator('li', { hasText: 'e2e-tool' }).getByRole('button', { name: 'Edit' }).click();
  await expect(page.getByRole('dialog', { name: 'Plugin: e2e-tool' }).getByLabel('Plugin: e2e-tool text')).toHaveValue(/E2E Tool Two/);
});

test('Logs: the Web tab shows the gateway log', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  await openF1(page, 'Logs');
  const box = page.getByRole('dialog', { name: 'Logs' });
  await box.getByRole('tab', { name: 'Web' }).click();
  await expect(box.locator('pre')).toContainText('web gateway listening');
});

test("What's New opens", async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  await openF1(page, "What's New");
  await expect(page.getByRole('dialog', { name: "What's New" })).toBeVisible();
});

test('Keys: switching the preset changes the keymap without a reload', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  await openF1(page, 'Settings');
  const box = page.getByRole('dialog', { name: 'Settings' });
  await box.getByRole('tab', { name: 'Keys' }).click();
  await box.getByLabel('Preset').selectOption('tmux');
  await keymapLoaded(page, 'tmux');
});

test('the beta notice shows on a fresh home and OK hides it', async ({ page, quil }) => {
  await login(page, quil, { keepBeta: true });
  const n = page.getByRole('dialog', { name: 'Beta notice' });
  await expect(n).toBeVisible();
  await n.getByRole('button', { name: 'OK' }).click();
  await expect(n).toHaveCount(0);
});
