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
func (p *ProjectModel) counts() (working, blocked int) {
	for _, tab := range p.tabs {
		if tab.Root == nil {
			continue
		}
		for _, pane := range tab.Leaves() {
			if pane.working {
				working++
			}
			if !pane.blockedSince.IsZero() {
				blocked++
			}
		}
	}
	return working, blocked
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
		working, blocked := p.counts()
		rows = append(rows, sidebarRow{
			text: projectRow(sanitizeRemoteText(p.displayName()),
				working, blocked, m.linkGlyph(p.Dest), i == m.activeProject, w),
			kind:  sidebarRowProject,
			index: i,
		})
	}

	rows = append(rows, sidebarRow{}, sidebarRow{text: sidebarHeading("PANES", w)})
	ordinal := 0
	for ti, tab := range m.curTabs() {
		rows = append(rows, sidebarRow{text: sidebarTabHeading(sanitizeRemoteText(tab.Name), w)})
		for _, pane := range tab.Leaves() {
			rows = append(rows, sidebarRow{
				text:   paneRow(pane, w),
				kind:   sidebarRowPane,
				index:  ordinal,
				tabIdx: ti,
				paneID: pane.ID,
			})
			ordinal++
		}
	}
	return rows
}

// renderSidebar renders the project sidebar. height is the number of content
// rows to fill — the tab bar and status bar are drawn separately by View()
// and are not part of this block.
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

	rows := m.sidebarRows(w)
	lines := make([]string, len(rows))
	for i, r := range rows {
		lines[i] = r.text
	}

	content := strings.Join(lines, "\n")
	// .Width/.Height are the authoritative sizing pass (ANSI- and wide-glyph-
	// aware) over the rune-count padding each row builder already applied —
	// same belt-and-suspenders pattern NotificationCenter.View uses for the
	// same reason: this block is about to sit inside a lipgloss.JoinHorizontal
	// next to tab content, and a ragged row there staggers every line after it.
	return lipgloss.NewStyle().Width(w).Height(height).Render(content)
}

// sidebarRowAt resolves the project-sidebar row under a SCREEN coordinate.
// View() joins the sidebar into tabContent, which starts at screen row 1
// (row 0 is the full-width tab bar) and ends before the status bar, so
// screen row y is sidebar row y-1.
func (m *Model) sidebarRowAt(x, y int) (sidebarRow, bool) {
	w := m.projectSidebarWidth()
	if w <= 0 || x < 0 || x >= w {
		return sidebarRow{}, false
	}
	if y < 1 || y >= m.height-1 {
		return sidebarRow{}, false
	}
	rows := m.sidebarRows(w)
	if i := y - 1; i < len(rows) {
		return rows[i], true
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
// clicked. Row 0 (tab bar) and the last row (status bar) are exempt — both
// are drawn full width, above and below the sidebar.
func (m Model) projectSidebarSwallowsMouse(x, y int) bool {
	w := m.projectSidebarWidth()
	return w > 0 && x >= 0 && x < w && y >= 1 && y < m.height-1
}

// sidebarHeadingStyle / sidebarDimStyle / the state-glyph styles mirror the
// palette already used for tab/pane state elsewhere (styles.go): amber for
// blocked-on-user, blue for active work, green for done-unseen, dim grey for
// idle and section chrome.
var (
	sidebarHeadingStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("244"))
	sidebarDimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	sidebarActiveStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230"))
	sidebarProjectStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	sidebarBlockedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	sidebarWorkingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	sidebarUnseenStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("28"))
)

func sidebarHeading(title string, w int) string {
	return sidebarHeadingStyle.Render(truncateRunes(title, w))
}

func sidebarTabHeading(name string, w int) string {
	return sidebarDimStyle.Render(truncateRunes(" "+name, w))
}

// projectRow renders one project's summary line: an active-project marker,
// its (already sanitized, dest-qualified) name, and a trailing badge of
// working/blocked counts plus link health. name is expected pre-sanitized —
// every call site in renderSidebar routes the raw daemon-sourced value
// through sanitizeRemoteText before reaching here.
func projectRow(name string, working, blocked int, link string, active bool, w int) string {
	marker := "  "
	if active {
		marker = "▸ "
	}
	badge := ""
	if working > 0 {
		badge += fmt.Sprintf(" ◐%d", working)
	}
	if blocked > 0 {
		badge += fmt.Sprintf(" ⚠%d", blocked)
	}
	if link != "" {
		badge += " " + link
	}

	avail := w - runeLen(marker) - runeLen(badge)
	if avail < 1 {
		avail = 1
	}
	name = truncateRunes(name, avail)

	line := marker + name
	if gap := w - runeLen(line) - runeLen(badge); gap > 0 {
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
func paneRow(pane *PaneModel, w int) string {
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
		if pane.subagents > 0 {
			suffix = fmt.Sprintf(" ⋯%d", pane.subagents)
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

	prefix := "  " + glyph + " "
	avail := w - runeLen(prefix) - runeLen(suffix)
	if avail < 1 {
		avail = 1
	}
	label = truncateRunes(label, avail)

	return style.Render(padOrTrunc(prefix+label+suffix, w))
}

// padOrTrunc truncates or right-pads (with plain spaces) s to exactly w
// runes, so every sidebar row occupies the identical column count before
// styling is applied — see renderSidebar's comment on why that matters for
// lipgloss.JoinHorizontal.
func padOrTrunc(s string, w int) string {
	s = truncateRunes(s, w)
	if pad := w - runeLen(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}
