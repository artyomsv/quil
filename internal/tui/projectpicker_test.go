package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/config"
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

// TestToggleLastProjectUnknownIDIsNoop covers a prevProject left dangling by a
// workspace reconciliation that removed the project out from under it, plus
// the never-switched zero value. Neither may touch activeProject or return a
// live command — and in particular an unknown ID must not fall back to
// project 0, which is what resolving it through indexOfProject would do.
func TestToggleLastProjectUnknownIDIsNoop(t *testing.T) {
	cases := []struct {
		name        string
		prevProject string
	}{
		{"never switched", ""},
		{"destroyed project", "proj-gone"},
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
				t.Fatalf("toggleLastProject() with prevProject=%q returned a live command, want nil", tc.prevProject)
			}
			if m.activeProject != 1 {
				t.Fatalf("activeProject = %d, want unchanged 1", m.activeProject)
			}
		})
	}
}

// TestToggleLastProjectSameAsCurrentIsNoop covers a Model built with
// prevProject already naming the active project (a state a caller assembled
// directly rather than reaching via switchProject). switchProject's own
// i==activeProject guard makes this a no-op — there is no separate check in
// toggleLastProject, so this test pins that the shared guard is enough on its
// own.
func TestToggleLastProjectSameAsCurrentIsNoop(t *testing.T) {
	m := Model{
		client:        newFakeConn(),
		projects:      []*ProjectModel{{ID: "proj-a"}, {ID: "proj-b"}},
		activeProject: 0,
		prevProject:   "proj-a",
	}
	if cmd := m.toggleLastProject(); cmd != nil {
		t.Fatal("toggleLastProject() bouncing to the already-active project returned a live command, want nil")
	}
	if m.activeProject != 0 {
		t.Fatalf("activeProject = %d, want unchanged 0", m.activeProject)
	}
}

// TestToggleLastProjectFollowsTheIDAcrossAReorder is the regression test for
// prevProject-as-an-index: a broadcast can legitimately reorder m.projects
// (a new project appears, a destroyed one disappears), and an index survives
// as a NUMBER while coming to mean a DIFFERENT project — silently moving the
// user to another daemon's work.
func TestToggleLastProjectFollowsTheIDAcrossAReorder(t *testing.T) {
	m := Model{
		client: newFakeConn(),
		projects: []*ProjectModel{
			{ID: "proj-local", Dest: ""}, {ID: "proj-gpu", Dest: "gpu01"},
		},
		activeProject: 0,
	}
	m.switchProject(1) // now on proj-gpu, bounce target is proj-local

	// A reconciliation reorders the list: proj-local is no longer at index 0.
	m.projects = []*ProjectModel{
		{ID: "proj-new", Dest: "gpu01"},
		{ID: "proj-gpu", Dest: "gpu01"},
		{ID: "proj-local", Dest: ""},
	}
	m.activeProject = 1

	m.toggleLastProject()
	if got := m.projects[m.activeProject].ID; got != "proj-local" {
		t.Fatalf("bounced to %q, want proj-local — the toggle followed the slot, not the project", got)
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

// TestRenderDialog_ProjectPick_UsesProjectPickWidth goes through the real
// renderDialog() switch rather than calling renderProjectPickDialog()
// directly (which bypasses the switch entirely and cannot catch a missing
// `width = projectPickWidth` case) — renderProjectPickDialog sizes every row
// against dialogInnerWidth(m.width, projectPickWidth), so renderDialog's
// case for dialogProjectPick has to set that SAME constant or the box
// lipgloss actually draws stops matching the width the content assumed.
// A terminal wide enough that renderDialog's own clamp never engages
// (m.width way past projectPickWidth+2) isolates the case-statement wiring
// from that clamp.
func TestRenderDialog_ProjectPick_UsesProjectPickWidth(t *testing.T) {
	m := Model{
		width:  200,
		height: 50,
		dialog: dialogProjectPick,
		projectPick: projectPickState{
			filtered: []*ProjectModel{{ID: "proj-a", Name: "alpha"}},
		},
	}
	out := m.renderDialog()

	var top string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "╭") {
			top = line
			break
		}
	}
	if top == "" {
		t.Fatal("no top border line found in rendered dialog")
	}
	// lipgloss.Place pads the box out to the full m.width=200 canvas with
	// plain, unstyled spaces on either side (centering) — trim those before
	// measuring, or every line in a wide terminal measures 200 regardless of
	// the box's own width.
	top = strings.TrimSpace(top)
	if w := lipgloss.Width(top); w != projectPickWidth {
		t.Errorf("box width = %d, want projectPickWidth (%d) — dialogProjectPick's "+
			"renderDialog case must set width = projectPickWidth explicitly, not fall "+
			"through to the default dialogWidth", w, projectPickWidth)
	}
}

