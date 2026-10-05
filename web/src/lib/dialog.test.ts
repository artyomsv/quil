import { describe, expect, it } from 'vitest';
import type { ClientInfo } from './client';
import { availableFrom, CreateDialog, enforceGroups, NEED_FULL_RIGHTS, viewOf } from './dialog';

const info: ClientInfo = {
  rights: 'full',
  categories: [
    { key: 'terminal', label: 'Terminal' },
    { key: 'ai', label: 'AI Assistant' },
    { key: 'tools', label: 'Tools' },
    { key: 'remote', label: 'Remote' },
  ],
  plugins: [
    { name: 'terminal', display_name: 'Terminal', category: 'terminal' },
    {
      name: 'claude-code',
      display_name: 'Claude Code',
      category: 'ai',
      prompts_cwd: true,
      sessions: 'claude',
      uses_claude_auth: true,
      toggles: [
        { name: 'a', label: 'A', group: 'g', default: true },
        { name: 'b', label: 'B', group: 'g' },
        { name: 'c', label: 'C' },
      ],
    },
    {
      name: 'ssh',
      display_name: 'SSH',
      category: 'remote',
      form_fields: [
        { name: 'name', label: 'Name', required: true },
        { name: 'host', label: 'Host', required: true },
      ],
    },
    { name: 'k9s', display_name: 'k9s', category: 'tools', discover: 'kube' },
  ],
  instances: { ssh: [{ id: 'i1', name: 'box', fields: { name: 'box', host: 'h' } }] },
  sandbox: { sign_in_default: 'shared', image_default: 'img:1' },
};
const avail = { terminal: true, 'claude-code': true, ssh: true, k9s: true };

function open(placement: 'pane' | 'new_tab' | 'replace' = 'pane') {
  return new CreateDialog(info, avail, { mode: placement, targetPaneId: 'p1', tabId: 't1', projectId: 'pr1', defaultCwd: '/repo' });
}

