import { describe, expect, it } from 'vitest';
import { FakeClock } from './fakeclock';
import { MAX_NOTE_BYTES, NOTE_LOAD_TIMEOUT_MS, type NoteIO, NOTES_DEBOUNCE_MS, NoteSession, saveText } from './notes';
import type { Outcome } from './requests';

type Call = { kind: 'get' } | { kind: 'set'; text: string; base: number };

function harness(viewOnly = false) {
  const calls: Call[] = [];
  const answers: ((o: Outcome) => void)[] = [];
  const io: NoteIO = {
    get: () => {
      calls.push({ kind: 'get' });
      return new Promise((r) => answers.push(r));
    },
    set: (_p, text, base) => {
      calls.push({ kind: 'set', text, base });
      return new Promise((r) => answers.push(r));
    },
  };
  const clock = new FakeClock();
  const n = new NoteSession('p1', io, clock, viewOnly);
  const settle = async (): Promise<void> => {
    for (let i = 0; i < 3; i++) await Promise.resolve();
  };
  // answer resolves the oldest unanswered call; answerAt the i-th one (0 =
  // oldest), so a test can answer a later save before an earlier get.
  const answer = async (o: Outcome): Promise<void> => {
    answers.shift()!(o);
    await settle();
  };
  const answerAt = async (i: number, o: Outcome): Promise<void> => {
    answers.splice(i, 1)[0]!(o);
    await settle();
  };
  const sets = (): Call[] => calls.filter((c) => c.kind === 'set');
  const gets = (): Call[] => calls.filter((c) => c.kind === 'get');
  return { n, calls, clock, answer, answerAt, sets, gets };
}

const got = (text: string, rev: number): Outcome => ({
  ok: true,
  reply: { type: 'note_resp', payload: { pane_id: 'p1', text, rev } },
});
const saved = (rev: number): Outcome => ({
  ok: true,
  reply: { type: 'note_set_resp', payload: { pane_id: 'p1', ok: true, rev } },
});
const conflict = (cur: number): Outcome => ({
  ok: false,
  code: 'failed',
  error: 'not done',
  reply: { type: 'note_set_resp', payload: { pane_id: 'p1', ok: false, conflict: true, current_rev: cur } },
});
const refused = (err: string): Outcome => ({
  ok: false,
  code: 'failed',
  error: err,
  reply: { type: 'note_set_resp', payload: { pane_id: 'p1', ok: false, error: err } },
});

async function loaded(text = 'a\n', rev = 3) {
  const h = harness();
  h.n.load();
  await h.answer(got(text, rev));
  return h;
}

describe('saveText (TakeSave)', () => {
  it('adds one trailing newline', () => {
    expect(saveText('a')).toBe('a\n');
    expect(saveText('a\n')).toBe('a\n');
  });
  it('sends an emptied buffer as the delete', () => {
    expect(saveText('')).toBe('');
  });
});

describe('NoteSession load', () => {
  it('cleans the text at load, keeping line breaks', async () => {
    const esc = String.fromCharCode(0x1b);
    const rlo = String.fromCharCode(0x202e);
    const { n } = await loaded(`a${esc}[1m\nb${rlo}`, 4);
    expect(n.text).toBe('a[1m\nb');
    expect(n.rev).toBe(4);
    expect(n.loading).toBe(false);
  });
  it('shows an error after 8 s with no answer', () => {
    const { n, clock } = harness();
    n.load();
    clock.advance(NOTE_LOAD_TIMEOUT_MS);
    expect(n.loadError).toBe('no answer from the daemon');
  });
  it('shows the daemon error of a first load', async () => {
    const h = harness();
    h.n.load();
    await h.answer({ ok: true, reply: { type: 'note_resp', payload: { pane_id: 'p1', text: '', rev: 0, error: 'no such pane' } } });
    expect(h.n.loadError).toBe('no such pane');
  });
  it('is read-only for a view-only session', async () => {
    const h = harness(true);
    h.n.load();
    await h.answer(got('x\n', 1));
    h.n.edit('y');
    expect(h.n.dirty).toBe(false);
    h.n.save();
    expect(h.sets()).toEqual([]);
  });
});

