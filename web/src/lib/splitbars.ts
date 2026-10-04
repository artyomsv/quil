import type { Clock } from './connection';
import type { Rect } from './layout';
import type { SerializedNode } from './protocol';
import type { Outcome } from './requests';

const STACKED = 1;
export const CONFIRM_MS = 2000;

export interface Bar {
  // L/R steps from the root to the inner node; '' is the root.
  path: string;
  dir: 0 | 1;
  ratio: number;
  // The inner node's own rect, as fractions of the pane area.
  rect: Rect;
}

// splitBars lists the tree's inner nodes, walked like paneRects (a missing or
// zero ratio is 0.5), so each bar sits on the line paneRects draws.
export function splitBars(tree: SerializedNode | undefined): Bar[] {
  const out: Bar[] = [];
  const walk = (n: SerializedNode | undefined, r: Rect, path: string): void => {
    if (!n || n.pane_id) return;
    if (!n.left || !n.right) {
      walk(n.left ?? n.right, r, path + (n.left ? 'L' : 'R'));
      return;
    }
    const ratio = n.ratio ? n.ratio : 0.5;
    const dir = n.split === STACKED ? 1 : 0;
    out.push({ path, dir, ratio, rect: r });
    if (dir === 1) {
      const h1 = r.h * ratio;
      walk(n.left, { x: r.x, y: r.y, w: r.w, h: h1 }, `${path}L`);
      walk(n.right, { x: r.x, y: r.y + h1, w: r.w, h: r.h - h1 }, `${path}R`);
    } else {
      const w1 = r.w * ratio;
      walk(n.left, { x: r.x, y: r.y, w: w1, h: r.h }, `${path}L`);
      walk(n.right, { x: r.x + w1, y: r.y, w: r.w - w1, h: r.h }, `${path}R`);
    }
  };
  walk(tree, { x: 0, y: 0, w: 1, h: 1 }, '');
  return out;
}

export function clampRatio(r: number): number {
  return Math.min(0.95, Math.max(0.05, r));
}

function nodeAt(tree: SerializedNode | undefined, path: string): SerializedNode | undefined {
  let n = tree;
  for (const step of path) n = step === 'L' ? n?.left : n?.right;
  return n;
}

// withRatio returns a deep copy of tree whose node at path has ratio r.
export function withRatio(tree: SerializedNode, path: string, r: number): SerializedNode {
  const copy = JSON.parse(JSON.stringify(tree)) as SerializedNode;
  const n = nodeAt(copy, path);
  if (n) n.ratio = clampRatio(r);
  return copy;
}

// ratioFromPointer is where the pointer sits inside the bar's node, as the
// left/top child's share.
export function ratioFromPointer(bar: Bar, fx: number, fy: number): number {
  return clampRatio(bar.dir === 1 ? (fy - bar.rect.y) / bar.rect.h : (fx - bar.rect.x) / bar.rect.w);
}

export interface DragPreview {
  tabId: string;
  tree: SerializedNode;
}

type Phase = 'idle' | 'dragging' | 'waiting';

// SplitDrag is one browser tab's border drag: the tree and its layout_rev are
// pinned on pointer-down; a newer rev before release cancels the drag (the
// TUI does the same); release sends ONE id-bearing update_layout when the
// ratio moved. The preview stays until a state with a higher rev confirms it,
// the daemon refuses it (error stale) or 2 s pass. A second drag waits.
export class SplitDrag {
  preview: DragPreview | null = null;
  private phase: Phase = 'idle';
  private tabId = '';
  private pinnedRev = 0;
  private path = '';
  private base: SerializedNode | null = null;
  private ratio = 0;
  private startRatio = 0;
  private timer: unknown = null;
  // Numbers each released drag, so a refusal that arrives after its own
  // preview already reverted cannot end a later one.
  private sent = 0;
  onChange: () => void = () => {};

  constructor(
    private readonly clock: Clock,
    private readonly write: (tabId: string, layout: SerializedNode, baseRev: number) => Promise<Outcome>,
  ) {}

  begin(tabId: string, tree: SerializedNode, rev: number, path: string): boolean {
    if (this.phase !== 'idle') return false;
    const n = nodeAt(tree, path);
    if (!n || n.pane_id) return false;
    this.phase = 'dragging';
    this.tabId = tabId;
    this.pinnedRev = rev;
    this.path = path;
    this.base = tree;
    this.startRatio = this.ratio = n.ratio ? n.ratio : 0.5;
    return true;
  }

  move(r: number): void {
    if (this.phase !== 'dragging' || !this.base) return;
    this.ratio = clampRatio(r);
    this.preview = { tabId: this.tabId, tree: withRatio(this.base, this.path, this.ratio) };
    this.onChange();
  }

  end(): void {
    if (this.phase !== 'dragging' || !this.base) return;
    if (this.ratio === this.startRatio || !this.preview) {
      this.reset();
      return;
    }
    this.phase = 'waiting';
    const seq = ++this.sent;
    const layout = this.preview.tree;
    this.timer = this.clock.setTimeout(() => this.reset(), CONFIRM_MS);
    void this.write(this.tabId, layout, this.pinnedRev).then((o) => {
      if (!o.ok && this.phase === 'waiting' && seq === this.sent) this.reset();
    });
  }

  // cancel drops a drag still in progress; a released one waits for its
  // confirmation as usual.
  cancel(): void {
    if (this.phase === 'dragging') this.reset();
  }

  // stateArrived runs for every applied workspace_state, per tab.
  stateArrived(tabId: string, rev: number): void {
    if (tabId !== this.tabId || rev <= this.pinnedRev) return;
    if (this.phase === 'dragging' || this.phase === 'waiting') this.reset();
  }

  // linkLost ends any drag: the next socket starts with a fresh state, and a
  // pinned revision from the old one means nothing there.
  linkLost(): void {
    if (this.phase !== 'idle') this.reset();
  }

  private reset(): void {
    if (this.timer !== null) this.clock.clearTimeout(this.timer);
    this.timer = null;
    this.phase = 'idle';
    this.preview = null;
    this.base = null;
    this.onChange();
  }
}
