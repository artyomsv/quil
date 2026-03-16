package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/artyomsv/aethel/internal/ipc"
	"github.com/artyomsv/aethel/internal/plugin"
)

const dialogWidth = 50

var (
	dialogBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63")).
			Padding(1, 2)

	dialogTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("230"))

	dialogSubtle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	dialogSelected = lipgloss.NewStyle().
			Foreground(lipgloss.Color("230")).
			Bold(true)

	dialogNormal = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250"))

	dialogKeyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("63")).
			Width(16)

	dialogValStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250"))

	dialogEditStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("238"))

	dialogLabelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("250")).
				Width(24)
)

// settingsField describes one editable config field.
type settingsField struct {
	label string
	get   func(m *Model) string
	set   func(m *Model, val string)
	isBool bool
}

func settingsFields() []settingsField {
	return []settingsField{
		{
			label: "Snapshot interval",
			get:   func(m *Model) string { return m.cfg.Daemon.SnapshotInterval },
			set: func(m *Model, v string) {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				m.cfg.Daemon.SnapshotInterval = v
			}
		},
		},
		{
			label:  "Ghost dimmed",
			get:    func(m *Model) string { return boolStr(m.cfg.GhostBuffer.Dimmed) },
			set:    func(m *Model, _ string) { m.cfg.GhostBuffer.Dimmed = !m.cfg.GhostBuffer.Dimmed },
			isBool: true,
		},
		{
			label: "Ghost buffer lines",
			get:   func(m *Model) string { return strconv.Itoa(m.cfg.GhostBuffer.MaxLines) },
			set: func(m *Model, v string) {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					m.cfg.GhostBuffer.MaxLines = n
				}
			},
		},
		{
			label: "Mouse scroll lines",
			get:   func(m *Model) string { return strconv.Itoa(m.cfg.UI.MouseScrollLines) },
			set: func(m *Model, v string) {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					m.cfg.UI.MouseScrollLines = n
				}
			},
		},
		{
			label: "Page scroll lines",
			get:   func(m *Model) string { return strconv.Itoa(m.cfg.UI.PageScrollLines) },
			set: func(m *Model, v string) {
				if n, err := strconv.Atoi(v); err == nil && n >= 0 {
					m.cfg.UI.PageScrollLines = n
				}
			},
		},
		{
			label: "Log level",
			get:   func(m *Model) string { return m.cfg.Logging.Level },
			set: func(m *Model, v string) {
			switch v {
			case "debug", "info", "warn", "error":
				m.cfg.Logging.Level = v
			}
		},
		},
	}
}

func shortcutsList(m *Model) []struct{ key, desc string } {
	kb := m.cfg.Keybindings
	return []struct{ key, desc string }{
		{kb.Quit, "Quit"},
		{kb.NewTab, "New tab"},
		{kb.ClosePane, "Close pane"},
		{kb.CloseTab, "Close tab"},
		{kb.SplitHorizontal, "Split horizontal"},
		{kb.SplitVertical, "Split vertical"},
		{kb.NextPane, "Next pane"},
		{kb.PrevPane, "Previous pane"},
		{kb.RenameTab, "Rename tab"},
		{kb.RenamePane, "Rename pane"},
		{kb.CycleTabColor, "Cycle tab color"},
		{kb.ScrollPageUp, "Scroll page up"},
		{kb.ScrollPageDown, "Scroll page down"},
		{kb.Paste, "Paste clipboard"},
		{"Ctrl+N", "New typed pane"},
		{"Alt+1..9", "Switch to tab N"},
		{"F1", "Help / About"},
	}
}

// --- Input handling ---

func (m Model) handleDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.dialog {
	case dialogAbout:
		return m.handleAboutKey(msg)
	case dialogSettings:
		return m.handleSettingsKey(msg)
	case dialogShortcuts:
		return m.handleShortcutsKey(msg)
	case dialogConfirm:
		return m.handleConfirmKey(msg)
	case dialogCreatePane:
		return m.handleCreatePaneKey(msg)
	case dialogPluginError:
		return m.handlePluginErrorKey(msg)
	}
	return m, nil
}

func (m Model) handleAboutKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.dialog = dialogNone
	case "up", "k":
		if m.dialogCursor > 0 {
			m.dialogCursor--
		}
	case "down", "j":
		if m.dialogCursor < 1 {
			m.dialogCursor++
		}
	case "enter":
		switch m.dialogCursor {
		case 0:
			m.dialog = dialogSettings
			m.dialogCursor = 0
		case 1:
			m.dialog = dialogShortcuts
			m.dialogCursor = 0
		}
	}
	return m, nil
}