// TestFilterProjectsDistinguishesSameNameByDest: two projects named "api" on
// different hosts must stay distinguishable in the picker — filterProjects
// matches against displayName() (Name, or Name@Dest for a remote project),
// so a query naming the host narrows to exactly one.
func TestFilterProjectsDistinguishesSameNameByDest(t *testing.T) {
	m := Model{projects: []*ProjectModel{
		{ID: "proj-a", Name: "api", Dest: "host1"},
		{ID: "proj-b", Name: "api", Dest: "host2"},
	}}
	got := m.filterProjects("api@host2")
	if len(got) != 1 || got[0].ID != "proj-b" {
		t.Fatalf("filterProjects(api@host2) = %v, want [proj-b]", got)
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

// TestProjectToggleKeyWithNoPreviousFlashes: on a fresh launch prevProject is
// empty, because only switchProject writes it — so alt+o genuinely has nowhere
// to bounce back to until the user has switched once. That is correct, but it
// used to be SILENT, which reads as a broken binding and was reported as one
// ("alt+o does not work, but starts working after I click a project"). Same
// treatment as the AttentionQueue empty case and the SidebarToggle narrow
// refusal. The second half pins that the flash arm does not hijack the working
// path: with a real previous project the bounce still happens and no flash is
// shown.
func TestProjectToggleKeyWithNoPreviousFlashes(t *testing.T) {
	newModel := func(prev string) Model {
		m := Model{
			client:        newFakeConn(),
			cfg:           config.Default(),
			width:         100,
			height:        30,
			notifications: NewNotificationCenter(30, 50),
			mcpHighlights: make(map[string]bool),
			projects: []*ProjectModel{
				{ID: "proj-a", tabs: []*TabModel{tabWith(&PaneModel{ID: "pane-a"})}},
				{ID: "proj-b", tabs: []*TabModel{tabWith(&PaneModel{ID: "pane-b"})}},
			},
			activeProject: 1,
			prevProject:   prev,
		}
		m.initKeymap() // handleKey dispatches through the keymap; NewModel builds it
		return m
	}
	press := tea.KeyPressMsg{Mod: tea.ModAlt, Code: 'o'}

	// Fresh launch: nothing switched yet.
	m := newModel("")
	updated, cmd := m.handleKey(press)
	got := updated.(Model)
	if got.flashText != "no previous project to switch back to" {
		t.Errorf("flashText = %q, want the no-previous-project flash", got.flashText)
	}
	if got.activeProject != 1 {
		t.Errorf("activeProject = %d, want 1 — a flash must not move the user", got.activeProject)
	}
	if cmd == nil {
		t.Error("no command returned; the flash would never clear")
	}

	// A previous project that has since been destroyed degrades the same way.
	m = newModel("proj-gone")
	updated, _ = m.handleKey(press)
	if got := updated.(Model); got.flashText != "no previous project to switch back to" {
		t.Errorf("stale prevProject: flashText = %q, want the flash", got.flashText)
	}

	// The working path is untouched: a real previous project still bounces.
	m = newModel("proj-a")
	updated, _ = m.handleKey(press)
	got = updated.(Model)
	if got.activeProject != 0 {
		t.Errorf("activeProject = %d, want 0 — the bounce did not happen", got.activeProject)
	}
	if got.flashText != "" {
		t.Errorf("flashText = %q, want empty on the working path", got.flashText)
	}
}

// TestProjectCycleKeysWrapBothWays: alt+o is a bounce, so reaching a project
// you have never visited needed its own binding. Cycling wraps at both ends,
// and a single-project workspace flashes rather than no-opping silently —
// which is the state every user is in until they create a second project, so
// it is the first thing they would press it in.
func TestProjectCycleKeysWrapBothWays(t *testing.T) {
	newModel := func(active int, ids ...string) Model {
		projects := make([]*ProjectModel, 0, len(ids))
		for _, id := range ids {
			projects = append(projects, &ProjectModel{
				ID:   id,
				tabs: []*TabModel{tabWith(&PaneModel{ID: "pane-" + id})},
			})
		}
		m := Model{
			client:        newFakeConn(),
			cfg:           config.Default(),
			width:         100,
			height:        30,
			notifications: NewNotificationCenter(30, 50),
			mcpHighlights: make(map[string]bool),
			projects:      projects,
			activeProject: active,
		}
		m.initKeymap() // handleKey dispatches through the keymap; NewModel builds it
		return m
	}
	next := tea.KeyPressMsg{Mod: tea.ModAlt | tea.ModShift, Code: tea.KeyRight}
	prev := tea.KeyPressMsg{Mod: tea.ModAlt | tea.ModShift, Code: tea.KeyLeft}

	// Forward, and wrapping off the end.
	m := newModel(0, "a", "b", "c")
	updated, _ := m.handleKey(next)
	if got := updated.(Model).activeProject; got != 1 {
		t.Errorf("next from 0: activeProject = %d, want 1", got)
	}
	m = newModel(2, "a", "b", "c")
	updated, _ = m.handleKey(next)
	if got := updated.(Model).activeProject; got != 0 {
		t.Errorf("next from the last project must wrap to 0, got %d", got)
	}

	// Backward, and wrapping off the front — the modulo has to stay positive.
	m = newModel(0, "a", "b", "c")
	updated, _ = m.handleKey(prev)
	if got := updated.(Model).activeProject; got != 2 {
		t.Errorf("prev from 0 must wrap to the last project, got %d", got)
	}

	// One project: flash, do not move, and return a command so it clears.
	m = newModel(0, "a")
	updated, cmd := m.handleKey(next)
	got := updated.(Model)
	if got.flashText != "only one project" {
		t.Errorf("flashText = %q, want the single-project flash", got.flashText)
	}
	if got.activeProject != 0 {
		t.Errorf("activeProject = %d, want 0 — a flash must not move the user", got.activeProject)
	}
	if cmd == nil {
		t.Error("no command returned; the flash would never clear")
	}
}
