// sanitizeRemoteText makes a name from a daemon the user may not control safe
// to render: C0 controls, DEL, C1 controls (including the CSI introducer
// U+009B) and bidi overrides and isolates are removed; tab becomes a space.
// Printable text, including non-ASCII, is kept byte for byte.
export function sanitizeRemoteText(s: string): string {
  let out = '';
  for (const ch of s) {
    const c = ch.codePointAt(0) ?? 0;
    if (c === 0x09) {
      out += ' ';
      continue;
    }
    if (c < 0x20 || c === 0x7f || (c >= 0x80 && c <= 0x9f)) continue;
    if ((c >= 0x202a && c <= 0x202e) || (c >= 0x2066 && c <= 0x2069)) continue;
    out += ch;
  }
  return out;
}

// sanitizeBlock is sanitizeRemoteText for multi-line text (a note, a history
// entry, a session prompt): line breaks and tabs are the text's own
// structure and stay; every other control and bidi override goes, as in the
// TUI's block rule (internal/tui/sharednotes.go).
export function sanitizeBlock(s: string): string {
  let out = '';
  for (const ch of s) {
    const c = ch.codePointAt(0) ?? 0;
    if (c === 0x0a || c === 0x09) {
      out += ch;
      continue;
    }
    if (c < 0x20 || c === 0x7f || (c >= 0x80 && c <= 0x9f)) continue;
    if ((c >= 0x202a && c <= 0x202e) || (c >= 0x2066 && c <= 0x2069)) continue;
    out += ch;
  }
  return out;
}
