import { describe, expect, it } from 'vitest';
import { chordOf, isEditable } from './chord';

const ev = (key: string, code: string, mods: Partial<Record<'ctrlKey' | 'altKey' | 'shiftKey' | 'metaKey', boolean>> = {}) => ({
  key,
  code,
  ctrlKey: false,
  altKey: false,
  shiftKey: false,
  metaKey: false,
  ...mods,
});

describe('chordOf', () => {
  it('names a ctrl letter by its code', () => {
    expect(chordOf(ev('b', 'KeyB', { ctrlKey: true }))).toBe('ctrl+b');
  });
  it('keeps shift as a modifier when ctrl or alt is held', () => {
    expect(chordOf(ev('H', 'KeyH', { altKey: true, shiftKey: true }))).toBe('alt+shift+h');
  });
  it('reads the letter from the code for a non-ASCII Option character (US Option+H)', () => {
    expect(chordOf(ev('˙', 'KeyH', { altKey: true }))).toBe('alt+h');
  });
  it('gives an AltGr character as text, not ctrl+alt (Swiss AltGr+2)', () => {
    expect(chordOf({ ...ev('@', 'Digit2', { ctrlKey: true, altKey: true }), altGraph: true })).toBe('@');
  });
  it('keeps ctrl+alt on a key that gives its own letter, even with AltGraph reported', () => {
    expect(chordOf({ ...ev('a', 'KeyA', { ctrlKey: true, altKey: true }), altGraph: true })).toBe('ctrl+alt+a');
  });
  it('gives an ASCII Option character other than the key as text on macOS (German Mac Option+5)', () => {
    expect(chordOf({ ...ev('[', 'Digit5', { altKey: true }), mac: true })).toBe('[');
    expect(chordOf({ ...ev('@', 'KeyL', { altKey: true }), mac: true })).toBe('@');
  });
  it('never composes with Alt off macOS', () => {
    // AZERTY Windows Alt+1 reports '&'.
    expect(chordOf(ev('&', 'Digit1', { altKey: true }))).toBe('alt+1');
    // US Alt+Shift+1 on Windows reports '!'.
    expect(chordOf(ev('!', 'Digit1', { altKey: true, shiftKey: true }))).toBe('alt+shift+1');
  });
  it('still reads alt with the key own letter or digit as a chord', () => {
    expect(chordOf(ev('h', 'KeyH', { altKey: true }))).toBe('alt+h');
    expect(chordOf(ev('5', 'Digit5', { altKey: true }))).toBe('alt+5');
    expect(chordOf(ev('[', 'BracketLeft', { altKey: true }))).toBe('alt+[');
  });
  it('names alt digits by their code', () => {
    expect(chordOf(ev('1', 'Digit1', { altKey: true }))).toBe('alt+1');
  });
  it('gives a shifted symbol as the symbol, without shift', () => {
    expect(chordOf(ev('%', 'Digit5', { shiftKey: true }))).toBe('%');
    expect(chordOf(ev('"', 'Quote', { shiftKey: true }))).toBe('"');
  });
  it('keeps a plain letter case-sensitive like the TUI', () => {
    expect(chordOf(ev('z', 'KeyZ'))).toBe('z');
    expect(chordOf(ev('Z', 'KeyZ', { shiftKey: true }))).toBe('Z');
  });
  it('maps named keys to the TUI names', () => {
    expect(chordOf(ev('ArrowLeft', 'ArrowLeft', { altKey: true }))).toBe('alt+left');
    expect(chordOf(ev('PageUp', 'PageUp', { altKey: true, shiftKey: true }))).toBe('alt+shift+pgup');
    expect(chordOf(ev('Escape', 'Escape'))).toBe('esc');
    expect(chordOf(ev('F1', 'F1'))).toBe('f1');
    expect(chordOf(ev('F2', 'F2', { altKey: true }))).toBe('alt+f2');
    expect(chordOf(ev('Backspace', 'Backspace', { altKey: true }))).toBe('alt+backspace');
    expect(chordOf(ev(' ', 'Space', { ctrlKey: true }))).toBe('ctrl+space');
  });
  it('maps meta to super', () => {
    expect(chordOf(ev('q', 'KeyQ', { metaKey: true }))).toBe('super+q');
  });
  it('returns null for a modifier pressed alone', () => {
    for (const k of ['Control', 'Alt', 'Shift', 'Meta', 'AltGraph', 'CapsLock', 'Dead', 'Unidentified', 'Process']) {
      expect(chordOf(ev(k, ''))).toBeNull();
    }
  });
});

describe('isEditable', () => {
  const el = (tagName: string, cls = '', editable = false) => ({
    tagName,
    isContentEditable: editable,
    classList: { contains: (c: string) => c === cls },
  });
  it('treats form fields as editable', () => {
    expect(isEditable(el('INPUT'))).toBe(true);
    expect(isEditable(el('TEXTAREA'))).toBe(true);
    expect(isEditable(el('DIV', '', true))).toBe(true);
  });
  it("never treats xterm's own textarea as a field", () => {
    expect(isEditable(el('TEXTAREA', 'xterm-helper-textarea'))).toBe(false);
  });
  it('ignores anything else', () => {
    expect(isEditable(null)).toBe(false);
    expect(isEditable(el('DIV'))).toBe(false);
  });
});
