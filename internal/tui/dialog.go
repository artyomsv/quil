package tui

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"

	"github.com/artyomsv/aethel/internal/config"
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
	case dialogInstanceForm:
		return m.handleInstanceFormKey(msg)
	case dialogPlugins:
		return m.handlePluginsKey(msg)
	case dialogTOMLEditor:
		return m.handleTOMLEditorKey(msg)
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
		if m.dialogCursor < 2 {
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
		case 2:
			m.dialog = dialogPlugins
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
		// Return to appropriate dialog based on confirm kind
		if m.confirmKind == "instance" {
			m.dialog = dialogCreatePane
			m.createPaneStep = 2
			m.dialogCursor = 0
			return m, nil
		}
		m.dialog = dialogNone
		return m, nil
	case "enter", "y":
		kind := m.confirmKind
		id := m.confirmID

		// Handle instance deletion locally (no IPC needed)
		if kind == "instance" {
			pluginName := m.selectedPlugin
			instances := m.instanceStore[pluginName]
			for i, inst := range instances {
				if inst.ID == id {
					m.instanceStore[pluginName] = append(instances[:i], instances[i+1:]...)
					break
				}
			}
			if err := SaveInstances(config.InstancesPath(), m.instanceStore); err != nil {
			log.Printf("save instances: %v", err)
		}
			m.dialog = dialogCreatePane
			m.createPaneStep = 2
			m.dialogCursor = 0
			return m, nil
		}

		m.dialog = dialogNone
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
	case dialogInstanceForm:
		content = m.renderInstanceFormDialog()
	case dialogPlugins:
		content = m.renderPluginsDialog()
	case dialogTOMLEditor:
		// Rendered in View() as full-screen, not here
	}

	// Use wider dialog for TOML editor, plugin-specific width, or default
	width := dialogWidth
	if m.dialog == dialogTOMLEditor {
		width = 74
	} else if m.selectedPlugin != "" {
		if p := m.pluginRegistry.Get(m.selectedPlugin); p != nil && p.Display.DialogWidth > 0 {
			width = p.Display.DialogWidth
		}
	}

	box := dialogBorder.Width(width).Render(content)
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

	items := []string{"Settings", "Shortcuts", "Plugins"}
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
		return "yes"
	}
	return "no"
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
			// Go back one step; skip instance list if plugin has no form fields
			m.createPaneStep--
			if m.createPaneStep == 2 {
				p := m.pluginRegistry.Get(m.selectedPlugin)
				if p == nil || len(p.Command.FormFields) == 0 {
					m.createPaneStep = 1 // skip instance step
				}
			}
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

	case "delete", "backspace":
		// Delete saved instance from the list (step 2, cursor > 0)
		if m.createPaneStep == 2 && m.dialogCursor > 0 {
			instances := m.instanceStore[m.selectedPlugin]
			idx := m.dialogCursor - 1
			if idx < len(instances) {
				m.confirmKind = "instance"
				m.confirmID = instances[idx].ID
				m.confirmName = instances[idx].Name
				m.dialog = dialogConfirm
			}
			return m, nil
		}
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
	case 2: // instance list
		return 1 + len(m.instanceStore[m.selectedPlugin]) // "+ New" + saved
	case 3: // split direction
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
		// Selected a plugin — check if it has form fields (instance management)
		if m.selectedCategory >= len(cats) {
			return m, nil
		}
		plugins := cats[m.selectedCategory].plugins
		if m.dialogCursor >= len(plugins) {
			return m, nil
		}
		m.selectedPlugin = plugins[m.dialogCursor].Name
		m.selectedInstanceArgs = nil
		m.selectedInstanceName = ""
		m.dialogCursor = 0

		// If plugin has form fields → instance list (step 2)
		p := m.pluginRegistry.Get(m.selectedPlugin)
		if p != nil && len(p.Command.FormFields) > 0 {
			// If no saved instances, jump directly to the form
			if len(m.instanceStore[m.selectedPlugin]) == 0 {
				m.openInstanceForm(p)
				return m, nil
			}
			m.createPaneStep = 2
		} else {
			m.createPaneStep = 3 // skip instance list
		}
		return m, nil
	}

	if m.createPaneStep == 2 {
		// Selected from instance list
		instances := m.instanceStore[m.selectedPlugin]
		if m.dialogCursor == 0 {
			// "+ New" — open instance form
			p := m.pluginRegistry.Get(m.selectedPlugin)
			if p != nil {
				m.openInstanceForm(p)
			}
			return m, nil
		}
		// Select existing instance
		idx := m.dialogCursor - 1
		if idx < len(instances) {
			inst := instances[idx]
			p := m.pluginRegistry.Get(m.selectedPlugin)
			if p != nil {
				m.selectedInstanceArgs = BuildArgs(p.Command.ArgTemplate, inst.Fields)
			}
			m.selectedInstanceName = inst.Name
		}
		m.createPaneStep = 3
		m.dialogCursor = 0
		return m, nil
	}

	// Step 3: selected placement (split direction)
	return m.handleCreatePaneSplit()
}

