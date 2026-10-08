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
        keymap: null,
        notifications: null,
        templates: [],
        templates_error: '',
        connect: false,
      },
    });
    expect('error' in (await loadClient(fake(200, null).fetchFn, 'k'))).toBe(true);
    const thrown: FetchLike = async () => {
      throw new Error('down');
    };
    expect(await loadClient(thrown, 'k')).toEqual({ error: 'Could not reach the quil web server' });
  });

  it('reads the keymap and the notification tables, dropping what it cannot use', async () => {
    const body = {
      plugins: [],
      categories: [],
      keymap: {
        preset: 'tmux',
        prefix: 'ctrl+b',
        timeout_ms: 0,
        group_order: ['Panes'],
        actions: [{ id: 'pane.split_h', label: 'Split', group: 'Panes', tier: 'late', keys: ['ctrl+b %', 7] }, { label: 'no id' }],
        builtins: [{ id: 'help', label: 'Key list', keys: ['f1'] }],
        conflicts: [],
      },
      notifications: {
        shown: { agent_turn: true, commands: 'no' },
        hook_groups: { Stop: 'agent_turn' },
        plain_groups: { bell: 'agent_blocked' },
        default_group: 'system',
        work_state_only: ['hook.claude.PostToolUse'],
      },
    };
    const r = await loadClient(fake(200, body).fetchFn, 'k');
    if (!('info' in r)) throw new Error('refused');
    expect(r.info.keymap?.actions).toEqual([
      { id: 'pane.split_h', label: 'Split', group: 'Panes', tier: 'late', keys: ['ctrl+b %'], fallback: undefined, fallback_unavailable: undefined },
    ]);
    expect(r.info.keymap?.builtins[0]?.keys).toEqual(['f1']);
    expect(r.info.notifications?.shown).toEqual({ agent_turn: true });
    expect(r.info.notifications?.work_state_only).toEqual(['hook.claude.PostToolUse']);
    const bad = await loadClient(fake(200, { plugins: [], categories: [], keymap: { actions: 'x' }, notifications: 3 }).fetchFn, 'k');
    expect('info' in bad && bad.info.keymap).toBeNull();
    expect('info' in bad && bad.info.notifications).toBeNull();
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

  it('reads templates, templates_error, connect and record_history', async () => {
    const body = {
      rights: 'full',
      plugins: [{ name: 'claude-code', display_name: 'Claude', category: 'ai', record_history: true }],
      categories: [],
      templates: [{ name: 'pair', description: 'two agents' }, { description: 'nameless' }],
      templates_error: '',
      connect: true,
    };
    const r = await loadClient(fake(200, body).fetchFn, 'k');
    if (!('info' in r)) throw new Error(r.error);
    expect(r.info.templates).toEqual([{ name: 'pair', description: 'two agents' }]);
    expect(r.info.connect).toBe(true);
    expect(r.info.plugins[0]?.record_history).toBe(true);
  });

  it('carries a templates error', async () => {
    const r = await loadClient(fake(200, { plugins: [], categories: [], templates: [], templates_error: 'bad' }).fetchFn, 'k');
    if (!('info' in r)) throw new Error(r.error);
    expect(r.info.templates_error).toBe('bad');
  });

  it('displayAddr matches the TUI', () => {
    expect(displayAddr({ id: '1', name: 'n', fields: { user: 'u', host: 'h', port: '22' } })).toBe('u@h');
    expect(displayAddr({ id: '1', name: 'n', fields: { host: 'h', port: '2222' } })).toBe('h:2222');
    expect(displayAddr({ id: '1', name: 'n', fields: { name: 'n', url: 'x' } })).toBe('x');
    expect(displayAddr({ id: '1', name: 'n', fields: {} })).toBe('');
  });
});
