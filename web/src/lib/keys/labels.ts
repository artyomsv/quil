import type { WebKeymap } from './engine';

// keyFor is the key that runs an action in this browser, for a menu row
// (spec §5.5, "menus show what works here"): the resolved binding, which is
// the fallback for a browser-reserved chord, or '' when none works here. id
// is an action id, or builtin.<name> for a key the TUI hardcodes.
export function keyFor(km: WebKeymap | null, id: string): string {
  if (!km) return '';
  const builtin = id.startsWith('builtin.') ? id.slice('builtin.'.length) : '';
  const keys = builtin ? km.builtins.find((b) => b.id === builtin)?.keys : km.actions.find((a) => a.id === id)?.keys;
  return keys?.[0] ?? '';
}

// keyTarget is where keys go: the shown overlay owns them, else the active
// pane.
export function keyTarget(overlayId: string | undefined, activePane: string): { pane: string; overlay: boolean } {
  return overlayId ? { pane: overlayId, overlay: true } : { pane: activePane, overlay: false };
}
