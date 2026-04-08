package tui

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/google/uuid"

	"github.com/artyomsv/quil/internal/clipboard"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

const dialogWidth = 50
const disclaimerWidth = 60

// disclaimerTips are shown randomly in the startup disclaimer dialog.
// Each body line is rendered as a bullet point.
var disclaimerTips = []struct {
	title string
	items []string
}{
	{"Quick Navigation", []string{
		"Ctrl+Arrow jumps words",
		"Ctrl+Alt+Arrow jumps 3 words",
		"Ctrl+Up/Down jumps paragraphs",
	}},
	{"Split Panes", []string{
		"Alt+H splits horizontal",
		"Alt+V splits vertical",
		"Tab/Shift+Tab cycles between panes",
	}},
	{"Typed Panes", []string{
		"Ctrl+N creates typed panes (SSH, Claude)",
		"Plugins configurable via F1 > Plugins",
	}},
	{"Session Persistence", []string{
		"Workspace survives reboots",
		"Tabs, panes, and history auto-restored",
	}},
	{"Pane Management", []string{
		"F2 renames tabs, Alt+F2 renames panes",
		"Ctrl+W closes pane, Alt+W closes tab",
		"Ctrl+E toggles focus mode",
	}},
	{"Text Selection", []string{
		"Shift+Arrows selects text",
		"Enter copies selection to clipboard",
		"Ctrl+V pastes from clipboard",
	}},
	{"Customization", []string{
		"Edit ~/.quil/config.toml for settings",
		"All keybindings are configurable",
		"Press F1 for help and shortcuts",
	}},
}

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

	dialogErrorStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("196")).
				Bold(true)
)

// settingsField describes one editable config field.
type settingsField struct {
	label  string
	get    func(m *Model) string
	set    func(m *Model, val string)
	isBool bool
}

// settingsFields returns the editable Settings rows. Every setter that
// actually mutates persistent state must set m.configChanged = true so
// the change is written to ~/.quil/config.toml when the TUI exits — without
// it, edits are silently lost. Live re-application (e.g. switching the log
// level for the running process) is intentionally NOT done here: changes
// take effect on the next launch.
func settingsFields() []settingsField {
	return []settingsField{
		{
			label: "Snapshot interval",
			get:   func(m *Model) string { return m.cfg.Daemon.SnapshotInterval },
			set: func(m *Model, v string) {
				if d, err := time.ParseDuration(v); err == nil && d > 0 && m.cfg.Daemon.SnapshotInterval != v {
					m.cfg.Daemon.SnapshotInterval = v
					m.configChanged = true
				}
			},
		},
		{
			label: "Ghost dimmed",
			get:   func(m *Model) string { return boolStr(m.cfg.GhostBuffer.Dimmed) },
			set: func(m *Model, _ string) {
				m.cfg.GhostBuffer.Dimmed = !m.cfg.GhostBuffer.Dimmed
				m.configChanged = true
			},
			isBool: true,
		},
		{
			label: "Ghost buffer lines",
			get:   func(m *Model) string { return strconv.Itoa(m.cfg.GhostBuffer.MaxLines) },
			set: func(m *Model, v string) {
				if n, err := strconv.Atoi(v); err == nil && n > 0 && m.cfg.GhostBuffer.MaxLines != n {
					m.cfg.GhostBuffer.MaxLines = n
					m.configChanged = true
				}
			},
		},
		{
			label: "Mouse scroll lines",
			get:   func(m *Model) string { return strconv.Itoa(m.cfg.UI.MouseScrollLines) },
			set: func(m *Model, v string) {
				if n, err := strconv.Atoi(v); err == nil && n > 0 && m.cfg.UI.MouseScrollLines != n {
					m.cfg.UI.MouseScrollLines = n
					m.configChanged = true
				}
			},
		},
		{
			label: "Page scroll lines",
			get:   func(m *Model) string { return strconv.Itoa(m.cfg.UI.PageScrollLines) },
			set: func(m *Model, v string) {
				if n, err := strconv.Atoi(v); err == nil && n >= 0 && m.cfg.UI.PageScrollLines != n {
					m.cfg.UI.PageScrollLines = n
					m.configChanged = true
				}
			},
		},
		{
			label: "Log level",
			get:   func(m *Model) string { return m.cfg.Logging.Level },
			set: func(m *Model, v string) {
				switch v {
				case "debug", "info", "warn", "error":
					if m.cfg.Logging.Level != v {
						m.cfg.Logging.Level = v
						m.configChanged = true
					}
				}
			},
		},
		{
			label: "Show disclaimer",
			get:   func(m *Model) string { return boolStr(m.cfg.UI.ShowDisclaimer) },
			set: func(m *Model, _ string) {
				m.cfg.UI.ShowDisclaimer = !m.cfg.UI.ShowDisclaimer
				m.configChanged = true
			},
			isBool: true,
		},
	}
}

