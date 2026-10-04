import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { decodePaneOutput, undecodableDataLength } from './frame';

interface Vector {
  name: string;
  pane_id: string;
  ghost: boolean;
  generation: string;
  data_hex: string;
  frame_hex: string;
}

const vectors: Vector[] = JSON.parse(
  readFileSync(new URL('../../../internal/webgw/testdata/frame_vectors.json', import.meta.url), 'utf8'),
);

function hex(s: string): Uint8Array {
  const out = new Uint8Array(s.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(s.slice(i * 2, i * 2 + 2), 16);
  return out;
}

describe('decodePaneOutput', () => {
  it('has vectors', () => expect(vectors.length).toBeGreaterThan(0));
  for (const v of vectors) {
    it(v.name, () => {
      const f = decodePaneOutput(hex(v.frame_hex).buffer as ArrayBuffer);
      expect(f.paneId).toBe(v.pane_id);
      expect(f.ghost).toBe(v.ghost);
      expect(f.generation).toBe(BigInt(v.generation));
      expect(Array.from(f.data)).toEqual(Array.from(hex(v.data_hex)));
    });
  }
  it('rejects a pane id that is not valid UTF-8', () => {
    expect(() => decodePaneOutput(hex('0100000000000000000001ff41').buffer as ArrayBuffer)).toThrow();
    expect(undecodableDataLength(hex('0100000000000000000001ff41').buffer as ArrayBuffer)).toBe(1);
  });
  it('rejects a truncated frame', () => {
    expect(() => decodePaneOutput(hex('0100000000').buffer as ArrayBuffer)).toThrow();
    expect(() => decodePaneOutput(hex('0200000000000000000001' + '70').buffer as ArrayBuffer)).toThrow();
  });
});
