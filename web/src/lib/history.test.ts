import { describe, expect, it } from 'vitest';
import { entryTitle, HistoryFlow, type HistoryIO, historySupported } from './history';
import type { Outcome } from './requests';

describe('history', () => {
  const plugins = [
    { name: 'claude-code', display_name: 'C', category: 'ai', record_history: true },
    { name: 'terminal', display_name: 'T', category: 'tools' },
  ];
  it('is supported only for a plugin that records history', () => {
    expect(historySupported('claude-code', plugins)).toBe(true);
    expect(historySupported('terminal', plugins)).toBe(false);
    expect(historySupported('', plugins)).toBe(false);
    expect(historySupported('unknown', plugins)).toBe(false);
  });
  it('titles an entry like the TUI', () => {
    const ts = new Date(2026, 0, 2, 3, 4, 5).getTime();
    expect(entryTitle(ts)).toBe('Input @ 2026-01-02 03:04:05');
  });
});

describe('HistoryFlow', () => {
  const listed = (entries: { ts_ms: number; preview: string }[]): Outcome => ({
    ok: true,
    reply: { type: 'pane_history_resp', payload: { pane_id: 'p', entries } },
  });
  const entry = (ts: number, text: string, found = true): Outcome => ({
    ok: true,
    reply: { type: 'pane_history_entry_resp', payload: { pane_id: 'p', ts_ms: ts, text, found } },
  });

  function harness(answers: { list: Outcome[]; entry: Outcome[] }) {
    const calls: string[] = [];
    const io: HistoryIO = {
      list: async () => {
        calls.push('list');
        return answers.list.shift() ?? listed([]);
      },
      entry: async (ts) => {
        calls.push(`entry ${ts}`);
        return answers.entry.shift() ?? entry(ts, '');
      },
    };
    return { calls, io };
  }
  const settle = async (): Promise<void> => {
    for (let i = 0; i < 6; i++) await Promise.resolve();
  };

  it('asks the daemon nothing for an unsupported pane type', async () => {
    const { calls, io } = harness({ list: [], entry: [] });
    const f = new HistoryFlow(io, false);
    f.open();
    await settle();
    expect(calls).toEqual([]);
    expect(f.view).toEqual({ state: 'unsupported' });
  });

  it('lists, opens an entry with its line breaks, and goes back', async () => {
    const { io } = harness({ list: [listed([{ ts_ms: 5, preview: 'hi' }])], entry: [entry(5, 'a\nb')] });
    const f = new HistoryFlow(io, true);
    f.open();
    await settle();
    expect(f.view).toEqual({ state: 'list', entries: [{ ts_ms: 5, preview: 'hi' }] });
    await f.pick(5);
    expect(f.view.state).toBe('entry');
    if (f.view.state === 'entry') expect(f.view.text).toBe('a\nb');
    expect(f.back()).toBe(true);
    expect(f.view).toEqual({ state: 'list', entries: [{ ts_ms: 5, preview: 'hi' }] });
    expect(f.back()).toBe(false);
  });

  it('reloads the list when the entry is gone', async () => {
    const { calls, io } = harness({ list: [listed([{ ts_ms: 5, preview: 'x' }]), listed([])], entry: [entry(5, '', false)] });
    const f = new HistoryFlow(io, true);
    f.open();
    await settle();
    await f.pick(5);
    expect(calls).toEqual(['list', 'entry 5', 'list']);
    expect(f.view).toEqual({ state: 'list', entries: [] });
  });

  it('shows a failed list as an error', async () => {
    const { io } = harness({ list: [{ ok: false, code: 'timeout', error: 'No answer from the daemon' }], entry: [] });
    const f = new HistoryFlow(io, true);
    f.open();
    await settle();
    expect(f.view).toEqual({ state: 'error', text: 'No answer from the daemon' });
  });
});
