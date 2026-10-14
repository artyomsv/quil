import { describe, expect, it } from 'vitest';
import { filesApi, NEEDS_FULL } from './files';
import type { FetchLike } from './login';

function fake(status: number, body: unknown, seen: { url?: string; init?: RequestInit } = {}): FetchLike {
  return async (url, init) => {
    seen.url = url;
    seen.init = init;
    return {
      status,
      json: async () => {
        if (body === undefined) throw new Error('no body');
        return body;
      },
    };
  };
}

describe('filesApi', () => {
  it('sends the key and a JSON PATCH', async () => {
    const seen: { url?: string; init?: RequestInit } = {};
    const r = await filesApi(fake(200, { rows: [], keys: {} }, seen), 'k1').setSetting('ui.scrollback_lines', 5);
    expect(r.ok).toBe(true);
    expect(seen.url).toBe('/api/settings');
    expect(seen.init?.method).toBe('PATCH');
    expect((seen.init?.headers as Record<string, string>)['X-Quil-Key']).toBe('k1');
    expect(JSON.parse(String(seen.init?.body))).toEqual({ key: 'ui.scrollback_lines', value: 5 });
  });

  it('tells needs-full-rights from other 403s', async () => {
    const a = await filesApi(fake(403, { error: NEEDS_FULL }), 'k').settings();
    const b = await filesApi(fake(403, undefined), 'k').settings();
    expect(!a.ok && a.error).toBe(NEEDS_FULL);
    expect(!b.ok && b.error).toBe('Refused by the quil web server');
  });

  it('carries the current file on a 409', async () => {
    const r = await filesApi(fake(409, { error: 'changed on disk', current: { text: 't', hash: 'h', exists: true } }), 'k').writeFile(
      { kind: 'templates' },
      'x',
      'h0',
      false,
    );
    expect(r.ok).toBe(false);
    if (!r.ok) {
      expect(r.status).toBe(409);
      expect(r.current?.hash).toBe('h');
    }
  });

  it('words 413 by direction and names the plugin URL safely', async () => {
    const seen: { url?: string } = {};
    const open = await filesApi(fake(413, { error: 'x' }, seen), 'k').readFile({ kind: 'plugin', name: 'ssh' });
    expect(seen.url).toBe('/api/files/plugins/ssh');
    expect(!open.ok && open.error).toBe('Too large to edit in the browser; use the TUI.');
    const save = await filesApi(fake(413, { error: 'too large' }), 'k').writeFile({ kind: 'plugin', name: 'ssh' }, 'x', 'h', false);
    expect(!save.ok && save.error).toBe('Too large (max 256 KiB).');
  });

  it('reports a network failure', async () => {
    const r = await filesApi(async () => {
      throw new Error('down');
    }, 'k').log('web');
    expect(!r.ok && r.status).toBe(0);
  });
});
