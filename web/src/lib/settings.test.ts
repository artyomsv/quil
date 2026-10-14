import { describe, expect, it } from 'vitest';
import type { SettingRow } from './files';
import { appliesLabel, overlayPolicy, parseInput, refreshesClient } from './settings';

const row = (key: string, kind: SettingRow['kind'], value: SettingRow['value'], applies = 'now'): SettingRow => ({
  key,
  group: 'general',
  label: key,
  kind,
  applies,
  value,
});

describe('settings helpers', () => {
  it('parses an input by kind', () => {
    expect(parseInput('int', '42')).toBe(42);
    expect(parseInput('int', '4.2')).toBeNull();
    expect(parseInput('int', '')).toBeNull();
    expect(parseInput('float', '0.35')).toBe(0.35);
    expect(parseInput('float', 'x')).toBeNull();
    expect(parseInput('bool', true)).toBe(true);
    expect(parseInput('duration', ' 30s ')).toBe('30s');
    expect(parseInput('string', 'img:1')).toBe('img:1');
  });

  it('labels when a row applies, overlay rows next start under --connect', () => {
    expect(appliesLabel(row('overlay.max_live', 'int', 1), false)).toBe('applies now');
    expect(appliesLabel(row('overlay.max_live', 'int', 1), true)).toBe('next start of the TUI and daemon');
    expect(appliesLabel(row('logging.level', 'enum', 'info', 'next start'), false)).toBe('next start of the TUI and daemon');
  });

  it('builds the overlay policy from the stored rows', () => {
    const rows = [row('overlay.idle_timeout_minutes', 'int', 7), row('overlay.max_live', 'int', 3)];
    expect(overlayPolicy(rows)).toEqual({ idle_timeout_minutes: 7, max_live: 3 });
  });

  it('knows which saves refresh /api/client', () => {
    expect(refreshesClient('keys.preset')).toBe(true);
    expect(refreshesClient('notification.events.idle')).toBe(true);
    expect(refreshesClient('sandbox.default_image')).toBe(true);
    expect(refreshesClient('ui.show_disclaimer')).toBe(true);
    expect(refreshesClient('logging.level')).toBe(false);
  });
});
