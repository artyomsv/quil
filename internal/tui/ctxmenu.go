package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// ctxMenuAction identifies one entry in the pane context menu. Dispatch in
// executeCtxMenuItem (Task 5) routes each id into the SAME handler logic the
// keybinding cases use — the menu is a second dispatcher, never a second
// implementation.
type ctxMenuAction int

const (
	ctxActNone ctxMenuAction = iota
	ctxActHistory
	ctxActFocus
	ctxActNotes
	ctxActLazygit
	ctxActHunk
	ctxActRename
	ctxActMute
	ctxActAttention
	ctxActClearAttention
	ctxActRestart
	ctxActClose
	// Project-row actions (Task 13) — only ever set on a menu opened via
	// openProjectCtxMenu, never mixed into buildCtxMenuItems' pane rows.
	ctxActRenameProject
	ctxActDestroyProject
	ctxActDisconnectHost
)

// ctxMenuItem is one row of the menu. Disabled rows render greyed, are
// skipped by cursor movement, and are inert to clicks. gapAfter draws a
// blank separator row below this item in the spaced layout — used at group
// boundaries (view actions / pane settings / destructive), not per row.
type ctxMenuItem struct {
	id       ctxMenuAction
	label    string
	enabled  bool
	gapAfter bool
}

// ctxMenuState is the live state of the pane context menu — a compositor
// overlay (overlayAt), NOT a dialogScreen: dialogs are modal and centered,
// this popup is positional and dismiss-on-outside-click. Zero value = closed.
//
// projectID (Task 13) is the sidebar's project-row menu sharing this same
// state/render/hit-test machinery: paneID and projectID are mutually
// exclusive target discriminators, never both set. A second dedicated struct
// was considered and rejected — none of the geometry/render/hit-test helpers
// below (innerWidth, boxSize, ctxMenuPos, ctxMenuHitRow, renderCtxMenu,
// nextEnabled…) touch paneID at all, so duplicating them for two rows would
// only buy an unused field.
type ctxMenuState struct {
	paneID    string // target pane; "" when the target is a project (or closed)
	projectID string // target project; "" when the target is a pane (or closed)
	title     string // pane/project display name shown as the header row
	x, y      int    // clamped top-left of the rendered box (screen coords)
	cursor    int    // index into items; always on an enabled item (or -1)
	// spaced honors the items' gapAfter group separators (a blank row
	// between action groups — near-misses at group edges land on an inert
	// spacer, and the destructive group stays visually isolated).
	// openCtxMenu falls back to the compact layout (no separators) when
	// the spaced box is taller than the content area.
	spaced bool
	items  []ctxMenuItem
}

func (s ctxMenuState) open() bool { return s.paneID != "" || s.projectID != "" }

// ctxMenuTitleCap bounds how far the header (pane display name — often a
// CWD) may widen the box beyond the widest item label. Longer titles are
// truncated at render; item labels always fit untruncated.
const ctxMenuTitleCap = 28

var (
	ctxMenuBorderStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("39"))
	ctxMenuTitleStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Bold(true)
	ctxMenuItemStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	ctxMenuCursorStyle   = lipgloss.NewStyle().Reverse(true)
	ctxMenuDisabledStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240")) // same grey as uninstalled plugins in Ctrl+N
)

