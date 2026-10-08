import { describe, expect, it } from 'vitest';
import { downloadRefusal, stageResult, updateLine } from './update';

describe('update', () => {
  it('never reads a missing update as up to date', () => {
    expect(updateLine(undefined, '1.87.1')).toBe('Daemon 1.87.1 — no update reported by the daemon');
    expect(updateLine(undefined, '')).toBe('Daemon unknown — no update reported by the daemon');
  });
  it('names the latest and the staged version', () => {
    expect(updateLine({ latest_version: '1.88.0', install_writable: true }, '1.87.1')).toBe(
      'Daemon 1.87.1 — version 1.88.0 is available',
    );
    expect(updateLine({ latest_version: '1.88.0', staged_version: '1.88.0', install_writable: true }, '1.87.1')).toBe(
      'Daemon 1.87.1 — version 1.88.0 is downloaded; it is applied when a TUI next starts on that machine',
    );
  });
  it.each([
    [undefined, 'full', false, 'no update reported'],
    [{ latest_version: '2', install_writable: false }, 'full', false, 'the install folder is not writable'],
    [{ latest_version: '2', install_writable: true }, 'standard', false, 'needs full rights'],
    [{ latest_version: '2', install_writable: true }, 'full', true, 'a remote daemon is updated on its own machine'],
    [{ latest_version: '2', install_writable: true }, 'full', false, ''],
  ] as const)('download refusal %#', (u, rights, connect, want) => {
    expect(downloadRefusal(u, rights, connect)).toBe(want);
  });
  it('reads every stage answer', () => {
    expect(stageResult({ ok: true, reply: { type: 'x', payload: { success: true, version: '2' } } })).toBe(
      'Version 2 is downloaded',
    );
    expect(
      stageResult({
        ok: false,
        code: 'failed',
        error: 'not done',
        reply: { type: 'x', payload: { success: false, already_staged: true, version: '2' } },
      }),
    ).toBe('Version 2 was already downloaded');
    expect(
      stageResult({ ok: false, code: 'failed', error: 'not done', reply: { type: 'x', payload: { success: false, check_failed: true } } }),
    ).toBe('The update check failed');
    expect(
      stageResult({ ok: false, code: 'failed', error: 'disk', reply: { type: 'x', payload: { success: false, error: 'disk' } } }),
    ).toBe('Download failed: disk');
    expect(stageResult({ ok: false, code: 'timeout', error: 'No answer from the daemon' })).toBe(
      'Download failed: No answer from the daemon',
    );
  });
});
