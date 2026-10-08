import { describe, expect, it } from 'vitest';
import { type Panel, panelTargetGone, stillShown } from './panels';
import type { WorkspaceState } from './protocol';

const s: WorkspaceState = {
  active_tab: 't1',
  active_project: 'p1',
  tabs: [{ id: 't1', name: 'a', color: '', panes: ['x1'], project_id: 'p1', layout_rev: 1 }],
  panes: [{ id: 'x1', tab_id: 't1', cwd: '/' }],
  projects: [{ id: 'p1', name: 'P', root_dir: '/', tab_ids: ['t1'], active_tab: 't1' }],
  groups: ['g1'],
};

describe('panelTargetGone', () => {
  it.each<[Panel, boolean]>([
    [{ kind: 'help' }, false],
    [{ kind: 'palette' }, false],
    [{ kind: 'processes' }, false],
    [{ kind: 'history', paneId: 'x1', paneType: 'claude-code' }, false],
    [{ kind: 'history', paneId: 'gone', paneType: 'claude-code' }, true],
    [{ kind: 'kill', paneId: 'x1', pid: 9, startMs: 1, name: 'n' }, false],
    [{ kind: 'kill', paneId: 'gone', pid: 9, startMs: 1, name: 'n' }, true],
    [{ kind: 'move_tab', tabId: 't1' }, false],
    [{ kind: 'move_tab', tabId: 'gone' }, true],
    [{ kind: 'project_rename', projectId: 'p1' }, false],
    [{ kind: 'project_rename', projectId: 'gone' }, true],
    [{ kind: 'project_remove', projectId: 'gone' }, true],
    [{ kind: 'group_new', projectId: 'gone' }, true],
    [{ kind: 'group_rename', name: 'g1' }, false],
    [{ kind: 'group_rename', name: 'gone' }, true],
    [{ kind: 'group_delete', name: 'gone' }, true],
  ])('%j gone: %s', (p, gone) => {
    expect(panelTargetGone(p, s)).toBe(gone);
  });

  it('reads a state without groups as no groups', () => {
    const bare: WorkspaceState = { ...s, groups: undefined };
    expect(panelTargetGone({ kind: 'group_rename', name: 'g1' }, bare)).toBe(true);
  });
});

describe('stillShown', () => {
  it('closes only the panel a late answer belongs to', () => {
    const old: Panel = { kind: 'project_new' };
    const reopened: Panel = { kind: 'project_new' };
    expect(stillShown(old, old)).toBe(true);
    // Cancelled, then the same kind opened again: a new object.
    expect(stillShown(old, reopened)).toBe(false);
    expect(stillShown(old, null)).toBe(false);
    expect(stillShown(null, null)).toBe(false);
  });
});