// buildCtxMenuItems resolves the 11 menu rows for a target pane. Labels are
// state-dependent (mute/attention toggles); gates mirror the keybinding
// handlers exactly: history needs the plugin's record_history opt-in (the
// kb.CommandHistory probe), each overlay tool needs its own installed binary
// (the handleToggleOverlay availability gate).
func (m *Model) buildCtxMenuItems(pane *PaneModel) []ctxMenuItem {
	historyOK := false
	lazygitOK := false
	hunkOK := false
	if m.pluginRegistry != nil {
		if p := m.pluginRegistry.Get(pane.Type); p != nil {
			historyOK = p.Command.RecordHistory
		}
		// Each overlay tool is gated on its OWN binary: they share a slot, not
		// an installation.
		if p := m.pluginRegistry.Get(overlayPluginLazygit); p != nil {
			lazygitOK = p.Available
		}
		if p := m.pluginRegistry.Get(overlayPluginHunk); p != nil {
			hunkOK = p.Available
		}
	}
	muteLabel := "Mute notifications"
	if pane.Muted {
		muteLabel = "Unmute notifications"
	}
	attnLabel := "Mark attention"
	if pane.pinnedAttention {
		attnLabel = "Unmark attention"
	}
	// "Clear attention" is the inverse of the whole state block, not the
	// inverse of the pin above it: it drops the BLOCKED mark, which is the one
	// the user cannot otherwise get rid of. Three of the four marks it clears
	// are client-owned display state and nothing is sent for them; the pin is
	// daemon-owned and IS sent (see the handler).
	//
	// blockedSince is set by a hook edge and cleared only by another hook edge
	// (workStart / workAbort / workStop / workStopFinal, workstate.go) — this
	// row is the sole exception, which is what it exists to be. Every other
	// route to a clear therefore runs through the agent, so when the clearing
	// event never arrives (the hook stream stopped, the session ended in a way
	// that emitted nothing, the prompt was answered somewhere the hooks do not
	// observe) the pane stays marked for the life of the TUI process, and the
	// project row it rolls up into stays marked with it. A lying indicator in
	// the one place the user looks to decide where to go next is worse than no
	// indicator, and there was no way to dismiss it short of restarting.
	//
	// FOCUS is not a route to a clear, deliberately (ackFocusedPane, which runs
	// on every message including a spinner tick, records why). The ▲ does
	// vanish from the focused pane's own sidebar row — paneRow suppresses the
	// glyph — but the mark itself survives, so this row stays the only way to
	// dismiss a stuck one, on a pane the user is NOT going to sit in.
	//
	// Disabled when there is nothing to clear, so the row also ANSWERS the
	// question "is this pane actually still flagged" rather than silently
	// doing nothing.
	clearable := !pane.blockedSince.IsZero() || pane.unseen || pane.pinnedAttention
	// The focus item toggles tab-level focus mode, so its label reflects the
	// ACTIVE TAB's current state (the menu always targets a pane on the
	// active tab; in focus mode the only clickable pane IS the focused one).
	focusLabel := "Enter focus mode"
	if tab := m.activeTabModel(); tab != nil && tab.FocusMode() {
		focusLabel = "Exit focus mode"
	}
	// Group boundaries (gapAfter): view actions | pane settings | destructive.
	return []ctxMenuItem{
		{ctxActHistory, "Input history", historyOK, false},
		{ctxActFocus, focusLabel, true, false},
		{ctxActNotes, "Open notes", true, false},
		{ctxActLazygit, "Open lazygit", lazygitOK, false},
		{ctxActHunk, "Open hunk", hunkOK, true},
		{ctxActRename, "Rename pane", true, false},
		{ctxActMute, muteLabel, true, false},
		{ctxActAttention, attnLabel, true, false},
		{ctxActClearAttention, "Clear attention", clearable, true},
		{ctxActRestart, "Restart pane…", true, false},
		{ctxActClose, "Close pane…", true, false},
	}
}

// innerWidth is the content width: the longest item label (or the
// cap-bounded title, whichever is wider) + one space of padding on each
// side. lipgloss.Width is rune/wide-glyph aware.
func (s ctxMenuState) innerWidth() int {
	w := 0
	for _, it := range s.items {
		if lw := lipgloss.Width(it.label); lw > w {
			w = lw
		}
	}
	if tw := lipgloss.Width(s.title); tw > w {
		w = tw
	}
	if w > ctxMenuTitleCap {
		w = ctxMenuTitleCap
	}
	return w + 2
}

// gapsBefore counts the group-separator rows above item i in the spaced
// layout (0 in compact).
func (s ctxMenuState) gapsBefore(i int) int {
	if !s.spaced {
		return 0
	}
	n := 0
	for j := 0; j < i && j < len(s.items); j++ {
		if s.items[j].gapAfter {
			n++
		}
	}
	return n
}

