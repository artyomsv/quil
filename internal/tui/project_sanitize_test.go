package tui

import (
	"strings"
	"testing"
)

// hostileName is a project name a daemon on a machine the user does not control
// could report. It carries an OSC 52 (set the clipboard from the terminal), a
// bare C1 CSI introducer, and a right-to-left override — three separate ways to
// make the rendered row lie about, or act beyond, the text it appears to be.
//
// Written as escapes, not literals: the C1 byte and the bidi override are
// invisible in an editor, so a literal here is a string nobody can review.
const hostileName = "Default\x1b]52;c;cGF5bG9hZA==\x072Jsafe‮gnp.exe"

// assertNeutralised fails when a rendered string still carries anything that
// reaches the terminal as a control rather than as text.
func assertNeutralised(t *testing.T, where, out string) {
	t.Helper()
	for _, bad := range []struct{ name, seq string }{
		{"OSC 52 introducer", "\x1b]52"},
		{"C1 CSI (U+009B)", ""},
		{"BEL string terminator", "\x07"},
		{"RTL override (U+202E)", "‮"},
	} {
		if strings.Contains(out, bad.seq) {
			t.Errorf("%s renders a remote project name still carrying its %s.\n"+
				"A width check is not a sanitiser: lipgloss.Width measures an "+
				"escape sequence as zero cells, so truncation neither counts nor "+
				"cuts it.\ngot: %q", where, bad.name, out)
		}
	}
}

// TestRemoteProjectNameIsNeutralisedOnEveryRenderPath covers the paths that
// render a name a REMOTE daemon chose. The sidebar was written with this in
// mind; three siblings added by the same feature were not, and each is enough
// on its own — the payload only has to reach the terminal once.
//
// Deliberately one test over four paths rather than four separate tests: the
// finding is that a new render path is easy to add and easy to forget, so the
// value is in having one obvious place to extend when the fifth appears.
func TestRemoteProjectNameIsNeutralisedOnEveryRenderPath(t *testing.T) {
	t.Run("sidebar", func(t *testing.T) {
		m := &Model{
			width: 100, height: 30, sidebarOpen: true, sidebarWidth: 22,
			projects: []*ProjectModel{{ID: "proj-1", Name: hostileName, Dest: "gpu01"}},
		}
		assertNeutralised(t, "the sidebar", strings.Join(rowTexts(m.sidebarRows(22)), "\n"))
	})

	t.Run("right-click menu", func(t *testing.T) {
		m := &Model{width: 100, height: 30}
		p := &ProjectModel{ID: "proj-1", Name: hostileName, Dest: "gpu01"}
		m.openProjectCtxMenu(p, 4, 4)
		assertNeutralised(t, "the project context menu", renderCtxMenu(m.ctxMenu))
	})

	t.Run("rename form", func(t *testing.T) {
		m := &Model{width: 100, height: 30, projectFormName: hostileName}
		assertNeutralised(t, "the project form's Name field", m.renderProjectDialog())
	})

	t.Run("palette label", func(t *testing.T) {
		p := NewPaneModel("pane-1", 1024)
		p.Type = "terminal"
		assertNeutralised(t, "the palette pane label", formatPaneNav(0, 0, p, hostileName))
	})
}

// TestRenameKeepsTheRawNameInState: sanitizing must happen at render only. The
// form's value is submitted back to the daemon, so stripping it in state would
// rewrite a name the user never edited — silently, and only for names that
// happen to contain a stripped rune.
func TestRenameKeepsTheRawNameInState(t *testing.T) {
	m := Model{
		width: 100, height: 30,
		projects: []*ProjectModel{{ID: "proj-1", Name: hostileName}},
	}
	renamed, _ := m.beginProjectRename("proj-1")
	got := renamed.(Model).projectFormName

	if got != hostileName {
		t.Fatalf("beginProjectRename stored %q, want the name verbatim %q — "+
			"submitting would rewrite the daemon's stored value",
			got, hostileName)
	}
}

func rowTexts(rows []sidebarRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.text)
	}
	return out
}
