package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
)

// TestShortcutsDialog_FitsTheTerminal is the regression for a dialog that drew
// itself past the bottom of the screen.
//
// renderShortcutsDialog wrote every row unconditionally — 60-odd once the
// project bindings were added — and renderDialog's lipgloss.Place does NOT
// clip. On any terminal shorter than the list the footer fell off, and with it
// whichever rows the user opened the dialog to find: entries are appended, so
// the newest shortcut is unreachable in exactly the release that adds it.
func TestShortcutsDialog_FitsTheTerminal(t *testing.T) {
	// 30 rows is an ordinary terminal and comfortably shorter than the list.
	for _, height := range []int{10, 24, 30, 50} {
		m := Model{width: 100, height: height, dialog: dialogShortcuts}
		got := strings.Count(m.renderDialog(), "\n") + 1
		if got > height {
			t.Errorf("at height %d the Shortcuts dialog renders %d lines — "+
				"lipgloss.Place does not clip, so %d of them are drawn past the "+
				"bottom edge, footer first", height, got, got-height)
		}
	}
}

// The same overflow returns on a NARROW terminal unless the description budget
// follows the box the way every other dialog's does.
//
// renderDialog clamps the box to m.width-2, but the description width was a
// constant derived from the PREFERRED 74 — so below that each row was truncated
// to a budget the box no longer had, lipgloss wrapped it onto a second line, and
// the arithmetic that counts one line per entry under-counted exactly as it did
// before the window existed. dialogInnerWidth is the shared helper this must go
// through; a private copy of the clamp is what drifts.
func TestShortcutsDialog_FitsANarrowTerminal(t *testing.T) {
	for _, width := range []int{minTermWidth, 50, 60, 74, 100} {
		m := Model{width: width, height: 24, dialog: dialogShortcuts}
		if got := strings.Count(m.renderDialog(), "\n") + 1; got > m.height {
			t.Errorf("at width %d the Shortcuts dialog renders %d lines against a height "+
				"of %d — %d rows wrapped and are drawn past the bottom edge",
				width, got, m.height, got-m.height)
		}
	}
}

// TestShortcutsDialog_ScrollReachesTheEnd: the fix is only worth having if the
// rows pushed out of the window are reachable. The project bindings are near
// the end of the list, which is the half a short terminal hides.
func TestShortcutsDialog_ScrollReachesTheEnd(t *testing.T) {
	m := Model{width: 100, height: 24, dialog: dialogShortcuts}

	first := m.renderShortcutsDialog()
	if !strings.Contains(first, "Quit") {
		t.Fatal("the unscrolled view does not show the first entry")
	}

	// End jumps to the bottom of the list.
	next, _ := m.handleShortcutsKey(tea.KeyPressMsg{Code: 'G', Text: "G"})
	last := next.(Model).renderShortcutsDialog()
	if last == first {
		t.Fatal("End did not move the window — the list cannot be scrolled and " +
			"every row past the first screen is unreachable")
	}
	if strings.Count(last, "\n")+1 > m.height {
		t.Error("the scrolled-to-end view overflows the terminal")
	}
}

// TestShortcutsList_CoversEveryProjectBinding: the dialog is where a user goes
// to discover a key they do not know. Shipping a feature whose bindings are
// absent from it makes them undiscoverable in the product — the sidebar toggle
// was listed, the other seven were not.
func TestShortcutsList_CoversEveryProjectBinding(t *testing.T) {
	m := Model{width: 100, height: 200}
	m.cfg = config.Default()
	m.initKeymap() // shortcutsList reads m.keymap now; NewModel builds it

	var keys []string
	for _, s := range shortcutsList(&m) {
		keys = append(keys, s.key)
	}
	joined := strings.Join(keys, "\n")

	kb := m.cfg.Keybindings
	for _, want := range []struct{ binding, what string }{
		{kb.SidebarToggle, "toggle the project sidebar"},
		{kb.NewProject, "create a project"},
		{kb.DestroyProject, "remove a project"},
		{kb.ProjectPicker, "open the project picker"},
		{kb.ProjectToggle, "bounce to the previous project"},
		{kb.ProjectNext, "go to the next project"},
		{kb.ProjectPrev, "go to the previous project"},
		{kb.AttentionQueue, "jump to the agent blocked longest"},
	} {
		if want.binding == "" {
			t.Errorf("no default binding to %s — the config default is empty", want.what)
			continue
		}
		if !strings.Contains(joined, kbDisplay(want.binding)) {
			t.Errorf("the Shortcuts dialog does not list %q (%s), so there is no "+
				"in-product way to discover it", want.binding, want.what)
		}
	}
}
