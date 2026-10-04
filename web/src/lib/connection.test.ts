import { describe, expect, it } from 'vitest';
import { Connection, type Clock, type ConnectionEvents, type SocketLike, type StorageLike } from './connection';
import type { Message, PaneOutputFrame } from './protocol';
import { SafeStorage } from './storage';

class FakeSocket implements SocketLike {
  binaryType = '';
  sent: Message[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string | ArrayBuffer }) => void) | null = null;
  onclose: ((ev: { code: number; reason: string }) => void) | null = null;
  send(data: string): void {
    this.sent.push(JSON.parse(data) as Message);
  }
  close(): void {}
  open(): void {
    this.onopen?.();
  }
  recv(m: Message): void {
    this.onmessage?.({ data: JSON.stringify(m) });
  }
  closeWith(code: number, reason = ''): void {
    this.onclose?.({ code, reason });
  }
}

class FakeStorage implements StorageLike {
  data = new Map<string, string>();
  getItem(k: string): string | null {
    return this.data.get(k) ?? null;
  }
  setItem(k: string, v: string): void {
    this.data.set(k, v);
  }
  removeItem(k: string): void {
    this.data.delete(k);
  }
}

class FakeClock implements Clock {
  t = 0;
  private next = 1;
  private timers = new Map<number, { at: number; fn: () => void }>();
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
    const end = this.t + ms;
    for (;;) {
      let due: [number, { at: number; fn: () => void }] | undefined;
      for (const e of this.timers) if (e[1].at <= end && (!due || e[1].at < due[1].at)) due = e;
      if (!due) break;
      this.timers.delete(due[0]);
      this.t = due[1].at;
      due[1].fn();
    }
    this.t = end;
  }
}

interface Rig {
  conn: Connection;
  sockets: FakeSocket[];
  storage: FakeStorage;
  clock: FakeClock;
  welcomes: number;
  messages: Message[];
  outputs: PaneOutputFrame[];
  reconnecting: number;
  closed: Array<[number, string, boolean]>;
}

function rig(
  random: () => number = () => 0.5,
  sessionGone: () => Promise<boolean> = async () => false,
  store?: StorageLike,
): Rig {
  const r = { sockets: [], welcomes: 0, messages: [], outputs: [], reconnecting: 0, closed: [] } as unknown as Rig;
  r.storage = new FakeStorage();
  r.clock = new FakeClock();
  const events: ConnectionEvents = {
    onWelcome: () => void r.welcomes++,
    onMessage: (m) => void r.messages.push(m),
    onOutput: (f) => void r.outputs.push(f),
    onReconnecting: () => void r.reconnecting++,
    onClosed: (c, reason, retrying) => void r.closed.push([c, reason, retrying]),
  };
  r.conn = new Connection(
    () => {
      const s = new FakeSocket();
      r.sockets.push(s);
      return s;
    },
    store ?? r.storage,
    r.clock,
    events,
    () => ({ cols: 80, rows: 24, winCols: 100, winRows: 30 }),
    random, // default 0.5 means no jitter
    sessionGone,
  );
  return r;
}

// settle lets pending promise callbacks (a session check's answer) run.
const settle = (): Promise<void> => new Promise((resolve) => setTimeout(resolve, 0));

const welcome: Message = { type: 'web_welcome', payload: { client_id: 'c-1', rights: 'full', version: '1.2.3' } };

function opened(r: Rig): FakeSocket {
  r.conn.start();
  const s = r.sockets[r.sockets.length - 1]!;
  s.open();
  return s;
}

