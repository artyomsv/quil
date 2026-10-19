import { describe, expect, it } from 'vitest';
import { shouldShowWelcome } from './welcome';

describe('shouldShowWelcome', () => {
  it('shows when the config says so and this tab has not dismissed it', () => {
    expect(shouldShowWelcome(true, null)).toBe(true);
    expect(shouldShowWelcome(true, '1')).toBe(false);
    expect(shouldShowWelcome(false, null)).toBe(false);
  });
});
