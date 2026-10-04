import type { Clock } from './connection';
import type { Message, PaneInfo } from './protocol';

// At most four refreshes a second.
const REFRESH_MS = 250;

// Agent state is not in workspace_state; it comes from list_panes_req. The
// first applied state asks at once, and pane events ask again on the trailing
// edge of a burst.
export class AgentStatePoller {
  private started = false;
  private timer: unknown;
  private seq = 0;

  constructor(
    private readonly send: (m: Message) => void,
    private readonly clock: Clock,
  ) {}

  stateApplied(): void {
    if (this.started) return;
    this.started = true;
    this.request();
  }

  paneEvent(): void {
    if (this.timer !== undefined) return;
    this.timer = this.clock.setTimeout(() => {
      this.timer = undefined;
      this.request();
    }, REFRESH_MS);
  }

  // pane id -> working | idle | blocked | '' (unknown)
  response(panes: PaneInfo[]): Map<string, string> {
    const out = new Map<string, string>();
    for (const p of panes) out.set(p.id, p.agent_state ?? '');
    return out;
  }

  private request(): void {
    this.send({ type: 'list_panes_req', id: `agent-${++this.seq}` });
  }
}