func shortcutsList(m *Model) []struct{ key, desc string } {
	kb := m.cfg.Keybindings
	list := []struct{ key, desc string }{
		{kb.Quit, "Quit"},
		{kb.NewTab, "New tab"},
		{kb.ClosePane, "Close pane"},
		{kb.CloseTab, "Close tab"},
		{kb.SplitHorizontal, "Split side-by-side"},
		{kb.SplitVertical, "Split top/bottom"},
		{kb.PaneLeft, "Focus pane left"},
		{kb.PaneRight, "Focus pane right"},
		{kb.PaneUp, "Focus pane up"},
		{kb.PaneDown, "Focus pane down"},
	}
	// Legacy linear pane cycling (unbound by default — hide when empty).
	if kb.NextPane != "" {
		list = append(list, struct{ key, desc string }{kb.NextPane, "Next pane"})
	}
	if kb.PrevPane != "" {
		list = append(list, struct{ key, desc string }{kb.PrevPane, "Previous pane"})
	}
	list = append(list, []struct{ key, desc string }{
		{kb.RenameTab, "Rename tab"},
		{kb.RenamePane, "Rename pane"},
		{kb.CycleTabColor, "Cycle tab color"},
		{kb.ScrollPageUp, "Scroll page up"},
		{kb.ScrollPageDown, "Scroll page down"},
		{kb.Paste, "Paste clipboard"},
		{kb.FocusPane, "Toggle focus mode"},
		{kb.NotesToggle, "Toggle pane notes"},
		{"Ctrl+N", "New typed pane"},
		{"Alt+1..9", "Switch to tab N"},
		{"F1", "Help / About"},
		{"Tab / Shift+Tab", "→ PTY (shell completion, Claude Code modes)"},
		{"Shift+Arrows", "Select text"},
		{"Ctrl+Shift+←→", "Select word"},
		{"Ctrl+Alt+Shift+←→", "Select 3 words"},
		{"Ctrl+←→", "Jump word"},
		{"Ctrl+Alt+←→", "Jump 3 words"},
		{"Enter", "Copy selection"},
		{"Right-click", "Copy selection"},
		{"Esc", "Clear selection"},
		{"", ""},
		{"", "── Editor ──"},
		{"Shift+Arrows", "Select text (editor)"},
		{"Ctrl+Shift+←→", "Select word (editor)"},
		{"Ctrl+Alt+Shift+←→", "Select 3 words (editor)"},
		{"Enter", "Copy selection (editor)"},
		{"Ctrl+X", "Cut selection (editor)"},
		{"Ctrl+V", "Paste (editor)"},
		{"Ctrl+A", "Select all (editor)"},
		{"Ctrl+S", "Save (editor)"},
	}...)
	return list
}

// --- Input handling ---

func (m Model) handleDialogKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
	case dialogCreatePaneSetup:
		return m.handleCreatePaneSetupKey(msg)
	case dialogPluginError:
		return m.handlePluginErrorKey(msg)
	case dialogInstanceForm:
		return m.handleInstanceFormKey(msg)
	case dialogPlugins:
		return m.handlePluginsKey(msg)
	case dialogTOMLEditor:
		return m.handleTOMLEditorKey(msg)
	case dialogLogViewer:
		return m.handleLogViewerKey(msg)
	case dialogDisclaimer:
		return m.handleDisclaimerKey(msg)
	}
	return m, nil
}

func (m Model) handleAboutKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	const lastAboutItem = 5 // 0:Settings 1:Shortcuts 2:Plugins 3:Client 4:Daemon 5:MCP
	switch msg.String() {
	case "esc":
		m.dialog = dialogNone
	case "up", "k":
		if m.dialogCursor > 0 {
			m.dialogCursor--
		}
	case "down", "j":
		if m.dialogCursor < lastAboutItem {
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
		case 3:
			return m.openLogViewer("Client log", filepath.Join(config.QuilDir(), "quil.log"))
		case 4:
			return m.openLogViewer("Daemon log", filepath.Join(config.QuilDir(), "quild.log"))
		case 5:
			return m.openMCPLogsViewer()
		}
	}
	return m, nil
}

func (m Model) handleDisclaimerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.dialog = dialogNone
		m.dialogCursor = 0
		return m, tea.ClearScreen
	case "enter":
		if m.dialogCursor == 1 {
			// "Don't show again"
			m.cfg.UI.ShowDisclaimer = false
			m.configChanged = true
		}
		m.dialog = dialogNone
		m.dialogCursor = 0
		return m, tea.ClearScreen
	case "left":
		if m.dialogCursor > 0 {
			m.dialogCursor--
		}
	case "right":
		if m.dialogCursor < 1 {
			m.dialogCursor++
		}
	case "tab":
		m.dialogCursor = (m.dialogCursor + 1) % 2
	}
	return m, nil
}

func (m Model) handleSettingsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	fields := settingsFields()
	key := msg.String()

	if m.dialogEdit {
		switch {
		case key == "esc":
			m.dialogEdit = false
			m.dialogInput = ""
		case key == "enter":
			fields[m.dialogCursor].set(&m, m.dialogInput)
			m.dialogEdit = false
			m.dialogInput = ""
		case key == "backspace":
			if len(m.dialogInput) > 0 {
				m.dialogInput = m.dialogInput[:len(m.dialogInput)-1]
			}
		case key == m.cfg.Keybindings.Paste:
			return m, m.pasteToDialog()
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

func (m Model) handleShortcutsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.dialog = dialogAbout
		m.dialogCursor = 0
	}
	return m, nil
}

