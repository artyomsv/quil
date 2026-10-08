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
