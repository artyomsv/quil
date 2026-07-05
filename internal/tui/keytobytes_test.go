package tui

import (
	"bytes"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestKeyToBytes_AltMeta covers the Alt+<printable> → ESC+<char> (Meta) branch
// that makes macOS Terminal.app Option-as-Meta word-jump (Option+b/f) reach the
// pane, without swallowing Ctrl+Alt combos or special keys.
func TestKeyToBytes_AltMeta(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.KeyPressMsg
		want []byte
	}{
		{"alt+b → ESC b", tea.KeyPressMsg{Code: 'b', Mod: tea.ModAlt}, []byte{0x1b, 'b'}},
		{"alt+f → ESC f", tea.KeyPressMsg{Code: 'f', Mod: tea.ModAlt}, []byte{0x1b, 'f'}},
		{"alt+. → ESC .", tea.KeyPressMsg{Code: '.', Mod: tea.ModAlt}, []byte{0x1b, '.'}},
		// Shift casing: Text carries the shifted glyph when present.
		{"alt+shift+b (Text B) → ESC B", tea.KeyPressMsg{Code: 'b', Mod: tea.ModAlt | tea.ModShift, Text: "B"}, []byte{0x1b, 'B'}},
		// Ctrl+Alt must NOT hit the Meta branch (explicit ctrl+alt+* cases own it).
		{"ctrl+alt+b → nil", tea.KeyPressMsg{Code: 'b', Mod: tea.ModAlt | tea.ModCtrl}, nil},
		// Special (non-printable) keys with Alt fall through, not Meta-encoded.
		{"alt+up → nil", tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt}, nil},
		// The explicit named ctrl+alt+left case still wins over the Meta branch.
		{"ctrl+alt+left → 3x word jump", tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl | tea.ModAlt}, []byte("\x1b[1;5D\x1b[1;5D\x1b[1;5D")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := keyToBytes(tc.msg)
			if !bytes.Equal(got, tc.want) {
				t.Errorf("keyToBytes(%q) = %q, want %q", tc.msg.String(), got, tc.want)
			}
		})
	}
}
