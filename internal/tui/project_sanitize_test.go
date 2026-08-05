package tui

import (
	"errors"
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

	// The fifth path this test was written to expect: the form's message line
	// names the project already on a host, and a remote daemon chose that name.
	t.Run("form message line", func(t *testing.T) {
		m := &Model{
			width: 100, height: 30, dialog: dialogProjectNew,
			client:          NewRouter(map[string]Client{"": newFakeConn(), "gpu01": newFakeConn()}),
			projects:        []*ProjectModel{{ID: "proj-1", Name: hostileName, Dest: "gpu01"}},
			projectFormDest: "gpu01",
		}
		// Drive the real refusal rather than assigning the field, so the test
		// covers the path a user reaches instead of one the test invents.
		m.submitNewProject("infra", "/srv/infra")
		if m.projectFormErr == "" {
			t.Fatal("the create was not refused, so the message under test never rendered")
		}
		assertNeutralised(t, "the project form's message line", m.renderProjectDialog())
	})
}

// The render site sanitises too, and this is what pins it.
//
// Every writer of the message line sanitises today, so a test driving a real
// refusal passes whether or not the render site does — the redundancy is
// invisible to it. This assigns the field DIRECTLY, which is precisely the
// ninth set site somebody adds later without the call: the render is the one
// place guaranteed to run for every message, so it is the one that must not
// trust its input.
func TestProjectFormMessage_RenderSanitisesUnconditionally(t *testing.T) {
	m := &Model{width: 100, height: 30, dialog: dialogProjectNew}
	m.projectFormErr = hostileName // a set site that forgot

	assertNeutralised(t, "the project form's message line", m.renderProjectDialog())
}

// A remote daemon can choose a project name of any length, and sanitising does
// not bound one — a megabyte of ordinary printable text survives it intact.
// The message line is the one value-bearing row in the dialog with no
// truncation of its own, and lipgloss WRAPS at the box width, so an unbounded
// name becomes thousands of rendered lines in every frame until the client is
// unusable.
func TestProjectFormMessage_BoundsARemoteName(t *testing.T) {
	m := &Model{
		width: 100, height: 30, dialog: dialogProjectNew,
		client: NewRouter(map[string]Client{"": newFakeConn(), "gpu01": newFakeConn()}),
		projects: []*ProjectModel{
			{ID: "proj-1", Name: strings.Repeat("A", 100_000), Dest: "gpu01"},
		},
		projectFormDest: "gpu01",
	}

	m.submitNewProject("infra", "/srv/infra")

	if m.projectFormErr == "" {
		t.Fatal("the create was not refused, so nothing was rendered")
	}
	if n := len(m.projectFormErr); n > 200 {
		t.Errorf("the message is %d bytes — the remote name was interpolated "+
			"unbounded, and lipgloss will wrap it across the whole frame", n)
	}
	if lines := strings.Count(m.renderProjectDialog(), "\n"); lines > 100 {
		t.Errorf("the dialog rendered %d lines; a remote project name should not "+
			"be able to grow the frame", lines)
	}
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

// The other bound on the same line: ssh's own words.
//
// The transport caps its stderr at 2000 bytes, which is small enough not to be
// an attack and far too large for this row — at the dialog's width it wraps to
// some forty lines and pushes the box past the terminal. Asserted because the
// sibling bound on the project name has a test and this one did not, so
// dropping either truncateToWidth wrapper would have failed nothing.
func TestProjectFormMessage_BoundsARemoteDiagnostic(t *testing.T) {
	m := Model{
		width: 100, height: 30, dialog: dialogProjectNew,
		client:             NewRouter(map[string]Client{"": newFakeConn()}),
		projectFormDialing: "gpu01",
		attached:           map[string]bool{},
	}

	next, _ := m.Update(destDialedMsg{
		dest: "gpu01",
		err:  errors.New("ssh: " + strings.Repeat("banner ", 400)),
	})
	got := next.(Model)

	if got.projectFormErr == "" {
		t.Fatal("the failure was not reported, so nothing was bounded")
	}
	if n := len(got.projectFormErr); n > 300 {
		t.Errorf("the message is %d bytes — ssh's stderr reaches this row whole "+
			"and wraps the dialog past the bottom of the terminal", n)
	}
	if lines := strings.Count(got.renderProjectDialog(), "\n"); lines > 100 {
		t.Errorf("the dialog rendered %d lines for one connection failure", lines)
	}
}