func (m Model) handleConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
	// Determine dialog width: plugin-specific for instance screens only
	width := dialogWidth
	if m.dialog == dialogTOMLEditor {
		width = 74
	} else if m.selectedPlugin != "" && (m.dialog == dialogInstanceForm || (m.dialog == dialogCreatePane && m.createPaneStep == 2)) {
		if p := m.pluginRegistry.Get(m.selectedPlugin); p != nil && p.Display.DialogWidth > 0 {
			width = p.Display.DialogWidth
		}
	}

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
	case dialogCreatePaneSetup:
		width = 70 // wider to fit paths and toggle labels comfortably
		content = m.renderCreatePaneSetupDialog()
	case dialogPluginError:
		content = m.renderPluginErrorDialog()
	case dialogInstanceForm:
		content = m.renderInstanceFormDialog()
	case dialogPlugins:
		content = m.renderPluginsDialog()
	case dialogTOMLEditor:
		// Rendered in View() as full-screen, not here
	case dialogDisclaimer:
		width = disclaimerWidth
		content = m.renderDisclaimerDialog()
	}

	box := dialogBorder.Width(width).Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m Model) renderAboutDialog() string {
	var b strings.Builder

	title := dialogTitle.Render("Quil v" + m.version)
	link := dialogSubtle.Render("github.com/artyomsv/quil")

	b.WriteString(lipgloss.PlaceHorizontal(dialogWidth, lipgloss.Center, title))
	b.WriteByte('\n')
	b.WriteString(lipgloss.PlaceHorizontal(dialogWidth, lipgloss.Center, link))
	b.WriteString("\n\n")

	items := []string{
		"Settings",
		"Shortcuts",
		"Plugins",
		"View client log",
		"View daemon log",
		"View MCP logs",
	}
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

func (m Model) renderDisclaimerDialog() string {
	var b strings.Builder
	w := disclaimerWidth

	// Title
	title := dialogTitle.Render("Quil v" + m.version + " -- Early Beta")
	b.WriteString(lipgloss.PlaceHorizontal(w, lipgloss.Center, title))
	b.WriteString("\n\n")

	// Beta notice
	b.WriteString(dialogSubtle.Render("  This software is in early beta. Some features may"))
	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  not work as expected. Linux and macOS support has"))
	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  not been fully tested yet."))
	b.WriteString("\n\n")

	// Separator
	b.WriteString(dialogSubtle.Render("  " + strings.Repeat("-", w-4)))
	b.WriteString("\n\n")

	// Random tip
	tip := disclaimerTips[m.disclaimerTipIdx]
	b.WriteString(dialogSelected.Render("  Tip: " + tip.title))
	b.WriteByte('\n')
	for _, item := range tip.items {
		b.WriteString(dialogNormal.Render("    - " + item))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')

	// Separator
	b.WriteString(dialogSubtle.Render("  " + strings.Repeat("-", w-4)))
	b.WriteByte('\n')

	// Buttons
	okLabel := "  OK  "
	dontShowLabel := "  Don't show again  "
	if m.dialogCursor == 0 {
		okLabel = dialogSelected.Render("[" + okLabel + "]")
		dontShowLabel = dialogSubtle.Render(" " + dontShowLabel + " ")
	} else {
		okLabel = dialogSubtle.Render(" " + okLabel + " ")
		dontShowLabel = dialogSelected.Render("[" + dontShowLabel + "]")
	}
	buttons := okLabel + "    " + dontShowLabel
	b.WriteString(lipgloss.PlaceHorizontal(w, lipgloss.Center, buttons))
	b.WriteByte('\n')

	// Hint
	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  Tab/Arrows navigate   Enter select   Esc close"))

	return b.String()
}

func (m Model) renderSettingsDialog() string {
	var b strings.Builder

	b.WriteString(dialogTitle.Render("Settings"))
	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  some changes persist to config.toml"))
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
			valRendered = dialogEditStyle.Render(m.dialogInput + "│")
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

func (m Model) handleCreatePaneKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
				return m, tea.ClearScreen // width changes — force full redraw
			}
			m.createPaneStep = 2
		} else {
			// No form fields — either show setup dialog (CWD/toggles) or jump to split.
			cmd := m.enterSetupOrSplit(p)
			if cmd != nil {
				return m, cmd
			}
		}
		// Plugin may have custom dialog_width — force redraw to avoid stale border cells
		if p != nil && p.Display.DialogWidth > 0 {
			return m, tea.ClearScreen
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
			return m, tea.ClearScreen // width changes — force full redraw
		}
		// Select existing instance
		idx := m.dialogCursor - 1
		var p *plugin.PanePlugin
		if idx < len(instances) {
			inst := instances[idx]
			p = m.pluginRegistry.Get(m.selectedPlugin)
			if p != nil {
				m.selectedInstanceArgs = BuildArgs(p.Command.ArgTemplate, inst.Fields)
			}
			m.selectedInstanceName = inst.Name
		}
		m.dialogCursor = 0
		// Either show setup dialog (CWD/toggles) or jump straight to split.
		// Mirror the same routing as the no-form-fields branch above and the
		// instance form submit path; otherwise saved instances would silently
		// skip the setup dialog while "+ New" wouldn't.
		if cmd := m.enterSetupOrSplit(p); cmd != nil {
			return m, cmd
		}
		m.createPaneStep = 3
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
	cwd := m.selectedCWD
	m.dialog = dialogNone
	m.createPaneStep = 0
	m.selectedCWD = ""
	m.cwdInputError = ""
	m.toggleStates = nil
	m.setupFieldCursor = 0
	m.cwdBrowseDir = ""
	m.cwdBrowseEntries = nil
	m.cwdBrowseCursor = 0
	m.cwdBrowseScroll = 0

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
				CWD:           cwd,
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
			CWD:          cwd,
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

func (m Model) handleInstanceFormKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.pluginRegistry.Get(m.selectedPlugin)
	if p == nil {
		m.dialog = dialogNone
		return m, nil
	}
	fields := p.Command.FormFields
	totalItems := len(fields) + 1 // fields + "Create" button
	key := msg.String()

	if m.dialogEdit {
		switch {
		case key == "esc":
			m.dialogEdit = false
			m.dialogInput = ""
		case key == "enter":
			if m.instanceFormCursor < len(fields) {
				m.instanceFormValues[m.instanceFormCursor] = m.dialogInput
			}
			m.dialogEdit = false
			m.dialogInput = ""
		case key == "backspace":
			if len(m.dialogInput) > 0 {
				m.dialogInput = m.dialogInput[:len(m.dialogInput)-1]
			}
		case key == "tab":
			// Commit and advance
			if m.instanceFormCursor < len(fields) {
				m.instanceFormValues[m.instanceFormCursor] = m.dialogInput
			}
			m.dialogEdit = false
			m.dialogInput = ""
			if m.instanceFormCursor < totalItems-1 {
				m.instanceFormCursor++
			}
		case key == m.cfg.Keybindings.Paste:
			return m, m.pasteToDialog()
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

	// Either show setup dialog (CWD/toggles) or proceed to split direction.
	if cmd := m.enterSetupOrSplit(p); cmd != nil {
		return m, cmd
	}
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
			valRendered = dialogEditStyle.Render(m.dialogInput + "│")
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

func (m Model) handlePluginsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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

func (m Model) handleTOMLEditorKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.tomlEditor == nil {
		m.dialog = dialogPlugins
		return m, nil
	}

	saved, closed, cmd := m.tomlEditor.HandleKey(msg.String())

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
		reloadCmd := func() tea.Msg {
			msg, _ := ipc.NewMessage(ipc.MsgReloadPlugins, nil)
			client.Send(msg)
			return nil
		}
		if cmd != nil {
			return m, tea.Batch(reloadCmd, cmd)
		}
		return m, tea.Batch(reloadCmd)
	}

	if closed {
		m.tomlEditor = nil
		m.dialog = dialogPlugins
		return m, nil
	}

	return m, cmd
}

// --- Log viewer dialog ---
//
// Reuses TextEditor in ReadOnly + HighlightPlain mode to show client/daemon/MCP
// log files. Opened from F1 → "View ... log". Esc returns to the F1 About menu.

// maxLogViewBytes caps how much of a log file we read into memory. Logs can
// grow unbounded; we tail the last N KB so the editor stays responsive.
const maxLogViewBytes = 256 * 1024

// openLogViewer reads the file at path (last maxLogViewBytes bytes) and opens
// the read-only TextEditor in dialogLogViewer mode. label is shown in the file
// path field so the user sees what they're looking at.
// logViewerViewport returns the viewport dimensions used by every log-viewer
// editor (client log, daemon log, MCP logs). Centralized so future tweaks to
// padding apply uniformly.
func (m Model) logViewerViewport() (w, h int) {
	w = m.width - 4
	if w < 40 {
		w = 40
	}
	h = m.height - 4
	if h < 5 {
		h = 5
	}
	return w, h
}

// newLogViewerEditor builds a read-only TextEditor pre-positioned at the end
// of the buffer (so the freshest log lines are in view) and stamped with the
// configured log-viewer page size.
func (m Model) newLogViewerEditor(content, path string) *TextEditor {
	viewW, viewH := m.logViewerViewport()
	editor := NewTextEditor(content, path, viewW, viewH)
	editor.Highlight = HighlightPlain
	editor.ReadOnly = true
	editor.PageSize = m.cfg.UI.LogViewerPageLines
	editor.CursorRow = len(editor.Lines) - 1
	if editor.CursorRow < 0 {
		editor.CursorRow = 0
	}
	editor.CursorCol = 0
	editor.ensureCursorVisible()
	return editor
}

func (m Model) openLogViewer(label, path string) (tea.Model, tea.Cmd) {
	content, err := readLogTail(path, maxLogViewBytes)
	if err != nil {
		// Show the error inline in an empty editor so the user knows
		// what went wrong (file missing, permission denied, etc.).
		content = fmt.Sprintf("# %s\n# %s\n#\n# Could not read log file: %v\n",
			label, path, err)
	}
	m.tomlEditor = m.newLogViewerEditor(content, path)
	m.dialog = dialogLogViewer
	return m, tea.ClearScreen
}

// openMCPLogsViewer aggregates the per-pane MCP interaction logs from
// config.MCPLogDir() into a single read-only buffer with file-name headers,
// most-recently-modified file first.
func (m Model) openMCPLogsViewer() (tea.Model, tea.Cmd) {
	dir := config.MCPLogDir(m.cfg.MCP)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return m.openLogViewer("MCP logs", filepath.Join(dir, "(unavailable)"))
	}

	type logFile struct {
		name string
		mod  time.Time
		size int64
	}
	var files []logFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		files = append(files, logFile{name: e.Name(), mod: info.ModTime(), size: info.Size()})
	}
	// Most recently modified first.
	sort.Slice(files, func(i, j int) bool {
		return files[i].mod.After(files[j].mod)
	})

	if len(files) == 0 {
		empty := fmt.Sprintf("# MCP logs\n# %s\n#\n# No MCP interactions logged yet.\n", dir)
		m.tomlEditor = m.newLogViewerEditor(empty, dir)
		m.dialog = dialogLogViewer
		return m, tea.ClearScreen
	}

	// Build aggregated content. Cap each file to a reasonable share of
	// maxLogViewBytes so one huge file doesn't squeeze out the others.
	perFile := maxLogViewBytes / len(files)
	if perFile < 4*1024 {
		perFile = 4 * 1024
	}
	var b strings.Builder
	for _, f := range files {
		b.WriteString(fmt.Sprintf("\n========== %s  (%s, %d bytes) ==========\n\n",
			f.name, f.mod.Format("2006-01-02 15:04:05"), f.size))
		full := filepath.Join(dir, f.name)
		tail, terr := readLogTail(full, perFile)
		if terr != nil {
			b.WriteString(fmt.Sprintf("(read error: %v)\n", terr))
			continue
		}
		b.WriteString(tail)
		if !strings.HasSuffix(tail, "\n") {
			b.WriteByte('\n')
		}
	}

	m.tomlEditor = m.newLogViewerEditor(b.String(), dir)
	m.dialog = dialogLogViewer
	return m, tea.ClearScreen
}

