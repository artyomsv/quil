package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/changelog"
)

// whatsNewMaxWidth is wider than dialogWidth (60) on purpose. This dialog is a
// reading surface, not a button form: at 60 columns a headline gets ~53 usable
// cells after the border and bullet, which is short enough that headlines stop
// being sentences. The 64-byte headline limit enforced by
// scripts/promote-changelog.sh is set against THIS width.
const whatsNewMaxWidth = 76

// whatsNewMinWidth keeps the dialog readable on a very narrow terminal rather
// than collapsing to nothing.
const whatsNewMinWidth = 20

// fixesAutoExpandLimit is the count at or below which the fixes list renders
// expanded. Collapsing three items is friction with no benefit.
const fixesAutoExpandLimit = 5

// releasesURL is the footer link — the full, unabridged changelog.
const releasesURL = "github.com/artyomsv/quil/releases"

// whatsNewWidth clamps the dialog to the terminal. A zero or unknown width
// falls back to the maximum: NewModel builds this dialog before the first
// WindowSizeMsg arrives, so the first frame can overflow a narrow terminal and
// corrects itself on the first resize.
func whatsNewWidth(termWidth int) int {
	if termWidth <= 0 {
		return whatsNewMaxWidth
	}
	w := termWidth - 4
	if w > whatsNewMaxWidth {
		return whatsNewMaxWidth
	}
	if w < whatsNewMinWidth {
		return whatsNewMinWidth
	}
	return w
}

// splitEntries groups a window's headlines into the four rendered sections.
// Deprecated and Removed fold into Changed: the distinction matters to a
// changelog reader and not to someone scanning what happened while they were
// away. Order is newest release first, then file order within a release.
func splitEntries(w changelog.Window) (added, changed, security, fixed []string) {
	for _, r := range w.Releases {
		for _, e := range r.Entries {
			switch e.Kind {
			case changelog.KindAdded:
				added = append(added, e.Text)
			case changelog.KindChanged, changelog.KindDeprecated, changelog.KindRemoved:
				changed = append(changed, e.Text)
			case changelog.KindSecurity:
				security = append(security, e.Text)
			case changelog.KindFixed:
				fixed = append(fixed, e.Text)
			}
		}
	}
	return added, changed, security, fixed
}

// openWhatsNew installs a window and opens the dialog.
func (m *Model) openWhatsNew(w changelog.Window) {
	_, _, _, fixed := splitEntries(w)
	m.dialog = dialogWhatsNew
	m.whatsNew = &w
	m.whatsNewExpanded = len(fixed) <= fixesAutoExpandLimit
	m.whatsNewScroll = 0
}

func (m Model) handleWhatsNewKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "enter":
		// Enter dismisses rather than expanding, matching the disclaimer and
		// update-notice dialogs where Enter activates the focused button.
		m.dialog = dialogNone
		m.whatsNewScroll = 0
		return m, tea.ClearScreen
	case "right", "l":
		m.whatsNewExpanded = true
		m.whatsNewScroll = 0
	case "left", "h":
		m.whatsNewExpanded = false
		m.whatsNewScroll = 0
	case "down", "j":
		m.whatsNewScroll++
	case "up", "k":
		if m.whatsNewScroll > 0 {
			m.whatsNewScroll--
		}
	case "pgdown":
		m.whatsNewScroll += 10
	case "pgup":
		m.whatsNewScroll -= 10
		if m.whatsNewScroll < 0 {
			m.whatsNewScroll = 0
		}
	}
	return m, nil
}

func (m Model) renderWhatsNewDialog() string {
	if m.whatsNew == nil {
		return ""
	}
	w := *m.whatsNew
	width := whatsNewWidth(m.lastWidth)

	var b strings.Builder
	b.WriteString(lipgloss.PlaceHorizontal(width, lipgloss.Center,
		dialogTitle.Render("What's new in Quil")))
	b.WriteString("\n\n")

	// A window with no From is the F1 path's single release, not an upgrade.
	header := "  Quil v" + w.To
	if w.From != "" {
		header = fmt.Sprintf("  You updated  v%s → v%s", w.From, w.To)
	}
	count := ""
	if w.Total > 1 {
		count = fmt.Sprintf("%d releases  ", w.Total)
	}
	b.WriteString(padPair(dialogNormal.Render(header), dialogSubtle.Render(count), width))
	b.WriteByte('\n')

	added, changed, security, fixed := splitEntries(w)
	section := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		b.WriteByte('\n')
		b.WriteString(dialogSelected.Render("  " + title))
		b.WriteByte('\n')
		for _, it := range items {
			b.WriteString(dialogNormal.Render(truncateToWidth("   • "+it, width)))
			b.WriteByte('\n')
		}
	}
	section("New", added)
	section("Changed", changed)
	section("Security", security)

	if len(fixed) > 0 {
		if m.whatsNewExpanded {
			section("Fixed", fixed)
		} else {
			b.WriteByte('\n')
			b.WriteString(padPair(
				dialogNormal.Render(fmt.Sprintf("   › %d fixes", len(fixed))),
				dialogSubtle.Render("→ to expand  "), width))
			b.WriteByte('\n')
		}
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render(truncateToWidth("  "+releasesURL, width)))
	b.WriteString("\n\n")
	b.WriteString(lipgloss.PlaceHorizontal(width, lipgloss.Center,
		dialogSelected.Render("  OK  ")))

	return m.applyWhatsNewScroll(b.String(), width)
}

// padPair renders left and right on one row of exactly width cells, dropping
// the right half when there is no room for it.
func padPair(left, right string, width int) string {
	pad := width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		return truncateToWidth(left, width)
	}
	return left + strings.Repeat(" ", pad) + right
}

// applyWhatsNewScroll windows the rendered body to the terminal height and
// appends a position indicator. Engaged only when the content overflows —
// expanding the fixes list is the usual way to reach that state.
func (m Model) applyWhatsNewScroll(content string, width int) string {
	lines := strings.Split(content, "\n")
	// 6 cells for the dialog border, padding and surrounding chrome.
	maxRows := m.lastHeight - 6
	if maxRows <= 0 || len(lines) <= maxRows {
		return content
	}
	start := m.whatsNewScroll
	if limit := len(lines) - maxRows; start > limit {
		start = limit
	}
	if start < 0 {
		start = 0
	}
	pos := fmt.Sprintf("%d/%d  ↑↓ scroll", start+1, len(lines)-maxRows+1)
	return strings.Join(lines[start:start+maxRows], "\n") + "\n" +
		lipgloss.PlaceHorizontal(width, lipgloss.Center, dialogSubtle.Render(pos))
}
