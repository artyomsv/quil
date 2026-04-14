package tui

import (
	"fmt"
	"log"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/BurntSushi/toml"
	"github.com/charmbracelet/x/ansi"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/plugin"
)

// fitToWidth truncates a string (which may contain ANSI escape codes) to
// exactly w visible columns, then pads with spaces if shorter. This
// guarantees every line occupies exactly w terminal cells regardless of
// how the TextEditor rendered it.
func fitToWidth(s string, w int) string {
	s = ansi.Truncate(s, w, "")
	visible := ansi.StringWidth(s)
	if visible < w {
		s += strings.Repeat(" ", w-visible)
	}
	return s
}

// migrationDiffLines returns a set of line numbers in `a` whose trimmed
// content does not appear anywhere in `b`. Empty and comment-only lines are
// excluded so the diff focuses on meaningful structural differences.
func migrationDiffLines(a, b *TextEditor) map[int]bool {
	otherSet := make(map[string]bool, len(b.Lines))
	for _, line := range b.Lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			otherSet[trimmed] = true
		}
	}
	diff := make(map[int]bool)
	for i, line := range a.Lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !otherSet[trimmed] {
			diff[i] = true
		}
	}
	return diff
}

// openMigrationDialog initializes the two TextEditor instances for the
// current stale plugin and switches to the migration dialog screen.
func (m *Model) openMigrationDialog() {
	if m.width < 10 || m.height < 6 {
		return // terminal too small; will retry on next WindowSizeMsg
	}
	sp := m.migrationPlugins[m.migrationIdx]
	halfW := m.width/2 - 1
	editorH := m.height - 4 // title bar + tab bar + column headers + status bar

	m.migrationLeft = NewTextEditor(string(sp.UserData), sp.FilePath, halfW, editorH)
	m.migrationRight = NewTextEditor(string(sp.DefaultData), "", halfW, editorH)
	m.migrationRight.ReadOnly = true
	m.migrationRightFocus = false
	m.migrationError = ""
	m.dialog = dialogPluginMigration
}

// handleMigrationKey processes key events for the plugin migration dialog.
// Only modifier keys trigger migration actions — all regular keys (letters,
// Enter, etc.) pass through to the focused TextEditor so the user can type
// freely.
func (m Model) handleMigrationKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch key {
	case "ctrl+q":
		return m, tea.Quit

	case "esc":
		// Blocked — migration must be resolved before using Quil.
		return m, nil

	case "tab":
		m.migrationRightFocus = !m.migrationRightFocus
		return m, nil

	case "ctrl+pgup":
		if m.migrationIdx > 0 {
			m.migrationIdx--
			m.openMigrationDialog()
		}
		return m, nil

	case "ctrl+pgdown":
		if m.migrationIdx < len(m.migrationPlugins)-1 {
			m.migrationIdx++
			m.openMigrationDialog()
		}
		return m, nil

	case "f5":
		// Accept default: replace left editor with the new default content.
		sp := m.migrationPlugins[m.migrationIdx]
		halfW := m.width/2 - 1
		editorH := m.height - 4
		m.migrationLeft = NewTextEditor(string(sp.DefaultData), sp.FilePath, halfW, editorH)
		m.migrationLeft.Dirty = true
		m.migrationError = ""
		return m, nil

	case "ctrl+s":
		// Save merged config and advance to next plugin (or close).
		return m.saveMigrationAndAdvance()
	}

	// All other keys (typing, Enter, Backspace, arrows, etc.) route to
	// the focused editor.
	if m.migrationRightFocus {
		_, _, cmd := m.migrationRight.HandleKey(key)
		return m, cmd
	}
	_, _, cmd := m.migrationLeft.HandleKey(key)
	return m, cmd
}

// saveMigrationAndAdvance validates the left editor content, writes it to
// disk, and either advances to the next stale plugin or closes the dialog.
func (m Model) saveMigrationAndAdvance() (tea.Model, tea.Cmd) {
	content := strings.Join(m.migrationLeft.Lines, "\n")

	// Validate TOML syntax.
	var parsed any
	if err := toml.Unmarshal([]byte(content), &parsed); err != nil {
		m.migrationError = fmt.Sprintf("Invalid TOML: %v", err)
		return m, nil
	}

	// Check schema_version meets the required minimum.
	sp := m.migrationPlugins[m.migrationIdx]
	requiredVer := plugin.ParseSchemaVersion(sp.DefaultData)
	userVer := plugin.ParseSchemaVersion([]byte(content))
	if requiredVer > 0 && userVer < requiredVer {
		m.migrationError = fmt.Sprintf("schema_version must be >= %d (currently %d)", requiredVer, userVer)
		return m, nil
	}

	// Write to disk.
	if err := os.WriteFile(sp.FilePath, []byte(content), 0600); err != nil {
		m.migrationError = fmt.Sprintf("Write failed: %v", err)
		return m, nil
	}
	log.Printf("migration: saved %s", sp.FilePath)

	// Advance to the next stale plugin or close the dialog.
	if m.migrationIdx < len(m.migrationPlugins)-1 {
		m.migrationIdx++
		m.openMigrationDialog()
		return m, nil
	}

	// All plugins resolved — reload the registry and close the dialog.
	if err := m.pluginRegistry.LoadFromDir(config.PluginsDir()); err != nil {
		log.Printf("migration: reload plugins: %v", err)
	}
	m.pluginRegistry.DetectAvailability()
	m.migrationLeft = nil
	m.migrationRight = nil
	m.migrationPlugins = nil
	m.dialog = dialogNone
	return m, tea.ClearScreen
}

