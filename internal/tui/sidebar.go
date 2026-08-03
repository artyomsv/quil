package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// minWidthForSidebar auto-collapses the project sidebar rather than
// squeezing panes into unusability on a narrow terminal.
const (
	minWidthForSidebar  = 100
	defaultSidebarWidth = 22
	// minPaneLabelCells is the floor a pane's own name keeps in its row, so a
	// long blocked-reason cannot crowd out the thing that identifies which
	// pane the row is about. Eight cells is enough for the id suffix quil
	// generates ("pane-b16e" of "pane-b16e3850") when a pane has no name.
	minPaneLabelCells = 8
)

// sidebarWidth returns the layout width the project sidebar reserves: 0 when
// closed or the terminal is too narrow to spare it, otherwise the configured
// width (falling back to defaultSidebarWidth when unset/invalid, and capped
// so at least minTermWidth columns always remain for panes). Model's
// paneAreaWidth/resizeTabs read this — it is the single source of truth for
// how much screen estate the sidebar takes from the panes, so this is the one
// place that has to bound it: an unclamped configured value larger than the
// terminal would drive paneAreaWidth() negative and reach tab.Resize and
// lipgloss.Width() with it.
func sidebarWidth(total int, open bool, configured int) int {
	if !open || total < minWidthForSidebar {
		return 0
	}
	if configured <= 0 {
		configured = defaultSidebarWidth
	}
	if max := total - minTermWidth; configured > max {
		configured = max
	}
	return configured
}

// displayName appends the destination for a remote project so two projects
// called "api" on different hosts stay distinguishable.
func (p *ProjectModel) displayName() string {
	if p.Dest == "" {
		return p.Name
	}
	return p.Name + "@" + p.Dest
}

// counts reports panes working and panes blocked on the user, for the
// project's summary row.
// counts aggregates the pane states a project row summarises. `done` counts
// panes that finished while unfocused: without it a turn completing in a
// BACKGROUND project is invisible at the project level, so the one place the
// user is looking when they are not in that project never tells them the work
// is ready — which is most of the reason to group panes by project at all.
func (p *ProjectModel) counts() (working, blocked, done int) {
	for _, tab := range p.tabs {
		if tab.Root == nil {
			continue
		}
		for _, pane := range tab.Leaves() {
			switch {
			// Ordered, not independent: a pane parked for input has also
			// finished its turn, and "needs you" outranks "is ready".
			case !pane.blockedSince.IsZero():
				blocked++
			case pane.working:
				working++
			case pane.unseen:
				done++
			}
		}
	}
	return working, blocked, done
}

// linkGlyph reports the connection health of a destination: ⟳ reconnecting,
// ⚡ parked, empty when healthy. Reads through linkOf, never linkFor — linkFor
// has a pointer receiver and CREATES a map entry on first use, and this runs
// on every render of every project row, so calling it here would allocate a
// reconnectState for every destination that has never dropped, once per
// frame, only to throw it away (a value-receiver View can't keep the
// mutation anyway).
func (m *Model) linkGlyph(dest string) string {
	ls := m.linkOf(dest)
	switch {
	case ls.parked:
		return "⚡"
	case ls.active:
		return "⟳"
	default:
		return ""
	}
}

// sidebarRowProject / sidebarRowPane label an actionable sidebar row. The
// empty kind is chrome (the two headings, the spacer, per-tab headings):
// inert, but still inside the strip, so a click on one is swallowed rather
// than falling through to whatever pane the strip displaced.
const (
	sidebarRowProject = "project"
	sidebarRowPane    = "pane"
)

// sidebarRow is one rendered row of the project sidebar: the painted text
// plus what it points at. renderSidebar joins the text of this slice and
// sidebarRowAt indexes the same slice, so a row inserted into the paint
// cannot drift out of step with what a click on that row resolves to —
// hit-testing written as a second, independent copy of the row layout is
// exactly how that drift happens.
type sidebarRow struct {
	text   string
	kind   string
	index  int    // project index (kind project) or pane ordinal (kind pane)
	tabIdx int    // pane rows only: index into curTabs()
	paneID string // pane rows only
}

