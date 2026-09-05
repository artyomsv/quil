package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// dragSlot answers, for a reorder drag, which slot the dragged item belongs in
// now that the pointer is at pos over the item at target, whose extent begins
// at start and is size cells long (columns in the tab bar, rows in the
// sidebar). It returns from when nothing should move.
//
// The rule is the midpoint: the dragged item moves past a neighbour only once
// the pointer has crossed the neighbour's middle. Hovering the near half of a
// neighbour further away slots the item just BEFORE that neighbour rather than
// on top of it, so a fast pointer still lands where it points.
//
// The midpoint is what makes a drag stable. Moving on first contact — what the
// tab bar did before — swaps the dragged item with a neighbour of a DIFFERENT
// size, which puts that neighbour under the pointer; the next motion event
// sees a different item there and swaps them straight back. A narrow tab
// dragged over a wide one flip-flopped on every event, and a wide tab dragged
// over narrow ones flew across several slots per cell of pointer travel. Once
// the move waits for the middle, the displaced neighbour lands on the far
// side of the pointer, so the pointer has to travel the neighbour's half-width
// back before anything moves again.
func dragSlot(from, target, pos, start, size int) int {
	if target == from {
		return from
	}
	if target > from {
		// Far half begins at the middle cell (integer division), which for a
		// one-cell item is the item itself.
		if pos >= start+size/2 {
			return target
		}
		return target - 1
	}
	// Moving to a lower index: the mirror image. The middle cell of an odd
	// extent counts as "past" here too, so (size+1)/2 rather than size/2.
	if pos < start+(size+1)/2 {
		return target
	}
	return target + 1
}

// tabSpan is one visible tab's extent in the tab bar, in BAR-LOCAL columns
// (screen column minus projectSidebarWidth()), plus the styled text painted
// there.
type tabSpan struct {
	index int // into curTabs()
	start int
	width int
	text  string
}

// tabSpans lays the visible tabs out: same labels, same styles, same overflow
// rule around the active tab, same one-column separators. It is the SINGLE
// geometry the painter (renderTabBar), the click (hitTestTab) and the reorder
// drag all read, so none of the three can disagree about which tab occupies
// which column.
//
// Carrying the rendered text costs nothing: the width has to come from
// lipgloss.Width(style.Render(label)) either way, so the string already exists
// and was previously thrown away. Keeping it is what let renderTabBar stop
// owning a second copy of this arithmetic — the copy that made
// TestTabSpansAgreeWithHitTestTab necessary in the first place.
func (m Model) tabSpans() []tabSpan {
	barW := m.paneAreaWidth()
	tabs := m.curTabs()
	if len(tabs) == 0 {
		return nil
	}

	texts := make([]string, len(tabs))
	widths := make([]int, len(tabs))
	totalW := 0
	for i := range tabs {
		texts[i] = m.tabStyle(i).Render(m.tabLabel(i))
		widths[i] = lipgloss.Width(texts[i])
		totalW += widths[i]
		if i > 0 {
			totalW++
		}
	}

	included := make([]bool, len(tabs))
	if totalW <= barW {
		for i := range included {
			included[i] = true
		}
	} else {
		activeIdx := m.activeTabIdx()
		included[activeIdx] = true
		usedW := widths[activeIdx]
		indicatorReserve := 12

		left := activeIdx - 1
		right := activeIdx + 1
		for left >= 0 || right < len(tabs) {
			if left >= 0 {
				need := widths[left] + 1
				if usedW+need+indicatorReserve <= barW {
					included[left] = true
					usedW += need
					left--
				} else {
					left = -1
				}
			}
			if right < len(tabs) {
				need := widths[right] + 1
				if usedW+need+indicatorReserve <= barW {
					included[right] = true
					usedW += need
					right++
				} else {
					right = len(tabs)
				}
			}
		}
	}

	spans := make([]tabSpan, 0, len(tabs))
	cursor := 0
	for i := range tabs {
		if !included[i] {
			continue
		}
		if cursor > 0 {
			cursor++ // space separator
		}
		spans = append(spans, tabSpan{index: i, start: cursor, width: widths[i], text: texts[i]})
		cursor += widths[i]
	}
	return spans
}

// tabSpanAt returns the visible tab under bar-local column x, if any.
func (m Model) tabSpanAt(x int) (tabSpan, bool) {
	for _, s := range m.tabSpans() {
		if x >= s.start && x < s.start+s.width {
			return s, true
		}
	}
	return tabSpan{}, false
}

