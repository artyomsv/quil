import { bytesToBase64, utf8Bytes } from './base64';
import { INPUT_CHUNK } from './connection';
import { NOT_CONNECTED, type Outcome } from './requests';

// The daemon's refusal text for a full pane input queue
// (ipc.PaneInputQueueFull; keep the two equal).
export const QUEUE_FULL_PREFIX = 'pane input queue is full';
export const RESEND_MS = 250;
export const PARTIAL = 'Paste may be partly delivered';
// A paste that ended before any chunk left the page.
export const NOT_SENT = 'Paste was not sent';

export interface PasteIO {
  // One id-bearing pane_input; resolves on its pane_input_resp.
  sendChunk(paneId: string, base64: string): Promise<Outcome>;
  // Id-less input, as 5a sends keystrokes.
  sendKeys(paneId: string, data: string): void;
  notice(text: string): void;
  sleep(ms: number): Promise<void>;
}

interface Current {
  paneId: string;
  ended: boolean;
  delivered: boolean;
  inFlight: boolean;
}

interface Job {
  paneId: string;
  data: string;
  // The UTF-8 bytes of a paste (input over INPUT_CHUNK bytes); null for keys.
  paste: Uint8Array | null;
}

// PasteFlow runs ONE paste at a time per socket with at most one unanswered
// chunk (spec §4.3). Delivered means QUEUED by the daemon, so a paste into a
// pane that stops reading fills its queue; the queue-full refusal and the
// gateway's busy refusal mean "wait and send the same chunk again". Anything
// typed while a paste runs waits behind it.
export class PasteFlow {
  private readonly queue: Job[] = [];
  private running = false;
  // The running paste: delivered once a chunk was answered as queued,
  // inFlight while a sent chunk waits for its answer (it may have arrived).
  private current: Current | null = null;

  constructor(private readonly io: PasteIO) {}

  input(paneId: string, data: string): void {
    // A string of n UTF-16 units is at most 3n UTF-8 bytes, so typed input
    // is never encoded twice.
    const bytes = data.length * 3 > INPUT_CHUNK ? utf8Bytes(data) : null;
    const paste = bytes !== null && bytes.length > INPUT_CHUNK ? bytes : null;
    if (!paste && !this.running && this.queue.length === 0) {
      this.io.sendKeys(paneId, data);
      return;
    }
    this.queue.push({ paneId, data: paste ? '' : data, paste });
    if (!this.running) void this.drain();
  }

  // reconnecting ends a running paste and drops what waited behind it.
  reconnecting(): void {
    this.queue.length = 0;
    this.endLost();
  }

  paneRestarted(paneId: string): void {
    if (this.current?.paneId === paneId) this.endLost();
  }

  // endLost ends a paste whose remaining chunks cannot go out. It says the
  // paste may be partly delivered only when some of it may have arrived.
  private endLost(): void {
    const c = this.current;
    if (c) this.end(c.delivered || c.inFlight ? PARTIAL : NOT_SENT);
  }

  private end(text: string): void {
    if (!this.current || this.current.ended) return;
    this.current.ended = true;
    this.io.notice(text);
  }

  private async drain(): Promise<void> {
    this.running = true;
    try {
      while (this.queue.length > 0) {
        const job = this.queue.shift() as Job;
        if (!job.paste) {
          this.io.sendKeys(job.paneId, job.data);
          continue;
        }
        await this.run({ ...job, paste: job.paste });
      }
    } finally {
      this.running = false;
      this.current = null;
    }
  }

  private async run(job: Job & { paste: Uint8Array }): Promise<void> {
    const cur: Current = { paneId: job.paneId, ended: false, delivered: false, inFlight: false };
    this.current = cur;
    const bytes = job.paste;
    let off = 0;
    while (off < bytes.length) {
      cur.inFlight = true;
      const out = await this.io.sendChunk(job.paneId, bytesToBase64(bytes.subarray(off, off + INPUT_CHUNK)));
      // A chunk that never left the page cannot have arrived.
      cur.inFlight = false;
      if (cur.ended) return;
      if (out.ok) {
        cur.delivered = true;
        off += INPUT_CHUNK;
        continue;
      }
      if (out.code === 'offline' && out.error === NOT_CONNECTED) {
        this.end(cur.delivered ? PARTIAL : NOT_SENT);
        return;
      }
      if (out.code === 'busy' || out.error.startsWith(QUEUE_FULL_PREFIX)) {
        await this.io.sleep(RESEND_MS);
        if (cur.ended) return;
        continue;
      }
      if (out.code === 'offline' || out.code === 'timeout') {
        this.end(PARTIAL);
        return;
      }
      this.end(`Paste stopped: ${out.error}`);
      return;
    }
  }
}
