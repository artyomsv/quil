import { describe, expect, it } from 'vitest';
import { groupOf, MAX_EVENTS, NotificationStore, type NotifyInfo, type PaneEvent, parsePaneEvent } from './notifications';

const info: NotifyInfo = {
  shown: { agent_turn: true, agent_blocked: true, pane: true, system: true, commands: false, idle: false },
  hook_groups: { Stop: 'agent_turn', 'chat.message': 'agent_turn' },
  plain_groups: { bell: 'agent_blocked', pane_destroyed: 'pane', command_complete: 'commands', output_idle: 'idle' },
  default_group: 'system',
  work_state_only: ['hook.claude.PostToolUse'],
};
const ev = (id: string, over: Partial<PaneEvent> = {}): PaneEvent => ({
  id,
  pane_id: 'p1',
  tab_id: 't1',
  pane_name: 'shell',
  type: 'bell',
  title: id,
  severity: 'info',
  timestamp: 1,
  ...over,
});
const ctx = { muted: (_: string) => false, activePaneId: 'p9' };

describe('groupOf', () => {
  it('files hook events by their trailing segment', () => {
    expect(groupOf('hook.claude.Stop', info)).toBe('agent_turn');
    expect(groupOf('hook.opencode.chat.message', info)).toBe('agent_turn');
    expect(groupOf('hook.x.Unknown', info)).toBe('system');
    expect(groupOf('hook.nosource', info)).toBe('system');
    expect(groupOf('never_seen', info)).toBe('system');
  });
});

describe('NotificationStore', () => {
  it('replaces an event with a known id and moves it to the top', () => {
    const s = new NotificationStore(info);
    s.add(ev('a'), ctx);
    s.add(ev('b'), ctx);
    s.add(ev('a', { title: 'a ×2', data: { count: '2' } }), ctx);
    expect(s.visible().map((e) => e.title)).toEqual(['a ×2', 'b']);
  });

  it('applies the TUI skip rules', () => {
    const s = new NotificationStore(info);
    expect(s.add(ev('w', { type: 'hook.claude.PostToolUse' }), ctx)).toBe(false);
    expect(s.add(ev('m'), { ...ctx, muted: (p) => p === 'p1' })).toBe(false);
    expect(s.add(ev('i', { type: 'output_idle', pane_id: 'p9' }), ctx)).toBe(false);
    expect(s.add(ev('i2', { type: 'output_idle', pane_id: 'p2' }), ctx)).toBe(true);
  });

  it('hides groups the filter turns off but keeps them stored', () => {
    const s = new NotificationStore(info);
    s.add(ev('c', { type: 'command_complete' }), ctx);
    s.add(ev('b'), ctx);
    expect(s.visible().map((e) => e.id)).toEqual(['b']);
    expect(s.size()).toBe(2);
  });

  it('rebuilds from a newest-first list', () => {
    const s = new NotificationStore(info);
    s.add(ev('stale'), ctx);
    s.rebuild([ev('new'), ev('old')], ctx);
    expect(s.visible().map((e) => e.id)).toEqual(['new', 'old']);
  });

  it('dismisses one or all', () => {
    const s = new NotificationStore(info);
    s.add(ev('a'), ctx);
    s.add(ev('b'), ctx);
    s.dismiss('a');
    expect(s.visible().map((e) => e.id)).toEqual(['b']);
    s.dismiss('');
    expect(s.size()).toBe(0);
  });

  it('evicts hidden events first when over the cap', () => {
    const s = new NotificationStore(info);
    s.add(ev('hidden', { type: 'command_complete' }), ctx);
    for (let i = 0; i < MAX_EVENTS; i++) s.add(ev(`v${i}`), ctx);
    expect(s.size()).toBe(MAX_EVENTS);
    expect(s.visible().some((e) => e.id === 'v0')).toBe(true);
  });

  it('shows everything before the page has the filter', () => {
    const s = new NotificationStore(null);
    s.add(ev('c', { type: 'command_complete' }), ctx);
    expect(s.visible()).toHaveLength(1);
  });

  it('applies a filter that arrives later to the stored events', () => {
    const s = new NotificationStore(null);
    s.add(ev('c', { type: 'command_complete' }), ctx);
    s.setInfo(info);
    expect(s.visible()).toHaveLength(0);
  });

  it('drops work-state events that arrived before the tables', () => {
    const s = new NotificationStore(null);
    s.add(ev('w', { type: 'hook.claude.PostToolUse' }), ctx);
    s.setInfo(info);
    expect(s.size()).toBe(0);
  });
});

describe('parsePaneEvent', () => {
  it('accepts a daemon event and refuses one without an id', () => {
    expect(parsePaneEvent({ id: 'a', pane_id: 'p', tab_id: 't', type: 'bell', title: 'x', severity: 'info', timestamp: 2 })?.id).toBe('a');
    expect(parsePaneEvent({ pane_id: 'p' })).toBeNull();
    expect(parsePaneEvent(null)).toBeNull();
  });
  it('keeps only string data values', () => {
    expect(parsePaneEvent({ id: 'a', data: { count: '3', bad: 4 } })?.data).toEqual({ count: '3' });
  });
});
