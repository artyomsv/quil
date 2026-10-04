import { describe, expect, it } from 'vitest';
import { AgentStatePoller } from './agentstate';
import type { Clock } from './connection';
import type { Message } from './protocol';

class FakeClock implements Clock {
  t = 0;
  private next = 1;
  private timers = new Map<number, { at: number; fn: () => void }>();
  setTimeout(fn: () => void, ms: number): unknown {
    const id = this.next++;
    this.timers.set(id, { at: this.t + ms, fn });
    return id;
  }
  clearTimeout(h: unknown): void {
    this.timers.delete(h as number);
  }
  now(): number {
    return this.t;
  }
  advance(ms: number): void {
    const end = this.t + ms;
    for (;;) {
      let due: [number, { at: number; fn: () => void }] | undefined;
      for (const e of this.timers) if (e[1].at <= end && (!due || e[1].at < due[1].at)) due = e;
      if (!due) break;
      this.timers.delete(due[0]);
      this.t = due[1].at;
      due[1].fn();
    }
    this.t = end;
  }
}

function rig(): { poller: AgentStatePoller; clock: FakeClock; sent: Message[] } {
  const clock = new FakeClock();
  const sent: Message[] = [];
  return { poller: new AgentStatePoller((m) => sent.push(m), clock), clock, sent };
}

describe('AgentStatePoller', () => {
  it('asks at once for the first applied state, with an id', () => {
    const { poller, sent } = rig();
    poller.stateApplied();
    expect(sent).toHaveLength(1);
    expect(sent[0]?.type).toBe('list_panes_req');
    expect(sent[0]?.id).toBeTruthy();
    poller.stateApplied();
    expect(sent).toHaveLength(1);
  });

  it('sends one request, 250 ms after the first event of a burst', () => {
    const { poller, clock, sent } = rig();
    poller.stateApplied();
    for (let i = 0; i < 10; i++) {
      poller.paneEvent();
      clock.advance(5);
    }
    expect(sent).toHaveLength(1);
    clock.advance(199);
    expect(sent).toHaveLength(1);
    clock.advance(1);
    expect(sent).toHaveLength(2);
    clock.advance(1000);
    expect(sent).toHaveLength(2);
  });

  it('sends the next burst as its own request', () => {
    const { poller, clock, sent } = rig();
    poller.paneEvent();
    clock.advance(250);
    poller.paneEvent();
    clock.advance(250);
    expect(sent).toHaveLength(2);
    expect(sent[0]?.id).not.toBe(sent[1]?.id);
  });

  it('maps agent_state per pane, empty for unknown', () => {
    const { poller } = rig();
    const got = poller.response([
      { id: 'a', tab_id: 't', agent_state: 'working' },
      { id: 'b', tab_id: 't', agent_state: 'blocked' },
      { id: 'c', tab_id: 't' },
    ]);
    expect([...got]).toEqual([
      ['a', 'working'],
      ['b', 'blocked'],
      ['c', ''],
    ]);
  });
});
