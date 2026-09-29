package tui

import (
	"bytes"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
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
		// Single-byte special keys get ESC + their own byte (#244): Terminal.app
		// with Option-as-Meta sends ESC CR for Option+Enter.
		{"alt+enter → ESC CR", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}, []byte("\x1b\r")},
		{"alt+tab → ESC TAB", tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModAlt}, []byte("\x1b\t")},
		{"alt+backspace → ESC DEL", tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModAlt}, []byte{0x1b, 0x7f}},
		{"ctrl+alt+enter → nil", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt | tea.ModCtrl}, nil},
		// Multi-byte (CSI) keys with Alt fall through, not Meta-encoded.
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

// TestUpdate_AltEnterReachesThePane drives Option+Enter through Update with the
// shipped keymap (#244). The encoding alone is not enough: an action bound to
// alt+enter, or a guard ahead of the default branch, would still swallow it.
func TestUpdate_AltEnterReachesThePane(t *testing.T) {
	t.Parallel()
	m, _ := inputOrderTestModel(t, "p1", true)
	m.cfg = config.Default()
	m.initKeymap()

	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})

	if got := drainQueued(t, m, 1); got != "\x1b\r" {
		t.Errorf("pane received %q, want %q", got, "\x1b\r")
	}
}
