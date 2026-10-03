package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/config"
)

// F1 → Settings → Sandbox: the Ctrl+N dialog's sandbox defaults.
//
// Two rows, so no windowing (the Notifications screen needs it for fifteen).
// Both are CLIENT defaults: the dialog sends its choice on the wire, so they
// apply at once and to remote projects too. The daemon reads the same keys at
// its own start, but only as the fallback for creates that name nothing (MCP,
// older clients) — which is why the page needs no "restart" note for its own
// purpose. Saved on TUI exit with every other Settings edit (configChanged).
const (
	sandboxSettingsImageRow  = 0
	sandboxSettingsSignInRow = 1
	sandboxSettingsRows      = 2
)

// setSandboxSignInDefault writes BOTH keys for a choice, so the page can never
// produce auth = "token" + shared_claude_config = true — the pair under which
// an MCP-created token pane and a browser pane share one directory and keep
// re-opening each other's sign-in.
func (m *Model) setSandboxSignInDefault(choice string) {
	auth, claudeConfig := sandboxSignInFields(choice)
	shared := claudeConfig == config.SandboxClaudeConfigShared
	if m.cfg.Sandbox.Auth == auth && m.cfg.Sandbox.SharedClaudeConfig == shared {
		return
	}
	m.cfg.Sandbox.Auth = auth
	m.cfg.Sandbox.SharedClaudeConfig = shared
	m.configChanged = true
}

// handleSandboxSettingsKey drives the screen.
func (m Model) handleSandboxSettingsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.dialogEdit {
		switch key {
		case "esc":
			m.dialogEdit = false
			m.dialogInput = ""
		case "enter":
			// Trimmed and bounded like the Ctrl+N field. An empty value is
			// legal here: it means "no default image".
			v := strings.TrimSpace(m.dialogInput)
			if v != m.cfg.Sandbox.DefaultImage && len([]rune(v)) <= sandboxImageMax {
				m.cfg.Sandbox.DefaultImage = v
				m.configChanged = true
			}
			m.dialogEdit = false
			m.dialogInput = ""
		case "backspace":
			if r := []rune(m.dialogInput); len(r) > 0 {
				m.dialogInput = string(r[:len(r)-1])
			}
		default:
			// isPrintableText is the gate the Ctrl+N field and the palette use,
			// so a control character or a paste marker cannot reach the value.
			if t := msg.Text; t != "" && isPrintableText(t) &&
				len([]rune(m.dialogInput))+len([]rune(t)) <= sandboxImageMax {
				m.dialogInput += t
			}
		}
		return m, nil
	}

	switch key {
	case "esc":
		m.dialog = dialogSettings
		// Back to the row that opened this screen, found by its flag.
		for i, f := range settingsFields() {
			if f.sandboxSettings {
				m.dialogCursor = i
			}
		}
		return m, tea.ClearScreen
	case "up", "k":
		if m.dialogCursor > 0 {
			m.dialogCursor--
		}
	case "down", "j":
		if m.dialogCursor < sandboxSettingsRows-1 {
			m.dialogCursor++
		}
	case "enter", " ", "space", "left", "h", "right", "l":
		switch m.dialogCursor {
		case sandboxSettingsImageRow:
			if key == "enter" || key == " " || key == "space" {
				m.dialogEdit = true
				m.dialogInput = m.cfg.Sandbox.DefaultImage
			}
		case sandboxSettingsSignInRow:
			delta := 1
			if key == "left" || key == "h" {
				delta = -1
			}
			m.setSandboxSignInDefault(stepSandboxChoice(defaultSandboxSignIn(m.cfg.Sandbox), delta))
		}
	}
	return m, nil
}

// sandboxSettingsLabel is this page's own label column. Narrower than
// dialogLabelStyle's 24 because the sign-in row's three choices are 34 cells
// and the box is 54 wide inside: at 24 the row soft-wrapped at every width.
var sandboxSettingsLabel = dialogLabelStyle.Width(15)

// renderSandboxSettingsDialog paints the screen at the standard dialog width.
//
// Every line is clamped to the box's inner width: dialogBorder soft-wraps a
// longer one, which pushes the hint and footer down and moves rows the cursor
// index assumes.
func (m Model) renderSandboxSettingsDialog() string {
	inner := dialogInnerWidth(m.width, dialogWidth)
	const cursorW = 2
	labelW := lipgloss.Width(sandboxSettingsLabel.Render(""))
	valueW := max(4, inner-cursorW-labelW)
	line := func(s string) string { return truncateToWidth(s, inner) + "\n" }
	row := func(i int, label, value string) string {
		cursor, style := "  ", sandboxSettingsLabel
		if i == m.dialogCursor {
			cursor, style = "> ", style.Foreground(lipgloss.Color("230")).Bold(true)
		}
		return line(cursor + style.Render(label) + value)
	}
	hint := func(s string) string {
		return line("    " + dialogSubtle.Render(truncateToWidth(s, inner-4)))
	}

	var b strings.Builder
	b.WriteString(line(dialogTitle.Render("Sandbox")))
	b.WriteString(line(dialogSubtle.Render("  defaults for Ctrl+N · existing panes keep their mode")))
	b.WriteByte('\n')

	// The image is user text that may have come from a hand-edited file, so it
	// is sanitized at render like every other free-text value. While editing,
	// the tail is shown, so the caret stays in view.
	var img string
	switch {
	case m.dialogEdit && m.dialogCursor == sandboxSettingsImageRow:
		img = dialogEditStyle.Render(lastCellsToWidth(sanitizeRemoteText(m.dialogInput), valueW-1) + "│")
	case m.cfg.Sandbox.DefaultImage == "":
		img = dialogSubtle.Render("(none)")
	default:
		img = dialogValStyle.Render(truncateToWidth(sanitizeRemoteText(m.cfg.Sandbox.DefaultImage), valueW))
	}
	b.WriteString(row(sandboxSettingsImageRow, "Default image", img))
	b.WriteString(hint("first image for a host; after that Ctrl+N remembers the last one used there"))

	// All three choices when they fit; on a narrow terminal only the selected
	// one, between arrows — truncating the list would cut off the very choice
	// that is selected.
	cur := defaultSandboxSignIn(m.cfg.Sandbox)
	var opts []string
	label, detail := "", ""
	for _, c := range sandboxAuthChoices {
		mark := "( ) "
		if c.choice == cur {
			mark, label, detail = "(•) ", c.label, c.detail
		}
		opts = append(opts, mark+c.label)
	}
	choices := strings.Join(opts, "  ")
	if lipgloss.Width(choices) > valueW {
		choices = "‹ " + label + " ›"
	}
	b.WriteString(row(sandboxSettingsSignInRow, "Sign-in", dialogValStyle.Render(choices)))
	b.WriteString(hint(detail))

	b.WriteByte('\n')
	b.WriteString(truncateToWidth(dialogSubtle.Render("  ↑↓ navigate  ←/→ change  Enter edit  Esc back"), inner))
	return b.String()
}
