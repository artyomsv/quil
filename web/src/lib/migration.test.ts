import { describe, expect, it } from 'vitest';
import type { Api, StaleFile } from './files';
import { MigrationFlow, type MigrationIO } from './migration';

const sf = (name: string): StaleFile => ({ name, mine: `mine-${name}`, default: `def-${name}`, need_version: 2 });

function harness(files: StaleFile[]) {
  const calls: { name: string; choice: string; text: string }[] = [];
  let answer: Api<{ files: StaleFile[] }> = { ok: true, value: { files: [] } };
  const io: MigrationIO = {
    resolve: async (name, choice, text) => {
      calls.push({ name, choice, text });
      return answer;
    },
  };
  return { f: new MigrationFlow(files, io, false), calls, set: (a: Api<{ files: StaleFile[] }>) => (answer = a) };
}

describe('MigrationFlow', () => {
  it('moves next and previous, editing the left side only', () => {
    const { f } = harness([sf('a'), sf('b')]);
    expect(f.current()?.name).toBe('a');
    f.edit('changed');
    expect(f.mine).toBe('changed');
    f.next();
    expect(f.current()?.name).toBe('b');
    expect(f.mine).toBe('mine-b');
    f.prev();
    expect(f.current()?.name).toBe('a');
  });

  it('saves mine, then the server list drives what is left', async () => {
    const h = harness([sf('a'), sf('b')]);
    h.f.edit('[plugin]\nschema_version = 2\n');
    h.set({ ok: true, value: { files: [sf('b')] } });
    await h.f.saveMine();
    expect(h.calls[0]).toEqual({ name: 'a', choice: 'mine', text: '[plugin]\nschema_version = 2\n' });
    expect(h.f.current()?.name).toBe('b');
    expect(h.f.done()).toBe(false);
    h.set({ ok: true, value: { files: [] } });
    await h.f.useDefault();
    expect(h.calls[1]).toEqual({ name: 'b', choice: 'default', text: '' });
    expect(h.f.done()).toBe(true);
  });

  it('shows a refusal and keeps the text', async () => {
    const h = harness([sf('a')]);
    h.f.edit('low');
    h.set({ ok: false, status: 422, error: 'schema_version must be >= 2 (currently 0)' });
    await h.f.saveMine();
    expect(h.f.error).toBe('schema_version must be >= 2 (currently 0)');
    expect(h.f.mine).toBe('low');
    expect(h.f.done()).toBe(false);
  });

  it('sends nothing while read-only or with the link down', async () => {
    const h = harness([sf('a')]);
    h.f.linkLost();
    await h.f.useDefault();
    expect(h.calls).toEqual([]);
  });
});
