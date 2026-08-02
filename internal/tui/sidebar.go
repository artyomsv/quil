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

// renderSidebar renders the project sidebar: every project with its
// aggregate working/blocked counts and link health (active project marked),
// then the active project's tabs and panes with per-pane agent-state
// glyphs. height is the number of content rows to fill — the tab bar and
// status bar are drawn separately by View() and are not part of this block.
func (m *Model) renderSidebar(height int) string {
	w := m.sidebarWidth
	if w <= 0 {
		w = defaultSidebarWidth
	}

	var lines []string
	lines = append(lines, sidebarHeading("PROJECTS", w))
	for i, p := range m.projects {
		working, blocked := p.counts()
		lines = append(lines, projectRow(sanitizeRemoteText(p.displayName()),
			working, blocked, m.linkGlyph(p.Dest), i == m.activeProject, w))
	}

	lines = append(lines, "")
	lines = append(lines, sidebarHeading("PANES", w))
	for _, tab := range m.curTabs() {
		lines = append(lines, sidebarTabHeading(sanitizeRemoteText(tab.Name), w))
		for _, pane := range tab.Leaves() {
			lines = append(lines, paneRow(pane, w))
		}
	}

	content := strings.Join(lines, "\n")
	// .Width/.Height are the authoritative sizing pass (ANSI- and wide-glyph-
	// aware) over the rune-count padding each row builder already applied —
	// same belt-and-suspenders pattern NotificationCenter.View uses for the
	// same reason: this block is about to sit inside a lipgloss.JoinHorizontal
	// next to tab content, and a ragged row there staggers every line after it.
	return lipgloss.NewStyle().Width(w).Height(height).Render(content)
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
