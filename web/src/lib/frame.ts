import type { PaneOutputFrame } from './protocol';
export type { PaneOutputFrame };

const KIND_PANE_OUTPUT = 1;
const FLAG_GHOST = 1;
export const FRAME_HEADER = 11;
const textDecoder = new TextDecoder('utf-8', { fatal: true });

// decodePaneOutput reads the gateway's binary pane_output frame: kind, flags
// (bit 0 = ghost replay), generation as big-endian uint64, pane id length,
// pane id, raw terminal bytes. The layout is pinned by vectors shared with
// the Go encoder.
export function decodePaneOutput(buf: ArrayBuffer): PaneOutputFrame {
  const view = new DataView(buf);
  if (buf.byteLength < FRAME_HEADER) throw new Error('pane output frame too short');
  if (view.getUint8(0) !== KIND_PANE_OUTPUT) throw new Error('unknown binary frame kind');
  const ghost = (view.getUint8(1) & FLAG_GHOST) !== 0;
  const generation = view.getBigUint64(2, false);
  const n = view.getUint8(10);
  if (n === 0 || buf.byteLength < FRAME_HEADER + n) throw new Error('bad pane id length');
  const paneId = textDecoder.decode(new Uint8Array(buf, FRAME_HEADER, n));
  const data = new Uint8Array(buf, FRAME_HEADER + n);
  return { paneId, ghost, generation, data };
}

// undecodableDataLength says how many data bytes a frame that failed to
// decode still carried, for acknowledging them: the length after the header
// and the pane id, or 0 when even the header is unusable.
export function undecodableDataLength(buf: ArrayBuffer): number {
  if (buf.byteLength < FRAME_HEADER) return 0;
  const n = new DataView(buf).getUint8(10);
  if (FRAME_HEADER + n > buf.byteLength) return 0;
  return buf.byteLength - FRAME_HEADER - n;
}
