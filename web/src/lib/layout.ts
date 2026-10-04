import type { SerializedNode } from './protocol';

const SIDE_BY_SIDE = 0;
const STACKED = 1;

// The tree to DRAW for a tab: the stored tree with leaves for departed panes
// removed and panes it lacks placed by the arrival rule, or the template
// layout for a template tab with no stored tree. A port of the TUI's own
// placement, held to it by the shared vectors. Never mutates stored; returns
// undefined only when panes is empty.
//
// The arrival rule splits the FIRST leaf in tree order, stacked, with the new
// pane below. Every inner node of the result carries an explicit split.
export function displayLayout(
  stored: SerializedNode | undefined,
  panes: string[],
  templateKeyword: string,
  templateMain: string,
): SerializedNode | undefined {
  if (panes.length === 0) return undefined;
  if (!stored && templateKeyword !== '') {
    const t = templateDisplay(templateKeyword, panes, templateMain);
    if (t) return t;
  }
  const listed = new Set(panes);
  let tree = pruneDisplay(cloneSerialized(stored), listed);
  const present = new Set<string>();
  collectDisplayPanes(tree, present);
  for (const id of panes) {
    if (present.has(id)) continue;
    present.add(id);
    if (!tree) {
      tree = { pane_id: id };
      continue;
    }
    const first = firstDisplayLeaf(tree);
    const existing: SerializedNode = { pane_id: first.pane_id };
    for (const k of Object.keys(first) as (keyof SerializedNode)[]) delete first[k];
    first.split = STACKED;
    first.ratio = 0.5;
    first.left = existing;
    first.right = { pane_id: id };
  }
  return tree;
}

// A deep copy; an inner node with no split gets the side-by-side default.
function cloneSerialized(n: SerializedNode | undefined): SerializedNode | undefined {
  if (!n) return undefined;
  const c: SerializedNode = {};
  if (n.pane_id) c.pane_id = n.pane_id;
  if (n.ratio) c.ratio = n.ratio;
  if (!n.pane_id) c.split = n.split ?? SIDE_BY_SIDE;
  else if (n.split !== undefined) c.split = n.split;
  const left = cloneSerialized(n.left);
  const right = cloneSerialized(n.right);
  if (left) c.left = left;
  if (right) c.right = right;
  return c;
}

// Drops leaves whose pane is not listed (and malformed inner nodes missing a
// child), promoting the surviving sibling.
function pruneDisplay(n: SerializedNode | undefined, listed: Set<string>): SerializedNode | undefined {
  if (!n) return undefined;
  if (n.pane_id) return listed.has(n.pane_id) ? n : undefined;
  const left = pruneDisplay(n.left, listed);
  const right = pruneDisplay(n.right, listed);
  if (!left && !right) return undefined;
  if (!left) return right;
  if (!right) return left;
  n.left = left;
  n.right = right;
  return n;
}

function collectDisplayPanes(n: SerializedNode | undefined, into: Set<string>): void {
  if (!n) return;
  if (n.pane_id) {
    into.add(n.pane_id);
    return;
  }
  collectDisplayPanes(n.left, into);
  collectDisplayPanes(n.right, into);
}

function firstDisplayLeaf(n: SerializedNode): SerializedNode {
  while (!n.pane_id && n.left) n = n.left;
  return n;
}

// The template layout, or undefined when the main pane is not listed: a
// template is not applied until its anchor exists, and panes are placed by
// the arrival rule meanwhile.
function templateDisplay(keyword: string, panes: string[], main: string): SerializedNode | undefined {
  let mainIdx = -1;
  panes.forEach((id, i) => {
    if (id === main) mainIdx = i;
  });
  if (mainIdx < 0) return undefined;
  return templateLayout(keyword, panes, mainIdx);
}

function templateLayout(keyword: string, panes: string[], main: number): SerializedNode {
  if (panes.length === 1) return { pane_id: panes[0] };
  switch (keyword) {
    case 'columns':
      return templateStack(panes, SIDE_BY_SIDE);
    case 'main-left':
    case 'main-top': {
      const rest = [...panes.slice(0, main), ...panes.slice(main + 1)];
      const dir = keyword === 'main-top' ? STACKED : SIDE_BY_SIDE;
      const other = keyword === 'main-top' ? SIDE_BY_SIDE : STACKED;
      return { split: dir, ratio: 0.5, left: { pane_id: panes[main] }, right: templateStack(rest, other) };
    }
    case 'grid': {
      // The left column fills top to bottom, then the right; an odd last pane
      // spans both columns underneath them.
      const pairs = Math.floor(panes.length / 2);
      const top: SerializedNode = {
        split: SIDE_BY_SIDE,
        ratio: 0.5,
        left: templateStack(panes.slice(0, pairs), STACKED),
        right: templateStack(panes.slice(pairs, 2 * pairs), STACKED),
      };
      if (panes.length % 2 === 0) return top;
      return { split: STACKED, ratio: pairs / (pairs + 1), left: top, right: { pane_id: panes[panes.length - 1] } };
    }
    default:
      return templateStack(panes, STACKED);
  }
}

function templateStack(panes: string[], dir: number): SerializedNode {
  if (panes.length === 1) return { pane_id: panes[0] };
  return { split: dir, ratio: 1 / panes.length, left: { pane_id: panes[0] }, right: templateStack(panes.slice(1), dir) };
}

// A pane's place as fractions of the pane area, 0..1.
export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

// Walks the tree: split 0 (or absent) divides the width, the left child
// taking w*ratio; split 1 divides the height. A ratio of 0 or absent means 0.5.
export function paneRects(tree: SerializedNode | undefined): Map<string, Rect> {
  const out = new Map<string, Rect>();
  const walk = (n: SerializedNode | undefined, r: Rect): void => {
    if (!n) return;
    if (n.pane_id) {
      out.set(n.pane_id, r);
      return;
    }
    if (!n.left || !n.right) {
      walk(n.left ?? n.right, r);
      return;
    }
    const ratio = n.ratio ? n.ratio : 0.5;
    if (n.split === STACKED) {
      const h1 = r.h * ratio;
      walk(n.left, { x: r.x, y: r.y, w: r.w, h: h1 });
      walk(n.right, { x: r.x, y: r.y + h1, w: r.w, h: r.h - h1 });
    } else {
      const w1 = r.w * ratio;
      walk(n.left, { x: r.x, y: r.y, w: w1, h: r.h });
      walk(n.right, { x: r.x + w1, y: r.y, w: r.w - w1, h: r.h });
    }
  };
  walk(tree, { x: 0, y: 0, w: 1, h: 1 });
  return out;
}
