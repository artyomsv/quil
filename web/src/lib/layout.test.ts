import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { displayLayout, paneRects } from './layout';
import type { SerializedNode } from './protocol';

interface Vector {
  name: string;
  stored: SerializedNode | null;
  panes: string[];
  template_layout: string;
  template_main: string;
  want: SerializedNode | null;
}

const vectors: Vector[] = JSON.parse(
  readFileSync(new URL('../../../internal/tui/testdata/layout_vectors.json', import.meta.url), 'utf8'),
);

describe('displayLayout matches the Go DisplayLayout vectors', () => {
  for (const v of vectors) {
    it(v.name, () => {
      const before = JSON.stringify(v.stored);
      const got = displayLayout(v.stored ?? undefined, v.panes, v.template_layout, v.template_main);
      expect(got ?? null).toEqual(v.want);
      expect(JSON.stringify(v.stored)).toBe(before);
    });
  }
});

describe('displayLayout split normalisation', () => {
  it('gives an inner node with no split the side-by-side default', () => {
    const got = displayLayout({ ratio: 0.5, left: { pane_id: 'a' }, right: { pane_id: 'b' } }, ['a', 'b'], '', '');
    expect(got).toEqual({ split: 0, ratio: 0.5, left: { pane_id: 'a' }, right: { pane_id: 'b' } });
  });
});

describe('paneRects', () => {
  it('splits side by side and stacked', () => {
    const r = paneRects({
      ratio: 0.25,
      left: { pane_id: 'a' },
      right: { split: 1, ratio: 0.5, left: { pane_id: 'b' }, right: { pane_id: 'c' } },
    });
    expect(r.get('a')).toEqual({ x: 0, y: 0, w: 0.25, h: 1 });
    expect(r.get('b')).toEqual({ x: 0.25, y: 0, w: 0.75, h: 0.5 });
    expect(r.get('c')).toEqual({ x: 0.25, y: 0.5, w: 0.75, h: 0.5 });
  });

  it('reads a missing or zero ratio as one half', () => {
    const r = paneRects({ split: 1, ratio: 0, left: { pane_id: 'a' }, right: { pane_id: 'b' } });
    expect(r.get('a')).toEqual({ x: 0, y: 0, w: 1, h: 0.5 });
    expect(r.get('b')).toEqual({ x: 0, y: 0.5, w: 1, h: 0.5 });
  });

  it('gives a lone leaf the whole area and an empty tree nothing', () => {
    expect(paneRects({ pane_id: 'a' }).get('a')).toEqual({ x: 0, y: 0, w: 1, h: 1 });
    expect(paneRects(undefined).size).toBe(0);
  });
});
