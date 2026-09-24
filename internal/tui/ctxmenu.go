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
	ctxActMarkDeletion
	ctxActRestart
	ctxActClose
	// Project-row actions (Task 13) — only ever set on a menu opened via
	// openProjectCtxMenu, never mixed into buildCtxMenuItems' pane rows.
	ctxActRenameProject
	ctxActDestroyProject
	ctxActDisconnectHost
	// Tab-row actions (Task 3) — only ever set on a menu opened via
	// openTabCtxMenu, never mixed into the pane or project rows above.
	ctxActRenameTab
	// ctxActTabColorList re-populates the OPEN menu in place with one row per
	// tabColors entry (buildTabColorItems) — it is not itself a colour choice.
	ctxActTabColorList
	// ctxActSetTabColor is one row of that re-populated list; the chosen
	// colour rides on ctxMenuItem.color, never decoded from the label.
	ctxActSetTabColor
	// ctxActMoveTab opens the project picker in MOVE mode (openMoveTabPicker)
	// for the target tab. buildTabCtxMenuItems hides the row entirely when
	// moveTabCandidates has nothing to offer, rather than greying it.
	ctxActMoveTab
	// ctxActMovePane opens the tab picker (openMovePanePicker) for the target
	// pane. Unlike ctxActMoveTab, buildCtxMenuItems GREYS this row rather
	// than hiding it — the pane menu's own convention already used by other
	// gated rows (history, lazygit and hunk in the view-actions group; Clear
	// attention in the pane-settings group beside this one).
	ctxActMovePane
	// ctxActTabLayoutList re-populates the OPEN tab menu in place with one row
	// per layoutPresets entry (buildTabLayoutItems) — the Set color… mechanism.
	ctxActTabLayoutList
	// ctxActTabLayout is one row of that list; the arrangement rides on
	// ctxMenuItem.layout, never decoded from the label.
	ctxActTabLayout
	// Project-group rows. ctxActGroupList re-populates the PROJECT menu in
	// place (the Set color… mechanism) with one ctxActSetGroup row per group,
	// then ctxActNewGroup and ctxActUngroup; the chosen group rides on
	// ctxMenuItem.groupName, never decoded from the label.
	ctxActGroupList
	ctxActSetGroup
	ctxActNewGroup
	ctxActUngroup
	// Group-header rows — only ever on a menu opened via openGroupCtxMenu.
	ctxActRenameGroup
	ctxActToggleGroup
	ctxActGroupUp
	ctxActGroupDown
	ctxActDeleteGroup
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
	// color is set only on ctxActSetTabColor rows: one of the tabColors
	// values ("" = default). renderCtxMenu paints an enabled, non-cursor row
	// with it set in that colour instead of ctxMenuItemStyle.
	color string
	// layout is set only on ctxActTabLayout rows: the arrangement the row applies.
	layout layoutKind
	// groupName is set only on ctxActSetGroup rows: the group (raw, unsanitized
	// name) the row moves the project into.
	groupName string
}

// ctxMenuState is the live state of the pane context menu — a compositor
// overlay (overlayAt), NOT a dialogScreen: dialogs are modal and centered,
// this popup is positional and dismiss-on-outside-click. Zero value = closed.
//
// projectID (Task 13), tabID (Task 3) and groupName (project groups) are the
// sidebar's project-row menu, the tab menu and the group-header menu sharing
// this same state/render/hit-test machinery: paneID, projectID, tabID and
// groupName are FOUR mutually exclusive target discriminators, never more
// than one set. A dedicated struct per kind was considered and rejected — none
// of the geometry/render/hit-test helpers below (innerWidth, boxSize,
// ctxMenuPos, ctxMenuHitRow, renderCtxMenu, nextEnabled…) touch any of the ID
// fields at all, so duplicating them would only buy unused fields.
type ctxMenuState struct {
	paneID    string // target pane; "" when the target is a project, a tab, a group, or closed
	projectID string // target project; "" when the target is a pane, a tab, a group, or closed
	// projectDest is the target project's Dest — the second half of its group
	// key. Project IDs are minted per daemon and can collide across daemons.
	projectDest string
	tabID       string // target tab; "" when the target is a pane, a project, a group, or closed
	groupName   string // target group; "" when the target is a pane, a project, a tab, or closed
	title       string // pane/project/tab display name shown as the header row
	x, y        int    // clamped top-left of the rendered box (screen coords)
	cursor      int    // index into items; always on an enabled item (or -1)
	// spaced honors the items' gapAfter group separators (a blank row
	// between action groups — near-misses at group edges land on an inert
	// spacer, and the destructive group stays visually isolated).
	// openCtxMenu falls back to the compact layout (no separators) when
	// the spaced box is taller than the content area.
	spaced bool
	items  []ctxMenuItem
}

