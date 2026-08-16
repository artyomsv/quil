// Package keymap owns Quil's key-to-action mapping: canonical chords, the
// action registry, and the lookup tables the TUI dispatches through.
//
// It imports stdlib only. Keeping config and tui out means the whole package
// is testable without QUIL_HOME and without building a Model.
package keymap

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Mod is a bitmask of chord modifiers.
type Mod uint8

const (
	ModCtrl Mod = 1 << iota
	ModAlt
	ModShift
	// ModSuper is Cmd on macOS, Win on Windows. kbMatches was an exact string
	// compare, so a config saying "super+q" works today — rejecting it here
	// would break a working binding.
	ModSuper
)

// Chord is one key press: zero or more modifiers plus a base key.
type Chord struct {
	Mods Mod
	Key  string
}

// modNames folds modifier spellings onto one bit. hyper and meta both appear
// in terminal documentation for what bubbletea reports as super.
var modNames = map[string]Mod{
	"ctrl": ModCtrl, "alt": ModAlt, "shift": ModShift,
	"super": ModSuper, "meta": ModSuper, "hyper": ModSuper,
}

// keyAliases folds spellings that mean the same key. The codebase already
// carries both "escape" (rename path) and "esc" (selection path).
var keyAliases = map[string]string{
	"escape": "esc", "pageup": "pgup", "pagedown": "pgdown",
	"pgdn": "pgdown", "return": "enter",
	// "comma" is the only way to BIND the comma key, because "," separates
	// alternatives in a spec and there is no escape: "ctrl+b ," splits into
	// "ctrl+b " and "", which ParseSpec rejects as an empty alternative. tmux
	// binds rename-window to the prefix followed by a comma, so without this
	// the tmux preset cannot express one of its core bindings.
	//
	// The canonical form is still "," — String must match what bubbletea
	// reports for a real press, and it reports the bare symbol.
	"comma": ",",
}

// ParseChord parses a chord in any accepted spelling and returns its canonical
// form. Input modifier order is irrelevant; String renders a fixed order.
//
// Case folds on modifier names and on NAMED keys ("Escape", "PgUp", "F2"), but
// NOT on a single-character base key, and that asymmetry is load-bearing.
// MatchTier runs the INCOMING key through this same parser, so folding the base
// would make "alt+M" and "alt+m" one chord on both the config and the press
// side. bubbletea's legacy ESC-prefix Meta decoding reports Option+Shift+M as
// {Code:'M', Mod:ModAlt} — case preserved, no ModShift — so on macOS
// Terminal.app with "Use Option as Meta key" every shifted Meta letter would be
// handed to whatever the lowercase chord is bound to, instead of reaching the
// PTY. keyAliases and modNames are keyed on lowercase, so named keys and
// modifiers keep working; only the one-rune keys stay case-sensitive, which is
// exactly the set bubbletea distinguishes.
func ParseChord(s string) (Chord, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Chord{}, fmt.Errorf("empty chord")
	}
	var c Chord
	rest := s
	for {
		plus := strings.Index(rest, "+")
		// No separator left, or rest IS the literal "+" key ("alt++" strips
		// "alt", leaving rest == "+"): whatever remains is the base key.
		// Checking plus == len(rest)-1 instead of rest == "+" was the bug:
		// it also matched "ctrl+", where a valid modifier is immediately
		// followed by end-of-string, and silently kept "ctrl+" as the key
		// instead of reporting a dangling modifier.
		if plus < 0 || rest == "+" {
			break
		}
		name := rest[:plus]
		mod, ok := modNames[strings.ToLower(name)]
		if !ok {
			// A non-modifier before a "+" means the base key sits in a
			// modifier position, e.g. "a+ctrl".
			return Chord{}, fmt.Errorf("chord %q: %q is not a modifier", s, name)
		}
		c.Mods |= mod
		rest = rest[plus+1:]
	}
	if rest == "" {
		return Chord{}, fmt.Errorf("chord %q ends with a modifier", s)
	}
	// Rune count, not byte length: "alt+Ä" is one key press, and folding it
	// would collide with "alt+ä" the same way "alt+M" collided with "alt+m".
	if utf8.RuneCountInString(rest) > 1 {
		rest = strings.ToLower(rest)
	}
	if _, isMod := modNames[rest]; isMod {
		return Chord{}, fmt.Errorf("chord %q has no base key", s)
	}
	if alias, ok := keyAliases[rest]; ok {
		rest = alias
	}
	if err := validateBaseKey(s, rest); err != nil {
		return Chord{}, err
	}
	c.Key = rest
	return c, nil
}

// validateBaseKey rejects a base key that must never reach a renderer. spec is
// the whole chord, carried only for the error message.
//
// This is a PARSER-level check on purpose: a Chord's Key is rendered raw by
// F1 -> Shortcuts (dialogKeyStyle pads, it does not clip) and right-aligned as
// the command palette's detail column, and both truncate with an ANSI-aware
// width helper — so an escape sequence measures zero cells, survives every
// width budget intact, and is written straight to the terminal. `quit =
// "]52;c;<base64>\a"` parsed cleanly before this check and set the system
// clipboard the moment the user pressed F1. Rejecting here fixes every consumer
// at once, which is why neither the dialog nor the palette grows a sanitizer of
// its own.
//
// The set matches internal/tui's sanitizeRemoteText: C0 and DEL, C1 (U+009B is
// CSI's single-rune encoding, which no r < 0x20 test catches), and the bidi
// embedding/override/isolate controls — printable, so they pass a control-
// character filter while reversing the text of the row a user reads to find out
// what their keys do.
//
// A rejected spec is not silently dropped: Build reports ConflictMalformed and
// falls back to that action's shipped default.
func validateBaseKey(spec, key string) error {
	// Invalid UTF-8 is refused outright rather than scanned, and that is not
	// belt-and-braces: ranging a Go string decodes a stray 0x9B byte to
	// utf8.RuneError, which is in none of the ranges below, so a raw C1 byte
	// would pass a scan that a properly encoded U+009B fails. Nothing
	// downstream can measure it either.
	if !utf8.ValidString(key) {
		return fmt.Errorf("chord %q: base key is not valid UTF-8", spec)
	}
	for _, r := range key {
		switch {
		case r < 0x20 || r == 0x7f, // C0 controls and DEL
			r >= 0x80 && r <= 0x9f,     // C1 controls
			r >= 0x202a && r <= 0x202e, // bidi embeddings/overrides (LRE..RLO)
			r >= 0x2066 && r <= 0x2069: // bidi isolates (LRI..PDI)
			return fmt.Errorf("chord %q: base key contains U+%04X, which is not a key name", spec, r)
		}
	}
	return nil
}

// String renders the canonical form: modifiers in fixed ctrl/alt/shift/super
// order, the base key as parsed. This must match bubbletea's
// KeyPressMsg.String(), or a configured chord can never match a real press.
//
// internal/tui's TestKeymap_EveryShippedDefaultMatchesARealKeyPress is what
// pins that, because it is the only test in the tree that builds a
// tea.KeyPressMsg and compares. This package cannot: it imports stdlib only, so
// TestChord_CanonicalFormIsStable next door can check the canonical form is
// stable but never that bubbletea agrees with it.
func (c Chord) String() string {
	var b strings.Builder
	for _, m := range []struct {
		bit  Mod
		name string
	}{{ModCtrl, "ctrl+"}, {ModAlt, "alt+"}, {ModShift, "shift+"}, {ModSuper, "super+"}} {
		if c.Mods&m.bit != 0 {
			b.WriteString(m.name)
		}
	}
	b.WriteString(c.Key)
	return b.String()
}
