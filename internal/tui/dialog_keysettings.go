package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/keymap"
)

// F1 → Settings → Keys: switch the preset and the prefix without a restart.
//
// Save is LoadBindings → change two fields → WriteBindings, so every
// [bindings] override in the file survives. Apply is cancelSequence, then
// SetBindings with the SAME struct that was written — never initKeymap, which
// rebuilds from the legacy [keybindings] table and drops the preset.
const (
	keysRowPreset = 0
	keysRowPrefix = 1
	keysRowSave   = 2
	keysRows      = 3
)

// keyDraft is the page's unsaved selection. prefix "" means "the preset's own".
//
// overrides and timeout are the FILE's, read when the page opens and after a
// save, because Save re-reads the file: the conflict preview must describe what
// Save will apply, not what was loaded at launch.
type keyDraft struct {
	preset    string
	prefix    string
	overrides map[keymap.ActionID]string
	timeout   time.Duration
}

// maxKeyConflictLines caps the preview so a many-conflict file cannot push the
// Save row off a short terminal.
const maxKeyConflictLines = 6

// keysSavedMsg carries the result of the disk write back to Update, which is
// the only place the keymap may change.
type keysSavedMsg struct {
	b   config.Bindings
	err error
}

// presetUsesPrefix reports whether a preset writes ${prefix}: only then does
// the prefix row mean anything.
func presetUsesPrefix(name string) bool {
	if name == "" || name == keymap.DefaultPresetName {
		return false
	}
	p, err := keymap.LoadPreset(name)
	return err == nil && p.Prefix != ""
}

func (m Model) openKeySettings() (tea.Model, tea.Cmd) {
	// The draft starts from what is LIVE: an unknown preset name in the file
	// fell back to the default, and Save must not write the bad name back.
	b := m.bindings
	if disk, err := config.LoadBindings(); err == nil {
		b = disk
	}
	m.keyDraft = keyDraft{
		preset:    keymap.FromSettings(m.bindings.Settings()).Preset,
		prefix:    m.bindings.Prefix,
		overrides: b.Overrides,
		timeout:   b.SequenceTimeout,
	}
	m.keyStatus = ""
	m.dialog = dialogKeySettings
	m.dialogCursor = keysRowPreset
	m.dialogEdit = false
	m.dialogInput = ""
	return m, tea.ClearScreen
}

// stepPreset cycles the draft through keymap.PresetNames().
func (m *Model) stepPreset(delta int) {
	names := keymap.PresetNames()
	i := 0
	for j, n := range names {
		if n == m.keyDraft.preset {
			i = j
		}
	}
	m.keyDraft.preset = names[(i+delta+len(names))%len(names)]
	m.keyStatus = ""
}

// saveKeysCmd re-reads the file so overrides edited by hand since launch are
// kept, changes only preset and prefix, and writes atomically.
func saveKeysCmd(d keyDraft) tea.Cmd {
	return func() tea.Msg {
		b, err := config.LoadBindings()
		if err != nil {
			return keysSavedMsg{err: fmt.Errorf("bindings.toml is unreadable, not saved: %w", err)}
		}
		// A preset that writes no ${prefix} has nothing to hold one; a stale
		// prefix would silently come back with the next tmux switch.
		prefix := d.prefix
		if !presetUsesPrefix(d.preset) {
			prefix = ""
		}
		b.Preset, b.Prefix = d.preset, prefix
		if err := config.WriteBindings(b); err != nil {
			return keysSavedMsg{err: fmt.Errorf("not saved: %w", err)}
		}
		return keysSavedMsg{b: b}
	}
}

func (m Model) applyKeysSaved(msg keysSavedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.keyStatus = msg.err.Error()
		return m, nil
	}
	m.cancelSequence()
	m.SetBindings(msg.b)
	m.keyDraft.overrides = msg.b.Overrides
	m.keyDraft.timeout = msg.b.SequenceTimeout
	m.keyDraft.prefix = msg.b.Prefix
	m.keyStatus = "saved and applied — other TUIs at their next start, the browser at its next load"
	return m, nil
}