// contentRows is the number of rows between the borders: header (title +
// blank separator) plus the item block — spaced layout adds one blank row
// per gapAfter group boundary.
func (s ctxMenuState) contentRows() int {
	return 2 + len(s.items) + s.gapsBefore(len(s.items))
}

// itemContentRow maps an item index to its content row (0-based, first row
// under the top border). Rows 0/1 are the title and separator.
func (s ctxMenuState) itemContentRow(i int) int {
	return 2 + i + s.gapsBefore(i)
}

// itemAtContentRow is the inverse of itemContentRow: -1 for the header rows
// and the inert group-separator rows.
func (s ctxMenuState) itemAtContentRow(r int) int {
	if r < 2 {
		return -1
	}
	for i := range s.items {
		switch row := s.itemContentRow(i); {
		case row == r:
			return i
		case row > r:
			return -1 // r landed on a separator row
		}
	}
	return -1
}

// itemScreenY is the absolute screen row of item i (for tests and hit-test
// call sites that need the forward mapping).
func (s ctxMenuState) itemScreenY(i int) int {
	return s.y + 1 + s.itemContentRow(i)
}

// boxSize returns the rendered box dimensions including the border. MUST
// stay in lockstep with renderCtxMenu — ctxMenuPos and ctxMenuHitRow both
// derive geometry from it.
func (s ctxMenuState) boxSize() (w, h int) {
	return s.innerWidth() + 2, s.contentRows() + 2
}

// ctxMenuPos clamps the menu's top-left so the whole box stays inside the
// content area — rows 1..screenH-2 (row 0 is the tab bar, row screenH-1 the
// status bar), columns 0..screenW-1. Preferred position is one cell right and
// below the anchor so the mouse pointer does not cover the first item.
func ctxMenuPos(anchorX, anchorY, boxW, boxH, screenW, screenH int) (int, int) {
	x := anchorX + 1
	y := anchorY + 1
	if x+boxW > screenW {
		x = screenW - boxW
	}
	if x < 0 {
		x = 0
	}
	if maxY := screenH - 1 - boxH; y > maxY {
		y = maxY
	}
	if y < 1 {
		y = 1
	}
	return x, y
}

// ctxMenuHitRow maps a screen coordinate to an item index. inside=false
// means the point is outside the box entirely; (-1, true) means inside the
// box but on no item (border, title, separator, or a spacer row).
func ctxMenuHitRow(s ctxMenuState, x, y int) (int, bool) {
	w, h := s.boxSize()
	if x < s.x || x >= s.x+w || y < s.y || y >= s.y+h {
		return -1, false
	}
	if x == s.x || x == s.x+w-1 {
		return -1, true
	}
	i := s.itemAtContentRow(y - s.y - 1)
	if i < 0 {
		return -1, true
	}
	return i, true
}

// firstEnabled returns the index of the first enabled item, or -1.
func firstEnabled(items []ctxMenuItem) int {
	for i, it := range items {
		if it.enabled {
			return i
		}
	}
	return -1
}

// nextEnabled returns the index of the next enabled item from cur in
// direction dir (+1 down, -1 up), wrapping past the ends and skipping
// disabled rows. A cursor of -1 (nothing enabled at open) resolves to
// firstEnabled; if no OTHER item is enabled the cursor stays put.
func nextEnabled(items []ctxMenuItem, cur, dir int) int {
	if len(items) == 0 {
		return -1
	}
	if cur < 0 {
		return firstEnabled(items)
	}
	for i := 1; i <= len(items); i++ {
		idx := ((cur+dir*i)%len(items) + len(items)) % len(items)
		if items[idx].enabled {
			return idx
		}
	}
	return cur
}

