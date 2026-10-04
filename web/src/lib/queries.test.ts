import { createRequire } from 'node:module';
import type { IParser } from '@xterm/xterm';
import { describe, expect, it } from 'vitest';
import { swallowQueries } from './queries';

// Loaded through Node's require: the package's "module" field names a file
// it does not ship, so an ESM import would resolve to nothing.
const { Terminal } = createRequire(import.meta.url)('@xterm/headless') as typeof import('@xterm/headless');

// Every query xterm's own parser answers: DA1, DA2, DSR, cursor position,
// the private DSR, ANSI and private mode requests, and a setting request.
const QUERIES = ['\x1b[c', '\x1b[>c', '\x1b[5n', '\x1b[6n', '\x1b[?6n', '\x1b[4$p', '\x1b[?2004$p', '\x1bP$qm\x1b\\'];

// replies writes data through a real (headless) xterm and returns every
// onData the parse produced: what the page would send as pane_input.
async function replies(swallow: boolean, data: string): Promise<string[]> {
  const term = new Terminal({ allowProposedApi: true, cols: 80, rows: 24 });
  if (swallow) swallowQueries(term.parser as unknown as IParser);
  const out: string[] = [];
  term.onData((d) => out.push(d));
  await new Promise<void>((resolve) => term.write(data, resolve));
  const text = term.buffer.active.getLine(0)?.translateToString(true) ?? '';
  term.dispose();
  return [...out, `screen:${text}`];
}

describe('swallowQueries', () => {
  it('answers each query without it, so the test reaches them', async () => {
    for (const q of QUERIES) {
      const got = await replies(false, q);
      expect(got.length, JSON.stringify(q)).toBeGreaterThan(1);
    }
  });

  it('sends nothing for any replayed query', async () => {
    for (const q of QUERIES) expect(await replies(true, q), JSON.stringify(q)).toEqual(['screen:']);
    expect(await replies(true, QUERIES.join(''))).toEqual(['screen:']);
  });

  it('still prints the text around the queries', async () => {
    expect(await replies(true, 'a\x1b[6nb\x1b[cc')).toEqual(['screen:abc']);
  });

  it('drops color queries and lets color sets through', () => {
    const osc = new Map<number, (data: string) => boolean | Promise<boolean>>();
    const none = { dispose: () => {} };
    const parser = {
      registerCsiHandler: () => none,
      registerDcsHandler: () => none,
      registerEscHandler: () => none,
      registerOscHandler: (ident: number, cb: (data: string) => boolean | Promise<boolean>) => {
        osc.set(ident, cb);
        return none;
      },
    } as unknown as IParser;
    swallowQueries(parser);
    expect([...osc.keys()].sort((a, b) => a - b)).toEqual([4, 10, 11, 12]);
    expect(osc.get(11)?.('?')).toBe(true);
    expect(osc.get(10)?.('?;?')).toBe(true);
    expect(osc.get(4)?.('1;?')).toBe(true);
    expect(osc.get(11)?.('rgb:00/00/00')).toBe(false);
    expect(osc.get(4)?.('1;rgb:ff/00/00')).toBe(false);
  });
});
