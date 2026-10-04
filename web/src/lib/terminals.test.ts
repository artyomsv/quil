import { describe, expect, it } from 'vitest';
import type { PaneOutputFrame } from './protocol';
import { TerminalStore, type TermLike } from './terminals';

type Op = ['write', string] | ['reset'] | ['resize', number, number];

class FakeTerm implements TermLike {
  ops: Op[] = [];
  disposed = false;
  // When set, write() parks its done callback here instead of finishing.
  held: Array<() => void> | null = null;
  // When set, write() throws after recording the op.
  throwOnWrite = false;

  write(data: Uint8Array, done: () => void): void {
    this.ops.push(['write', new TextDecoder().decode(data)]);
    if (this.throwOnWrite) throw new Error('write failed');
    if (this.held) this.held.push(done);
    else queueMicrotask(done);
  }
  reset(): void {
    this.ops.push(['reset']);
  }
  resize(cols: number, rows: number): void {
    this.ops.push(['resize', cols, rows]);
  }
  dispose(): void {
    this.disposed = true;
  }
}

const frame = (paneId: string, text: string, generation: bigint, ghost = false): PaneOutputFrame => ({
  paneId,
  ghost,
  generation,
  data: new TextEncoder().encode(text),
});

const flush = () => new Promise<void>((r) => setTimeout(r, 0));

function setup() {
  const terms = new Map<string, FakeTerm>();
  const acks: Array<[number, number]> = [];
  let epoch = 1;
  const store = new TerminalStore(
    (id) => {
      const t = new FakeTerm();
      terms.set(id, t);
      return t;
    },
    (bytes, e) => acks.push([bytes, e]),
    () => epoch,
  );
  store.sync(['p1']);
  return {
    store,
    terms,
    acks,
    term: terms.get('p1')!,
    setEpoch: (e: number) => {
      epoch = e;
    },
  };
}

