package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// settingsFieldByLabel is already defined in sidebar_width_settings_test.go
// (same package) — reused here rather than redeclared.

func TestSettings_OverlayRowsEditAndFlagConfigChanged(t *testing.T) {
	m := &Model{cfg: config.Default(), client: newFakeConn()}

	idle := settingsFieldByLabel(t, "Overlay idle timeout (min)")
	idle.set(m, "9")
	if m.cfg.Overlay.IdleTimeoutMinutes != 9 {
		t.Errorf("IdleTimeoutMinutes = %d, want 9", m.cfg.Overlay.IdleTimeoutMinutes)
	}
	if !m.configChanged {
		t.Error("editing the row did not flag configChanged; the edit would be lost on exit")
	}

	m.configChanged = false
	capRow := settingsFieldByLabel(t, "Max live overlays")
	capRow.set(m, "3")
	if m.cfg.Overlay.MaxLive != 3 {
		t.Errorf("MaxLive = %d, want 3", m.cfg.Overlay.MaxLive)
	}
	if !m.configChanged {
		t.Error("editing the cap row did not flag configChanged")
	}
}

// Negative and non-numeric input must be refused, not stored: a negative
// timeout would make every overlay instantly expired.
func TestSettings_OverlayRowsRefuseInvalidInput(t *testing.T) {
	m := &Model{cfg: config.Default(), client: newFakeConn()}
	idle := settingsFieldByLabel(t, "Overlay idle timeout (min)")
	for _, bad := range []string{"-1", "abc", ""} {
		idle.set(m, bad)
		if m.cfg.Overlay.IdleTimeoutMinutes != 5 {
			t.Fatalf("input %q stored %d; want the default 5 retained", bad, m.cfg.Overlay.IdleTimeoutMinutes)
		}
	}
}

func TestOverlayPolicyCmd_SendsTheCurrentSettings(t *testing.T) {
	conn := newFakeConn()
	m := &Model{cfg: config.Default(), client: conn}
	m.cfg.Overlay.IdleTimeoutMinutes = 4
	m.cfg.Overlay.MaxLive = 2

	cmd := m.overlayPolicyCmd()
	if cmd == nil {
		t.Fatal("no policy command produced")
	}
	cmd()

	for _, sent := range conn.sent {
		if sent.Type != ipc.MsgOverlayPolicy {
			continue
		}
		var p ipc.OverlayPolicyPayload
		if err := sent.DecodePayload(&p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if p.IdleTimeoutMinutes != 4 || p.MaxLive != 2 {
			t.Errorf("payload = %+v, want {4 2}", p)
		}
		return
	}
	t.Fatal("no overlay_policy message sent")
}

// The push must not fire on every keystroke while editing — only when a
// value is actually committed with Enter.
func TestHandleSettingsKey_OverlayEditCommitPushesPolicy(t *testing.T) {
	fields := settingsFields()
	idx := -1
	for i, f := range fields {
		if f.label == "Overlay idle timeout (min)" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("no \"Overlay idle timeout (min)\" row in settingsFields()")
	}

	conn := newFakeConn()
	m := Model{
		cfg:    config.Default(),
		client: conn,
		dialog: dialogSettings, dialogEdit: true,
		dialogCursor: idx, dialogInput: "9",
	}

	// Typing a digit while editing must not push anything.
	_, cmd := m.handleSettingsKey(tea.KeyPressMsg{Code: '9', Text: "9"})
	if cmd != nil {
		cmd()
	}
	if conn.sentCount() != 0 {
		t.Fatalf("typing pushed %d messages, want 0", conn.sentCount())
	}

	out, cmd := m.handleSettingsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := out.(Model)
	if got.cfg.Overlay.IdleTimeoutMinutes != 9 {
		t.Fatalf("IdleTimeoutMinutes = %d, want 9", got.cfg.Overlay.IdleTimeoutMinutes)
	}
	if cmd == nil {
		t.Fatal("commit produced no command — the daemon would not learn the new policy")
	}
	cmd()

	found := false
	for _, sent := range conn.sent {
		if sent.Type == ipc.MsgOverlayPolicy {
			found = true
		}
	}
	if !found {
		t.Error("Enter commit did not push overlay_policy")
	}
}