// moveActiveTab slides the active tab by delta slots (negative = left) and
// tells the daemon, exactly as a drag move does. At either end of the strip
// it does nothing and sends nothing — a no-op reorder would still cost a
// broadcast on every client's must-deliver queue.
func (m *Model) moveActiveTab(delta int) tea.Cmd {
	tabs := m.curTabs()
	from := m.activeTabIdx()
	to := from + delta
	if to < 0 || to >= len(tabs) || from < 0 || from >= len(tabs) {
		return nil
	}
	tabID := tabs[from].ID
	if !m.moveTab(from, to) {
		return nil
	}
	return m.sendReorderTab(tabID, to)
}

// moveProject slides the project at from to ordinal to in the sidebar, the
// projects between them shifting by one — the same slide moveTab does, for the
// same reason (a swap teleports the displaced row). m.activeProject is an INDEX,
// so it is re-resolved by ID afterwards: the active project follows itself
// whether it was the one dragged or one the drag displaced.
//
// Returns true when the order actually changed.
func (m *Model) moveProject(from, to int) bool {
	n := len(m.projects)
	if from == to || from < 0 || to < 0 || from >= n || to >= n {
		return false
	}
	// The active project is re-found by POINTER, not by ID. A project ID is
	// "proj-" plus the first 8 hex digits of a UUID (daemon/project.go),
	// minted independently by every daemon — so two daemons in one sidebar can
	// hand out the same one, and indexOfProject returns the FIRST match. That
	// would move focus, and every action that follows it, to the other
	// daemon's project. This slide only permutes the existing slice, so the
	// pointer is guaranteed to still be in it.
	active := m.cur()
	p := m.projects[from]
	if from < to {
		copy(m.projects[from:to], m.projects[from+1:to+1])
	} else {
		copy(m.projects[to+1:from+1], m.projects[to:from])
	}
	m.projects[to] = p
	if active != nil {
		for i, q := range m.projects {
			if q == active {
				m.activeProject = i
				break
			}
		}
	}
	return true
}

// sendReorderProject tells the daemon that owns p where p now sits — among
// THAT daemon's projects, not in the sidebar. The sidebar interleaves every
// connected daemon's projects (mergeProjects), and a daemon's projectOrder
// holds only its own, so the sidebar ordinal is meaningless to it; what it
// can store is p's rank among its siblings. The cross-daemon interleaving is
// client-side state and is not persisted.
//
// An offline or synthetic project is reordered locally and NOT reported:
// Router.Send would drop the message and log it as delivered (projectActionable).
func (m Model) sendReorderProject(p *ProjectModel) tea.Cmd {
	if !m.projectActionable(p) {
		return nil
	}
	idx, found := 0, false
	for _, q := range m.projects {
		if q == p {
			found = true
			break
		}
		if q.Dest == p.Dest {
			idx++
		}
	}
	// A miss is refused rather than sent. Falling off the end leaves idx at
	// "one past that daemon's last project" — a perfectly legal ordinal the
	// daemon would clamp and act on, so a lookup failure would silently move a
	// project to the end instead of doing nothing.
	//
	// Pointer identity rather than ID, for the reason moveProject documents:
	// project IDs are only 8 hex digits and are minted per daemon, so an ID
	// compare could match a DIFFERENT daemon's project and break the count
	// early — the same class of bug, one level down.
	if !found {
		return nil
	}
	id, dest := p.ID, p.Dest
	return func() tea.Msg {
		msg, _ := ipc.NewMessage(ipc.MsgReorderProject, ipc.ReorderProjectPayload{
			ProjectID: id,
			NewIndex:  idx,
		})
		if m.client != nil {
			_ = m.sendForDest(dest, msg)
		}
		return nil
	}
}

// moveActiveProject slides the active project by delta slots (negative = up)
// and reports the move. A no-op at either end, with no traffic.
func (m *Model) moveActiveProject(delta int) tea.Cmd {
	from := m.activeProject
	to := from + delta
	if from < 0 || from >= len(m.projects) || to < 0 || to >= len(m.projects) {
		return nil
	}
	p := m.projects[from]
	if !m.moveProject(from, to) {
		return nil
	}
	return m.sendReorderProject(p)
}

