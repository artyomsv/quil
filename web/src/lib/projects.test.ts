import { describe, expect, it } from 'vitest';
import { CONNECT_ONE_PROJECT, GroupRenames, groupNameError, MAX_GROUP_RUNES, newProjectPlan } from './projects';
import type { ProjectState, WorkspaceState } from './protocol';

const proj = (id: string, over: Partial<ProjectState> = {}): ProjectState => ({
  id,
  name: id,
  root_dir: '/',
  tab_ids: [],
  active_tab: '',
  ...over,
});
const st = (projects: ProjectState[]): WorkspaceState => ({
  active_tab: '',
  active_project: projects[0]?.id ?? '',
  tabs: [],
  panes: [],
  projects,
});

describe('newProjectPlan', () => {
  it.each([
    ['adopts the only bootstrap project', [proj('d', { bootstrap: true })], false, { kind: 'adopt', projectId: 'd' }],
    ['adopts it under --connect too', [proj('d', { bootstrap: true })], true, { kind: 'adopt', projectId: 'd' }],
    ['creates beside named projects locally', [proj('a'), proj('b')], false, { kind: 'create' }],
    ['refuses beside a named project under --connect', [proj('a')], true, { kind: 'refuse', text: CONNECT_ONE_PROJECT }],
    ['creates on an empty daemon', [], true, { kind: 'create' }],
    ['does not adopt a bootstrap project that has company', [proj('d', { bootstrap: true }), proj('a')], false, { kind: 'create' }],
    ['refuses a name taken on the daemon, any case', [proj('a', { name: ' New ' })], false, { kind: 'refuse', text: 'new already exists on that host' }],
  ] as const)('%s', (_n, projects, connect, want) => {
    expect(newProjectPlan(st([...projects]), connect, 'new')).toEqual(want);
  });
});

describe('groupNameError', () => {
  it('refuses blank, too long and taken names', () => {
    expect(groupNameError('  ', [])).not.toBe('');
    expect(groupNameError('x'.repeat(MAX_GROUP_RUNES + 1), [])).not.toBe('');
    expect(groupNameError('😀'.repeat(MAX_GROUP_RUNES), [])).toBe('');
    expect(groupNameError('ops', ['ops'])).not.toBe('');
    expect(groupNameError('ops', ['ops'], 'ops')).toBe('');
    expect(groupNameError('dev', ['ops'])).toBe('');
  });
});

describe('GroupRenames', () => {
  it('allows one rename in flight per group', () => {
    const g = new GroupRenames();
    expect(g.start('a')).toBe(true);
    expect(g.start('a')).toBe(false);
    expect(g.busy('a')).toBe(true);
    expect(g.start('b')).toBe(true);
    g.end('a');
    expect(g.busy('a')).toBe(false);
    expect(g.start('a')).toBe(true);
  });
  it('replaces its set on every change', () => {
    const g = new GroupRenames();
    let seen = 0;
    g.onChange = () => seen++;
    const before = g.inFlight;
    g.start('a');
    expect(g.inFlight).not.toBe(before);
    g.end('a');
    expect(seen).toBe(2);
  });
});
