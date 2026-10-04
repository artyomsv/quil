import { describe, expect, it } from 'vitest';
import { INPUT_CHUNK } from './connection';
import { PasteFlow, QUEUE_FULL_PREFIX, RESEND_MS } from './paste';
import type { Outcome } from './requests';

interface Call {
  paneId: string;
  bytes: number;
  resolve: (o: Outcome) => void;
}

function setup() {
  const chunks: Call[] = [];
  const keys: { paneId: string; data: string }[] = [];
  const notices: string[] = [];
  const timers: (() => void)[] = [];
  const flow = new PasteFlow({
    sendChunk: (paneId, b64) =>
      new Promise<Outcome>((resolve) => chunks.push({ paneId, bytes: atob(b64).length, resolve })),
    sendKeys: (paneId, data) => keys.push({ paneId, data }),
    notice: (t) => notices.push(t),
    sleep: () => new Promise<void>((r) => timers.push(() => r())),
  });
  const tick = (): Promise<void> => new Promise((r) => setTimeout(r, 0));
  return { flow, chunks, keys, notices, timers, tick };
}

const big = (n: number): string => 'x'.repeat(n);

describe('PasteFlow', () => {
  it('small input goes straight out as keys', () => {
    const { flow, keys, chunks } = setup();
    flow.input('p1', 'ls\r');
    flow.input('p1', big(INPUT_CHUNK));
    expect(keys.map((k) => k.data.length)).toEqual([3, INPUT_CHUNK]);
    expect(chunks).toHaveLength(0);
  });

  it('a large paste keeps exactly one chunk unanswered', async () => {
    const { flow, chunks, tick } = setup();
    flow.input('p1', big(INPUT_CHUNK * 2 + 10));
    await tick();
    expect(chunks).toHaveLength(1);
    chunks[0]?.resolve({ ok: true });
    await tick();
    expect(chunks).toHaveLength(2);
    chunks[1]?.resolve({ ok: true });
    await tick();
    expect(chunks.map((c) => c.bytes)).toEqual([INPUT_CHUNK, INPUT_CHUNK, 10]);
  });

  it('queue full and busy resend the SAME chunk after a wait; nothing after it goes first', async () => {
    const { flow, chunks, timers, tick } = setup();
    flow.input('p1', big(INPUT_CHUNK + 5));
    await tick();
    chunks[0]?.resolve({ ok: false, code: 'failed', error: 'pane input queue is full — its child has stopped reading stdin' });
    await tick();
    expect(chunks).toHaveLength(1);
    timers.shift()?.();
    await tick();
    expect(chunks).toHaveLength(2);
    expect(chunks[1]?.bytes).toBe(INPUT_CHUNK);
    chunks[1]?.resolve({ ok: false, code: 'busy', error: 'busy' });
    await tick();
    timers.shift()?.();
    await tick();
    expect(chunks[2]?.bytes).toBe(INPUT_CHUNK);
    chunks[2]?.resolve({ ok: true });
    await tick();
    expect(chunks[3]?.bytes).toBe(5);
    expect(RESEND_MS).toBe(250);
    expect(QUEUE_FULL_PREFIX).toBe('pane input queue is full');
  });

  it('keys typed during a paste go out after the last chunk', async () => {
    const { flow, chunks, keys, tick } = setup();
    flow.input('p1', big(INPUT_CHUNK + 1));
    flow.input('p1', 'q');
    await tick();
    expect(keys).toHaveLength(0);
    chunks[0]?.resolve({ ok: true });
    await tick();
    chunks[1]?.resolve({ ok: true });
    await tick();
    expect(keys).toEqual([{ paneId: 'p1', data: 'q' }]);
    flow.input('p1', 'r');
    expect(keys).toHaveLength(2);
  });

  it('a second paste, even to another pane, waits for the first (reject(A) blocks B)', async () => {
    const { flow, chunks, notices, tick } = setup();
    flow.input('a', big(INPUT_CHUNK + 1));
    flow.input('b', big(INPUT_CHUNK + 1));
    await tick();
    expect(chunks.map((c) => c.paneId)).toEqual(['a']);
    chunks[0]?.resolve({ ok: false, code: 'failed', error: 'no such pane' });
    await tick();
    expect(notices[0]).toContain('no such pane');
    expect(chunks.map((c) => c.paneId)).toEqual(['a', 'b']);
  });

  it('a lost answer ends the paste with the partial notice and never resends', async () => {
    const { flow, chunks, notices, tick } = setup();
    flow.input('p1', big(INPUT_CHUNK * 3));
    await tick();
    chunks[0]?.resolve({ ok: false, code: 'offline', error: 'connection lost' });
    await tick();
    expect(chunks).toHaveLength(1);
    expect(notices).toEqual(['Paste may be partly delivered']);
  });

  it('a reconnect ends the paste once and drops what waited behind it', async () => {
    const { flow, chunks, keys, notices, tick } = setup();
    flow.input('p1', big(INPUT_CHUNK * 3));
    flow.input('p1', 'q');
    await tick();
    flow.reconnecting();
    chunks[0]?.resolve({ ok: false, code: 'offline', error: 'connection lost' });
    await tick();
    expect(chunks).toHaveLength(1);
    expect(keys).toHaveLength(0);
    expect(notices).toEqual(['Paste may be partly delivered']);
  });

  it('a restart of the pane ends its paste the same way', async () => {
    const { flow, chunks, notices, tick } = setup();
    flow.input('p1', big(INPUT_CHUNK * 3));
    await tick();
    flow.paneRestarted('other');
    flow.paneRestarted('p1');
    chunks[0]?.resolve({ ok: true });
    await tick();
    expect(chunks).toHaveLength(1);
    expect(notices).toEqual(['Paste may be partly delivered']);
  });
});