// sidebarRows builds the sidebar's rows in paint order at width w: every
// project with its aggregate working/blocked counts and link health (active
// project marked), then the active project's tabs and panes with per-pane
// agent-state glyphs.
func (m *Model) sidebarRows(w int) []sidebarRow {
	rows := []sidebarRow{{text: sidebarHeading("PROJECTS", w)}}
	for i, p := range m.projects {
		// Same separation the tab groups get, and needed for the same reason
		// now that a remote project spans two rows: without it a host row and
		// the next project's name row sit adjacent and read as one entry.
		if i > 0 {
			rows = append(rows, sidebarRow{})
		}
		working, blocked, done := p.counts()
		// The NAME alone on the first row. displayName's "name@dest" was
		// written for the picker, where a dialog is wide enough for it; at the
		// sidebar's 22 columns "Default@artyom@192.168.6.12" leaves nothing of
		// either half, and the badges that say whether the project needs you
		// are what gets truncated away first.
		rows = append(rows, sidebarRow{
			text: projectRow(sanitizeRemoteText(p.Name),
				working, blocked, done, m.linkGlyph(p.Dest), i == m.activeProject, w),
			kind:  sidebarRowProject,
			index: i,
		})
		// The host gets its own row, and only a remote project has one — a
		// local project spending a line to say "this machine" would halve how
		// many projects fit for no information. Same kind and index, so a
		// click on either row selects the same project rather than falling
		// through to the pane underneath.
		if p.Dest != "" {
			rows = append(rows, sidebarRow{
				text:  projectDestRow(sanitizeRemoteText(p.Dest), w),
				kind:  sidebarRowProject,
				index: i,
			})
		}
	}

	rows = append(rows, sidebarRow{}, sidebarRow{text: sidebarHeading("PANES", w)})
	// The active tab and the pane inside it that holds focus are marked the
	// same way the active project is, so one glance answers "where am I"
	// at all three levels. Only the ACTIVE tab's focused pane is marked:
	// every tab carries an ActivePane, and marking all of them would say
	// "you are here" in several places at once.
	activeTabIdx := -1
	if p := m.cur(); p != nil {
		activeTabIdx = p.activeTab
	}
	ordinal := 0
	for ti, tab := range m.curTabs() {
		onTab := ti == activeTabIdx
		// A blank row between tab groups. Without it the whole PANES section
		// is one unbroken column and a tab heading reads as just another pane
		// row — the grouping the section exists to show is the first thing
		// lost. Not before the FIRST group: the section heading already
		// separates it, and a second blank there is just wasted height on a
		// strip that scrolls.
		if ti > 0 {
			rows = append(rows, sidebarRow{})
		}
		rows = append(rows, sidebarRow{text: sidebarTabHeading(sanitizeRemoteText(tab.Name), onTab, w)})
		for _, pane := range tab.Leaves() {
			rows = append(rows, sidebarRow{
				text:   paneRow(pane, onTab && pane.ID == tab.ActivePane, w),
				kind:   sidebarRowPane,
				index:  ordinal,
				tabIdx: ti,
				paneID: pane.ID,
			})
			ordinal++
			// Git state gets its own row rather than more suffix: at the
			// default 22 columns a branch name and a pane name cannot share
			// one. Non-interactive, like a tab heading — giving it the pane's
			// ordinal would put two rows on one index and desync every hit
			// test from the attention queue's numbering.
			if git := gitRow(pane, w); git != "" {
				rows = append(rows, sidebarRow{text: git})
			}
		}
	}
	return rows
}