func (m Model) handleSettingsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	fields := settingsFields()
	key := msg.String()

	if m.dialogEdit {
		switch key {
		case "esc":
			m.dialogEdit = false
			m.dialogInput = ""
		case "enter":
			fields[m.dialogCursor].set(&m, m.dialogInput)
			m.dialogEdit = false
			m.dialogInput = ""
		case "backspace":
			if len(m.dialogInput) > 0 {
				m.dialogInput = m.dialogInput[:len(m.dialogInput)-1]
			}
		default:
			if len(key) == 1 {
				m.dialogInput += key
			}
		}
		return m, nil
	}

	switch key {
	case "esc":
		m.dialog = dialogAbout
		m.dialogCursor = 0
	case "up", "k":
		if m.dialogCursor > 0 {
			m.dialogCursor--
		}
	case "down", "j":
		if m.dialogCursor < len(fields)-1 {
			m.dialogCursor++
		}
	case "enter", " ":
		f := fields[m.dialogCursor]
		if f.isBool {
			f.set(&m, "")
		} else {
			m.dialogEdit = true
			m.dialogInput = f.get(&m)
		}
	}
	return m, nil
}

func (m Model) handleShortcutsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.dialog = dialogAbout
		m.dialogCursor = 0
	}
	return m, nil
}

func (m Model) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "n":
		m.dialog = dialogNone
		return m, nil
	case "enter", "y":
		m.dialog = dialogNone
		kind := m.confirmKind
		id := m.confirmID
		client := m.client
		return m, func() tea.Msg {
			switch kind {
			case "pane":
				req, _ := ipc.NewMessage(ipc.MsgDestroyPane, ipc.DestroyPanePayload{
					PaneID: id,
				})
				client.Send(req)
			case "tab":
				req, _ := ipc.NewMessage(ipc.MsgDestroyTab, ipc.DestroyTabPayload{
					TabID: id,
				})
				client.Send(req)
			}
			return nil
		}
	}
	return m, nil
}

// --- Rendering ---

func (m Model) renderDialog() string {
	var content string

	switch m.dialog {
	case dialogAbout:
		content = m.renderAboutDialog()
	case dialogSettings:
		content = m.renderSettingsDialog()
	case dialogShortcuts:
		content = m.renderShortcutsDialog()
	case dialogConfirm:
		content = m.renderConfirmDialog()
	case dialogCreatePane:
		content = m.renderCreatePaneDialog()
	case dialogPluginError:
		content = m.renderPluginErrorDialog()
	}

	box := dialogBorder.Width(dialogWidth).Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m Model) renderAboutDialog() string {
	var b strings.Builder

	title := dialogTitle.Render("Aethel v" + m.version)
	link := dialogSubtle.Render("github.com/artyomsv/aethel")

	b.WriteString(lipgloss.PlaceHorizontal(dialogWidth, lipgloss.Center, title))
	b.WriteByte('\n')
	b.WriteString(lipgloss.PlaceHorizontal(dialogWidth, lipgloss.Center, link))
	b.WriteString("\n\n")

	items := []string{"Settings", "Shortcuts"}
	for i, item := range items {
		cursor := "  "
		style := dialogNormal
		if i == m.dialogCursor {
			cursor = "> "
			style = dialogSelected
		}
		b.WriteString(cursor + style.Render(item) + "\n")
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("Esc close"))

	return b.String()
}

func (m Model) renderSettingsDialog() string {
	var b strings.Builder

	b.WriteString(dialogTitle.Render("Settings"))
	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  changes apply to this session only"))
	b.WriteString("\n\n")

	fields := settingsFields()
	for i, f := range fields {
		cursor := "  "
		labelStyle := dialogLabelStyle
		if i == m.dialogCursor {
			cursor = "> "
			labelStyle = labelStyle.Foreground(lipgloss.Color("230")).Bold(true)
		}

		val := f.get(&m)
		var valRendered string
		if m.dialogEdit && i == m.dialogCursor {
			valRendered = dialogEditStyle.Render(m.dialogInput + "▎")
		} else {
			valRendered = dialogValStyle.Render(val)
		}

		b.WriteString(cursor + labelStyle.Render(f.label) + valRendered + "\n")
	}

	b.WriteByte('\n')
	hint := "↑↓ navigate  Enter edit  Esc back"
	b.WriteString(dialogSubtle.Render(hint))

	return b.String()
}

