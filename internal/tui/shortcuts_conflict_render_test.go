package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/config"
)

// conflictRow returns the single rendered conflict line, ANSI stripped. It
// asserts there is exactly one: a test that silently picked the first of
// several would stop testing what it names as soon as a second conflict
// appeared in the fixture.
func conflictRow(t *testing.T, m Model) string {
	t.Helper()
	var found []string
	for _, line := range strings.Split(stripANSI(m.renderShortcutsDialog()), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "!") {
			found = append(found, strings.TrimSpace(line))
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one rendered conflict row, got %d:\n%s",
			len(found), strings.Join(found, "\n"))
	}
	return found[0]
}

// TestShortcutsDialog_ConflictRowsRenderWhole is the coverage gap that let two
// defects ship together: one task wrote Conflict.String() and pinned its
// content with TestConflict_StringIsActionable, a different task rendered it,
// and nothing tested the two together. The rows were cut at 43 cells —
// "duplicate binding: \"ctrl+w\" resolves to pan…" — losing the winner, the
// loser and the consequence, which is the whole message.
//
// Asserted on the RENDERED dialog, not on shortcutsList: the list was already
// correct in the shipped build. What was wrong was the width it was drawn at.
func TestShortcutsDialog_ConflictRowsRenderWhole(t *testing.T) {
	tests := []struct {
		name string
		bind func(*config.KeybindingsConfig)
		want []string
		// whole: the entire message must land, ellipsis-free. False for the
		// malformed row alone, whose tail is the parser's complaint — it
		// quotes the offending spec twice and is unbounded in length, which is
		// why Conflict.String puts the fallback ahead of it.
		whole bool
	}{
		{
			// Two actions, one chord, one tier: pane.close has the lower
			// registry Order, so it keeps ctrl+w and tab.close is dead.
			name:  "duplicate",
			bind:  func(kb *config.KeybindingsConfig) { kb.CloseTab = kb.ClosePane },
			want:  []string{"duplicate binding", `"ctrl+w"`, "pane.close", "tab.close", "never fires"},
			whole: true,
		},
		{
			// f8 is checked after BOTH tier switches, so the action wins and
			// the built-in paste alias is what dies.
			name:  "hardcoded, action wins",
			bind:  func(kb *config.KeybindingsConfig) { kb.RestartPane = "f8" },
			want:  []string{"collides with a built-in key", `"f8"`, "pane.restart wins", "paste", "lost"},
			whole: true,
		},
		{
			// isSelectionExtendKey runs between the tiers, so a LATE action
			// bound to a selection chord never fires.
			name:  "hardcoded, built-in wins",
			bind:  func(kb *config.KeybindingsConfig) { kb.ClosePane = "shift+left" },
			want:  []string{"collides with a built-in key", `"shift+left"`, "text selection wins", "pane.close", "never fires"},
			whole: true,
		},
		{
			// A spec that does not parse: what the user needs is the action
			// and the chord it fell back to, and both must survive whatever
			// the parser has to say about the spec.
			name: "malformed",
			bind: func(kb *config.KeybindingsConfig) { kb.Quit = "ctrl+" },
			want: []string{"unreadable binding", "app.quit", "default", `"ctrl+q"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			tt.bind(&cfg.Keybindings)
			// 120 columns is an ordinary terminal, and wider than the box, so
			// what this measures is the dialog's own budget, not the screen's.
			m := Model{width: 120, height: 60, dialog: dialogShortcuts, cfg: cfg}
			(&m).initKeymap()
			if len(m.keyConflicts) == 0 {
				t.Fatal("fixture produced no conflict — the test would pass vacuously")
			}

			row := conflictRow(t, m)
			for _, want := range tt.want {
				if !strings.Contains(row, want) {
					t.Errorf("rendered conflict row = %q\n  missing %q", row, want)
				}
			}
			if tt.whole && strings.Contains(row, "…") {
				t.Errorf("rendered conflict row was truncated: %q", row)
			}
		})
	}
}

// TestShortcutsDialog_ConflictRowSpansTheKeyColumn pins the mechanism rather
// than the outcome: a conflict row must be drawn against the FULL inner width,
// recovering the key column it does not use. The message it uses is chosen to
// straddle the two budgets — longer than a description row gets, shorter than a
// conflict row gets — so the assertion can only pass if the wider budget is the
// one in force.
//
// The previous version asserted shortcutsFullRowWidth() != shortcutsDescWidth()
// + dialogKeyColWidth, which is those two functions' definitions restated: an
// identity above the floor, true whatever renderShortcutsDialog does with them.
// Reverting the render to the narrow budget — the exact bug the name describes —
// left it green. This one renders.
func TestShortcutsDialog_ConflictRowSpansTheKeyColumn(t *testing.T) {
	cfg := config.Default()
	// isSelectionExtendKey runs between the tiers, so a late action bound to a
	// selection chord produces the longest shape the registry can make: kind,
	// chord, the built-in that wins, and the action that dies.
	cfg.Keybindings.ClosePane = "shift+left"
	m := Model{width: 120, height: 60, dialog: dialogShortcuts, cfg: cfg}
	(&m).initKeymap()
	if len(m.keyConflicts) != 1 {
		t.Fatalf("fixture produced %d conflicts, want exactly 1", len(m.keyConflicts))
	}

	// The row's content is `key + " " + desc` — "! " plus the message — measured
	// against the budget before the indent. If it stops straddling, this test
	// stops testing what it names, so that is checked rather than assumed.
	content := lipgloss.Width("! " + m.keyConflicts[0].String())
	desc, full := m.shortcutsDescWidth(), m.shortcutsFullRowWidth()
	if content <= desc {
		t.Fatalf("fixture message is %d cells against a description budget of %d — it fits "+
			"either way, so the test cannot fail; pick a longer conflict", content, desc)
	}
	if content > full {
		t.Fatalf("fixture message is %d cells against a conflict-row budget of %d — it truncates "+
			"either way, so the test cannot pass; widen the box or pick a shorter conflict", content, full)
	}

	if row := conflictRow(t, m); strings.Contains(row, "…") {
		t.Errorf("conflict row was truncated: %q\n  it measures %d cells and the conflict-row "+
			"budget is %d, so it was drawn against the %d-cell description budget instead — "+
			"the key column it does not use has to come back to it", row, content, full, desc)
	}
}

// TestShortcutsDialog_EscapeInABindingNeverReachesTheTerminal is the end-to-end
// half of the parser's base-key check, asserted where the damage would happen.
//
// A chord's key is drawn raw by the key column, and truncateToWidth is
// ANSI-aware — an escape measures zero cells, so it passes every width budget
// untouched and is written straight to the terminal. `quit = "<ESC>]52;c;…"`
// therefore set the system clipboard the moment the user pressed F1.
//
// Asserted on the RAW frame, not the ANSI-stripped one: stripping is what a
// smuggled sequence would hide behind. lipgloss emits only CSI (ESC + "["), so
// an OSC introducer or a C1 byte anywhere in the frame can only have come from
// the config.
func TestShortcutsDialog_EscapeInABindingNeverReachesTheTerminal(t *testing.T) {
	cfg := config.Default()
	cfg.Keybindings.Quit = "\x1b]52;c;aGk=\a" // OSC 52: set the system clipboard
	m := Model{width: 120, height: 60, dialog: dialogShortcuts, cfg: cfg}
	(&m).initKeymap()

	frame := m.renderShortcutsDialog()
	for _, bad := range []struct{ seq, what string }{
		{"\x1b]", "an OSC introducer"},
		{"\x1b_", "an APC introducer"},
		{"\x1bP", "a DCS introducer"},
		{"\x9b", "a C1 CSI byte"},
		{"\x9d", "a C1 OSC byte"},
	} {
		if strings.Contains(frame, bad.seq) {
			t.Errorf("the rendered dialog carries %s from a configured binding — "+
				"the key column draws Chord.Key raw and ANSI-aware truncation preserves it", bad.what)
		}
	}

	// The spec is rejected rather than dropped: app.quit falls back to its
	// shipped default and the dialog says why. Without this, a renderer that
	// simply blanked every key would also pass the assertion above.
	if got := m.keymap.Display("app.quit"); got != "ctrl+q" {
		t.Errorf("app.quit = %q, want the shipped default — a rejected spec must fall back, not unbind", got)
	}
	if row := conflictRow(t, m); !strings.Contains(row, "app.quit") {
		t.Errorf("conflict row = %q, does not name the action whose binding was refused", row)
	}
}

// TestShortcutsDialog_ConflictRowsStillFitTheBox: the rows got their width by
// making the dialog wider, so the guarantee every other row already had —
// one entry is one line — has to survive the change. A wrapped row breaks the
// height arithmetic that counts one line per entry, which is how this dialog
// drew itself past the bottom edge before the window existed.
func TestShortcutsDialog_ConflictRowsStillFitTheBox(t *testing.T) {
	cfg := config.Default()
	cfg.Keybindings.ClosePane = "shift+left" // the longest message shape
	for _, width := range []int{minTermWidth, 60, 80, 100, 120} {
		m := Model{width: width, height: 24, dialog: dialogShortcuts, cfg: cfg}
		(&m).initKeymap()
		if got := strings.Count(m.renderDialog(), "\n") + 1; got > m.height {
			t.Errorf("at width %d the dialog renders %d lines against a height of %d — "+
				"%d rows wrapped and are drawn past the bottom edge",
				width, got, m.height, got-m.height)
		}
	}
}