// readLogTail reads up to maxBytes from the END of the given file. If the
// file is shorter than maxBytes the whole file is returned. Always reads from
// the start of a line (skipping any partial first line) so the result is
// well-formed.
//
// Symlinks are rejected: an Lstat is performed first and any non-regular
// file (symlink, device, named pipe, etc.) is refused. This prevents the log
// viewer from being redirected to read arbitrary files via a swapped link
// inside ~/.quil/. Same hardening pattern as internal/persist/notes.go.
func readLogTail(path string, maxBytes int) (string, error) {
	li, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !li.Mode().IsRegular() {
		return "", fmt.Errorf("refusing to read non-regular file %q (mode=%v)", path, li.Mode())
	}

	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Re-stat through the open handle to defeat a TOCTOU swap between Lstat
	// and Open. If the inode changed we refuse the read.
	stat, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !stat.Mode().IsRegular() {
		return "", fmt.Errorf("refusing to read non-regular file %q after open", path)
	}
	size := stat.Size()
	if size <= int64(maxBytes) {
		data, rerr := io.ReadAll(f)
		if rerr != nil {
			return "", rerr
		}
		return string(data), nil
	}

	// Seek to (size - maxBytes), read to end, then drop everything before
	// the first newline so we don't show a partial line.
	if _, err := f.Seek(size-int64(maxBytes), io.SeekStart); err != nil {
		return "", err
	}
	buf := make([]byte, maxBytes)
	n, err := f.Read(buf)
	if err != nil {
		return "", err
	}
	buf = buf[:n]
	if i := bytes.IndexByte(buf, '\n'); i >= 0 && i+1 < len(buf) {
		buf = buf[i+1:]
	}
	return "[... older lines truncated ...]\n" + string(buf), nil
}

