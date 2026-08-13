package notify

import (
	"encoding/xml"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxFieldRunes bounds each rendered field.
//
// Separate from sanitising on purpose: a control-character filter preserves
// printable non-ASCII byte-identically, so it removes escapes without
// shortening anything. An unbounded project name would otherwise produce a
// toast payload Windows truncates or rejects on its own terms, which is a
// failure the user sees and we cannot explain.
const MaxFieldRunes = 120

// Notification is one toast.
//
// Title and Body are expected to have already been through the caller's
// remote-text sanitiser — internal/tui does that at the build site, matching
// its render-only rule, since a project name round-trips back to the daemon on
// rename and must not be rewritten in state. This package escapes and bounds
// them, and independently drops anything that survived.
type Notification struct {
	Title  string
	Body   string
	Tag    string // per-pane key, used to withdraw the toast later
	Launch string // activation URI; empty means a toast with no click target
}

// BuildToastXML renders the ToastGeneric payload.
//
// activationType="protocol" is what lets the click reach a registered URI
// handler with no COM activator — the alternative needs a CLSID and a
// hand-implemented INotificationActivationCallback vtable, with no CGo
// available.
func BuildToastXML(n Notification) string {
	var b strings.Builder
	b.WriteString(`<toast`)
	if n.Launch != "" {
		b.WriteString(` activationType="protocol" launch="`)
		b.WriteString(escape(clean(n.Launch)))
		b.WriteString(`"`)
	}
	b.WriteString(`><visual><binding template="ToastGeneric">`)
	b.WriteString(`<text>`)
	b.WriteString(escape(clean(n.Title)))
	b.WriteString(`</text><text>`)
	b.WriteString(escape(clean(n.Body)))
	b.WriteString(`</text></binding></visual></toast>`)
	return b.String()
}

// clean drops what must never reach a rendered surface, then bounds what is
// left. Order matters: bounding first could cut a multi-byte sequence and
// leave a replacement character behind for the filter to keep.
func clean(s string) string {
	return truncateRunes(sanitize(s), MaxFieldRunes)
}

// sanitize removes C0/DEL, the C1 range (including U+009B, the CSI introducer
// internal/tui/oscfilter.go exists because of), and the bidi overrides and
// isolates. Tab, newline and carriage return become spaces so a rendered width
// matches a measured one.
//
// Printable non-ASCII is preserved byte-identically — this is a control
// filter, not a transliteration. The sidebar's own glyphs and any non-Latin
// project name depend on that.
func sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t', r == '\n', r == '\r':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7f:
			// C0 and DEL.
		case r >= 0x80 && r <= 0x9f:
			// C1, including U+009B.
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			// Bidi overrides and isolates. These are PRINTABLE, so no
			// control-character test catches them and no XML escape touches
			// them — U+202E alone reverses the rest of the rendered line.
		case r == utf8.RuneError:
			// Invalid UTF-8 that range already replaced. Dropped rather than
			// kept, so a mangled remote name cannot smuggle U+FFFD into a
			// field the caller believes it truncated cleanly.
		case r != ' ' && !unicode.IsPrint(r):
			// Everything else non-printable, including the remaining Cf.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// truncateRunes cuts on a rune boundary. Cutting on a byte index would emit
// half a rune into a document Windows parses as XML.
func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i] + "…"
		}
		n++
	}
	return s
}

// escape uses encoding/xml so the escaping rules are the standard library's
// rather than a hand-rolled replacer that forgets a case.
func escape(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return ""
	}
	return b.String()
}