// sidebarDragRows resolves the pointer to a sidebar row AND hands back the row
// slice it came from, so one motion event builds the sidebar exactly ONCE.
//
// The build is not cheap: it renders every project row, tab heading, pane row
// and git row through lipgloss. A real workspace reaches ~70 of them. The first
// version called sidebarRowAt (which builds) and then a span helper (which
// built again), so every motion event paid for two full builds on the Update
// goroutine that also forwards keystrokes — for as long as the button is held.
//
// The guards mirror sidebarRowAt's exactly, so the drag and a click resolve the
// same coordinate to the same row.
func (m *Model) sidebarDragRows(x, y int) ([]sidebarRow, sidebarRow, bool) {
	w := m.projectSidebarWidth()
	if w <= 0 || x < 0 || x >= w || y < 0 || y >= m.height-1 {
		return nil, sidebarRow{}, false
	}
	rows := m.sidebarVisibleRows(w, m.sidebarContentHeight())
	if y >= len(rows) {
		return nil, sidebarRow{}, false
	}
	return rows, rows[y], true
}

// projectRowSpanIn is the screen-row extent of project idx's rows: one row for
// a local project, two for a remote one (name + host). Pure over an
// already-built slice — see sidebarDragRows for why that matters.
func projectRowSpanIn(rows []sidebarRow, idx int) (start, size int) {
	start, end := -1, -1
	for y, row := range rows {
		if row.kind != sidebarRowProject || row.index != idx {
			continue
		}
		if start < 0 {
			start = y
		}
		end = y
	}
	if start < 0 {
		return 0, 0
	}
	return start, end - start + 1
}

// trackProjectDrag advances an armed project drag to the pointer at (x, y).
// Rows that are not a project row — the heading, the PANES section, a column
// outside the strip — leave the order alone, so the drag survives a pointer
// that wanders and resumes when it comes back. Returns the IPC cmd for a move,
// or nil when nothing moved.
func (m *Model) trackProjectDrag(x, y int) tea.Cmd {
	rows, row, ok := m.sidebarDragRows(x, y)
	if !ok || row.kind != sidebarRowProject {
		return nil
	}
	from := m.projectDragIdx
	if from < 0 || from >= len(m.projects) {
		return nil
	}
	start, size := projectRowSpanIn(rows, row.index)
	if size == 0 {
		return nil
	}
	to := dragSlot(from, row.index, y, start, size)
	if to == from {
		return nil
	}
	p := m.projects[from]
	if !m.moveProject(from, to) {
		return nil
	}
	m.projectDragIdx = to
	return m.sendReorderProject(p)
}

// tabGroupSpanIn is the screen-row extent of tab idx's group — heading, pane
// rows and git rows (every row marked inTab). Measured on the VISIBLE rows, so
// a group partly scrolled off screen is as tall as the part the user can see,
// which is the part the pointer can be over. Pure over an already-built slice,
// like projectRowSpanIn.
func tabGroupSpanIn(rows []sidebarRow, idx int) (start, size int) {
	start, end := -1, -1
	for y, row := range rows {
		if !row.inTab || row.tabIdx != idx {
			continue
		}
		if start < 0 {
			start = y
		}
		end = y
	}
	if start < 0 {
		return 0, 0
	}
	return start, end - start + 1
}

// trackSidebarTabDrag advances an armed tab drag in the sidebar to the pointer
// at (x, y). Any row of a tab's group counts as hovering that tab; the blank
// separators, the headings and columns outside the strip leave the order
// alone. The move itself is moveTab plus the same reorder_tab the tab bar
// sends, so the daemon sees one kind of reorder however it was made.
func (m *Model) trackSidebarTabDrag(x, y int) tea.Cmd {
	rows, row, ok := m.sidebarDragRows(x, y)
	if !ok || !row.inTab {
		return nil
	}
	from := m.sidebarTabDragIdx
	tabs := m.curTabs()
	if from < 0 || from >= len(tabs) {
		return nil
	}
	start, size := tabGroupSpanIn(rows, row.tabIdx)
	if size == 0 {
		return nil
	}
	to := dragSlot(from, row.tabIdx, y, start, size)
	if to == from {
		return nil
	}
	tabID := tabs[from].ID
	if !m.moveTab(from, to) {
		return nil
	}
	m.sidebarTabDragIdx = to
	return m.sendReorderTab(tabID, to)
}
