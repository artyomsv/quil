package tui

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
)

const paletteVisibleRows = 12 // result rows shown before the list scrolls
const paletteWidth = 72       // outer dialog width; renderDialog clamps to m.width-2

// paletteState holds the command-palette query buffer + result cursor. There is
// NO `open` field — m.dialog == dialogCommandPalette is the sole open/closed
// authority (mirrors ctxMenu needing no such flag). Zero value = empty.
type paletteState struct {
	query    string
	cursor   int
	commands []paletteCommand // full registry, rebuilt on open
	filtered []paletteCommand // commands matching query, best score first
}

// paletteAction identifies one command palette entry. Dispatch in
// executePaletteCommand routes each action into the SAME handler the keybinding
// case calls — a third dispatcher (alongside the key switch and the context
// menu), never a second implementation.
type paletteAction int

const (
	palActNone paletteAction = iota
	palActGoToPane  // arg = paneID
	palActSwitchTab // arg = tabID
	palActSplitH
	palActSplitV
	palActFocus
	palActNotes
	palActRenamePane
	palActMute
	palActEager
	palActHistory
	palActLazygit
	palActRestartPane
	palActClosePane
	palActNewTab
	palActCloseTab
	palActRenameTab
	palActCycleTabColor
	palActNewPane
	palActSettings
	palActShortcuts
	palActPlugins
	palActMemory
	palActAbout
	palActClientLog
	palActDaemonLog
	palActMCPLog
	palActRedraw
)

// paletteCommand is one row of the palette. Disabled rows render greyed and are
// inert on Enter. arg carries a navigation target id (paneID/tabID); it is empty
// for static commands. keywords are extra fuzzy targets beyond the label.
type paletteCommand struct {
	action   paletteAction
	label    string
	detail   string
	keywords []string
	enabled  bool
	arg      string
}

// fuzzyScore reports whether query is a case-insensitive subsequence of target
// and, if so, a score (higher = better). It rewards consecutive runs, a match
// at the target start, a match right after a separator, and earlier position.
// Empty query returns (0, true) — everything passes.
func fuzzyScore(query, target string) (int, bool) {
	if query == "" {
		return 0, true
	}
	q := []rune(strings.ToLower(query))
	t := []rune(strings.ToLower(target))
	score, qi, prevMatch := 0, 0, -2
	for ti := 0; ti < len(t) && qi < len(q); ti++ {
		if t[ti] != q[qi] {
			continue
		}
		gain := 1
		if ti == 0 {
			gain += 5 // start of target
		} else if isSeparator(t[ti-1]) {
			gain += 3 // word boundary
		}
		if ti == prevMatch+1 {
			gain += 4 // consecutive run
		}
		gain -= ti / 8 // mild earlier-is-better bias, bounded below at 1
		if gain < 1 {
			gain = 1
		}
		score += gain
		prevMatch = ti
		qi++
	}
	if qi < len(q) {
		return 0, false // not a full subsequence
	}
	return score, true
}

func isSeparator(r rune) bool {
	switch r {
	case ' ', ':', '-', '_', '.', '/':
		return true
	}
	return unicode.IsSpace(r)
}

// commandScore returns the best fuzzyScore of query against the command's label
// and each keyword; matched iff any target matches.
func commandScore(query string, c paletteCommand) (int, bool) {
	best, matched := fuzzyScore(query, c.label)
	for _, kw := range c.keywords {
		if s, ok := fuzzyScore(query, kw); ok && (!matched || s > best) {
			best, matched = s, true
		}
	}
	return best, matched
}

// filterPalette keeps commands matching query, sorted by score descending with
// a STABLE sort so equal scores preserve registry order (the navigation →
// system grouping survives). Empty query returns all commands in registry order
// (a fresh slice — never the caller's backing array).
func filterPalette(query string, commands []paletteCommand) []paletteCommand {
	type scored struct {
		c paletteCommand
		s int
	}
	matched := make([]scored, 0, len(commands))
	for _, c := range commands {
		if s, ok := commandScore(query, c); ok {
			matched = append(matched, scored{c, s})
		}
	}
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].s > matched[j].s })
	out := make([]paletteCommand, len(matched))
	for i, m := range matched {
		out[i] = m.c
	}
	return out
}

// tabIndexName renders a tab's 1-based index + name, matching the tab bar
// prefix (e.g. "1:Shell").
func tabIndexName(i int, tab *TabModel) string {
	return fmt.Sprintf("%d:%s", i+1, tab.Name)
}

