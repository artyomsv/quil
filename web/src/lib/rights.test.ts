import { describe, expect, it } from 'vitest';
import { allows, refusal, rightsOf } from './rights';

describe('rights', () => {
  it('reads the welcome, defaulting to read-only', () => {
    expect(rightsOf(null)).toBe('read-only');
    expect(rightsOf({ client_id: 'c', rights: 'full', version: '' })).toBe('full');
    expect(rightsOf({ client_id: 'c', rights: 'standard', version: '' })).toBe('standard');
    expect(rightsOf({ client_id: 'c', rights: 'weird', version: '' })).toBe('read-only');
  });

  it.each([
    ['read-only', 'view', true],
    ['read-only', 'act', false],
    ['read-only', 'admin', false],
    ['standard', 'view', true],
    ['standard', 'act', true],
    ['standard', 'admin', false],
    ['full', 'act', true],
    ['full', 'admin', true],
  ] as const)('%s may %s: %s', (r, c, ok) => {
    expect(allows(r, c)).toBe(ok);
  });

  it('names the reason', () => {
    expect(refusal('read-only', 'act', true)).toBe('read-only connection');
    expect(refusal('standard', 'admin', true)).toBe('needs full rights');
    expect(refusal('full', 'act', false)).toBe('not connected');
    expect(refusal('full', 'admin', true)).toBe('');
    expect(refusal('standard', 'act', true)).toBe('');
    // A view needs no live link: it is a question the page may ask later.
    expect(refusal('read-only', 'view', false)).toBe('');
  });
});
