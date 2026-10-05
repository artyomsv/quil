import { spawnSync } from 'node:child_process';
import { chmodSync, mkdtempSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { bufferText, expect, ipcRequest, keymapLoaded, listPanes, login, testWith } from './harness';

// A stand-in lazygit first on PATH: it answers the daemon's `lazygit
// --version` probe, then prints a marker and echoes its input, so the test
// sees what reaches the overlay's PTY. The daemon opens the first pane in
// QUIL_HOME; each test makes that a git repo, which git_repos_req then names
// as the overlay's repo.
const bin = mkdtempSync('/tmp/qw-bin-');
const lazygit = path.join(bin, 'lazygit');
writeFileSync(lazygit, '#!/bin/sh\ncase "$1" in --version) echo "version=0.0.0-fake"; exit 0;; esac\necho FAKE-LAZYGIT\nexec cat\n');
chmodSync(lazygit, 0o755);
const test = testWith({ path: bin });

async function overlayId(home: string): Promise<string> {
  let id = '';
  await expect
    .poll(async () => {
      id = (await listPanes(home)).find((p) => p.type === 'lazygit')?.id ?? '';
      return id;
    })
    .not.toBe('');
  return id;
}

test('Alt+G shows lazygit over the panes, takes the keys, and hides again', async ({ page, quil }) => {
  expect(spawnSync('git', ['init', '-q', quil.home]).status).toBe(0);
  await login(page, quil);
  await keymapLoaded(page, 'default');
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('Alt+g');
  const overlay = page.locator('.slot.overlay');
  await expect(overlay).toBeVisible();
  const id = await overlayId(quil.home);
  await expect.poll(() => bufferText(page, id)).toContain('FAKE-LAZYGIT');

  // Alt+R is pane.restart outside an overlay; here it is input for the tool.
  // The tty echoes the ESC that Alt sends as ^[.
  await page.keyboard.press('Alt+r');
  await expect.poll(() => bufferText(page, id)).toContain('^[r');
  expect((await listPanes(quil.home)).some((p) => p.id === id)).toBe(true);

  await page.keyboard.press('Alt+g');
  await expect(overlay).toHaveCount(0);
  // The same key shows the tool that is already running; no second one.
  await page.keyboard.press('Alt+g');
  await expect(overlay).toBeVisible();
  expect((await listPanes(quil.home)).filter((p) => p.type === 'lazygit')).toHaveLength(1);
});

test('an overlay that leaves the state leaves the page', async ({ page, quil }) => {
  expect(spawnSync('git', ['init', '-q', quil.home]).status).toBe(0);
  await login(page, quil);
  await keymapLoaded(page, 'default');
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('Alt+g');
  const overlay = page.locator('.slot.overlay');
  await expect(overlay).toBeVisible();
  const id = await overlayId(quil.home);
  await ipcRequest(quil.home, 'destroy_pane_req', { pane_id: id });
  await expect(overlay).toHaveCount(0);
  await expect(page.locator('.pane')).toHaveCount(1);
});
