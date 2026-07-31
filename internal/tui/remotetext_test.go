package tui

import (
	"testing"

	"charm.land/lipgloss/v2"
)

// Each case is built with string(rune(x)) rather than a raw byte literal for
// the C1 cases: the values arrive over IPC as JSON, and encoding/json only
// carries valid UTF-8 — a raw invalid byte never reaches this function in
// practice. string(rune(0x9b)) is Go's proper 2-byte UTF-8 encoding of
// U+009B, matching what a JSON-decoded C1 character actually looks like.
func TestSanitizeRemoteText_StripsControlClasses(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"NUL (C0)", "a\x00b", "ab"},
		{"ESC (C0)", "a" + string(rune(0x1b)) + "b", "ab"},
		{"BEL (C0)", "a\x07b", "ab"},
		{"DEL", "a\x7fb", "ab"},
		{"C1 low, U+0080", "a" + string(rune(0x80)) + "b", "ab"},
		{"C1 CSI, U+009B", "a" + string(rune(0x9b)) + "b", "ab"},
		{"C1 high, U+009F", "a" + string(rune(0x9f)) + "b", "ab"},
		{"tab becomes a space", "a\tb", "a b"},
		{"multiple controls in one name", "\x1bevil" + string(rune(0x9b)) + ".name\x7f", "evil.name"},
		// Bidi embeddings/overrides (U+202A-U+202E) and isolates
		// (U+2066-U+2069): this is the surface those exist to attack — a CWD
		// pick-list row where the raw value becomes the pane's spawn
		// directory, so a visually-reordered name would open the real path
		// while displaying a different one.
		{"LRE, U+202A", "a" + string(rune(0x202a)) + "b", "ab"},
		{"RLE, U+202B", "a" + string(rune(0x202b)) + "b", "ab"},
		{"PDF, U+202C", "a" + string(rune(0x202c)) + "b", "ab"},
		{"LRO, U+202D", "a" + string(rune(0x202d)) + "b", "ab"},
		{"RLO, U+202E", "a" + string(rune(0x202e)) + "b", "ab"},
		{"LRI, U+2066", "a" + string(rune(0x2066)) + "b", "ab"},
		{"RLI, U+2067", "a" + string(rune(0x2067)) + "b", "ab"},
		{"FSI, U+2068", "a" + string(rune(0x2068)) + "b", "ab"},
		{"PDI, U+2069", "a" + string(rune(0x2069)) + "b", "ab"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeRemoteText(tc.in); got != tc.want {
				t.Errorf("sanitizeRemoteText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The reported reproduction: a name that visually reads "gnp.txt.safe" (or
// similar, terminal-dependent) while the raw bytes are "safe" + RLO +
// "gnp.txt" — U+202E reorders everything after it right-to-left until a
// terminator or the end of the run. Pinned as its own test because it is the
// exact shape a spoofed extension attack takes, not just one more entry in
// the control-class table.
func TestSanitizeRemoteText_DropsBidiOverride_SpoofedExtension(t *testing.T) {
	in := "safe" + string(rune(0x202e)) + "gnp.txt"
	want := "safegnp.txt"
	if got := sanitizeRemoteText(in); got != want {
		t.Errorf("sanitizeRemoteText(%q) = %q, want %q", in, got, want)
	}
}

// ZWJ (U+200D) and combining marks are Unicode formatting/combining
// characters too, in the same general neighborhood as the bidi controls, but
// sanitizeRemoteText's range check is deliberately narrower than
// unicode.Is(unicode.Cf, r) and must not touch them. ZWJ composes multi-glyph
// emoji sequences (family/profession emoji); combining marks compose
// accented letters. Both are legitimate in a directory name.
func TestSanitizeRemoteText_PreservesZWJAndCombiningMarks(t *testing.T) {
	cases := []string{
		"👨‍👩‍👧‍👦",                  // man + ZWJ + woman + ZWJ + girl + ZWJ + boy
		"e" + string(rune(0x0301)), // "e" + COMBINING ACUTE ACCENT
	}
	for _, in := range cases {
		if got := sanitizeRemoteText(in); got != in {
			t.Errorf("sanitizeRemoteText(%q) = %q, want unchanged", in, got)
		}
	}
}

// The property that actually matters, per the design note on
// sanitizeRemoteText: lipgloss.Width must measure the same width the string
// will actually draw at, or a tab-padded name passes a row's width budget and
// then expands once rendered. For an ASCII input with no other control
// characters, that means lipgloss.Width of the result must equal the rune
// count — one cell per character, tabs included (now that they're spaces).
func TestSanitizeRemoteText_TabWidthMatchesRuneCount(t *testing.T) {
	in := "one\ttwo\tthree\tfour"
	got := sanitizeRemoteText(in)
	want := len([]rune(in))
	if w := lipgloss.Width(got); w != want {
		t.Errorf("lipgloss.Width(sanitizeRemoteText(%q)) = %d, want %d (rune count)", in, w, want)
	}
}

// Non-ASCII printable text is a legitimate directory name, not an attack, and
// must survive byte-identical — sanitizeRemoteText must not restrict to
// ASCII.
func TestSanitizeRemoteText_NonASCIISurvives(t *testing.T) {
	cases := []string{
		"Отчёт",          // Cyrillic
		"日本語のフォルダ",       // CJK
		"プロジェクト_2026",    // CJK + ASCII
		"🚀rocket📁folder", // emoji
	}
	for _, in := range cases {
		if got := sanitizeRemoteText(in); got != in {
			t.Errorf("sanitizeRemoteText(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestSanitizeRemoteText_CleanInputUnchanged(t *testing.T) {
	cases := []string{
		"",
		"a perfectly ordinary directory name",
		`C:\Users\dev\quil`,
		"/home/dev/projects",
	}
	for _, in := range cases {
		if got := sanitizeRemoteText(in); got != in {
			t.Errorf("sanitizeRemoteText(%q) = %q, want unchanged", in, got)
		}
	}
}

// Pins the "no allocation on the clean path" half of the contract: the
// common case — an ordinary directory listing with no control bytes at all
// — must not pay for a strings.Builder it will never need. Mirrors
// TestRingBuffer_WriteSteadyStateZeroAllocs's use of testing.AllocsPerRun.
func TestSanitizeRemoteText_CleanInputZeroAllocs(t *testing.T) {
	clean := "a perfectly ordinary directory name"
	var sink string
	allocs := testing.AllocsPerRun(100, func() {
		sink = sanitizeRemoteText(clean)
	})
	if allocs != 0 {
		t.Errorf("clean input allocates %.1f times per call, want 0", allocs)
	}
	_ = sink
}
