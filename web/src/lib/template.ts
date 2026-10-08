import type { CreateFromTemplateReq } from './protocol';
import type { Outcome } from './requests';

export interface TemplateForm {
  template: string;
  task: string;
  cwd: string;
  branch: string;
}

export interface TemplateIO {
  resolveFolder(input: string): Promise<{ dir: string } | { error: string }>;
  create(req: CreateFromTemplateReq): Promise<Outcome>;
  // The form that submitted is still the one shown.
  stillOpen(): boolean;
}

export type TemplateSubmit = { ok: true } | { error: string } | { cancelled: true };

// submitTemplate takes the target and the form values AT SUBMIT: the folder
// check waits on the daemon, and a project switched meanwhile (by another
// client) must not move the new tab. A form closed during that wait sends
// nothing.
export async function submitTemplate(form: TemplateForm, projectId: string, io: TemplateIO): Promise<TemplateSubmit> {
  const snap: CreateFromTemplateReq = {
    template: form.template,
    task: form.task.trim() || undefined,
    branch: form.branch.trim() || undefined,
    project_id: projectId || undefined,
  };
  const folder = await io.resolveFolder(form.cwd);
  if ('error' in folder) return { error: folder.error };
  if (!io.stillOpen()) return { cancelled: true };
  const out = await io.create({ ...snap, cwd: folder.dir || undefined });
  const err = (out.reply?.payload as { error?: string } | undefined)?.error;
  if (out.ok && !err) return { ok: true };
  return { error: err || (out.ok ? 'not done' : out.error) };
}

export const TEMPLATE_TOO_OLD = 'no answer — the daemon may be too old for templates';
export const TEMPLATE_UNSUPPORTED = 'the daemon is too old for templates';

// templateGate reads version_resp.requests: a daemon that lists its gated
// requests and leaves templates out cannot run one (an older daemon drops
// the request silently). An absent or empty list means "cannot say"
// (ipc.VersionRespPayload), and the request may go.
export function templateGate(requests: string[] | null): string {
  if (!requests || requests.length === 0) return '';
  return requests.includes('create_from_template_req') ? '' : TEMPLATE_UNSUPPORTED;
}