// renderSidebar renders the project sidebar. height is the number of screen
// rows to fill, and callers pass sidebarContentHeight() — the strip spans the
// TAB BAR row too (the bar is joined inside the pane column, to the right of
// this block), so only the status bar is drawn separately by View().
//
// The width comes from projectSidebarWidth(), NOT the raw m.sidebarWidth
// field: that field is the CONFIGURED value, and sidebarWidth() is what
// clamps it against the terminal. Sizing the box off the raw field made the
// pane area clamp correctly while this box did not, so the
// lipgloss.JoinHorizontal in View() composited a frame wider than the
// terminal for any sidebar_width larger than it. The <= 0 fallback only
// covers callers with no window size yet (tests) — View() never draws the
// sidebar unless projectSidebarWidth() is already positive.
func (m *Model) renderSidebar(height int) string {
	w := m.projectSidebarWidth()
	if w <= 0 {
		w = defaultSidebarWidth
	}

	rows := m.sidebarVisibleRows(w, height)
	lines := make([]string, len(rows))
	for i, r := range rows {
		lines[i] = r.text
	}

	content := strings.Join(lines, "\n")
	// .Width/.Height is a final sizing pass over rows this file has already
	// padded to exactly w CELLS (padOrTrunc). It must never have anything
	// left to do horizontally: .Width WRAPS an over-wide line onto a new
	// one rather than truncating it, which would shift every row below it
	// while sidebarRowAt still maps screen row y to rows[y-1].
	return lipgloss.NewStyle().Width(w).Height(height).Render(content)
}

// sidebarVisibleRows caps sidebarRows to the rows that actually fit in
// height. lipgloss's .Height PADS but never CLIPS, so an uncapped list
// (1 + projects + 2 + Σ(1 + panes) rows, all unbounded) grows the composited
// block past the terminal and pushes the status bar off the bottom —
// the vertical twin of the width clamp, and the reason
// NotificationCenter.View computes its own maxVisible rather than trusting
// .Height. minWidthForSidebar gates columns only.
//
// The TAIL is what gets dropped, and the last visible row becomes an
// explicit overflow marker rather than silently ending: the PROJECTS block
// is the navigation the sidebar exists for, and trimming from the top would
// shift every remaining row while sidebarRowAt still indexes rows[y-1].
//
// Both the paint and the hit test call this, with the same height — a cap
// applied in only one of them is the row-drift bug in another form.
func (m *Model) sidebarVisibleRows(w, height int) []sidebarRow {
	rows := m.sidebarRows(w)
	if height <= 0 || len(rows) <= height {
		return rows
	}
	rows = rows[:height]
	rows[height-1] = sidebarRow{text: sidebarDimStyle.Render(padOrTrunc(" …", w))}
	return rows
}

// sidebarRowAt resolves the project-sidebar row under a SCREEN coordinate.
// View() joins the sidebar to the LEFT of the pane column — tab bar included
// — so the strip starts at screen row 0 and ends before the status bar, and
// screen row y is sidebar row y. Its first row is the PROJECTS heading,
// which is why the design puts that heading level with the tab names.
func (m *Model) sidebarRowAt(x, y int) (sidebarRow, bool) {
	w := m.projectSidebarWidth()
	if w <= 0 || x < 0 || x >= w {
		return sidebarRow{}, false
	}
	if y < 0 || y >= m.height-1 {
		return sidebarRow{}, false
	}
	// Same height View() passes renderSidebar, so paint and hit test cap
	// at the identical row.
	rows := m.sidebarVisibleRows(w, m.sidebarContentHeight())
	if y < len(rows) {
		return rows[y], true
	}
	return sidebarRow{}, false
}

// sidebarHit maps a screen coordinate to the sidebar row under it, as a
// kind ("project" / "pane") and that kind's index. Returns ("", -1) for any
// x at or beyond the reserved width — the panes begin exactly there — and
// for inert chrome rows inside the strip.
func (m *Model) sidebarHit(x, y int) (kind string, index int) {
	row, ok := m.sidebarRowAt(x, y)
	if !ok || row.kind == "" {
		return "", -1
	}
	return row.kind, row.index
}

