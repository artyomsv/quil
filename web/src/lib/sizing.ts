import type { Message, PaneState, PaneSize } from './protocol';

// The smallest window, in cells, that gets laid out; below it the client
// reports 0x0 and the daemon keeps its own size.
export const MIN_WIN_COLS = 40;
export const MIN_WIN_ROWS = 10;

const MIN_FONT = 6;
const MAX_FONT = 24;
const DEFAULT_FONT = 14;

// A follower defers to the size master's grid. A read-only tab is always a
// follower. A master named by a marker such as "reserved-for-local:" is
// someone else too.
export function isFollower(sizeMaster: string | undefined, myId: string, readOnly: boolean): boolean {
  if (readOnly) return true;
  if (!sizeMaster) return false;
  return sizeMaster !== myId;
}

// The largest font size, in px, at which a grid of gridCols x gridRows cells
// fits the box. cellWPerPx and cellHPerPx are a cell's size per px of font.
export function fitFontSize(
  gridCols: number,
  gridRows: number,
  boxW: number,
  boxH: number,
  cellWPerPx: number,
  cellHPerPx: number,
): number {
  if (gridCols < 1 || gridRows < 1 || cellWPerPx <= 0 || cellHPerPx <= 0) return DEFAULT_FONT;
  const fit = Math.floor(Math.min(boxW / (gridCols * cellWPerPx), boxH / (gridRows * cellHPerPx)));
  return Math.min(MAX_FONT, Math.max(MIN_FONT, fit));
}

// The whole cells that fit a box, at least 1x1.
export function gridFor(boxW: number, boxH: number, cellW: number, cellH: number): { cols: number; rows: number } {
  if (cellW <= 0 || cellH <= 0) return { cols: 1, rows: 1 };
  return { cols: Math.max(1, Math.floor(boxW / cellW)), rows: Math.max(1, Math.floor(boxH / cellH)) };
}

// The whole cells that fit the viewport; 0x0 below the floor.
export function windowCells(viewW: number, viewH: number, cellW: number, cellH: number): { cols: number; rows: number } {
  if (cellW <= 0 || cellH <= 0) return { cols: 0, rows: 0 };
  const cols = Math.floor(viewW / cellW);
  const rows = Math.floor(viewH / cellH);
  if (cols < MIN_WIN_COLS || rows < MIN_WIN_ROWS) return { cols: 0, rows: 0 };
  return { cols, rows };
}

export interface SizerInput {
  myId: string;
  readOnly: boolean;
  sizeMaster: string | undefined;
  paintable: boolean;
  // The grid each visible pane would take if this tab sized it.
  visible: Map<string, { cols: number; rows: number }>;
  // A workspace_state has named this tab as the size master.
  masterConfirmed: boolean;
}

// Sends resize_panes only as a confirmed master with a paintable viewport,
// one batch holding the panes whose size changed, and reports the window to
// the daemon on change.
export class Sizer {
  private sent = new Map<string, { cols: number; rows: number }>();
  private lastGeometry: { cols: number; rows: number } | undefined;
  private readOnly = false;

  constructor(private readonly send: (m: Message) => void) {}

  update(input: SizerInput): void {
    this.readOnly = input.readOnly;
    if (
      isFollower(input.sizeMaster, input.myId, input.readOnly) ||
      !input.paintable ||
      !input.masterConfirmed
    ) {
      // Whatever was sent before may no longer stand once the master
      // changes, so every pane is owed a fresh send on the way back.
      this.sent.clear();
      return;
    }
    const panes: { pane_id: string; cols: number; rows: number }[] = [];
    for (const [paneId, size] of input.visible) {
      if (size.cols < 1 || size.rows < 1) continue;
      const prev = this.sent.get(paneId);
      if (prev && prev.cols === size.cols && prev.rows === size.rows) continue;
      panes.push({ pane_id: paneId, cols: size.cols, rows: size.rows });
    }
    if (panes.length === 0) return;
    for (const p of panes) this.sent.set(p.pane_id, { cols: p.cols, rows: p.rows });
    this.send({ type: 'resize_panes', payload: { panes } });
  }

  // Sent from every writable tab, follower or not, whenever it changes.
  geometry(cols: number, rows: number): void {
    if (this.readOnly) return;
    if (this.lastGeometry && this.lastGeometry.cols === cols && this.lastGeometry.rows === rows) return;
    this.lastGeometry = { cols, rows };
    this.send({ type: 'client_geometry', payload: { cols, rows } });
  }
}

// The daemon's size per pane, from workspace_state and pane_sizes frames. A
// frame is adopted only when its size_seq is at least the one held, so a
// broadcast racing a newer pane_sizes cannot undo it. forget() on reconnect:
// a restarted daemon numbers from zero.
export class DaemonSizes {
  private sizes = new Map<string, { cols: number; rows: number; seq: number }>();

  fromState(panes: PaneState[]): void {
    const live = new Set<string>();
    for (const p of panes) {
      live.add(p.id);
      if (p.cols && p.rows) this.adopt(p.id, p.cols, p.rows, p.size_seq ?? 0);
    }
    for (const id of [...this.sizes.keys()]) if (!live.has(id)) this.sizes.delete(id);
  }

  fromPaneSizes(sizes: PaneSize[]): void {
    for (const s of sizes) if (s.cols && s.rows) this.adopt(s.pane_id, s.cols, s.rows, s.size_seq ?? 0);
  }

  get(paneId: string): { cols: number; rows: number } | undefined {
    const s = this.sizes.get(paneId);
    return s ? { cols: s.cols, rows: s.rows } : undefined;
  }

  forget(): void {
    this.sizes.clear();
  }

  private adopt(paneId: string, cols: number, rows: number, seq: number): void {
    const held = this.sizes.get(paneId);
    if (held && seq < held.seq) return;
    this.sizes.set(paneId, { cols, rows, seq });
  }
}