// openInstanceForm initializes the instance form dialog with default values.
func (m *Model) openInstanceForm(p *plugin.PanePlugin) {
	m.instanceFormValues = make([]string, len(p.Command.FormFields))
	for i, ff := range p.Command.FormFields {
		m.instanceFormValues[i] = ff.Default
	}
	m.instanceFormCursor = 0
	m.dialogEdit = false
	m.dialogInput = ""
	m.dialog = dialogInstanceForm
}

// handleCreatePaneSplit handles the final split direction selection (step 3).
func (m Model) handleCreatePaneSplit() (tea.Model, tea.Cmd) {
	pluginName := m.selectedPlugin
	instanceName := m.selectedInstanceName
	instanceArgs := m.selectedInstanceArgs
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
				InstanceName:  instanceName,
				InstanceArgs:  instanceArgs,
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
			TabID:        tabID,
			Type:         pluginName,
			InstanceName: instanceName,
			InstanceArgs: instanceArgs,
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
		// Step 2: Instance list
		p := m.pluginRegistry.Get(m.selectedPlugin)
		title := "Instances"
		if p != nil {
			title = p.DisplayName
		}
		b.WriteString(dialogTitle.Render(title))
		b.WriteString("\n\n")

		instances := m.instanceStore[m.selectedPlugin]

		// First item: "+ New"
		cursor := "  "
		style := dialogNormal
		if m.dialogCursor == 0 {
			cursor = "> "
			style = dialogSelected
		}
		b.WriteString(cursor + style.Render("+ New Connection") + "\n")

		// Saved instances
		for i, inst := range instances {
			cursor = "  "
			style = dialogNormal
			if i+1 == m.dialogCursor {
				cursor = "> "
				style = dialogSelected
			}
			line := style.Render(inst.Name)
			if addr := inst.DisplayAddr(); addr != "" {
				line += "  " + dialogSubtle.Render(addr)
			}
			if inst.Description != "" {
				line += "  " + dialogSubtle.Render(inst.Description)
			}
			b.WriteString(cursor + line + "\n")
		}

		b.WriteByte('\n')
		b.WriteString(dialogSubtle.Render("Enter select  Del remove  Esc back"))

	case 3:
		// Step 3: Select split direction
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

// --- Instance form dialog ---

func (m Model) handleInstanceFormKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.pluginRegistry.Get(m.selectedPlugin)
	if p == nil {
		m.dialog = dialogNone
		return m, nil
	}
	fields := p.Command.FormFields
	totalItems := len(fields) + 1 // fields + "Create" button
	key := msg.String()

	if m.dialogEdit {
		switch key {
		case "esc":
			m.dialogEdit = false
			m.dialogInput = ""
		case "enter":
			if m.instanceFormCursor < len(fields) {
				m.instanceFormValues[m.instanceFormCursor] = m.dialogInput
			}
			m.dialogEdit = false
			m.dialogInput = ""
		case "backspace":
			if len(m.dialogInput) > 0 {
				m.dialogInput = m.dialogInput[:len(m.dialogInput)-1]
			}
		case "tab":
			// Commit and advance
			if m.instanceFormCursor < len(fields) {
				m.instanceFormValues[m.instanceFormCursor] = m.dialogInput
			}
			m.dialogEdit = false
			m.dialogInput = ""
			if m.instanceFormCursor < totalItems-1 {
				m.instanceFormCursor++
			}
		default:
			if len(key) == 1 {
				m.dialogInput += key
			} else if key == "space" {
				m.dialogInput += " "
			}
		}
		return m, nil
	}

	switch key {
	case "esc":
		// Return to instance list or plugin selection
		m.dialog = dialogCreatePane
		if len(m.instanceStore[m.selectedPlugin]) > 0 {
			m.createPaneStep = 2
		} else {
			m.createPaneStep = 1
		}
		m.dialogCursor = 0
		return m, nil

	case "up", "k":
		if m.instanceFormCursor > 0 {
			m.instanceFormCursor--
		}
		return m, nil

	case "down", "j":
		if m.instanceFormCursor < totalItems-1 {
			m.instanceFormCursor++
		}
		return m, nil

	case "tab":
		m.instanceFormCursor = (m.instanceFormCursor + 1) % totalItems
		return m, nil

	case "enter":
		if m.instanceFormCursor < len(fields) {
			// Start editing this field
			m.dialogEdit = true
			m.dialogInput = m.instanceFormValues[m.instanceFormCursor]
			return m, nil
		}
		// "Create" button — validate and save
		return m.submitInstanceForm(p)
	}

	return m, nil
}

