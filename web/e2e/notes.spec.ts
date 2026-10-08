import type { Page } from '@playwright/test';
import { createPane, expect, ipcRequest, keymapLoaded, login, paneMenu, type QuilWeb, test } from './harness';

async function openNotes(page: Page, title: string) {
  await paneMenu(page, title);
  await page.getByRole('menuitem', { name: 'Notes…' }).click();
  return page.getByRole('dialog', { name: /Notes/ });
}

async function noteText(quil: QuilWeb, id: string): Promise<string> {
  const r = await ipcRequest(quil.home, 'note_get', { pane_id: id });
  return (r.payload as { text: string }).text;
}

test('a multi-line note saved in the browser keeps its line breaks', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  const id = await createPane(quil.home, { name: 'noted' });
  const ed = await openNotes(page, 'noted');
  const area = ed.getByRole('textbox');
  await expect(area).toBeEditable();
  await area.fill('line one\nline two');
  await expect(ed.getByText('Unsaved', { exact: true })).toBeVisible();
  await page.keyboard.press('Control+s');
  await expect(ed.getByText('Saved', { exact: true })).toBeVisible();
  expect(await noteText(quil, id)).toBe('line one\nline two\n');
});

test('an IPC save makes the browser save conflict; keep mine wins', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  const id = await createPane(quil.home, { name: 'shared' });
  const ed = await openNotes(page, 'shared');
  const area = ed.getByRole('textbox');
  await expect(area).toBeEditable();
  const first = await ipcRequest(quil.home, 'note_get', { pane_id: id });
  const base = (first.payload as { rev: number }).rev;
  await area.fill('mine');
  await expect(ed.getByText('Unsaved', { exact: true })).toBeVisible();
  // Another client saves first, from the same base; its state frame marks
  // the browser's unsaved text as a conflict.
  await ipcRequest(quil.home, 'note_set', { pane_id: id, text: 'theirs\n', base_rev: base });
  await expect(ed.getByText(/changed elsewhere/)).toBeVisible();
  await ed.getByRole('button', { name: 'Keep mine' }).click();
  await expect(ed.getByText('Saved', { exact: true })).toBeVisible();
  expect(await noteText(quil, id)).toBe('mine\n');
});

// secondTab is another browser tab of the same login: the login code is
// single-use, and the login key is shared per origin (lib/storage.ts).
async function secondTab(page: Page, quil: QuilWeb): Promise<Page> {
  const p = await page.context().newPage();
  await p.goto(quil.url);
  await expect(p.locator('.pane').first()).toBeVisible();
  await keymapLoaded(p, 'default');
  return p;
}

test('two browser tabs on one note: the later one sees the conflict', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  const id = await createPane(quil.home, { name: 'two' });
  const other = await secondTab(page, quil);
  const a = await openNotes(page, 'two');
  const b = await openNotes(other, 'two');
  await expect(a.getByRole('textbox')).toBeEditable();
  await expect(b.getByRole('textbox')).toBeEditable();
  await a.getByRole('textbox').fill('from a');
  await b.getByRole('textbox').fill('from b');
  await a.getByRole('textbox').press('Control+s');
  await expect(a.getByText('Saved', { exact: true })).toBeVisible();
  // a's save bumps the rev; b holds unsaved text, so the frame marks it.
  await expect(b.getByText(/changed elsewhere/)).toBeVisible();
  await b.getByRole('button', { name: 'Keep mine' }).click();
  await expect(b.getByText('Saved', { exact: true })).toBeVisible();
  expect(await noteText(quil, id)).toBe('from b\n');
  await other.close();
});
