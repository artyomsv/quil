import type { StageUpdateResp, UpdateInfo } from './protocol';
import type { Outcome } from './requests';

// updateLine is the Update page's status. A frame without `update` does NOT
// mean up to date: the daemon also sends none with checking off, on a dev
// build, or before its first check.
export function updateLine(u: UpdateInfo | undefined, daemonVersion: string): string {
  const head = `Daemon ${daemonVersion || 'unknown'}`;
  if (!u) return `${head} — no update reported by the daemon`;
  if (u.staged_version) {
    return `${head} — version ${u.staged_version} is downloaded; it is applied when a TUI next starts on that machine`;
  }
  return `${head} — version ${u.latest_version} is available`;
}

// downloadRefusal is why "Download" is greyed, '' when it may run.
export function downloadRefusal(u: UpdateInfo | undefined, rights: string, connect: boolean): string {
  if (!u) return 'no update reported';
  if (rights !== 'full') return 'needs full rights';
  if (connect) return 'a remote daemon is updated on its own machine';
  if (!u.install_writable) return 'the install folder is not writable';
  return '';
}

// stageResult reads a stage_update_req answer.
export function stageResult(o: Outcome): string {
  const p = (o.reply?.payload ?? null) as StageUpdateResp | null;
  if (p?.success) return `Version ${p.version ?? ''} is downloaded`;
  if (p?.already_staged) return `Version ${p.version ?? ''} was already downloaded`;
  if (p?.check_failed) return 'The update check failed';
  return `Download failed: ${p?.error || (o.ok ? 'unknown' : o.error)}`;
}
