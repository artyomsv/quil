import { describe, expect, it } from 'vitest';
import { createInstance, deleteInstance, displayAddr, loadClient, updateInstance } from './client';
import type { FetchLike } from './login';

function fake(status: number, body?: unknown) {
  const calls: { url: string; init: RequestInit }[] = [];
  const fetchFn: FetchLike = async (url, init) => {
    calls.push({ url, init });
    return { status, json: async () => body };
  };
  return { fetchFn, calls };
}

const INFO = {
  rights: 'full',
  plugins: [],
  categories: [],
  instances: {},
  sandbox: { sign_in_default: 'browser', image_default: '' },
};

describe('client api', () => {
  it('GET /api/client carries the key header and no body', async () => {
    const { fetchFn, calls } = fake(200, INFO);
    const r = await loadClient(fetchFn, 'k1');
    expect('info' in r && r.info.rights).toBe('full');
    expect(calls[0]?.url).toBe('/api/client');
    expect(calls[0]?.init.method).toBe('GET');
    expect(calls[0]?.init.body).toBeUndefined();
    expect((calls[0]?.init.headers as Record<string, string>)['X-Quil-Key']).toBe('k1');
    expect(calls[0]?.init.credentials).toBe('same-origin');
  });

  it('fills what a server omits and refuses a shapeless answer', async () => {
    const r = await loadClient(fake(200, { plugins: [], categories: [] }).fetchFn, 'k');
    expect(r).toEqual({
      info: {
        rights: '',
        plugins: [],
        categories: [],
        instances: {},
        sandbox: { sign_in_default: 'browser', image_default: '' },
        keymap: undefined,
        notifications: undefined,
      },
    });
    expect('error' in (await loadClient(fake(200, null).fetchFn, 'k'))).toBe(true);
    const thrown: FetchLike = async () => {
      throw new Error('down');
    };
    expect(await loadClient(thrown, 'k')).toEqual({ error: 'Could not reach the quil web server' });
  });

  it('maps refusals to words', async () => {
    expect(await loadClient(fake(401).fetchFn, 'k')).toEqual({ error: 'Log in again to load the dialog' });
    expect(await createInstance(fake(403).fetchFn, 'k', { plugin: 'ssh', name: 'x', fields: {} })).toEqual({
      error: 'This session may not save instances',
    });
    expect(await createInstance(fake(409, { error: 'x' }).fetchFn, 'k', { plugin: 'ssh', name: 'x', fields: {} })).toEqual({
      error: 'instances.json does not parse; fix it in the TUI first',
    });
    expect(await updateInstance(fake(404).fetchFn, 'k', { plugin: 'ssh', id: 'i', name: 'x', fields: {} })).toEqual({
      error: 'That instance no longer exists',
    });
  });

  it('writes JSON with the key; delete uses the query', async () => {
    const c = fake(201, { id: 'ab12cd34', name: 'x', fields: {} });
    const made = await createInstance(c.fetchFn, 'k', { plugin: 'ssh', name: 'x', fields: { host: 'h' } });
    expect(made.instance?.id).toBe('ab12cd34');
    expect(c.calls[0]?.init.method).toBe('POST');
    expect(c.calls[0]?.url).toBe('/api/instances');
    expect((c.calls[0]?.init.headers as Record<string, string>)['Content-Type']).toBe('application/json');
    expect(JSON.parse(c.calls[0]?.init.body as string)).toEqual({ plugin: 'ssh', name: 'x', fields: { host: 'h' } });
    const u = fake(200, { id: 'i', name: 'y', fields: {} });
    await updateInstance(u.fetchFn, 'k', { plugin: 'ssh', id: 'i', name: 'y', fields: {} });
    expect(u.calls[0]?.init.method).toBe('PUT');
    const d = fake(204);
    expect(await deleteInstance(d.fetchFn, 'k', 'ssh', 'i/x')).toEqual({});
    expect(d.calls[0]?.url).toBe('/api/instances?plugin=ssh&id=i%2Fx');
    expect(d.calls[0]?.init.method).toBe('DELETE');
  });

  it('displayAddr matches the TUI', () => {
    expect(displayAddr({ id: '1', name: 'n', fields: { user: 'u', host: 'h', port: '22' } })).toBe('u@h');
    expect(displayAddr({ id: '1', name: 'n', fields: { host: 'h', port: '2222' } })).toBe('h:2222');
    expect(displayAddr({ id: '1', name: 'n', fields: { name: 'n', url: 'x' } })).toBe('x');
    expect(displayAddr({ id: '1', name: 'n', fields: {} })).toBe('');
  });
});
