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
	// moveTabID != "" puts the picker in MOVE mode: the list is scoped to
	// moveTabCandidates(moveTabID) (via projectPickBase) and Enter sends
	// MsgMoveTab instead of switching project — see handleProjectPickKey.
	// openProjectPicker's fresh zero value leaves it off.
	moveTabID string
}

// projectPickBase returns the picker's base list BEFORE fuzzy filtering:
// every project in switch mode, or the move-mode scope in move mode. This is
// where the move-mode scope has to live, rather than only at open time —
// model.go's broadcast refresh calls filterProjects again on every
// workspace_state while the picker is open, and a scope applied only at open
// would let the first broadcast widen the list back to every project.
func (m *Model) projectPickBase() []*ProjectModel {
	if m.projectPick.moveTabID != "" {
		return m.moveTabCandidates(m.projectPick.moveTabID)
	}
	return m.projects
}

// filterProjects ranks projects with fuzzyScore — the same matcher the command
// palette uses (palette.go:109), so there is one ranking behaviour rather than
// two that drift apart. Matches against displayName() (Name, or Name@Dest for
// a remote project) so a query narrows on either half.
func (m *Model) filterProjects(query string) []*ProjectModel {
	base := m.projectPickBase()
	if query == "" {
		// A COPY, not the base slice itself. The picker holds this slice
		// across Update calls while a workspace broadcast can rebuild
		// m.projects underneath it; aliasing makes "what is on screen" and
		// "what exists" the same variable in one case and different in every
		// other, which is the harder bug to reason about of the two.
		return append([]*ProjectModel(nil), base...)
	}
	type scored struct {
		p     *ProjectModel
		score int
	}
	var hits []scored
	for _, p := range base {
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
// indefinitely.
//
// The ID is resolved through projectByID, NOT indexOfProject: the latter
// answers 0 for an unknown ID (its always-somewhere-valid contract, right for
// an active-project pointer), and here that would silently bounce to the FIRST
// project whenever the remembered one is gone. A project that no longer exists
// must be a no-op. Naming the already-active project resolves through
// switchProject's own no-op guard rather than a second check here.
// cycleProject moves delta places through the project list, wrapping at both
// ends. Distinct from toggleLastProject, which bounces between the two most
// recent: cycling is how you reach a project you have never visited, bouncing
// is how you return to the one you just left.
//
// It walks m.projects in list order rather than by ID, which is safe because
// applyWorkspaceState merges each destination's projects in place — a
// background daemon broadcasting no longer reshuffles the slice under the
// user's fingers.
func (m *Model) cycleProject(delta int) tea.Cmd {
	n := len(m.projects)
	if n < 2 {
		return nil
	}
	return m.switchProject(((m.activeProject+delta)%n + n) % n)
}

func (m *Model) toggleLastProject() tea.Cmd {
	if m.prevProject == "" || m.projectByID(m.prevProject) == nil {
		return nil
	}
	return m.switchProject(indexOfProject(m.projects, m.prevProject))
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

// openMoveTabPicker opens the SAME picker in MOVE mode for tabID: the list is
// scoped to moveTabCandidates(tabID) via projectPickBase, and Enter sends
// MsgMoveTab instead of switching project (handleProjectPickKey). A sibling
// dialog was considered and rejected — see the plan's "Alternatives
// considered" note — because the query editing, fuzzy ranking, sanitized
// rendering and broadcast refresh are all identical to the switch picker.
//
// The tab context menu's own refusal (proj == m.cur()) already ran before
// this is reached, and buildTabCtxMenuItems already hid the menu row that
// leads here when moveTabCandidates is empty — but this function does not
// re-check either, matching openProjectPicker's own lack of preconditions.
func (m Model) openMoveTabPicker(tabID string) (tea.Model, tea.Cmd) {
	m.projectPick = projectPickState{moveTabID: tabID}
	m.projectPick.filtered = m.filterProjects("")
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
		//
		// projectByID, NOT indexOfProject, for the reason toggleLastProject
		// gives: indexOfProject answers 0 for an unknown id, so a row whose
		// project was destroyed or disconnected while the picker was open would
		// switch to the FIRST project — a jump the user did not ask for and
		// cannot undo. Gone means no-op.
		target := m.projectByID(m.projectPick.filtered[c].ID)
		if target == nil {
			m.closeProjectPicker()
			return m, tea.ClearScreen
		}
		// Move mode: never switchProject — the user stays in the project they
		// were in. Re-check the target is still in scope rather than trusting
		// the snapshot filterProjects took when the picker opened (or last
		// refreshed): the target may have gone offline or been destroyed
		// between then and this Enter.
		if tabID := m.projectPick.moveTabID; tabID != "" {
			inScope := false
			for _, cand := range m.moveTabCandidates(tabID) {
				if cand.ID == target.ID {
					inScope = true
					break
				}
			}
			if !inScope {
				m.closeProjectPicker()
				return m, tea.ClearScreen
			}
			cmd := m.sendMoveTab(tabID, target.ID)
			m.closeProjectPicker()
			return m, tea.Batch(tea.ClearScreen, cmd)
		}
		idx := indexOfProject(m.projects, target.ID)
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
	m.clampProjectPickCursor()
	return m, nil
}

// clampProjectPickCursor keeps the cursor inside the filtered list. Split out
// of afterProjectPickQueryChange because the list can also shrink without the
// query changing — a workspace broadcast rebuilding m.projects while the picker
// is open.
func (m *Model) clampProjectPickCursor() {
	if n := len(m.projectPick.filtered); m.projectPick.cursor >= n {
		m.projectPick.cursor = n - 1
	}
	if m.projectPick.cursor < 0 {
		m.projectPick.cursor = 0
	}
}

// renderProjectPickDialog returns the picker box CONTENT (renderDialog wraps
// it in dialogBorder and centers it, at the default dialogWidth). Move mode
// adds a title row naming the tab being moved and swaps the empty-list
// message and the hint row for their move-mode wording.
func (m Model) renderProjectPickDialog() string {
	inner := dialogInnerWidth(m.width, projectPickWidth)
	var b strings.Builder

	moveMode := m.projectPick.moveTabID != ""
	if moveMode {
		// sanitizeRemoteText: the tab's Name can come from a remote daemon,
		// same trust boundary as every project name drawn below.
		name := sanitizeRemoteText(tabNameFor(m.tabByID(m.projectPick.moveTabID)))
		b.WriteString(dialogTitle.Render(truncateToWidth(`Move "`+name+`" to:`, inner)))
		b.WriteByte('\n')
	}

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
		empty := "No matching projects"
		if moveMode && m.projectPick.query == "" {
			empty = "No other project on this host"
		}
		b.WriteString(dialogSubtle.Render(truncateToWidth(empty, inner)))
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

	hint := "↑↓ nav · Enter switch · Esc close"
	if moveMode {
		hint = "↑↓ nav · Enter move · Esc cancel"
	}
	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render(truncateToWidth(hint, inner)))
	return b.String()
}

// tabNameFor returns tab.Name, or "" for a nil tab — the tab named by
// moveTabID can vanish (destroyed elsewhere) between open and render; the
// broadcast-refresh close in model.go handles the common case, but render
// must not assume it already ran.
func tabNameFor(tab *TabModel) string {
	if tab == nil {
		return ""
	}
	return tab.Name
}
