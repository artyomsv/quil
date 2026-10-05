import { spawnSync } from 'node:child_process';
import { chmodSync, mkdtempSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { type Page } from '@playwright/test';
import { activePane, bufferText, createPane, expect, ipcRequest, keymapLoaded, listPanes, login, type QuilWeb, testWith } from './harness';

// A stand-in lazygit first on PATH: it answers the daemon's `lazygit
// --version` probe, then prints a marker and echoes its input, so the test
// sees what reaches the overlay's PTY. Each test opens a pane with an
// explicit cwd in a fresh git repo and makes it active, so the overlay's repo
// never comes from the daemon's own default directory.
const bin = mkdtempSync('/tmp/qw-bin-');
const lazygit = path.join(bin, 'lazygit');
writeFileSync(lazygit, '#!/bin/sh\ncase "$1" in --version) echo "version=0.0.0-fake"; exit 0;; esac\necho FAKE-LAZYGIT\nexec cat\n');
chmodSync(lazygit, 0o755);
const test = testWith({ path: bin });

// gitPane logs in, opens a pane in a new git repo and makes it the active
// pane, with the keyboard in its terminal.
async function gitPane(page: Page, quil: QuilWeb): Promise<string> {
  const repo = mkdtempSync('/tmp/qw-git-');
  expect(spawnSync('git', ['init', '-q', repo]).status).toBe(0);
  await login(page, quil);
  await keymapLoaded(page, 'default');
  const id = await createPane(quil.home, { name: 'gitpane', cwd: repo });
  await page.locator('.pane', { has: page.locator('.title', { hasText: 'gitpane' }) }).locator('.term').click();
  await expect.poll(() => activePane(page)).toBe(id);
  return id;
}

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
  await gitPane(page, quil);
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
  await gitPane(page, quil);
  await page.keyboard.press('Alt+g');
  const overlay = page.locator('.slot.overlay');
  await expect(overlay).toBeVisible();
  const id = await overlayId(quil.home);
  await ipcRequest(quil.home, 'destroy_pane_req', { pane_id: id });
  await expect(overlay).toHaveCount(0);
  await expect(page.locator('.pane')).toHaveCount(2);
});

async function overlayCwd(home: string, id: string): Promise<string> {
  const r = await ipcRequest(home, 'state_req', {});
  return (r.payload as { panes?: { id: string; cwd: string }[] }).panes?.find((p) => p.id === id)?.cwd ?? '';
}

// The TUI's rule (internal/tui/overlay.go step 4): a tab's overlay of the
// same tool on ANOTHER repository is replaced, not shown again.
test('Alt+G from a pane in another repository replaces the overlay', async ({ page, quil }) => {
  await gitPane(page, quil);
  await page.keyboard.press('Alt+g');
  const overlay = page.locator('.slot.overlay');
  await expect(overlay).toBeVisible();
  const first = await overlayId(quil.home);
  await page.keyboard.press('Alt+g');
  await expect(overlay).toHaveCount(0);

  const repo2 = mkdtempSync('/tmp/qw-git2-');
  expect(spawnSync('git', ['init', '-q', repo2]).status).toBe(0);
  const other = await createPane(quil.home, { name: 'otherrepo', cwd: repo2 });
  await page.locator('.pane', { has: page.locator('.title', { hasText: 'otherrepo' }) }).locator('.term').click();
  await expect.poll(() => activePane(page)).toBe(other);
  await page.keyboard.press('Alt+g');
  await expect(overlay).toBeVisible();
  let second = '';
  await expect
    .poll(async () => {
      const ids = (await listPanes(quil.home)).filter((p) => p.type === 'lazygit').map((p) => p.id);
      second = ids.length === 1 ? (ids[0] ?? '') : '';
      return second !== '' && second !== first;
    })
    .toBe(true);
  expect(await overlayCwd(quil.home, second)).toBe(repo2);
});

// Several repositories under the active pane's folder open a picker, as in
// the TUI; the overlay opens on the one picked.
test('Alt+G in a folder of several repositories asks which one', async ({ page, quil }) => {
  const base = mkdtempSync('/tmp/qw-repos-');
  for (const name of ['alpha', 'beta']) expect(spawnSync('git', ['init', '-q', path.join(base, name)]).status).toBe(0);
  await login(page, quil);
  await keymapLoaded(page, 'default');
  const id = await createPane(quil.home, { name: 'repos', cwd: base });
  await page.locator('.pane', { has: page.locator('.title', { hasText: 'repos' }) }).locator('.term').click();
  await expect.poll(() => activePane(page)).toBe(id);
  await page.keyboard.press('Alt+g');
  const picker = page.getByRole('menu', { name: 'Repository for lazygit' });
  await expect(picker).toBeVisible();
  await expect(picker.getByRole('menuitem')).toHaveCount(2);
  await picker.getByRole('menuitem', { name: /beta$/ }).click();
  await expect(page.locator('.slot.overlay')).toBeVisible();
  const ov = await overlayId(quil.home);
  expect(await overlayCwd(quil.home, ov)).toBe(path.join(base, 'beta'));
});
