import { describe, expect, it } from 'vitest';
import { bannerFor } from './banner';

describe('bannerFor', () => {
  it('shows nothing for a resync', () => {
    expect(bannerFor(4001, '', true)).toBeNull();
  });

  it('names each close code', () => {
    expect(bannerFor(4002, '', true)).toEqual({ text: 'This tab fell behind — reconnecting', retrying: true });
    expect(bannerFor(4003, '', true)).toEqual({ text: 'The daemon is unavailable — reconnecting', retrying: true });
    expect(bannerFor(4004, 'revoked', false)).toEqual({ text: 'Token refused: revoked', retrying: false });
    expect(bannerFor(4005, 'daemon 1.2.3', false)).toEqual({ text: 'Version mismatch: daemon 1.2.3', retrying: false });
    expect(bannerFor(4006, '', false)).toEqual({ text: 'Closed by an agent', retrying: false });
    expect(bannerFor(1001, 'going away', false)).toEqual({ text: 'The web server stopped', retrying: false });
  });

  it('never claims a retry for the permanent codes', () => {
    for (const code of [4004, 4005, 4006, 1001]) expect(bannerFor(code, '', true)?.retrying).toBe(false);
  });

  it('covers a network drop', () => {
    expect(bannerFor(1006, '', true)).toEqual({ text: 'Connection lost — reconnecting', retrying: true });
  });

  it('sanitizes the reason', () => {
    const esc = String.fromCodePoint(0x1b);
    const rlo = String.fromCodePoint(0x202e);
    expect(bannerFor(4004, `bad${esc}[31m${rlo}token`, false)?.text).toBe('Token refused: bad[31mtoken');
  });
});
