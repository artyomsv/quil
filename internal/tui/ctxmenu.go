package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
	x, y   int    // clamped top-left of the rendered box (screen coords)
	cursor int    // index into items; always on an enabled item (or -1)
	items  []ctxMenuItem
}

func (s ctxMenuState) open() bool { return s.paneID != "" }

var (
	ctxMenuBorderStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("39"))
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
	return []ctxMenuItem{
		{ctxActHistory, "Input history", historyOK},
		{ctxActFocus, "Focus mode", true},
		{ctxActNotes, "Open notes", true},
		{ctxActLazygit, "Open lazygit", lazygitOK},
		{ctxActRename, "Rename pane", true},
		{ctxActMute, muteLabel, true},
		{ctxActAttention, attnLabel, true},
		{ctxActRestart, "Restart pane…", true},
		{ctxActClose, "Close pane…", true},
	}
}

// ctxMenuInnerWidth is the content width: longest label + one space of
// padding on each side. lipgloss.Width is rune/wide-glyph aware.
func ctxMenuInnerWidth(items []ctxMenuItem) int {
	w := 0
	for _, it := range items {
		if lw := lipgloss.Width(it.label); lw > w {
			w = lw
		}
	}
	return w + 2
}

// ctxMenuBoxSize returns the rendered box dimensions including the border.
// MUST stay in lockstep with renderCtxMenu — ctxMenuPos and ctxMenuHitRow
// both derive geometry from it.
func ctxMenuBoxSize(items []ctxMenuItem) (w, h int) {
	return ctxMenuInnerWidth(items) + 2, len(items) + 2
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

// ctxMenuHitRow maps a screen coordinate to a menu row. inside=false means
// the point is outside the box entirely; (-1, true) means inside the box but
// on the border, not on an item row.
func ctxMenuHitRow(s ctxMenuState, x, y int) (int, bool) {
	w, h := ctxMenuBoxSize(s.items)
	if x < s.x || x >= s.x+w || y < s.y || y >= s.y+h {
		return -1, false
	}
	row := y - s.y - 1
	if row < 0 || row >= len(s.items) || x == s.x || x == s.x+w-1 {
		return -1, true
	}
	return row, true
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

// renderCtxMenu draws the menu box. Every content line is padded to exactly
// ctxMenuInnerWidth so the border renders a straight right edge and
// ctxMenuBoxSize's geometry matches the output cell-for-cell.
func renderCtxMenu(s ctxMenuState) string {
	innerW := ctxMenuInnerWidth(s.items)
	rows := make([]string, len(s.items))
	for i, it := range s.items {
		label := " " + it.label + strings.Repeat(" ", innerW-lipgloss.Width(it.label)-2) + " "
		switch {
		case !it.enabled:
			rows[i] = ctxMenuDisabledStyle.Render(label)
		case i == s.cursor:
			rows[i] = ctxMenuCursorStyle.Render(label)
		default:
			rows[i] = ctxMenuItemStyle.Render(label)
		}
	}
	return ctxMenuBorderStyle.Render(strings.Join(rows, "\n"))
}

// openCtxMenu opens (or re-targets) the context menu for pane, anchored at
// the given screen coordinate. Closes any previous menu first (clearing the
// old target's highlight), kills in-flight drags (one interaction at a
// time), and drops any live selection — the menu owns the mouse now.
func (m *Model) openCtxMenu(pane *PaneModel, anchorX, anchorY int) {
	m.closeCtxMenu()
	m.clearDragState()
	m.selection = nil
	items := m.buildCtxMenuItems(pane)
	w, h := ctxMenuBoxSize(items)
	x, y := ctxMenuPos(anchorX, anchorY, w, h, m.width, m.height)
	m.ctxMenu = ctxMenuState{
		paneID: pane.ID,
		x:      x,
		y:      y,
		cursor: firstEnabled(items),
		items:  items,
	}
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
// editor.
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
	tab.ActivePane = paneID

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
