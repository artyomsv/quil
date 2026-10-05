import { type ChildProcess, spawn, spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import net from 'node:net';
import path from 'node:path';
import { expect, type Page, test as base } from '@playwright/test';

// The repository root: Playwright runs from web/, and CI builds quil and
// quild there.
const ROOT = path.resolve(process.cwd(), '..');
const QUIL = path.join(ROOT, 'quil');

export interface QuilWeb {
  url: string;
  code: string;
  home: string;
  stop(): Promise<void>;
}

export interface Message {
  type: string;
  id?: string;
  payload?: unknown;
}

// startQuilWeb runs `quil web` against a fresh, short QUIL_HOME (a unix
// socket path over about 108 bytes does not bind). quil web starts the
// daemon itself. It resolves once the URL and the login code are printed.
// plugins (file name → TOML) are written to the home's plugins directory
// first, since quil web and the daemon load plugin definitions at start.
// path is put first on PATH; the daemon quil web starts inherits it.
export interface QuilWebOpts {
  plugins?: Record<string, string>;
  path?: string;
}

export async function startQuilWeb(opts: QuilWebOpts = {}): Promise<QuilWeb> {
  const home = mkdtempSync('/tmp/qw-');
  if (opts.plugins) {
    mkdirSync(path.join(home, 'plugins'), { recursive: true });
    for (const [file, body] of Object.entries(opts.plugins)) writeFileSync(path.join(home, 'plugins', file), body);
  }
  const env: NodeJS.ProcessEnv = { ...process.env, QUIL_HOME: home };
  if (opts.path) env.PATH = `${opts.path}${path.delimiter}${process.env.PATH ?? ''}`;
  const child = spawn(QUIL, ['web', '--no-open', '--port', '0'], { cwd: ROOT, env, stdio: ['pipe', 'pipe', 'pipe'] });
  let out = '';
  let err = '';
  child.stderr?.on('data', (b: Buffer) => {
    err += b.toString();
  });
  // A start that fails still leaves no quil web or daemon behind: the child
  // is stopped and the daemon it may have started is told to stop.
  let failed = false;
  const fail = (reject: (e: Error) => void, msg: string): void => {
    if (failed) return;
    failed = true;
    void stopQuilWeb(child, env).finally(() => reject(new Error(msg)));
  };
  const ready = await new Promise<{ url: string; code: string }>((resolve, reject) => {
    const timer = setTimeout(() => fail(reject, `quil web did not start: ${out}${err}`), 30_000);
    child.on('exit', (c) => {
      clearTimeout(timer);
      fail(reject, `quil web exited (${c}): ${out}${err}`);
    });
    child.stdout?.on('data', (b: Buffer) => {
      out += b.toString();
      const url = /Quil web: (http:\/\/\S+\/)/.exec(out)?.[1];
      const code = /Login code: ([0-9A-Z]{5}-[0-9A-Z]{5})/.exec(out)?.[1];
      if (url && code) {
        clearTimeout(timer);
        resolve({ url, code });
      }
    });
  });
  child.removeAllListeners('exit');
  return { ...ready, home, stop: () => stopQuilWeb(child, env) };
}

async function stopQuilWeb(child: ChildProcess, env: NodeJS.ProcessEnv): Promise<void> {
  if (child.exitCode === null) {
    const exited = new Promise<void>((resolve) => child.once('exit', () => resolve()));
    child.kill('SIGINT');
    const timeout = new Promise<void>((resolve) => setTimeout(resolve, 10_000));
    await Promise.race([exited, timeout]);
    if (child.exitCode === null) child.kill('SIGKILL');
  }
  // quil web leaves its daemon running, as it would for a user. The home
  // directory stays: CI uploads its logs when a test fails.
  spawnSync(QUIL, ['daemon', 'stop'], { cwd: ROOT, env, timeout: 15_000 });
}

// FrameConn is one IPC connection to the daemon: a 4-byte big-endian length
// before each JSON message, both ways.
class FrameConn {
  private buf = Buffer.alloc(0);
  private readonly waiters: { match: (m: Message) => boolean; resolve: (m: Message) => void }[] = [];
  onMessage: (m: Message) => void = () => {};

  private constructor(private readonly sock: net.Socket) {
    sock.on('data', (b: Buffer) => this.read(b));
  }

  static open(home: string): Promise<FrameConn> {
    return new Promise((resolve, reject) => {
      const sock = net.createConnection(path.join(home, 'quild.sock'));
      sock.once('error', reject);
      sock.once('connect', () => resolve(new FrameConn(sock)));
    });
  }

  send(m: Message): void {
    const body = Buffer.from(JSON.stringify(m));
    const head = Buffer.alloc(4);
    head.writeUInt32BE(body.length);
    this.sock.write(Buffer.concat([head, body]));
  }

  next(match: (m: Message) => boolean, timeoutMs = 10_000): Promise<Message> {
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('no matching IPC message')), timeoutMs);
      this.waiters.push({
        match,
        resolve: (m) => {
          clearTimeout(timer);
          resolve(m);
        },
      });
    });
  }

  close(): void {
    this.sock.destroy();
  }

  private read(b: Buffer): void {
    this.buf = Buffer.concat([this.buf, b]);
    while (this.buf.length >= 4) {
      const n = this.buf.readUInt32BE(0);
      if (this.buf.length < 4 + n) return;
      const m = JSON.parse(this.buf.subarray(4, 4 + n).toString()) as Message;
      this.buf = this.buf.subarray(4 + n);
      this.onMessage(m);
      const i = this.waiters.findIndex((w) => w.match(m));
      if (i >= 0) this.waiters.splice(i, 1)[0]?.resolve(m);
    }
  }
}