describe('CreateDialog', () => {
  it('walks category → plugin → placement for a plugin with no setup', () => {
    const d = open();
    expect(d.step).toBe('category');
    d.pickCategory('terminal');
    d.pickPlugin('terminal');
    expect(d.step).toBe('placement');
    expect(d.submit('below')).toEqual({ target_pane_id: 'p1', placement: 'below', pane: { type: 'terminal', cwd: '/repo' } });
  });

  it('lists categories in the gateway order, only those with a plugin', () => {
    const d = new CreateDialog({ ...info, plugins: info.plugins.filter((p) => p.category !== 'tools') }, avail, {
      mode: 'pane',
      targetPaneId: 'p1',
      tabId: 't1',
      projectId: 'pr1',
      defaultCwd: '/',
    });
    expect(d.categories.map((c) => c.key)).toEqual(['terminal', 'ai', 'remote']);
  });

  it('a plugin unavailable on this daemon cannot be picked, and sorts last', () => {
    const plugins = [...info.plugins, { name: 'aaa', display_name: 'Aaa', category: 'tools' }];
    const d = new CreateDialog({ ...info, plugins }, { ...avail, k9s: false, aaa: true }, {
      mode: 'pane',
      targetPaneId: 'p1',
      tabId: 't1',
      projectId: 'pr1',
      defaultCwd: '/',
    });
    d.pickCategory('tools');
    expect(d.pluginsIn('tools').map((p) => p.name)).toEqual(['aaa', 'k9s']);
    expect(d.pickPlugin('k9s')).toBe(false);
    expect(d.step).toBe('plugin');
  });

  it('setup: toggles start from defaults with one per group, and a group stays exclusive', () => {
    const d = open();
    d.pickCategory('ai');
    d.pickPlugin('claude-code');
    expect(d.step).toBe('setup');
    expect(d.toggles).toEqual([true, false, false]);
    d.setToggle(1, true);
    expect(d.toggles).toEqual([false, true, false]);
    d.setToggle(2, true);
    d.cwd = '/repo/x';
    d.continueSetup();
    expect(d.submit('right')?.pane).toEqual({ type: 'claude-code', cwd: '/repo/x', toggles: ['b', 'c'] });
  });

  it('enforceGroups keeps the last checked member when two start on', () => {
    const states = [true, true, true];
    enforceGroups([{ group: 'g' }, { group: 'g' }, {}], states, -1);
    expect(states).toEqual([false, true, true]);
  });

  it('sandbox: needs an image, pre-selects the configured sign-in, hides the resume picker', () => {
    const d = open();
    d.pickCategory('ai');
    d.pickPlugin('claude-code');
    d.resumeId = '0d4c2f9e-1b7a-4c3e-9f5d-2a6b8c0e1f3a';
    d.sandboxOn = true;
    expect(d.showSession).toBe(false);
    expect(d.sandboxImage).toBe('img:1');
    d.sandboxImage = '';
    expect(d.continueSetup()).toBe('Enter a container image, or turn the sandbox off');
    d.sandboxImage = 'img:2';
    expect(d.continueSetup()).toBe('');
    expect(d.submit('right')?.pane.sandbox).toEqual({ image: 'img:2', auth: 'browser', claude_config: 'shared' });
    expect(d.submit('right')?.pane.resume_session_id).toBeUndefined();
  });

  it('the sandbox row needs an available engine; sign-in only for a Claude plugin', () => {
    const d = open();
    d.pickCategory('ai');
    d.pickPlugin('claude-code');
    expect(d.showSandbox).toBe(false);
    const n = d.scanning('sandbox');
    d.listed('sandbox', { ok: true, reply: { type: 'sandbox_cap_resp', payload: { available: true } } }, n);
    expect(d.showSandbox).toBe(true);
    expect(d.showSignIn).toBe(false);
    d.sandboxOn = true;
    expect(d.showSignIn).toBe(true);
    d.signIn = 'token';
    d.continueSetup();
    expect(d.submit('right')?.pane.sandbox).toEqual({ image: 'img:1', auth: 'token', claude_config: 'own' });
  });

  it('a resume pick listed for another folder is dropped at submit', () => {
    const d = open();
    d.pickCategory('ai');
    d.pickPlugin('claude-code');
    d.cwd = '/a';
    d.listedSessionsFor = '/a';
    d.resumeId = '0d4c2f9e-1b7a-4c3e-9f5d-2a6b8c0e1f3a';
    d.cwd = '/b';
    d.continueSetup();
    expect(d.submit('right')?.pane.resume_session_id).toBeUndefined();
  });

  it('a resume pick for the spawn folder is sent', () => {
    const d = open();
    d.pickCategory('ai');
    d.pickPlugin('claude-code');
    d.listed('sessions', { ok: true, reply: { type: 'claude_sessions_resp', payload: { cwd: '/repo', sessions: [{ id: 's' }] } } });
    d.resumeId = '0d4c2f9e-1b7a-4c3e-9f5d-2a6b8c0e1f3a';
    d.continueSetup();
    expect(d.submit('right')?.pane.resume_session_id).toBe('0d4c2f9e-1b7a-4c3e-9f5d-2a6b8c0e1f3a');
  });

  it('a folder change forgets the worktree and session choices of the old folder', () => {
    const d = open();
    d.pickCategory('ai');
    d.pickPlugin('claude-code');
    d.existingWorktree = '/repo-wt';
    d.newBranch = 'x';
    d.resumeId = 'r';
    d.folderChanged('/other');
    expect([d.cwd, d.existingWorktree, d.newBranch, d.resumeId]).toEqual(['/other', '', '', '']);
  });

  it('worktree: an existing one is {existing_path}; a new branch is {branch} (R-A)', () => {
    const a = open();
    a.pickCategory('ai');
    a.pickPlugin('claude-code');
    a.cwd = '/repo';
    a.worktreeRoot = '/repo';
    a.existingWorktree = '/repo-wt';
    a.newBranch = 'ignored';
    a.continueSetup();
    expect(a.submit('right')?.pane).toEqual(expect.objectContaining({ cwd: '/repo-wt', worktree: { existing_path: '/repo-wt' } }));
    const b = open();
    b.pickCategory('ai');
    b.pickPlugin('claude-code');
    b.cwd = '/repo/sub';
    b.worktreeRoot = '/repo';
    b.newBranch = 'feat-x';
    expect(b.showSession).toBe(false);
    b.continueSetup();
    const pane = b.submit('right')?.pane;
    expect(pane).toMatchObject({ cwd: '/repo/sub', worktree: { branch: 'feat-x' } });
    expect(pane?.worktree).not.toHaveProperty('repo_root');
  });

  it('instances: a saved one sends only its id; standard rights are refused before the form', () => {
    const d = open();
    d.pickCategory('remote');
    d.pickPlugin('ssh');
    expect(d.step).toBe('instances');
    d.pickInstance('i1');
    expect(d.step).toBe('placement');
    expect(d.submit('right')?.pane).toEqual({ type: 'ssh', cwd: '/repo', instance_id: 'i1' });
    const s = new CreateDialog({ ...info, rights: 'standard' }, avail, {
      mode: 'pane',
      targetPaneId: 'p1',
      tabId: 't1',
      projectId: 'pr1',
      defaultCwd: '/',
    });
    s.pickCategory('remote');
    expect(s.pickPlugin('ssh')).toBe(false);
    expect(s.error).toContain('full rights');
    expect(s.error).toBe(NEED_FULL_RIGHTS);
  });

  it('instances: no saved one opens the form; a save goes on; a change returns to the list', () => {
    const d = new CreateDialog({ ...info, instances: {} }, avail, {
      mode: 'pane',
      targetPaneId: 'p1',
      tabId: 't1',
      projectId: 'pr1',
      defaultCwd: '/repo',
    });
    d.pickCategory('remote');
    d.pickPlugin('ssh');
    expect(d.step).toBe('form');
    d.instanceSaved('n1');
    expect(d.step).toBe('placement');
    expect(d.submit('right')?.pane.instance_id).toBe('n1');
    const e = open();
    e.pickCategory('remote');
    e.pickPlugin('ssh');
    e.editInstance('i1');
    expect([e.step, e.editing]).toEqual(['form', 'i1']);
    e.instancesChanged({ ...info, instances: {} });
    expect(e.step).toBe('form');
    e.instancesChanged(info);
    expect(e.step).toBe('instances');
  });

  it('kube: the picked context is sent by name', () => {
    const d = open();
    d.pickCategory('tools');
    d.pickPlugin('k9s');
    d.kubeContext = 'prod';
    d.continueSetup();
    expect(d.submit('right')?.pane).toEqual({ type: 'k9s', cwd: '/repo', kube_context: 'prod' });
  });

  it('new tab skips placement; replace has only replace', () => {
    const t = open('new_tab');
    t.pickCategory('terminal');
    t.pickPlugin('terminal');
    expect(t.step).toBe('done');
    expect(t.request).toEqual({ tab_id: 't1', placement: 'new_tab', new_tab: { name: '', project_id: 'pr1' }, pane: { type: 'terminal', cwd: '/repo' } });
    const r = open('replace');
    r.pickCategory('terminal');
    r.pickPlugin('terminal');
    expect(r.placements).toEqual(['replace']);
    expect(r.submit('right')).toBeNull();
    expect(r.submit('replace')).toEqual({ target_pane_id: 'p1', placement: 'replace', pane: { type: 'terminal', cwd: '/repo' } });
  });

  it('a refusal returns to the step it was sent from', () => {
    const d = open();
    d.pickCategory('terminal');
    d.pickPlugin('terminal');
    d.submit('right');
    d.refused('no such pane');
    expect([d.step, d.error, d.request]).toEqual(['placement', 'no such pane', null]);
    const t = open('new_tab');
    t.pickCategory('ai');
    t.pickPlugin('claude-code');
    t.continueSetup();
    expect(t.step).toBe('done');
    t.refused('x');
    expect(t.step).toBe('setup');
  });

  it('each daemon list shows scanning, empty, failed and busy apart', () => {
    const d = open();
    d.pickCategory('ai');
    d.pickPlugin('claude-code');
    d.scanning('sessions');
    expect(d.lists.sessions.status).toBe('scanning');
    d.listed('sessions', { ok: true, reply: { type: 'claude_sessions_resp', payload: { cwd: '/repo', sessions: [] } } });
    expect(d.lists.sessions.status).toBe('empty');
    d.listed('sessions', { ok: false, code: 'failed', error: 'scan already running' });
    expect(d.lists.sessions.status).toBe('failed');
    expect(d.lists.sessions.retry).toBe(true);
    d.listed('folders', { ok: true, reply: { type: 'browse_dir_resp', payload: { path: '/', entries: [{ name: 'a', is_dir: true }] } } });
    expect(d.lists.folders.status).toBe('ready');
    d.listed('folders', { ok: true, reply: { type: 'browse_dir_resp', payload: { path: '/', roots: ['/'] } } });
    expect(d.lists.folders.status).toBe('ready');
    d.listed('worktrees', { ok: true, reply: { type: 'worktree_list_resp', payload: { path: '/r', root: '/r', worktrees: [{ path: '/r' }] } } });
    expect([d.lists.worktrees.status, d.worktreeRoot]).toEqual(['ready', '/r']);
  });

  it('an answer to an older request than the newest is dropped', () => {
    const d = open();
    const first = d.scanning('folders');
    const second = d.scanning('folders');
    d.listed('folders', { ok: true, reply: { type: 'browse_dir_resp', payload: { path: '/old', entries: [] } } }, first);
    expect(d.lists.folders.status).toBe('scanning');
    d.listed('folders', { ok: true, reply: { type: 'browse_dir_resp', payload: { path: '/new', entries: [{ name: 'x', is_dir: true }] } } }, second);
    expect(d.lists.folders.status).toBe('ready');
  });

  it('Back returns one step', () => {
    const d = open();
    d.pickCategory('remote');
    d.pickPlugin('ssh');
    d.back();
    expect(d.step).toBe('plugin');
    d.back();
    expect(d.step).toBe('category');
    expect(d.back()).toBe(false);
  });
});

