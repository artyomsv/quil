import { asWebKeymap, type WebKeymap } from './keys/engine';
import type { FetchLike } from './login';
import { asNotifyInfo, type NotifyInfo } from './notifications';

// GET /api/client and the saved-instance writes (internal/webgw/api.go).
// Every call carries the port-scoped login key in X-Quil-Key; the cookie
// alone opens nothing (a page on another 127.0.0.1 port is sent it too).

export interface FieldDef {
  name: string;
  label: string;
  required?: boolean;
  default?: string;
}

export interface ToggleDef {
  name: string;
  label: string;
  default?: boolean;
  group?: string;
}

// PluginDef is what the gateway tells the page about a plugin: enough to
// draw the dialog, never its command or arguments.
export interface PluginDef {
  name: string;
  display_name: string;
  category: string;
  description?: string;
  homepage?: string;
  prompts_cwd?: boolean;
  discover?: string;
  sessions?: string;
  form_fields?: FieldDef[];
  toggles?: ToggleDef[];
  raw_keys?: string[];
  uses_claude_auth?: boolean;
  // The plugin records input history (Command.RecordHistory).
  record_history?: boolean;
}

// TemplateDef is a workspace template as the page lists it; the daemon reads
// its content itself.
export interface TemplateDef {
  name: string;
  description?: string;
}

export interface SavedInstance {
  id: string;
  name: string;
  fields: Record<string, string>;
  description?: string;
}

export interface ClientInfo {
  // The lowest rights any open tab of this login holds from the daemon; ''
  // when none is open.
  rights: string;
  plugins: PluginDef[];
  categories: { key: string; label: string }[];
  instances: Record<string, SavedInstance[]>;
  sandbox: { sign_in_default: 'browser' | 'shared' | 'token' | string; image_default: string };
  // The keymap as the browser dispatches it, and the notification tables;
  // null when the server sent none.
  keymap: WebKeymap | null;
  notifications: NotifyInfo | null;
  // The templates the TUI's dialog offers, read on the gateway machine, and
  // why that file did not read ('' = it did).
  templates: TemplateDef[];
  templates_error: string;
  // The gateway dials a remote daemon (quil web --connect).
  connect: boolean;
}

export interface InstanceInput {
  plugin: string;
  id?: string;
  name: string;
  fields: Record<string, string>;
  description?: string;
}

export const KEY_HEADER = 'X-Quil-Key';
export const API_UNREACHABLE = 'Could not reach the quil web server';

function words(status: number, write: boolean): string {
  switch (status) {
    case 401:
      return 'Log in again to load the dialog';
    case 403:
      return write ? 'This session may not save instances' : 'Refused by the quil web server';
    case 404:
      return 'That instance no longer exists';
    case 409:
      return 'instances.json does not parse; fix it in the TUI first';
    case 400:
      return write ? 'The quil web server refused the instance (unknown field, or a value too long)' : 'Bad request';
    default:
      return `The quil web server answered HTTP ${status}`;
  }
}

function call(fetchFn: FetchLike, key: string, url: string, method: string, body?: unknown) {
  const headers: Record<string, string> = { [KEY_HEADER]: key };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  return fetchFn(url, {
    method,
    credentials: 'same-origin',
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}

// asClientInfo checks the shape the dialog relies on and fills the lists a
// server may omit, so the page never renders from a half-read answer.
function asClientInfo(v: unknown): ClientInfo | null {
  if (typeof v !== 'object' || v === null) return null;
  const o = v as Record<string, unknown>;
  if (!Array.isArray(o.plugins) || !Array.isArray(o.categories)) return null;
  const sandbox = (typeof o.sandbox === 'object' && o.sandbox !== null ? o.sandbox : {}) as Record<string, unknown>;
  return {
    rights: typeof o.rights === 'string' ? o.rights : '',
    plugins: o.plugins as PluginDef[],
    categories: o.categories as ClientInfo['categories'],
    instances: (typeof o.instances === 'object' && o.instances !== null ? o.instances : {}) as ClientInfo['instances'],
    sandbox: {
      sign_in_default: typeof sandbox.sign_in_default === 'string' ? sandbox.sign_in_default : 'browser',
      image_default: typeof sandbox.image_default === 'string' ? sandbox.image_default : '',
    },
    keymap: asWebKeymap(o.keymap),
    notifications: asNotifyInfo(o.notifications),
    templates: Array.isArray(o.templates)
      ? (o.templates as unknown[]).filter(
          (t): t is TemplateDef => typeof t === 'object' && t !== null && typeof (t as TemplateDef).name === 'string',
        )
      : [],
    templates_error: typeof o.templates_error === 'string' ? o.templates_error : '',
    connect: o.connect === true,
  };
}

export async function loadClient(fetchFn: FetchLike, key: string): Promise<{ info: ClientInfo } | { error: string }> {
  try {
    const r = await call(fetchFn, key, '/api/client', 'GET');
    if (r.status !== 200) return { error: words(r.status, false) };
    const info = asClientInfo(await r.json());
    return info ? { info } : { error: 'The quil web server sent an answer this page does not understand' };
  } catch {
    return { error: API_UNREACHABLE };
  }
}

export type WriteResult = { instance?: SavedInstance; error?: string };

async function write(
  fetchFn: FetchLike,
  key: string,
  method: string,
  url: string,
  body: unknown,
  okStatus: number,
): Promise<WriteResult> {
  try {
    const r = await call(fetchFn, key, url, method, body);
    if (r.status !== okStatus) return { error: words(r.status, true) };
    return okStatus === 204 ? {} : { instance: (await r.json()) as SavedInstance };
  } catch {
    return { error: API_UNREACHABLE };
  }
}

export const createInstance = (f: FetchLike, key: string, i: InstanceInput): Promise<WriteResult> =>
  write(f, key, 'POST', '/api/instances', i, 201);

export const updateInstance = (f: FetchLike, key: string, i: InstanceInput): Promise<WriteResult> =>
  write(f, key, 'PUT', '/api/instances', i, 200);

export const deleteInstance = (f: FetchLike, key: string, plugin: string, id: string): Promise<WriteResult> =>
  write(f, key, 'DELETE', `/api/instances?plugin=${encodeURIComponent(plugin)}&id=${encodeURIComponent(id)}`, undefined, 204);

// displayAddr is instances.Saved.DisplayAddr: user@host:port (port 22
// omitted), else the first other non-empty field.
export function displayAddr(si: SavedInstance): string {
  const f = si.fields ?? {};
  const host = f.host ?? '';
  if (host !== '') {
    let addr = host;
    if (f.user) addr = `${f.user}@${addr}`;
    if (f.port && f.port !== '22') addr += `:${f.port}`;
    return addr;
  }
  for (const [k, v] of Object.entries(f).sort(([a], [b]) => a.localeCompare(b))) {
    if (k !== 'name' && k !== 'description' && v !== '') return v;
  }
  return '';
}
