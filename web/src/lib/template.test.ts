import { describe, expect, it } from 'vitest';
import { TEMPLATE_UNSUPPORTED, templateGate } from './template';

describe('templateGate', () => {
  it('allows when the daemon lists the request or says nothing', () => {
    expect(templateGate(null)).toBe('');
    expect(templateGate(['create_from_template_req', 'list_clients_req'])).toBe('');
    // An older daemon sends no list at all: "cannot say" is allowed.
    expect(templateGate([])).toBe('');
  });
  it('refuses when the daemon lists gated requests without templates', () => {
    expect(templateGate(['list_clients_req'])).toBe(TEMPLATE_UNSUPPORTED);
  });
});