// renderMigrationFullScreen renders the plugin migration dialog as a
// full-screen split view with two editors side by side.
func (m Model) renderMigrationFullScreen() string {
	var b strings.Builder

	// Title bar (raw ANSI — background 236, same pattern as renderTOMLEditorFullScreen)
	title := " Plugin Migration"
	for ansi.StringWidth(title) < m.width {
		title += " "
	}
	b.WriteString("\x1b[48;5;236m\x1b[1;38;5;230m" + title + "\x1b[0m\n")

	// Tab bar — only shown when there are multiple stale plugins.
	if len(m.migrationPlugins) > 1 {
		var tabLine strings.Builder
		tabLine.WriteString(" ")
		for i, sp := range m.migrationPlugins {
			if i == m.migrationIdx {
				tabLine.WriteString("\x1b[1;38;5;230m " + sp.Name + " \x1b[0m")
			} else {
				tabLine.WriteString("\x1b[38;5;241m " + sp.Name + " \x1b[0m")
			}
			if i < len(m.migrationPlugins)-1 {
				tabLine.WriteString("\x1b[38;5;241m|\x1b[0m")
			}
		}
		line := tabLine.String()
		// Pad to full width
		for ansi.StringWidth(line) < m.width {
			line += " "
		}
		b.WriteString("\x1b[48;5;236m" + line + "\x1b[0m\n")
	}

	// Column headers
	halfW := m.width/2 - 1
	leftHeader := " Your config (editable)"
	rightHeader := " New default (read-only)"

	leftColor := "\x1b[38;5;241m"
	rightColor := "\x1b[38;5;241m"
	if !m.migrationRightFocus {
		leftColor = "\x1b[1;38;5;117m"
	} else {
		rightColor = "\x1b[1;38;5;117m"
	}

	for ansi.StringWidth(leftHeader) < halfW {
		leftHeader += " "
	}
	for ansi.StringWidth(rightHeader) < halfW {
		rightHeader += " "
	}

	b.WriteString(leftColor + leftHeader + "\x1b[0m")
	b.WriteString("\x1b[48;5;238m \x1b[0m") // separator
	b.WriteString(rightColor + rightHeader + "\x1b[0m\n")

	// Editor panes — render both and join side by side
	m.migrationLeft.ViewWidth = halfW
	m.migrationRight.ViewWidth = halfW

	editorH := m.height - 4
	if len(m.migrationPlugins) > 1 {
		editorH-- // account for tab bar
	}
	m.migrationLeft.ViewHeight = editorH
	m.migrationRight.ViewHeight = editorH

	leftRender := m.migrationLeft.Render()
	rightRender := m.migrationRight.Render()

	leftLines := strings.Split(strings.TrimSuffix(leftRender, "\n"), "\n")
	rightLines := strings.Split(strings.TrimSuffix(rightRender, "\n"), "\n")

	// Ensure both have the same number of lines
	for len(leftLines) < editorH {
		leftLines = append(leftLines, "")
	}
	for len(rightLines) < editorH {
		rightLines = append(rightLines, "")
	}

	// Compute diff: build sets of trimmed non-empty lines from each side.
	// Lines unique to one side get a tinted background so the user can
	// quickly spot what changed between their config and the new default.
	leftDiff := migrationDiffLines(m.migrationLeft, m.migrationRight)
	rightDiff := migrationDiffLines(m.migrationRight, m.migrationLeft)

	separator := "\x1b[48;5;238m \x1b[0m"
	for i := 0; i < editorH; i++ {
		left := ""
		if i < len(leftLines) {
			left = leftLines[i]
			if leftDiff[m.migrationLeft.ScrollTop+i] {
				left = "\x1b[48;5;52m" + left + "\x1b[0m" // red tint: missing from default
			}
		}
		right := ""
		if i < len(rightLines) {
			right = rightLines[i]
			if rightDiff[m.migrationRight.ScrollTop+i] {
				right = "\x1b[48;5;22m" + right + "\x1b[0m" // green tint: new in default
			}
		}
		// Pad each pane to exactly halfW visible characters so the
		// separator stays at a fixed column and pane content never
		// bleeds across the boundary.
		left = fitToWidth(left, halfW)
		right = fitToWidth(right, halfW)
		b.WriteString(left + separator + right + "\n")
	}

	// Status bar
	var status string
	if m.migrationError != "" {
		status = fmt.Sprintf(" \x1b[31m%s\x1b[0m\x1b[48;5;236m\x1b[38;5;250m", m.migrationError)
	} else {
		status = " Tab focus  "
		if len(m.migrationPlugins) > 1 {
			status += "Ctrl+PgUp/PgDn switch plugin  "
		}
		status += "F5 accept default  Ctrl+S save  Ctrl+Q quit"
	}
	for ansi.StringWidth(status) < m.width {
		status += " "
	}
	b.WriteString("\x1b[48;5;236m\x1b[38;5;250m" + status + "\x1b[0m")

	return b.String()
}
