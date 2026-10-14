import type { SettingRow } from './files';

export const OVERLAY_KEYS = new Set(['overlay.idle_timeout_minutes', 'overlay.max_live']);

// parseInput turns a field's raw value into the JSON value the PATCH sends,
// null when it is not a value of the row's kind (nothing is sent then).
export function parseInput(kind: SettingRow['kind'], raw: string | boolean): boolean | number | string | null {
  if (kind === 'bool') return typeof raw === 'boolean' ? raw : raw === 'true';
  const s = String(raw).trim();
  if (kind === 'int') return /^-?\d+$/.test(s) ? Number(s) : null;
  if (kind === 'float') {
    const n = Number(s);
    return s !== '' && Number.isFinite(n) ? n : null;
  }
  return kind === 'duration' ? s : String(raw);
}

// appliesLabel: overlay rows apply now only where the page pushes them
// (local mode, spec 5d §5.4).
export function appliesLabel(row: SettingRow, connect: boolean): string {
  const now = row.applies === 'now' && !(connect && OVERLAY_KEYS.has(row.key));
  return now ? 'applies now' : 'next start of the TUI and daemon';
}

export function overlayPolicy(rows: SettingRow[]): { idle_timeout_minutes: number; max_live: number } {
  const v = (k: string): number => Number(rows.find((r) => r.key === k)?.value ?? 0);
  return { idle_timeout_minutes: v('overlay.idle_timeout_minutes'), max_live: v('overlay.max_live') };
}

// refreshesClient: these saves change what /api/client tells the page.
export function refreshesClient(key: string): boolean {
  return key.startsWith('keys.') || key.startsWith('notification.') || key.startsWith('sandbox.') || key === 'ui.show_disclaimer';
}