describe('Connection', () => {
  it('sends web_open with the stored hint and key first', () => {
    const r = rig();
    r.storage.setItem('quil.web.client_id', 'hint-1');
    r.storage.setItem('quil.web.key', 'k-1');
    const s = opened(r);
    expect(s.sent).toEqual([{ type: 'web_open', payload: { client_id_hint: 'hint-1', key: 'k-1' } }]);
  });

  it('answers a welcome with hello and attach, reattach only after the first', () => {
    const r = rig();
    const s = opened(r);
    s.recv(welcome);
    expect(r.storage.getItem('quil.web.client_id')).toBe('c-1');
    expect(r.welcomes).toBe(1);
    const [, hello, attach] = s.sent;
    expect(hello!.type).toBe('hello');
    expect(hello!.id).toMatch(/^hello-/);
    expect(hello!.payload).toMatchObject({ kind: 'web', proto: 1, client_id: 'c-1', version: '1.2.3', exe: 'browser' });
    expect(attach!.type).toBe('attach');
    expect(attach!.id).toMatch(/^attach-/);
    expect(attach!.payload).toEqual({ cols: 80, rows: 24, win_cols: 100, win_rows: 30, client_id: 'c-1', reattach: false });

    s.closeWith(4001);
    const s2 = r.sockets[1]!;
    s2.open();
    s2.recv(welcome);
    expect(s2.sent[2]!.payload).toMatchObject({ reattach: true });
  });

  it('sends a large input as one id-less frame', () => {
    const r = rig();
    const s = opened(r);
    const text = 'x'.repeat(200 * 1024);
    r.conn.sendInput('p1', text);
    const frames = s.sent.filter((m) => m.type === 'pane_input');
    expect(frames).toHaveLength(1);
    expect(frames[0]!.id).toBeUndefined();
    const p = frames[0]!.payload as { pane_id: string; data: string };
    expect(p.pane_id).toBe('p1');
    expect(atob(p.data)).toBe(text);
  });

  it('batches acknowledgements by size and by time', () => {
    const r = rig();
    const s = opened(r);
    r.conn.processed(70_000);
    expect(s.sent.filter((m) => m.type === 'web_ack')).toEqual([{ type: 'web_ack', payload: { bytes: 70_000 } }]);
    r.conn.processed(10);
    expect(s.sent.filter((m) => m.type === 'web_ack')).toHaveLength(1);
    r.clock.advance(100);
    expect(s.sent.filter((m) => m.type === 'web_ack')).toHaveLength(2);
    expect(s.sent[s.sent.length - 1]).toEqual({ type: 'web_ack', payload: { bytes: 10 } });
  });

  it('acknowledges only the data bytes of an undecodable frame', () => {
    const r = rig();
    const s = opened(r);
    const bad = new Uint8Array(11 + 3 + 500);
    bad[0] = 9; // unknown kind
    bad[10] = 3; // pane id length
    s.onmessage?.({ data: bad.buffer });
    expect(s.sent.filter((m) => m.type === 'web_ack')).toHaveLength(0);
    r.clock.advance(100);
    expect(s.sent.filter((m) => m.type === 'web_ack')).toEqual([{ type: 'web_ack', payload: { bytes: 500 } }]);
  });

  it('acknowledges nothing for a frame too short to parse', () => {
    const r = rig();
    const s = opened(r);
    s.onmessage?.({ data: new Uint8Array(5).buffer });
    const lying = new Uint8Array(20);
    lying[10] = 200; // id length past the end
    s.onmessage?.({ data: lying.buffer });
    r.clock.advance(1000);
    expect(s.sent.filter((m) => m.type === 'web_ack')).toHaveLength(0);
  });

  it('ignores processed() after the socket closed', () => {
    const r = rig();
    const s = opened(r);
    s.closeWith(1006);
    r.conn.processed(70_000);
    r.clock.advance(1000);
    expect(s.sent.filter((m) => m.type === 'web_ack')).toHaveLength(0);
  });

  it('ignores processed() carrying a stale epoch after a reconnect', () => {
    const r = rig();
    const s = opened(r);
    const old = r.conn.epoch;
    s.closeWith(4001);
    const s2 = r.sockets[1]!;
    s2.open();
    expect(r.conn.epoch).not.toBe(old);
    r.conn.processed(70_000, old);
    r.clock.advance(1000);
    expect(s2.sent.filter((m) => m.type === 'web_ack')).toHaveLength(0);
    r.conn.processed(70_000, r.conn.epoch);
    expect(s2.sent.filter((m) => m.type === 'web_ack')).toHaveLength(1);
  });

  it('jitters the first back-off by 20 percent either way', () => {
    for (const [random, wait] of [[0, 800], [1, 1200]] as const) {
      const r = rig(() => random);
      const s = opened(r);
      s.closeWith(4003);
      r.clock.advance(wait - 1);
      expect(r.sockets).toHaveLength(1);
      r.clock.advance(1);
      expect(r.sockets).toHaveLength(2);
    }
  });

  it('reconnects at once on 4001 and with back-off on 4003', () => {
    const r = rig();
    let s = opened(r);
    s.closeWith(4001);
    expect(r.reconnecting).toBe(1);
    expect(r.sockets).toHaveLength(2);

    s = r.sockets[1]!;
    s.open();
    s.closeWith(4003);
    expect(r.sockets).toHaveLength(2);
    r.clock.advance(999);
    expect(r.sockets).toHaveLength(2);
    r.clock.advance(1);
    expect(r.sockets).toHaveLength(3);

    for (const [i, wait] of [2000, 4000, 8000, 16000, 30000, 30000].entries()) {
      r.sockets[r.sockets.length - 1]!.closeWith(4003);
      const before = r.sockets.length;
      r.clock.advance(wait - 1);
      expect(r.sockets, `step ${i}`).toHaveLength(before);
      r.clock.advance(1);
      expect(r.sockets, `step ${i}`).toHaveLength(before + 1);
    }
  });

  it('resets the back-off after a socket stays open 60 s', () => {
    const r = rig();
    opened(r);
    r.sockets[0]!.closeWith(4003);
    r.clock.advance(1000);
    r.sockets[1]!.open();
    r.clock.advance(60_000);
    r.sockets[1]!.closeWith(4003);
    r.clock.advance(1000);
    expect(r.sockets).toHaveLength(3);
  });

  it('stays down on 4004, 4005, 4006 and 1001', () => {
    for (const code of [4004, 4005, 4006, 1001]) {
      const r = rig();
      const s = opened(r);
      s.closeWith(code, 'why');
      r.clock.advance(60_000);
      expect(r.sockets).toHaveLength(1);
      expect(r.closed).toEqual([[code, 'why', false]]);
    }
  });

  it('on a refused login key clears it and does not reconnect', () => {
    const r = rig();
    r.storage.setItem('quil.web.key', 'k-1');
    const s = opened(r);
    s.closeWith(1008, 'login required');
    r.clock.advance(60_000);
    expect(r.storage.getItem('quil.web.key')).toBeNull();
    expect(r.sockets).toHaveLength(1);
    expect(r.closed).toEqual([[1008, 'login required', false]]);
  });

  it('after a refused handshake with the session gone, clears the key and shows the login', async () => {
    let checks = 0;
    const r = rig(undefined, async () => {
      checks++;
      return true;
    });
    r.storage.setItem('quil.web.key', 'k-1');
    r.conn.start();
    // A 401 at the upgrade reaches the page as 1006 with no open before it.
    r.sockets[0]!.closeWith(1006);
    await settle();
    expect(checks).toBe(1);
    expect(r.storage.getItem('quil.web.key')).toBeNull();
    expect(r.closed[r.closed.length - 1]).toEqual([1008, 'login required', false]);
    r.clock.advance(60_000);
    expect(r.sockets).toHaveLength(1);
  });

  it('keeps the key another tab stored while the session check was out', async () => {
    const r = rig(undefined, async () => {
      // Another tab logs in before this check answers.
      r.storage.setItem('quil.web.key', 'k-2');
      return true;
    });
    r.storage.setItem('quil.web.key', 'k-1');
    r.conn.start();
    r.sockets[0]!.closeWith(1006);
    await settle();
    expect(r.storage.getItem('quil.web.key')).toBe('k-2');
    expect(r.closed).toEqual([[1006, '', true]]);
    r.clock.advance(1000);
    expect(r.sockets).toHaveLength(2);
  });

  it('keeps the key another tab stored through its own wrapper over the shared store', async () => {
    // As in the page: each tab has its own SafeStorage over one localStorage.
    const shared = new FakeStorage();
    const tabA = new SafeStorage(shared);
    const tabB = new SafeStorage(shared);
    let answer: (gone: boolean) => void = () => {};
    const r = rig(undefined, () => new Promise<boolean>((resolve) => (answer = resolve)), tabA);
    tabA.setItem('quil.web.key', 'k1');
    r.conn.start();
    r.sockets[0]!.closeWith(1006);
    // Tab B logs in before tab A's old check answers 401.
    tabB.setItem('quil.web.key', 'k2');
    answer(true);
    await settle();
    expect(shared.getItem('quil.web.key')).toBe('k2');
    expect(tabA.getItem('quil.web.key')).toBe('k2');
    expect(r.closed).toEqual([[1006, '', true]]);
    r.clock.advance(1000);
    expect(r.sockets).toHaveLength(2);
  });

  it('after a refused handshake with the session live or unknown, keeps retrying', async () => {
    const r = rig(undefined, async () => false);
    r.storage.setItem('quil.web.key', 'k-1');
    r.conn.start();
    r.sockets[0]!.closeWith(1006);
    await settle();
    expect(r.storage.getItem('quil.web.key')).toBe('k-1');
    expect(r.closed).toEqual([[1006, '', true]]);
    r.clock.advance(1000);
    expect(r.sockets).toHaveLength(2);
  });

  it('does not ask for the session when an open socket drops', async () => {
    let checks = 0;
    const r = rig(undefined, async () => {
      checks++;
      return true;
    });
    const s = opened(r);
    s.closeWith(1006);
    await settle();
    expect(checks).toBe(0);
    r.clock.advance(1000);
    expect(r.sockets).toHaveLength(2);
  });

  it('ignores a session answer that comes back after a new login', async () => {
    let answer: (gone: boolean) => void = () => {};
    const r = rig(undefined, () => new Promise<boolean>((resolve) => (answer = resolve)));
    r.conn.start();
    r.sockets[0]!.closeWith(1006);
    r.conn.stop();
    r.storage.setItem('quil.web.key', 'k-2');
    // The new login's socket is still connecting when the old answer lands.
    r.conn.start();
    answer(true);
    await settle();
    expect(r.storage.getItem('quil.web.key')).toBe('k-2');
    expect(r.closed).toEqual([[1006, '', true]]);
    expect(r.sockets[1]!.onclose).not.toBeNull();
  });

  it('drops pending acknowledgements when the socket closes', () => {
    const r = rig();
    const s = opened(r);
    r.conn.processed(10);
    s.closeWith(4001);
    const s2 = r.sockets[1]!;
    s2.open();
    r.clock.advance(1000);
    expect(s2.sent.filter((m) => m.type === 'web_ack')).toHaveLength(0);
  });

  it('delivers a binary frame to onOutput', () => {
    const r = rig();
    const s = opened(r);
    const frame = new Uint8Array([1, 0, 0, 0, 0, 0, 0, 0, 0, 7, 1, 0x70, 0x41]);
    s.onmessage?.({ data: frame.buffer });
    expect(r.outputs).toHaveLength(1);
    expect(r.outputs[0]!.paneId).toBe('p');
    expect(r.outputs[0]!.generation).toBe(7n);
    expect(Array.from(r.outputs[0]!.data)).toEqual([0x41]);
  });

  it('passes other daemon messages to onMessage', () => {
    const r = rig();
    const s = opened(r);
    s.recv(welcome);
    s.recv({ type: 'hello_resp', id: s.sent[1]!.id, payload: {} });
    s.recv({ type: 'workspace_state', payload: {} });
    expect(r.messages.map((m) => m.type)).toEqual(['hello_resp', 'workspace_state']);
  });

  it('holds back workspace_state until this socket hello is answered', () => {
    const r = rig();
    let s = opened(r);
    s.recv({ type: 'workspace_state', payload: { rev: 1 } });
    s.recv(welcome);
    s.recv({ type: 'workspace_state', payload: { rev: 2 } });
    s.recv({ type: 'pane_sizes', payload: { panes: [] } });
    s.recv({ type: 'hello_resp', id: 'not-ours', payload: {} });
    s.recv({ type: 'workspace_state', payload: { rev: 3 } });
    expect(r.messages.map((m) => m.type)).toEqual(['pane_sizes', 'hello_resp']);
    s.recv({ type: 'hello_resp', id: s.sent[1]!.id, payload: {} });
    s.recv({ type: 'workspace_state', payload: { rev: 4 } });
    expect(r.messages[r.messages.length - 1]).toEqual({ type: 'workspace_state', payload: { rev: 4 } });

    // A resynced socket re-attaches on the same daemon connection: a state the
    // daemon queued before the new hello must not be applied.
    s.closeWith(4001);
    s = r.sockets[1]!;
    s.open();
    s.recv(welcome);
    const before = r.messages.length;
    s.recv({ type: 'workspace_state', payload: { rev: 5 } });
    expect(r.messages).toHaveLength(before);
    s.recv({ type: 'hello_resp', id: s.sent[1]!.id, payload: {} });
    s.recv({ type: 'workspace_state', payload: { rev: 6 } });
    expect(r.messages.slice(before).map((m) => m.payload)).toEqual([{}, { rev: 6 }]);
  });

  it('counts an error naming the hello as its answer', () => {
    const r = rig();
    const s = opened(r);
    s.recv(welcome);
    s.recv({ type: 'error', id: s.sent[1]!.id, payload: { code: 'bad_payload', message: 'x', type: 'hello' } });
    s.recv({ type: 'workspace_state', payload: {} });
    expect(r.messages.map((m) => m.type)).toEqual(['error', 'workspace_state']);
  });

  it('answers a second welcome with a new hello and attach under the new id', () => {
    const r = rig();
    const s = opened(r);
    s.recv(welcome);
    s.recv({ type: 'hello_resp', id: s.sent[1]!.id, payload: {} });
    s.recv({ type: 'error', id: s.sent[2]!.id, payload: { code: 'refused', message: 'client id in use', type: 'attach' } });
    s.recv({ type: 'web_welcome', payload: { client_id: 'c-2', rights: 'full', version: '1.2.3' } });
    const [hello, attach] = s.sent.slice(3);
    expect(hello!.payload).toMatchObject({ client_id: 'c-2' });
    expect(attach!.payload).toMatchObject({ client_id: 'c-2' });
    expect(r.storage.getItem('quil.web.client_id')).toBe('c-2');
    // States wait for the answer to the new hello.
    const before = r.messages.length;
    s.recv({ type: 'workspace_state', payload: {} });
    expect(r.messages).toHaveLength(before);
    s.recv({ type: 'hello_resp', id: hello!.id, payload: {} });
    s.recv({ type: 'workspace_state', payload: {} });
    expect(r.messages.map((m) => m.type).slice(before)).toEqual(['hello_resp', 'workspace_state']);
  });

  it('sends a paste over 256 KiB as ordered id-less pieces that fit a frame', () => {
    const r = rig();
    const s = opened(r);
    // In the second text one byte precedes the two-byte characters, so the
    // cut at 256 KiB falls inside a character; the bytes still reassemble.
    for (const text of ['x'.repeat(600 * 1024), 'a' + 'é'.repeat(300 * 1024)]) {
      s.sent = [];
      r.conn.sendInput('p1', text);
      const frames = s.sent.filter((m) => m.type === 'pane_input');
      const pieces = frames.map((m) => {
        expect(m.id).toBeUndefined();
        const p = m.payload as { pane_id: string; data: string };
        expect(p.pane_id).toBe('p1');
        return Uint8Array.from(atob(p.data), (c) => c.charCodeAt(0));
      });
      expect(pieces.length).toBeGreaterThan(1);
      for (const p of pieces) expect(p.length).toBeLessThanOrEqual(256 * 1024);
      for (const m of frames) expect(JSON.stringify(m).length).toBeLessThan(1 << 20);
      const all = new Uint8Array(pieces.reduce((n, p) => n + p.length, 0));
      let at = 0;
      for (const p of pieces) {
        all.set(p, at);
        at += p.length;
      }
      expect(new TextDecoder().decode(all)).toBe(text);
    }
  });

  it('stays down on a protocol error (1008) and keeps the login key', () => {
    const r = rig();
    r.storage.setItem('quil.web.key', 'k-1');
    const s = opened(r);
    s.closeWith(1008, 'protocol error');
    r.clock.advance(60_000);
    expect(r.sockets).toHaveLength(1);
    expect(r.storage.getItem('quil.web.key')).toBe('k-1');
    expect(r.closed).toEqual([[1008, 'protocol error', false]]);
  });
});
