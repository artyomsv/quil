package keymap

import "testing"

func TestParseChord_Canonicalizes(t *testing.T) {
	tests := []struct{ name, input, want string }{
		{"plain key", "a", "a"},
		{"single mod", "ctrl+a", "ctrl+a"},
		{"mod order normalized", "shift+ctrl+a", "ctrl+shift+a"},
		{"all mods", "shift+alt+ctrl+f2", "ctrl+alt+shift+f2"},
		{"super preserved", "super+q", "super+q"},
		{"meta folds to super", "meta+q", "super+q"},
		// Modifier names fold; a one-character base key does NOT. MatchTier
		// parses the incoming press too, and bubbletea reports Option+Shift+A
		// as {Code:'A', Mod:ModAlt} — so folding the base here would make the
		// shifted press dispatch the lowercase chord's action.
		{"modifier folded, base key kept", "Ctrl+A", "ctrl+A"},
		{"named key folded", "Alt+PageUp", "alt+pgup"},
		{"escape alias", "escape", "esc"},
		{"pageup alias", "pageup", "pgup"},
		{"pgdn alias", "pgdn", "pgdown"},
		{"return alias", "return", "enter"},
		{"space key", "space", "space"},
		{"tab key", "tab", "tab"},
		{"function key", "f12", "f12"},
		{"literal plus", "alt++", "alt++"},
		{"whitespace trimmed", "  ctrl+a  ", "ctrl+a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := ParseChord(tt.input)
			if err != nil {
				t.Fatalf("ParseChord(%q) error: %v", tt.input, err)
			}
			if got := c.String(); got != tt.want {
				t.Errorf("= %q, want %q", got, tt.want)
			}
		})
	}
}

// TestParseChord_ShiftedMetaLetterIsItsOwnChord pins the parser half of the
// case-folding fix. alt+M and alt+m must be DIFFERENT chords: MatchTier runs the
// incoming key through this same function, so a fold here silently routes
// Option+Shift+<letter> on macOS Terminal.app ("Use Option as Meta key") to
// whatever the lowercase chord is bound to instead of the PTY.
// internal/tui's TestUpdate_ShiftedMetaLetterReachesThePTY pins the dispatch
// half, which is where the bug actually bit.
func TestParseChord_ShiftedMetaLetterIsItsOwnChord(t *testing.T) {
	upper, err := ParseChord("alt+M")
	if err != nil {
		t.Fatalf("ParseChord(alt+M): %v", err)
	}
	lower, err := ParseChord("alt+m")
	if err != nil {
		t.Fatalf("ParseChord(alt+m): %v", err)
	}
	if upper.String() == lower.String() {
		t.Errorf("alt+M and alt+m both canonicalize to %q — a shifted Meta letter "+
			"would dispatch the lowercase chord's action", upper.String())
	}
	if got := upper.String(); got != "alt+M" {
		t.Errorf("ParseChord(alt+M).String() = %q, want %q", got, "alt+M")
	}
}

func TestParseChord_Rejects(t *testing.T) {
	for _, in := range []string{
		"", "   ", "ctrl+", "ctrl+alt",
		// A base key is a key NAME. Anything that can steer a terminal is
		// refused at the parser so no renderer has to defend itself: the F1 key
		// column and the palette's detail column both draw Chord.Key raw, and
		// both truncate with an ANSI-aware helper — so an escape measures zero
		// cells and survives every width budget intact.
		"\x1b]52;c;aGk=\a",  // OSC 52 - sets the system clipboard
		"ctrl+\x1b[31m",     // CSI, smuggled behind a valid modifier
		"alt+\u009b",        // C1 CSI: one rune, invisible to an r < 0x20 test
		"alt+\x9b",          // the same control as a raw byte: invalid UTF-8
		"alt+\x7f",          // DEL
		"alt+\u202egnp.exe", // bidi override - printable, reverses the row
		"alt+\u2066spoofed", // bidi isolate
	} {
		t.Run(in, func(t *testing.T) {
			if _, err := ParseChord(in); err == nil {
				t.Errorf("ParseChord(%q) = nil error, want error", in)
			}
		})
	}
}

// TestChord_CanonicalFormIsStable checks that every shipped default is already
// its own canonical form: parsing it and rendering it back must return the same
// string, or the binding is stored under one key and looked up under another.
//
// It does NOT check agreement with bubbletea, and the name now says so. The
// previous name (TestChord_RoundTripsBubbleTea) and comment claimed it did,
// while the body compares the parser only against itself — no tea.KeyPressMsg
// appears anywhere in this package, which imports stdlib only.
// internal/tui's TestKeymap_EveryShippedDefaultMatchesARealKeyPress is the test
// that builds real presses and compares.
func TestChord_CanonicalFormIsStable(t *testing.T) {
	// Every chord shipped in config.Default().Keybindings.
	shipped := []string{
		"ctrl+q", "ctrl+t", "ctrl+w", "alt+w", "alt+shift+h", "alt+shift+v",
		"alt+left", "alt+right", "alt+up", "alt+down", "f2", "alt+f2",
		"alt+shift+r", "alt+c", "alt+pgup", "alt+pgdown", "ctrl+v", "ctrl+j",
		"alt+a", "ctrl+e", "alt+n", "f3", "alt+m", "alt+r", "alt+backspace",
		"alt+e", "alt+shift+l", "alt+shift+e", "alt+shift+i", "alt+g",
		"alt+shift+w", "alt+shift+p", "alt+shift+s", "alt+p", "alt+o",
		"alt+shift+right", "alt+shift+left", "alt+shift+a", "alt+shift+n",
		"alt+shift+x",
	}
	for _, s := range shipped {
		t.Run(s, func(t *testing.T) {
			c, err := ParseChord(s)
			if err != nil {
				t.Fatalf("ParseChord(%q) error: %v", s, err)
			}
			if got := c.String(); got != s {
				t.Errorf("canonical form = %q — the default is spelled differently from the "+
					"form it parses to, so it would be stored and looked up under different keys", got)
			}
		})
	}
}