// buildPaletteCommands assembles the full command registry, rebuilt on every
// open so dynamic entries (tabs/panes) and per-active-pane gates/labels are
// current. Assembly order defines the empty-query display order.
func (m *Model) buildPaletteCommands() []paletteCommand {
	kb := m.cfg.Keybindings
	var cmds []paletteCommand

	// --- Navigation: one Switch-to per tab, one Go-to per pane -------------
	for i, tab := range m.tabs {
		if tab == nil {
			continue
		}
		cmds = append(cmds, paletteCommand{
			action:   palActSwitchTab,
			arg:      tab.ID,
			enabled:  true,
			label:    "Switch to tab: " + tabIndexName(i, tab),
			detail:   "tab",
			keywords: []string{"tab", "goto"},
		})
		for _, p := range tab.Leaves() {
			if p == nil {
				continue
			}
			cmds = append(cmds, paletteCommand{
				action:   palActGoToPane,
				arg:      p.ID,
				enabled:  true,
				label:    "Go to: " + tabIndexName(i, tab) + " / " + paneDisplayName(p),
				detail:   p.Type,
				keywords: []string{"pane", "goto", "focus"},
			})
		}
	}

	// --- Per-active-pane gates + toggle labels -----------------------------
	historyOK, lazygitOK := false, false
	muteLabel, eagerLabel := "Mute notifications", "Enable eager restore"
	if tab := m.activeTabModel(); tab != nil {
		if p := tab.ActivePaneModel(); p != nil {
			if p.Muted {
				muteLabel = "Unmute notifications"
			}
			if p.Eager {
				eagerLabel = "Disable eager restore"
			}
			if m.pluginRegistry != nil {
				if pl := m.pluginRegistry.Get(p.Type); pl != nil {
					historyOK = pl.Command.RecordHistory
				}
			}
		}
	}
	if m.pluginRegistry != nil {
		if pl := m.pluginRegistry.Get("lazygit"); pl != nil {
			lazygitOK = pl.Available
		}
	}

	// --- Pane actions (act on the active pane) -----------------------------
	cmds = append(cmds,
		paletteCommand{action: palActSplitH, enabled: true, label: "Split horizontal", detail: kbDisplay(kb.SplitHorizontal), keywords: []string{"hsplit", "horizontal"}},
		paletteCommand{action: palActSplitV, enabled: true, label: "Split vertical", detail: kbDisplay(kb.SplitVertical), keywords: []string{"vsplit", "vertical"}},
		paletteCommand{action: palActFocus, enabled: true, label: "Toggle focus mode", detail: kbDisplay(kb.FocusPane), keywords: []string{"fullscreen", "zoom", "maximize"}},
		paletteCommand{action: palActNotes, enabled: true, label: "Toggle notes", detail: kbDisplay(kb.NotesToggle), keywords: []string{"note", "editor"}},
		paletteCommand{action: palActRenamePane, enabled: true, label: "Rename pane", detail: kbDisplay(kb.RenamePane)},
		paletteCommand{action: palActMute, enabled: true, label: muteLabel, detail: kbDisplay(kb.MutePane), keywords: []string{"mute", "silence", "notification"}},
		paletteCommand{action: palActEager, enabled: true, label: eagerLabel, detail: kbDisplay(kb.ToggleEager), keywords: []string{"eager", "restore", "restart"}},
		paletteCommand{action: palActHistory, enabled: historyOK, label: "Input history", detail: kbDisplay(kb.CommandHistory), keywords: []string{"history", "prompts"}},
		paletteCommand{action: palActLazygit, enabled: lazygitOK, label: "Open lazygit", detail: kbDisplay(kb.ToggleLazygit), keywords: []string{"git", "lazygit"}},
		paletteCommand{action: palActRestartPane, enabled: true, label: "Restart pane…", detail: kbDisplay(kb.RestartPane), keywords: []string{"restart", "respawn"}},
		paletteCommand{action: palActClosePane, enabled: true, label: "Close pane…", detail: kbDisplay(kb.ClosePane), keywords: []string{"close", "kill"}},
	)

	// --- Tab actions -------------------------------------------------------
	cmds = append(cmds,
		paletteCommand{action: palActNewTab, enabled: true, label: "New tab", detail: kbDisplay(kb.NewTab), keywords: []string{"tab", "create"}},
		paletteCommand{action: palActCloseTab, enabled: true, label: "Close tab…", detail: kbDisplay(kb.CloseTab), keywords: []string{"tab", "close"}},
		paletteCommand{action: palActRenameTab, enabled: true, label: "Rename tab", detail: kbDisplay(kb.RenameTab), keywords: []string{"tab", "rename"}},
		paletteCommand{action: palActCycleTabColor, enabled: true, label: "Cycle tab color", detail: kbDisplay(kb.CycleTabColor), keywords: []string{"tab", "color"}},
	)

	// --- Create ------------------------------------------------------------
	cmds = append(cmds,
		paletteCommand{action: palActNewPane, enabled: true, label: "New pane…", detail: "ctrl+n", keywords: []string{"create", "pane", "plugin", "claude", "terminal"}},
	)

	// --- System ------------------------------------------------------------
	cmds = append(cmds,
		paletteCommand{action: palActSettings, enabled: true, label: "Settings", detail: "", keywords: []string{"config", "preferences"}},
		paletteCommand{action: palActShortcuts, enabled: true, label: "Keyboard shortcuts", detail: "", keywords: []string{"keys", "bindings", "help"}},
		paletteCommand{action: palActPlugins, enabled: true, label: "Plugins", detail: "", keywords: []string{"plugin", "toml"}},
		paletteCommand{action: palActMemory, enabled: true, label: "Memory report", detail: "", keywords: []string{"memory", "mem", "ram"}},
		paletteCommand{action: palActAbout, enabled: true, label: "About", detail: "f1", keywords: []string{"about", "version"}},
		paletteCommand{action: palActClientLog, enabled: true, label: "View client log", detail: "", keywords: []string{"log", "client"}},
		paletteCommand{action: palActDaemonLog, enabled: true, label: "View daemon log", detail: "", keywords: []string{"log", "daemon"}},
		paletteCommand{action: palActMCPLog, enabled: true, label: "View MCP logs", detail: "", keywords: []string{"log", "mcp"}},
		paletteCommand{action: palActRedraw, enabled: true, label: "Force redraw", detail: kbDisplay(kb.Redraw), keywords: []string{"redraw", "repaint", "refresh"}},
	)

	return cmds
}