// projectSidebarSwallowsMouse reports whether a press or wheel at (x, y)
// lands on the project sidebar's strip. Deliberately wider than sidebarHit:
// chrome rows resolve to no action but must still be swallowed, because the
// pane area now starts at column projectSidebarWidth() and letting the press
// fall through would arm a drag-selection at a column the user never
// clicked.
//
// Row 0 is INCLUDED: the tab bar no longer spans the frame, it starts where
// the sidebar ends, so the sidebar's own first row occupies row 0 in these
// columns. Excluding it let a click on the PROJECTS heading reach the
// Y==0 tab-bar branch in Update and switch tabs. Only the last row (the
// status bar, still drawn full width beneath the sidebar) is exempt.
func (m Model) projectSidebarSwallowsMouse(x, y int) bool {
	w := m.projectSidebarWidth()
	return w > 0 && x >= 0 && x < w && y >= 0 && y < m.height-1
}

// sidebarHeadingStyle / sidebarDimStyle / the state-glyph styles mirror the
// palette already used for tab/pane state elsewhere (styles.go): amber for
// blocked-on-user, blue for active work, green for done-unseen, dim grey for
// idle and section chrome.
var (
	sidebarHeadingStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("244"))
	sidebarDimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	sidebarActiveStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230"))
	sidebarProjectStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	sidebarBlockedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	sidebarWorkingStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	sidebarUnseenStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("28"))
	sidebarGitStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	sidebarGitStaleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Italic(true)
)

// projectDestRow renders a remote project's host under its name. Indented and
// dimmed so the column still reads as a list of projects rather than eight
// entries, and elided in the middle for the same reason a branch is: an ssh
// destination is user@host, and cutting either end alone leaves a column where
// every row looks the same.
func projectDestRow(dest string, w int) string {
	const prefix = "   @"
	avail := w - len(prefix)
	if avail < 1 {
		avail = 1
	}
	return sidebarGitStyle.Render(padOrTrunc(prefix+elideMiddle(dest, avail), w))
}

// minGitBranchCells is the floor a branch name keeps on its row, for the same
// reason paneRow has one: the branch is the answer, the divergence counts are
// detail about it. Subtracting the counts first would leave "fea…" beside a
// crisp "↑12↓3".
const minGitBranchCells = 8

// gitRow renders a pane's checkout beneath it: branch, linked-worktree marker,
// and divergence from upstream. Empty string when there is nothing to say —
// a pane outside a repository gets no row at all rather than a blank one.
//
// The counts are omitted when the branch has no upstream, because "↑0↓0" and
// "no upstream to compare against" are different facts and only one of them is
// true. A stale entry is dimmed and marked rather than hidden: the last branch
// we actually saw is more useful than nothing, as long as it does not claim to
// be current.
func gitRow(pane *PaneModel, w int) string {
	name := pane.GitBranch
	if name == "" && pane.GitDetached {
		name = "detached"
	}
	if name == "" && !pane.GitWorktree {
		return ""
	}
	name = sanitizeRemoteText(name)

	prefix := "  ⎇ "
	var suffix string
	if pane.GitWorktree {
		suffix += " wt"
	}
	if pane.GitUpstream {
		if pane.GitAhead > 0 {
			suffix += fmt.Sprintf(" ↑%d", pane.GitAhead)
		}
		if pane.GitBehind > 0 {
			suffix += fmt.Sprintf(" ↓%d", pane.GitBehind)
		}
	}
	if pane.GitStale {
		suffix += " ~"
	}

	avail := w - lipgloss.Width(prefix)
	if avail < 1 {
		avail = 1
	}
	nameW := avail - lipgloss.Width(suffix)
	if nameW < minGitBranchCells {
		nameW = minGitBranchCells
	}
	if nameW > avail {
		nameW = avail
	}
	name = elideMiddle(name, nameW)
	suffix = truncateCells(suffix, avail-lipgloss.Width(name))

	style := sidebarGitStyle
	if pane.GitStale {
		style = sidebarGitStaleStyle
	}
	return style.Render(padOrTrunc(prefix+name+suffix, w))
}