describe('NoteSession save', () => {
  it('saves on demand from the loaded rev', async () => {
    const { n, calls, answer } = await loaded();
    n.edit('b');
    n.save();
    expect(calls.at(-1)).toEqual({ kind: 'set', text: 'b\n', base: 3 });
    await answer(saved(4));
    expect(n.rev).toBe(4);
    expect(n.dirty).toBe(false);
  });
  it('sends an emptied note as the delete', async () => {
    const { n, calls } = await loaded();
    n.edit('');
    n.save();
    expect(calls.at(-1)).toEqual({ kind: 'set', text: '', base: 3 });
  });
  it('autosaves 30 s after the last edit', async () => {
    const { n, calls, clock, sets } = await loaded();
    n.edit('b');
    clock.advance(NOTES_DEBOUNCE_MS - 1);
    n.edit('bc');
    clock.advance(NOTES_DEBOUNCE_MS - 1);
    expect(sets()).toEqual([]);
    clock.advance(1);
    expect(calls.at(-1)).toEqual({ kind: 'set', text: 'bc\n', base: 3 });
  });
  it('keeps edits made during a save dirty, and saves them after', async () => {
    const { n, calls, answer } = await loaded();
    n.edit('b');
    n.save();
    n.edit('bc');
    await answer(saved(4));
    expect(n.dirty).toBe(true);
    n.save();
    expect(calls.at(-1)).toEqual({ kind: 'set', text: 'bc\n', base: 4 });
  });
  it('sends one save at a time', async () => {
    const { n, sets } = await loaded();
    n.edit('b');
    n.save();
    n.save();
    expect(sets().length).toBe(1);
  });
  it('conflict on save stops autosave and keeps the text', async () => {
    const { n, clock, answer, sets } = await loaded();
    n.edit('mine');
    n.save();
    await answer(conflict(7));
    expect(n.conflict).toBe(true);
    expect(n.currentRev).toBe(7);
    expect(n.text).toBe('mine');
    n.edit('mine2');
    clock.advance(NOTES_DEBOUNCE_MS * 2);
    expect(sets().length).toBe(1);
  });
  it('keep mine saves from current_rev', async () => {
    const { n, calls, answer } = await loaded();
    n.edit('mine');
    n.save();
    await answer(conflict(7));
    n.save();
    expect(calls.at(-1)?.kind).toBe('set');
    expect(calls.filter((c) => c.kind === 'set').length).toBe(1);
    n.save(true);
    expect(calls.at(-1)).toEqual({ kind: 'set', text: 'mine\n', base: 7 });
  });
  it('a later conflict never lowers the current rev', async () => {
    const { n, answer } = await loaded();
    n.edit('mine');
    n.save();
    await answer(conflict(9));
    n.save(true);
    await answer(conflict(8));
    expect(n.currentRev).toBe(9);
  });
  it('a refused save holds autosave until the next edit', async () => {
    const { n, clock, answer, sets } = await loaded();
    n.edit('b');
    n.save();
    await answer(refused('disk full'));
    expect(n.saveError).toBe('disk full');
    expect(n.hold).toBe(true);
    clock.advance(NOTES_DEBOUNCE_MS * 2);
    expect(sets().length).toBe(1);
    n.edit('bb');
    clock.advance(NOTES_DEBOUNCE_MS);
    expect(sets().length).toBe(2);
  });
  it('a save with no verdict keeps the text dirty and does not hold', async () => {
    const { n, answer } = await loaded();
    n.edit('b');
    n.save();
    await answer({ ok: false, code: 'timeout', error: 'No answer from the daemon' });
    expect(n.dirty).toBe(true);
    expect(n.hold).toBe(false);
  });
  it('refuses to send a note above the cap that is larger than the loaded one', async () => {
    const { n, sets } = await loaded('x\n', 1);
    n.edit('y'.repeat(MAX_NOTE_BYTES + 1));
    expect(n.tooLarge()).toBe(true);
    n.save();
    expect(sets()).toEqual([]);
  });
  it('a too-large note holds and can be discarded on close', async () => {
    const { n, sets } = await loaded('x\n', 1);
    n.edit('y'.repeat(MAX_NOTE_BYTES + 1));
    expect(n.close()).toBe('wait');
    expect(n.hold).toBe(true);
    expect(n.closing).toBe(true);
    expect(n.saveError).toBe('too large');
    expect(sets()).toEqual([]);
  });
  it('measures growth against the last saved size, not the largest one', async () => {
    const big = 'z'.repeat(MAX_NOTE_BYTES + 10);
    const { n, answer } = await loaded(big, 1);
    n.edit(big.slice(0, MAX_NOTE_BYTES + 5));
    n.save();
    await answer(saved(2));
    // The stored note is now MAX+6 bytes (with its newline): growing back to
    // MAX+8 is above the cap AND larger than it — the daemon refuses that.
    n.edit(big.slice(0, MAX_NOTE_BYTES + 8));
    expect(n.tooLarge()).toBe(true);
  });
  it('allows shrinking a note that was already above the cap', async () => {
    const big = 'z'.repeat(MAX_NOTE_BYTES + 10);
    const { n, sets } = await loaded(big, 1);
    n.edit(big.slice(0, MAX_NOTE_BYTES + 5));
    expect(n.tooLarge()).toBe(false);
    n.save();
    expect(sets().length).toBe(1);
  });
});

