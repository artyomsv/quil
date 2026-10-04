import { describe, expect, it } from 'vitest';
import { shouldRegisterE2EHook } from './e2ehook';

describe('shouldRegisterE2EHook', () => {
  it('registers only when VITE_QUIL_E2E is exactly 1', () => {
    expect(shouldRegisterE2EHook({ VITE_QUIL_E2E: '1' })).toBe(true);
  });

  it('does not register when the variable is unset', () => {
    expect(shouldRegisterE2EHook({})).toBe(false);
    expect(shouldRegisterE2EHook({ MODE: 'production', PROD: true })).toBe(false);
  });

  it('does not register for any other value', () => {
    for (const v of ['', '0', 'true', 'yes', 1, true]) {
      expect(shouldRegisterE2EHook({ VITE_QUIL_E2E: v })).toBe(false);
    }
  });

  it('does not register in this test build, which sets no VITE_QUIL_E2E', () => {
    expect(shouldRegisterE2EHook(import.meta.env)).toBe(false);
  });
});
