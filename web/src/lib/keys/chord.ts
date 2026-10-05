// chordOf turns a keydown into the TUI's canonical chord string
// (internal/keymap/chord.go Chord.String: ctrl/alt/shift/super, then the key),
// or null for a key that is no chord on its own (a bare modifier).
//
// With ctrl, alt or meta held, a letter or digit is read from event.code, so
// macOS Option characters and keyboard layouts do not change the chord. With
// none held, a printable key is its own character with shift folded in —
// bubbletea reports Shift+5 as "%", and the tmux preset binds "${prefix} %".
export interface KeyEventLike {
  key: string;
  code: string;
  ctrlKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
  metaKey: boolean;
  isComposing?: boolean;
  // getModifierState('AltGraph'): AltGr is held. Windows reports it as
  // ctrl+alt too, so the flag is what tells a typed '@' from a chord.
  altGraph?: boolean;
  // The page runs on macOS, where Option composes characters (IS_MAC).
  mac?: boolean;
}

// IS_MAC is read once: Option composes characters on macOS only.
export const IS_MAC: boolean = (() => {
  if (typeof navigator === 'undefined') return false;
  const n = navigator as Navigator & { userAgentData?: { platform?: string } };
  return /mac/i.test(n.userAgentData?.platform ?? n.platform ?? '');
})();

const NAMED: Record<string, string> = {
  ArrowLeft: 'left',
  ArrowRight: 'right',
  ArrowUp: 'up',
  ArrowDown: 'down',
  PageUp: 'pgup',
  PageDown: 'pgdown',
  Home: 'home',
  End: 'end',
  Insert: 'insert',
  Delete: 'delete',
  Backspace: 'backspace',
  Enter: 'enter',
  Escape: 'esc',
  Tab: 'tab',
  ' ': 'space',
};

const NOT_A_KEY = new Set([
  'Control',
  'Alt',
  'Shift',
  'Meta',
  'AltGraph',
  'CapsLock',
  'NumLock',
  'ScrollLock',
  'OS',
  'Hyper',
  'Super',
  'Fn',
  'FnLock',
  'Dead',
  'Process',
  'Unidentified',
  'Compose',
]);

function mods(e: KeyEventLike, withShift: boolean): string {
  return (e.ctrlKey ? 'ctrl+' : '') + (e.altKey ? 'alt+' : '') + (withShift && e.shiftKey ? 'shift+' : '') + (e.metaKey ? 'super+' : '');
}

export function chordOf(e: KeyEventLike): string | null {
  if (NOT_A_KEY.has(e.key) || e.key === '') return null;
  const named = NAMED[e.key] ?? (/^F([1-9]|1[0-9]|2[0-4])$/.test(e.key) ? e.key.toLowerCase() : undefined);
  if (named) return mods(e, true) + named;
  const single = [...e.key].length === 1;
  const letter = /^Key([A-Z])$/.exec(e.code);
  const digit = /^Digit([0-9])$/.exec(e.code);
  const own = letter ? letter[1]!.toLowerCase() : digit ? digit[1]! : '';
  // The key gives its own letter or digit: a chord, whatever is held.
  const ownChar = own !== '' && e.key.toLowerCase() === own;
  // AltGr types a character (Swiss AltGr+2 is '@'): text, never a chord.
  // Chrome can report AltGraph for a left Ctrl+Alt, so a key that gives its
  // own letter stays the ctrl+alt chord.
  if (e.altGraph && single && !ownChar) return e.key;
  // Option alone on macOS types a character (German Option+5 is '['): an
  // ASCII character other than the key's own is that text; a non-ASCII one
  // (US Option+H is '˙') is still the chord alt+h. Elsewhere Alt does not
  // compose: AZERTY Alt+1 reports '&' and is alt+1.
  if (e.mac && own && e.altKey && !e.ctrlKey && !e.metaKey && single && /^[\x21-\x7e]$/.test(e.key) && !ownChar) {
    return e.key;
  }
  if (own && (e.ctrlKey || e.altKey || e.metaKey)) return mods(e, true) + own;
  if (!single) return null;
  // A symbol or a plain letter carries shift in the character itself.
  return mods(e, false) + e.key;
}

interface ElementLike {
  tagName: string;
  isContentEditable: boolean;
  classList: { contains(c: string): boolean };
}

// isEditable reports whether a key event's target is a field that owns its
// keys. xterm's hidden textarea is the terminal, not a field.
export function isEditable(t: unknown): boolean {
  if (typeof t !== 'object' || t === null || !('tagName' in t)) return false;
  const el = t as ElementLike;
  if (el.classList?.contains('xterm-helper-textarea')) return false;
  return el.isContentEditable === true || el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT';
}
