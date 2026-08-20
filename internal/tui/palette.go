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
	"github.com/rivo/uniseg"

	"github.com/artyomsv/quil/internal/config"
)

const paletteVisibleLines = 12 // rendered lines shown before the list scrolls (a hit row is 2 lines)
const paletteWidth = 72        // outer dialog width; renderDialog clamps to m.width-2

// paletteState holds the command-palette query buffer + result cursor. There is
// NO `open` field — m.dialog == dialogCommandPalette is the sole open/closed
// authority (mirrors ctxMenu needing no such flag). Zero value = empty.
type paletteState struct {
	query    string
	cursor   int
	commands []paletteCommand // full registry, rebuilt on open
	filtered []paletteCommand // commands matching query, best score first

	// Content search runs ALONGSIDE the command filter whenever the query is
	// non-empty — there is no separate mode or sigil. Matching panes render as a
	// "Found in panes" section below the filtered commands, each a palActGoToPane
	// row so Enter/navigation reuse the command machinery. paletteDisplay()
	// assembles filtered + this section into the single list the cursor walks.
	contentHits []paletteCommand // resolved pane matches (palActGoToPane rows), daemon-sorted
	searching   bool             // a content request is in flight, no fresh response yet
	timedOut    bool             // the in-flight request never answered (see paletteSearchTimeout)
	truncated   bool             // some pane hit the per-pane match cap
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
	palActHunk
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
	palActProcesses
	palActAbout
	palActClientLog
	palActDaemonLog
	palActMCPLog
	palActRedraw
	// Projects. palActSwitchProject carries a project ID rather than an index
	// for the reason toggleLastProject documents: an index resolved later can
	// name a different project, or a destroyed one resolves to 0 and switches
	// somewhere the user never asked for.
	palActSwitchProject // arg = projectID
	palActNewProject
	palActRenameProject   // arg = projectID
	palActRemoveProject   // arg = projectID (destroy if local, disconnect if remote)
	palActProjectSidebar  // toggle the reserved column
	palActAttentionQueue  // jump to the agent blocked longest
	palActPrevProject     // bounce to the previous project
)

// paletteCommand is one row of the palette. Disabled rows render greyed and are
// inert on Enter. header rows are dim section titles and info rows are dim status
// lines ("Searching…", "No matches…"): neither is selectable, both are skipped by
// the cursor. arg carries a navigation target id (paneID/tabID); it is empty for
// static commands. keywords are extra fuzzy targets beyond the label. excerpt, when
// set on a content-hit row, renders as a dim preview line beneath the row.
type paletteCommand struct {
	action   paletteAction
	label    string
	detail   string
	keywords []string
	enabled  bool
	header   bool
	info     bool
	arg      string
	excerpt  string
}

// selectable reports whether the cursor may land on this row and Enter may run
// it — everything except section headers, status rows, and disabled rows.
func (c paletteCommand) selectable() bool { return !c.header && !c.info && c.enabled }

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

// formatPaneNav renders the shared palette navigation label for a pane:
// "i.j · type[· name][· project]" with 1-based tab/pane indices. Empty type
// falls back to "terminal" (the daemon default); an unnamed pane omits the
// trailing segment so there is never a dangling separator. project is
// appended only when non-empty: callers pass it for a pane outside the
// active project so two tabs both named "Shell" in different projects stay
// distinguishable, and omit it for the active project's own panes (it would
// only repeat what's already on screen).
// sidebarToggleLabel names what the row will DO, not what the state is. The
// palette's other toggles (mute, eager) read the same way — a row labelled
// with the current state leaves the user working out which direction Enter
// moves it.
func sidebarToggleLabel(open bool) string {
	if open {
		return "Hide project sidebar"
	}
	return "Show project sidebar"
}

func formatPaneNav(tabIdx, paneIdx int, p *PaneModel, project string) string {
	paneType := p.Type
	if paneType == "" {
		paneType = "terminal"
	}
	parts := []string{fmt.Sprintf("%d.%d", tabIdx+1, paneIdx+1), paneType}
	// Both names are a daemon's answer, and a daemon may sit on a host the user
	// does not control. Sanitizing here rather than at the two callers is what
	// makes the palette and the content-search hit list agree: they share this
	// one implementation precisely so a label can never differ between them.
	// truncateToWidth downstream cannot stand in for this — it measures with
	// lipgloss.Width, and an escape sequence is zero cells wide, so it is
	// neither counted nor cut.
	if p.Name != "" {
		parts = append(parts, sanitizeRemoteText(p.Name))
	}
	if project != "" {
		parts = append(parts, sanitizeRemoteText(project))
	}
	return strings.Join(parts, " · ")
}

