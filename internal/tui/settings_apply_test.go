package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

func TestApplySetting_RefusedAndUnchangedAreNotChanges(t *testing.T) {
	m := Model{cfg: config.Default()}
	if m.applySetting("ui.scrollback_lines", "0") || m.configChanged {
		t.Fatal("a refused value marked the config changed")
	}
	m.cfg.UI.UnfocusedDim = 1.5 // hand-edited; the row shows 0.90
	if m.applySetting("ui.unfocused_dim", "0.90") || m.configChanged || m.cfg.UI.UnfocusedDim != 1.5 {
		t.Fatal("accepting the displayed dim level rewrote the config")
	}
	if !m.applySetting("ui.scrollback_lines", "5000") || !m.configChanged || m.cfg.UI.ScrollbackLines != 5000 {
		t.Fatal("an accepted value was not stored")
	}
	if m.applySetting("no.such.key", "1") {
		t.Fatal("an unknown key reported a change")
	}
}

// The sign-in row reads "token" for the forbidden token + shared pair, so a
// Get compare would skip the repair; the stored value decides instead.
func TestApplySetting_SignInRepairsTokenPlusShared(t *testing.T) {
	m := Model{cfg: config.Default()}
	m.cfg.Sandbox.Auth = string(config.SandboxAuthToken)
	m.cfg.Sandbox.SharedClaudeConfig = true
	if !m.applySetting("sandbox.sign_in", "token") || !m.configChanged || m.cfg.Sandbox.SharedClaudeConfig {
		t.Fatalf("token over token+shared: changed=%v cfg=%+v", m.configChanged, m.cfg.Sandbox)
	}
}
