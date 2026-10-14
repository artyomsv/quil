import { describe, expect, it } from 'vitest';
import { KIND_LABELS, LOG_TABS, logBody } from './logs';

// Built from code points: a raw bidi override or ESC in the source would be
// invisible to a reader.
const RLO = String.fromCodePoint(0x202e);
const ESC = String.fromCodePoint(0x1b);
const CSI = String.fromCodePoint(0x9b);

describe('logs', () => {
  it('has the four tabs, daemon named for --connect', () => {
    expect(LOG_TABS(false).map((t) => t.name)).toEqual(['client', 'daemon', 'mcp', 'web']);
    expect(LOG_TABS(true).find((t) => t.name === 'daemon')?.label).toBe('Daemon (this machine)');
  });

  it('sanitizes the text: bidi and CSI go, tab and newline stay', () => {
    const b = logBody({
      ok: true,
      value: { text: `a${RLO}b\tc\n${ESC}[31mred${CSI}`, truncated: false, size: 1, mtime: '', missing: false },
    });
    expect(b.text).toBe('ab\tc\n[31mred');
    expect(b.note).toBe('');
  });

  it('says when there is no log yet, and passes refusals through', () => {
    expect(logBody({ ok: true, value: { text: '', truncated: false, size: 0, mtime: '', missing: true } }).note).toBe('No log file yet.');
    expect(logBody({ ok: false, status: 403, error: 'needs full rights' }).note).toBe('needs full rights');
  });

  it('labels change kinds', () => {
    expect(KIND_LABELS.added).toBe('Added');
    expect(KIND_LABELS.fixed).toBe('Fixed');
  });
});
