package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/config"
)

// openSandboxSettings drives F1 → Settings → Sandbox through the real Enter
// handler, so a row that stopped opening the page fails here.
func openSandboxSettings(t *testing.T, cfg config.Config) Model {
	t.Helper()
	m := Model{cfg: cfg, dialog: dialogSettings}
	m.width, m.height = 120, 40
	found := false
	for i, f := range settingsFields() {
		if f.sandboxSettings {
			m.dialogCursor, found = i, true
		}
	}
	if !found {
		t.Fatal("no Sandbox row in F1 → Settings")
	}
	upd, _ := m.handleSettingsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = upd.(Model)
	if m.dialog != dialogSandboxSettings {
		t.Fatalf("Enter on the Sandbox row opened dialog %v", m.dialog)
	}
	return m
}

func pressSandboxSettings(t *testing.T, m Model, k tea.KeyPressMsg) Model {
	t.Helper()
	upd, _ := m.handleSandboxSettingsKey(k)
	return upd.(Model)
}

// Each choice writes BOTH keys, so the page can never write the mixed
// token + shared pair that lets a token pane and a browser pane share one
// directory and re-onboard each other.
func TestSandboxSettings_SignInWritesBothKeys(t *testing.T) {
	m := openSandboxSettings(t, config.Default())
	m.dialogCursor = sandboxSettingsSignInRow
	want := []struct {
		auth   string
		shared bool
	}{{"browser", true}, {"token", false}, {"browser", false}}
	for _, w := range want {
		m.configChanged = false
		m = pressSandboxSettings(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
		if m.cfg.Sandbox.Auth != w.auth || m.cfg.Sandbox.SharedClaudeConfig != w.shared {
			t.Errorf("got auth=%q shared=%v, want %q %v", m.cfg.Sandbox.Auth, m.cfg.Sandbox.SharedClaudeConfig, w.auth, w.shared)
		}
		if !m.configChanged {
			t.Error("configChanged not set — the edit is lost on exit")
		}
	}
}

// A hand-edited token + shared config shows Token (ResolveAuth decides), and
// stepping from it writes both keys.
func TestSandboxSettings_LegacyTokenSharedShowsToken(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox.Auth, cfg.Sandbox.SharedClaudeConfig = "token", true
	m := openSandboxSettings(t, cfg)
	if frame := stripANSI(m.renderSandboxSettingsDialog()); !strings.Contains(frame, "(•) Token") {
		t.Errorf("legacy pair not shown as Token:\n%s", frame)
	}
	m.dialogCursor = sandboxSettingsSignInRow
	m = pressSandboxSettings(t, m, tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.cfg.Sandbox.Auth != "browser" || !m.cfg.Sandbox.SharedClaudeConfig {
		t.Errorf("left from Token gave auth=%q shared=%v, want Shared", m.cfg.Sandbox.Auth, m.cfg.Sandbox.SharedClaudeConfig)
	}
}

func TestSandboxSettings_DefaultImageEdit(t *testing.T) {
	m := openSandboxSettings(t, config.Default())
	m.dialogCursor = sandboxSettingsImageRow
	m = pressSandboxSettings(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	for _, r := range "img:1" {
		m = pressSandboxSettings(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m = pressSandboxSettings(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.cfg.Sandbox.DefaultImage != "img:1" || !m.configChanged {
		t.Errorf("default_image = %q changed=%v", m.cfg.Sandbox.DefaultImage, m.configChanged)
	}
}

// Esc while editing cancels the edit and keeps the stored value.
func TestSandboxSettings_EscCancelsTheImageEdit(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox.DefaultImage = "keep:1"
	m := openSandboxSettings(t, cfg)
	m.dialogCursor = sandboxSettingsImageRow
	m = pressSandboxSettings(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = pressSandboxSettings(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = pressSandboxSettings(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.cfg.Sandbox.DefaultImage != "keep:1" || m.configChanged || m.dialog != dialogSandboxSettings {
		t.Errorf("Esc in edit: image=%q changed=%v dialog=%v", m.cfg.Sandbox.DefaultImage, m.configChanged, m.dialog)
	}
}

func TestSandboxSettings_EscReturnsToTheSandboxRow(t *testing.T) {
	m := openSandboxSettings(t, config.Default())
	m = pressSandboxSettings(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.dialog != dialogSettings || !settingsFields()[m.dialogCursor].sandboxSettings {
		t.Errorf("Esc landed on dialog %v row %d", m.dialog, m.dialogCursor)
	}
}

// The page is drawn through the dialog dispatcher, not only by direct call.
func TestSandboxSettings_RenderedThroughTheDialog(t *testing.T) {
	m := openSandboxSettings(t, config.Default())
	frame := stripANSI(m.renderDialog())
	for _, want := range []string{"Sandbox", "Default image", "Sign-in", "(•) Browser"} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame lacks %q:\n%s", want, frame)
		}
	}
}

// Every content line must fit the box's inner width, or dialogBorder
// soft-wraps it: the sign-in choices broke onto a second line and pushed the
// hint and footer down. Checked at the minimum width too, in editing state,
// and for each choice (their detail lines differ in length).
func TestRenderSandboxSettingsDialog_EveryLineFitsTheBox(t *testing.T) {
	for _, width := range []int{40, 60, 100} {
		for _, choice := range []string{"browser", "shared", "token"} {
			for _, editing := range []bool{false, true} {
				cfg := config.Default()
				cfg.Sandbox.DefaultImage = strings.Repeat("registry.example/very-long-image-name:", 3) + "tag"
				m := Model{cfg: cfg, dialog: dialogSandboxSettings}
				m.width, m.height = width, 30
				m.setSandboxSignInDefault(choice)
				if editing {
					m.dialogEdit, m.dialogInput = true, cfg.Sandbox.DefaultImage
				}
				inner := dialogInnerWidth(m.width, dialogWidth)
				for i, line := range strings.Split(m.renderSandboxSettingsDialog(), "\n") {
					if w := lipgloss.Width(line); w > inner {
						t.Errorf("width %d, %s, editing=%v: line %d is %d cells, box inner is %d: %q",
							width, choice, editing, i, w, inner, stripANSI(line))
					}
				}
			}
		}
	}
}
