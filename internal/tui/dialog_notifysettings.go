package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/artyomsv/quil/internal/config"
)

// notifyToggle is one row of F1 -> Settings -> Notifications.
//
// Every row is a plain on/off switch, so there is no edit mode and set takes no
// value — unlike settingsField, which also has to serve text and number rows. A
// heading row is inert: it carries a label and nothing else, and the cursor
// skips over it in both directions so Enter can never land on one.
type notifyToggle struct {
	label   string
	hint    string
	get     func(m *Model) string
	set     func(m *Model)
	heading bool
}

// notifySettingsRows returns the screen's rows, headings included.
//
// Sidebar-event toggles apply LIVE: each setter re-projects the config onto the
// notification center's filter, because a visible control that did nothing
// until relaunch reads as a broken dialog — the same rule the Sidebar width row
// states. The toast toggles apply live for free, since raiseAttentionToast
// reads m.cfg on every edge.
func notifySettingsRows() []notifyToggle {
	// group builds one event-group row. The accessor returns a POINTER into
	// the config so one closure serves every group; m.cfg is a value field on
	// Model, so &m.cfg.Notification.Events is addressable through *Model. The
	// store goes through the shared row (key), so the browser's page applies
	// the same check; the filter is re-projected live here, where the
	// notification center is.
	group := func(label, hint, key string, read func(*config.EventGroupsConfig) *bool) notifyToggle {
		return notifyToggle{
			label: label,
			hint:  hint,
			get:   func(m *Model) string { return boolStr(*read(&m.cfg.Notification.Events)) },
			set: func(m *Model) {
				if m.applySetting(key, strconv.FormatBool(!*read(&m.cfg.Notification.Events))) {
					m.notifications.SetGroups(groupFilterFrom(m.cfg.Notification.Events))
				}
			},
		}
	}

	return []notifyToggle{
		{label: "Desktop toasts", heading: true},
		{
			// Reports STATE, not the flag. With Enabled defaulting true,
			// enabled-but-unregistered is the DEFAULT on a fresh Windows
			// install rather than an edge case, and a bare "on" there would
			// claim toasts are working when no toast can be displayed at all.
			//
			// The row deliberately does NOT perform registration: writing a
			// Start Menu shortcut and an HKCU key as a side effect of a config
			// toggle is the auto-register behaviour this design rejected. It
			// names the command instead.
			label: "Enabled",
			hint:  "needs notify setup",
			get:   func(m *Model) string { return m.desktopState().label() },
			set: func(m *Model) {
				m.applySetting("notification.desktop.enabled", strconv.FormatBool(!m.cfg.Notification.Desktop.Enabled))
			},
		},
		{
			label: "On blocked",
			hint:  "waiting on you",
			get:   func(m *Model) string { return boolStr(m.cfg.Notification.Desktop.Blocked) },
			set: func(m *Model) {
				m.applySetting("notification.desktop.blocked", strconv.FormatBool(!m.cfg.Notification.Desktop.Blocked))
			},
		},
		{
			label: "On done",
			hint:  "finished while away",
			get:   func(m *Model) string { return boolStr(m.cfg.Notification.Desktop.Done) },
			set: func(m *Model) {
				m.applySetting("notification.desktop.done", strconv.FormatBool(!m.cfg.Notification.Desktop.Done))
			},
		},
		{label: "Sidebar events", heading: true},
		group("Agent turns", "start + reply ready", "notification.events.agent_turn",
			func(c *config.EventGroupsConfig) *bool { return &c.AgentTurn }),
		group("Agent blocked", "permission prompts", "notification.events.agent_blocked",
			func(c *config.EventGroupsConfig) *bool { return &c.AgentBlocked }),
		group("Agent subagents", "subagent start/stop", "notification.events.agent_subagent",
			func(c *config.EventGroupsConfig) *bool { return &c.AgentSubagent }),
		group("Agent session", "end, compaction", "notification.events.agent_session",
			func(c *config.EventGroupsConfig) *bool { return &c.AgentSession }),
		group("Process", "exited / failed", "notification.events.process",
			func(c *config.EventGroupsConfig) *bool { return &c.Process }),
		group("Pane", "closed, pinned, marked", "notification.events.pane",
			func(c *config.EventGroupsConfig) *bool { return &c.Pane }),
		group("MCP", "agent drove a pane", "notification.events.mcp",
			func(c *config.EventGroupsConfig) *bool { return &c.MCP }),
		group("System", "blocked, worktree", "notification.events.system",
			func(c *config.EventGroupsConfig) *bool { return &c.System }),
		group("Commands", "every shell command", "notification.events.commands",
			func(c *config.EventGroupsConfig) *bool { return &c.Commands }),
		group("Idle", "output idle", "notification.events.idle",
			func(c *config.EventGroupsConfig) *bool { return &c.Idle }),
	}
}

// notifyChromeRows is every row the screen spends outside the row list: the
// rounded border (2), dialogBorder's Padding(1,2) top and bottom (2), the
// title, the "changes persist" subtitle, the blank line under it, the blank
// line above the footer, the two footer lines, and one spare so the centred box
// never sits flush against the terminal edge.
const notifyChromeRows = 11

