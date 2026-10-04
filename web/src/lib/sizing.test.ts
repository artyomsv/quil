import { describe, expect, it } from 'vitest';
import type { Message } from './protocol';
import {
  DaemonSizes,
  fitFontSize,
  gridFor,
  isFollower,
  MIN_WIN_COLS,
  MIN_WIN_ROWS,
  Sizer,
  windowCells,
  type SizerInput,
} from './sizing';

describe('isFollower', () => {
  it('is false with no master or with me as master', () => {
    expect(isFollower(undefined, 'me', false)).toBe(false);
    expect(isFollower('me', 'me', false)).toBe(false);
  });
  it('is true for another master or a reserved marker', () => {
    expect(isFollower('other', 'me', false)).toBe(true);
    expect(isFollower('reserved-for-local:abc', 'me', false)).toBe(true);
  });
  it('is always true when read-only', () => {
    expect(isFollower(undefined, 'me', true)).toBe(true);
    expect(isFollower('me', 'me', true)).toBe(true);
  });
});

describe('fitFontSize', () => {
  it('picks the largest font that fits both dimensions', () => {
    // 80 cols * 0.6 per px = 48 px of width per px of font; 24 rows * 1.2 = 28.8.
    expect(fitFontSize(80, 24, 960, 2000, 0.6, 1.2)).toBe(20);
    expect(fitFontSize(80, 24, 4000, 432, 0.6, 1.2)).toBe(15);
  });
  it('clamps to 6..24', () => {
    expect(fitFontSize(80, 24, 100, 100, 0.6, 1.2)).toBe(6);
    expect(fitFontSize(10, 5, 5000, 5000, 0.6, 1.2)).toBe(24);
  });
});

describe('gridFor and windowCells', () => {
  it('counts whole cells', () => {
    expect(gridFor(805, 410, 10, 20)).toEqual({ cols: 80, rows: 20 });
  });
  it('windowCells is 0x0 below the floor', () => {
    expect(windowCells(10 * (MIN_WIN_COLS - 1), 20 * 30, 10, 20)).toEqual({ cols: 0, rows: 0 });
    expect(windowCells(10 * 100, 20 * (MIN_WIN_ROWS - 1), 10, 20)).toEqual({ cols: 0, rows: 0 });
    expect(windowCells(10 * MIN_WIN_COLS, 20 * MIN_WIN_ROWS, 10, 20)).toEqual({ cols: 40, rows: 10 });
  });
});

function rig(): { sizer: Sizer; sent: Message[] } {
  const sent: Message[] = [];
  return { sizer: new Sizer((m) => sent.push(m)), sent };
}

function input(over: Partial<SizerInput> = {}): SizerInput {
  return {
    myId: 'me',
    readOnly: false,
    sizeMaster: 'me',
    paintable: true,
    masterConfirmed: true,
    visible: new Map([
      ['a', { cols: 80, rows: 24 }],
      ['b', { cols: 40, rows: 24 }],
    ]),
    ...over,
  };
}

