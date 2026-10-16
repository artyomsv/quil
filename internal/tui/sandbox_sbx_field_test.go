package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
	"github.com/artyomsv/quil/internal/sbx"
)

// A destination with sbx alone still offers the row, on the sbx backend.
func TestResetSandboxField_PicksTheBackendTheDestinationOffers(t *testing.T) {
	tests := []struct {
		name            string
		cap             ipc.SandboxCapRespPayload
		cfgBackend      string
		wantAvail       bool
		wantSbxSelected bool
	}{
		{"docker only", ipc.SandboxCapRespPayload{Available: true}, "", true, false},
		{"docker only, config asks sbx", ipc.SandboxCapRespPayload{Available: true}, "sbx", true, false},
		{"sbx only", ipc.SandboxCapRespPayload{SbxAvailable: true}, "", true, true},
		{"both, default docker", ipc.SandboxCapRespPayload{Available: true, SbxAvailable: true}, "", true, false},
		{"both, config sbx", ipc.SandboxCapRespPayload{Available: true, SbxAvailable: true}, "sbx", true, true},
		{"neither", ipc.SandboxCapRespPayload{}, "sbx", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m Model
			m.cfg.Sandbox.Backend = tt.cfgBackend
			m.applySandboxCap("", tt.cap)
			m.resetSandboxField("")
			if m.sandboxDialogAvail != tt.wantAvail {
				t.Errorf("avail = %v, want %v", m.sandboxDialogAvail, tt.wantAvail)
			}
			if m.sandboxBackendIsSbx() != tt.wantSbxSelected {
				t.Errorf("sbx selected = %v, want %v", m.sandboxBackendIsSbx(), tt.wantSbxSelected)
			}
		})
	}
}

// An sbx pane sends the backend and nothing else: the daemon refuses an sbx
// spec that carries an image or a sign-in mode.
func TestSandboxSpec_SbxCarriesOnlyTheBackend(t *testing.T) {
	var m Model
	m.sandboxOn = true
	m.sandboxBackend = config.SandboxBackendSbx
	m.sandboxImage = "img:1"
	spec := m.sandboxSpec()
	if spec == nil || spec.Backend != "sbx" || spec.Image != "" || spec.Auth != "" || spec.ClaudeConfig != "" {
		t.Fatalf("spec = %+v", spec)
	}
	if err := m.sandboxSubmitError(); err != "" {
		t.Errorf("an sbx row with no image refused to submit: %q", err)
	}
	if m.showSandboxAuthField(promptsCWDPlugin()) {
		t.Error("the sign-in row is offered for an sbx pane")
	}
}

func TestSandboxRow_ArrowsSwitchOnlyWhenBothAreOffered(t *testing.T) {
	var m Model
	m.sandboxOn = true
	m.sandboxDialogDocker = true
	m.sandboxBackend = config.SandboxBackendDocker
	if m.handleSandboxFieldKey(tea.KeyPressMsg{Code: tea.KeyRight}) {
		t.Error("→ was taken with only docker offered")
	}
	m.sandboxDialogSbx = true
	if !m.handleSandboxFieldKey(tea.KeyPressMsg{Code: tea.KeyRight}) || !m.sandboxBackendIsSbx() {
		t.Error("→ did not switch to sbx")
	}
	// Typing is not taken on an sbx row: there is no field to type into.
	if m.handleSandboxFieldKey(tea.KeyPressMsg{Code: 'a', Text: "a"}) {
		t.Error("typing was taken by an sbx row")
	}
	if !m.handleSandboxFieldKey(tea.KeyPressMsg{Code: tea.KeyLeft}) || m.sandboxBackendIsSbx() {
		t.Error("← did not switch back to docker")
	}
	out := m.renderSetupSandboxField(true)
	if !strings.Contains(out, "←/→ sbx") {
		t.Errorf("the row does not name the other backend:\n%s", out)
	}
}

// With sbx selected the row names sbx, offers Docker as the other backend,
// points at the profile instead of an image field, and shows a refusal.
func TestSandboxRow_SbxRendersTheProfileNotAnImage(t *testing.T) {
	m := Model{pluginRegistry: plugin.NewRegistry()}
	m.sandboxOn = true
	m.sandboxDialogDocker = true
	m.sandboxDialogSbx = true
	m.sandboxBackend = config.SandboxBackendSbx
	m.sandboxErr = "no profile here"
	out := m.renderSetupSandboxField(true)
	for _, want := range []string{"Run in an sbx sandbox", "←/→ Docker", sbx.ProfileFile, "no profile here"} {
		if !strings.Contains(out, want) {
			t.Errorf("row does not contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "image") {
		t.Errorf("an sbx row shows an image field:\n%s", out)
	}
}
