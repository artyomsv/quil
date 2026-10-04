import type { SplitPaneReq, WorkspaceState } from './protocol';

// The TUI's tab colour palette (internal/tui/model.go tabColors), shown with
// the colours a terminal draws for those ANSI numbers.
export const TAB_COLORS: { value: string; label: string; css: string }[] = [
  { value: '', label: 'Default', css: 'transparent' },
  { value: '1', label: 'Red', css: '#e06c75' },
  { value: '2', label: 'Green', css: '#98c379' },
  { value: '3', label: 'Yellow', css: '#e5c07b' },
  { value: '4', label: 'Blue', css: '#61afef' },
  { value: '5', label: 'Magenta', css: '#c678dd' },
  { value: '6', label: 'Cyan', css: '#56b6c2' },
  { value: '208', label: 'Orange', css: '#ff8700' },
];

// tabColorCss is the CSS colour a tab's stored colour draws with; an unknown
// value draws as the default.
export function tabColorCss(value: string): string {
  return TAB_COLORS.find((c) => c.value === value)?.css ?? 'transparent';
}

// nextTabColor is the TAB_COLORS value after cur; an unknown value restarts
// at the first entry ("none").
export function nextTabColor(cur: string): string {
  const i = TAB_COLORS.findIndex((c) => c.value === cur);
  if (i < 0) return TAB_COLORS[0]?.value ?? '';
  return TAB_COLORS[(i + 1) % TAB_COLORS.length]?.value ?? '';
}

export function projectRootOf(s: WorkspaceState, tabId: string): string {
  const tab = s.tabs.find((t) => t.id === tabId);
  return s.projects.find((p) => p.id === tab?.project_id)?.root_dir ?? '';
}

// A browser create always carries a cwd (spec §7): the pane's own folder,
// else its project's root.
export function cwdForSplit(s: WorkspaceState, paneId: string): string {
  const p = s.panes.find((x) => x.id === paneId);
  return p?.cwd || projectRootOf(s, p?.tab_id ?? s.active_tab);
}

export function quickSplit(s: WorkspaceState, paneId: string, placement: 'right' | 'below'): SplitPaneReq {
  return { target_pane_id: paneId, placement, pane: { type: 'terminal', cwd: cwdForSplit(s, paneId) } };
}
