import { describe, expect, it } from 'vitest';
import type { CreateFromTemplateReq } from './protocol';
import type { Outcome } from './requests';
import { submitTemplate, TEMPLATE_UNSUPPORTED, type TemplateIO, templateGate } from './template';

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

describe('submitTemplate', () => {
  const form = { template: 'pair', task: ' fix it ', cwd: '~/repo', branch: '' };
  const okAnswer: Outcome = { ok: true, reply: { type: 'create_from_template_resp', payload: { tab_id: 't', pane_ids: ['p'] } } };

  function deferred() {
    let release: (v: { dir: string } | { error: string }) => void = () => {};
    const sent: CreateFromTemplateReq[] = [];
    let open = true;
    const io: TemplateIO = {
      resolveFolder: () => new Promise((r) => (release = r)),
      create: async (req) => {
        sent.push(req);
        return okAnswer;
      },
      stillOpen: () => open,
    };
    return { io, sent, release: (v: { dir: string } | { error: string }) => release(v), close: () => (open = false) };
  }

  it('targets the project of the submit, not one switched to while the folder resolves', async () => {
    const d = deferred();
    let active = 'A';
    const done = submitTemplate(form, active, d.io);
    // Another client switches the shown project meanwhile.
    active = 'B';
    d.release({ dir: '/home/u/repo' });
    expect(await done).toEqual({ ok: true });
    expect(active).toBe('B');
    expect(d.sent).toEqual([{ template: 'pair', task: 'fix it', branch: undefined, project_id: 'A', cwd: '/home/u/repo' }]);
  });
  it('sends nothing when the form closed while the folder resolved', async () => {
    const d = deferred();
    const done = submitTemplate(form, 'A', d.io);
    d.close();
    d.release({ dir: '/home/u/repo' });
    expect(await done).toEqual({ cancelled: true });
    expect(d.sent).toEqual([]);
  });
  it('keeps the folder error and sends nothing', async () => {
    const d = deferred();
    const done = submitTemplate(form, 'A', d.io);
    d.release({ error: 'no such folder' });
    expect(await done).toEqual({ error: 'no such folder' });
    expect(d.sent).toEqual([]);
  });
  it('reads the daemon error of a refused create', async () => {
    const io: TemplateIO = {
      resolveFolder: async () => ({ dir: '' }),
      create: async () => ({ ok: false, code: 'failed', error: 'not done', reply: { type: 'x', payload: { error: 'unknown template "pair"' } } }),
      stillOpen: () => true,
    };
    expect(await submitTemplate(form, '', io)).toEqual({ error: 'unknown template "pair"' });
  });
});
