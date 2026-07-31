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
// Unicode bidi embedding/override controls (U+202A-U+202E) and isolates
// (U+2066-U+2069) are dropped too. This is the one surface in the codebase
// where they matter: this text is a list the user picks a spawn CWD from,
// and the RAW value — not what got drawn — becomes the pane's CWD and
// lazygit's --path argument. A name containing U+202E (RIGHT-TO-LEFT
// OVERRIDE) can display as something other than what it is while the thing
// actually opened is the real, un-reordered path — display spoofing on
// exactly the surface where the display is the decision.
//
// This is a TARGETED range check, not unicode.Is(unicode.Cf, r): that
// broader category also contains U+200D (ZERO WIDTH JOINER), which composes
// family/profession emoji sequences and must survive intact — a directory
// named after one would otherwise render as several disconnected glyphs
// instead of one. internal/claudesessions/claudesessions.go's sanitizeTitle
// strips the whole Cf category from the same class of remote-read text
// (session titles), which is safe there because a title is discarded prose,
// never reinterpreted as a path or an argument; a CWD candidate is, so
// narrowing to just the bidi ranges here is deliberate, not an oversight.
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
//
// PRECONDITION: s must already be valid UTF-8. An invalid byte sequence
// decodes rune-by-rune (via Go's range-over-string, which this function
// uses) to utf8.RuneError (U+FFFD) — a rune outside every range this
// function checks, so it passes through untouched rather than being
// stripped, and whether that happens can depend on unrelated bytes
// elsewhere in the same string (the ContainsFunc fast path and the main
// loop can disagree on a string that is invalid UTF-8, since both decode
// independently). This holds today because every caller's value arrived
// over IPC as JSON, and encoding/json only carries valid UTF-8 — invalid
// byte sequences are already coerced to U+FFFD by the daemon's own Marshal
// before this process's Unmarshal ever sees them, on both ends of the wire.
// A future caller feeding this function raw OS-level bytes (a filename read
// directly, bypassing JSON) would not get that guarantee for free.
func sanitizeRemoteText(s string) string {
	if !strings.ContainsFunc(s, isRemoteTextStripped) {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteByte(' ')
		case isRemoteTextStripped(r):
			// C0 (\t already handled above), DEL, C1, or a bidi
			// embedding/override/isolate control — dropped.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isRemoteTextStripped reports whether r is a rune sanitizeRemoteText acts
// on: \t (mapped to a space there, not removed), a C0 control, DEL, a C1
// control, or a Unicode bidi embedding/override/isolate control.
func isRemoteTextStripped(r rune) bool {
	switch {
	case r == '\t':
		return true
	case r < 0x20 || r == 0x7f: // C0 controls and DEL
		return true
	case r >= 0x80 && r <= 0x9f: // C1 controls
		return true
	case r >= 0x202a && r <= 0x202e: // bidi embeddings/overrides (LRE..RLO)
		return true
	case r >= 0x2066 && r <= 0x2069: // bidi isolates (LRI..PDI)
		return true
	default:
		return false
	}
}
