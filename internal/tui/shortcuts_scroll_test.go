package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/keymap"
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
		m := Model{width: 100, height: height, dialog: dialogShortcuts, cfg: config.Default()}
		(&m).initKeymap()
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
		m := Model{width: width, height: 24, dialog: dialogShortcuts, cfg: config.Default()}
		(&m).initKeymap()
		if got := strings.Count(m.renderDialog(), "\n") + 1; got > m.height {
			t.Errorf("at width %d the Shortcuts dialog renders %d lines against a height "+
				"of %d — %d rows wrapped and are drawn past the bottom edge",
				width, got, m.height, got-m.height)
		}
	}
}

// TestShortcutsDialog_LongKeyColumnDoesNotWrap: the KEY half has to be
// truncated too, and lipgloss is why — dialogKeyStyle is Width(dialogKeyColWidth),
// which pads a short value and does nothing whatever to a long one. Only the
// description went through truncateToWidth, so an over-long key column reflowed
// and one entry became three lines, breaking the one-row-one-line arithmetic
// every height calculation in this dialog depends on.
//
// The fixture is a LEGAL chord list reachable by honest typo, not an attack:
// pane.rename already ships two bindings, and a user adding two more of their
// own gets a 49-cell key column against a 22-cell budget. (Escapes are refused
// at keymap.ParseChord — truncateToWidth is ANSI-aware and would carry one
// through at zero measured width, so truncation is the layout fix and the
// parser is the safety fix.)
func TestShortcutsDialog_LongKeyColumnDoesNotWrap(t *testing.T) {
	cfg := config.Default()
	cfg.Keybindings.RenamePane = "alt+f2,alt+shift+r,alt+shift+q,ctrl+alt+shift+f4"
	m := Model{width: 100, height: 40, dialog: dialogShortcuts, cfg: cfg}
	(&m).initKeymap()

	// The fixture must actually overflow the column, or the test passes without
	// exercising anything.
	keys := m.keymap.Display("pane.rename")
	if got := lipgloss.Width(keys); got <= dialogKeyColWidth {
		t.Fatalf("fixture key column is %d cells against a budget of %d — it fits, "+
			"so the test cannot fail; add another binding", got, dialogKeyColWidth)
	}
	// And it must be a legal config: a spec that failed to parse would fall back
	// to the default and never reach the renderer at all.
	for _, c := range m.keyConflicts {
		t.Fatalf("fixture produced a conflict (%s) — the binding never reached the row", c)
	}

	if got := strings.Count(m.renderDialog(), "\n") + 1; got > m.height {
		t.Errorf("a %d-cell key column made the dialog render %d lines against a height of %d — "+
			"%d rows wrapped and are drawn past the bottom edge",
			lipgloss.Width(keys), got, m.height, got-m.height)
	}
}

// TestShortcutsDialog_ScrollReachesTheEnd: the fix is only worth having if the
// rows pushed out of the window are reachable. The project bindings are near
// the end of the list, which is the half a short terminal hides.
func TestShortcutsDialog_ScrollReachesTheEnd(t *testing.T) {
	m := Model{width: 100, height: 24, dialog: dialogShortcuts, cfg: config.Default()}
	(&m).initKeymap()

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

// TestShortcutsList_CoversEveryBoundVisibleAction supersedes
// TestShortcutsList_CoversEveryProjectBinding: hand-maintenance drift (a
// feature bound but never added to the F1 rows, as happened to seven of the
// eight project bindings) is now structurally impossible — the list is
// derived from the same registry the config binds against, not hand-copied
// from it.
func TestShortcutsList_CoversEveryBoundVisibleAction(t *testing.T) {
	m := &Model{cfg: config.Default(), width: 120, height: 40}
	m.initKeymap()
	descs := make(map[string]bool)
	for _, r := range shortcutsList(m) {
		descs[r.desc] = true
	}
	for _, a := range keymap.Actions() {
		if a.Hidden || m.keymap.Display(a.ID) == "" {
			continue // hidden, or deliberately unbound (pane.next/prev)
		}
		if !descs[a.Label] {
			t.Errorf("action %q (%s) is bound but missing from F1", a.ID, a.Label)
		}
	}
}

// TestShortcutsList_OmitsHiddenActions: json.transform has no dispatch site
// (ctrl+j is a leftover M5 config field) — advertising it in F1 would send a
// user pressing a key that does nothing.
func TestShortcutsList_OmitsHiddenActions(t *testing.T) {
	m := &Model{cfg: config.Default(), width: 120, height: 40}
	m.initKeymap()
	for _, r := range shortcutsList(m) {
		if strings.Contains(r.desc, "Transform selection as JSON") {
			t.Error("F1 advertises json.transform, which has no handler")
		}
	}
}

// TestShortcutsList_RendersConflictsFirst: a dropped binding is the one thing
// a user cannot discover any other way, so it has to be the first thing they
// see, not buried under 40-odd rows of bindings that DO work.
func TestShortcutsList_RendersConflictsFirst(t *testing.T) {
	cfg := config.Default()
	cfg.Keybindings.CloseTab = cfg.Keybindings.ClosePane // force a duplicate
	m := &Model{cfg: cfg, width: 120, height: 40}
	m.initKeymap()
	if len(m.keyConflicts) == 0 {
		t.Fatal("test setup produced no conflict")
	}
	rows := shortcutsList(m)
	if len(rows) == 0 || rows[0].key != "!" {
		t.Errorf("first row = %+v, want a conflict warning", rows[0])
	}
}

// TestShortcutsList_KeepsNonActionRows: rows with no registry action behind
// them — handleKey intercepts these outside the two dispatch tiers, or they
// belong to the terminal/editor selection machinery, which the registry does
// not model at all. Each exists in the pre-registry list; dropping one
// silently removes documentation an existing user relies on.
//
// Matched by exact (key, desc) pair rather than a loose substring: a plain
// strings.Contains(joined, "right-click") passes even if the standalone
// "Right-click / Copy selection" row is deleted, because
// pane.quick_actions's own label already contains the substring "right-click"
// ("Pane context menu (also mouse right-click)") — it would not catch the
// regression it is meant to catch.
func TestShortcutsList_KeepsNonActionRows(t *testing.T) {
	m := &Model{cfg: config.Default(), width: 120, height: 40}
	m.initKeymap()
	rows := shortcutsList(m)
	has := func(key, desc string) bool {
		for _, r := range rows {
			if strings.EqualFold(r.key, key) && strings.EqualFold(r.desc, desc) {
				return true
			}
		}
		return false
	}
	for _, want := range []struct{ key, desc string }{
		{"Ctrl+N", "New typed pane"},
		{"Alt+1..9", "Switch to tab N"},
		{"F1", "Help / About"},
		{"Right-click", "Copy selection"},
	} {
		if !has(want.key, want.desc) {
			t.Errorf("F1 lost the %q / %q row", want.key, want.desc)
		}
	}
}