describe('Sizer', () => {
  it('never resizes as a follower, whatever is visible', () => {
    const { sizer, sent } = rig();
    sizer.update(input({ sizeMaster: 'other' }));
    sizer.update(input({ readOnly: true }));
    expect(sent).toEqual([]);
  });

  it('still reports geometry as a follower, once per change', () => {
    const { sizer, sent } = rig();
    sizer.update(input({ sizeMaster: 'other' }));
    sizer.geometry(120, 40);
    sizer.geometry(120, 40);
    sizer.geometry(100, 40);
    expect(sent).toEqual([
      { type: 'client_geometry', payload: { cols: 120, rows: 40 } },
      { type: 'client_geometry', payload: { cols: 100, rows: 40 } },
    ]);
  });

  it('sends no geometry from a read-only tab', () => {
    const { sizer, sent } = rig();
    sizer.update(input({ readOnly: true }));
    sizer.geometry(120, 40);
    expect(sent).toEqual([]);
  });

  it('never resizes from an unpaintable viewport', () => {
    const { sizer, sent } = rig();
    sizer.update(input({ paintable: false }));
    expect(sent).toEqual([]);
  });

  it('waits for the master to be confirmed', () => {
    const { sizer, sent } = rig();
    sizer.update(input({ masterConfirmed: false }));
    expect(sent).toEqual([]);
    sizer.update(input());
    expect(sent).toEqual([
      {
        type: 'resize_panes',
        payload: {
          panes: [
            { pane_id: 'a', cols: 80, rows: 24 },
            { pane_id: 'b', cols: 40, rows: 24 },
          ],
        },
      },
    ]);
  });

  it('sends only what changed', () => {
    const { sizer, sent } = rig();
    sizer.update(input());
    sizer.update(input());
    expect(sent).toHaveLength(1);
    sizer.update(
      input({
        visible: new Map([
          ['a', { cols: 80, rows: 24 }],
          ['b', { cols: 50, rows: 24 }],
        ]),
      }),
    );
    expect(sent).toHaveLength(2);
    expect(sent[1]).toEqual({ type: 'resize_panes', payload: { panes: [{ pane_id: 'b', cols: 50, rows: 24 }] } });
  });

  it('turns from follower to master without a reload', () => {
    const { sizer, sent } = rig();
    sizer.update(input({ sizeMaster: 'other', masterConfirmed: false }));
    expect(sent).toEqual([]);
    sizer.update(input({ sizeMaster: 'me', masterConfirmed: true }));
    expect(sent).toHaveLength(1);
    expect(sent[0]?.type).toBe('resize_panes');
  });

  it('sends the newly visible panes of a tab switch in one batch', () => {
    const { sizer, sent } = rig();
    sizer.update(input());
    sizer.update(
      input({
        visible: new Map([
          ['c', { cols: 60, rows: 20 }],
          ['d', { cols: 60, rows: 20 }],
        ]),
      }),
    );
    expect(sent).toHaveLength(2);
    expect(sent[1]).toEqual({
      type: 'resize_panes',
      payload: {
        panes: [
          { pane_id: 'c', cols: 60, rows: 20 },
          { pane_id: 'd', cols: 60, rows: 20 },
        ],
      },
    });
  });

  it('resends every pane after losing and regaining the master', () => {
    const { sizer, sent } = rig();
    sizer.update(input());
    sizer.update(input({ sizeMaster: 'other' }));
    sizer.update(input());
    expect(sent).toHaveLength(2);
  });
});

describe('DaemonSizes', () => {
  const pane = (id: string, cols?: number, rows?: number, size_seq?: number) => ({
    id,
    tab_id: 't',
    cwd: '',
    cols,
    rows,
    size_seq,
  });

  it('keeps a newer pane_sizes size against an older state', () => {
    const d = new DaemonSizes();
    d.fromPaneSizes([{ pane_id: 'a', cols: 100, rows: 30, size_seq: 7 }]);
    d.fromState([pane('a', 80, 24, 6)]);
    expect(d.get('a')).toEqual({ cols: 100, rows: 30 });
    d.fromState([pane('a', 90, 28, 8)]);
    expect(d.get('a')).toEqual({ cols: 90, rows: 28 });
  });

  it('returns undefined for a pane with no size', () => {
    const d = new DaemonSizes();
    d.fromState([pane('a')]);
    expect(d.get('a')).toBeUndefined();
  });

  it('forgets a pane the state no longer lists', () => {
    const d = new DaemonSizes();
    d.fromState([pane('a', 80, 24, 1)]);
    d.fromState([pane('b', 80, 24, 1)]);
    expect(d.get('a')).toBeUndefined();
  });

  it('applies a seq-1 frame again after forget()', () => {
    const d = new DaemonSizes();
    d.fromPaneSizes([{ pane_id: 'a', cols: 100, rows: 30, size_seq: 7 }]);
    d.forget();
    expect(d.get('a')).toBeUndefined();
    d.fromPaneSizes([{ pane_id: 'a', cols: 70, rows: 20, size_seq: 1 }]);
    expect(d.get('a')).toEqual({ cols: 70, rows: 20 });
  });
});