let nextId = 0;
const newId = (prefix: string): string => `${prefix}-${process.pid}-${++nextId}`;

// hello registers the conn as a protocol-1 client of kind and waits for the
// daemon's answer.
async function hello(c: FrameConn, kind: string, clientId: string): Promise<void> {
  const id = newId('hello');
  c.send({
    type: 'hello',
    id,
    payload: { kind, proto: 1, client_id: clientId, version: 'e2e', pid: process.pid, exe: 'playwright', uptime_ms: 0 },
  });
  await c.next((m) => m.id === id && m.type === 'hello_resp');
}

// ipcRequest sends one request on a fresh connection, as the MCP bridge
// does, and returns the response carrying the same id.
export async function ipcRequest(home: string, type: string, payload: unknown, id = newId('e2e')): Promise<Message> {
  const c = await FrameConn.open(home);
  try {
    await hello(c, 'bridge', newId('bridge'));
    const resp = c.next((m) => m.id === id);
    c.send({ type, id, payload });
    return await resp;
  } finally {
    c.close();
  }
}

// ipcSend sends a message the daemon does not answer (close_tui).
export async function ipcSend(home: string, type: string, payload: unknown): Promise<void> {
  const c = await FrameConn.open(home);
  try {
    await hello(c, 'bridge', newId('bridge'));
    c.send({ type, payload });
    // A version request is answered in order after the message above, so its
    // answer means the daemon has read it.
    const id = newId('flush');
    const flushed = c.next((m) => m.id === id);
    c.send({ type: 'version_req', id });
    await flushed;
  } finally {
    c.close();
  }
}

export async function createPane(home: string, payload: Record<string, unknown>): Promise<string> {
  const r = await ipcRequest(home, 'create_pane_req', payload);
  const p = r.payload as { pane_id?: string; error?: string };
  if (!p.pane_id) throw new Error(`create_pane_req failed: ${p.error ?? JSON.stringify(r)}`);
  return p.pane_id;
}

export async function createTab(home: string, name: string, firstPaneName = ''): Promise<{ tabId: string; paneId: string }> {
  const r = await ipcRequest(home, 'create_tab_req', { name, first_pane: firstPaneName ? { name: firstPaneName } : undefined });
  const p = r.payload as { tab_id?: string; pane_id?: string; error?: string };
  if (!p.tab_id || !p.pane_id) throw new Error(`create_tab_req failed: ${p.error ?? JSON.stringify(r)}`);
  return { tabId: p.tab_id, paneId: p.pane_id };
}

