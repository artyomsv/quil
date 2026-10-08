import { describe, expect, it } from 'vitest';
import { sanitizeBlock, sanitizeRemoteText } from './sanitize';

describe('sanitizeRemoteText', () => {
  it('drops escapes, C1 and bidi overrides, keeps printable text', () => {
    const esc = String.fromCharCode(0x1b);
    const csi = String.fromCharCode(0x9b);
    const rlo = String.fromCharCode(0x202e);
    const isolate = String.fromCharCode(0x2066);
    expect(sanitizeRemoteText(`${esc}[31mred${esc}[0m`)).toBe('[31mred[0m');
    expect(sanitizeRemoteText(`a${csi}b`)).toBe('ab');
    expect(sanitizeRemoteText(`abc${rlo}gpj.exe`)).toBe('abcgpj.exe');
    expect(sanitizeRemoteText(`x${isolate}y`)).toBe('xy');
    expect(sanitizeRemoteText('tab\there')).toBe('tab here');
    expect(sanitizeRemoteText('Grüße 名前 🚀')).toBe('Grüße 名前 🚀');
  });
});

describe('sanitizeBlock', () => {
  it('keeps line breaks and tabs', () => {
    expect(sanitizeBlock('a\nb\tc')).toBe('a\nb\tc');
  });
  it('drops the controls the one-line rule drops', () => {
    const esc = String.fromCharCode(0x1b);
    const bel = String.fromCharCode(0x07);
    const csi = String.fromCharCode(0x9b);
    const rlo = String.fromCharCode(0x202e);
    const cr = String.fromCharCode(0x0d);
    expect(sanitizeBlock(`x${esc}[31my${bel}${csi}z${rlo}w${cr}`)).toBe('x[31myzw');
  });
  it('keeps printable non-ASCII', () => {
    expect(sanitizeBlock('Grüße 名前 🚀')).toBe('Grüße 名前 🚀');
  });
});
