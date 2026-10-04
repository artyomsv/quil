import type { PaneOutputFrame } from './protocol';

export interface TermLike {
  // done runs after xterm has parsed the bytes.
  write(data: Uint8Array, done: () => void): void;
  reset(): void;
  resize(cols: number, rows: number): void;
  dispose(): void;
}

export type TermFactory = (paneId: string) => TermLike;

// A frame accepted for writing and not yet acknowledged.
interface Pending {
  bytes: number;
  epoch: number;
}

interface Slot {
  term: TermLike;
  generation: bigint; // 0n = no baseline yet
  chain: Promise<void>;
  disposed: boolean;
  pending: Set<Pending>;
  // The grid last queued by resize(); undefined until the first.
  size: { cols: number; rows: number } | undefined;
  // Set by the reset that follows a reconnect: the pane's first replayed
  // frame resets the terminal again, see stateApplied.
  awaitReplay: boolean;
}

// TerminalStore keeps one terminal per pane for the pane's whole life,
// visible or not: the daemon replays history only on attach, never on a tab
// switch, so a terminal that missed its pane's output could not recover it.
//
// Every frame it accepts (written or dropped) is acknowledged through
// processed() with its data length and the socket epoch read when the frame
// arrived. A write that finishes after a reconnect therefore carries the old
// epoch, and the connection ignores it instead of crediting the new socket.
export class TerminalStore {
  private slots = new Map<string, Slot>();
  private ignoring = false;

  constructor(
    private readonly factory: TermFactory,
    private readonly processed: (bytes: number, epoch: number) => void,
    private readonly epoch: () => number,
  ) {}

  // sync creates missing terminals and disposes those of departed panes.
  sync(paneIds: string[]): void {
    const want = new Set(paneIds);
    for (const [id, slot] of this.slots) {
      if (!want.has(id)) {
        // A disposed terminal may never call back, so every frame still
        // waiting on it is acknowledged now; the chain skips the rest.
        slot.disposed = true;
        for (const p of [...slot.pending]) this.finish(slot, p);
        try {
          slot.term.dispose();
        } catch {
          // already gone
        }
        this.slots.delete(id);
      }
    }
    for (const id of paneIds) {
      if (!this.slots.has(id)) {
        this.slots.set(id, {
          term: this.factory(id),
          generation: 0n,
          chain: Promise.resolve(),
          disposed: false,
          pending: new Set(),
          size: undefined,
          awaitReplay: false,
        });
      }
    }
  }

  // finish acknowledges a pending frame, once.
  private finish(slot: Slot, p: Pending): void {
    if (!slot.pending.delete(p)) return;
    this.processed(p.bytes, p.epoch);
  }

  get(paneId: string): TermLike | undefined {
    return this.slots.get(paneId)?.term;
  }

  // output applies the TUI's generation rules, keyed on the generation alone.
  // Generation 0 (replayed history) is written and leaves the tracked
  // generation alone. The first
  // live generation sets the baseline with no reset. A lower generation is
  // dropped. A higher one resets the terminal before its bytes are written.
  // Writes and resets for one pane run in order: a reset waits for every
  // earlier write to finish parsing.
  output(f: PaneOutputFrame): void {
    const slot = this.slots.get(f.paneId);
    const n = f.data.byteLength;
    const epoch = this.epoch();
    if (!slot || this.ignoring) {
      this.processed(n, epoch);
      return;
    }
    let reset = false;
    if (f.ghost && slot.awaitReplay) {
      slot.awaitReplay = false;
      reset = true;
    }
    if (f.generation !== 0n) {
      if (slot.generation === 0n) {
        slot.generation = f.generation;
      } else if (f.generation < slot.generation) {
        this.processed(n, epoch);
        return;
      } else if (f.generation > slot.generation) {
        slot.generation = f.generation;
        reset = true;
      }
    }
    const data = f.data.slice();
    const p: Pending = { bytes: n, epoch };
    slot.pending.add(p);
    this.enqueue(slot, () => {
      if (slot.disposed) {
        this.finish(slot, p);
        return Promise.resolve();
      }
      return new Promise<void>((resolve) => {
        try {
          if (reset) slot.term.reset();
          slot.term.write(data, () => {
            this.finish(slot, p);
            resolve();
          });
        } catch {
          // A terminal that throws must not stall the pane: acknowledge the
          // frame and let the next one run.
          this.finish(slot, p);
          resolve();
        }
      });
    });
  }

  // resize queues a grid change behind the pane's earlier writes, so bytes
  // that arrived before it are parsed at the old size and every later one at
  // the new size, whether or not the terminal is shown. Repeating the queued
  // size queues nothing.
  resize(paneId: string, cols: number, rows: number): void {
    const slot = this.slots.get(paneId);
    if (!slot || cols < 1 || rows < 1) return;
    if (slot.size && slot.size.cols === cols && slot.size.rows === rows) return;
    slot.size = { cols, rows };
    this.enqueue(slot, async () => {
      if (!slot.disposed) slot.term.resize(cols, rows);
    });
  }

  // enqueue appends a step to the pane's chain. The chain never stays
  // rejected, or every later step would be skipped.
  private enqueue(slot: Slot, step: () => Promise<void>): void {
    slot.chain = slot.chain.then(step).catch(() => {});
  }

  // reconnecting makes output() drop (and acknowledge) every frame until the
  // next stateApplied().
  reconnecting(): void {
    this.ignoring = true;
  }

  // stateApplied runs for each workspace_state. The first one after a
  // reconnect, or one that starts a new daemon run, resets every terminal and
  // forgets the generations, because the daemon replays history after it.
  //
  // That first state is not always the attach's own: a broadcast can arrive
  // between the daemon's answer to hello and its handling of attach, and live
  // output can follow it before the attach's replay. The replay holds that
  // output too, so each pane's first replayed frame resets its terminal
  // again. A pane the daemon replays nothing for keeps this reset alone.
  stateApplied(newRun: boolean): void {
    if (!this.ignoring && !newRun) return;
    this.ignoring = false;
    for (const slot of this.slots.values()) {
      slot.generation = 0n;
      slot.awaitReplay = true;
      this.enqueue(slot, async () => {
        if (!slot.disposed) slot.term.reset();
      });
    }
  }
}