func (m Model) renderShortcutsDialog() string {
	var b strings.Builder

	b.WriteString(dialogTitle.Render("Shortcuts"))
	b.WriteString("\n\n")

	for _, s := range shortcutsList(&m) {
		b.WriteString(fmt.Sprintf("  %s%s\n",
			dialogKeyStyle.Render(s.key),
			dialogValStyle.Render(s.desc)))
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("Esc back"))

	return b.String()
}

func (m Model) renderConfirmDialog() string {
	var b strings.Builder

	b.WriteString(dialogTitle.Render("Confirm"))
	b.WriteString("\n\n")

	label := fmt.Sprintf("Close %s %q?", m.confirmKind, m.confirmName)
	b.WriteString("  " + dialogNormal.Render(label))
	b.WriteString("\n\n")

	b.WriteString("  " + dialogSubtle.Render("Enter confirm    Esc cancel"))

	return b.String()
}

func boolStr(v bool) string {
	if v {
		return "✓"
	}
	return "✗"
}

// --- Pane creation dialog ---

// createPaneCategories returns the ordered list of categories with their plugins
// for the pane creation dialog.
func (m *Model) createPaneCategories() []struct {
	key     string
	label   string
	plugins []*plugin.PanePlugin
} {
	if m.pluginRegistry == nil {
		return nil
	}
	byCategory := m.pluginRegistry.AvailableByCategory()
	order := plugin.CategoryOrder()

	var result []struct {
		key     string
		label   string
		plugins []*plugin.PanePlugin
	}
	for _, cat := range order {
		plugins := byCategory[cat.Key]
		if len(plugins) == 0 {
			continue
		}
		// Sort plugins by display name for consistency
		sort.Slice(plugins, func(i, j int) bool {
			return plugins[i].DisplayName < plugins[j].DisplayName
		})
		result = append(result, struct {
			key     string
			label   string
			plugins []*plugin.PanePlugin
		}{cat.Key, cat.Label, plugins})
	}

	// Add any categories not in the standard order
	for cat, plugins := range byCategory {
		found := false
		for _, o := range order {
			if o.Key == cat {
				found = true
				break
			}
		}
		if !found {
			sort.Slice(plugins, func(i, j int) bool {
				return plugins[i].DisplayName < plugins[j].DisplayName
			})
			result = append(result, struct {
				key     string
				label   string
				plugins []*plugin.PanePlugin
			}{cat, cat, plugins})
		}
	}
	return result
}

func (m Model) handleCreatePaneKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch key {
	case "esc":
		if m.createPaneStep > 0 {
			// Go back one step
			m.createPaneStep--
			m.dialogCursor = 0
			return m, nil
		}
		m.dialog = dialogNone
		m.createPaneStep = 0
		return m, nil

	case "up", "k":
		if m.dialogCursor > 0 {
			m.dialogCursor--
		}
		return m, nil

	case "down", "j":
		items := m.createPaneItemCount()
		if m.dialogCursor < items-1 {
			m.dialogCursor++
		}
		return m, nil

	case "enter":
		return m.handleCreatePaneSelect()
	}

	return m, nil
}

func (m *Model) createPaneItemCount() int {
	cats := m.createPaneCategories()
	switch m.createPaneStep {
	case 0:
		return len(cats)
	case 1:
		if m.selectedCategory < len(cats) {
			return len(cats[m.selectedCategory].plugins)
		}
	case 2:
		return 3 // Horizontal, Vertical, Replace
	}
	return 0
}

