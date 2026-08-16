package tui

import (
	"fmt"
	"log"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/changelog"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/update"
	"github.com/artyomsv/quil/internal/version"
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

// resolveWhatsNew decides whether the post-upgrade dialog should open, and
// records the running version.
//
// The comparison is a persisted string against this binary's version, which
// makes it indifferent to HOW the upgrade happened. Returns nil when there is
// nothing to show.
//
// A user upgrading from a version that predates this feature has no marker, and
// an absent marker is indistinguishable from a fresh install — the binary that
// would have written it did not contain this feature. That one launch therefore
// shows nothing. The marker is written then, so every later upgrade works. This
// is inherent to any marker-based scheme and is not closed by backfilling data.
func resolveWhatsNew(v string) *changelog.Window {
	// A dev or unstamped build has no meaningful version, and recording "dev"
	// would make the next real launch look like a downgrade.
	if _, _, _, err := version.Parsed(v); err != nil {
		return nil
	}
	path := config.LastRunPath()
	last := update.LoadLastRunVersion(path)
	if err := update.SaveLastRunVersion(path, v); err != nil {
		log.Printf("save last-run version: %v", err)
	}
	if last == "" {
		return nil // fresh install, or a first launch after a pre-feature version
	}
	cmp, err := version.Compare(last, v)
	if err != nil || cmp >= 0 {
		return nil // unparseable marker, same version, or a downgrade
	}
	w, ok := changelog.Between(last, v)
	if !ok || len(w.Releases) == 0 {
		return nil
	}
	return &w
}

// latestWindow is the F1 path: a one-release window built straight from the
// newest record. Deriving it as a range from the previous record would come
// back empty on the release that ships this feature, making the menu row a
// silent no-op on exactly the version that introduces it.
//
// A package-var seam, following listFilesystemRoots: until the first release
// cut after this lands, the embedded file holds no records, so the "opens the
// dialog" branch is statically unreachable from a test and only the no-op
// branch would ever be exercised. That is the shape where a wiring bug — the
// wrong index, an inverted ok check — passes every test in the repo.
var latestWindow = func() (changelog.Window, bool) {
	r := changelog.Latest()
	if r == nil || len(r.Entries) == 0 {
		return changelog.Window{}, false
	}
	return changelog.Window{
		To:       r.Version,
		Total:    1,
		Releases: []changelog.Release{*r},
	}, true
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
			b.WriteString(dialogNormal.Render(
				truncateToWidth("   • "+sanitizeHeadline(it), width)))
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

// sanitizeHeadline makes one release headline safe to write to a terminal.
//
// The text is authored in a pull request and travels fragment → shell printf →
// highlights.txt → go:embed → this row. promote-changelog.sh's charset check is
// BYTE-wise (`tr -d '\040-\176\200-\377'`), so it rejects only C0 and DEL:
// every high byte passes. That admits U+009B — the single-rune C1 CSI
// introducer internal/tui/oscfilter.go already exists because of — and the bidi
// overrides U+202A–U+202E, which are *printable* and so pass any
// control-character filter while reversing the rendered row.
//
// ToValidUTF8 first, because sanitizeRemoteText documents a valid-UTF-8
// precondition and names this exact caller as the one that would not get it for
// free: every other caller's value arrived over IPC as JSON, which coerces
// invalid bytes to U+FFFD before this process sees them. An embedded file has
// no such stage, and a bare 0x9B does survive the shell validator. Without the
// conversion, sanitizeRemoteText's ContainsFunc fast path decodes that byte to
// U+FFFD, matches nothing, and returns the string with the raw byte intact.
func sanitizeHeadline(s string) string {
	return sanitizeRemoteText(strings.ToValidUTF8(s, ""))
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