describe('NoteSession live changes (reconcileNoteRev, applyNoteResp)', () => {
  it('ignores a frame at or below its rev', async () => {
    const { n, gets } = await loaded('a\n', 3);
    n.frameRev(3);
    n.frameRev(2);
    n.frameRev(undefined);
    expect(gets().length).toBe(1);
  });
  it('reloads silently when clean', async () => {
    const { n, calls, answer } = await loaded('a\n', 3);
    n.frameRev(5);
    expect(calls.at(-1)).toEqual({ kind: 'get' });
    await answer(got('theirs\n', 5));
    expect(n.text).toBe('theirs\n');
    expect(n.rev).toBe(5);
  });
  it('sends one get for two newer frames in a row', async () => {
    const { n, gets } = await loaded('a\n', 3);
    n.frameRev(5);
    n.frameRev(6);
    expect(gets().length).toBe(2);
  });
  it('reads again when the pending read answers older than a later frame', async () => {
    const { n, gets, answer } = await loaded('a\n', 3);
    n.frameRev(5);
    n.frameRev(6);
    expect(gets().length).toBe(2);
    await answer(got('five\n', 5));
    // rev 6 was named while the read was out: it is read now.
    expect(gets().length).toBe(3);
    await answer(got('six\n', 6));
    expect(n.text).toBe('six\n');
    expect(n.rev).toBe(6);
  });
  it('reads a frame that came during its own save once the save is answered', async () => {
    const { n, gets, answer } = await loaded('a\n', 3);
    n.edit('b');
    n.save();
    n.frameRev(9);
    expect(n.conflict).toBe(false);
    await answer(saved(4));
    expect(gets().length).toBe(2);
    await answer(got('theirs\n', 9));
    expect(n.text).toBe('theirs\n');
  });
  it('marks a conflict when dirty', async () => {
    const { n, gets } = await loaded('a\n', 3);
    n.edit('mine');
    n.frameRev(5);
    expect(n.conflict).toBe(true);
    expect(n.currentRev).toBe(5);
    expect(gets().length).toBe(1);
  });
  it('ignores frames while its own save is in flight', async () => {
    const { n } = await loaded('a\n', 3);
    n.edit('b');
    n.save();
    n.frameRev(9);
    expect(n.conflict).toBe(false);
  });
  it('turns a silent reload into a conflict when the user typed meanwhile', async () => {
    const { n, answer } = await loaded('a\n', 3);
    n.frameRev(5);
    n.edit('typed');
    await answer(got('theirs\n', 5));
    expect(n.conflict).toBe(true);
    expect(n.text).toBe('typed');
  });
  it('drops a silent reload answer older than the editor', async () => {
    const { n, answer } = await loaded('a\n', 3);
    n.frameRev(5);
    await answer(got('old\n', 2));
    expect(n.text).toBe('a\n');
    expect(n.rev).toBe(3);
  });
  it('keeps the text when a reload fails', async () => {
    const { n, answer } = await loaded('a\n', 3);
    n.frameRev(5);
    await answer({ ok: false, code: 'timeout', error: 'No answer' });
    expect(n.text).toBe('a\n');
    expect(n.loadError).toBe('');
  });
});