// handleLogViewerKey routes editor keys to the read-only TextEditor for
// log viewing. Esc closes the viewer and returns to the F1 About menu.
// Save (Ctrl+S) is suppressed by TextEditor.ReadOnly so we never overwrite
// a log file by accident.
func (m Model) handleLogViewerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.tomlEditor == nil {
		m.dialog = dialogAbout
		return m, nil
	}
	_, closed, cmd := m.tomlEditor.HandleKey(msg.String())
	if closed {
		m.tomlEditor = nil
		m.dialog = dialogAbout
		// Cursor is at the position of the menu item the user came from
		// (3, 4, or 5). Don't reset it — feels jarring.
		return m, tea.ClearScreen
	}
	return m, cmd
}

// --- Plugin error dialog ---

func (m Model) handlePluginErrorKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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

// --- Pane setup dialog (CWD prompt + runtime toggles) ---
//
// Receiver convention for the helpers below: Bubble Tea's Update loop expects
// `func (m Model) Update(msg) (Model, Cmd)`, so the top-level handlers
// (handleCreatePaneSetupKey, handleSetupCWDKey, submitSetupDialog,
// renderCreatePaneSetupDialog, setupFieldCount, setupFieldKind) all use a
// value receiver and return the (modified) `m` for the framework to install
// as the next state. Helpers they call internally — enterSetupOrSplit,
// loadBrowseDir, loadBrowseDirAndSelect, adjustBrowseScroll — mutate state
// in place and use a pointer receiver, since they're invoked via the
// addressable local `m` inside a value-receiver method (Go takes its address
// implicitly). Mixing the two styles is intentional and matches the pattern
// already used for handleInstanceFormKey + openInstanceForm in this file.

// enterSetupOrSplit routes after a plugin or instance is picked: either show
// the setup dialog (if plugin needs CWD prompt or has toggles) or jump to
// split-direction selection. Returns nil when no setup is needed, in which
// case the caller is responsible for advancing to step 3.
//
// Receiver is *Model because this method always mutates state — even on the
// "no setup" branch it must clear stale CWD/toggle state from a prior plugin
// (otherwise picking plugin A → setup → Esc → plugin B leaks A's CWD into
// B's spawn). See the matching comment near the rest of the setup helpers.
func (m *Model) enterSetupOrSplit(p *plugin.PanePlugin) tea.Cmd {
	// Always clear setup state first — even when the new plugin has no setup
	// dialog, leftover state from a prior plugin must not survive into the
	// next CreatePanePayload.
	m.selectedCWD = ""
	m.cwdInputError = ""
	m.toggleStates = nil
	m.setupFieldCursor = 0
	m.cwdBrowseDir = ""
	m.cwdBrowseEntries = nil
	m.cwdBrowseCursor = 0
	m.cwdBrowseScroll = 0

	needsSetup := p != nil && (p.Command.PromptsCWD || len(p.Command.Toggles) > 0)
	if !needsSetup {
		m.createPaneStep = 3
		return nil
	}

	// Initialize the directory browser. Smart default: active pane's CWD
	// (already tracked via OSC 7); fall back to the user's home directory.
	if p.Command.PromptsCWD {
		startDir := ""
		if tab := m.activeTabModel(); tab != nil {
			if pane := tab.ActivePaneModel(); pane != nil {
				startDir = pane.CWD
			}
		}
		if startDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				log.Printf("setup dialog: os.UserHomeDir: %v", err)
			} else {
				startDir = home
			}
		}
		if startDir != "" {
			if err := m.loadBrowseDir(startDir); err != nil {
				log.Printf("setup dialog: load browse dir %q: %v", startDir, err)
				// loadBrowseDir leaves state empty on failure; submit will use ""
				// (daemon default). Continue without blocking the dialog open.
			}
		}
	}

	// Initialize toggle states from defaults.
	m.toggleStates = make([]bool, len(p.Command.Toggles))
	for i, t := range p.Command.Toggles {
		m.toggleStates[i] = t.Default
	}

	m.dialogEdit = false // browser doesn't use edit mode
	m.dialog = dialogCreatePaneSetup
	return tea.ClearScreen
}