func (m Model) handleCreatePaneSelect() (tea.Model, tea.Cmd) {
	cats := m.createPaneCategories()

	if m.createPaneStep == 0 {
		// Selected a category
		if m.dialogCursor < len(cats) {
			m.selectedCategory = m.dialogCursor
			m.createPaneStep = 1
			m.dialogCursor = 0
		}
		return m, nil
	}

	if m.createPaneStep == 1 {
		// Selected a plugin — advance to split direction
		if m.selectedCategory >= len(cats) {
			return m, nil
		}
		plugins := cats[m.selectedCategory].plugins
		if m.dialogCursor >= len(plugins) {
			return m, nil
		}
		m.selectedPlugin = plugins[m.dialogCursor].Name
		m.createPaneStep = 2
		m.dialogCursor = 0
		return m, nil
	}

	// Step 2: selected placement
	pluginName := m.selectedPlugin
	m.dialog = dialogNone
	m.createPaneStep = 0

	tab := m.activeTabModel()
	if tab == nil {
		return m, nil
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		return m, nil
	}

	tabID := tab.ID
	client := m.client

	// Option 2: Replace current pane
	if m.dialogCursor == 2 {
		oldPaneID := pane.ID

		// Turn the current leaf into a placeholder so the new pane
		// fills the same layout position.
		if leaf := tab.Root.FindLeaf(oldPaneID); leaf != nil {
			leaf.Pane = nil
			if m.pendingSplit == nil {
				m.pendingSplit = make(map[string]*LayoutNode)
			}
			m.pendingSplit[tab.ID] = leaf
		}

		return m, func() tea.Msg {
			msg, _ := ipc.NewMessage(ipc.MsgCreatePane, ipc.CreatePanePayload{
				TabID:         tabID,
				Type:          pluginName,
				ReplacePaneID: oldPaneID,
			})
			client.Send(msg)
			return nil
		}
	}

	// Options 0/1: Split horizontal or vertical
	var dir SplitDir
	if m.dialogCursor == 0 {
		dir = SplitHorizontal
	} else {
		dir = SplitVertical
	}

	placeholder := tab.SplitAtPane(pane.ID, dir)
	if placeholder == nil {
		return m, nil
	}

	if m.pendingSplit == nil {
		m.pendingSplit = make(map[string]*LayoutNode)
	}
	m.pendingSplit[tab.ID] = placeholder

	return m, func() tea.Msg {
		msg, _ := ipc.NewMessage(ipc.MsgCreatePane, ipc.CreatePanePayload{
			TabID: tabID,
			Type:  pluginName,
		})
		client.Send(msg)
		return nil
	}
}

func (m Model) renderCreatePaneDialog() string {
	var b strings.Builder

	cats := m.createPaneCategories()

	switch m.createPaneStep {
	case 0:
		// Step 0: Select category
		b.WriteString(dialogTitle.Render("New Pane"))
		b.WriteString("\n\n")

		for i, cat := range cats {
			cursor := "  "
			style := dialogNormal
			if i == m.dialogCursor {
				cursor = "> "
				style = dialogSelected
			}
			b.WriteString(cursor + style.Render(cat.label) + "\n")
		}

		if len(cats) == 0 {
			b.WriteString("  " + dialogSubtle.Render("No plugins available") + "\n")
		}

		b.WriteByte('\n')
		b.WriteString(dialogSubtle.Render("Esc cancel"))

	case 1:
		// Step 1: Select plugin within category
		if m.selectedCategory < len(cats) {
			cat := cats[m.selectedCategory]
			b.WriteString(dialogTitle.Render(cat.label))
			b.WriteString("\n\n")

			for i, p := range cat.plugins {
				cursor := "  "
				style := dialogNormal
				if i == m.dialogCursor {
					cursor = "> "
					style = dialogSelected
				}
				line := style.Render(p.DisplayName)
				if p.Description != "" {
					line += "  " + dialogSubtle.Render(p.Description)
				}
				b.WriteString(cursor + line + "\n")
			}

			b.WriteByte('\n')
			b.WriteString(dialogSubtle.Render("Esc back"))
		}

	case 2:
		// Step 2: Select split direction
		b.WriteString(dialogTitle.Render("Split Direction"))
		b.WriteString("\n\n")

		dirs := []string{"Horizontal  (left | right)", "Vertical    (top / bottom)", "Replace current pane"}
		for i, d := range dirs {
			cursor := "  "
			style := dialogNormal
			if i == m.dialogCursor {
				cursor = "> "
				style = dialogSelected
			}
			b.WriteString(cursor + style.Render(d) + "\n")
		}

		b.WriteByte('\n')
		b.WriteString(dialogSubtle.Render("Esc back"))
	}

	return b.String()
}

// --- Plugin error dialog ---

func (m Model) handlePluginErrorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "enter":
		m.dialog = dialogNone
		return m, nil
	}
	return m, nil
}

func (m Model) renderPluginErrorDialog() string {
	var b strings.Builder

	b.WriteString(dialogTitle.Render(m.pluginErrorTitle))
	b.WriteString("\n\n")

	// Render multi-line message
	lines := strings.Split(m.pluginErrorMessage, "\n")
	for _, line := range lines {
		b.WriteString("  " + dialogNormal.Render(line) + "\n")
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("Enter/Esc dismiss"))

	return b.String()
}
