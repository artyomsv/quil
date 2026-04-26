package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
)

// TestSettingsFields_LabelsAndInitialValues verifies that every Settings
// row exposed in the F1 → Settings dialog has a label and a getter that
// reads the matching cfg field. A typo in the field list would otherwise
// drop a setting silently from the dialog.
func TestSettingsFields_LabelsAndInitialValues(t *testing.T) {
	fields := settingsFields()
	wantLabels := []string{
		"Snapshot interval",
		"Ghost dimmed",
		"Ghost buffer lines",
		"Mouse scroll lines",
		"Page scroll lines",
		"Log level",
		"Show disclaimer",
	}
	if len(fields) != len(wantLabels) {
		t.Fatalf("settingsFields len = %d, want %d", len(fields), len(wantLabels))
	}
	for i, want := range wantLabels {
		if fields[i].label != want {
			t.Errorf("field[%d].label = %q, want %q", i, fields[i].label, want)
		}
	}

	cfg := config.Default()
	m := &Model{cfg: cfg}
	if got := fields[0].get(m); got != cfg.Daemon.SnapshotInterval {
		t.Errorf("Snapshot interval get = %q, want %q", got, cfg.Daemon.SnapshotInterval)
	}
}

// TestHandleSettingsKey_BoolToggle ensures the "Ghost dimmed" boolean field
// flips the cfg value AND sets configChanged so the new value is persisted
// to ~/.quil/config.toml on TUI exit. A regression here is invisible until
// the user closes Quil and finds their setting was silently dropped.
func TestHandleSettingsKey_BoolToggle(t *testing.T) {
	cfg := config.Default()
	cfg.GhostBuffer.Dimmed = false
	m := Model{
		cfg:          cfg,
		dialog:       dialogSettings,
		dialogCursor: 1, // "Ghost dimmed"
	}
	out, _ := m.handleSettingsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got, ok := out.(Model)
	if !ok {
		t.Fatalf("returned model type = %T", out)
	}
	if !got.cfg.GhostBuffer.Dimmed {
		t.Errorf("GhostBuffer.Dimmed not toggled to true")
	}
	if !got.configChanged {
		t.Errorf("configChanged not set — Settings edit would be lost on exit")
	}
}

// TestHandleSettingsKey_EscFromEditor cancels an in-progress string edit
// and clears the input buffer.
func TestHandleSettingsKey_EscFromEditor(t *testing.T) {
	m := Model{
		cfg:          config.Default(),
		dialog:       dialogSettings,
		dialogCursor: 0,
		dialogEdit:   true,
		dialogInput:  "5m",
	}
	out, _ := m.handleSettingsKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	got := out.(Model)
	if got.dialogEdit {
		t.Errorf("dialogEdit still true after Esc")
	}
	if got.dialogInput != "" {
		t.Errorf("dialogInput = %q, want empty", got.dialogInput)
	}
	// Esc inside the editor must NOT mark the config as changed — the user
	// abandoned the edit, so any in-progress value is dropped.
	if got.configChanged {
		t.Errorf("configChanged set after Esc-cancelled edit")
	}
}

// TestHandleSettingsKey_EscReturnsToAbout walks back from the Settings list
// to the parent About dialog rather than closing the dialog stack.
func TestHandleSettingsKey_EscReturnsToAbout(t *testing.T) {
	m := Model{
		cfg:          config.Default(),
		dialog:       dialogSettings,
		dialogCursor: 3,
	}
	out, _ := m.handleSettingsKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	got := out.(Model)
	if got.dialog != dialogAbout {
		t.Errorf("dialog = %v, want dialogAbout", got.dialog)
	}
	if got.dialogCursor != 0 {
		t.Errorf("dialogCursor = %d, want 0 (reset for parent menu)", got.dialogCursor)
	}
}

// TestHandleConfirmKey_CancelPane verifies that 'n' / Esc from a pane-close
// confirm returns the dialog to none without dispatching any IPC message.
func TestHandleConfirmKey_CancelPane(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{
		{Code: tea.KeyEscape},
		{Text: "n"},
	} {
		m := Model{
			dialog:      dialogConfirm,
			confirmKind: "pane",
			confirmID:   "pane-aabbccdd",
		}
		out, cmd := m.handleConfirmKey(key)
		got := out.(Model)
		if got.dialog != dialogNone {
			t.Errorf("key %+v: dialog = %v, want dialogNone", key, got.dialog)
		}
		if cmd != nil {
			t.Errorf("key %+v: cancel must not return a Cmd", key)
		}
	}
}
