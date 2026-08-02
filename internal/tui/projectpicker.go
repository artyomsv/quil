package tui

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// projectPickWidth is the fuzzy project picker's dialog width — narrower
// than gitRepoPickWidth (repo paths run long) but wide enough for a
// displayName() with a @dest suffix.
const projectPickWidth = 60

// projectPickState holds the fuzzy project picker's query buffer, result
// list, and cursor. Same shape as paletteState: no `open` field, m.dialog ==
// dialogProjectPick is the sole open/closed authority. Zero value = empty.
type projectPickState struct {
	query    string
	cursor   int
	filtered []*ProjectModel
}

// filterProjects ranks projects with fuzzyScore — the same matcher the command
// palette uses (palette.go:109), so there is one ranking behaviour rather than
// two that drift apart. Matches against displayName() (Name, or Name@Dest for
// a remote project) so a query narrows on either half.
func (m *Model) filterProjects(query string) []*ProjectModel {
	if query == "" {
		return m.projects
	}
	type scored struct {
		p     *ProjectModel
		score int
	}
	var hits []scored
	for _, p := range m.projects {
		if score, ok := fuzzyScore(query, p.displayName()); ok {
			hits = append(hits, scored{p, score})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].score > hits[b].score })

	out := make([]*ProjectModel, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.p)
	}
	return out
}

// toggleLastProject bounces between the two most recent projects, the way
// `sesh pop` does for tmux sessions. switchProject records prevProject on
// every successful switch, so repeated toggles bounce back and forth
// indefinitely. A stale prevProject (out of range, or naming the project
// already active) resolves through switchProject's own bounds/no-op guard
// rather than a second check here.
func (m *Model) toggleLastProject() tea.Cmd {
	if m.prevProject < 0 || m.prevProject >= len(m.projects) {
		return nil
	}
	return m.switchProject(m.prevProject)
}

// openProjectPicker opens the fuzzy project picker (Alt+P). Unlike the
// command palette, there is no notes-mode guard here: switchProject (reached
// on Enter, see handleProjectPickKey) already flushes notes via
// exitNotesModeInPlace before it moves activeProject, so the picker itself
// needs no extra teardown — kb.ProjectPicker is listed in notesKeyExempt so
// the keypress even reaches this function while notes mode is open.
func (m Model) openProjectPicker() (tea.Model, tea.Cmd) {
	m.projectPick = projectPickState{filtered: m.filterProjects("")}
	m.dialog = dialogProjectPick
	return m, tea.ClearScreen
}

// closeProjectPicker closes the picker and clears its state. m.dialog is the
// open/closed authority.
func (m *Model) closeProjectPicker() {
	m.dialog = dialogNone
	m.projectPick = projectPickState{}
}

// handleProjectPickKey routes keys while the picker is open. Value receiver,
// like the sibling handleXKey dialog handlers (handleCommandPaletteKey,
// handleGitRepoPickKey).
func (m Model) handleProjectPickKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch {
	case key == "esc":
		m.closeProjectPicker()
		return m, tea.ClearScreen
	case key == "enter":
		c := m.projectPick.cursor
		if c < 0 || c >= len(m.projectPick.filtered) {
			return m, nil
		}
		// Resolve by ID rather than closing over the filtered pointer: the
		// index switchProject wants is into m.projects, not m.projectPick.filtered,
		// and the two lists diverge as soon as a query is typed.
		idx := indexOfProject(m.projects, m.projectPick.filtered[c].ID)
		// Sequenced, not `return m, tea.Batch(tea.ClearScreen, m.switchProject(idx))`:
		// switchProject mutates m through a pointer receiver, and Go does not
		// order a plain operand against a call in the same return statement
		// (see activateSidebarRow's identical note in project.go).
		cmd := m.switchProject(idx)
		m.closeProjectPicker()
		return m, tea.Batch(tea.ClearScreen, cmd)
	case key == "up" || key == "ctrl+p":
		if m.projectPick.cursor > 0 {
			m.projectPick.cursor--
		}
		return m, nil
	case key == "down" || key == "ctrl+n":
		if m.projectPick.cursor < len(m.projectPick.filtered)-1 {
			m.projectPick.cursor++
		}
		return m, nil
	case key == "backspace":
		if q := []rune(m.projectPick.query); len(q) > 0 {
			m.projectPick.query = string(q[:len(q)-1])
			return m.afterProjectPickQueryChange()
		}
		return m, nil
	case key == "space":
		m.projectPick.query += " "
		return m.afterProjectPickQueryChange()
	case msg.Text != "" && isPrintableText(msg.Text):
		// Only printable text extends the query — mirrors
		// handleCommandPaletteKey's guard against control chars riding in
		// msg.Text for an unhandled key (e.g. tab).
		m.projectPick.query += msg.Text
		return m.afterProjectPickQueryChange()
	}
	return m, nil
}

// afterProjectPickQueryChange refilters the project list and clamps the
// cursor back onto it. Single choke point for every path that mutates
// m.projectPick.query (typed text, backspace, space) — mirrors
// afterPaletteQueryChange.
func (m Model) afterProjectPickQueryChange() (tea.Model, tea.Cmd) {
	m.projectPick.filtered = m.filterProjects(m.projectPick.query)
	if n := len(m.projectPick.filtered); m.projectPick.cursor >= n {
		m.projectPick.cursor = n - 1
	}
	if m.projectPick.cursor < 0 {
		m.projectPick.cursor = 0
	}
	return m, nil
}

// renderProjectPickDialog returns the picker box CONTENT (renderDialog wraps
// it in dialogBorder and centers it, at the default dialogWidth).
func (m Model) renderProjectPickDialog() string {
	inner := dialogInnerWidth(m.width, projectPickWidth)
	var b strings.Builder

	// Query row: "> " (2 cells) + query + caret (1 cell), tail-truncated like
	// the command palette's so the caret stays visible on a narrow terminal.
	qAvail := inner - 3
	if qAvail < 1 {
		qAvail = 1
	}
	b.WriteString(dialogTitle.Render("> "))
	b.WriteString(dialogEditStyle.Render(lastCellsToWidth(m.projectPick.query, qAvail) + "│"))
	b.WriteByte('\n')
	b.WriteByte('\n')

	if len(m.projectPick.filtered) == 0 {
		b.WriteString(dialogSubtle.Render(truncateToWidth("No matching projects", inner)))
		b.WriteByte('\n')
	}
	for i, p := range m.projectPick.filtered {
		// sanitizeRemoteText: a project's Name (and thus displayName) can
		// come from a remote daemon's own project list, not just this
		// process's local input — same trust boundary as every other
		// daemon-sourced string drawn in a dialog row.
		name := truncateToWidth(sanitizeRemoteText(p.displayName()), inner-2)
		if i == m.projectPick.cursor {
			b.WriteString(dialogSelected.Render("> " + name))
		} else {
			b.WriteString(dialogNormal.Render("  " + name))
		}
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render(truncateToWidth("↑↓ nav · Enter switch · Esc close", inner)))
	return b.String()
}
