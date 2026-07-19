package tui

import (
	"strings"

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
	ctxActRename
	ctxActMute
	ctxActAttention
	ctxActRestart
	ctxActClose
)

// ctxMenuItem is one row of the menu. Disabled rows render greyed, are
// skipped by cursor movement, and are inert to clicks.
type ctxMenuItem struct {
	id      ctxMenuAction
	label   string
	enabled bool
}

// ctxMenuState is the live state of the pane context menu — a compositor
// overlay (overlayAt), NOT a dialogScreen: dialogs are modal and centered,
// this popup is positional and dismiss-on-outside-click. Zero value = closed.
type ctxMenuState struct {
	paneID string // target pane; "" = closed
	title  string // pane display name shown as the header row
	x, y   int    // clamped top-left of the rendered box (screen coords)
	cursor int    // index into items; always on an enabled item (or -1)
	// spaced inserts a blank row between items (easier mouse targeting — a
	// near-miss lands on an inert spacer instead of the neighboring action).
	// openCtxMenu falls back to the compact layout when the spaced box is
	// taller than the content area.
	spaced bool
	items  []ctxMenuItem
}

func (s ctxMenuState) open() bool { return s.paneID != "" }

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

// buildCtxMenuItems resolves the 9 menu rows for a target pane. Labels are
// state-dependent (mute/attention toggles); gates mirror the keybinding
// handlers exactly: history needs the plugin's record_history opt-in (the
// kb.CommandHistory probe), lazygit needs an installed binary (the
// handleToggleLazygit availability gate).
func (m *Model) buildCtxMenuItems(pane *PaneModel) []ctxMenuItem {
	historyOK := false
	lazygitOK := false
	if m.pluginRegistry != nil {
		if p := m.pluginRegistry.Get(pane.Type); p != nil {
			historyOK = p.Command.RecordHistory
		}
		if p := m.pluginRegistry.Get("lazygit"); p != nil {
			lazygitOK = p.Available
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
	// The focus item toggles tab-level focus mode, so its label reflects the
	// ACTIVE TAB's current state (the menu always targets a pane on the
	// active tab; in focus mode the only clickable pane IS the focused one).
	focusLabel := "Enter focus mode"
	if tab := m.activeTabModel(); tab != nil && tab.FocusMode() {
		focusLabel = "Exit focus mode"
	}
	return []ctxMenuItem{
		{ctxActHistory, "Input history", historyOK},
		{ctxActFocus, focusLabel, true},
		{ctxActNotes, "Open notes", true},
		{ctxActLazygit, "Open lazygit", lazygitOK},
		{ctxActRename, "Rename pane", true},
		{ctxActMute, muteLabel, true},
		{ctxActAttention, attnLabel, true},
		{ctxActRestart, "Restart pane…", true},
		{ctxActClose, "Close pane…", true},
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

// contentRows is the number of rows between the borders: header (title +
// blank separator) plus the item block — spaced layout interleaves one
// blank row between adjacent items.
func (s ctxMenuState) contentRows() int {
	n := len(s.items)
	if n == 0 {
		return 2
	}
	if s.spaced {
		return 2 + 2*n - 1
	}
	return 2 + n
}

// itemContentRow maps an item index to its content row (0-based, first row
// under the top border). Rows 0/1 are the title and separator.
func (s ctxMenuState) itemContentRow(i int) int {
	if s.spaced {
		return 2 + 2*i
	}
	return 2 + i
}

// itemAtContentRow is the inverse of itemContentRow: -1 for the header rows
// and the inert spacer rows between items.
func (s ctxMenuState) itemAtContentRow(r int) int {
	r -= 2
	if r < 0 {
		return -1
	}
	if s.spaced {
		if r%2 != 0 {
			return -1
		}
		r /= 2
	}
	if r >= len(s.items) {
		return -1
	}
	return r
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

	title := s.title
	if lipgloss.Width(title) > innerW-2 {
		title = ansi.Truncate(title, innerW-3, "…")
	}
	rows = append(rows,
		ctxMenuTitleStyle.Render(" "+title+strings.Repeat(" ", innerW-lipgloss.Width(title)-2)+" "),
		blank,
	)
	for i, it := range s.items {
		if s.spaced && i > 0 {
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

// closeCtxMenu closes the menu and clears the target-pane highlight. Safe to
// call when already closed; nil-safe when the target pane has vanished.
func (m *Model) closeCtxMenu() {
	if m.ctxMenu.paneID != "" {
		if pane, _ := m.findPaneAndTab(m.ctxMenu.paneID); pane != nil {
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
		m.openCtxMenu(rect.Pane, rect.OX+1, rect.OY+1)
	}
	return m, nil
}

// handleCtxMenuKey captures keyboard input while the menu is open. Quit is
// the only global that passes through — everything else is either menu
// navigation or swallowed (the menu is short-lived; no exempt list).
func (m Model) handleCtxMenuKey(key string) (tea.Model, tea.Cmd) {
	kb := m.cfg.Keybindings
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
	case kbMatches(key, kb.Quit):
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
	paneID := m.ctxMenu.paneID
	m.closeCtxMenu()
	if !item.enabled || paneID == "" {
		return m, nil
	}
	tab := m.activeTabModel()
	if tab == nil || tab.Root == nil || tab.Root.FindLeaf(paneID) == nil {
		return m, nil // target vanished between open and execute
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
	if pane, _ := m.findPaneAndTab(paneID); pane != nil {
		pane.Active = true
	}

	switch item.id {
	case ctxActHistory:
		return m.openHistoryForActivePane()
	case ctxActFocus:
		return m.toggleFocusForActiveTab()
	case ctxActNotes:
		return m.toggleNotesMode()
	case ctxActLazygit:
		return m, m.handleToggleLazygit()
	case ctxActRename:
		return m.beginPaneRename()
	case ctxActMute:
		return m, m.toggleActivePaneMute()
	case ctxActAttention:
		if pane, _ := m.findPaneAndTab(paneID); pane != nil {
			pane.pinnedAttention = !pane.pinnedAttention
		}
		return m, nil
	case ctxActRestart:
		return m.openRestartPaneConfirm()
	case ctxActClose:
		return m.openClosePaneConfirm()
	}
	return m, nil
}