describe('TerminalStore', () => {
  it('writes ghost history and the first live generation with no reset', async () => {
    const { store, term } = setup();
    store.output(frame('p1', 'old', 0n, true));
    store.output(frame('p1', 'live', 5n));
    await flush();
    expect(term.ops).toEqual([['write', 'old'], ['write', 'live']]);
  });

  it('resets before the data of a higher generation', async () => {
    const { store, term } = setup();
    store.output(frame('p1', 'a', 5n));
    store.output(frame('p1', 'b', 6n));
    await flush();
    expect(term.ops).toEqual([['write', 'a'], ['reset'], ['write', 'b']]);
  });

  it('drops a lower generation but still credits its bytes', async () => {
    const { store, term, acks } = setup();
    store.output(frame('p1', 'new', 6n));
    store.output(frame('p1', 'stale', 5n));
    await flush();
    expect(term.ops).toEqual([['write', 'new']]);
    expect(acks).toContainEqual([5, 1]);
  });

  it('drops and credits output while reconnecting, then resets once on the first state', async () => {
    const { store, term, acks } = setup();
    store.output(frame('p1', 'x', 4n));
    await flush();
    store.reconnecting();
    store.output(frame('p1', 'lost', 4n));
    expect(acks).toContainEqual([4, 1]);
    store.stateApplied(false);
    store.stateApplied(false);
    await flush();
    expect(term.ops).toEqual([['write', 'x'], ['reset']]);
    // generations were forgotten: 2 is a baseline, not a reset
    store.output(frame('p1', 'y', 2n));
    await flush();
    expect(term.ops).toEqual([['write', 'x'], ['reset'], ['write', 'y']]);
  });

  it('resets every terminal on a new run without reconnecting', async () => {
    const { store, terms } = setup();
    store.sync(['p1', 'p2']);
    store.stateApplied(true);
    await flush();
    expect(terms.get('p1')!.ops).toEqual([['reset']]);
    expect(terms.get('p2')!.ops).toEqual([['reset']]);
  });

  it('ignores a state that is neither a reconnect nor a new run', async () => {
    const { store, term } = setup();
    store.stateApplied(false);
    await flush();
    expect(term.ops).toEqual([]);
  });

  it('parses output for a pane nobody shows', async () => {
    const { store, term } = setup();
    store.output(frame('p1', 'hidden', 1n));
    await flush();
    expect(term.ops).toEqual([['write', 'hidden']]);
  });

  it('shows only the new run after a pane restarted while hidden', async () => {
    const { store, term } = setup();
    store.output(frame('p1', 'run3', 3n));
    store.output(frame('p1', 'run4', 4n));
    await flush();
    expect(term.ops).toEqual([['write', 'run3'], ['reset'], ['write', 'run4']]);
  });

  it('keeps write, reset, write in order when the first done is late', async () => {
    const { store, term } = setup();
    term.held = [];
    store.output(frame('p1', 'A', 1n));
    store.output(frame('p1', 'B', 2n));
    await flush();
    expect(term.ops).toEqual([['write', 'A']]);
    term.held!.shift()!();
    await flush();
    expect(term.ops).toEqual([['write', 'A'], ['reset'], ['write', 'B']]);
  });

  it('disposes a departed pane and creates new ones on sync', () => {
    const { store, terms, term } = setup();
    store.sync(['p2']);
    expect(term.disposed).toBe(true);
    expect(store.get('p1')).toBeUndefined();
    expect(store.get('p2')).toBe(terms.get('p2'));
  });

  it('follows the same generation rules for a ghost frame with a non-zero generation', async () => {
    const { store, term, acks } = setup();
    store.output(frame('p1', 'a', 5n, true));
    store.output(frame('p1', 'b', 6n, true));
    store.output(frame('p1', 'c', 5n, true));
    await flush();
    expect(term.ops).toEqual([['write', 'a'], ['reset'], ['write', 'b']]);
    expect(acks).toContainEqual([1, 1]);
    expect(acks).toHaveLength(3);
  });

  it('acknowledges queued frames once when the pane is disposed mid-chain', async () => {
    const { store, term, acks } = setup();
    term.held = [];
    store.output(frame('p1', 'AA', 1n));
    store.output(frame('p1', 'BBB', 2n));
    await flush();
    expect(acks).toEqual([]);
    store.sync([]);
    expect(acks).toEqual([[2, 1], [3, 1]]);
    // the late done of the parked write must not acknowledge again
    term.held.shift()!();
    await flush();
    expect(acks).toHaveLength(2);
    // the queued reset and write never touched the disposed terminal
    expect(term.ops).toEqual([['write', 'AA']]);
  });

  it('acknowledges a frame whose write throws and still writes the next one', async () => {
    const { store, term, acks } = setup();
    term.throwOnWrite = true;
    store.output(frame('p1', 'bad', 1n));
    await flush();
    expect(acks).toEqual([[3, 1]]);
    term.throwOnWrite = false;
    store.output(frame('p1', 'good', 1n));
    await flush();
    expect(term.ops).toEqual([['write', 'bad'], ['write', 'good']]);
    expect(acks).toEqual([[3, 1], [4, 1]]);
  });

  it('survives a throwing terminal on reset', async () => {
    const { store, term, acks } = setup();
    term.reset = () => {
      throw new Error('reset failed');
    };
    store.output(frame('p1', 'a', 1n));
    store.output(frame('p1', 'b', 2n));
    store.output(frame('p1', 'c', 2n));
    await flush();
    expect(acks).toEqual([[1, 1], [1, 1], [1, 1]]);
    expect(term.ops).toEqual([['write', 'a'], ['write', 'c']]);
  });

  it('credits an unknown pane', () => {
    const { store, acks } = setup();
    store.output(frame('nope', 'abc', 1n));
    expect(acks).toEqual([[3, 1]]);
  });

  it('credits bytes only after done', async () => {
    const { store, term, acks } = setup();
    term.held = [];
    store.output(frame('p1', 'abcd', 1n));
    await flush();
    expect(acks).toEqual([]);
    term.held!.shift()!();
    expect(acks).toEqual([[4, 1]]);
  });

  it('credits a write with the epoch it arrived under, not the one at finish', async () => {
    const { store, term, acks, setEpoch } = setup();
    term.held = [];
    store.output(frame('p1', 'abcd', 1n));
    await flush();
    setEpoch(2);
    term.held!.shift()!();
    expect(acks).toEqual([[4, 1]]);
  });

  it('resizes a hidden terminal after the bytes before it and before the bytes after it', async () => {
    const { store, term } = setup();
    term.held = [];
    store.output(frame('p1', 'before', 1n));
    store.resize('p1', 120, 40);
    store.output(frame('p1', '\x1b[35;100Hmark', 1n));
    await flush();
    // The first write has not finished parsing: the resize waits for it.
    expect(term.ops).toEqual([['write', 'before']]);
    term.held.shift()!();
    await flush();
    expect(term.ops).toEqual([['write', 'before'], ['resize', 120, 40], ['write', '\x1b[35;100Hmark']]);
  });

  it('queues a size once and skips a repeat of it', async () => {
    const { store, term } = setup();
    store.resize('p1', 120, 40);
    store.resize('p1', 120, 40);
    store.resize('p1', 0, 40);
    store.resize('nope', 120, 40);
    store.resize('p1', 100, 30);
    await flush();
    expect(term.ops).toEqual([['resize', 120, 40], ['resize', 100, 30]]);
  });

  it('wipes live output from the reconnect gap at the first replayed frame', async () => {
    const { store, term, acks } = setup();
    store.output(frame('p1', 'old', 3n));
    await flush();
    store.reconnecting();
    // A broadcast state arrives before the attach's own one, then live output.
    store.stateApplied(false);
    store.output(frame('p1', 'gap', 3n));
    // The attach's state, its replay in two chunks, then held live output.
    store.stateApplied(false);
    store.output(frame('p1', 'hist', 0n, true));
    store.output(frame('p1', 'ory', 0n, true));
    store.output(frame('p1', 'next', 3n));
    await flush();
    expect(term.ops).toEqual([
      ['write', 'old'],
      ['reset'],
      ['write', 'gap'],
      ['reset'],
      ['write', 'hist'],
      ['write', 'ory'],
      ['write', 'next'],
    ]);
    // Every byte is acknowledged, the wiped ones included.
    expect(acks.reduce((n, [b]) => n + b, 0)).toBe(3 + 3 + 4 + 3 + 4);
  });

  it('forgets the gap output generation at the replay, so a restart keeps the replay', async () => {
    const { store, term } = setup();
    store.reconnecting();
    store.stateApplied(false);
    // Live output from the old run reaches the gap, then the pane restarts
    // before the attach: its replay and live output carry the new run.
    store.output(frame('p1', 'gap', 3n));
    store.stateApplied(false);
    store.output(frame('p1', 'hist', 0n, true));
    store.output(frame('p1', 'live', 4n));
    await flush();
    expect(term.ops).toEqual([
      ['reset'],
      ['write', 'gap'],
      ['reset'],
      ['write', 'hist'],
      ['write', 'live'],
    ]);
  });

  it('keeps the state-time reset alone for a pane with no replay', async () => {
    const { store, term } = setup();
    store.reconnecting();
    store.stateApplied(false);
    store.output(frame('p1', 'live', 2n));
    await flush();
    expect(term.ops).toEqual([['reset'], ['write', 'live']]);
  });

  it('does not reset for a replay outside a reconnect', async () => {
    const { store, term } = setup();
    store.output(frame('p1', 'a', 0n, true));
    store.output(frame('p1', 'b', 0n, true));
    await flush();
    expect(term.ops).toEqual([['write', 'a'], ['write', 'b']]);
  });
});
