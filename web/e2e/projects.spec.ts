import { expect, ipcRequest, keymapLoaded, login, test } from './harness';

async function projectNames(home: string): Promise<string[]> {
  const r = await ipcRequest(home, 'list_projects_req', {});
  return ((r.payload as { projects?: { name: string }[] }).projects ?? []).map((p) => p.name);
}

test('new project adopts Default on a fresh daemon', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('Alt+Shift+N');
  const form = page.getByRole('dialog', { name: 'New project' });
  await form.getByLabel('Name').fill('alpha');
  await form.getByRole('button', { name: 'Create' }).click();
  await expect(form).toHaveCount(0);
  await expect(page.locator('nav').getByRole('button', { name: 'alpha', exact: true })).toBeVisible();
  expect(await projectNames(quil.home)).toEqual(['alpha']);
});

test('a second project is created beside the first', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  for (const name of ['one', 'two']) {
    await page.locator('.pane .term').first().click();
    await page.keyboard.press('Alt+Shift+N');
    const form = page.getByRole('dialog', { name: 'New project' });
    await form.getByLabel('Name').fill(name);
    await form.getByRole('button', { name: 'Create' }).click();
    await expect(page.locator('nav').getByRole('button', { name, exact: true })).toBeVisible();
  }
  expect((await projectNames(quil.home)).sort()).toEqual(['one', 'two']);
});

async function projectRoots(home: string): Promise<string[]> {
  const r = await ipcRequest(home, 'list_projects_req', {});
  return ((r.payload as { projects?: { root_dir: string }[] }).projects ?? []).map((p) => p.root_dir);
}

test('the daemon resolves a typed folder; a missing one keeps the form open', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  await page.locator('.pane .term').first().click();
  await page.keyboard.press('Alt+Shift+N');
  const form = page.getByRole('dialog', { name: 'New project' });
  await form.getByLabel('Name').fill('lost');
  await form.getByLabel(/Folder/).fill('/no/such/folder/5c');
  await form.getByRole('button', { name: 'Create' }).click();
  await expect(page.getByText(/^Folder:/)).toBeVisible();
  await expect(form).toBeVisible();
  await form.getByLabel(/Folder/).fill('~');
  await form.getByRole('button', { name: 'Create' }).click();
  await expect(form).toHaveCount(0);
  const roots = await projectRoots(quil.home);
  expect(roots.length).toBe(1);
  expect(roots[0]?.startsWith('/')).toBe(true);
});

test('a project filed in a new group shows under it', async ({ page, quil }) => {
  await login(page, quil);
  await keymapLoaded(page, 'default');
  await page.locator('nav').getByRole('button', { name: 'Project menu' }).first().click();
  await page.getByRole('menuitem', { name: 'Move to new group…' }).click();
  const form = page.getByRole('dialog', { name: 'New group' });
  await form.getByRole('textbox').fill('ops');
  await form.getByRole('button', { name: 'Create' }).click();
  await expect(page.locator('nav .group', { hasText: 'ops' })).toBeVisible();
});
