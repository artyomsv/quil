// Package keymap owns Quil's key-to-action mapping: canonical chords, the
// action registry, and the lookup tables the TUI dispatches through.
//
// It imports stdlib only. Keeping config and tui out means the whole package
// is testable without QUIL_HOME and without building a Model.
package keymap

import (
	"fmt"
	"strings"
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
}

// ParseChord parses a chord in any accepted spelling and returns its canonical
// form. Input modifier order is irrelevant; String renders a fixed order.
func ParseChord(s string) (Chord, error) {
	s = strings.TrimSpace(strings.ToLower(s))
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
		mod, ok := modNames[name]
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
	if _, isMod := modNames[rest]; isMod {
		return Chord{}, fmt.Errorf("chord %q has no base key", s)
	}
	if alias, ok := keyAliases[rest]; ok {
		rest = alias
	}
	c.Key = rest
	return c, nil
}

// String renders the canonical form: modifiers in fixed ctrl/alt/shift/super
// order, lowercase. This must match bubbletea's KeyPressMsg.String(), or a
// configured chord can never match a real press — TestChord_RoundTripsBubbleTea
// pins that against every shipped default.
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