func (m Model) submitInstanceForm(p *plugin.PanePlugin) (tea.Model, tea.Cmd) {
	fields := p.Command.FormFields

	// Validate required fields
	fieldMap := make(map[string]string)
	for i, ff := range fields {
		val := m.instanceFormValues[i]
		if ff.Required && val == "" {
			// Move cursor to the first empty required field
			m.instanceFormCursor = i
			return m, nil
		}
		fieldMap[ff.Name] = val
	}

	// Create saved instance
	name := fieldMap["name"]
	if name == "" {
		name = "unnamed"
	}
	desc := fieldMap["description"]

	inst := SavedInstance{
		ID:          uuid.New().String()[:8],
		Name:        name,
		Fields:      fieldMap,
		Description: desc,
	}

	// Save to store
	if m.instanceStore == nil {
		m.instanceStore = make(InstanceStore)
	}
	m.instanceStore[m.selectedPlugin] = append(m.instanceStore[m.selectedPlugin], inst)
	if err := SaveInstances(config.InstancesPath(), m.instanceStore); err != nil {
			log.Printf("save instances: %v", err)
		}

	// Build args from template
	m.selectedInstanceArgs = BuildArgs(p.Command.ArgTemplate, fieldMap)
	m.selectedInstanceName = name

	// Proceed to split direction
	m.dialog = dialogCreatePane
	m.createPaneStep = 3
	m.dialogCursor = 0
	return m, nil
}

func (m Model) renderInstanceFormDialog() string {
	var b strings.Builder

	p := m.pluginRegistry.Get(m.selectedPlugin)
	if p == nil {
		return ""
	}
	fields := p.Command.FormFields

	title := "New " + p.DisplayName
	b.WriteString(dialogTitle.Render(title))
	b.WriteString("\n\n")

	for i, ff := range fields {
		cursor := "  "
		labelStyle := dialogLabelStyle
		if i == m.instanceFormCursor {
			cursor = "> "
			labelStyle = labelStyle.Foreground(lipgloss.Color("230")).Bold(true)
		}

		val := m.instanceFormValues[i]
		var valRendered string
		if m.dialogEdit && i == m.instanceFormCursor {
			valRendered = dialogEditStyle.Render(m.dialogInput + "▎")
		} else if val != "" {
			valRendered = dialogValStyle.Render(val)
		} else {
			valRendered = dialogSubtle.Render("—")
		}

		label := ff.Label
		if ff.Required {
			label += "*"
		}

		b.WriteString(cursor + labelStyle.Render(label) + valRendered + "\n")
	}

	// "Create" button
	b.WriteByte('\n')
	btnCursor := "  "
	btnStyle := dialogNormal
	if m.instanceFormCursor == len(fields) {
		btnCursor = "> "
		btnStyle = dialogSelected
	}
	b.WriteString(btnCursor + btnStyle.Render("[Create]") + "\n")

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("Tab next  Enter edit/confirm  Esc back"))

	return b.String()
}

// --- Plugins management dialog ---

