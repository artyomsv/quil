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

export const KIND_LABELS: Record<string, string> = {
  added: 'Added',
  changed: 'Changed',
  deprecated: 'Deprecated',
  removed: 'Removed',
  fixed: 'Fixed',
  security: 'Security',
};
