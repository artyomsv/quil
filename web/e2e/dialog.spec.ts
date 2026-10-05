import { mkdirSync, mkdtempSync } from 'node:fs';
import path from 'node:path';
import { expect, fakeTUI, ipcRequest, listPanes, login, tabOf, test, testWithPlugins } from './harness';

const E2E_DIR = `
[plugin]
name = "e2e-dir"
display_name = "E2E Dir"
category = "tools"

[command]
cmd = "cat"
detect = "cat --version"
prompts_cwd = true

[persistence]
strategy = "none"
`;

testWithPlugins({ 'e2e-dir.toml': E2E_DIR })(
  'AC-8: the dialog opens a pane of the chosen type in the folder picked through the page',
  async ({ page, quil }) => {
    await login(page, quil);
    const root = mkdtempSync('/tmp/qw-ac8-');
    mkdirSync(path.join(root, 'picked'));
    const before = (await listPanes(quil.home)).map((p) => p.id);
    await page.locator('.pane').first().getByRole('button', { name: 'Pane menu' }).click();
    await page.getByRole('menuitem', { name: 'New pane…' }).click();
    const dlg = page.getByRole('dialog', { name: 'New pane' });
    await dlg.getByRole('button', { name: 'Tools' }).click();
    await dlg.getByRole('button', { name: 'E2E Dir' }).click();
    const folder = dlg.getByRole('textbox', { name: 'Folder' });
    await folder.fill(root);
    await folder.press('Enter'); // browse_dir_req for the typed path
    await dlg.getByRole('button', { name: 'picked', exact: true }).click(); // descend: browse_dir_req{path, child}
    await expect(folder).toHaveValue(path.join(root, 'picked'));
    await dlg.getByRole('button', { name: 'Continue' }).click();
    await dlg.getByRole('button', { name: 'Right', exact: true }).click();
    await expect(dlg).toHaveCount(0);
    await expect(page.locator('.pane')).toHaveCount(2);
    const st = await ipcRequest(quil.home, 'state_req', {});
    const panes = (st.payload as { panes: { id: string; type?: string; cwd: string }[] }).panes;
    const added = panes.find((p) => !before.includes(p.id));
    expect(added?.type).toBe('e2e-dir');
    expect(added?.cwd).toBe(path.join(root, 'picked'));
  },
);

test('AC-8: the tab bar + opens a new tab from the dialog', async ({ page, quil }) => {
  await login(page, quil);
  const tabsBefore = await page.locator('header .tab').count();
  await page.getByRole('button', { name: 'New tab' }).click();
  const dlg = page.getByRole('dialog', { name: 'New pane' });
  await dlg.getByRole('button', { name: 'Terminal' }).first().click();
  await dlg.getByRole('button', { name: 'Terminal' }).first().click();
  await expect(dlg).toHaveCount(0);
  await expect(page.locator('header .tab')).toHaveCount(tabsBefore + 1);
});

test("AC-13: replace keeps the old leaf's place, orientation and ratio in every client", async ({ page, quil }) => {
  await login(page, quil);
  const tui = await fakeTUI(quil.home, 'tui-ac13');
  try {
    const [first] = await listPanes(quil.home);
    if (!first) throw new Error('no pane');
    // Right, then below on the right pane, then drag that inner border to ~0.3.
    await page.locator('.pane').first().getByRole('button', { name: 'Pane menu' }).click();
    await page.getByRole('menuitem', { name: 'Split right' }).click();
    await expect(page.locator('.pane')).toHaveCount(2);
    await page.locator('.pane').nth(1).getByRole('button', { name: 'Pane menu' }).click();
    await page.getByRole('menuitem', { name: 'Split below' }).click();
    await expect(page.locator('.pane')).toHaveCount(3);
    const bar = page.locator('.split-bar.h');
    await expect(bar).toHaveCount(1);
    const box = await bar.boundingBox();
    const area = await page.locator('.area').boundingBox();
    if (!box || !area) throw new Error('no geometry');
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width / 2, area.y + area.height * 0.3, { steps: 8 });
    await page.mouse.up();
    const rightOf = (): { split?: number; ratio?: number; left?: { pane_id?: string } } | undefined =>
      tabOf(tui.state(), first.tab_id)?.layout?.right as { split?: number; ratio?: number; left?: { pane_id?: string } } | undefined;
    await expect.poll(() => rightOf()?.ratio ?? 0.5).toBeLessThan(0.4);
    const before = rightOf();
    const replaced = before?.left?.pane_id;
    expect(replaced).toBeTruthy();
    // Replace the upper-right pane through the dialog.
    await page.locator('.pane').nth(1).getByRole('button', { name: 'Pane menu' }).click();
    await page.getByRole('menuitem', { name: 'Replace…' }).click();
    const dlg = page.getByRole('dialog', { name: 'New pane' });
    await dlg.getByRole('button', { name: 'Terminal' }).first().click();
    await dlg.getByRole('button', { name: 'Terminal' }).first().click();
    await dlg.getByRole('button', { name: 'Replace', exact: true }).click();
    await expect.poll(() => rightOf()?.left?.pane_id).not.toBe(replaced);
    const after = rightOf();
    expect(after?.left?.pane_id).toBeTruthy();
    expect(after?.split).toBe(before?.split);
    expect(after?.ratio).toBeCloseTo(before?.ratio ?? 0, 5);
    // The page shows the same three panes, the replaced one gone.
    await expect(page.locator('.pane')).toHaveCount(3);
    expect((await listPanes(quil.home)).map((p) => p.id)).not.toContain(replaced);
  } finally {
    tui.close();
  }
});
