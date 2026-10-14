import { API_UNREACHABLE, KEY_HEADER } from './client';
import type { FetchLike } from './login';

// The 5d routes (internal/webgw/files_api.go). Every call carries the
// port-scoped login key; nothing here is cached.

export const NEEDS_FULL = 'needs full rights';
export const FILE_MAX_BYTES = 256 * 1024;

export interface SettingRow {
  key: string;
  group: 'general' | 'notifications' | 'sandbox';
  label: string;
  kind: 'bool' | 'int' | 'float' | 'duration' | 'enum' | 'string';
  choices?: string[];
  applies: string;
  value: boolean | number | string;
}

export interface KeysView {
  preset: string;
  prefix: string;
  presets: string[];
  prefix_used: boolean;
  preset_prefix: string;
  error?: string;
}

export interface SettingsView {
  rows: SettingRow[];
  keys: KeysView;
}

export interface TextFile {
  text: string;
  hash: string;
  exists: boolean;
  default?: string;
}

// StaleFile: hash is the gateway's hash of the file's raw bytes, echoed as
// base_hash on resolve, so a save elsewhere while the view is open answers
// 409 with the current text instead of being overwritten.
export interface StaleFile {
  name: string;
  mine: string;
  hash: string;
  default: string;
  need_version: number;
}

export interface LogText {
  text: string;
  truncated: boolean;
  size: number;
  mtime: string;
  missing: boolean;
}

export type FileTarget = { kind: 'templates' } | { kind: 'plugin'; name: string };
export type LogName = 'client' | 'daemon' | 'mcp' | 'web';

export type Api<T> = { ok: true; value: T } | { ok: false; status: number; error: string; current?: TextFile };

function isTextFile(v: unknown): v is TextFile {
  return typeof v === 'object' && v !== null && typeof (v as TextFile).text === 'string' && typeof (v as TextFile).hash === 'string';
}

// apiWords is what the page shows for a refusal. 409 and 422 carry the
// server's own fixed text; a 403 says "needs full rights" only when the body
// says exactly that (apiAuth's own 403s have no body, spec 5d §4.0).
export function apiWords(status: number, text: string, method: string): string {
  switch (status) {
    case 0:
      return API_UNREACHABLE;
    case 401:
      return 'Log in again';
    case 403:
      return text === NEEDS_FULL ? NEEDS_FULL : 'Refused by the quil web server';
    case 404:
      return 'Not found';
    case 413:
      return method === 'GET' ? 'Too large to edit in the browser; use the TUI.' : 'Too large (max 256 KiB).';
    case 409:
    case 422:
      return text || `The quil web server answered HTTP ${status}`;
    case 500:
      return 'Failed — see web.log';
    default:
      return `The quil web server answered HTTP ${status}`;
  }
}

async function api<T>(fetchFn: FetchLike, key: string, method: string, url: string, body?: unknown): Promise<Api<T>> {
  const headers: Record<string, string> = { [KEY_HEADER]: key };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  let r;
  try {
    r = await fetchFn(url, { method, credentials: 'same-origin', headers, body: body === undefined ? undefined : JSON.stringify(body) });
  } catch {
    return { ok: false, status: 0, error: API_UNREACHABLE };
  }
  let j: unknown = null;
  try {
    j = await r.json();
  } catch {
    j = null;
  }
  if (r.status === 200) {
    return j === null || typeof j !== 'object'
      ? { ok: false, status: 200, error: 'The quil web server sent an answer this page does not understand' }
      : { ok: true, value: j as T };
  }
  const o = (typeof j === 'object' && j !== null ? j : {}) as { error?: unknown; current?: unknown };
  const text = typeof o.error === 'string' ? o.error : '';
  return { ok: false, status: r.status, error: apiWords(r.status, text, method), current: isTextFile(o.current) ? o.current : undefined };
}

function fileUrl(t: FileTarget): string {
  return t.kind === 'templates' ? '/api/files/templates' : `/api/files/plugins/${encodeURIComponent(t.name)}`;
}

export interface FilesApi {
  settings(): Promise<Api<SettingsView>>;
  setSetting(key: string, value: boolean | number | string): Promise<Api<SettingsView>>;
  readFile(t: FileTarget): Promise<Api<TextFile>>;
  writeFile(t: FileTarget, text: string, baseHash: string, force: boolean): Promise<Api<TextFile>>;
  migration(): Promise<Api<{ files: StaleFile[] }>>;
  resolve(name: string, choice: 'default' | 'mine', text: string, baseHash: string): Promise<Api<{ files: StaleFile[] }>>;
  log(name: LogName): Promise<Api<LogText>>;
}

export function filesApi(fetchFn: FetchLike, key: string): FilesApi {
  return {
    settings: () => api(fetchFn, key, 'GET', '/api/settings'),
    setSetting: (k, value) => api(fetchFn, key, 'PATCH', '/api/settings', { key: k, value }),
    readFile: (t) => api(fetchFn, key, 'GET', fileUrl(t)),
    writeFile: (t, text, baseHash, force) => api(fetchFn, key, 'PUT', fileUrl(t), { text, base_hash: baseHash, force }),
    migration: () => api(fetchFn, key, 'GET', '/api/migration'),
    resolve: (name, choice, text, baseHash) =>
      api(fetchFn, key, 'POST', `/api/migration/${encodeURIComponent(name)}`, { choice, text, base_hash: baseHash }),
    log: (name) => api(fetchFn, key, 'GET', `/api/logs/${name}`),
  };
}
