import { describe, expect, it } from 'vitest';
import type { Clock } from './connection';
import type { Message } from './protocol';
import { REQUEST_TIMEOUT_MS, Requests } from './requests';

class FakeClock implements Clock {
  t = 0;
  private next = 1;
  timers = new Map<number, { at: number; fn: () => void }>();
  setTimeout(fn: () => void, ms: number): unknown {
    const id = this.next++;
    this.timers.set(id, { at: this.t + ms, fn });
    return id;
  }
  clearTimeout(h: unknown): void {
    this.timers.delete(h as number);
  }
  now(): number {
    return this.t;
  }
  advance(ms: number): void {
    this.t += ms;
    for (const [id, tm] of [...this.timers]) {
      if (tm.at <= this.t) {
        this.timers.delete(id);
        tm.fn();
      }
    }
  }
}

function setup(online = true) {
  const sent: Message[] = [];
  const clock = new FakeClock();
  const r = new Requests((m) => {
    if (!online) return false;
    sent.push(m);
    return true;
  }, clock);
  return { r, sent, clock };
}

describe('Requests', () => {
  it('gives each request its own id and resolves on the answer carrying it', async () => {
    const { r, sent } = setup();
    const a = r.request('destroy_tab', { tab_id: 't1' });
    const b = r.request('update_tab', { tab_id: 't1', name: 'x' });
    expect(sent.map((m) => m.id)).toEqual(['req-1', 'req-2']);
    expect(r.answer({ type: 'tab_op_resp', id: 'req-2', payload: { ok: true } })).toBe(true);
    expect(await b).toEqual({ ok: true, reply: { type: 'tab_op_resp', id: 'req-2', payload: { ok: true } } });
    r.answer({ type: 'tab_op_resp', id: 'req-1', payload: { ok: false, error: 'no such tab' } });
    expect(await a).toMatchObject({ ok: false, code: 'failed', error: 'no such tab' });
  });

  it('an error envelope carrying the id ends the request with its code', async () => {
    const { r } = setup();
    const p = r.request('update_layout', {});
    r.answer({ type: 'error', id: 'req-1', payload: { code: 'stale', message: 'layout changed', type: 'update_layout' } });
    expect(await p).toMatchObject({ ok: false, code: 'stale' });
  });

  it('reads the failure shapes: error text, ok:false, success:false, delivered:false', async () => {
    const { r } = setup();
    const ps = [r.request('a', {}), r.request('b', {}), r.request('c', {}), r.request('d', {})];
    r.answer({ type: 'split_pane_resp', id: 'req-1', payload: { error: 'unknown target' } });
    r.answer({ type: 'pane_op_resp', id: 'req-2', payload: { ok: false, error: 'no such pane' } });
    r.answer({ type: 'destroy_pane_resp', id: 'req-3', payload: { success: false } });
    r.answer({ type: 'pane_input_resp', id: 'req-4', payload: { delivered: false, error: 'pane input queue is full — x' } });
    const out = await Promise.all(ps);
    expect(out.map((o) => o.ok)).toEqual([false, false, false, false]);
    expect(out[3]).toMatchObject({ error: 'pane input queue is full — x' });
  });

  it('a message that is not an answer is not consumed', () => {
    const { r } = setup();
    void r.request('a', {});
    expect(r.answer({ type: 'workspace_state', payload: {} })).toBe(false);
    expect(r.answer({ type: 'pane_op_resp', id: 'someone-else', payload: { ok: true } })).toBe(false);
  });

  it('times out after 10 s with the given text', async () => {
    const { r, clock } = setup();
    const p = r.request('split_pane_req', {}, { timeoutText: 'Still working' });
    clock.advance(REQUEST_TIMEOUT_MS - 1);
    clock.advance(1);
    expect(await p).toEqual({ ok: false, code: 'timeout', error: 'Still working' });
    expect(r.answer({ type: 'split_pane_resp', id: 'req-1', payload: {} })).toBe(false);
  });

  it('quietMs resolves ok:quiet when nothing answers in time', async () => {
    const { r, clock } = setup();
    const p = r.request('update_layout', {}, { quietMs: 2000 });
    clock.advance(2000);
    expect(await p).toEqual({ ok: true, quiet: true });
  });

  it('fails at once while offline, and fails every pending request on reconnect', async () => {
    const off = setup(false);
    expect(await off.r.request('x', {})).toMatchObject({ ok: false, code: 'offline' });
    const { r } = setup();
    const p = r.request('x', {});
    r.reconnecting();
    expect(await p).toMatchObject({ ok: false, code: 'offline', error: 'connection lost' });
  });

  it('sanitizes remote error text', async () => {
    const { r } = setup();
    const p = r.request('x', {});
    const rlo = String.fromCodePoint(0x202e);
    r.answer({ type: 'error', id: 'req-1', payload: { code: 'refused', message: `bad${rlo}name`, type: 'x' } });
    expect(await p).toMatchObject({ error: 'badname' });
  });
});
