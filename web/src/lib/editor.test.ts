import { describe, expect, it } from 'vitest';
import { EditorSession, type EditorIO } from './editor';
import type { Api, TextFile } from './files';

function harness(viewOnly = false) {
  const writes: { text: string; base: string; force: boolean }[] = [];
  let readAnswer: Api<TextFile> = { ok: true, value: { text: 'a', hash: 'h1', exists: true } };
  let writeAnswer: Api<TextFile> = { ok: true, value: { text: 'b', hash: 'h2', exists: true } };
  let hold: Promise<void> = Promise.resolve();
  const io: EditorIO = {
    read: async () => readAnswer,
    write: async (text, base, force) => {
      writes.push({ text, base, force });
      await hold;
      return writeAnswer;
    },
  };
  const e = new EditorSession({ kind: 'templates' }, 'Templates', io, viewOnly);
  return {
    e,
    writes,
    setRead: (a: Api<TextFile>) => (readAnswer = a),
    setWrite: (a: Api<TextFile>) => (writeAnswer = a),
    // holdWrites parks every write until the returned release is called.
    holdWrites: (): (() => void) => {
      let release = (): void => {};
      hold = new Promise<void>((r) => (release = r));
      return release;
    },
  };
}

describe('EditorSession', () => {
  it('loads clean, edits dirty, saves clean with the loaded hash', async () => {
    const { e, writes } = harness();
    await e.load();
    expect(e.loading).toBe(false);
    expect(e.dirty).toBe(false);
    e.edit('b');
    expect(e.dirty).toBe(true);
    await e.save();
    expect(writes).toEqual([{ text: 'b', base: 'h1', force: false }]);
    expect(e.dirty).toBe(false);
    expect(e.hash).toBe('h2');
  });

  it('keeps text typed during a save, unsaved, under the new hash', async () => {
    const h = harness();
    await h.e.load();
    h.e.edit('b');
    const release = h.holdWrites();
    const saving = h.e.save();
    h.e.edit('bc'); // typed while the save is in flight
    release();
    await saving;
    expect(h.writes).toEqual([{ text: 'b', base: 'h1', force: false }]);
    expect(h.e.hash).toBe('h2');
    expect(h.e.text).toBe('bc');
    expect(h.e.dirty).toBe(true);
    await h.e.save();
    expect(h.writes.at(-1)).toEqual({ text: 'bc', base: 'h2', force: false });
  });

  it('shows a conflict, then Overwrite forces and Reload takes theirs', async () => {
    const h = harness();
    await h.e.load();
    h.e.edit('mine');
    h.setWrite({ ok: false, status: 409, error: 'changed on disk', current: { text: 'theirs', hash: 'h9', exists: true } });
    await h.e.save();
    expect(h.e.conflict?.hash).toBe('h9');
    expect(h.e.dirty).toBe(true);
    h.setWrite({ ok: true, value: { text: 'mine', hash: 'h10', exists: true } });
    await h.e.save(true);
    expect(h.writes.at(-1)).toEqual({ text: 'mine', base: 'h9', force: true });
    expect(h.e.conflict).toBeNull();

    h.e.edit('again');
    h.setWrite({ ok: false, status: 409, error: 'changed on disk', current: { text: 'theirs2', hash: 'h11', exists: true } });
    await h.e.save();
    h.e.takeTheirs();
    expect(h.e.text).toBe('theirs2');
    expect(h.e.dirty).toBe(false);
  });

  it('keeps the text and refuses edits while the link is down', async () => {
    const { e } = harness();
    await e.load();
    e.edit('kept');
    e.linkLost();
    e.edit('lost');
    expect(e.text).toBe('kept');
    expect(e.readOnly()).toBe(true);
    e.linkBack();
    expect(e.readOnly()).toBe(false);
  });

  it('asks before closing with unsaved text', async () => {
    const { e } = harness();
    await e.load();
    expect(e.close()).toBe('closed');
    e.edit('x');
    expect(e.close()).toBe('ask');
  });

  it('is read-only without full rights and never writes', async () => {
    const { e, writes } = harness(true);
    await e.load();
    e.edit('x');
    await e.save();
    expect(e.text).toBe('a');
    expect(writes).toEqual([]);
  });

  it('refuses a text over 256 KiB before sending', async () => {
    const { e, writes } = harness();
    await e.load();
    e.edit('x'.repeat(256 * 1024 + 1));
    await e.save();
    expect(writes).toEqual([]);
    expect(e.saveError).toBe('Too large (max 256 KiB).');
  });

  it('shows a load error and stays closed to edits', async () => {
    const h = harness();
    h.setRead({ ok: false, status: 413, error: 'Too large to edit in the browser; use the TUI.' });
    await h.e.load();
    expect(h.e.loadError).toBe('Too large to edit in the browser; use the TUI.');
    h.e.edit('x');
    expect(h.e.dirty).toBe(false);
  });
});