// loadBrowseDir reads `path` and populates the directory browser state. Only
// directories are listed; ".." is prepended unless `path` is at the filesystem
// root. The cursor is reset to position 0. On error, the existing browser
// state is left untouched and the error is returned.
func (m *Model) loadBrowseDir(path string) error {
	return m.loadBrowseDirAndSelect(path, "")
}

// loadBrowseDirAndSelect is loadBrowseDir but positions the cursor on
// `selectName` (without trailing slash) if it appears in the listing.
// Used by parent-up navigation to keep the user oriented on the directory
// they just exited.
func (m *Model) loadBrowseDirAndSelect(path, selectName string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("abs path: %w", err)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return fmt.Errorf("read dir: %w", err)
	}

	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		// Plain directory — keep.
		if e.IsDir() {
			dirs = append(dirs, e.Name())
			continue
		}
		// Directory symlink / Windows junction: DirEntry reports them as
		// ModeSymlink (not ModeDir) so the IsDir() above misses them. Stat
		// follows the link and tells us whether the target is a directory.
		if e.Type()&fs.ModeSymlink != 0 {
			if info, err := os.Stat(filepath.Join(abs, e.Name())); err == nil && info.IsDir() {
				dirs = append(dirs, e.Name())
			}
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		return strings.ToLower(dirs[i]) < strings.ToLower(dirs[j])
	})

	listing := make([]string, 0, len(dirs)+1)
	if parent := filepath.Dir(abs); parent != abs {
		listing = append(listing, "..")
	}
	listing = append(listing, dirs...)

	m.cwdBrowseDir = abs
	m.cwdBrowseEntries = listing
	m.cwdBrowseCursor = 0
	m.cwdBrowseScroll = 0

	// Position the cursor on the requested entry if asked.
	if selectName != "" {
		for i, name := range listing {
			if name == selectName {
				m.cwdBrowseCursor = i
				m.adjustBrowseScroll()
				break
			}
		}
	}
	return nil
}

// browserVisibleRows is the height of the directory browser viewport.
const browserVisibleRows = 12

// adjustBrowseScroll keeps the cursor inside the visible window.
func (m *Model) adjustBrowseScroll() {
	if m.cwdBrowseCursor < m.cwdBrowseScroll {
		m.cwdBrowseScroll = m.cwdBrowseCursor
	}
	if m.cwdBrowseCursor >= m.cwdBrowseScroll+browserVisibleRows {
		m.cwdBrowseScroll = m.cwdBrowseCursor - browserVisibleRows + 1
	}
	if m.cwdBrowseScroll < 0 {
		m.cwdBrowseScroll = 0
	}
}

// setupFieldCount returns the number of focusable fields in the setup dialog:
// CWD (if PromptsCWD) + one per toggle + 1 for the Continue button.
func (m Model) setupFieldCount(p *plugin.PanePlugin) int {
	n := len(p.Command.Toggles) + 1 // +1 for Continue
	if p.Command.PromptsCWD {
		n++
	}
	return n
}

// setupFieldKind reports what field is at the given cursor index in the setup
// dialog. Returns "cwd", "toggle" (with toggleIdx), or "continue".
func (m Model) setupFieldKind(p *plugin.PanePlugin, cursor int) (kind string, toggleIdx int) {
	i := cursor
	if p.Command.PromptsCWD {
		if i == 0 {
			return "cwd", -1
		}
		i--
	}
	if i < len(p.Command.Toggles) {
		return "toggle", i
	}
	return "continue", -1
}

// handleCreatePaneSetupKey handles keystrokes in dialogCreatePaneSetup.
func (m Model) handleCreatePaneSetupKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.pluginRegistry.Get(m.selectedPlugin)
	if p == nil {
		m.dialog = dialogCreatePane
		m.createPaneStep = 1
		return m, tea.ClearScreen
	}

	key := msg.String()
	kind, togIdx := m.setupFieldKind(p, m.setupFieldCursor)

	// Esc and Tab/Shift+Tab work the same regardless of which field is focused.
	switch key {
	case "esc":
		if len(p.Command.FormFields) > 0 {
			m.dialog = dialogInstanceForm
			// The instance-form flow lives at step 2; restore that explicitly
			// rather than relying on whatever value happened to be left in
			// createPaneStep before the setup dialog was opened.
			m.createPaneStep = 2
		} else {
			m.dialog = dialogCreatePane
			m.createPaneStep = 1
		}
		m.cwdInputError = ""
		m.dialogEdit = false
		m.dialogCursor = 0
		return m, tea.ClearScreen

	case "tab":
		m.setupFieldCursor = (m.setupFieldCursor + 1) % m.setupFieldCount(p)
		return m, nil

	case "shift+tab":
		n := m.setupFieldCount(p)
		m.setupFieldCursor = (m.setupFieldCursor - 1 + n) % n
		return m, nil
	}

	// Field-specific behavior.
	switch kind {
	case "cwd":
		return m.handleSetupCWDKey(p, key)

	case "toggle":
		switch key {
		case " ", "space":
			if togIdx >= 0 && togIdx < len(m.toggleStates) {
				m.toggleStates[togIdx] = !m.toggleStates[togIdx]
			}
			return m, nil
		case "up":
			n := m.setupFieldCount(p)
			m.setupFieldCursor = (m.setupFieldCursor - 1 + n) % n
			return m, nil
		case "down":
			m.setupFieldCursor = (m.setupFieldCursor + 1) % m.setupFieldCount(p)
			return m, nil
		case "enter":
			return m.submitSetupDialog(p)
		}
		return m, nil

	case "continue":
		switch key {
		case "up":
			n := m.setupFieldCount(p)
			m.setupFieldCursor = (m.setupFieldCursor - 1 + n) % n
			return m, nil
		case "enter":
			return m.submitSetupDialog(p)
		}
		return m, nil
	}
	return m, nil
}