func (m Model) handleKeySettingsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.dialogEdit {
		switch key {
		case "esc":
			m.dialogEdit = false
			m.dialogInput = ""
		case "enter":
			v := strings.TrimSpace(m.dialogInput)
			if v != "" {
				canon, err := keymap.ValidatePrefix(v)
				if err != nil {
					m.keyStatus = "prefix must be one chord: " + err.Error()
					m.dialogEdit = false
					m.dialogInput = ""
					return m, nil
				}
				v = canon
			}
			m.keyDraft.prefix = v
			m.keyStatus = ""
			m.dialogEdit = false
			m.dialogInput = ""
		case "backspace":
			if r := []rune(m.dialogInput); len(r) > 0 {
				m.dialogInput = string(r[:len(r)-1])
			}
		default:
			if t := msg.Text; t != "" && isPrintableText(t) && len([]rune(m.dialogInput)) < 32 {
				m.dialogInput += t
			}
		}
		return m, nil
	}

	prefixLive := presetUsesPrefix(m.keyDraft.preset)
	switch key {
	case "esc":
		m.dialog = dialogSettings
		for i, f := range settingsFields() {
			if f.keysSettings {
				m.dialogCursor = i
			}
		}
		return m, tea.ClearScreen
	case "up", "k":
		m.dialogCursor--
		if m.dialogCursor == keysRowPrefix && !prefixLive {
			m.dialogCursor--
		}
		if m.dialogCursor < 0 {
			m.dialogCursor = 0
		}
	case "down", "j":
		m.dialogCursor++
		if m.dialogCursor == keysRowPrefix && !prefixLive {
			m.dialogCursor++
		}
		if m.dialogCursor >= keysRows {
			m.dialogCursor = keysRows - 1
		}
	case "left", "h":
		if m.dialogCursor == keysRowPreset {
			m.stepPreset(-1)
		}
	case "right", "l", " ", "space":
		if m.dialogCursor == keysRowPreset {
			m.stepPreset(1)
		}
	case "enter":
		switch m.dialogCursor {
		case keysRowPreset:
			m.stepPreset(1)
		case keysRowPrefix:
			if prefixLive {
				m.dialogEdit = true
				m.dialogInput = m.keyDraft.prefix
			}
		case keysRowSave:
			return m, saveKeysCmd(m.keyDraft)
		}
	}
	return m, nil
}

func (m Model) renderKeySettingsDialog() string {
	var b strings.Builder
	b.WriteString(dialogTitle.Render("Keys"))
	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  saving rewrites bindings.toml — comments in it are not kept"))
	b.WriteString("\n\n")

	row := func(i int, label, value string) {
		cursor := "    "
		ls := dialogLabelStyle
		if i == m.dialogCursor {
			cursor = "  > "
			ls = ls.Foreground(lipgloss.Color("230")).Bold(true)
		}
		b.WriteString(cursor + ls.Render(label) + dialogValStyle.Render(value) + "\n")
	}
	row(keysRowPreset, "Preset", "◀ "+m.keyDraft.preset+" ▶")
	switch {
	case !presetUsesPrefix(m.keyDraft.preset):
		row(keysRowPrefix, "Prefix", "— (not used by this preset)")
	case m.dialogEdit && m.dialogCursor == keysRowPrefix:
		row(keysRowPrefix, "Prefix", m.dialogInput+"█")
	case m.keyDraft.prefix == "":
		p, _ := keymap.LoadPreset(m.keyDraft.preset)
		row(keysRowPrefix, "Prefix", p.Prefix+" (preset default)")
	default:
		row(keysRowPrefix, "Prefix", m.keyDraft.prefix)
	}
	row(keysRowSave, "Save and apply", "")

	// The draft's own conflicts, so a clash is visible before it is saved.
	inner := dialogInnerWidth(m.width, dialogWidth)
	draft := keymap.FromSettings(keymap.Settings{
		Preset: m.keyDraft.preset, Prefix: m.keyDraft.prefix,
		Timeout: m.keyDraft.timeout, Overrides: m.keyDraft.overrides,
	})
	if lines := conflictLines(draft.Conflicts, inner-2); len(lines) > 0 {
		b.WriteByte('\n')
		for _, l := range lines {
			b.WriteString("  " + dialogSubtle.Render(l) + "\n")
		}
	}
	if m.keyStatus != "" {
		b.WriteByte('\n')
		b.WriteString("  " + truncateRunes(m.keyStatus, inner-2) + "\n")
	}
	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  ↑↓ move  ←→ preset  Enter edit/save  Esc back"))
	return b.String()
}

// conflictLines renders at most maxKeyConflictLines conflicts, then a count of
// the rest.
func conflictLines(cs []keymap.Conflict, width int) []string {
	var out []string
	for i, c := range cs {
		if i == maxKeyConflictLines {
			out = append(out, fmt.Sprintf("+%d more", len(cs)-maxKeyConflictLines))
			break
		}
		out = append(out, truncateRunes("! "+c.String(), width))
	}
	return out
}
