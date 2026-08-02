package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestProjectPickerFiltersFuzzily(t *testing.T) {
	m := Model{projects: []*ProjectModel{
		{ID: "proj-a", Name: "quil"},
		{ID: "proj-b", Name: "quil-docs"},
		{ID: "proj-c", Name: "unrelated"},
	}}
	got := m.filterProjects("qd")
	if len(got) != 1 || got[0].Name != "quil-docs" {
		t.Fatalf("filterProjects(qd) = %v, want [quil-docs]", got)
	}
}

func TestLastProjectToggleReturnsAndBounces(t *testing.T) {
	m := Model{
		client:        newFakeConn(),
		projects:      []*ProjectModel{{ID: "proj-a"}, {ID: "proj-b"}, {ID: "proj-c"}},
		activeProject: 0,
	}
	m.switchProject(2)
	m.toggleLastProject()
	if m.activeProject != 0 {
		t.Fatalf("toggle should return to 0, got %d", m.activeProject)
	}
	m.toggleLastProject()
	if m.activeProject != 2 {
		t.Fatalf("toggle should bounce back to 2, got %d", m.activeProject)
	}
}

// TestFilterProjectsMatchesNothing pins the "no hits" edge case: the score
// loop appends nothing, and the pre-sized `make` still returns a real empty
// slice rather than nil (callers range over it either way, but a nil
// vs. empty distinction here would be an easy accidental regression).
func TestFilterProjectsMatchesNothing(t *testing.T) {
	m := Model{projects: []*ProjectModel{
		{ID: "proj-a", Name: "quil"},
		{ID: "proj-b", Name: "quil-docs"},
	}}
	got := m.filterProjects("zzz-does-not-exist")
	if len(got) != 0 {
		t.Fatalf("filterProjects(no match) = %v, want empty", got)
	}
}

// TestFilterProjectsSingleProject pins the picker's smallest legal case: one
// project, matched or not, never panics or misbehaves on a slice of length 1.
func TestFilterProjectsSingleProject(t *testing.T) {
	m := Model{projects: []*ProjectModel{{ID: "proj-a", Name: "quil"}}}

	if got := m.filterProjects(""); len(got) != 1 || got[0].ID != "proj-a" {
		t.Fatalf("filterProjects(\"\") = %v, want [proj-a]", got)
	}
	if got := m.filterProjects("quil"); len(got) != 1 || got[0].ID != "proj-a" {
		t.Fatalf("filterProjects(quil) = %v, want [proj-a]", got)
	}
	if got := m.filterProjects("nope"); len(got) != 0 {
		t.Fatalf("filterProjects(nope) = %v, want empty", got)
	}
}

// TestToggleLastProjectOutOfRangeIsNoop covers a prevProject left dangling by
// a workspace reconciliation that removed projects out from under it —
// negative and too-large are both "not a real index" and must not touch
// activeProject or return a live command.
func TestToggleLastProjectOutOfRangeIsNoop(t *testing.T) {
	cases := []struct {
		name        string
		prevProject int
	}{
		{"negative", -1},
		{"equal to length", 3},
		{"past length", 99},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{
				client:        newFakeConn(),
				projects:      []*ProjectModel{{ID: "proj-a"}, {ID: "proj-b"}, {ID: "proj-c"}},
				activeProject: 1,
				prevProject:   tc.prevProject,
			}
			if cmd := m.toggleLastProject(); cmd != nil {
				t.Fatalf("toggleLastProject() with prevProject=%d returned a live command, want nil", tc.prevProject)
			}
			if m.activeProject != 1 {
				t.Fatalf("activeProject = %d, want unchanged 1", m.activeProject)
			}
		})
	}
}