// handleSetupCWDKey processes keystrokes when the CWD browser field is focused.
// The browser shows a scrollable directory listing; arrows navigate, Enter
// descends/ascends, and Ctrl+V pastes a path to jump there.
func (m Model) handleSetupCWDKey(p *plugin.PanePlugin, key string) (tea.Model, tea.Cmd) {
	if len(m.cwdBrowseEntries) == 0 {
		// Browser failed to load — Enter still submits using empty selectedCWD.
		if key == "enter" {
			return m.submitSetupDialog(p)
		}
		return m, nil
	}

	switch key {
	case "up", "k":
		if m.cwdBrowseCursor > 0 {
			m.cwdBrowseCursor--
			m.adjustBrowseScroll()
		}
		return m, nil

	case "down", "j":
		if m.cwdBrowseCursor < len(m.cwdBrowseEntries)-1 {
			m.cwdBrowseCursor++
			m.adjustBrowseScroll()
		}
		return m, nil

	case "pgup":
		m.cwdBrowseCursor -= browserVisibleRows
		if m.cwdBrowseCursor < 0 {
			m.cwdBrowseCursor = 0
		}
		m.adjustBrowseScroll()
		return m, nil

	case "pgdown":
		m.cwdBrowseCursor += browserVisibleRows
		if m.cwdBrowseCursor > len(m.cwdBrowseEntries)-1 {
			m.cwdBrowseCursor = len(m.cwdBrowseEntries) - 1
		}
		m.adjustBrowseScroll()
		return m, nil

	case "home":
		m.cwdBrowseCursor = 0
		m.adjustBrowseScroll()
		return m, nil

	case "end":
		m.cwdBrowseCursor = len(m.cwdBrowseEntries) - 1
		m.adjustBrowseScroll()
		return m, nil

	case "enter", "right", "l":
		entry := m.cwdBrowseEntries[m.cwdBrowseCursor]
		var target string
		if entry == ".." {
			target = filepath.Dir(m.cwdBrowseDir)
		} else {
			target = filepath.Join(m.cwdBrowseDir, entry)
		}
		if err := m.loadBrowseDir(target); err != nil {
			m.cwdInputError = err.Error()
		} else {
			m.cwdInputError = ""
		}
		return m, nil

	case "backspace", "left", "h":
		parent := filepath.Dir(m.cwdBrowseDir)
		if parent == m.cwdBrowseDir {
			return m, nil // already at root
		}
		// Remember which child we came from so we can highlight it in the
		// parent listing — keeps the user oriented during quick up/down
		// navigation.
		child := filepath.Base(m.cwdBrowseDir)
		if err := m.loadBrowseDirAndSelect(parent, child); err != nil {
			m.cwdInputError = err.Error()
		} else {
			m.cwdInputError = ""
		}
		return m, nil

	case "ctrl+v":
		text, err := clipboard.Read()
		if err != nil {
			log.Printf("setup dialog: clipboard read: %v", err)
			m.cwdInputError = fmt.Sprintf("clipboard: %v", err)
			return m, nil
		}
		path := sanitizePastedPath(text)
		if path == "" {
			return m, nil
		}
		// Validate before jumping; reuse the same normalize logic so ~ and
		// quoted Windows paths work in the browser too.
		cleaned, vErr := validateAndNormalizeCWD(path)
		if vErr != nil {
			m.cwdInputError = vErr.Error()
			return m, nil
		}
		if cleaned == "" {
			return m, nil // empty after normalization
		}
		if err := m.loadBrowseDir(cleaned); err != nil {
			m.cwdInputError = err.Error()
		} else {
			m.cwdInputError = ""
		}
		return m, nil
	}
	return m, nil
}

// submitSetupDialog commits the browser-selected directory and toggle states,
// then advances the create-pane flow to the split-direction step.
func (m Model) submitSetupDialog(p *plugin.PanePlugin) (tea.Model, tea.Cmd) {
	if p.Command.PromptsCWD {
		m.selectedCWD = m.cwdBrowseDir
	}
	m.cwdInputError = ""

	// Append enabled-toggle args to whatever instance args came in.
	var extra []string
	for i, t := range p.Command.Toggles {
		if i < len(m.toggleStates) && m.toggleStates[i] {
			extra = append(extra, t.ArgsWhenOn...)
		}
	}
	if len(extra) > 0 {
		merged := make([]string, 0, len(m.selectedInstanceArgs)+len(extra))
		merged = append(merged, m.selectedInstanceArgs...)
		merged = append(merged, extra...)
		m.selectedInstanceArgs = merged
	}

	m.dialog = dialogCreatePane
	m.createPaneStep = 3
	m.dialogCursor = 0
	m.dialogEdit = false
	return m, tea.ClearScreen
}

