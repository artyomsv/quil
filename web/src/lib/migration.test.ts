import { describe, expect, it } from 'vitest';
import type { Api, StaleFile } from './files';
import { CHANGED_ELSEWHERE, MigrationFlow, type MigrationIO } from './migration';

const sf = (name: string): StaleFile => ({ name, mine: `mine-${name}`, hash: `h-${name}`, default: `def-${name}`, need_version: 2 });

function harness(files: StaleFile[]) {
  const calls: { name: string; choice: string; text: string; baseHash: string }[] = [];
  let answer: Api<{ files: StaleFile[] }> = { ok: true, value: { files: [] } };
  let hold: Promise<void> = Promise.resolve();
  const io: MigrationIO = {
    resolve: async (name, choice, text, baseHash) => {
      calls.push({ name, choice, text, baseHash });
      await hold;
      return answer;
    },
  };
  return {
    f: new MigrationFlow(files, io, false),
    calls,
    set: (a: Api<{ files: StaleFile[] }>) => (answer = a),
    // holdResolves parks every resolve until the returned release is called.
    holdResolves: (): (() => void) => {
      let release = (): void => {};
      hold = new Promise<void>((r) => (release = r));
      return release;
    },
  };
}

const conflict = (text: string, hash: string): Api<{ files: StaleFile[] }> => ({
  ok: false,
  status: 409,
  error: 'changed on disk',
  current: { text, hash, exists: true },
});

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

  it('keeps an edited draft across next and previous', () => {
    const { f } = harness([sf('a'), sf('b')]);
    expect(f.dirty).toBe(false);
    f.edit('draft-a');
    f.next();
    expect(f.mine).toBe('mine-b');
    expect(f.dirty).toBe(true);
    f.prev();
    expect(f.mine).toBe('draft-a');
  });

  it('saves mine with the file hash, then the server list drives what is left', async () => {
    const h = harness([sf('a'), sf('b')]);
    h.f.edit('[plugin]\nschema_version = 2\n');
    h.set({ ok: true, value: { files: [sf('b')] } });
    await h.f.saveMine();
    expect(h.calls[0]).toEqual({ name: 'a', choice: 'mine', text: '[plugin]\nschema_version = 2\n', baseHash: 'h-a' });
    expect(h.f.current()?.name).toBe('b');
    expect(h.f.dirty).toBe(false);
    expect(h.f.done()).toBe(false);
    h.set({ ok: true, value: { files: [] } });
    await h.f.useDefault();
    expect(h.calls[1]).toEqual({ name: 'b', choice: 'default', text: '', baseHash: 'h-b' });
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

  it('reloads a file changed elsewhere, keeps the draft, and saves with the new hash', async () => {
    const h = harness([sf('a')]);
    h.f.edit('draft');
    h.set(conflict('theirs', 'h9'));
    await h.f.saveMine();
    expect(h.f.error).toBe(CHANGED_ELSEWHERE);
    expect(h.f.current()).toMatchObject({ mine: 'theirs', hash: 'h9' });
    expect(h.f.mine).toBe('draft');
    h.set({ ok: true, value: { files: [] } });
    await h.f.saveMine();
    expect(h.calls[1]).toEqual({ name: 'a', choice: 'mine', text: 'draft', baseHash: 'h9' });
  });

  it("a draft keeps the hash it started from across another file's save", async () => {
    const h = harness([sf('a'), sf('b')]);
    h.f.next();
    h.f.edit('draft-b');
    h.f.prev();
    // a is resolved; b was saved elsewhere meanwhile, so the fresh list carries its new hash.
    h.set({ ok: true, value: { files: [{ ...sf('b'), mine: 'theirs-b', hash: 'h-b2' }] } });
    await h.f.useDefault();
    expect(h.f.current()?.name).toBe('b');
    expect(h.f.mine).toBe('draft-b');
    h.set(conflict('theirs-b', 'h-b2'));
    await h.f.saveMine();
    expect(h.calls[1]?.baseHash).toBe('h-b'); // the hash the draft started from, never the refreshed one
    expect(h.f.error).toBe(CHANGED_ELSEWHERE);
    h.set({ ok: true, value: { files: [] } });
    await h.f.saveMine();
    expect(h.calls[2]).toEqual({ name: 'b', choice: 'mine', text: 'draft-b', baseHash: 'h-b2' });
  });

  it('keeps a draft whose file another client updated first, as an orphan, until discarded', async () => {
    const h = harness([sf('a'), sf('b')]);
    h.f.next();
    h.f.edit('draft-b');
    h.f.prev();
    h.set({ ok: true, value: { files: [] } }); // b is no longer stale: someone else updated it
    await h.f.useDefault();
    expect(h.f.done()).toBe(true);
    expect(h.f.dirty).toBe(true);
    expect(h.f.orphans).toEqual([{ name: 'b', text: 'draft-b' }]);
  });

  it('keeps the orphan while another file still waits', async () => {
    const h = harness([sf('a'), sf('b'), sf('c')]);
    h.f.next();
    h.f.edit('draft-b');
    h.f.prev();
    h.set({ ok: true, value: { files: [sf('c')] } }); // b updated elsewhere; c still stale
    await h.f.useDefault();
    expect(h.f.done()).toBe(false);
    expect(h.f.current()?.name).toBe('c');
    expect(h.f.orphans).toEqual([{ name: 'b', text: 'draft-b' }]);
    expect(h.f.dirty).toBe(true);
  });

  it('refuses to move while a save is in flight, so a 409 lands on the file it was sent for', async () => {
    const h = harness([sf('a'), sf('b')]);
    h.f.edit('draft-a');
    const release = h.holdResolves();
    h.set(conflict('theirs-a', 'h-a2'));
    const saving = h.f.saveMine();
    h.f.next();
    expect(h.f.current()?.name).toBe('a');
    release();
    await saving;
    expect(h.f.current()?.name).toBe('a');
    expect(h.f.error).toBe(CHANGED_ELSEWHERE);
    expect(h.f.mine).toBe('draft-a');
    h.f.next(); // allowed again once the answer is in
    expect(h.f.current()?.name).toBe('b');
  });

  it('shows the reloaded text when there was no draft', async () => {
    const h = harness([sf('a')]);
    h.set(conflict('theirs', 'h9'));
    await h.f.useDefault();
    expect(h.f.mine).toBe('theirs');
    expect(h.f.dirty).toBe(false);
  });

  it('sends nothing while read-only or with the link down', async () => {
    const h = harness([sf('a')]);
    h.f.linkLost();
    await h.f.useDefault();
    expect(h.calls).toEqual([]);
  });
});
