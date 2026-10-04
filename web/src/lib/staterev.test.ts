import { describe, expect, it } from 'vitest';
import { StateRev } from './staterev';
import type { WorkspaceState } from './protocol';

const st = (rev: number | undefined, run_id: string | undefined): WorkspaceState => ({
  active_tab: '',
  tabs: [],
  panes: [],
  projects: [],
  active_project: '',
  rev,
  run_id,
});

describe('StateRev', () => {
  it('applies newer, drops older or equal, within one run', () => {
    const r = new StateRev();
    expect(r.accept(st(5, 'a'))).toBe('apply-new-run');
    expect(r.accept(st(6, 'a'))).toBe('apply');
    expect(r.accept(st(6, 'a'))).toBe('drop');
    expect(r.accept(st(4, 'a'))).toBe('drop');
  });
  it('a new run id starts over', () => {
    const r = new StateRev();
    r.accept(st(50, 'a'));
    expect(r.accept(st(1, 'b'))).toBe('apply-new-run');
  });
  it('an unnumbered frame from an older daemon applies', () => {
    const r = new StateRev();
    expect(r.accept(st(undefined, undefined))).toBe('apply');
  });
  it('forget makes the next frame a new run', () => {
    const r = new StateRev();
    r.accept(st(9, 'a'));
    r.forget();
    expect(r.accept(st(3, 'a'))).toBe('apply-new-run');
  });
  it('a malformed frame asks for state once, until a frame applies', () => {
    const r = new StateRev();
    expect(r.malformed()).toBe(false); // nothing numbered applied yet
    r.accept(st(1, 'a'));
    expect(r.malformed()).toBe(true);
    expect(r.malformed()).toBe(false); // already pending
    r.accept(st(2, 'a'));
    expect(r.malformed()).toBe(true);
  });
});
