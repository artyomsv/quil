import type { WebWelcome } from './protocol';

export type Rights = 'read-only' | 'standard' | 'full';
// The daemon's class table (internal/clientauth/rights.go) as the page uses
// it: a view runs at every level, an act at standard and full, an admin
// message at full only.
export type MsgClass = 'view' | 'act' | 'admin';

export function rightsOf(w: WebWelcome | null): Rights {
  const r = w?.rights;
  return r === 'full' || r === 'standard' ? r : 'read-only';
}

export function allows(r: Rights, c: MsgClass): boolean {
  if (c === 'view') return true;
  if (c === 'act') return r !== 'read-only';
  return r === 'full';
}

// refusal is why a control of class c is greyed, '' when it may run. A view
// is a question and needs no live link at the moment the control renders; an
// act or admin control does.
export function refusal(r: Rights, c: MsgClass, live: boolean): string {
  if (c === 'view') return '';
  if (r === 'read-only') return 'read-only connection';
  if (c === 'admin' && r !== 'full') return 'needs full rights';
  if (!live) return 'not connected';
  return '';
}