// validateAndNormalizeCWD cleans a user-entered path, expands a leading ~,
// runs filepath.Abs, and verifies the target exists and is a directory.
// An empty string is accepted (daemon falls back to its own os.Getwd).
func validateAndNormalizeCWD(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, `"`) // Windows "Copy as path" wraps the path in quotes
	if s == "" {
		return "", nil
	}

	// Expand a leading ~ (Go stdlib doesn't do this).
	if s == "~" || strings.HasPrefix(s, "~/") || strings.HasPrefix(s, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand ~: %w", err)
		}
		switch {
		case s == "~":
			s = home
		case strings.HasPrefix(s, "~/"):
			s = filepath.Join(home, s[2:])
		case strings.HasPrefix(s, `~\`):
			s = filepath.Join(home, s[2:])
		}
	}

	abs, err := filepath.Abs(s)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("path does not exist")
		}
		return "", fmt.Errorf("stat path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory")
	}
	// Resolve symlinks so the daemon receives a canonical path. This also
	// closes the small TOCTOU window between Stat (above) and the eventual
	// PTY spawn — a symlink swap on the original path can no longer redirect
	// the spawn to a different directory. EvalSymlinks failure is non-fatal:
	// fall back to the lexically-cleaned absolute path.
	if resolved, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
		return resolved, nil
	}
	return abs, nil
}

// sanitizePastedPath strips common clipboard noise (whitespace, surrounding
// quotes, and any control bytes) so paths copied from GUI file managers are
// accepted cleanly. Control bytes are dropped to prevent terminal-escape
// injection: a clipboard payload containing OSC/CSI sequences would otherwise
// flow through os.Stat into m.cwdInputError and reach the rendered dialog.
func sanitizePastedPath(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"`)
	// Some Linux file managers wrap paths in single quotes too.
	s = strings.Trim(s, `'`)

	// Drop any non-printable control bytes. We keep tab (0x09) since some
	// shells legitimately produce it inside paths via completion, even though
	// it is uncommon.
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\t' {
			b.WriteRune(r)
			continue
		}
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// renderCreatePaneSetupDialog renders the setup dialog: a CWD directory
// browser (optional) + one checkbox per plugin Toggle + a Continue button.
// The focused field is highlighted; inside the browser the selected entry
// is highlighted.
func (m Model) renderCreatePaneSetupDialog() string {
	p := m.pluginRegistry.Get(m.selectedPlugin)
	if p == nil {
		return ""
	}

	var b strings.Builder
	b.WriteString(dialogTitle.Render(p.DisplayName + " — Setup"))
	b.WriteString("\n\n")

	cursor := m.setupFieldCursor
	fieldIdx := 0

	if p.Command.PromptsCWD {
		focused := cursor == fieldIdx
		label := "Working directory:"
		if focused {
			label = dialogSelected.Render("> " + label)
		} else {
			label = dialogNormal.Render("  " + label)
		}
		b.WriteString(label + "\n")

		// Current path on its own line so it's always visible regardless of
		// where the cursor is in the listing.
		path := m.cwdBrowseDir
		if path == "" {
			path = dialogSubtle.Render("(no directory loaded — daemon default will be used)")
		} else {
			path = dialogValStyle.Render(path)
		}
		b.WriteString("    " + path + "\n")

		if m.cwdInputError != "" {
			b.WriteString("    " + dialogErrorStyle.Render("✗ "+m.cwdInputError) + "\n")
		}

		// Listing window — always allocate `browserVisibleRows` lines so the
		// dialog height stays stable across navigation.
		entries := m.cwdBrowseEntries
		visible := browserVisibleRows
		start := m.cwdBrowseScroll
		end := start + visible
		if end > len(entries) {
			end = len(entries)
		}

		for i := 0; i < visible; i++ {
			idx := start + i
			if idx >= len(entries) {
				b.WriteString("\n")
				continue
			}
			name := entries[idx]
			displayName := name
			if name != ".." {
				displayName = name + "/"
			}
			line := "    " + displayName
			if focused && idx == m.cwdBrowseCursor {
				line = "  > " + dialogSelected.Render(displayName)
			} else {
				line = "    " + dialogNormal.Render(displayName)
			}
			b.WriteString(line + "\n")
		}

		// Scroll indicator — shows position inside the list.
		if len(entries) > visible {
			b.WriteString(dialogSubtle.Render(fmt.Sprintf("    %d/%d  ↑↓ navigate  Enter descend  ← parent  Ctrl+V paste path", m.cwdBrowseCursor+1, len(entries))) + "\n")
		} else if len(entries) > 0 {
			b.WriteString(dialogSubtle.Render("    ↑↓ navigate  Enter descend  ← parent  Ctrl+V paste path") + "\n")
		} else {
			b.WriteString(dialogSubtle.Render("    (empty directory)") + "\n")
		}
		b.WriteString("\n")
		fieldIdx++
	}

	for i, t := range p.Command.Toggles {
		focused := cursor == fieldIdx
		box := "[ ]"
		if i < len(m.toggleStates) && m.toggleStates[i] {
			box = "[x]"
		}
		prefix := "  "
		lineStyle := dialogNormal
		if focused {
			prefix = "> "
			lineStyle = dialogSelected
		}
		b.WriteString(prefix + lineStyle.Render(box+" "+t.Label) + "\n")
		fieldIdx++
	}

	b.WriteByte('\n')
	btnCursor := "  "
	btnStyle := dialogNormal
	if cursor == fieldIdx {
		btnCursor = "> "
		btnStyle = dialogSelected
	}
	b.WriteString(btnCursor + btnStyle.Render("[Continue]") + "\n")

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("Tab next field  Space toggle  Enter submit  Esc back"))

	return b.String()
}
