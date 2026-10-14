import { describe, expect, it } from 'vitest';
import { shouldShowBeta } from './beta';

describe('shouldShowBeta', () => {
  it('shows when the config says so and this tab has not dismissed it', () => {
    expect(shouldShowBeta(true, null)).toBe(true);
    expect(shouldShowBeta(true, '1')).toBe(false);
    expect(shouldShowBeta(false, null)).toBe(false);
  });
});