func (m Model) handlePluginsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	allPlugins := m.sortedPlugins()
	totalItems := len(allPlugins) + 2 // plugins + Reload + Reset

	switch msg.String() {
	case "esc":
		m.dialog = dialogAbout
		m.dialogCursor = 2
		return m, nil

	case "up", "k":
		if m.dialogCursor > 0 {
			m.dialogCursor--
		}
		return m, nil

	case "down", "j":
		if m.dialogCursor < totalItems-1 {
			m.dialogCursor++
		}
		return m, nil

	case "enter":
		if m.dialogCursor < len(allPlugins) {
			// Open TOML editor for selected plugin
			p := allPlugins[m.dialogCursor]
			if p.Name == "terminal" {
				// Terminal is built-in Go, no TOML to edit
				return m, nil
			}
			filePath := filepath.Join(config.PluginsDir(), p.Name+".toml")
			data, err := os.ReadFile(filePath)
			if err != nil {
				return m, nil
			}
			// Calculate editor viewport from available space
			viewH := m.height - 10 // title + footer + borders + padding
			if viewH < 5 {
				viewH = 5
			}
			viewW := 70
			m.tomlEditor = NewTextEditor(string(data), filePath, viewW, viewH)
			m.dialog = dialogTOMLEditor
			return m, nil
		}

		btnIdx := m.dialogCursor - len(allPlugins)
		if btnIdx == 1 {
			plugin.EnsureDefaultPlugins(config.PluginsDir())
		}
		// Both buttons: reload plugins
		if err := m.pluginRegistry.LoadFromDir(config.PluginsDir()); err != nil {
			log.Printf("reload plugins: %v", err)
		}
		m.pluginRegistry.DetectAvailability()
		client := m.client
		m.dialog = dialogNone
		return m, func() tea.Msg {
			msg, _ := ipc.NewMessage(ipc.MsgReloadPlugins, nil)
			client.Send(msg)
			return nil
		}
	}

	return m, nil
}

// sortedPlugins returns all plugins sorted by display name.
func (m Model) sortedPlugins() []*plugin.PanePlugin {
	all := m.pluginRegistry.All()
	sort.Slice(all, func(i, j int) bool {
		return all[i].DisplayName < all[j].DisplayName
	})
	return all
}

func (m Model) renderPluginsDialog() string {
	var b strings.Builder

	b.WriteString(dialogTitle.Render("Plugins"))
	b.WriteString("\n\n")

	allPlugins := m.sortedPlugins()

	// Plugin list (selectable — Enter opens editor)
	for i, p := range allPlugins {
		cursor := "  "
		style := dialogNormal
		if i == m.dialogCursor {
			cursor = "> "
			style = dialogSelected
		}

		avail := dialogSubtle.Render("[x]")
		if p.Available {
			avail = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render("[ok]")
		}

		name := style.Render(p.DisplayName)
		cat := dialogSubtle.Render(p.Category)
		label := ""
		if p.Name == "terminal" {
			label = dialogSubtle.Render("  (built-in)")
		}
		b.WriteString(fmt.Sprintf("%s%s  %s  %s%s\n", cursor, name, cat, avail, label))
	}

	// Action buttons
	b.WriteByte('\n')
	btnLabels := []string{"Reload Plugins", "Restore Missing Defaults"}
	for i, label := range btnLabels {
		btnIdx := len(allPlugins) + i
		cursor := "  "
		style := dialogNormal
		if btnIdx == m.dialogCursor {
			cursor = "> "
			style = dialogSelected
		}
		b.WriteString(cursor + style.Render(label) + "\n")
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("Enter edit/action  Esc back"))

	return b.String()
}

// --- TOML editor dialog ---

func (m Model) handleTOMLEditorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.tomlEditor == nil {
		m.dialog = dialogPlugins
		return m, nil
	}

	saved, closed := m.tomlEditor.HandleKey(msg.String())

	if saved {
		// Reload plugins after save, re-enable mouse
		if err := m.pluginRegistry.LoadFromDir(config.PluginsDir()); err != nil {
			log.Printf("reload plugins: %v", err)
		}
		m.pluginRegistry.DetectAvailability()
		m.tomlEditor = nil
		m.dialog = dialogPlugins
		m.dialogCursor = 0
		client := m.client
		return m, tea.Batch(func() tea.Msg {
			msg, _ := ipc.NewMessage(ipc.MsgReloadPlugins, nil)
			client.Send(msg)
			return nil
		})
	}

	if closed {
		m.tomlEditor = nil
		m.dialog = dialogPlugins
		return m, nil
	}

	return m, nil
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