// buildPaletteCommands assembles the full command registry, rebuilt on every
// open so dynamic entries (tabs/panes) and per-active-pane gates/labels are
// current. Entries are grouped under dim section headers (Go to pane / Tabs /
// Pane / System), in that order — navigation first (jumping to a pane/tab is the
// most common reason to open the palette), then pane actions, then system.
// Headers are shown only while browsing (empty query) and skipped by the cursor.
func (m *Model) buildPaletteCommands() []paletteCommand {
	home, _ := os.UserHomeDir()
	var cmds []paletteCommand
	header := func(label string) { cmds = append(cmds, paletteCommand{header: true, label: label}) }

	// Per-active-pane gates + toggle labels.
	historyOK, lazygitOK, hunkOK := false, false, false
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
	// Each overlay tool is gated on its OWN binary: they share a tab's overlay
	// slot, not an installation.
	if m.pluginRegistry != nil {
		if pl := m.pluginRegistry.Get(overlayPluginLazygit); pl != nil {
			lazygitOK = pl.Available
		}
		if pl := m.pluginRegistry.Get(overlayPluginHunk); pl != nil {
			hunkOK = pl.Available
		}
	}

	// --- Go to pane: navigation leads — jumping to a pane is the most common
	// reason to open the palette. One row per pane across EVERY project (not
	// just the active one — an agent stuck in a background project still
	// needs to be reachable), distinguished by a tab.pane index + plugin
	// type so same-name/same-CWD panes are told apart, plus the project name
	// for a pane outside the active project.
	//
	// This loop, paneNavLabel (palette_search.go), and findPaneAndTab
	// (workstate.go) must all resolve the SAME (project, tabIdx, paneIdx)
	// triple for a given pane — a label built by one and an action resolved
	// by another would name the wrong tab — so all three walk m.projects
	// then a project's own tabs/panes with the same per-project indices.
	header("Go to pane")
	for _, proj := range m.projects {
		projName := ""
		if proj != m.cur() {
			projName = proj.Name
		}
		for i, tab := range proj.tabs {
			if tab == nil {
				continue
			}
			for j, p := range tab.Leaves() {
				if p == nil {
					continue
				}
				// Label format is shared with content-search hit resolution
				// (paneNavLabel) via formatPaneNav — one implementation.
				paneType := p.Type
				if paneType == "" {
					paneType = "terminal"
				}
				cmds = append(cmds, paletteCommand{
					action:   palActGoToPane,
					arg:      p.ID,
					enabled:  true,
					label:    formatPaneNav(i, j, p, projName),
					detail:   shortCWD(p.CWD, home),
					keywords: []string{"go to", "goto", "pane", "focus", p.Name, filepath.Base(p.CWD), paneType, proj.Name},
				})
			}
		}
	}

	// --- Tabs: switch-to (navigation) first, then tab management -----------
	header("Tabs")
	for i, tab := range m.curTabs() {
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
	// Greyed while there IS an active project and it is unreachable: the create
	// would be accepted here and dropped by Router.Send on its way to a daemon
	// with no connection, which reads as a broken keybinding. A NIL active
	// project (before the first workspace_state broadcast) is NOT the same
	// thing — the send still resolves through Router.Send's sole-conn startup
	// fallback, so it must stay enabled. Mirrors handleNewTab's own refusal in
	// model.go; this row is the greyed-out form of the same rule.
	newTabEnabled := true
	if p := m.cur(); p != nil {
		// onlyOfflineProjects mirrors handleNewTab's guard in model.go: every
		// row seeded before the first broadcast stands in for a destination
		// that is not the one the send actually resolves to.
		newTabEnabled = m.projectActionable(p) || m.onlyOfflineProjects()
	}
	cmds = append(cmds,
		paletteCommand{
			action:   palActNewTab,
			enabled:  newTabEnabled,
			label:    "New tab",
			detail:   m.keymap.Display("tab.new"),
			keywords: []string{"tab", "create"},
		},
		paletteCommand{action: palActCloseTab, enabled: true, label: "Close tab…", detail: m.keymap.Display("tab.close"), keywords: []string{"tab", "close"}},
		paletteCommand{action: palActRenameTab, enabled: true, label: "Rename tab", detail: m.keymap.Display("tab.rename"), keywords: []string{"tab", "rename"}},
		paletteCommand{action: palActCycleTabColor, enabled: true, label: "Cycle tab color", detail: m.keymap.Display("tab.cycle_color"), keywords: []string{"tab", "color"}},
	)

	// --- Projects ----------------------------------------------------------
	//
	// Switch-to rows first, mirroring the Tabs section: jumping somewhere is
	// the most common reason to open the palette, and with several daemons in
	// one window the project list is how a user reaches another machine at all.
	header("Projects")
	for _, proj := range m.projects {
		if proj == nil {
			continue
		}
		if proj == m.cur() {
			continue // already here; the row would be a no-op
		}
		// displayName is "Name" locally and "Name@dest" for a remote, so a
		// query narrows on either half — the same string the picker matches.
		cmds = append(cmds, paletteCommand{
			action:   palActSwitchProject,
			arg:      proj.ID,
			enabled:  true,
			label:    "Switch to " + sanitizeRemoteText(proj.displayName()),
			keywords: []string{"project", "switch", "go to", "goto", proj.Name, proj.Dest},
		})
	}
	active := m.cur()
	remote := active != nil && active.Dest != ""
	// Destroy and Disconnect are the same slot, never both — the same choice
	// the sidebar's context menu and the keybinding make. On a remote project
	// Destroy cannot do what the user means: the daemon just bootstraps a
	// replacement, so what they want is to stop showing that machine.
	removeLabel := "Destroy project…"
	if remote {
		removeLabel = "Disconnect host…"
	}
	removeArg := ""
	if active != nil {
		removeArg = active.ID
	}
	cmds = append(cmds,
		paletteCommand{action: palActNewProject, enabled: true, label: "New project", detail: m.keymap.Display("project.new"), keywords: []string{"project", "create", "add", "workspace"}},
		paletteCommand{
			action: palActRenameProject, arg: removeArg,
			// Greyed on a daemon that cannot hold a project, or one this
			// client cannot currently reach, for the same reason the context
			// menu greys it: the message is accepted and silently does
			// nothing (or is silently dropped by the router), which reads as
			// a broken dialog either way.
			enabled:  m.projectActionable(active),
			label:    "Rename project",
			keywords: []string{"project", "rename"},
		},
		paletteCommand{
			action: palActRemoveProject, arg: removeArg,
			enabled:  active != nil && (remote || m.projectActionable(active)),
			label:    removeLabel,
			detail:   m.keymap.Display("project.destroy"),
			keywords: []string{"project", "destroy", "delete", "remove", "disconnect", "host"},
		},
		paletteCommand{
			action: palActPrevProject,
			// prevProject is only written by switchProject, so on a fresh
			// launch there is genuinely nowhere to bounce back to.
			enabled:  m.prevProject != "" && m.projectByID(m.prevProject) != nil,
			label:    "Previous project",
			detail:   m.keymap.Display("project.toggle"),
			keywords: []string{"project", "back", "bounce", "last", "previous"},
		},
		paletteCommand{
			action:   palActAttentionQueue,
			enabled:  len(m.blockedPanes()) > 0,
			label:    "Go to the agent waiting longest",
			detail:   m.keymap.Display("project.attention_queue"),
			keywords: []string{"attention", "blocked", "waiting", "permission", "agent", "project"},
		},
		paletteCommand{
			action: palActProjectSidebar,
			// The toggle refuses below minWidthForSidebar rather than flipping
			// invisibly, so the row says so up front instead of flashing.
			enabled:  m.width >= minWidthForSidebar,
			label:    sidebarToggleLabel(m.sidebarOpen),
			detail:   m.keymap.Display("sidebar.toggle"),
			keywords: []string{"sidebar", "project", "panel", "column"},
		},
	)

	// --- Pane: actions on the active pane ----------------------------------
	header("Pane")
	cmds = append(cmds,
		paletteCommand{action: palActSplitH, enabled: true, label: "Split horizontal", detail: m.keymap.Display("pane.split_h"), keywords: []string{"hsplit", "horizontal"}},
		paletteCommand{action: palActSplitV, enabled: true, label: "Split vertical", detail: m.keymap.Display("pane.split_v"), keywords: []string{"vsplit", "vertical"}},
		paletteCommand{action: palActFocus, enabled: true, label: "Toggle focus mode", detail: m.keymap.Display("pane.focus_toggle"), keywords: []string{"fullscreen", "zoom", "maximize"}},
		paletteCommand{action: palActNotes, enabled: true, label: "Toggle notes", detail: m.keymap.Display("pane.notes_toggle"), keywords: []string{"note", "editor"}},
		paletteCommand{action: palActRenamePane, enabled: true, label: "Rename pane", detail: m.keymap.Display("pane.rename")},
		paletteCommand{action: palActMute, enabled: true, label: muteLabel, detail: m.keymap.Display("pane.mute"), keywords: []string{"mute", "silence", "notification"}},
		paletteCommand{action: palActEager, enabled: true, label: eagerLabel, detail: m.keymap.Display("pane.toggle_eager"), keywords: []string{"eager", "restore", "restart"}},
		paletteCommand{action: palActHistory, enabled: historyOK, label: "Input history", detail: m.keymap.Display("pane.command_history"), keywords: []string{"history", "prompts"}},
		paletteCommand{action: palActLazygit, enabled: lazygitOK, label: "Open lazygit", detail: m.keymap.Display("pane.toggle_lazygit"), keywords: []string{"git", "lazygit"}},
		paletteCommand{action: palActHunk, enabled: hunkOK, label: "Open hunk", detail: m.keymap.Display("pane.toggle_hunk"), keywords: []string{"git", "hunk", "diff", "review"}},
		paletteCommand{action: palActNewPane, enabled: true, label: "New pane…", detail: "ctrl+n", keywords: []string{"create", "plugin", "claude", "terminal"}},
		paletteCommand{action: palActRestartPane, enabled: true, label: "Restart pane…", detail: m.keymap.Display("pane.restart"), keywords: []string{"restart", "respawn"}},
		paletteCommand{action: palActClosePane, enabled: true, label: "Close pane…", detail: m.keymap.Display("pane.close"), keywords: []string{"close", "kill"}},
	)

	// --- System ------------------------------------------------------------
	header("System")
	cmds = append(cmds,
		paletteCommand{action: palActSettings, enabled: true, label: "Settings", keywords: []string{"config", "preferences"}},
		paletteCommand{action: palActShortcuts, enabled: true, label: "Keyboard shortcuts", keywords: []string{"keys", "bindings", "help"}},
		paletteCommand{action: palActPlugins, enabled: true, label: "Plugins", keywords: []string{"plugin", "toml"}},
		paletteCommand{action: palActProcesses, enabled: true, label: "Processes", keywords: []string{"process", "processes", "memory", "mem", "ram", "cpu", "kill"}},
		paletteCommand{action: palActAbout, enabled: true, label: "About", detail: "f1", keywords: []string{"about", "version"}},
		paletteCommand{action: palActClientLog, enabled: true, label: "View client log", keywords: []string{"log", "client"}},
		paletteCommand{action: palActDaemonLog, enabled: true, label: "View daemon log", keywords: []string{"log", "daemon"}},
		paletteCommand{action: palActMCPLog, enabled: true, label: "View MCP logs", keywords: []string{"log", "mcp"}},
		paletteCommand{action: palActRedraw, enabled: true, label: "Force redraw", detail: m.keymap.Display("app.redraw"), keywords: []string{"redraw", "repaint", "refresh"}},
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
	// Content capacity is the box minus the rounded border (2) AND the
	// dialogBorder Padding(1,2) (4) — lipgloss draws the border INSIDE Width, so
	// a row of boxW-4 would soft-wrap its trailing (right-aligned) shortcut onto
	// the next line. dialogInnerWidth owns that accounting for every dialog.
	return dialogInnerWidth(m.width, paletteWidth)
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

	// The displayed list is the filtered commands plus, once the query is
	// non-empty, the "Found in panes" content-search section — one list the
	// cursor walks across. It is never empty: the command registry always has
	// System rows, and any non-empty query appends the content section (a hit or
	// a status row), so there is no "no rows at all" case to guard.
	display := m.paletteDisplay()
	start, end := paletteWindow(display, m.palette.cursor)
	if start > 0 {
		b.WriteString(subtle(fmt.Sprintf("  ↑ %d more", start)))
		b.WriteByte('\n')
	}
	for i := start; i < end; i++ {
		row := display[i]
		switch {
		case row.header:
			b.WriteString(renderPaletteHeader(row.label, inner))
		case row.info:
			b.WriteString(subtle("  " + row.label))
		default:
			b.WriteString(renderPaletteRow(row, i == m.palette.cursor, inner))
			if row.excerpt != "" {
				b.WriteByte('\n')
				b.WriteString(ctxMenuDisabledStyle.Render(truncateToWidth("    "+row.excerpt, inner)))
			}
		}
		b.WriteByte('\n')
	}
	if end < len(display) {
		b.WriteString(subtle(fmt.Sprintf("  ↓ %d more", len(display)-end)))
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	b.WriteString(subtle(hint))
	return b.String()
}

// paletteDisplay assembles the single list the cursor walks: the filtered
// commands, then — whenever the query is non-empty — a "Found in panes" section
// carrying the content-search results (or a status line while it resolves).
// Content hits are palActGoToPane rows, so the cursor, Enter dispatch, and row
// rendering all reuse the command machinery unchanged.
func (m Model) paletteDisplay() []paletteCommand {
	display := append([]paletteCommand(nil), m.palette.filtered...)
	if strings.TrimSpace(m.palette.query) == "" {
		return display
	}
	display = append(display, paletteCommand{header: true, label: "Found in panes"})
	switch {
	case len(m.palette.contentHits) > 0:
		display = append(display, m.palette.contentHits...)
		if m.palette.truncated {
			display = append(display, paletteCommand{info: true, label: "some panes hit the per-pane match cap"})
		}
	case m.palette.timedOut:
		display = append(display, paletteCommand{info: true, label: "Search timed out — is the daemon running?"})
	case m.palette.searching:
		display = append(display, paletteCommand{info: true, label: "Searching…"})
	default:
		display = append(display, paletteCommand{info: true, label: "No matches in any pane"})
	}
	return display
}

// clampPaletteCursor keeps cur in range and on a selectable row, preferring to
// leave it where it is (so an arriving content response does not yank the cursor
// off a command the user navigated to). Returns -1 when nothing is selectable.
func clampPaletteCursor(display []paletteCommand, cur int) int {
	if len(display) == 0 {
		return -1
	}
	if cur < 0 {
		cur = 0
	}
	if cur >= len(display) {
		cur = len(display) - 1
	}
	if display[cur].selectable() {
		return cur
	}
	for i := cur; i < len(display); i++ {
		if display[i].selectable() {
			return i
		}
	}
	return firstSelectable(display)
}

// rowLines is how many rendered lines a display row occupies: a content-hit row
// draws a label line PLUS a dim excerpt line (2); everything else is 1.
func rowLines(c paletteCommand) int {
	if c.excerpt != "" && !c.header && !c.info {
		return 2
	}
	return 1
}

// paletteWindow returns the [start, end) slice of display to render, sized to at
// most paletteVisibleLines RENDERED lines (a hit row counts as 2, not 1) and
// grown outward from the cursor so it stays visible. Budgeting by rendered lines
// — not entry count — keeps the box from overflowing a short terminal when the
// visible slice fills with two-line pane-match rows.
func paletteWindow(display []paletteCommand, cursor int) (int, int) {
	n := len(display)
	if n == 0 {
		return 0, 0
	}
	total := 0
	for _, c := range display {
		total += rowLines(c)
	}
	if total <= paletteVisibleLines {
		return 0, n
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= n {
		cursor = n - 1
	}
	start, end := cursor, cursor+1
	used := rowLines(display[cursor])
	// Grow forward first (show what follows the cursor), then backward, until the
	// next row on either side would exceed the line budget.
	for used < paletteVisibleLines {
		grew := false
		if end < n && used+rowLines(display[end]) <= paletteVisibleLines {
			used += rowLines(display[end])
			end++
			grew = true
		}
		if start > 0 && used+rowLines(display[start-1]) <= paletteVisibleLines {
			used += rowLines(display[start-1])
			start--
			grew = true
		}
		if !grew {
			break
		}
	}
	return start, end
}

// renderPaletteHeader draws a dim, upper-cased section title bounded to inner.
func renderPaletteHeader(label string, inner int) string {
	return ctxMenuDisabledStyle.Render(truncateToWidth(strings.ToUpper(label), inner))
}

// renderPaletteLine lays out one palette row: "› "/"  " cursor prefix, label
// left, detail right-aligned, padded to inner width. Shared by command rows and
// content-search hit rows — the single source of the width-clamp math. Both the
// detail and the label are bounded cell-aware (wide glyphs never wrap the box):
// the detail is clamped first so a long shortcut cannot starve the label.
func renderPaletteLine(label, detail string, cursor, disabled bool, inner int) string {
	prefix := "  "
	if cursor {
		prefix = "› "
	}
	contentW := inner - 2 // prefix takes 2 cells
	if contentW < 1 {
		contentW = 1
	}
	if maxDetail := contentW - 2; maxDetail >= 0 && lipgloss.Width(detail) > maxDetail {
		detail = truncateToWidth(detail, maxDetail)
	}
	detailW := lipgloss.Width(detail)
	labelMax := contentW - detailW - 1 // ≥1 space gap
	if labelMax < 1 {
		labelMax = 1
	}
	if lipgloss.Width(label) > labelMax {
		label = truncateToWidth(label, labelMax)
	}
	gap := contentW - lipgloss.Width(label) - detailW
	if gap < 1 {
		gap = 1
	}
	labelStyle := dialogNormal
	switch {
	case disabled:
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

// renderPaletteRow renders one command row via the shared layout.
func renderPaletteRow(c paletteCommand, cursor bool, inner int) string {
	return renderPaletteLine(c.label, c.detail, cursor, !c.enabled, inner)
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

// handleCommandPaletteKey routes keys while the palette is open. Value receiver,
// like the sibling handleXKey dialog handlers.
func (m Model) handleCommandPaletteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch {
	case key == "esc":
		m.closeCommandPalette()
		return m, nil
	case key == "enter":
		display := m.paletteDisplay()
		if c := m.palette.cursor; c >= 0 && c < len(display) && display[c].selectable() {
			return m.executePaletteCommand(display[c])
		}
		return m, nil
	case key == "up" || key == "ctrl+p":
		m.palette.cursor = nextSelectable(m.paletteDisplay(), m.palette.cursor, -1)
		return m, nil
	case key == "down" || key == "ctrl+n":
		m.palette.cursor = nextSelectable(m.paletteDisplay(), m.palette.cursor, +1)
		return m, nil
	case key == "backspace":
		if q := []rune(m.palette.query); len(q) > 0 {
			m.palette.query = string(q[:len(q)-1])
			return m.afterPaletteQueryChange()
		}
		return m, nil
	case key == "space":
		m.palette.query += " "
		return m.afterPaletteQueryChange()
	case msg.Text != "" && isPrintableText(msg.Text):
		// Only printable text extends the query; a key we do not handle above
		// (e.g. tab) may carry a control character in msg.Text — never inject it.
		m.palette.query += msg.Text
		return m.afterPaletteQueryChange()
	}
	return m, nil
}

// afterPaletteQueryChange refilters the command list and, whenever the query is
// non-empty, kicks off a debounced content search across all panes — both feed
// the one list paletteDisplay assembles. Single choke point for every path that
// mutates m.palette.query (typed text, backspace, space, paste) so the filter and
// the 150ms debounce behave identically regardless of how the query changed.
func (m Model) afterPaletteQueryChange() (tea.Model, tea.Cmd) {
	m.palette.filtered = filterPalette(m.palette.query, m.palette.commands)
	q := m.palette.query
	if strings.TrimSpace(q) == "" {
		// Empty query: browse commands only, no content search.
		m.palette.contentHits = nil
		m.palette.searching = false
		m.palette.timedOut = false
		m.palette.cursor = firstSelectable(m.paletteDisplay())
		return m, nil
	}
	// Non-empty query — drop the previous query's hits immediately so they never
	// render under the new query, show "Searching…" right away (not only once the
	// debounce fires), and schedule the content search. Any prior timeout state
	// goes with the stale hits.
	m.palette.contentHits = nil
	m.palette.searching = true
	m.palette.timedOut = false
	m.palette.cursor = firstSelectable(m.paletteDisplay())
	return m, paletteSearchDebounce(q)
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
	// Segment into GRAPHEME CLUSTERS, then take them from the end.
	//
	// A rune is not the unit of width: a variation selector measures 0 alone
	// while making the pair before it 2, so summing independently-measured
	// runes returns a suffix WIDER than w — the same overflow truncateCells
	// documents at the other end of the string, and remote-sourced names reach
	// both.
	//
	// One forward segmentation pass, then a backward walk over the collected
	// clusters. Re-measuring a growing suffix each step instead is quadratic:
	// it reallocates the candidate every iteration, and a zero-width cluster
	// never advances the total, so the loop cannot exit early on a long run of
	// them. lipgloss does the measuring so this cannot disagree with the width
	// the caller padded to.
	type cluster struct {
		text  string
		cells int
	}
	var clusters []cluster
	state, rest := -1, s
	for len(rest) > 0 {
		var c string
		c, rest, _, state = uniseg.FirstGraphemeClusterInString(rest, state)
		clusters = append(clusters, cluster{text: c, cells: lipgloss.Width(c)})
	}

	total, i := 0, len(clusters)
	for i > 0 {
		if total+clusters[i-1].cells > w {
			break
		}
		total += clusters[i-1].cells
		i--
	}

	var b strings.Builder
	for _, c := range clusters[i:] {
		b.WriteString(c.text)
	}
	return b.String()
}

// goToPane switches to the tab containing paneID and makes it the active
// pane — crossing a project boundary if that's where the pane lives.
// Shared by the command palette's "Go to pane" rows and content-search
// Enter. The old active pane's Active flag is cleared BEFORE jumpToPane
// moves the active project/tab (ordering is load-bearing for the border
// repaint); jumpToPane itself sends no IPC, so the daemon is told about the
// new active tab via switchTab afterward.
func (m Model) goToPane(paneID string) (tea.Model, tea.Cmd) {
	if cur := m.activeTabModel(); cur != nil {
		if old := cur.ActivePaneModel(); old != nil {
			old.Active = false
		}
	}
	ok, overlayCmd := m.jumpToPane(paneID)
	if !ok {
		return m, nil
	}
	if tab := m.activeTabModel(); tab != nil {
		if pane := tab.ActivePaneModel(); pane != nil {
			pane.Active = true
		}
	}
	return m, tea.Batch(overlayCmd, m.switchTab(m.activeTabIdx()))
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
		return m.goToPane(c.arg)
	case palActSwitchTab:
		for i, tab := range m.curTabs() {
			if tab != nil && tab.ID == c.arg {
				return m, m.switchTab(i)
			}
		}
		return m, nil

	case palActSwitchProject:
		// Resolved by ID through projectByID, never indexOfProject: the latter
		// answers 0 for an unknown id, so a project destroyed between building
		// the list and pressing Enter would switch to the FIRST one.
		for i, p := range m.projects {
			if p != nil && p.ID == c.arg {
				cmd := m.switchProject(i)
				return m, cmd
			}
		}
		return m, nil

	// --- Projects ----------------------------------------------------------
	case palActNewProject:
		return m.openNewProjectDialog()
	case palActRenameProject:
		return m.beginProjectRename(c.arg)
	case palActRemoveProject:
		if p := m.projectByID(c.arg); p != nil {
			if p.Dest != "" {
				return m, m.confirmDisconnectHost(p.ID)
			}
			return m, m.confirmDestroyProject(p.ID)
		}
		return m, nil
	case palActPrevProject:
		// Sequenced, not returned inline: toggleLastProject mutates m through
		// a pointer receiver via switchProject, and Go does not order a plain
		// operand against a call in the same return statement.
		cmd := m.toggleLastProject()
		return m, cmd
	case palActAttentionQueue:
		cmd := m.jumpToNextBlocked()
		return m, cmd
	case palActProjectSidebar:
		return m.toggleProjectSidebar()

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
	case palActHunk:
		return m, m.handleToggleHunk()
	case palActRestartPane:
		return m.openRestartPaneConfirm()
	case palActClosePane:
		return m.openClosePaneConfirm()

	// --- Tab ---------------------------------------------------------------
	case palActNewTab:
		return m.handleNewTab()
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
	case palActProcesses:
		m = m.openProcessesDialog()
		return m, m.refreshResources(true)
	case palActAbout:
		return m.openAboutDialog()
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