// TestToggleLastProjectSameAsCurrentIsNoop covers a Model built with
// prevProject already equal to activeProject (the zero-value pairing, or a
// state a caller assembled directly rather than reaching via switchProject).
// switchProject's own i==activeProject guard makes this a no-op — there is
// no separate check in toggleLastProject, so this test pins that the shared
// guard is enough on its own.
func TestToggleLastProjectSameAsCurrentIsNoop(t *testing.T) {
	m := Model{
		client:        newFakeConn(),
		projects:      []*ProjectModel{{ID: "proj-a"}, {ID: "proj-b"}},
		activeProject: 0,
		prevProject:   0,
	}
	if cmd := m.toggleLastProject(); cmd != nil {
		t.Fatal("toggleLastProject() bouncing to the already-active project returned a live command, want nil")
	}
	if m.activeProject != 0 {
		t.Fatalf("activeProject = %d, want unchanged 0", m.activeProject)
	}
}

func TestOpenProjectPickerPopulatesFilteredList(t *testing.T) {
	m := Model{projects: []*ProjectModel{{ID: "proj-a", Name: "alpha"}, {ID: "proj-b", Name: "beta"}}}
	updated, cmd := m.openProjectPicker()
	got := updated.(Model)
	if got.dialog != dialogProjectPick {
		t.Fatalf("dialog = %v, want dialogProjectPick", got.dialog)
	}
	if len(got.projectPick.filtered) != 2 {
		t.Fatalf("filtered = %d projects, want 2 (empty query browses all)", len(got.projectPick.filtered))
	}
	if cmd == nil {
		t.Fatal("openProjectPicker() returned a nil cmd, want tea.ClearScreen")
	}
}

func TestHandleProjectPickKey_EscCloses(t *testing.T) {
	m := Model{projects: []*ProjectModel{{ID: "proj-a"}}}
	m, _ = mustOpenProjectPicker(t, m)
	m.projectPick.query = "something"

	updated, _ := m.handleProjectPickKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	got := updated.(Model)
	if got.dialog != dialogNone {
		t.Fatalf("dialog = %v, want dialogNone", got.dialog)
	}
	if got.projectPick.query != "" || got.projectPick.filtered != nil {
		t.Fatalf("projectPick = %+v, want zero value after Esc", got.projectPick)
	}
}

// TestHandleProjectPickKey_EnterSwitchesProject drives the picker end to end:
// type a query that narrows to one project, Enter, and the active project
// moves to the ONE THAT WAS FILTERED — not whatever index the cursor happens
// to hold in the unfiltered list, which is the bug this test would catch if
// handleProjectPickKey resolved by cursor position into m.projects directly.
func TestHandleProjectPickKey_EnterSwitchesProject(t *testing.T) {
	fake := newFakeConn()
	m := Model{
		client: fake,
		projects: []*ProjectModel{
			{ID: "proj-a", Name: "alpha"},
			{ID: "proj-b", Name: "beta"},
			{ID: "proj-c", Name: "gamma"},
		},
		activeProject: 0,
	}
	m, _ = mustOpenProjectPicker(t, m)

	updated, _ := m.handleProjectPickKey(tea.KeyPressMsg{Text: "g"})
	m = updated.(Model)
	if len(m.projectPick.filtered) != 1 || m.projectPick.filtered[0].ID != "proj-c" {
		t.Fatalf("filtered = %v, want [proj-c]", m.projectPick.filtered)
	}

	updated, cmd := m.handleProjectPickKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(Model)
	if got.activeProject != 2 {
		t.Fatalf("activeProject = %d, want 2 (proj-c)", got.activeProject)
	}
	if got.dialog != dialogNone {
		t.Fatalf("dialog = %v, want dialogNone after Enter", got.dialog)
	}
	if cmd == nil {
		t.Fatal("Enter returned a nil cmd, want tea.ClearScreen + resizeAllPanes batch")
	}
}

