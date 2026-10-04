import { bytesToBase64, utf8Bytes } from './base64';
import { decodePaneOutput, undecodableDataLength } from './frame';
import { CLOSE, type Message, type PaneOutputFrame, type WebWelcome } from './protocol';
import { CLIENT_ID_KEY, LOGIN_KEY, SafeStorage, type StorageLike } from './storage';

export type { StorageLike };

export interface SocketLike {
  send(data: string): void;
  close(code?: number, reason?: string): void;
  binaryType: string;
  onopen: (() => void) | null;
  onmessage: ((ev: { data: string | ArrayBuffer }) => void) | null;
  onclose: ((ev: { code: number; reason: string }) => void) | null;
}

export interface Clock {
  setTimeout(fn: () => void, ms: number): unknown;
  clearTimeout(h: unknown): void;
  now(): number;
}

export interface ConnectionEvents {
  onWelcome(w: WebWelcome): void;
  // Every daemon JSON message.
  onMessage(m: Message): void;
  // Every binary frame. The gateway counts the terminal bytes it has sent and
  // not yet had acknowledged, and resyncs the page past 2 MiB. So whoever
  // receives a frame and does not write it to a terminal (a stale generation,
  // an unknown pane, a frame that arrives before the first workspace_state of
  // a reconnect) must still call processed(frame.data.length), or the count
  // never drains. Callers read epoch when the frame arrives and pass it as
  // processed(frame.data.length, epoch), so a late finish cannot credit a
  // newer socket.
  onOutput(f: PaneOutputFrame): void;
  // The next workspace_state resets every terminal.
  onReconnecting(): void;
  // The socket closed. retrying is false when the connection stays down.
  // A close with code 1008 and reason "login required" means the key was
  // refused: the stored key is already cleared and the UI shows the login
  // form. No reconnect follows.
  onClosed(code: number, reason: string, retrying: boolean): void;
}

export interface AttachSizes {
  cols: number;
  rows: number;
  winCols: number;
  winRows: number;
}

const ACK_BYTES = 65536;
const ACK_DELAY_MS = 100;
const BACKOFF_MIN_MS = 1000;
const BACKOFF_MAX_MS = 30000;
const STABLE_MS = 60000;
const JITTER = 0.2;
// The gateway reads at most 1 MiB per frame, and base64 grows data by a
// third, so input goes out in pieces of at most this many bytes.
export const INPUT_CHUNK = 256 * 1024;

export class Connection {
  private readonly storage: SafeStorage;
  private socket: SocketLike | null = null;
  private opened = false;
  private stopped = false;
  private seq = 0;
  private attaches = 0;
  private clientId = '';
  private backoff = BACKOFF_MIN_MS;
  private reconnectTimer: unknown = null;
  private stableTimer: unknown = null;
  private unacked = 0;
  private socketEpoch = 0;
  private ackTimer: unknown = null;
  // The id of this socket's latest hello, and whether the daemon answered it.
  // A workspace_state is passed on only after the answer: see onFrame.
  private helloId = '';
  private helloAnswered = false;

  constructor(
    private readonly open: () => SocketLike,
    storage: StorageLike,
    private readonly clock: Clock,
    private readonly events: ConnectionEvents,
    private readonly sizes: () => AttachSizes,
    private readonly random: () => number = Math.random,
  ) {
    this.storage = storage instanceof SafeStorage ? storage : new SafeStorage(storage);
  }

  start(): void {
    this.stopped = false;
    this.connect();
  }

  // send queues a daemon-bound message. It is dropped while no socket is open.
  send(m: Message): void {
    this.sendRaw(m);
  }

  // sendInput sends pane_input without an id (the daemon answers only
  // id-bearing input, and one reply per keystroke would flood the queue). A
  // large paste goes out as several messages of at most INPUT_CHUNK bytes. A
  // cut can fall inside a character: the pieces travel in order on the one
  // socket and the daemon writes them to the pane in order, so the pane reads
  // the same bytes.
  sendInput(paneId: string, data: string): void {
    const bytes = utf8Bytes(data);
    for (let i = 0; i < bytes.length; i += INPUT_CHUNK) {
      const piece = bytesToBase64(bytes.subarray(i, i + INPUT_CHUNK));
      this.sendRaw({ type: 'pane_input', payload: { pane_id: paneId, data: piece } });
    }
  }

  // epoch counts sockets; it changes on every new one. Terminal code reads it
  // when it writes a frame and passes it back to processed(), so a write that
  // finishes after a reconnect cannot credit bytes the gateway has reset.
  get epoch(): number {
    return this.socketEpoch;
  }

  // processed reports terminal bytes that xterm finished with. Acks are sent
  // when 64 KiB have built up or 100 ms after the first unacknowledged byte.
  // It does nothing while no socket is open, or when epoch is given and is
  // not the current one.
  processed(bytes: number, epoch?: number): void {
    if (!this.opened) return;
    if (epoch !== undefined && epoch !== this.socketEpoch) return;
    if (bytes <= 0) return;
    this.unacked += bytes;
    if (this.unacked >= ACK_BYTES) {
      this.flushAck();
      return;
    }
    if (this.ackTimer === null) {
      this.ackTimer = this.clock.setTimeout(() => {
        this.ackTimer = null;
        this.flushAck();
      }, ACK_DELAY_MS);
    }
  }

  stop(): void {
    this.stopped = true;
    this.cancelTimers();
    const s = this.socket;
    this.socket = null;
    this.opened = false;
    if (s) {
      s.onopen = s.onmessage = s.onclose = null;
      s.close(CLOSE.goingAway, 'page closed');
    }
  }