func sidebarHeading(title string, w int) string {
	return sidebarHeadingStyle.Render(truncateCells(title, w))
}

// sidebarTabHeading renders one tab's name above its panes. The active tab
// carries the same ▸ marker as the active project, in the same column, so the
// two read as one vocabulary rather than two conventions.
func sidebarTabHeading(name string, active bool, w int) string {
	marker := " "
	style := sidebarDimStyle
	if active {
		marker = "▸"
		style = sidebarActiveStyle
	}
	return style.Render(truncateCells(marker+name, w))
}

// projectRow renders one project's summary line: an active-project marker,
// its (already sanitized, dest-qualified) name, and a trailing badge of
// working/blocked counts plus link health. name is expected pre-sanitized —
// every call site in renderSidebar routes the raw daemon-sourced value
// through sanitizeRemoteText before reaching here.
func projectRow(name string, working, blocked, done int, link string, active bool, w int) string {
	marker := "  "
	if active {
		marker = "▸ "
	}
	// Badge order is urgency order, and it is the same glyph vocabulary the
	// pane rows use so the summary reads as a roll-up rather than a second
	// notation: ⚠ needs you, ◐ still running, ✓ finished while you were away.
	badge := ""
	if blocked > 0 {
		badge += fmt.Sprintf(" ⚠%d", blocked)
	}
	if working > 0 {
		badge += fmt.Sprintf(" ◐%d", working)
	}
	if done > 0 {
		badge += fmt.Sprintf(" ✓%d", done)
	}
	if link != "" {
		badge += " " + link
	}

	avail := w - lipgloss.Width(marker) - lipgloss.Width(badge)
	if avail < 1 {
		avail = 1
	}
	name = truncateCells(name, avail)

	line := marker + name
	if gap := w - lipgloss.Width(line) - lipgloss.Width(badge); gap > 0 {
		line += strings.Repeat(" ", gap)
	}
	line += badge

	style := sidebarProjectStyle
	if active {
		style = sidebarActiveStyle
	}
	return style.Render(padOrTrunc(line, w))
}

// paneRow renders one pane's agent state: ◐ working (with ⋯N outstanding
// subagents when any are running), ⚠ blocked-on-user (with the hook-reported
// tool name when present — never invented when blockedReason is empty), ✓
// done and unseen, ○ idle. Every remote-sourced string (the pane's name/ID,
// the blocked reason) is sanitized here since this is a render path a
// remote-attached daemon's data reaches directly.
// paneRow renders one pane's agent state. `focused` marks the pane the user
// is actually typing into — with the ▸ marker rather than a colour, because
// the row's colour already carries the pane's STATE (blocked, working, unseen)
// and that is the more urgent signal of the two. A blocked pane must stay
// visibly blocked whether or not it happens to be focused.
func paneRow(pane *PaneModel, focused bool, w int) string {
	var glyph string
	var style lipgloss.Style
	var suffix string
	switch {
	case !pane.blockedSince.IsZero():
		glyph, style = "⚠", sidebarBlockedStyle
		if pane.blockedReason != "" {
			suffix = " " + sanitizeRemoteText(pane.blockedReason)
		}
	case pane.working:
		glyph, style = "◐", sidebarWorkingStyle
		// "+" when the ledger overflowed: a refused start may still be live
		// with no entry to count, so the number is a floor. Marking it beats
		// printing a confidently low count — and the badge still appears when
		// the overflow is the ONLY reason the pane reads as working.
		if n := pane.outstandingSubagents(); n > 0 || pane.subagentsOverflow {
			mark := ""
			if pane.subagentsOverflow {
				mark = "+"
			}
			suffix = fmt.Sprintf(" ⋯%d%s", n, mark)
		}
	case pane.unseen:
		glyph, style = "✓", sidebarUnseenStyle
	default:
		glyph, style = "○", sidebarDimStyle
	}

	label := pane.Name
	if label == "" {
		label = pane.ID
	}
	label = sanitizeRemoteText(label)

	marker := "  "
	if focused {
		marker = "▸ "
	}
	prefix := marker + glyph + " "
	avail := w - lipgloss.Width(prefix)
	if avail < 1 {
		avail = 1
	}
	// The label says WHICH pane; the suffix is secondary detail (the tool a
	// pane is blocked on, its subagent count). Subtracting the suffix first
	// inverts that: a long tool name like "AskUserQuestion" leaves two cells
	// for the name, so the row reads "⚠ cl AskUserQuestion" and no longer
	// identifies the pane at all. Give the label a floor and truncate the
	// suffix into whatever is left instead.
	labelW := avail - lipgloss.Width(suffix)
	if labelW < minPaneLabelCells {
		labelW = minPaneLabelCells
	}
	if labelW > avail {
		labelW = avail
	}
	label = truncateCells(label, labelW)
	suffix = truncateCells(suffix, avail-lipgloss.Width(label))

	return style.Render(padOrTrunc(prefix+label+suffix, w))
}

