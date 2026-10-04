import http from 'node:http';
import {
  bufferText,
  createPane,
  createTab,
  expect,
  fakeTUI,
  ipcSend,
  listPanes,
  login,
  readPaneOutput,
  tabButton,
  test,
  typeInto,
} from './harness';

// rawGet sends a GET straight from Node, so no browser adds or drops a header.
function rawGet(url: string, headers: Record<string, string>): Promise<number> {
  return new Promise((resolve, reject) => {
    const req = http.get(url, { headers }, (res) => {
      res.resume();
      resolve(res.statusCode ?? 0);
    });
    req.on('upgrade', () => resolve(101));
    req.on('error', reject);
  });
}

const upgradeHeaders = {
  Connection: 'Upgrade',
  Upgrade: 'websocket',
  'Sec-WebSocket-Version': '13',
  'Sec-WebSocket-Key': 'dGhlIHNhbXBsZSBub25jZQ==',
};

test('login shows the first tab', async ({ page, quil }) => {
  await login(page, quil);
  await expect(page.locator('header .tab')).toHaveCount(1);
  await expect(page.locator('.pane')).toHaveCount(1);
  const [pane] = await listPanes(quil.home);
  expect(pane).toBeDefined();
  // The shell's prompt reaches the page's terminal.
  await expect.poll(() => bufferText(page, pane?.id ?? '')).not.toBe('');
});

test('a pane created elsewhere appears without a reload', async ({ page, quil }) => {
  await login(page, quil);
  await expect(page.locator('.pane')).toHaveCount(1);
  let reloaded = false;
  page.on('framenavigated', () => {
    reloaded = true;
  });
  await createPane(quil.home, { name: 'from-ipc' });
  await expect(page.locator('.pane')).toHaveCount(2);
  await expect(page.locator('.pane .title', { hasText: 'from-ipc' })).toBeVisible();
  expect(reloaded).toBe(false);
});

test('a hidden tab keeps its history and shows it on first view', async ({ page, quil }) => {
  await login(page, quil);
  const { paneId } = await createTab(quil.home, 'second', 'hidden-pane');
  await expect(tabButton(page, 'second')).toBeVisible();
  // The shell expands the arithmetic, so the marker is in the output only
  // once the command ran, never in the echoed input.
  await typeInto(quil.home, paneId, 'echo marker-$((100+23))\r');
  await expect.poll(() => readPaneOutput(quil.home, paneId)).toContain('marker-123');
  // Stay on the first tab: the pane has never been shown.
  await page.waitForTimeout(2_000);
  await expect(page.locator('.pane .title', { hasText: 'hidden-pane' })).toHaveCount(0);

  await tabButton(page, 'second').click();
  await expect(page.locator('.pane .title', { hasText: 'hidden-pane' })).toBeVisible();
  await expect
    .poll(async () => {
      const text = await bufferText(page, paneId);
      return text !== '' && text.includes('marker-123');
    })
    .toBe(true);
});

test('typing in the page reaches the pane', async ({ page, quil }) => {
  await login(page, quil);
  const [pane] = await listPanes(quil.home);
  const paneId = pane?.id ?? '';
  expect(paneId).not.toBe('');
  await page.locator('.pane .term').first().click();
  await page.keyboard.type('echo typed-$((1+1))-in-browser');
  await page.keyboard.press('Enter');
  await expect.poll(() => readPaneOutput(quil.home, paneId), { timeout: 5_000 }).toContain('typed-2-in-browser');
});

test('Take control makes the page size master', async ({ page, quil }) => {
  const tui = await fakeTUI(quil.home, 'e2e-fake-tui');
  try {
    await expect.poll(() => tui.state()?.size_master).toBe('e2e-fake-tui');
    const id = await login(page, quil);
    const button = page.getByRole('button', { name: 'Take control' });
    await expect(button).toBeVisible();
    await button.click();
    await expect.poll(() => tui.state()?.size_master).toBe(id);
    await expect(button).toHaveCount(0);
  } finally {
    tui.close();
  }
});

test('a missing cookie or a foreign origin is refused', async ({ page, quil }) => {
  const origin = quil.url.replace(/\/$/, '');
  const ws = quil.url.replace(/\/$/, '/ws');
  expect(await rawGet(ws, { ...upgradeHeaders, Origin: origin })).toBe(401);

  await login(page, quil);
  const cookie = (await page.context().cookies()).map((c) => `${c.name}=${c.value}`).join('; ');
  expect(cookie).toContain('quil_web_session=');
  expect(await rawGet(ws, { ...upgradeHeaders, Origin: 'http://evil.example', Cookie: cookie })).toBe(403);

  const login403 = await page.request.post(`${origin}/login`, {
    headers: { Origin: 'http://evil.example', 'Content-Type': 'application/json' },
    data: JSON.stringify({ code: quil.code }),
  });
  expect(login403.status()).toBe(403);
});

test('close_tui for the page ends it without a reconnect', async ({ page, quil }) => {
  const id = await login(page, quil);
  let sockets = 0;
  page.on('websocket', () => {
    sockets++;
  });
  await ipcSend(quil.home, 'close_tui', { client: id });
  const banner = page.getByRole('status');
  await expect(banner).toHaveText('Closed by an agent');
  await page.waitForTimeout(3_000);
  await expect(banner).toHaveText('Closed by an agent');
  expect(sockets).toBe(0);
});
