package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/config"
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
	palActNone      paletteAction = iota
	palActGoToPane                // arg = paneID
	palActSwitchTab               // arg = tabID
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
// inert on Enter. header rows are dim section titles: not selectable, skipped by
// the cursor, and hidden while a query is active. arg carries a navigation
// target id (paneID/tabID); it is empty for static commands. keywords are extra
// fuzzy targets beyond the label.
type paletteCommand struct {
	action   paletteAction
	label    string
	detail   string
	keywords []string
	enabled  bool
	header   bool
	arg      string
}

// selectable reports whether the cursor may land on this row and Enter may run
// it — everything except section headers and disabled rows.
func (c paletteCommand) selectable() bool { return !c.header && c.enabled }

// fuzzyScore reports whether query is a case-insensitive subsequence of target
// and, if so, a score (higher = better). It rewards consecutive runs, a match
// at the target start, a match right after a separator, and earlier position.
// Empty query returns (0, true) — everything passes.
//
// Matching is greedy: each query rune takes the FIRST available target position,
// not the globally best alignment. This can under-rank a target whose better
// (more consecutive) run sits after a poorer early match. Acceptable for the
// short, well-separated command labels here; revisit with a full DP scorer only
// if ranking quality ever becomes a complaint.
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

// filterPalette produces the visible rows for query. Empty query = browse mode:
// all commands in registry order, section headers included. Non-empty query =
// search mode: headers are dropped and matching commands are sorted by score
// descending with a STABLE sort so equal scores preserve registry order.
func filterPalette(query string, commands []paletteCommand) []paletteCommand {
	if query == "" {
		return append([]paletteCommand(nil), commands...)
	}
	type scored struct {
		c paletteCommand
		s int
	}
	matched := make([]scored, 0, len(commands))
	for _, c := range commands {
		if c.header {
			continue // headers are section labels, never search hits
		}
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

// firstSelectable returns the index of the first row the cursor may land on
// (skipping headers and disabled rows), or -1 if none.
func firstSelectable(cmds []paletteCommand) int {
	for i, c := range cmds {
		if c.selectable() {
			return i
		}
	}
	return -1
}

// nextSelectable returns the next selectable index from cur in direction dir
// (+1 down, -1 up), skipping headers and disabled rows WITHOUT wrapping (the
// cursor stays put at the ends). cur == -1 resolves to the first/last.
func nextSelectable(cmds []paletteCommand, cur, dir int) int {
	i := cur + dir
	for i >= 0 && i < len(cmds) {
		if cmds[i].selectable() {
			return i
		}
		i += dir
	}
	if cur >= 0 && cur < len(cmds) && cmds[cur].selectable() {
		return cur // no move available; stay
	}
	return firstSelectable(cmds)
}

// tabIndexName renders a tab's 1-based index + name, matching the tab bar
// prefix (e.g. "1:Shell").
func tabIndexName(i int, tab *TabModel) string {
	return fmt.Sprintf("%d:%s", i+1, tab.Name)
}

// buildPaletteCommands assembles the full command registry, rebuilt on every
// open so dynamic entries (tabs/panes) and per-active-pane gates/labels are
// current. Entries are grouped under dim section headers (Go to pane / Tabs /
// Pane / System), in that order — navigation first (jumping to a pane/tab is the
// most common reason to open the palette), then pane actions, then system.
// Headers are shown only while browsing (empty query) and skipped by the cursor.
func (m *Model) buildPaletteCommands() []paletteCommand {
	kb := m.cfg.Keybindings
	home, _ := os.UserHomeDir()
	var cmds []paletteCommand
	header := func(label string) { cmds = append(cmds, paletteCommand{header: true, label: label}) }

	// Per-active-pane gates + toggle labels.
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

	// --- Go to pane: navigation leads — jumping to a pane is the most common
	// reason to open the palette. One row per pane across every tab,
	// distinguished by a tab.pane index + plugin type so same-name/same-CWD
	// panes are told apart.
	header("Go to pane")
	for i, tab := range m.tabs {
		if tab == nil {
			continue
		}
		for j, p := range tab.Leaves() {
			if p == nil {
				continue
			}
			// Join only the non-empty parts with " · " so a typeless or
			// unnamed pane never renders a dangling/doubled separator. Empty
			// type falls back to "terminal" (the daemon's default plugin).
			paneType := p.Type
			if paneType == "" {
				paneType = "terminal"
			}
			parts := []string{fmt.Sprintf("%d.%d", i+1, j+1), paneType}
			if p.Name != "" {
				parts = append(parts, p.Name)
			}
			cmds = append(cmds, paletteCommand{
				action:   palActGoToPane,
				arg:      p.ID,
				enabled:  true,
				label:    strings.Join(parts, " · "),
				detail:   shortCWD(p.CWD, home),
				keywords: []string{"go to", "goto", "pane", "focus", p.Name, filepath.Base(p.CWD), paneType},
			})
		}
	}

	// --- Tabs: switch-to (navigation) first, then tab management -----------
	header("Tabs")
	for i, tab := range m.tabs {
		if tab == nil {
			continue
		}
		cmds = append(cmds, paletteCommand{
			action:   palActSwitchTab,
			arg:      tab.ID,
			enabled:  true,
			label:    "Switch to " + tabIndexName(i, tab),
			keywords: []string{"tab", "go to", "goto", "switch"},
		})
	}
	cmds = append(cmds,
		paletteCommand{action: palActNewTab, enabled: true, label: "New tab", detail: kbDisplay(kb.NewTab), keywords: []string{"tab", "create"}},
		paletteCommand{action: palActCloseTab, enabled: true, label: "Close tab…", detail: kbDisplay(kb.CloseTab), keywords: []string{"tab", "close"}},
		paletteCommand{action: palActRenameTab, enabled: true, label: "Rename tab", detail: kbDisplay(kb.RenameTab), keywords: []string{"tab", "rename"}},
		paletteCommand{action: palActCycleTabColor, enabled: true, label: "Cycle tab color", detail: kbDisplay(kb.CycleTabColor), keywords: []string{"tab", "color"}},
	)

	// --- Pane: actions on the active pane ----------------------------------
	header("Pane")
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
		paletteCommand{action: palActNewPane, enabled: true, label: "New pane…", detail: "ctrl+n", keywords: []string{"create", "plugin", "claude", "terminal"}},
		paletteCommand{action: palActRestartPane, enabled: true, label: "Restart pane…", detail: kbDisplay(kb.RestartPane), keywords: []string{"restart", "respawn"}},
		paletteCommand{action: palActClosePane, enabled: true, label: "Close pane…", detail: kbDisplay(kb.ClosePane), keywords: []string{"close", "kill"}},
	)

	// --- System ------------------------------------------------------------
	header("System")
	cmds = append(cmds,
		paletteCommand{action: palActSettings, enabled: true, label: "Settings", keywords: []string{"config", "preferences"}},
		paletteCommand{action: palActShortcuts, enabled: true, label: "Keyboard shortcuts", keywords: []string{"keys", "bindings", "help"}},
		paletteCommand{action: palActPlugins, enabled: true, label: "Plugins", keywords: []string{"plugin", "toml"}},
		paletteCommand{action: palActMemory, enabled: true, label: "Memory report", keywords: []string{"memory", "mem", "ram"}},
		paletteCommand{action: palActAbout, enabled: true, label: "About", detail: "f1", keywords: []string{"about", "version"}},
		paletteCommand{action: palActClientLog, enabled: true, label: "View client log", keywords: []string{"log", "client"}},
		paletteCommand{action: palActDaemonLog, enabled: true, label: "View daemon log", keywords: []string{"log", "daemon"}},
		paletteCommand{action: palActMCPLog, enabled: true, label: "View MCP logs", keywords: []string{"log", "mcp"}},
		paletteCommand{action: palActRedraw, enabled: true, label: "Force redraw", detail: kbDisplay(kb.Redraw), keywords: []string{"redraw", "repaint", "refresh"}},
	)

	return cmds
}

// shortCWD renders a compact, tail-preserving directory hint: home is collapsed
// to ~ and an over-long path keeps its last cells (the meaningful basename) with
// a leading ellipsis.
func shortCWD(cwd, home string) string {
	if cwd == "" {
		return ""
	}
	if home != "" && strings.HasPrefix(cwd, home) {
		cwd = "~" + cwd[len(home):]
	}
	const maxCWD = 22
	if lipgloss.Width(cwd) > maxCWD {
		cwd = "…" + lastCellsToWidth(cwd, maxCWD-1)
	}
	return cwd
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
	// Content capacity is boxW minus the rounded border (2) AND the dialogBorder
	// Padding(1,2) (4) — lipgloss draws the border INSIDE Width, so a row of
	// boxW-4 would soft-wrap its trailing (right-aligned) shortcut onto the next
	// line. boxW-6 is the true usable width.
	inner := boxW - 6
	if inner < 1 {
		inner = 1
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

	// Query row: "> " (2 cells) + query + caret (1 cell). Show the TAIL of a long
	// query so the caret stays visible AND the row never exceeds inner — an
	// over-wide query row wraps the box border on a narrow terminal.
	qAvail := inner - 3
	if qAvail < 1 {
		qAvail = 1
	}
	b.WriteString(dialogTitle.Render("> "))
	b.WriteString(dialogEditStyle.Render(lastCellsToWidth(m.palette.query, qAvail) + "│"))
	b.WriteByte('\n')
	b.WriteByte('\n')

	// subtle renders an informational line bounded to inner so it never wraps the
	// box border on a narrow terminal (greptile P1 applies to these lines too).
	subtle := func(s string) string { return dialogSubtle.Render(truncateToWidth(s, inner)) }
	const hint = "↑↓ nav · Enter run · Esc close"

	filtered := m.palette.filtered
	if len(filtered) == 0 {
		b.WriteString(subtle("  No matching commands"))
		b.WriteByte('\n')
		b.WriteByte('\n')
		b.WriteString(subtle(hint))
		return b.String()
	}

	start, end := paletteWindow(m.palette.cursor, len(filtered))
	if start > 0 {
		b.WriteString(subtle(fmt.Sprintf("  ↑ %d more", start)))
		b.WriteByte('\n')
	}
	for i := start; i < end; i++ {
		if filtered[i].header {
			b.WriteString(renderPaletteHeader(filtered[i].label, inner))
		} else {
			b.WriteString(renderPaletteRow(filtered[i], i == m.palette.cursor, inner))
		}
		b.WriteByte('\n')
	}
	if end < len(filtered) {
		b.WriteString(subtle(fmt.Sprintf("  ↓ %d more", len(filtered)-end)))
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	b.WriteString(subtle(hint))
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

// renderPaletteHeader draws a dim, upper-cased section title bounded to inner.
func renderPaletteHeader(label string, inner int) string {
	return ctxMenuDisabledStyle.Render(truncateToWidth(strings.ToUpper(label), inner))
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
	if contentW < 1 {
		contentW = 1
	}
	// Bound the detail first so a long shortcut cannot overflow a narrow row;
	// leave at least 2 cells for a label + gap. Then bound the label against the
	// remaining budget. Both are cell-aware so wide glyphs never wrap the box.
	detail := c.detail
	if maxDetail := contentW - 2; maxDetail >= 0 && lipgloss.Width(detail) > maxDetail {
		detail = truncateToWidth(detail, maxDetail)
	}
	detailW := lipgloss.Width(detail)
	labelMax := contentW - detailW - 1 // ≥1 space gap
	if labelMax < 1 {
		labelMax = 1
	}
	label := c.label
	if lipgloss.Width(label) > labelMax {
		label = truncateToWidth(label, labelMax)
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

// openCommandPalette builds the command registry and opens the palette. No-op
// in notes mode: its actions restructure the layout under the notes editor.
// This explicit guard mirrors openQuickActionsMenu — notesKeyExempt only
// governs the editor-focused path, so absence from it is NOT enough to keep the
// palette out of the pane-focused notes path.
func (m Model) openCommandPalette() (tea.Model, tea.Cmd) {
	if m.notesMode {
		return m, nil
	}
	m.palette = paletteState{}
	m.palette.commands = m.buildPaletteCommands()
	m.palette.filtered = filterPalette("", m.palette.commands)
	m.palette.cursor = firstSelectable(m.palette.filtered)
	m.dialog = dialogCommandPalette
	return m, tea.ClearScreen
}

// closeCommandPalette closes the palette and clears its state. m.dialog is the
// open/closed authority.
func (m *Model) closeCommandPalette() {
	m.dialog = dialogNone
	m.palette = paletteState{}
}

// refilterPalette recomputes the visible results for the current query and
// resets the cursor to the top.
func (m *Model) refilterPalette() {
	m.palette.filtered = filterPalette(m.palette.query, m.palette.commands)
	m.palette.cursor = firstSelectable(m.palette.filtered)
}

// handleCommandPaletteKey routes keys while the palette is open. Value receiver,
// like the sibling handleXKey dialog handlers.
func (m Model) handleCommandPaletteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch {
	case key == "esc":
		m.closeCommandPalette()
		return m, nil
	case key == "enter":
		if c := m.palette.cursor; c >= 0 && c < len(m.palette.filtered) && m.palette.filtered[c].selectable() {
			return m.executePaletteCommand(m.palette.filtered[c])
		}
		return m, nil
	case key == "up" || key == "ctrl+p":
		m.palette.cursor = nextSelectable(m.palette.filtered, m.palette.cursor, -1)
		return m, nil
	case key == "down" || key == "ctrl+n":
		m.palette.cursor = nextSelectable(m.palette.filtered, m.palette.cursor, +1)
		return m, nil
	case key == "backspace":
		if q := []rune(m.palette.query); len(q) > 0 {
			m.palette.query = string(q[:len(q)-1])
			m.refilterPalette()
		}
		return m, nil
	case key == "space":
		m.palette.query += " "
		m.refilterPalette()
		return m, nil
	case msg.Text != "" && isPrintableText(msg.Text):
		// Only printable text extends the query; a key we do not handle above
		// (e.g. tab) may carry a control character in msg.Text — never inject it.
		m.palette.query += msg.Text
		m.refilterPalette()
		return m, nil
	}
	return m, nil
}

// sanitizePaletteQuery keeps only printable runes from s — the paste-path
// counterpart to isPrintableText, used to fold clipboard text into the query
// without letting newlines/tabs/control bytes through.
func sanitizePaletteQuery(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
}

// isPrintableText reports whether every rune in s is printable — the guard that
// keeps control characters (tab, etc.) out of the palette query.
func isPrintableText(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// truncateToWidth shortens s to at most w display cells (lipgloss.Width),
// appending "…". Cell-aware, NOT rune-count: a CJK/emoji label — a pane or tab
// name is user-settable — would otherwise render up to 2× its budget and wrap
// the palette box border. Returns "" for w <= 0.
func truncateToWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	target := w - 1 // reserve one cell for the ellipsis
	var b strings.Builder
	width := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if width+rw > target {
			break
		}
		b.WriteRune(r)
		width += rw
	}
	return b.String() + "…"
}

// lastCellsToWidth returns the trailing substring of s whose display width is at
// most w cells — used to show the tail (most recent input) of a long palette
// query so the caret stays visible without overflowing the box.
func lastCellsToWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	runes := []rune(s)
	width := 0
	i := len(runes)
	for i > 0 {
		rw := lipgloss.Width(string(runes[i-1]))
		if width+rw > w {
			break
		}
		width += rw
		i--
	}
	return string(runes[i:])
}

// executePaletteCommand closes the palette and dispatches into the SAME handler
// the keybinding case calls. Navigation commands change the active tab/pane;
// every other command acts on the active pane, exactly like pressing its key.
func (m Model) executePaletteCommand(c paletteCommand) (tea.Model, tea.Cmd) {
	m.closeCommandPalette()
	if !c.selectable() {
		return m, nil
	}
	switch c.action {
	// --- Navigation --------------------------------------------------------
	case palActGoToPane:
		pane, idx := m.findPaneAndTab(c.arg)
		if pane == nil || idx < 0 || idx >= len(m.tabs) {
			return m, nil
		}
		// Clear the previously-active tab's active-pane flag BEFORE switchTab
		// moves m.activeTab (ordering is load-bearing for the border repaint).
		if cur := m.activeTabModel(); cur != nil {
			if old := cur.ActivePaneModel(); old != nil {
				old.Active = false
			}
		}
		m.tabs[idx].ActivePane = c.arg
		pane.Active = true
		return m, m.switchTab(idx)
	case palActSwitchTab:
		for i, tab := range m.tabs {
			if tab != nil && tab.ID == c.arg {
				return m, m.switchTab(i)
			}
		}
		return m, nil

	// --- Layout / pane (active pane) ---------------------------------------
	case palActSplitH:
		if tab := m.activeTabModel(); tab != nil && tab.FocusMode() {
			tab.ExitFocus()
		}
		return m, m.splitPane(SplitHorizontal)
	case palActSplitV:
		if tab := m.activeTabModel(); tab != nil && tab.FocusMode() {
			tab.ExitFocus()
		}
		return m, m.splitPane(SplitVertical)
	case palActFocus:
		return m.toggleFocusForActiveTab()
	case palActNotes:
		return m.toggleNotesMode()
	case palActRenamePane:
		return m.beginPaneRename()
	case palActMute:
		return m, m.toggleActivePaneMute()
	case palActEager:
		return m, m.toggleActivePaneEager()
	case palActHistory:
		return m.openHistoryForActivePane()
	case palActLazygit:
		return m, m.handleToggleLazygit()
	case palActRestartPane:
		return m.openRestartPaneConfirm()
	case palActClosePane:
		return m.openClosePaneConfirm()

	// --- Tab ---------------------------------------------------------------
	case palActNewTab:
		return m, m.createTab()
	case palActCloseTab:
		return m.openCloseTabConfirm()
	case palActRenameTab:
		return m.beginTabRename()
	case palActCycleTabColor:
		return m, m.cycleTabColor()

	// --- Create ------------------------------------------------------------
	case palActNewPane:
		return m.openCreatePaneDialog()

	// --- System ------------------------------------------------------------
	case palActSettings:
		m.dialog = dialogSettings
		m.dialogCursor = 0
		return m, tea.ClearScreen
	case palActShortcuts:
		m.dialog = dialogShortcuts
		m.dialogCursor = 0
		return m, tea.ClearScreen
	case palActPlugins:
		m.dialog = dialogPlugins
		m.dialogCursor = 0
		return m, tea.ClearScreen
	case palActMemory:
		m = m.openMemoryDialog()
		return m, m.refreshMemory()
	case palActAbout:
		m.dialog = dialogAbout
		m.dialogCursor = 0
		return m, tea.ClearScreen
	case palActClientLog:
		return m.openLogViewer("Client log", filepath.Join(config.QuilDir(), "quil.log"))
	case palActDaemonLog:
		return m.openLogViewer("Daemon log", filepath.Join(config.QuilDir(), "quild.log"))
	case palActMCPLog:
		return m.openMCPLogsViewer()
	case palActRedraw:
		return m.forceRedraw()
	}
	return m, nil
}
