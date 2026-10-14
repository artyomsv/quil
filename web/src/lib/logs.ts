import type { Api, LogName, LogText } from './files';
import { sanitizeBlock } from './sanitize';

export function LOG_TABS(connect: boolean): { name: LogName; label: string }[] {
  return [
    { name: 'client', label: 'Client' },
    { name: 'daemon', label: connect ? 'Daemon (this machine)' : 'Daemon' },
    { name: 'mcp', label: 'MCP' },
    { name: 'web', label: 'Web' },
  ];
}

// logBody is what the Logs page shows: the sanitized text, or a note.
export function logBody(r: Api<LogText>): { text: string; note: string; truncated: boolean } {
  if (!r.ok) return { text: '', note: r.error, truncated: false };
  if (r.value.missing) return { text: '', note: 'No log file yet.', truncated: false };
  return { text: sanitizeBlock(r.value.text), note: '', truncated: r.value.truncated };
}

// latestOnly answers each awaited value only while no later call has been
// made: a tab clicked before the last one's log arrived must not show that
// log under the new tab's name. A superseded answer is null.
export function latestOnly<T>(): (p: Promise<T>) => Promise<T | null> {
  let gen = 0;
  return async (p) => {
    const mine = ++gen;
    const v = await p;
    return mine === gen ? v : null;
  };
}

export const KIND_LABELS: Record<string, string> = {
  added: 'Added',
  changed: 'Changed',
  deprecated: 'Deprecated',
  removed: 'Removed',
  fixed: 'Fixed',
  security: 'Security',
};