// renderCtxMenu draws the menu box: a title row (target pane's display
// name), a blank separator, then the items — with a blank spacer between
// adjacent items in the spaced layout. Every content line is padded to
// exactly innerWidth so the border renders a straight right edge and
// boxSize's geometry matches the output cell-for-cell (itemContentRow /
// itemAtContentRow depend on this row order).
func renderCtxMenu(s ctxMenuState) string {
	innerW := s.innerWidth()
	blank := strings.Repeat(" ", innerW)
	rows := make([]string, 0, s.contentRows())

	// Sanitize BEFORE measuring. Both titles this menu carries name something a
	// daemon told us about — a pane's name or a project's — and a daemon may be
	// on a host the user does not control. Truncation is not a substitute:
	// lipgloss.Width measures an escape sequence as zero cells, so a title that
	// is nothing but escapes passes the width check untouched and reaches the
	// terminal intact. Doing it here rather than at the two assignment sites
	// keeps the raw value in state (the codebase's render-only rule) and covers
	// any third title added later by construction.
	title := sanitizeRemoteText(s.title)
	if lipgloss.Width(title) > innerW-2 {
		title = ansi.Truncate(title, innerW-3, "…")
	}
	rows = append(rows,
		ctxMenuTitleStyle.Render(" "+title+strings.Repeat(" ", innerW-lipgloss.Width(title)-2)+" "),
		blank,
	)
	for i, it := range s.items {
		if s.spaced && i > 0 && s.items[i-1].gapAfter {
			rows = append(rows, blank)
		}
		label := " " + it.label + strings.Repeat(" ", innerW-lipgloss.Width(it.label)-2) + " "
		switch {
		case !it.enabled:
			rows = append(rows, ctxMenuDisabledStyle.Render(label))
		case i == s.cursor:
			rows = append(rows, ctxMenuCursorStyle.Render(label))
		default:
			rows = append(rows, ctxMenuItemStyle.Render(label))
		}
	}
	return ctxMenuBorderStyle.Render(strings.Join(rows, "\n"))
}

// openCtxMenu opens (or re-targets) the context menu for pane, anchored at
// the given screen coordinate. Closes any previous menu first (clearing the
// old target's highlight), kills in-flight drags (one interaction at a
// time), and drops any live selection — the menu owns the mouse now.
func (m *Model) openCtxMenu(pane *PaneModel, anchorX, anchorY int) {
	s := ctxMenuState{
		paneID: pane.ID,
		title:  paneDisplayName(pane),
		spaced: true,
		cursor: -1,
		items:  m.buildCtxMenuItems(pane),
	}
	s.cursor = firstEnabled(s.items)
	w, h := s.boxSize()
	// Prefer the spaced layout (blank row between items — forgiving mouse
	// targets); fall back to compact when the content area is too short.
	if h > m.height-2 {
		s.spaced = false
		w, h = s.boxSize()
	}
	// Bail before any state mutation when even the compact box cannot fit
	// inside the content area (row 0 is the tab bar, row m.height-1 the
	// status bar, so the usable content height is m.height-2). overlayAt
	// silently returns base unchanged when x+boxW > totalW, so opening
	// anyway would leave an INVISIBLE menu that still captures every
	// keyboard/mouse event until Esc. Applies to both entry points
	// (right-click and quick_actions).
	if w > m.width || h > m.height-2 {
		return
	}
	m.closeCtxMenu()
	m.clearDragState()
	// Menu wins over a live selection on this path: right-click never
	// reaches here with a selection active (Update's copy-to-clipboard
	// branch intercepts it first), but the keyboard entry point
	// (openQuickActionsMenu / kb.QuickActions) has no such gate — pressing
	// quick actions mid-selection is treated as abandoning the selection
	// (Enter remains the copy key), so this unconditionally discards it.
	m.selection = nil
	s.x, s.y = ctxMenuPos(anchorX, anchorY, w, h, m.width, m.height)
	m.ctxMenu = s
	pane.ctxTargetHighlight = true
}

