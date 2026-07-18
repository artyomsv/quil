package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// TestSettingsFields_LabelsAndInitialValues verifies that every Settings
// row exposed in the F1 → Settings dialog has a label and a getter that
// reads the matching cfg field. A typo in the field list would otherwise
// drop a setting silently from the dialog.
func TestSettingsFields_LabelsAndInitialValues(t *testing.T) {
	t.Parallel()
	fields := settingsFields()
	wantLabels := []string{
		"Snapshot interval",
		"Ghost dimmed",
		"Ghost buffer lines",
		"Mouse scroll lines",
		"Page scroll lines",
		"Log level",
		"Show disclaimer",
		"Update check",
		"Update auto-download",
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

// TestRenderAboutDialog_HasStopDaemon verifies "Stop daemon" lives on the F1
// → About (root) menu (promoted out of the nested Settings list). A
// regression dropping it would leave no in-TUI way to stop the daemon.
func TestRenderAboutDialog_HasStopDaemon(t *testing.T) {
	t.Parallel()
	m := Model{cfg: config.Default()}
	got := m.renderAboutDialog()
	if !strings.Contains(got, "Stop daemon") {
		t.Errorf("About dialog missing %q row:\n%s", "Stop daemon", got)
	}
}

// TestRenderAboutDialog_StopDaemonAtIndex guards against drift between the
// hardcoded aboutStopDaemonIndex / case ladder in handleAboutKey and the
// items slice rendered by renderAboutDialog. The order lives in two places
// (the render slice and the index constant + Enter case), so inserting a new
// row before "Stop daemon" would shift the rendered row while the index
// constant still routes Enter to position 7 — Enter would then fire on the
// wrong action. Rendering with the cursor on aboutStopDaemonIndex must
// highlight the "Stop daemon" row, pinning the two in sync.
func TestRenderAboutDialog_StopDaemonAtIndex(t *testing.T) {
	t.Parallel()
	m := Model{cfg: config.Default(), dialogCursor: aboutStopDaemonIndex}
	got := m.renderAboutDialog()

	var selected string
	for _, line := range strings.Split(got, "\n") {
		// The selected row is the only one prefixed with the "> " cursor
		// marker (others get "  "); the marker is plain text written before
		// the styled label, so it survives lipgloss rendering.
		if strings.Contains(line, "> ") {
			selected = line
			break
		}
	}
	if selected == "" {
		t.Fatalf("no selected (\"> \") row found in About dialog:\n%s", got)
	}
	if !strings.Contains(selected, "Stop daemon") {
		t.Errorf("cursor at aboutStopDaemonIndex=%d highlights %q, want the Stop daemon row", aboutStopDaemonIndex, selected)
	}
}

// TestHandleAboutKey_StopDaemonOpensConfirm verifies that pressing Enter on
// the Stop daemon row (root menu) routes to the confirmation dialog rather
// than directly sending MsgShutdown. Without the confirm step, a misclick
// would terminate the TUI + every pane child with no chance to abort.
func TestHandleAboutKey_StopDaemonOpensConfirm(t *testing.T) {
	t.Parallel()
	m := Model{
		cfg:          config.Default(),
		dialog:       dialogAbout,
		dialogCursor: aboutStopDaemonIndex,
	}
	out, cmd := m.handleAboutKey(tea.KeyPressMsg{Code: tea.KeyEnter})
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

// TestHandleConfirmKey_StopDaemonEscReturnsToAbout keeps the user on the
// About (root) menu, cursor on Stop daemon, when they back out of the
// confirm. Returning to dialogNone — the default for confirm Esc — would
// drop the user back to the workspace and lose the menu they were in.
func TestHandleConfirmKey_StopDaemonEscReturnsToAbout(t *testing.T) {
	t.Parallel()
	m := Model{
		dialog:      dialogConfirm,
		confirmKind: confirmKindShutdown,
	}
	out, cmd := m.handleConfirmKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	got := out.(Model)
	if got.dialog != dialogAbout {
		t.Errorf("dialog = %v, want dialogAbout", got.dialog)
	}
	if got.dialogCursor != aboutStopDaemonIndex {
		t.Errorf("dialogCursor = %d, want %d (Stop daemon About row)", got.dialogCursor, aboutStopDaemonIndex)
	}
	if cmd != nil {
		t.Errorf("cancel must not return a Cmd")
	}
}

// TestRenderConfirmDialog_StopDaemonMessage locks in the exact warning text
// the user sees before confirming. The "this TUI window will close" line is
// the load-bearing piece — without it, users hit `y` expecting the daemon
// to stop in the background, then act surprised when their session ends.
// The "y confirm" footer is also tested: it differs from the generic
// "Enter confirm" so users can't accept by finger memory after toggling.
func TestRenderConfirmDialog_StopDaemonMessage(t *testing.T) {
	t.Parallel()
	m := Model{
		dialog:      dialogConfirm,
		confirmKind: confirmKindShutdown,
	}
	got := m.renderConfirmDialog()
	wants := []string{"Stop the daemon?", "TUI window will close", "y confirm", "Esc cancel"}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("confirm dialog missing %q\nrendered:\n%s", want, got)
		}
	}
	// Negative assertion: the shutdown confirm must NOT render "Enter
	// confirm" — that's what allowed accidental Enter to commit shutdown.
	if strings.Contains(got, "Enter confirm") {
		t.Errorf("shutdown confirm still shows 'Enter confirm' footer — Enter must not be advertised as an accept key for this kind\nrendered:\n%s", got)
	}
}

// TestHandleConfirmKey_StopDaemonEnterIsNoOp guards the UX hardening: Enter
// is the universal "accept" in every other confirm (pane / tab / instance),
// but in Stop daemon we explicitly reject it so finger memory after
// editing toggles cannot kill the daemon. Without this guard, the user's
// expectation that Enter accepts a confirm would override the much higher
// stakes of "stop the daemon and all pane children."
func TestHandleConfirmKey_StopDaemonEnterIsNoOp(t *testing.T) {
	t.Parallel()
	m := Model{
		dialog:      dialogConfirm,
		confirmKind: confirmKindShutdown,
	}
	out, cmd := m.handleConfirmKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := out.(Model)
	if got.dialog != dialogConfirm {
		t.Errorf("dialog = %v, want dialogConfirm (Enter on shutdown must NOT accept)", got.dialog)
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil — Enter must be a no-op on the shutdown confirm", cmd)
	}
}

// TestHandleConfirmKey_StopDaemonYSendsAndQuits is the critical missing
// coverage from the original PR: it exercises the path that actually fires
// MsgShutdown over IPC and returns tea.Quit. A regression where someone
// removes the Send call (or swaps tea.Quit for nil) would let the user
// click confirm, see the TUI close, and discover later that the daemon is
// still alive — silent, expensive failure mode.
func TestHandleConfirmKey_StopDaemonYSendsAndQuits(t *testing.T) {
	t.Parallel()
	fake := &fakeSender{}
	m := Model{
		client:      fake,
		dialog:      dialogConfirm,
		confirmKind: confirmKindShutdown,
	}
	out, cmd := m.handleConfirmKey(tea.KeyPressMsg{Text: "y"})
	got := out.(Model)
	if got.dialog != dialogNone {
		t.Errorf("dialog = %v, want dialogNone", got.dialog)
	}
	// Send must have happened synchronously — the message is on the wire
	// before handleConfirmKey returns control to the runtime. This is the
	// guarantee that closes the tea.Batch race the original PR had.
	if len(fake.sent) != 1 {
		t.Fatalf("fake.sent len = %d, want 1 (MsgShutdown must be sent synchronously)", len(fake.sent))
	}
	if fake.sent[0].Type != ipc.MsgShutdown {
		t.Errorf("sent[0].Type = %q, want %q", fake.sent[0].Type, ipc.MsgShutdown)
	}
	// tea.Quit must be returned so the program loop exits. We verify by
	// invoking the returned Cmd and asserting on the message type.
	if cmd == nil {
		t.Fatalf("cmd is nil — expected tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("cmd() returned %T, want tea.QuitMsg", cmd())
	}
}

// TestHandleConfirmKey_StopDaemonYWithSendErrorStillQuits guards the "fail
// open" contract documented in the source comment: even when the IPC Send
// errors (stale socket, daemon already crashed, etc.), the TUI still
// quits. The operator explicitly asked to stop; surfacing a partial-failure
// dialog after the user's deliberate confirm would be more confusing than
// the silent best-effort path.
func TestHandleConfirmKey_StopDaemonYWithSendErrorStillQuits(t *testing.T) {
	t.Parallel()
	fake := &fakeSender{sendErr: errors.New("socket closed")}
	m := Model{
		client:      fake,
		dialog:      dialogConfirm,
		confirmKind: confirmKindShutdown,
	}
	out, cmd := m.handleConfirmKey(tea.KeyPressMsg{Text: "y"})
	got := out.(Model)
	if got.dialog != dialogNone {
		t.Errorf("dialog = %v, want dialogNone", got.dialog)
	}
	if len(fake.sent) != 1 {
		t.Errorf("Send should have been attempted exactly once, got %d", len(fake.sent))
	}
	if cmd == nil {
		t.Fatalf("cmd is nil — tea.Quit must fire even when Send fails")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("cmd() returned %T, want tea.QuitMsg", cmd())
	}
}

// TestHandleConfirmKey_StopDaemonYWithNilClientStillQuits guards the
// defensive nil-check. The current main.go flow never produces a nil
// client at NewModel time (connect failure os.Exits before NewModel), but
// the guard exists so a future refactor permitting a delayed-attach
// pattern doesn't introduce a panic mid-shutdown.
func TestHandleConfirmKey_StopDaemonYWithNilClientStillQuits(t *testing.T) {
	t.Parallel()
	m := Model{
		client:      nil,
		dialog:      dialogConfirm,
		confirmKind: confirmKindShutdown,
	}
	out, cmd := m.handleConfirmKey(tea.KeyPressMsg{Text: "y"})
	got := out.(Model)
	if got.dialog != dialogNone {
		t.Errorf("dialog = %v, want dialogNone", got.dialog)
	}
	if cmd == nil {
		t.Fatalf("cmd is nil — tea.Quit must fire even when client is nil")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("cmd() returned %T, want tea.QuitMsg", cmd())
	}
}

// TestHandleSettingsKey_BoolToggle ensures the "Ghost dimmed" boolean field
// flips the cfg value AND sets configChanged so the new value is persisted
// to ~/.quil/config.toml on TUI exit. A regression here is invisible until
// the user closes Quil and finds their setting was silently dropped.
func TestHandleSettingsKey_BoolToggle(t *testing.T) {
	t.Parallel()
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

// TestHandleSettingsKey_UpdateTogglesFlipCfgAndPersist covers the two rows
// added for [update] check/auto — mirrors TestHandleSettingsKey_BoolToggle
// so a regression that drops either toggle's set/configChanged wiring is
// caught the same way the Ghost-dimmed regression would be.
func TestHandleSettingsKey_UpdateTogglesFlipCfgAndPersist(t *testing.T) {
	t.Parallel()
	fields := settingsFields()
	checkIdx, autoIdx := -1, -1
	for i, f := range fields {
		switch f.label {
		case "Update check":
			checkIdx = i
		case "Update auto-download":
			autoIdx = i
		}
	}
	if checkIdx == -1 || autoIdx == -1 {
		t.Fatalf("settingsFields missing Update rows: checkIdx=%d autoIdx=%d", checkIdx, autoIdx)
	}

	cfg := config.Default()
	cfg.Update.Check = false
	m := Model{cfg: cfg, dialog: dialogSettings, dialogCursor: checkIdx}
	out, _ := m.handleSettingsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got, ok := out.(Model)
	if !ok {
		t.Fatalf("returned model type = %T", out)
	}
	if !got.cfg.Update.Check {
		t.Error("Update.Check not toggled to true")
	}
	if !got.configChanged {
		t.Error("configChanged not set for Update.Check toggle")
	}

	cfg2 := config.Default()
	cfg2.Update.Auto = false
	m2 := Model{cfg: cfg2, dialog: dialogSettings, dialogCursor: autoIdx}
	out2, _ := m2.handleSettingsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got2, ok := out2.(Model)
	if !ok {
		t.Fatalf("returned model type = %T", out2)
	}
	if !got2.cfg.Update.Auto {
		t.Error("Update.Auto not toggled to true")
	}
	if !got2.configChanged {
		t.Error("configChanged not set for Update.Auto toggle")
	}
}

// TestHandleSettingsKey_EscFromEditor cancels an in-progress string edit
// and clears the input buffer.
func TestHandleSettingsKey_EscFromEditor(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// fakeSender is a tuiClient stub for handler tests. It records every Send
// call and returns a caller-supplied error so we can exercise both the
// happy path and the "Send failed but we still quit" path. Receive is a
// no-op — the shutdown handler never reads from the wire.
type fakeSender struct {
	sent    []*ipc.Message
	sendErr error
}

func (f *fakeSender) Send(m *ipc.Message) error {
	f.sent = append(f.sent, m)
	return f.sendErr
}

func (f *fakeSender) Receive() (*ipc.Message, error) {
	return nil, nil
}
