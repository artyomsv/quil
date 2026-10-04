import type { PaneOutputFrame } from './protocol';

export interface TermLike {
  // done runs after xterm has parsed the bytes.
  write(data: Uint8Array, done: () => void): void;
  reset(): void;
  resize(cols: number, rows: number): void;
  dispose(): void;
}

export type TermFactory = (paneId: string) => TermLike;

interface Slot {
  term: TermLike;
  generation: bigint; // 0n = no baseline yet
  chain: Promise<void>;
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
        slot.term.dispose();
        this.slots.delete(id);
      }
    }
    for (const id of paneIds) {
      if (!this.slots.has(id)) {
        this.slots.set(id, { term: this.factory(id), generation: 0n, chain: Promise.resolve() });
      }
    }
  }

  get(paneId: string): TermLike | undefined {
    return this.slots.get(paneId)?.term;
  }

  // output applies the TUI's generation rules. Ghost frames (replayed
  // history) are written and leave the tracked generation alone. The first
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
    if (!f.ghost && f.generation !== 0n) {
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
    slot.chain = slot.chain.then(
      () =>
        new Promise<void>((resolve) => {
          if (reset) slot.term.reset();
          slot.term.write(data, () => {
            this.processed(n, epoch);
            resolve();
          });
        }),
    );
  }

  // reconnecting makes output() drop (and acknowledge) every frame until the
  // next stateApplied().
  reconnecting(): void {
    this.ignoring = true;
  }

  // stateApplied runs for each workspace_state. The first one after a
  // reconnect, or one that starts a new daemon run, resets every terminal and
  // forgets the generations, because the daemon replays history after it.
  stateApplied(newRun: boolean): void {
    if (!this.ignoring && !newRun) return;
    this.ignoring = false;
    for (const slot of this.slots.values()) {
      slot.generation = 0n;
      slot.chain = slot.chain.then(() => slot.term.reset());
    }
  }
}
