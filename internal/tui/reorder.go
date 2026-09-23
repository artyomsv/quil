package tui

import (
	"fmt"

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

// tabBarWidths renders every tab label once and returns its styled text and
// width, the bar's own budget, and both overflow markers' WORST-CASE widths
// (N = n-1, the most tabs either marker can ever report). tabBarLayout and
// scrollTabBar both need these numbers before they can decide anything else,
// and computing them once here is what keeps there being only one place that
// renders a tab label for layout purposes.
func (m Model) tabBarWidths() (texts []string, widths []int, barW, leftMarkerW, rightMarkerW int) {
	tabs := m.curTabs()
	barW = m.paneAreaWidth()
	n := len(tabs)
	if n == 0 {
		return nil, nil, barW, 0, 0
	}
	texts = make([]string, n)
	widths = make([]int, n)
	for i := range tabs {
		texts[i] = m.tabStyle(i).Render(m.tabLabel(i))
		widths[i] = lipgloss.Width(texts[i])
	}
	leftMarkerW = lipgloss.Width(fmt.Sprintf("«%d ", n-1))
	rightMarkerW = lipgloss.Width(fmt.Sprintf(" %d»", n-1))
	return texts, widths, barW, leftMarkerW, rightMarkerW
}

// maxFirstIndex is the smallest first-visible-tab index f such that tabs
// f..n-1 — with their one-column separators, plus the left marker's width
// once f > 0 — fit in barW. Scrolling any further right than this hides more
// tabs on the left while showing an already-complete tail, so both manual
// mode's clamp and scrollTabBar's own re-clamp read it from here: one
// implementation, not two.
//
// Computed as a suffix-sum table scanned forward for the first fit, rather
// than an incremental break from the top: the left marker's width is added
// only once f reaches 0, so the total is not strictly monotonic across that
// one step and a break-on-first-failure loop can stop one index early.
func maxFirstIndex(widths []int, barW, leftMarkerW int) int {
	n := len(widths)
	if n == 0 {
		return 0
	}
	suffix := make([]int, n)
	suffix[n-1] = widths[n-1]
	for f := n - 2; f >= 0; f-- {
		suffix[f] = suffix[f+1] + widths[f] + 1
	}
	for f := 0; f < n; f++ {
		total := suffix[f]
		if f > 0 {
			total += leftMarkerW
		}
		if total <= barW {
			return f
		}
	}
	return n - 1
}

// tabBarManualMode reports whether the tab bar is currently showing a
// user-set scroll window rather than auto-centering: the anchor is non-empty
// AND names the CURRENT active tab. tabBarLayout, scrollTabBar and the
// tab-bar click handler (model.go) all read this same check, so "a tab
// switch returns the bar to auto mode" is one comparison rather than three
// copies of it that could drift.
func (m Model) tabBarManualMode() bool {
	tabs := m.curTabs()
	activeIdx := m.activeTabIdx()
	if m.tabScrollAnchor == "" || activeIdx < 0 || activeIdx >= len(tabs) {
		return false
	}
	return tabs[activeIdx].ID == m.tabScrollAnchor
}

// tabBarLayout is the layout engine tabSpans wraps: same labels, same styles,
// same one-column separators as before, plus the manual scroll offset
// (scrollTabBar) and the two-sided overflow markers. It is the SINGLE
// geometry the painter (renderTabBar), the click (hitTestTab) and the reorder
// drag all read via tabSpans, so none of the three can disagree about which
// tab occupies which column — and renderTabBar reads hiddenLeft/hiddenRight
// off this SAME call, so the markers can never report a different set than
// the one actually painted.
//
// Carrying the rendered text costs nothing: the width has to come from
// lipgloss.Width(style.Render(label)) either way, so the string already
// exists and was previously thrown away.
//
// Manual mode is in effect only while tabScrollAnchor names the CURRENT
// active tab — any tab switch changes the active tab without touching the
// anchor, so the compare alone returns the bar to auto mode. That is also why
// this method needs no separate reset hook on every switch path.
func (m Model) tabBarLayout() (spans []tabSpan, hiddenLeft, hiddenRight int) {
	tabs := m.curTabs()
	n := len(tabs)
	if n == 0 {
		return nil, 0, 0
	}
	texts, widths, barW, leftMarkerW, rightMarkerW := m.tabBarWidths()

	totalW := 0
	for i, w := range widths {
		totalW += w
		if i > 0 {
			totalW++
		}
	}

	included := make([]bool, n)
	if totalW <= barW {
		for i := range included {
			included[i] = true
		}
	} else {
		activeIdx := m.activeTabIdx()
		manual := m.tabBarManualMode()

		if manual {
			maxFirst := maxFirstIndex(widths, barW, leftMarkerW)
			first := clampInt(m.tabScrollFirst, 0, maxFirst)
			included[first] = true
			usedW := widths[first]
			if first > 0 {
				usedW += leftMarkerW
			}
			for i := first + 1; i < n; i++ {
				need := widths[i] + 1 // separator
				reserve := 0
				if i < n-1 {
					reserve = rightMarkerW
				}
				if usedW+need+reserve > barW {
					break
				}
				included[i] = true
				usedW += need
			}
		} else {
			// Auto mode, unchanged from before this existed: expand around
			// the active tab alternately left/right. Only the reserve
			// changes — both markers' worst case now, not a fixed guess.
			included[activeIdx] = true
			usedW := widths[activeIdx]
			indicatorReserve := leftMarkerW + rightMarkerW

			left := activeIdx - 1
			right := activeIdx + 1
			for left >= 0 || right < n {
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
				if right < n {
					need := widths[right] + 1
					if usedW+need+indicatorReserve <= barW {
						included[right] = true
						usedW += need
						right++
					} else {
						right = n
					}
				}
			}
		}
	}

	spans = make([]tabSpan, 0, n)
	cursor := 0
	firstIdx, lastIdx := -1, -1
	for i := range tabs {
		if !included[i] {
			continue
		}
		if firstIdx < 0 {
			firstIdx = i
			if firstIdx > 0 {
				// The left marker occupies columns before the first visible
				// tab — span.start must include its width or the painted
				// column (renderTabBar paints the marker first) disagrees
				// with the geometry hitTestTab and the drag read.
				cursor += leftMarkerW
			}
		} else {
			cursor++ // space separator
		}
		spans = append(spans, tabSpan{index: i, start: cursor, width: widths[i], text: texts[i]})
		cursor += widths[i]
		lastIdx = i
	}
	if firstIdx > 0 {
		hiddenLeft = firstIdx
	}
	if lastIdx >= 0 && lastIdx < n-1 {
		hiddenRight = n - 1 - lastIdx
	}
	return spans, hiddenLeft, hiddenRight
}

// tabSpans is a thin wrapper over tabBarLayout for the callers that only need
// the geometry — hitTestTab, tabSpanAt, the reorder drag. renderTabBar is the
// one caller that also needs the hidden-tab counts, and it calls tabBarLayout
// directly so its markers read off the exact spans it paints.
func (m Model) tabSpans() []tabSpan {
	spans, _, _ := m.tabBarLayout()
	return spans
}

// scrollTabBar moves the tab bar's manual scroll window by delta tabs
// (negative = toward tab 0), from the wheel handler in Update. It is a no-op
// — but still swallows the event, per the call site — when there are no tabs
// or every tab already fits.
//
// The starting point is the CURRENT first-visible index, from manual mode's
// own stored value when manual mode is already in effect, or from auto
// mode's computed window otherwise — so the first notch over an
// auto-centered bar scrolls from where the user is actually looking, not
// from tab 0. Re-clamping the stored value against maxFirst BEFORE adding
// the notch (rather than clamping only the result) is what keeps a wheel
// notch after a tab closes or the terminal resizes moving from the clamped
// position instead of a stale one that could be arbitrarily far past it.
func (m *Model) scrollTabBar(delta int) {
	tabs := m.curTabs()
	n := len(tabs)
	if n == 0 {
		return
	}
	_, widths, barW, leftMarkerW, _ := m.tabBarWidths()

	totalW := 0
	for i, w := range widths {
		totalW += w
		if i > 0 {
			totalW++
		}
	}
	if totalW <= barW {
		return
	}

	maxFirst := maxFirstIndex(widths, barW, leftMarkerW)
	activeIdx := m.activeTabIdx()
	manual := m.tabBarManualMode()

	var start int
	if manual {
		start = clampInt(m.tabScrollFirst, 0, maxFirst)
	} else {
		spans := m.tabSpans()
		if len(spans) > 0 {
			start = spans[0].index
		}
	}

	next := clampInt(start+delta, 0, maxFirst)
	// A notch must never move the window opposite to the direction it was
	// turned. Auto mode's window is a conservative APPROXIMATION — it always
	// reserves space for BOTH markers while expanding outward, even on the
	// side that turns out not to need one (an active tab near the end
	// expands only inward, but still pays the far side's marker on every
	// step) — so its start can land PAST maxFirst, the exact "smallest index
	// whose tail already fits with only a left marker" bound manual mode
	// clamps to. From such a position, clamping start+1 back down to
	// maxFirst on a wheel-DOWN notch would move the visible window LEFT, the
	// opposite of what the user just turned the wheel. Refusing the move
	// entirely (rather than clamping it) leaves the bar exactly where it
	// was — indistinguishable from "nothing left to do here", which is what
	// it is.
	if (delta > 0 && next < start) || (delta < 0 && next > start) {
		return
	}

	m.tabScrollFirst = next
	if activeIdx >= 0 && activeIdx < n {
		m.tabScrollAnchor = tabs[activeIdx].ID
	}
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
