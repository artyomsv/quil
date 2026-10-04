import type { WorkspaceState } from './protocol';

// StateRev applies a workspace_state only when its number is newer than the
// highest applied for the same daemon run. Every frame is a full snapshot, so
// a skipped number needs no repair. A new run_id (the daemon restarted)
// starts the numbering over, and the caller resets its terminals.
export class StateRev {
  private runId: string | undefined;
  private highest = -1;
  private seen = false;
  private pending = false;

  accept(s: WorkspaceState): 'apply' | 'apply-new-run' | 'drop' {
    if (s.rev === undefined || s.run_id === undefined) {
      this.pending = false;
      return 'apply';
    }
    if (!this.seen || s.run_id !== this.runId) {
      this.seen = true;
      this.runId = s.run_id;
      this.highest = s.rev;
      this.pending = false;
      return 'apply-new-run';
    }
    if (s.rev <= this.highest) return 'drop';
    this.highest = s.rev;
    this.pending = false;
    return 'apply';
  }

  forget(): void {
    this.seen = false;
    this.runId = undefined;
    this.highest = -1;
    this.pending = false;
  }

  // A workspace_state that does not decode is dropped, never applied as an
  // empty workspace. malformed() says whether to send ONE state_req now:
  // true only after a numbered frame was applied in this run and while no
  // request is pending; any applied frame clears the pending request.
  malformed(): boolean {
    if (!this.seen || this.pending) return false;
    this.pending = true;
    return true;
  }
}
