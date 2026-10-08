import type { WorkspaceState } from './protocol';

// Panel is the one 5c dialog open at a time. The notes editor is apart
// (App.notes): it survives a lost link, spec §6, and every Panel does not.
export type Panel =
  | { kind: 'help' }
  | { kind: 'palette' }
  | { kind: 'projects' }
  | { kind: 'history'; paneId: string; paneType: string }
  | { kind: 'session'; cwd: string; sessionId: string }
  | { kind: 'project_new' }
  | { kind: 'project_rename'; projectId: string }
  | { kind: 'project_remove'; projectId: string }
  | { kind: 'group_new'; projectId: string }
  | { kind: 'group_rename'; name: string }
  | { kind: 'group_delete'; name: string }
  | { kind: 'move_tab'; tabId: string }
  | { kind: 'template' }
  | { kind: 'processes' }
  | { kind: 'kill'; paneId: string; pid: number; startMs: number; name: string }
  | { kind: 'plugins' }
  | { kind: 'update' };

// panelTargetGone reports a panel whose pane, tab, project or group the new
// state no longer holds: it closes rather than send for a dead id.
export function panelTargetGone(p: Panel, s: WorkspaceState): boolean {
  switch (p.kind) {
    case 'history':
    case 'kill':
      return !s.panes.some((x) => x.id === p.paneId);
    case 'move_tab':
      return !s.tabs.some((t) => t.id === p.tabId);
    case 'project_rename':
    case 'project_remove':
    case 'group_new':
      return !s.projects.some((x) => x.id === p.projectId);
    case 'group_rename':
    case 'group_delete':
      return !(s.groups ?? []).includes(p.name);
    default:
      return false;
  }
}