describe('NoteSession load theirs (confirmed reload, noteSavesTaken)', () => {
  async function conflicted() {
    const h = await loaded('a\n', 3);
    h.n.edit('mine');
    h.n.save();
    await h.answer(conflict(7));
    return h;
  }
  it('needs a second click', async () => {
    const { n, calls } = await conflicted();
    n.loadTheirs();
    expect(n.loadArmed).toBe(true);
    expect(calls.at(-1)?.kind).toBe('set');
    n.loadTheirs();
    expect(calls.at(-1)).toEqual({ kind: 'get' });
  });
  it('replaces the text, even with a lower rev (a restored daemon)', async () => {
    const { n, answer } = await conflicted();
    n.loadTheirs();
    n.loadTheirs();
    await answer(got('theirs\n', 2));
    expect(n.text).toBe('theirs\n');
    expect(n.rev).toBe(2);
    expect(n.conflict).toBe(false);
    expect(n.dirty).toBe(false);
  });
  it('does not discard text typed after the confirmation', async () => {
    const { n, answer } = await conflicted();
    n.loadTheirs();
    n.loadTheirs();
    n.edit('mine, more');
    await answer(got('theirs\n', 8));
    expect(n.text).toBe('mine, more');
    expect(n.conflict).toBe(true);
  });
  it('does not discard after a save was accepted since the confirmation', async () => {
    const { n, calls, answerAt } = await conflicted();
    n.loadTheirs();
    n.loadTheirs();
    // The get is pending; an overwrite goes out and is answered first.
    n.save(true);
    expect(calls.at(-1)).toEqual({ kind: 'set', text: 'mine\n', base: 7 });
    // Unanswered calls, oldest first: [get, set]. The set answers first.
    await answerAt(1, saved(8));
    expect(n.rev).toBe(8);
    await answerAt(0, got('theirs\n', 7));
    expect(n.text).toBe('mine');
    expect(n.rev).toBe(8);
  });
  it('is refused outside a conflict', async () => {
    const h = await loaded('a\n', 3);
    h.n.edit('mine');
    h.n.loadTheirs();
    expect(h.n.loadArmed).toBe(false);
  });
  it('is refused while a save is in flight', async () => {
    const { n } = await conflicted();
    n.save(true);
    n.loadTheirs();
    expect(n.loadArmed).toBe(false);
  });
});

describe('NoteSession link and close', () => {
  it('linkLost abandons the save and keeps the text dirty', async () => {
    const { n, calls, clock } = await loaded();
    n.edit('b');
    n.save();
    n.linkLost();
    expect(n.saving).toBe(false);
    expect(n.dirty).toBe(true);
    expect(n.linkDown).toBe(true);
    n.edit('c');
    expect(n.text).toBe('b');
    n.linkBack();
    clock.advance(NOTES_DEBOUNCE_MS);
    expect(calls.at(-1)).toEqual({ kind: 'set', text: 'b\n', base: 3 });
  });
  it('a close that waited on the link saves at once when it is back', async () => {
    const { n, calls, answer } = await loaded();
    let closed = false;
    n.onClosed = () => (closed = true);
    n.edit('b');
    n.linkLost();
    expect(n.close()).toBe('wait');
    n.linkBack();
    expect(calls.at(-1)).toEqual({ kind: 'set', text: 'b\n', base: 3 });
    await answer(saved(4));
    expect(closed).toBe(true);
  });
  it('a session that turned read-only keeps its unsaved text on close', async () => {
    const { n, sets } = await loaded();
    n.edit('b');
    n.setViewOnly(true);
    expect(n.close()).toBe('wait');
    expect(n.closing).toBe(true);
    expect(n.text).toBe('b');
    expect(sets()).toEqual([]);
  });
  it('sends nothing once the session became read-only', async () => {
    const { n, clock, sets } = await loaded();
    n.edit('b');
    n.setViewOnly(true);
    clock.advance(NOTES_DEBOUNCE_MS * 2);
    n.save();
    expect(sets()).toEqual([]);
    expect(n.text).toBe('b');
  });
  it('a first load lost with the link is sent again when it is back', async () => {
    const h = harness();
    h.n.load();
    h.n.linkLost();
    h.n.linkBack();
    expect(h.gets().length).toBe(2);
    await h.answerAt(1, got('x\n', 2));
    expect(h.n.text).toBe('x\n');
  });
  it('closes at once when clean', async () => {
    const { n } = await loaded();
    expect(n.close()).toBe('closed');
  });
  it('waits for the save of a dirty editor, then closes', async () => {
    const { n, answer } = await loaded();
    let closed = false;
    n.onClosed = () => (closed = true);
    n.edit('b');
    expect(n.close()).toBe('wait');
    await answer(saved(4));
    expect(closed).toBe(true);
  });
  it('stays open in a conflict', async () => {
    const { n, answer } = await loaded();
    n.edit('b');
    n.save();
    await answer(conflict(9));
    expect(n.close()).toBe('wait');
  });
  it('keeps the text when the pane is closed, and closes only by a discard', async () => {
    const { n, sets } = await loaded();
    n.edit('b');
    n.paneClosed();
    expect(n.paneGone).toBe(true);
    expect(n.text).toBe('b');
    expect(n.close()).toBe('wait');
    expect(n.closing).toBe(true);
    expect(sets()).toEqual([]);
  });
  it('closes a clean editor of a closed pane at once', async () => {
    const { n } = await loaded();
    n.paneClosed();
    expect(n.close()).toBe('closed');
  });
});
