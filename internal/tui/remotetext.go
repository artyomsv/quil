package tui

import "strings"

// sanitizeRemoteText makes a string from the daemon safe to draw in a
// fixed-width dialog row.
//
// Phase 3 moved the setup dialog's directory listing from this machine's own
// disk to a daemon that may be remote (ssh). BrowseDirRespPayload's Name,
// Resolved, and Error strings are therefore no longer bytes this process
// wrote — the same trust boundary internal/transport's terminalSanitizer
// already exists to police for ssh's stderr, applied here on the render
// side instead of the wire side, and to text that lands in ONE fixed-width
// row rather than a scrolling log.
//
// C0 (U+0000-U+001F) and DEL (U+007F) are dropped — ESC included, so a
// filename cannot smuggle a CSI or OSC sequence into the frame. C1
// (U+0080-U+009F) is dropped too: internal/tui/oscfilter.go exists because
// this project's VT emulator already terminated an OSC string early on a raw
// U+009C landing inside what should have been an unrelated UTF-8 sequence,
// and U+009B is CSI's second, single-rune encoding — a live concern for a
// name this daemon did not generate itself, not a theoretical one.
//
// \t becomes a single space rather than being dropped, for the same reason
// firstErrLine (reconnect.go) converts tabs: lipgloss.Width measures a tab as
// ZERO cells, so a name padded with tabs would pass a width budget check here
// and then expand across the row once the terminal draws it, displacing the
// dialog frame. A space keeps the measured width equal to the drawn width.
//
// Every other rune survives byte-identical, including non-ASCII — a
// Cyrillic, CJK, or emoji directory name is legitimate and must not be
// mangled into ASCII.
//
// Returns s unchanged, with no allocation, when nothing needs stripping —
// the common case, since most directory listings contain no control bytes
// at all.
func sanitizeRemoteText(s string) string {
	if !strings.ContainsFunc(s, isRemoteTextControl) {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteByte(' ')
		case isRemoteTextControl(r):
			// C0 (\t already handled above), DEL, or C1 — dropped.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isRemoteTextControl reports whether r is a rune sanitizeRemoteText acts on:
// \t (mapped to a space there, not removed), a C0 control, DEL, or a C1
// control.
func isRemoteTextControl(r rune) bool {
	return r == '\t' || r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}