  private connect(): void {
    const s = this.open();
    s.binaryType = 'arraybuffer';
    this.socket = s;
    this.socketEpoch++;
    this.opened = false;
    this.helloId = '';
    this.helloAnswered = false;
    s.onopen = () => {
      if (this.socket !== s) return;
      this.opened = true;
      this.sendRaw({
        type: 'web_open',
        payload: {
          client_id_hint: this.storage.getItem(CLIENT_ID_KEY) ?? '',
          key: this.storage.getItem(LOGIN_KEY) ?? '',
        },
      });
      this.stableTimer = this.clock.setTimeout(() => {
        this.stableTimer = null;
        this.backoff = BACKOFF_MIN_MS;
      }, STABLE_MS);
    };
    s.onmessage = (ev) => {
      if (this.socket !== s) return;
      this.onFrame(ev.data);
    };
    s.onclose = (ev) => {
      if (this.socket !== s) return;
      this.socket = null;
      this.opened = false;
      this.onSocketClosed(ev.code, ev.reason);
    };
  }

  private onFrame(data: string | ArrayBuffer): void {
    if (typeof data !== 'string') {
      let f: PaneOutputFrame;
      try {
        f = decodePaneOutput(data);
      } catch {
        // The gateway counted the data bytes when it sent them; acknowledge
        // what a lenient parse finds so the count drains.
        this.processed(undecodableDataLength(data));
        return;
      }
      this.events.onOutput(f);
      return;
    }
    let m: Message;
    try {
      m = JSON.parse(data) as Message;
    } catch {
      return;
    }
    if (m.type === 'web_welcome') {
      this.onWelcome(m.payload as WebWelcome);
      return;
    }
    // After a resync the page re-attaches on the same daemon connection, and
    // a workspace_state the daemon queued for it before the new hello still
    // arrives first. The first state applied after a reconnect resets every
    // terminal, so applying that stale one would let live bytes land before
    // the replay. The daemon handles one connection's frames in order and
    // answers hello before it reads the attach, so states are held back until
    // the answer to this socket's hello (hello_resp, or an error naming it).
    if (this.helloId !== '' && m.id === this.helloId && (m.type === 'hello_resp' || m.type === 'error')) {
      this.helloAnswered = true;
    }
    if (m.type === 'workspace_state' && !this.helloAnswered) return;
    this.events.onMessage(m);
  }

  private onWelcome(w: WebWelcome): void {
    this.clientId = w.client_id;
    this.storage.setItem(CLIENT_ID_KEY, w.client_id);
    // A second welcome on one socket (the gateway renewed an id the daemon
    // refused as in use) starts a new hello, and states wait for its answer.
    this.helloId = `hello-${++this.seq}`;
    this.helloAnswered = false;
    this.sendRaw({
      type: 'hello',
      id: this.helloId,
      payload: {
        kind: 'web',
        proto: 1,
        client_id: w.client_id,
        version: w.version,
        pid: 0,
        exe: 'browser',
        uptime_ms: 0,
      },
    });
    const sz = this.sizes();
    const reattach = this.attaches > 0;
    this.attaches++;
    const payload: Record<string, unknown> = {
      cols: sz.cols,
      rows: sz.rows,
      client_id: this.clientId,
      reattach,
    };
    if (sz.winCols > 0 && sz.winRows > 0) {
      payload.win_cols = sz.winCols;
      payload.win_rows = sz.winRows;
    }
    this.sendRaw({ type: 'attach', id: `attach-${++this.seq}`, payload });
    this.events.onWelcome(w);
  }

  private onSocketClosed(code: number, reason: string): void {
    // Acks belong to the connection that carried the bytes; a new one starts
    // its count at zero.
    this.unacked = 0;
    if (this.ackTimer !== null) {
      this.clock.clearTimeout(this.ackTimer);
      this.ackTimer = null;
    }
    if (this.stableTimer !== null) {
      this.clock.clearTimeout(this.stableTimer);
      this.stableTimer = null;
    }
    if (this.stopped) return;
    if (code === 1008) {
      if (reason === 'login required') this.storage.removeItem(LOGIN_KEY);
      this.events.onClosed(code, reason, false);
      return;
    }
    switch (code) {
      case CLOSE.resync:
        this.events.onReconnecting();
        this.events.onClosed(code, reason, true);
        this.connect();
        return;
      case CLOSE.tokenRefused:
      case CLOSE.versionMismatch:
      case CLOSE.byAgent:
      case CLOSE.goingAway:
        this.events.onClosed(code, reason, false);
        return;
      default:
        // 4002, 4003 and any other code (1006 after a network drop).
        this.events.onReconnecting();
        this.events.onClosed(code, reason, true);
        this.scheduleReconnect();
    }
  }

  private scheduleReconnect(): void {
    const jitter = 1 + (this.random() * 2 - 1) * JITTER;
    const delay = Math.round(this.backoff * jitter);
    this.backoff = Math.min(this.backoff * 2, BACKOFF_MAX_MS);
    this.reconnectTimer = this.clock.setTimeout(() => {
      this.reconnectTimer = null;
      if (!this.stopped) this.connect();
    }, delay);
  }

  private flushAck(): void {
    if (this.ackTimer !== null) {
      this.clock.clearTimeout(this.ackTimer);
      this.ackTimer = null;
    }
    const bytes = this.unacked;
    this.unacked = 0;
    if (bytes > 0) this.sendRaw({ type: 'web_ack', payload: { bytes } });
  }

  private sendRaw(m: Message): void {
    if (!this.socket || !this.opened) return;
    this.socket.send(JSON.stringify(m));
  }

  private cancelTimers(): void {
    for (const h of [this.reconnectTimer, this.stableTimer, this.ackTimer]) {
      if (h !== null) this.clock.clearTimeout(h);
    }
    this.reconnectTimer = this.stableTimer = this.ackTimer = null;
  }
}
