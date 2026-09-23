package tui

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// tabPickWidth is the "Move to tab…" picker's dialog width — same footprint
// as the project picker (projectPickWidth), which this is modelled on.
const tabPickWidth = 60

// tabPickState holds the "Move to tab…" picker's query buffer, result list,
// and cursor. m.dialog == dialogTabPick is the sole open/closed authority,
// the same contract projectPickState uses — no `open` field, and
// openMovePanePicker's fresh zero value is what "closed" looks like.
//
// A sibling of projectPickState rather than a move MODE of it (as tab-to-
// project reuses the project picker): the row type, the scope (tabs across
// projects rather than projects), and the refresh rule (close when the PANE
// leaves its tab, not when the TAB it names vanishes) all differ, while
// everything actually shareable between the two — fuzzyScore, the dialog
// styles, dialogInnerWidth, sanitizeRemoteText, truncateToWidth/
// lastCellsToWidth — is already a free function neither struct owns.
// Generalising the project picker itself was considered and rejected: its
// state, filter, Enter path and existing move-mode tests are all typed on
// *ProjectModel, and a third row kind would need an interface-typed list and
// a rewrite of the just-merged move-mode coverage for no shared benefit.
type tabPickState struct {
	paneID   string // the pane being moved
	srcTabID string // its tab at OPEN time; the picker closes if that changes
	query    string
	cursor   int
	filtered []tabPickRow
}

// tabPickRow carries IDs, never pointers: a workspace broadcast rebuilds tabs
// under an open picker, and a *TabModel captured before that rebuild can
// point at a tree nothing walks any more. label is RAW ("project / tab") and
// is sanitized only at render — the same render-only rule every other
// daemon-sourced string in this package follows.
type tabPickRow struct {
	tabID string
	label string
}

// filterTabPick ranks movePaneCandidates(paneID) with fuzzyScore against each
// row's "project / tab" label — the same matcher filterProjects uses. An
// empty query returns candidate order; otherwise hits are stable-sorted by
// score, high to low.
//
// The scope lives HERE, not just at open time: model.go's broadcast refresh
// recomputes this on every workspace_state while the picker is open, so a
// scope captured only once at open would be widened back to every reachable
// tab by the very next broadcast (the #229 lesson the project picker's move
// mode already learned).
func (m *Model) filterTabPick(query string) []tabPickRow {
	base := m.movePaneCandidates(m.tabPick.paneID)
	rows := make([]tabPickRow, 0, len(base))
	for _, tab := range base {
		label := tab.Name
		if proj := m.projectOf(tab.ID); proj != nil {
			label = proj.Name + " / " + tab.Name
		}
		rows = append(rows, tabPickRow{tabID: tab.ID, label: label})
	}
	if query == "" {
		return rows
	}
	type scored struct {
		row   tabPickRow
		score int
	}
	var hits []scored
	for _, r := range rows {
		if score, ok := fuzzyScore(query, r.label); ok {
			hits = append(hits, scored{r, score})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].score > hits[b].score })
	out := make([]tabPickRow, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.row)
	}
	return out
}

// openMovePanePicker opens the tab picker for paneID, reached from the pane
// context menu's "Move to tab…" row. No-op in notes mode, matching every
// other context-menu-launched picker/dialog in this package — the editor is
// bound to a specific pane and tab, and this dialog restructures both.
//
// Resolves the pane's owning tab via findPaneAndTab rather than trusting a
// caller-supplied one: the menu's own uniform refusal has already run by the
// time this is reached from executeCtxMenuItem, but a miss here (the pane
// vanished in the interim) is still a plain no-op rather than a panic.
func (m Model) openMovePanePicker(paneID string) (tea.Model, tea.Cmd) {
	if m.notesMode {
		return m, nil
	}
	pane, proj, tabIdx := m.findPaneAndTab(paneID)
	if pane == nil || proj == nil || tabIdx < 0 || tabIdx >= len(proj.tabs) {
		return m, nil
	}
	m.tabPick = tabPickState{paneID: paneID, srcTabID: proj.tabs[tabIdx].ID}
	m.tabPick.filtered = m.filterTabPick("")
	m.dialog = dialogTabPick
	return m, tea.ClearScreen
}

// closeTabPicker closes the picker and clears its state. m.dialog is the
// open/closed authority, so nothing else needs to check tabPick directly.
func (m *Model) closeTabPicker() {
	m.dialog = dialogNone
	m.tabPick = tabPickState{}
}

// tabPickSourceIntact reports whether the pane being moved still exists AND
// is still in the tab it was in when the picker opened. Backs both the
// broadcast refresh's vanish-close and the Enter re-check — a pane moved or
// destroyed by another client while this picker sat open must not be acted
// on here.
func (m *Model) tabPickSourceIntact() bool {
	pane, proj, tabIdx := m.findPaneAndTab(m.tabPick.paneID)
	if pane == nil || proj == nil || tabIdx < 0 || tabIdx >= len(proj.tabs) {
		return false
	}
	return proj.tabs[tabIdx].ID == m.tabPick.srcTabID
}

// clampTabPickCursor keeps the cursor inside the filtered list. Split out of
// the query-change path because the list can also shrink with the query
// unchanged — a workspace broadcast narrowing the candidate set while the
// picker is open.
func (m *Model) clampTabPickCursor() {
	if n := len(m.tabPick.filtered); m.tabPick.cursor >= n {
		m.tabPick.cursor = n - 1
	}
	if m.tabPick.cursor < 0 {
		m.tabPick.cursor = 0
	}
}

