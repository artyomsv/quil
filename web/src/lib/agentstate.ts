import type { Clock } from './connection';
import type { Message, PaneInfo } from './protocol';

// At most four refreshes a second.
const REFRESH_MS = 250;
// An unanswered request stops blocking the next one after this long.
const IN_FLIGHT_MS = 2000;

// Agent state is not in workspace_state; it comes from list_panes_req. The
// first applied state asks at once, and pane events ask again on the trailing
// edge of a burst. Requests are spaced REFRESH_MS apart and never overlap:
// one asked for while another is outstanding goes out when that one is
// answered or times out. Nothing is sent before the first applied state.
export class AgentStatePoller {
  private started = false;
  private timer: unknown;
  private flightTimer: unknown;
  private inFlight = false;
  private inFlightId = '';
  private dirty = false;
  private lastSentAt = -Infinity;
  private seq = 0;

  constructor(
    private readonly send: (m: Message) => void,
    private readonly clock: Clock,
  ) {}

  stateApplied(): void {
    this.started = true;
    this.ask();
  }

  paneEvent(): void {
    if (!this.started || this.timer !== undefined) return;
    this.timer = this.clock.setTimeout(() => {
      this.timer = undefined;
      this.ask();
    }, REFRESH_MS);
  }

  // pane id -> working | idle | blocked | '' (unknown). Only the answer to
  // the request in flight releases it: a late answer to a request that timed
  // out still carries usable states, but the newer request is still waiting.
  response(id: string | undefined, panes: PaneInfo[]): Map<string, string> {
    const out = new Map<string, string>();
    for (const p of panes) out.set(p.id, p.agent_state ?? '');
    if (this.inFlight && id !== undefined && id === this.inFlightId) this.settle();
    return out;
  }

  // On disconnect: drops pending work. The next stateApplied starts over.
  stop(): void {
    if (this.timer !== undefined) this.clock.clearTimeout(this.timer);
    if (this.flightTimer !== undefined) this.clock.clearTimeout(this.flightTimer);
    this.timer = undefined;
    this.flightTimer = undefined;
    this.inFlight = false;
    this.inFlightId = '';
    this.dirty = false;
    this.started = false;
  }

  private ask(): void {
    const wait = this.lastSentAt + REFRESH_MS - this.clock.now();
    if (wait > 0) {
      if (this.timer === undefined) {
        this.timer = this.clock.setTimeout(() => {
          this.timer = undefined;
          this.ask();
        }, wait);
      }
      return;
    }
    if (this.inFlight) {
      this.dirty = true;
      return;
    }
    this.lastSentAt = this.clock.now();
    this.inFlight = true;
    this.inFlightId = `agent-${++this.seq}`;
    this.flightTimer = this.clock.setTimeout(() => this.settle(), IN_FLIGHT_MS);
    this.send({ type: 'list_panes_req', id: this.inFlightId });
  }

  private settle(): void {
    if (this.flightTimer !== undefined) this.clock.clearTimeout(this.flightTimer);
    this.flightTimer = undefined;
    this.inFlight = false;
    this.inFlightId = '';
    if (this.dirty) {
      this.dirty = false;
      this.ask();
    }
  }
}