// typeInto writes text to a pane's stdin; Enter is CR.
export async function typeInto(home: string, paneId: string, text: string): Promise<void> {
  const r = await ipcRequest(home, 'pane_input', { pane_id: paneId, data: Buffer.from(text).toString('base64') });
  const p = r.payload as { delivered?: boolean; error?: string };
  if (!p.delivered) throw new Error(`pane_input not delivered: ${p.error ?? JSON.stringify(r)}`);
}

export async function readPaneOutput(home: string, paneId: string, lastLines = 200): Promise<string> {
  const r = await ipcRequest(home, 'read_pane_output_req', { pane_id: paneId, last_lines: lastLines });
  return (r.payload as { text?: string }).text ?? '';
}

export interface PaneListing {
  id: string;
  tab_id: string;
  name: string;
  type?: string;
}

export async function listPanes(home: string): Promise<PaneListing[]> {
  const r = await ipcRequest(home, 'list_panes_req', {});
  return (r.payload as { panes?: PaneListing[] }).panes ?? [];
}

export interface TabSnapshot {
  id: string;
  panes: string[];
  layout_rev?: number;
  layout?: { pane_id?: string; split?: number; ratio?: number; left?: unknown; right?: unknown };
}

export interface StateSnapshot {
  size_master?: string;
  tabs?: TabSnapshot[];
  panes?: { id: string; tab_id: string; cwd: string; [k: string]: unknown }[];
}

export interface FakeTUI {
  // The newest workspace_state payload received, or undefined before one.
  state(): StateSnapshot | undefined;
  // Every workspace_state received since attach, oldest first.
  states(): StateSnapshot[];
  // Sends a message on the TUI's own connection (resize_panes as master).
  send(m: Message): void;
  close(): void;
}

// fakeTUI attaches like a TUI with an 80x24 window, so it is a paintable
// client the daemon can make size master, and keeps every state.
export async function fakeTUI(home: string, clientId: string): Promise<FakeTUI> {
  const c = await FrameConn.open(home);
  const all: StateSnapshot[] = [];
  c.onMessage = (m) => {
    if (m.type === 'workspace_state') all.push(m.payload as StateSnapshot);
  };
  await hello(c, 'tui', clientId);
  const first = c.next((m) => m.type === 'workspace_state');
  c.send({ type: 'attach', payload: { cols: 80, rows: 24, win_cols: 80, win_rows: 24, client_id: clientId } });
  await first;
  return {
    state: () => all[all.length - 1],
    states: () => [...all],
    send: (m) => c.send(m),
    close: () => c.close(),
  };
}

// layoutIds lists every pane id in a serialized tree.
export function layoutIds(n: unknown): string[] {
  const node = n as { pane_id?: string; left?: unknown; right?: unknown } | undefined;
  if (!node) return [];
  if (node.pane_id) return [node.pane_id];
  return [...layoutIds(node.left), ...layoutIds(node.right)];
}

export function tabOf(s: StateSnapshot | undefined, tabId: string): TabSnapshot | undefined {
  return s?.tabs?.find((t) => t.id === tabId);
}

// paste pastes text into a pane through the page's own paste flow.
export async function paste(page: Page, paneId: string, text: string): Promise<void> {
  await page.evaluate(([id, t]) => (window as unknown as TestHookWindow).__quilTest?.paste(id, t), [paneId, text] as [string, string]);
}

// activePane is the pane the page treats as active.
export function activePane(page: Page): Promise<string> {
  return page.evaluate(() => (window as unknown as TestHookWindow).__quilTest?.activePane() ?? '');
}

// keymapLoaded waits until the page dispatches keys with the named preset:
// /api/client loads after the attach, so a key pressed right after login
// can reach the page before its keymap does.
export async function keymapLoaded(page: Page, preset: string): Promise<void> {
  await expect
    .poll(() => page.evaluate(() => (window as unknown as TestHookWindow).__quilTest?.keymapPreset() ?? ''))
    .toBe(preset);
}

