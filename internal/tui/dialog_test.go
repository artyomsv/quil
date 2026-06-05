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
		"Stop daemon",
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

// TestSettingsFields_StopDaemonIsAction verifies that the "Stop daemon" row
// is an action row (action!=nil, set==nil, isBool==false). A regression
// where the action gets dropped would render the row as a plain editable
// field — pressing Enter would open the inline editor with no obvious way
// to actually stop the daemon.
func TestSettingsFields_StopDaemonIsAction(t *testing.T) {
	fields := settingsFields()
	stop := fields[len(fields)-1]
	if stop.label != "Stop daemon" {
		t.Fatalf("last field label = %q, want %q (test assumes Stop daemon is last)", stop.label, "Stop daemon")
	}
	if stop.action == nil {
		t.Errorf("Stop daemon row has nil action — Enter would open inline editor instead")
	}
	if stop.set != nil {
		t.Errorf("Stop daemon row has non-nil set — action rows ignore set but a stray setter is a smell")
	}
	if stop.isBool {
		t.Errorf("Stop daemon row marked isBool — would make it look like a toggle")
	}
	// Description text must convey the consequence so the user understands
	// before pressing Enter that the TUI window is affected.
	if got := stop.get(nil); got == "" {
		t.Errorf("Stop daemon get() returned empty — user has no hint about consequence")
	}
}

// TestHandleSettingsKey_StopDaemonOpensConfirm verifies that pressing Enter
// on the Stop daemon row routes to the confirmation dialog (rather than
// directly sending MsgShutdown). Without the confirm step, a misclick
// would terminate the TUI + every pane child with no chance to abort.
func TestHandleSettingsKey_StopDaemonOpensConfirm(t *testing.T) {
	fields := settingsFields()
	stopIdx := len(fields) - 1
	m := Model{
		cfg:          config.Default(),
		dialog:       dialogSettings,
		dialogCursor: stopIdx,
	}
	out, cmd := m.handleSettingsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := out.(Model)
	if got.dialog != dialogConfirm {
		t.Errorf("dialog = %v, want dialogConfirm", got.dialog)
	}
	if got.confirmKind != confirmKindShutdown {
		t.Errorf("confirmKind = %q, want %q", got.confirmKind, confirmKindShutdown)
	}
	if cmd != nil {
		t.Errorf("opening confirm should not emit a Cmd; got %v", cmd)
	}
	if got.configChanged {
		t.Errorf("configChanged set — opening confirm must not mutate persistent state")
	}
}

// TestHandleConfirmKey_StopDaemonEscReturnsToSettings keeps the user in the
// Settings menu (cursor on Stop daemon) when they back out of the confirm.
// Returning to dialogNone — which is the default for confirm Esc — would
// drop the user back to the workspace and lose the menu they were in.
func TestHandleConfirmKey_StopDaemonEscReturnsToSettings(t *testing.T) {
	m := Model{
		dialog:      dialogConfirm,
		confirmKind: confirmKindShutdown,
	}
	out, cmd := m.handleConfirmKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	got := out.(Model)
	if got.dialog != dialogSettings {
		t.Errorf("dialog = %v, want dialogSettings", got.dialog)
	}
	wantCursor := len(settingsFields()) - 1
	if got.dialogCursor != wantCursor {
		t.Errorf("dialogCursor = %d, want %d (last row, Stop daemon)", got.dialogCursor, wantCursor)
	}
	if cmd != nil {
		t.Errorf("cancel must not return a Cmd")
	}
}

// TestRenderConfirmDialog_StopDaemonMessage locks in the exact warning text
// the user sees before confirming. The "this TUI window will close" line is
// the load-bearing piece — without it, users hit Enter expecting the daemon
// to stop in the background, then act surprised when their session ends.
func TestRenderConfirmDialog_StopDaemonMessage(t *testing.T) {
	m := Model{
		dialog:      dialogConfirm,
		confirmKind: confirmKindShutdown,
	}
	got := m.renderConfirmDialog()
	for _, want := range []string{"Stop the daemon?", "TUI window will close", "Enter confirm", "Esc cancel"} {
		if !contains(got, want) {
			t.Errorf("confirm dialog missing %q\nrendered:\n%s", want, got)
		}
	}
}

// contains is a tiny helper avoiding strings import noise in tests that
// just need a substring check.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
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