// buildProjectCtxMenuItems is the sidebar project row's menu: Rename and
// Destroy. No availability gates — unlike the pane menu's history/lazygit
// rows, both actions are always valid for any project the sidebar can show.
func buildProjectCtxMenuItems(remote, unreachable bool) []ctxMenuItem {
	// Rename AND Destroy are greyed for an UNREACHABLE project — either the
	// SYNTHETIC placeholder the client invents for a daemon that has reported
	// no projects (its ID exists only here, so either message names something
	// the daemon has never heard of and its map lookup misses: the dialog is
	// accepted and nothing changes, which is exactly how it was reported for a
	// remote host while the same actions worked locally), or a real project
	// whose destination has no connection — Router.Send drops a message aimed
	// at it and returns nil, so the dialog would look just as silently accepted.
	//
	// Disconnect stays ENABLED in both cases, and is the only thing that can
	// work there: it is client-side entirely, and detaching the machine is
	// what a user reaching for "remove this" actually wants when the daemon
	// cannot hold a project in the first place (or cannot be reached at all).
	items := []ctxMenuItem{{ctxActRenameProject, "Rename project", !unreachable, false}}
	// ONE removal action, chosen by what the project is.
	//
	// Offering both on a remote read as two ways to do the same thing, and the
	// one users reached for first was the one that cannot work there: a daemon
	// may not be left with no project, so destroying the last one on a host
	// bootstraps a fresh "Default" and looks like the delete was ignored.
	// Disconnect is what "get this machine out of my sidebar" actually means,
	// and it leaves everything on the far side running.
	if remote {
		items = append(items, ctxMenuItem{ctxActDisconnectHost, "Disconnect host…", true, false})
	} else {
		items = append(items, ctxMenuItem{ctxActDestroyProject, "Destroy project…", !unreachable, false})
	}
	return items
}

// openProjectCtxMenu opens (or re-targets) the sidebar's project-row menu,
// mirroring openCtxMenu but keyed by projectID instead of paneID — see
// ctxMenuState's doc comment for why the two share one type. No
// ctxTargetHighlight equivalent: that field lives on PaneModel and marks the
// pane border, which has no project analogue (the active-project marker in
// the sidebar already shows which row is selected).
func (m *Model) openProjectCtxMenu(p *ProjectModel, anchorX, anchorY int) {
	s := ctxMenuState{
		projectID: p.ID,
		title:     p.Name,
		spaced:    false,
		cursor:    -1,
		items:     buildProjectCtxMenuItems(p.Dest != "", !m.projectActionable(p)),
	}
	s.cursor = firstEnabled(s.items)
	w, h := s.boxSize()
	if w > m.width || h > m.height-2 {
		return
	}
	m.closeCtxMenu()
	m.clearDragState()
	m.selection = nil
	s.x, s.y = ctxMenuPos(anchorX, anchorY, w, h, m.width, m.height)
	m.ctxMenu = s
}

// closeCtxMenu closes the menu and clears the target-pane highlight. Safe to
// call when already closed; nil-safe when the target pane has vanished.
func (m *Model) closeCtxMenu() {
	if m.ctxMenu.paneID != "" {
		if pane, _, _ := m.findPaneAndTab(m.ctxMenu.paneID); pane != nil {
			pane.ctxTargetHighlight = false
		}
	}
	m.ctxMenu = ctxMenuState{}
}

// openQuickActionsMenu is the keyboard entry point (kb.QuickActions): same
// menu as right-click, for the ACTIVE pane, anchored at its content
// top-left. No-op in notes mode — the key is notes-exempt so it reaches
// here, but the menu's actions restructure the layout out from under the
// editor. Unlike right-click (which yields to copy-selection when one is
// active), this path always wins over a live selection — see the
// m.selection = nil comment in openCtxMenu.
func (m Model) openQuickActionsMenu() (tea.Model, tea.Cmd) {
	if m.notesMode {
		return m, nil
	}
	if rect := m.activePaneRect(); rect != nil && rect.Pane != nil {
		// rect.OX is screen-absolute — every rect walk in model.go seeds its
		// recursion with projectSidebarWidth() (see the PaneRect origin
		// contract above activePaneRectFocus), so it already counts the
		// columns View() gives the sidebar before compositing the menu over
		// the joined frame. Adding the sidebar width here as well double-
		// counted it and drove the anchor a second sidebar-width to the right.
		m.openCtxMenu(rect.Pane, rect.OX+1, rect.OY+1)
	}
	return m, nil
}