// paneMenu opens a pane's menu by the pane's title text.
export async function paneMenu(page: Page, title: string): Promise<void> {
  await page.locator('.pane', { has: page.locator('.title', { hasText: title }) }).getByRole('button', { name: 'Pane menu' }).click();
}

// stopDaemon stops the daemon behind quil web; quil web keeps running.
export function stopDaemon(home: string): void {
  spawnSync(QUIL, ['daemon', 'stop'], { cwd: ROOT, env: { ...process.env, QUIL_HOME: home }, timeout: 15_000 });
}

interface TestHookWindow {
  __quilTest?: {
    bufferText(paneId: string): string;
    screenLine(paneId: string, row: number): string;
    clientId(): string;
    paste(paneId: string, text: string): void;
    activePane(): string;
    keymapPreset(): string;
  };
  __quilCSP?: (v: string) => void;
}

// bufferText is the page's terminal buffer for the pane, through the hook a
// VITE_QUIL_E2E=1 build registers.
export function bufferText(page: Page, paneId: string): Promise<string> {
  return page.evaluate((id) => (window as unknown as TestHookWindow).__quilTest?.bufferText(id) ?? '', paneId);
}

// screenLine is one row of the pane's screen in the page (0 is the top).
export function screenLine(page: Page, paneId: string, row: number): Promise<string> {
  return page.evaluate(
    ([id, r]) => (window as unknown as TestHookWindow).__quilTest?.screenLine(id, r) ?? '',
    [paneId, row] as [string, number],
  );
}

export async function clientId(page: Page): Promise<string> {
  const read = (): Promise<string> =>
    page.evaluate(() => (window as unknown as TestHookWindow).__quilTest?.clientId() ?? '');
  await expect.poll(read).not.toBe('');
  return read();
}

// login opens the page, types the code, and waits for the workspace to show
// a pane. It returns the client id the gateway leased to the page.
export async function login(page: Page, q: QuilWeb): Promise<string> {
  await page.goto(q.url);
  await page.locator('#code').fill(q.code);
  await page.getByRole('button', { name: 'Log in' }).click();
  await expect(page.locator('.pane').first()).toBeVisible();
  return clientId(page);
}

// tabButton is the tab bar's button for a tab name (the sidebar has one too).
export function tabButton(page: Page, name: string) {
  return page.locator('header').getByRole('button', { name, exact: true });
}

// cspPage is the page fixture of every test: it fails the test on any
// Content Security Policy violation the page reports.
async function cspPage({ page }: { page: Page }, use: (p: Page) => Promise<void>): Promise<void> {
  const violations: string[] = [];
  await page.exposeFunction('__quilCSP', (v: string) => {
    violations.push(v);
  });
  await page.addInitScript(() => {
    document.addEventListener('securitypolicyviolation', (e) => {
      (window as unknown as TestHookWindow).__quilCSP?.(`${e.violatedDirective} ${e.blockedURI}`);
    });
  });
  page.on('console', (m) => {
    if (/Content Security Policy/i.test(m.text())) violations.push(m.text());
  });
  await use(page);
  expect(violations, 'CSP violations').toEqual([]);
}

// withQuil is a test whose quil web starts with plugins in its home.
function withQuil(opts: QuilWebOpts = {}) {
  return base.extend<{ quil: QuilWeb }>({
    quil: async ({}, use) => {
      const q = await startQuilWeb(opts);
      try {
        await use(q);
      } finally {
        await q.stop();
      }
    },
    page: cspPage,
  });
}

// test gives every test its own quil web and daemon, and fails a test on any
// Content Security Policy violation the page reports.
export const test = withQuil();

// testWithPlugins is test with these plugin files (name → TOML) in place
// before quil web and its daemon start.
export function testWithPlugins(plugins: Record<string, string>) {
  return withQuil({ plugins });
}

// testWith is test with the quil web options above.
export function testWith(opts: QuilWebOpts) {
  return withQuil(opts);
}

export { expect };
