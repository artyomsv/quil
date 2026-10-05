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
}

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
  if (e.ctrlKey || e.altKey || e.metaKey) {
    const letter = /^Key([A-Z])$/.exec(e.code);
    if (letter) return mods(e, true) + letter[1]!.toLowerCase();
    const digit = /^Digit([0-9])$/.exec(e.code);
    if (digit) return mods(e, true) + digit[1]!;
  }
  if ([...e.key].length !== 1) return null;
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
