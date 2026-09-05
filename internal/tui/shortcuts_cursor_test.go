package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/artyomsv/quil/internal/config"
)

// shortcutsListLines returns the rendered list rows — everything between the
// title block and the footer — with ANSI stripped, so a test can read the
// cursor marker as text.
func shortcutsListLines(t *testing.T, m Model) []string {
	t.Helper()
	lines := strings.Split(ansi.Strip(m.renderShortcutsDialog()), "\n")
	// Title, blank, rows…, blank, footer.
	if len(lines) < 5 {
		t.Fatalf("Shortcuts dialog rendered %d lines, want at least 5", len(lines))
	}
	return lines[2 : len(lines)-2]
}

// TestShortcutsDialog_MarksTheCursorRow: ↑/↓ moved shortcutsCursor and
// historyWindow kept it on screen, but the renderer never marked the row, so
// the only visible effect of an arrow key was the whole list sliding once the
// cursor hit the window's edge. Every other list in Quil shows where the
// cursor is; this one must too.
func TestShortcutsDialog_MarksTheCursorRow(t *testing.T) {
	m := Model{width: 100, height: 24, dialog: dialogShortcuts, cfg: config.Default()}
	(&m).initKeymap()

	rows := shortcutsListLines(t, m)
	if !strings.HasPrefix(rows[0], "> ") {
		t.Fatalf("row 0 = %q, want the cursor marker on the first row when the dialog opens", rows[0])
	}
	for i, r := range rows[1:] {
		if strings.HasPrefix(r, "> ") {
			t.Errorf("row %d also carries the cursor marker: %q", i+1, r)
		}
	}

	next := m
	for i := 0; i < 3; i++ {
		updated, _ := next.handleShortcutsKey(tea.KeyPressMsg{Code: tea.KeyDown})
		next = updated.(Model)
	}
	rows = shortcutsListLines(t, next)
	if !strings.HasPrefix(rows[3], "> ") {
		t.Fatalf("row 3 = %q, want the cursor marker after three Down presses", rows[3])
	}
	if strings.HasPrefix(rows[0], "> ") {
		t.Fatalf("row 0 still carries the marker after the cursor moved: %q", rows[0])
	}
}

// End scrolls the window to the bottom and the marker has to be on the last
// visible row — the cursor and the window move together.
func TestShortcutsDialog_CursorMarkerFollowsScroll(t *testing.T) {
	m := Model{width: 100, height: 24, dialog: dialogShortcuts, cfg: config.Default()}
	(&m).initKeymap()
	updated, _ := m.handleShortcutsKey(tea.KeyPressMsg{Code: 'G', Text: "G"})
	rows := shortcutsListLines(t, updated.(Model))
	if last := rows[len(rows)-1]; !strings.HasPrefix(last, "> ") {
		t.Fatalf("last visible row = %q after End, want the cursor marker", last)
	}
}

// Enter on "Shortcuts" swapped a 60-column box for a 100-column one and
// returned no ClearScreen, unlike the palette row and the system.shortcuts key
// that open the same dialog. Bubble Tea v2's cell diff then left the About
// box's border standing inside the new box, and dropped the new box's own
// border on those rows, until something else forced a full repaint — a focus
// change, in the report.
//
// Table-driven over every About row whose sub-dialog is drawn at a DIFFERENT
// width from the About box itself. Settings and Plugins are omitted on purpose:
// both render at dialogWidth, so the border lands on the same cells and the
// diff has nothing to leave behind.
func TestAboutMenu_OpeningAWiderDialogClearsTheScreen(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  int
		want dialogScreen
		boxW int
	}{
		{"Shortcuts", 1, dialogShortcuts, shortcutsDialogWidth},
		{"Processes", 3, dialogProcesses, processesDialogWidth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.boxW == dialogWidth {
				t.Fatalf("%s renders at dialogWidth — this case tests nothing", tc.name)
			}
			m := Model{width: 120, height: 40, dialog: dialogAbout, dialogCursor: tc.row, cfg: config.Default()}
			(&m).initKeymap()
			updated, cmd := m.handleAboutKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			if got := updated.(Model).dialog; got != tc.want {
				t.Fatalf("dialog = %v after Enter on %s, want %v", got, tc.name, tc.want)
			}
			if !hasClearScreen(cmd) {
				t.Fatalf("opening %s from the About menu must emit tea.ClearScreen — "+
					"the box goes from %d columns to %d", tc.name, dialogWidth, tc.boxW)
			}
		})
	}
}

// The way back is the same size change in reverse.
func TestShortcutsDialog_EscBackToAboutClearsTheScreen(t *testing.T) {
	m := Model{width: 100, height: 40, dialog: dialogShortcuts, cfg: config.Default()}
	(&m).initKeymap()
	updated, cmd := m.handleShortcutsKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if got := updated.(Model).dialog; got != dialogAbout {
		t.Fatalf("dialog = %v after Esc, want dialogAbout", got)
	}
	if !hasClearScreen(cmd) {
		t.Fatal("leaving Shortcuts for the About menu must emit tea.ClearScreen — the box changes size")
	}
}