// paletteInnerWidth is the usable content width inside the dialog border for the
// current terminal size (paletteWidth clamped to the terminal, minus the
// dialogBorder Padding(1,2) → 4 cells). Kept in lockstep with renderDialog's
// width clamp so the box and its content agree.
func (m Model) paletteInnerWidth() int {
	boxW := paletteWidth
	if m.width > 2 && boxW > m.width-2 {
		boxW = m.width - 2
	}
	inner := boxW - 4
	if inner < 20 {
		inner = 20
	}
	return inner
}

// renderCommandPalette returns the palette box CONTENT (renderDialog wraps it in
// dialogBorder and centers it). Layout: query row, blank, up to
// paletteVisibleRows result rows (a scroll window keeps the cursor visible),
// blank, footer hint.
func renderCommandPalette(m Model) string {
	inner := m.paletteInnerWidth()
	var b strings.Builder

	// Query row: "> " + edit-styled query with a caret (matches dialogEditStyle).
	b.WriteString(dialogTitle.Render("> "))
	b.WriteString(dialogEditStyle.Render(m.palette.query + "│"))
	b.WriteByte('\n')
	b.WriteByte('\n')

	filtered := m.palette.filtered
	if len(filtered) == 0 {
		b.WriteString(dialogSubtle.Render("  No matching commands"))
		b.WriteByte('\n')
		b.WriteByte('\n')
		b.WriteString(dialogSubtle.Render("↑↓ nav · Enter run · Esc close"))
		return b.String()
	}

	start, end := paletteWindow(m.palette.cursor, len(filtered))
	if start > 0 {
		b.WriteString(dialogSubtle.Render(fmt.Sprintf("  ↑ %d more", start)))
		b.WriteByte('\n')
	}
	for i := start; i < end; i++ {
		b.WriteString(renderPaletteRow(filtered[i], i == m.palette.cursor, inner))
		b.WriteByte('\n')
	}
	if end < len(filtered) {
		b.WriteString(dialogSubtle.Render(fmt.Sprintf("  ↓ %d more", len(filtered)-end)))
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("↑↓ nav · Enter run · Esc close"))
	return b.String()
}

// paletteWindow returns the [start, end) slice of a length-n result list to
// render, sized to paletteVisibleRows and shifted to keep cursor visible.
func paletteWindow(cursor, n int) (int, int) {
	if n <= paletteVisibleRows {
		return 0, n
	}
	start := 0
	if cursor >= paletteVisibleRows {
		start = cursor - paletteVisibleRows + 1
	}
	if max := n - paletteVisibleRows; start > max {
		start = max
	}
	if start < 0 {
		start = 0
	}
	return start, start + paletteVisibleRows
}

// renderPaletteRow lays out one result row: "› "/"  " cursor prefix, label left,
// detail (shortcut) right-aligned, padded to inner width. Disabled rows render
// greyed; the cursor row is bold.
func renderPaletteRow(c paletteCommand, cursor bool, inner int) string {
	prefix := "  "
	if cursor {
		prefix = "› "
	}
	contentW := inner - 2 // prefix takes 2 cells
	detail := c.detail
	detailW := lipgloss.Width(detail)
	labelMax := contentW - detailW - 1 // ≥1 space gap
	if labelMax < 1 {
		labelMax = 1
	}
	label := c.label
	if lipgloss.Width(label) > labelMax {
		label = truncateHistory(label, labelMax) // rune-aware, appends …
	}
	gap := contentW - lipgloss.Width(label) - detailW
	if gap < 1 {
		gap = 1
	}

	labelStyle := dialogNormal
	switch {
	case !c.enabled:
		labelStyle = ctxMenuDisabledStyle
	case cursor:
		labelStyle = dialogSelected
	}
	row := prefix + labelStyle.Render(label) + strings.Repeat(" ", gap)
	if detail != "" {
		row += dialogSubtle.Render(detail)
	}
	return row
}