// afterTabPickQueryChange refilters the tab list and clamps the cursor back
// onto it. Single choke point for every path that mutates m.tabPick.query
// (typed text, backspace, space) — mirrors afterProjectPickQueryChange.
func (m Model) afterTabPickQueryChange() (tea.Model, tea.Cmd) {
	m.tabPick.filtered = m.filterTabPick(m.tabPick.query)
	m.clampTabPickCursor()
	return m, nil
}

// handleTabPickKey routes keys while the picker is open — the same shape as
// handleProjectPickKey (value receiver, like every sibling handleXKey dialog
// handler).
func (m Model) handleTabPickKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch {
	case key == "esc":
		m.closeTabPicker()
		return m, tea.ClearScreen
	case key == "enter":
		c := m.tabPick.cursor
		if c < 0 || c >= len(m.tabPick.filtered) {
			return m, nil
		}
		// Capture both ids before any mutation. Re-check BOTH halves of what
		// this dialog promises, since either can have changed while it sat
		// open: the pane may have moved or been destroyed elsewhere
		// (tabPickSourceIntact), and the chosen target may have gone
		// ineligible — another client started a worktree create there, or it
		// went offline (re-derive movePaneCandidates rather than trust the
		// filtered snapshot).
		paneID := m.tabPick.paneID
		tabID := m.tabPick.filtered[c].tabID
		if !m.tabPickSourceIntact() {
			m.closeTabPicker()
			return m, tea.ClearScreen
		}
		inScope := false
		for _, cand := range m.movePaneCandidates(paneID) {
			if cand.ID == tabID {
				inScope = true
				break
			}
		}
		if !inScope {
			m.closeTabPicker()
			return m, tea.ClearScreen
		}
		// Never switch tabs or projects: the user stays where they are. No
		// local tree mutation — the daemon's broadcast is what places the
		// pane (see internal/tui's placeArrivingPane / tui-rendering.md's
		// "Panes moved between tabs").
		cmd := m.sendMovePane(paneID, tabID)
		m.closeTabPicker()
		return m, tea.Batch(tea.ClearScreen, cmd)
	case key == "up" || key == "ctrl+p":
		if m.tabPick.cursor > 0 {
			m.tabPick.cursor--
		}
		return m, nil
	case key == "down" || key == "ctrl+n":
		if m.tabPick.cursor < len(m.tabPick.filtered)-1 {
			m.tabPick.cursor++
		}
		return m, nil
	case key == "backspace":
		if q := []rune(m.tabPick.query); len(q) > 0 {
			m.tabPick.query = string(q[:len(q)-1])
			return m.afterTabPickQueryChange()
		}
		return m, nil
	case key == "space":
		m.tabPick.query += " "
		return m.afterTabPickQueryChange()
	case msg.Text != "" && isPrintableText(msg.Text):
		// Only printable text extends the query — mirrors
		// handleProjectPickKey's guard against control chars riding in
		// msg.Text for an unhandled key.
		m.tabPick.query += msg.Text
		return m.afterTabPickQueryChange()
	}
	return m, nil
}

// tabPickPaneTitle resolves the display name for the picker's title row. The
// pane can vanish between open and render — the broadcast refresh's vanish-
// close handles the common case, but render must not assume it already ran,
// matching tabNameFor's reasoning in projectpicker.go.
func tabPickPaneTitle(pane *PaneModel) string {
	if pane == nil {
		return ""
	}
	return paneDisplayName(pane)
}

// renderTabPickDialog returns the picker box CONTENT (renderDialog wraps it
// in dialogBorder and centers it, at tabPickWidth). Modelled directly on
// renderProjectPickDialog's move-mode rendering.
func (m Model) renderTabPickDialog() string {
	inner := dialogInnerWidth(m.width, tabPickWidth)
	var b strings.Builder

	pane, _, _ := m.findPaneAndTab(m.tabPick.paneID)
	// sanitizeRemoteText: the pane's display name can be a daemon-sourced CWD
	// or a user-set name synced from a remote host — same trust boundary as
	// every other daemon-sourced string drawn in a dialog row.
	title := `Move "` + sanitizeRemoteText(tabPickPaneTitle(pane)) + `" to:`
	b.WriteString(dialogTitle.Render(truncateToWidth(title, inner)))
	b.WriteByte('\n')

	// Query row: "> " (2 cells) + query + caret (1 cell), tail-truncated like
	// the project picker's so the caret stays visible on a narrow terminal.
	qAvail := inner - 3
	if qAvail < 1 {
		qAvail = 1
	}
	b.WriteString(dialogTitle.Render("> "))
	b.WriteString(dialogEditStyle.Render(lastCellsToWidth(m.tabPick.query, qAvail) + "│"))
	b.WriteByte('\n')
	b.WriteByte('\n')

	if len(m.tabPick.filtered) == 0 {
		empty := "No matching tabs"
		if m.tabPick.query == "" {
			empty = "No other tab on this host"
		}
		b.WriteString(dialogSubtle.Render(truncateToWidth(empty, inner)))
		b.WriteByte('\n')
	}
	for i, row := range m.tabPick.filtered {
		name := truncateToWidth(sanitizeRemoteText(row.label), inner-2)
		if i == m.tabPick.cursor {
			b.WriteString(dialogSelected.Render("> " + name))
		} else {
			b.WriteString(dialogNormal.Render("  " + name))
		}
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render(truncateToWidth("↑↓ nav · Enter move · Esc cancel", inner)))
	return b.String()
}