// handleCtxMenuKey captures keyboard input while the menu is open. Quit is
// the only global that passes through — everything else is either menu
// navigation or swallowed (the menu is short-lived; no exempt list).
func (m Model) handleCtxMenuKey(key string) (tea.Model, tea.Cmd) {
	switch {
	case key == "esc":
		m.closeCtxMenu()
		return m, nil
	case key == "up" || key == "k":
		m.ctxMenu.cursor = nextEnabled(m.ctxMenu.items, m.ctxMenu.cursor, -1)
		return m, nil
	case key == "down" || key == "j":
		m.ctxMenu.cursor = nextEnabled(m.ctxMenu.items, m.ctxMenu.cursor, +1)
		return m, nil
	case key == "enter":
		if c := m.ctxMenu.cursor; c >= 0 && c < len(m.ctxMenu.items) && m.ctxMenu.items[c].enabled {
			return m.executeCtxMenuItem(m.ctxMenu.items[c])
		}
		return m, nil
	case m.isAction(key, "app.quit"):
		m.closeCtxMenu()
		return m, tea.Quit
	}
	return m, nil
}

// executeCtxMenuItem closes the menu, focuses the target pane (TUI-local,
// mirroring the setActivePaneMsg handler), and dispatches to the SAME
// handler logic the keybinding cases use. Destructive items keep their
// confirm dialogs.
func (m Model) executeCtxMenuItem(item ctxMenuItem) (tea.Model, tea.Cmd) {
	// Project row (Task 13): branches out before any of the pane-focus
	// bookkeeping below, which assumes a pane target throughout (tab lookup,
	// ActivePane sync). Both project actions keep the destructive one behind
	// the shared confirm dialog, same as ctxActClose/ctxActRestart.
	if projectID := m.ctxMenu.projectID; projectID != "" {
		m.closeCtxMenu()
		if !item.enabled {
			return m, nil
		}
		switch item.id {
		case ctxActRenameProject:
			return m.beginProjectRename(projectID)
		case ctxActDestroyProject:
			return m, m.confirmDestroyProject(projectID)
		case ctxActDisconnectHost:
			return m, m.confirmDisconnectHost(projectID)
		}
		return m, nil
	}

	paneID := m.ctxMenu.paneID
	m.closeCtxMenu()
	if !item.enabled || paneID == "" {
		return m, nil
	}
	// The target is resolved across EVERY project, not through curTabs():
	// that helper is the active project's slice alone, so indexing it with a
	// foreign tabIdx would act on an unrelated tab — or panic.
	pane, proj, tabIdx := m.findPaneAndTab(paneID)
	if pane == nil || proj == nil || tabIdx < 0 || tabIdx >= len(proj.tabs) {
		return m, nil // target vanished between open and execute
	}
	tab := proj.tabs[tabIdx]
	if tab == nil || tab.Root == nil || tab.Root.FindLeaf(paneID) == nil {
		return m, nil
	}
	// Eight of the ten items below resolve their target through
	// activeTabModel().ActivePaneModel() — they are shared with the keybinding
	// and command-palette paths, and this block cannot redirect them. So they
	// are correct only while the target sits in the ACTIVE tab.
	//
	// Every entry point focuses the pane before opening the menu, which
	// establishes that at OPEN time and does not keep it true afterwards: MCP
	// set_active_pane (setActivePaneMsg → jumpToPane) moves the active project
	// AND tab, and the Update-entry guard only closes a menu whose target has
	// VANISHED, not one whose active tab moved. Keyboard and mouse cannot reach
	// that state; MCP is the one producer that can. Acting anyway had Rename
	// seed the on-screen pane's name and Restart/Close arm a confirm for it.
	//
	// The refusal is UNIFORM, including the two items that resolve paneID
	// directly (ctxActAttention/ctxActClearAttention) and could still have acted
	// correctly: one menu is one surface, and "two of eleven rows work after the
	// tab moved" is a rule nobody can hold. The remedy is a second right-click.
	// It is checked BEFORE the focus sync below, so a refused execute leaves no
	// half-applied focus on a background tab either.
	//
	// A FUTURE entry point that opens this menu on a pane outside the active tab
	// without focusing it first will find every row inert here. That is the
	// intended failure — the fix is to focus first, as the sidebar right-click
	// and quick_actions paths do, not to loosen this.
	if proj != m.cur() || tabIdx != m.activeTabIdx() {
		return m, nil
	}
	// Sync the Active bool alongside ActivePane — mirrors the mouse-release
	// pane-focus path (model.go) and NavigateDirection (tab.go). Leaving
	// the old pane's Active flag set would keep its purple border while
	// the real target renders inactive; ActivePaneModel() only heals a
	// stale ID, never a stale flag.
	if old := tab.ActivePaneModel(); old != nil {
		old.Active = false
	}
	tab.ActivePane = paneID
	pane.Active = true

	switch item.id {
	case ctxActHistory:
		return m.openHistoryForActivePane()
	case ctxActFocus:
		return m.toggleFocusForActiveTab()
	case ctxActNotes:
		return m.toggleNotesMode()
	case ctxActLazygit:
		return m, m.handleToggleLazygit()
	case ctxActHunk:
		return m, m.handleToggleHunk()
	case ctxActRename:
		return m.beginPaneRename()
	case ctxActMute:
		return m, m.toggleActivePaneMute()
	case ctxActAttention:
		if pane, _, _ := m.findPaneAndTab(paneID); pane != nil {
			// Sent, not written. The pin is daemon-owned now, and
			// syncPaneMeta copies it back on every broadcast — so a local flip
			// would be reverted by the next workspace_state (the git ticker
			// alone delivers one every 5 s) and the mark would visibly undo
			// itself. The mute toggle has taken this route since it was
			// written; this is the same shape.
			return m, m.sendPinnedAttention(paneID, !pane.pinnedAttention)
		}
		return m, nil
	case ctxActClearAttention:
		if pane, _, _ := m.findPaneAndTab(paneID); pane != nil {
			// All four marks, because the row promises one thing. Clearing
			// only blockedSince leaves the pane green instead of amber and the
			// project row still counting it, which reads as the action having
			// half-worked.
			//
			// The first three are display state the user is dismissing, not
			// facts about the agent: nothing about THEM is sent to the daemon,
			// and the next hook edge re-derives whatever is actually true
			// (workstate.go owns every write to these fields but this one). So
			// a pane that really IS still parked marks itself again on its next
			// event rather than staying silently clear.
			pane.blockedSince = time.Time{}
			pane.blockedReason = ""
			pane.unseen = false
			// The pin is the exception: it lives on the daemon, so it is SENT
			// and not written here — the same route ctxActAttention takes, for
			// the same reason. Two things were wrong with doing both.
			//
			// The send was gated on the local value, and that value now says
			// only "what the last broadcast reported", never "what the daemon
			// holds". Mark deliberately does not write locally, so Mark
			// followed by Clear before the broadcast returns read the pin as
			// false, sent NOTHING, and then let the Mark's own broadcast put
			// the ◆ back after the user had cleared it — persisted. Two
			// right-clicks, and over ssh the window is hundreds of
			// milliseconds. It is sent unconditionally now: one idempotent
			// message on a user-initiated action buys a guarantee that does not
			// depend on what has arrived yet.
			//
			// The local write was also a lie in the two states that matter. A
			// broadcast already in flight re-sets the pin and the next one
			// clears it, so the ◆ blinks off, on, off; and with the link parked
			// Router.Send drops the message and returns nil, so nothing ever
			// arrives to revert the local clear and the mark stays gone until a
			// reconnect brings it back minutes later. One broadcast of latency
			// is the price the mute toggle already pays for its chip.
			return m, m.sendPinnedAttention(paneID, false)
		}
		return m, nil
	case ctxActRestart:
		return m.openRestartPaneConfirm()
	case ctxActClose:
		return m.openClosePaneConfirm()
	}
	return m, nil
}