func (s ctxMenuState) open() bool {
	return s.paneID != "" || s.projectID != "" || s.tabID != "" || s.groupName != ""
}

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
		// an installation. Asked of the active project's daemon, which is the
		// one handleToggleOverlay would create the overlay on.
		dest := m.activeDest()
		lazygitOK = m.pluginAvailableFor(dest, overlayPluginLazygit)
		hunkOK = m.pluginAvailableFor(dest, overlayPluginHunk)
	}
	muteLabel := "Mute notifications"
	if pane.Muted {
		muteLabel = "Unmute notifications"
	}
	attnLabel := "Mark attention"
	if pane.pinnedAttention {
		attnLabel = "Unmark attention"
	}
	delLabel := "Mark for deletion"
	if pane.markedForDeletion {
		delLabel = "Unmark for deletion"
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
		{id: ctxActHistory, label: "Input history", enabled: historyOK},
		{id: ctxActFocus, label: focusLabel, enabled: true},
		{id: ctxActNotes, label: "Open notes", enabled: true},
		{id: ctxActLazygit, label: "Open lazygit", enabled: lazygitOK},
		{id: ctxActHunk, label: "Open hunk", enabled: hunkOK, gapAfter: true},
		{id: ctxActRename, label: "Rename pane", enabled: true},
		// Moving deletes nothing, so it stays above the destructive separator
		// with the rest of the pane-settings group — and there is
		// deliberately no replace variant, unlike a worktree create.
		{id: ctxActMovePane, label: "Move to tab…", enabled: len(m.movePaneCandidates(pane.ID)) > 0},
		{id: ctxActMute, label: muteLabel, enabled: true},
		// Before the attention pair rather than after it, and NOT beside
		// Close pane… below the separator. Two reasons. The pin and Clear
		// attention are one vocabulary and have to stay adjacent — the group
		// separator sits on Clear attention precisely to close that block.
		// And this row deletes nothing: it records a decision about a pane
		// that stays alive, so putting it under a separator that means
		// "destructive from here down" would misdescribe it.
		{id: ctxActMarkDeletion, label: delLabel, enabled: true},
		{id: ctxActAttention, label: attnLabel, enabled: true},
		{id: ctxActClearAttention, label: "Clear attention", enabled: clearable, gapAfter: true},
		{id: ctxActRestart, label: "Restart pane…", enabled: true},
		{id: ctxActClose, label: "Close pane…", enabled: true},
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
		// innerWidth caps the box at ctxMenuTitleCap, so a label can be wider
		// than the box — a dynamic one (a group name) always could. Cut it like
		// the title: an uncut one makes the pad count negative and Repeat
		// panics, taking the whole TUI down on a render.
		text := it.label
		if lipgloss.Width(text) > innerW-2 {
			text = ansi.Truncate(text, innerW-3, "…")
		}
		label := " " + text + strings.Repeat(" ", innerW-lipgloss.Width(text)-2) + " "
		switch {
		case !it.enabled:
			rows = append(rows, ctxMenuDisabledStyle.Render(label))
		case i == s.cursor:
			// Reverse video shows the colour as a background even on a
			// ctxActSetTabColor row — the cursor style always wins here.
			rows = append(rows, ctxMenuCursorStyle.Render(label))
		case it.color != "":
			rows = append(rows, lipgloss.NewStyle().Foreground(lipgloss.Color(it.color)).Render(label))
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
	items := []ctxMenuItem{
		{id: ctxActRenameProject, label: "Rename project", enabled: !unreachable},
		// Client-side only, so — like Disconnect — it works on every row the
		// sidebar can show, offline and synthetic included.
		{id: ctxActGroupList, label: "Move to group…", enabled: true},
	}
	// ONE removal action, chosen by what the project is.
	//
	// Offering both on a remote read as two ways to do the same thing, and the
	// one users reached for first was the one that cannot work there: a daemon
	// may not be left with no project, so destroying the last one on a host
	// bootstraps a fresh "Default" and looks like the delete was ignored.
	// Disconnect is what "get this machine out of my sidebar" actually means,
	// and it leaves everything on the far side running.
	if remote {
		items = append(items, ctxMenuItem{id: ctxActDisconnectHost, label: "Disconnect host…", enabled: true})
	} else {
		items = append(items, ctxMenuItem{id: ctxActDestroyProject, label: "Destroy project…", enabled: !unreachable})
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
	// The group-name editor owns every key; a menu opened over it could start
	// a second edit (New group…) that silently replaces the one being typed.
	if m.groupEdit.active() {
		return
	}
	s := ctxMenuState{
		projectID:   p.ID,
		projectDest: p.Dest,
		title:       p.Name,
		spaced:      false,
		cursor:      -1,
		items:       buildProjectCtxMenuItems(p.Dest != "", !m.projectActionable(p)),
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

// buildTabCtxMenuItems is the tab bar / sidebar tab-heading's menu: Rename,
// Set color…, Layout…, and — as the LAST row, only when moveTabCandidates has
// something to offer — Move to project…. Compact layout (spaced=false): none
// of the rows have a natural group boundary to space out. Move to project…
// is HIDDEN rather than greyed when there is nowhere to move the tab to (a
// single-project workspace, or every other project on the tab's own Dest
// offline or synthetic). Layout… is GREYED instead: it has a reason to be
// unavailable (one pane, or a tab busy with a create, a template, a worktree
// checkout or this client's own split) that the grey states, and its
// presence must not depend on the tab's state.
func (m *Model) buildTabCtxMenuItems(tab *TabModel) []ctxMenuItem {
	items := []ctxMenuItem{
		{id: ctxActRenameTab, label: "Rename tab", enabled: true},
		{id: ctxActTabColorList, label: "Set color…", enabled: true},
		{id: ctxActTabLayoutList, label: "Layout…", enabled: m.tabArrangeable(tab)},
	}
	if len(m.moveTabCandidates(tab.ID)) > 0 {
		items = append(items, ctxMenuItem{id: ctxActMoveTab, label: "Move to project…", enabled: true})
	}
	return items
}

// moveTabCandidates lists the projects a tab may move to: same Dest as the
// tab's own project (a project ID is only meaningful to the daemon that
// minted it), not the tab's own project, and projectActionable (no synthetic
// placeholder, no offline stand-in — Router.Send would drop the message).
// Empty when the tab's own project is unknown or itself not actionable.
// Order = m.projects order.
func (m *Model) moveTabCandidates(tabID string) []*ProjectModel {
	owner := m.projectOf(tabID)
	if !m.projectActionable(owner) {
		return nil
	}
	var out []*ProjectModel
	for _, p := range m.projects {
		if p == owner {
			continue
		}
		if p.Dest == owner.Dest && m.projectActionable(p) {
			out = append(out, p)
		}
	}
	return out
}

// tabInFlight reports whether tab has something in progress that a moved
// pane could be swallowed by or that could itself be disrupted by a move
// arriving mid-operation: a worktree create or replace, a template layout not
// yet applied, or a leaf still waiting on its own worktree checkout (the
// new-tab worktree placeholder, Pane.PreparingWorktree). A nil tab counts as
// in flight — there is nothing to move into or out of.
func (m *Model) tabInFlight(tab *TabModel) bool {
	if tab == nil {
		return true
	}
	if m.worktreeCreates[tab.ID] != "" {
		return true
	}
	if m.worktreeReplaced[tab.ID] != nil {
		return true
	}
	if tab.templateLayoutPending {
		return true
	}
	for _, p := range tab.Leaves() {
		if p.PreparingWorktree != "" {
			return true
		}
	}
	return false
}

// movePaneCandidates lists the tabs paneID may move to, or nil when the pane
// cannot move at all.
//
// The pane must be in an actionable project (projectActionable), must not be
// a preparing placeholder itself (PreparingWorktree), and its own tab must
// not be in flight (tabInFlight) — a pane cannot leave a tab whose worktree
// checkout, replace, or template layout is still resolving.
//
// Candidates are every tab of every actionable project on the SAME Dest as
// the pane's own project, except the pane's own tab and any tab that is
// itself in flight (tabInFlight, which also excludes a tab holding a
// PreparingWorktree leaf — the new-tab worktree placeholder).
//
// Order: the pane's own project's tabs first, in tab order, then the other
// projects in m.projects (sidebar) order.
func (m *Model) movePaneCandidates(paneID string) []*TabModel {
	pane, proj, tabIdx := m.findPaneAndTab(paneID)
	if pane == nil || proj == nil || tabIdx < 0 || tabIdx >= len(proj.tabs) {
		return nil
	}
	if pane.PreparingWorktree != "" {
		return nil
	}
	if !m.projectActionable(proj) {
		return nil
	}
	srcTab := proj.tabs[tabIdx]
	if m.tabInFlight(srcTab) {
		return nil
	}

	var out []*TabModel
	for _, t := range proj.tabs {
		if t == srcTab || m.tabInFlight(t) {
			continue
		}
		out = append(out, t)
	}
	for _, p := range m.projects {
		if p == proj || p.Dest != proj.Dest || !m.projectActionable(p) {
			continue
		}
		for _, t := range p.tabs {
			if m.tabInFlight(t) {
				continue
			}
			out = append(out, t)
		}
	}
	return out
}

// buildTabColorItems returns one row per tabColors entry, in palette order,
// for the menu ctxActTabColorList re-populates. The marker is exactly two
// cells on every row ("✓ " for the current colour, "  " otherwise) so
// ctxMenuState.innerWidth — measured from the label BEFORE styling — stays
// honest across every row.
func buildTabColorItems(current string) []ctxMenuItem {
	items := make([]ctxMenuItem, 0, len(tabColors))
	for _, c := range tabColors {
		marker := "  "
		if c == current {
			marker = "✓ "
		}
		items = append(items, ctxMenuItem{
			id:      ctxActSetTabColor,
			label:   marker + tabColorLabel(c),
			enabled: true,
			color:   c,
		})
	}
	return items
}

// openTabCtxMenu opens (or re-targets) the tab bar / sidebar's tab-heading
// menu, mirroring openProjectCtxMenu but keyed by tabID — see
// ctxMenuState's doc comment for why the three target kinds share one type.
// Never switches tabs, unlike a left-click on the same row: right-click only
// targets a tab, it does not act on it.
//
// Refuses to open (without mutating anything) while another modal surface
// owns input. Notes mode and an inline rename would be stranded behind a
// menu they cannot see; a dialog is already modal. Rename would otherwise
// switch tabs out from under the notes editor and strand it bound to a pane
// that just left the screen — see switchProject's own notes comment for why
// that matters.
func (m *Model) openTabCtxMenu(tab *TabModel, anchorX, anchorY int) {
	if m.notesMode || m.renaming || m.renamingPane || m.groupEdit.active() || m.dialog != dialogNone {
		return
	}
	s := ctxMenuState{
		tabID:  tab.ID,
		title:  tab.Name,
		spaced: false,
		cursor: -1,
		items:  m.buildTabCtxMenuItems(tab),
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

// openTabColorList re-populates the OPEN tab menu in place with the colour
// list: same tabID and title, items replaced, cursor on the tab's current
// colour. Esc closes the whole menu from here — there is no "back" item.
//
// Re-derives the top-left from the box's own current position rather than a
// fresh anchor, so it stays put unless the taller box no longer fits —
// ctxMenuPos always adds one cell to its anchor, so subtracting one first is
// what makes re-deriving idempotent. Closes the menu instead of opening it
// when even the re-clamped box cannot fit, matching openTabCtxMenu's own
// bail (never leave an invisible menu still owning input).
func (m *Model) openTabColorList(tab *TabModel) {
	items := buildTabColorItems(tab.Color)
	cursor := 0
	for i, it := range items {
		if it.color == tab.Color {
			cursor = i
			break
		}
	}
	s := m.ctxMenu
	s.items = items
	s.spaced = false
	s.cursor = cursor
	w, h := s.boxSize()
	if w > m.width || h > m.height-2 {
		m.closeCtxMenu()
		return
	}
	s.x, s.y = ctxMenuPos(m.ctxMenu.x-1, m.ctxMenu.y-1, w, h, m.width, m.height)
	m.ctxMenu = s
}

// buildTabLayoutItems returns one row per layoutPresets entry, in table order,
// all enabled or all greyed: the gate belongs to the tab, not to a row.
func buildTabLayoutItems(enabled bool) []ctxMenuItem {
	items := make([]ctxMenuItem, 0, len(layoutPresets))
	for _, p := range layoutPresets {
		items = append(items, ctxMenuItem{id: ctxActTabLayout, label: p.label, enabled: enabled, layout: p.kind})
	}
	return items
}

// openTabLayoutList re-populates the OPEN tab menu in place with the layout
// list, exactly as openTabColorList does with the colours: same tabID and
// title, items replaced, Esc closes the whole menu (no "back" row), the box
// re-derived from its own position, and the menu CLOSED when even the
// re-clamped box cannot fit rather than left invisible and owning input.
//
// The rows are gated here too, but the gate that matters runs at execute
// time: applyTabArrangement re-checks the tab, so one that turned busy while
// the list was open is refused with a flash.
func (m *Model) openTabLayoutList(tab *TabModel) {
	s := m.ctxMenu
	s.items = buildTabLayoutItems(m.tabArrangeable(tab))
	s.spaced = false
	s.cursor = firstEnabled(s.items)
	w, h := s.boxSize()
	if w > m.width || h > m.height-2 {
		m.closeCtxMenu()
		return
	}
	s.x, s.y = ctxMenuPos(m.ctxMenu.x-1, m.ctxMenu.y-1, w, h, m.width, m.height)
	m.ctxMenu = s
}

// executeTabCtxMenuItem dispatches one tab-menu row. The uniform refusal
// below (proj != m.cur()) mirrors executeCtxMenuItem's pane branch: every
// entry point shows only the active project's tabs, and MCP switch_project
// is the one producer that can move the active project underneath an open
// menu.
func (m Model) executeTabCtxMenuItem(tabID string, item ctxMenuItem) (tea.Model, tea.Cmd) {
	proj := m.projectOf(tabID)
	if proj == nil || proj != m.cur() {
		m.closeCtxMenu()
		return m, nil
	}
	// Resolved with an explicit loop, never indexOfTab, which answers 0 on a
	// miss — indistinguishable from "the first tab", the one target this
	// menu must never silently redirect to.
	idx := -1
	for i, t := range proj.tabs {
		if t.ID == tabID {
			idx = i
			break
		}
	}
	if idx < 0 {
		m.closeCtxMenu()
		return m, nil
	}
	if !item.enabled {
		m.closeCtxMenu()
		return m, nil
	}
	tab := proj.tabs[idx]
	switch item.id {
	case ctxActRenameTab:
		m.closeCtxMenu()
		// switchTab has a pointer receiver: sequenced on its own statement so
		// it is never mixed into the return expression, where Go gives no
		// ordering guarantee against the other operand.
		var cmd tea.Cmd
		if idx != m.activeTabIdx() {
			cmd = m.switchTab(idx)
		}
		// Comma-ok, not a bare assertion: beginTabRename is typed tea.Model,
		// the signature that invites returning something else, and a failed
		// assertion here would panic inside the Update loop.
		next, _ := m.beginTabRename()
		if nm, ok := next.(Model); ok {
			m = nm
		}
		return m, cmd
	case ctxActTabColorList:
		m.openTabColorList(tab)
		return m, nil
	case ctxActSetTabColor:
		// Optimistic local write, same as cycleTabColor: the daemon is the
		// authority and syncs it back on the next broadcast, but not writing
		// it here would show the old colour until then.
		tab.Color = item.color
		m.closeCtxMenu()
		return m, m.updateTab(tab.ID, tab.Name, item.color)
	case ctxActTabLayoutList:
		m.openTabLayoutList(tab)
		return m, nil
	case ctxActTabLayout:
		// Acts on THIS tab, active or not — no switchTab, unlike Rename. The
		// apply step owns every refusal.
		m.closeCtxMenu()
		cmd := m.arrangeTab(tab, item.layout)
		return m, cmd
	case ctxActMoveTab:
		// The proj == m.cur() refusal above already ran; openMoveTabPicker
		// needs only the tab id, resolved fresh at Enter time by the picker
		// itself rather than a pointer closed over here.
		m.closeCtxMenu()
		return m.openMoveTabPicker(tabID)
	}
	return m, nil
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
		dest := m.ctxMenu.projectDest
		// Move to group… re-populates the menu in place, so it is the one row
		// that runs BEFORE the close below.
		if item.enabled && item.id == ctxActGroupList {
			cmd := m.openProjectGroupList()
			return m, cmd
		}
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
		case ctxActSetGroup:
			cmd := m.moveProjectToGroup(dest, projectID, item.groupName)
			return m, cmd
		case ctxActUngroup:
			cmd := m.ungroupProject(dest, projectID)
			return m, cmd
		case ctxActNewGroup:
			m.beginGroupEdit(groupEditState{mode: groupEditNew, dest: dest, projectID: projectID})
			return m, nil
		}
		return m, nil
	}

	// Group header: its own dispatcher, like the tab row below.
	if name := m.ctxMenu.groupName; name != "" {
		return m.executeGroupCtxMenuItem(name, item)
	}

	// Tab row (Task 3): same early branch-out as the project row above, for
	// the same reason — executeTabCtxMenuItem owns its own refusal and
	// dispatch, and the pane-focus bookkeeping below assumes a pane target.
	if tabID := m.ctxMenu.tabID; tabID != "" {
		return m.executeTabCtxMenuItem(tabID, item)
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
	case ctxActMovePane:
		// The uniform active-tab refusal and the focus sync above it have
		// already run; openMovePanePicker needs only the pane id, resolved
		// fresh at Enter time by the picker itself rather than a pointer
		// closed over here.
		return m.openMovePanePicker(paneID)
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
	case ctxActMarkDeletion:
		if pane, _, _ := m.findPaneAndTab(paneID); pane != nil {
			// Sent, not written — the same rule ctxActAttention follows and
			// for the same reason: the mark is daemon-owned, syncPaneMeta
			// copies it back on every broadcast, and a local flip would be
			// reverted by the next workspace_state and visibly undo itself.
			//
			// The daemon also clears the attention pin as a side effect of
			// setting this, which is a second reason the answer has to come
			// from there: this client cannot know the pin is gone until the
			// broadcast says so.
			return m, m.sendMarkedForDeletion(paneID, !pane.markedForDeletion)
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
			// The daemon keeps a copy of the unseen mark so it survives a TUI
			// restart, so the clear is reported as well as applied — else the
			// next start seeds the green back. Unconditional for the same
			// reason the pin send below is: a user-initiated action buys a
			// guarantee that does not depend on what has arrived yet.
			m.reportUnseen(paneID, false)
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