// TestHandleProjectPickKey_EnterOnEmptyFilteredIsNoop: a query matching
// nothing must not panic or switch to some fallback project on Enter.
func TestHandleProjectPickKey_EnterOnEmptyFilteredIsNoop(t *testing.T) {
	m := Model{
		client:        newFakeConn(),
		projects:      []*ProjectModel{{ID: "proj-a"}},
		activeProject: 0,
	}
	m, _ = mustOpenProjectPicker(t, m)
	m.projectPick.query = "zzz"
	m.projectPick.filtered = nil

	updated, _ := m.handleProjectPickKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(Model)
	if got.activeProject != 0 {
		t.Fatalf("activeProject = %d, want unchanged 0", got.activeProject)
	}
	if got.dialog != dialogProjectPick {
		t.Fatalf("dialog = %v, want dialogProjectPick (Enter on no matches must not close)", got.dialog)
	}
}

// TestProjectPickerSingleProjectNavDoesNotPanic covers the picker's smallest
// legal case end to end: opening it, moving the cursor past both ends, and
// pressing Enter must all be safe no-ops on a one-project workspace.
func TestProjectPickerSingleProjectNavDoesNotPanic(t *testing.T) {
	m := Model{client: newFakeConn(), projects: []*ProjectModel{{ID: "proj-a", Name: "solo"}}}
	m, _ = mustOpenProjectPicker(t, m)

	m, _ = keyModel(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.projectPick.cursor != 0 {
		t.Fatalf("cursor = %d, want clamped at 0 with one row", m.projectPick.cursor)
	}
	m, _ = keyModel(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.projectPick.cursor != 0 {
		t.Fatalf("cursor = %d, want clamped at 0", m.projectPick.cursor)
	}
	updated, _ := m.handleProjectPickKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(Model)
	if got.activeProject != 0 {
		t.Fatalf("activeProject = %d, want unchanged 0 (switching to the already-active project is a no-op)", got.activeProject)
	}
}

// TestRenderProjectPickDialog_HostileCandidate_NoRawESCByte mirrors
// TestRenderGitRepoPick_HostileCandidate_NoRawESCByte: a project name is
// daemon-sourced (Task 14 brief: "Every remote-sourced string rendered must
// pass through sanitizeRemoteText") and must not be able to inject a raw
// ESC byte into the rendered dialog.
func TestRenderProjectPickDialog_HostileCandidate_NoRawESCByte(t *testing.T) {
	hostileName := "alpha\x1b[31m;rm -rf\x1b[0m"
	clean := Model{projectPick: projectPickState{filtered: []*ProjectModel{{ID: "proj-a", Name: "alpha"}}}}
	hostile := Model{projectPick: projectPickState{filtered: []*ProjectModel{{ID: "proj-a", Name: hostileName}}}}

	cleanOut := clean.renderProjectPickDialog()
	hostileOut := hostile.renderProjectPickDialog()

	wantESC := strings.Count(cleanOut, "\x1b")
	if got := strings.Count(hostileOut, "\x1b"); got != wantESC {
		t.Errorf("hostile project name changed the raw ESC byte count: got %d, want %d (dialog-chrome baseline)\n%s", got, wantESC, hostileOut)
	}

	wantContent := sanitizeRemoteText(hostileName)
	if !strings.Contains(stripANSI(hostileOut), wantContent) {
		t.Errorf("sanitized project name %q missing from render — row may have been dropped rather than cleaned\n%s", wantContent, stripANSI(hostileOut))
	}
}

// mustOpenProjectPicker runs openProjectPicker and unwraps the tea.Model
// result — every test below drives the picker from its real opened state
// rather than hand-assembling projectPick, so a future change to what
// opening it initializes is caught here too.
func mustOpenProjectPicker(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.openProjectPicker()
	got, ok := updated.(Model)
	if !ok {
		t.Fatalf("openProjectPicker() returned %T, want Model", updated)
	}
	return got, cmd
}

// keyModel runs handleProjectPickKey and unwraps the tea.Model result.
func keyModel(t *testing.T, m Model, msg tea.KeyPressMsg) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.handleProjectPickKey(msg)
	got, ok := updated.(Model)
	if !ok {
		t.Fatalf("handleProjectPickKey() returned %T, want Model", updated)
	}
	return got, cmd
}