// truncateCells cuts s to at most w CELLS, not runes.
//
// A rune count is not a width, and the difference is not theoretical here:
// linkGlyph's ⚡ (U+26A1) is one rune and two cells, ⚠ and the CJK or emoji
// characters that reach these rows through project names, pane names and
// blockedReason are the same — all remote-sourced text that
// sanitizeRemoteText deliberately preserves non-ASCII in. Rune-counted
// padding therefore produced rows of w runes and MORE than w cells, and
// renderSidebar's closing .Width(w) WRAPS the excess onto a new painted
// line instead of truncating it. That shifts every row below it down by one
// while sidebarRowAt still maps screen row y to rows[y-1] — the user clicks
// project 3 and selects project 2.
//
// A wide glyph that would straddle the boundary is dropped whole (padOrTrunc
// backfills the odd cell with a space): emitting half of one is a different
// character. No ellipsis — unlike palette rows these are padded to an exact
// column count, and an ellipsis cell would come out of the content budget.
// elideMiddle shortens to w cells by removing the MIDDLE, keeping both ends.
//
// Branch names are the case this exists for. They are conventionally
// prefix/suffix pairs — feat/projects-sidebar, fix/ghost-replay — where the
// prefix says the kind of work and the tail says which work, so cutting either
// end alone throws away half the identity: a column of "feat/proje…" rows is
// indistinguishable, and a column of "…ts-sidebar" rows has lost the
// convention the user organises by. Falls back to plain truncation below the
// width where an elision would cost more than it saves.
func elideMiddle(s string, w int) string {
	if w <= 0 || lipgloss.Width(s) <= w {
		return truncateCells(s, w)
	}
	// Under this, "a…b" is mostly ellipsis — a tail-truncated string carries
	// more information than two one-character stubs.
	const minElide = 8
	if w < minElide {
		return truncateCells(s, w)
	}
	runes := []rune(s)
	head := (w - 1) / 2
	tail := w - 1 - head
	return string(runes[:head]) + "…" + string(runes[len(runes)-tail:])
}

func truncateCells(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if used+rw > w {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String()
}

// padOrTrunc truncates or right-pads (with plain spaces) s to exactly w
// CELLS, so every sidebar row occupies the identical column count before
// styling is applied — see truncateCells for why cells and not runes, and
// renderSidebar's comment on why an exact count matters.
func padOrTrunc(s string, w int) string {
	s = truncateCells(s, w)
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}