describe('availableFrom', () => {
  it('files the daemon answer; a plugin it did not list is unavailable', () => {
    const got = availableFrom(info, {
      ok: true,
      reply: { type: 'plugin_list_resp', payload: { plugins: [{ name: 'terminal', available: true }, { name: 'k9s', available: false }] } },
    });
    expect(got).toEqual({ terminal: true, k9s: false });
  });

  it('a daemon that did not answer keeps every plugin offered', () => {
    const got = availableFrom(info, { ok: false, code: 'timeout', error: 'x' });
    expect(got).toEqual({ terminal: true, 'claude-code': true, ssh: true, k9s: true });
  });
});

describe('viewOf', () => {
  it('is a copy: changing the dialog afterwards does not change a taken view', () => {
    const d = open();
    d.pickCategory('ai');
    d.pickPlugin('claude-code');
    const v = viewOf(d);
    d.setToggle(1, true);
    expect(v.toggles).toEqual([true, false, false]);
    expect(viewOf(d).toggles).toEqual([false, true, false]);
    expect(v.step).toBe('setup');
    expect(v.plugins.map((p) => p.name)).toEqual(['claude-code']);
    expect(v.showFolder && v.showWorktree && v.showSession).toBe(true);
    expect(v.showKube || v.showSandbox).toBe(false);
  });
});