// notifyMinRows is 1 for the reason historyMinRows is: lipgloss.Place does not
// clip, so any floor above the height actually available MANUFACTURES the
// overflow it looks like it prevents.
const notifyMinRows = 1

// notifyVisibleRows is how many row lines the list may draw.
func (m Model) notifyVisibleRows() int {
	if n := m.height - notifyChromeRows; n > notifyMinRows {
		return n
	}
	return notifyMinRows
}

// firstNotifyRow is the index the cursor starts on: the first non-heading row.
func firstNotifyRow(rows []notifyToggle) int {
	for i, r := range rows {
		if !r.heading {
			return i
		}
	}
	return 0
}

// settingsSubmenuIndex is where Esc puts the cursor on the way back — the
// Settings row that opens this screen, found by its flag rather than by its
// label or a literal index, so inserting a row above it cannot silently send
// the user somewhere else.
func settingsSubmenuIndex() int {
	for i, f := range settingsFields() {
		if f.submenu {
			return i
		}
	}
	return 0
}

// syncNotifyScroll keeps the cursor inside the drawn window after a move.
//
// A pointer receiver, and the scroll is stored on the Model rather than
// recomputed: historyWindow re-derives the origin from the cursor on every
// render, so this only has to carry the user's position between frames.
func (m *Model) syncNotifyScroll(total int) {
	start, _ := historyWindow(total, m.dialogCursor, m.notifyScroll, m.notifyVisibleRows())
	m.notifyScroll = start
}

// handleNotifySettingsKey drives the screen.
func (m Model) handleNotifySettingsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	rows := notifySettingsRows()
	switch msg.String() {
	case "esc":
		m.dialog = dialogSettings
		// Back to the row that opened this screen, not to the top. Esc is
		// back-navigation, and dropping the user at "Snapshot interval" makes
		// them hunt for where they were.
		m.dialogCursor = settingsSubmenuIndex()
		// Both boxes are centred by lipgloss.Place and this one is markedly
		// taller, so the tall box's own rows land on cells Bubble Tea's diff
		// considers unchanged — the debris stays painted until something else
		// forces a full frame. Same reason handleShortcutsKey clears.
		return m, tea.ClearScreen
	case "up", "k":
		for i := m.dialogCursor - 1; i >= 0; i-- {
			if !rows[i].heading {
				m.dialogCursor = i
				break
			}
		}
		m.syncNotifyScroll(len(rows))
	case "down", "j":
		for i := m.dialogCursor + 1; i < len(rows); i++ {
			if !rows[i].heading {
				m.dialogCursor = i
				break
			}
		}
		m.syncNotifyScroll(len(rows))
	case "enter", " ":
		if m.dialogCursor >= 0 && m.dialogCursor < len(rows) && !rows[m.dialogCursor].heading {
			rows[m.dialogCursor].set(&m)
		}
	}
	return m, nil
}

// renderNotifySettingsDialog paints the screen at the standard dialog width.
func (m Model) renderNotifySettingsDialog() string {
	var b strings.Builder
	b.WriteString(dialogTitle.Render("Notifications"))
	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  changes persist to config.toml"))
	b.WriteString("\n")

	// One line per row, hint INLINE, and the list is WINDOWED.
	//
	// Both are required. renderDialog clamps width but never height, and
	// lipgloss.Place does not clip, so a box taller than the terminal is drawn
	// straight off the bottom edge with no scroll to recover it — the cursor
	// then moves into rows nobody can see. A hint on its own line put the
	// screen at 35 content rows; inline it is 15, which still needs a 26-row
	// terminal, and 24 is an ordinary size.
	//
	// historyWindow is the shared shape: pure, and called by the renderer
	// rather than depending on Update having run, because a WindowSizeMsg can
	// change the row budget between them.
	inner := dialogInnerWidth(m.width, dialogWidth)
	rows := notifySettingsRows()
	start, end := historyWindow(len(rows), m.dialogCursor, m.notifyScroll, m.notifyVisibleRows())
	for i := start; i < end; i++ {
		r := rows[i]
		if r.heading {
			// No leading blank inside a window: it is a row of the budget like
			// any other, and spending one on whitespace at the top of a short
			// terminal costs a toggle the user could otherwise see.
			b.WriteString("  " + dialogTitle.Render(r.label) + "\n")
			continue
		}
		cursor := "    "
		labelStyle := dialogLabelStyle
		if i == m.dialogCursor {
			cursor = "  > "
			labelStyle = labelStyle.Foreground(lipgloss.Color("230")).Bold(true)
		}
		row := cursor + labelStyle.Render(r.label) + dialogValStyle.Render(r.get(&m))
		if r.hint != "" {
			// Budgeted against what the row has ALREADY spent, because the
			// value column is not fixed: "on (run notify setup)" is 21 cells
			// where "on" is 2. The hint is the part that gives way.
			if room := inner - lipgloss.Width(row) - 2; room > 3 {
				row += "  " + dialogSubtle.Render(truncateRunes(r.hint, room))
			}
		}
		b.WriteString(row + "\n")
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  Hidden events still reach MCP agents"))
	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  ↑↓ navigate  Enter toggle  Esc back"))
	return b.String()
}
