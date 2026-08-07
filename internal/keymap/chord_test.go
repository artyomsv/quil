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
		{"uppercase folded", "Ctrl+A", "ctrl+a"},
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

func TestParseChord_Rejects(t *testing.T) {
	for _, in := range []string{"", "   ", "ctrl+", "ctrl+alt"} {
		t.Run(in, func(t *testing.T) {
			if _, err := ParseChord(in); err == nil {
				t.Errorf("ParseChord(%q) = nil error, want error", in)
			}
		})
	}
}

func TestChord_RoundTripsBubbleTea(t *testing.T) {
	// Every chord shipped in config.Default().Keybindings. If the canonical
	// modifier order disagrees with bubbletea's rendering, these stop matching
	// real key presses and the affected bindings go silently dead.
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
				t.Errorf("round trip = %q — canonical form disagrees with the shipped default, so this binding would never match", got)
			}
		})
	}
}
