import type { WorkspaceState } from './protocol';
import type { Outcome } from './requests';
import { sanitizeRemoteText } from './sanitize';

export const MAX_GROUP_RUNES = 32;
// The TUI folds a remote host's projects instead (merge_projects, TUI-only);
// the browser refuses and names the route it has.
export const CONNECT_ONE_PROJECT = 'This host keeps one project. Rename the existing project, or fold projects in the TUI.';

export type NewProjectPlan = { kind: 'adopt'; projectId: string } | { kind: 'create' } | { kind: 'refuse'; text: string };

const norm = (s: string): string => s.trim().toLowerCase();

// newProjectPlan follows the TUI's submitProjectForm: the daemon's lone
// bootstrap project is adopted rather than joined by a second one (spec
// AC-20, on every daemon); under --connect a host keeps one named project;
// otherwise a name the daemon already holds is refused (projectNamedOnDest:
// trimmed, case-insensitive).
export function newProjectPlan(s: WorkspaceState, connect: boolean, name: string): NewProjectPlan {
  const only = s.projects.length === 1 ? s.projects[0] : undefined;
  if (only?.bootstrap) return { kind: 'adopt', projectId: only.id };
  if (connect && s.projects.length > 0) return { kind: 'refuse', text: CONNECT_ONE_PROJECT };
  if (s.projects.some((p) => norm(p.name) === norm(name))) {
    return { kind: 'refuse', text: `${sanitizeRemoteText(name.trim())} already exists on that host` };
  }
  return { kind: 'create' };
}

// folderFromBrowse reads a browse_dir_req answer as a folder check: the
// daemon expanded ~, made the path absolute and listed it, so its Resolved
// is the folder to send; an error (missing, not a folder, no answer) keeps
// the form open with that reason.
export function folderFromBrowse(o: Outcome): { dir: string } | { error: string } {
  const p = (o.reply?.payload ?? null) as { resolved?: string; error?: string } | null;
  if (p?.error) return { error: sanitizeRemoteText(p.error) };
  if (!o.ok) return { error: o.error };
  if (!p?.resolved) return { error: 'the daemon did not resolve the folder' };
  return { dir: p.resolved };
}

// groupNameError is why a group name cannot be sent ('' = it can): blank,
// longer than the daemon's limit, or a group that already exists.
export function groupNameError(name: string, taken: string[], current?: string): string {
  const n = name.trim();
  if (n === '') return 'A group needs a name';
  if (Array.from(n).length > MAX_GROUP_RUNES) return `At most ${MAX_GROUP_RUNES} characters`;
  if (n !== current && taken.includes(n)) return 'That group already exists';
  return '';
}

// GroupRenames keeps one rename in flight per group (the TUI's rule). The
// page waits for the daemon's answer and never shows an alias. The set is
// replaced on every change, so a reactive holder sees each one.
export class GroupRenames {
  inFlight: ReadonlySet<string> = new Set();
  onChange: () => void = () => {};

  start(name: string): boolean {
    if (this.inFlight.has(name)) return false;
    this.inFlight = new Set([...this.inFlight, name]);
    this.onChange();
    return true;
  }

  end(name: string): void {
    const next = new Set(this.inFlight);
    next.delete(name);
    this.inFlight = next;
    this.onChange();
  }

  busy(name: string): boolean {
    return this.inFlight.has(name);
  }
}
