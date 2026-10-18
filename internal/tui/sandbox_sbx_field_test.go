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

// attachModel is a dialog on a destination with docker, sbx and two existing
// sandboxes, one of which mounts the browsed folder.
func attachModel() Model {
	m := Model{pluginRegistry: plugin.NewRegistry()}
	m.applySandboxCap("", ipc.SandboxCapRespPayload{Available: true, SbxAvailable: true, SbxSandboxes: []ipc.SbxSandbox{
		{Name: "ticket-1", Status: "stopped", Folders: []string{"/w/ticket-1"}},
		{Name: "ticket-2", Status: "running", Folders: []string{"/w/ticket-2"}},
		{Name: "-evil", Folders: []string{"/w"}},
	}})
	m.resetSandboxField("")
	m.sandboxOn = true
	m.cwdBrowseDir = "/w/ticket-1/app"
	return m
}

// Only sandboxes that mount the folder are offered, and never a name sbx
// would read as a flag.
func TestSandboxRow_AttachOffersOnlyTheSandboxesThatMountTheFolder(t *testing.T) {
	m := attachModel()
	got := m.sandboxChoices()
	want := []sandboxChoice{{backend: "docker"}, {backend: "sbx"}, {backend: "sbx", name: "ticket-1"}}
	if len(got) != len(want) {
		t.Fatalf("choices = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("choice %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// No folder chosen yet: every usable sandbox is offered.
	m.cwdBrowseDir = ""
	if n := len(m.sbxAttachChoices()); n != 2 {
		t.Errorf("with no folder, %d sandboxes offered, want 2", n)
	}
}

// → walks Docker, the profile, then the sandbox; the spec carries its name.
func TestSandboxRow_ArrowsReachAnExistingSandbox(t *testing.T) {
	m := attachModel()
	m.sandboxBackend = config.SandboxBackendDocker
	for i := 0; i < 2; i++ {
		if !m.handleSandboxFieldKey(tea.KeyPressMsg{Code: tea.KeyRight}) {
			t.Fatal("→ was not taken")
		}
	}
	if !m.sandboxBackendIsSbx() || m.sandboxSbxName != "ticket-1" {
		t.Fatalf("backend=%q name=%q, want sbx ticket-1", m.sandboxBackend, m.sandboxSbxName)
	}
	spec := m.sandboxSpec()
	if spec == nil || spec.Backend != "sbx" || spec.Name != "ticket-1" || spec.Image != "" {
		t.Fatalf("spec = %+v", spec)
	}
	out := m.renderSetupSandboxField(true)
	for _, w := range []string{"attach: existing sandbox ticket-1 (stopped)", "←/→ Docker"} {
		if !strings.Contains(out, w) {
			t.Errorf("row does not contain %q:\n%s", w, out)
		}
	}
	if !m.handleSandboxFieldKey(tea.KeyPressMsg{Code: tea.KeyLeft}) || m.sandboxSbxName != "" || !m.sandboxBackendIsSbx() {
		t.Errorf("← did not go back to the profile: backend=%q name=%q", m.sandboxBackend, m.sandboxSbxName)
	}
}

// A sandbox picked before the folder changed is refused at submit, with the
// way out named.
func TestSandboxSubmitError_AttachAfterTheFolderMoved(t *testing.T) {
	m := attachModel()
	m.sandboxBackend, m.sandboxSbxName = config.SandboxBackendSbx, "ticket-1"
	if msg := m.sandboxSubmitError(); msg != "" {
		t.Fatalf("a covered folder refused: %q", msg)
	}
	m.cwdBrowseDir = "/w/ticket-2"
	if msg := m.sandboxSubmitError(); !strings.Contains(msg, "does not mount this folder") {
		t.Errorf("submit error = %q", msg)
	}
	// The cycle restarts from the first choice rather than losing its place.
	if next, ok := m.nextSandboxChoice(1); !ok || next.backend != "docker" {
		t.Errorf("next = %+v, %v", next, ok)
	}
}

// The dialog opens on the profile, never on a sandbox picked for an earlier
// pane.
func TestResetSandboxField_ForgetsTheAttachChoice(t *testing.T) {
	m := attachModel()
	m.sandboxSbxName = "ticket-1"
	m.resetSandboxField("")
	if m.sandboxSbxName != "" {
		t.Errorf("sandboxSbxName = %q after reset", m.sandboxSbxName)
	}
}

func TestFolderHolds(t *testing.T) {
	for _, tc := range []struct {
		folder, dir string
		want        bool
	}{
		{"/w/t", "/w/t", true},
		{"/w/t/", "/w/t/app", true},
		{"/w/t", "/w/t-2", false},
		{`C:\w\t`, `C:\w\t\app`, true},
		{"/", "/w", false},
		{"", "/w", false},
	} {
		if got := folderHolds(tc.folder, tc.dir); got != tc.want {
			t.Errorf("folderHolds(%q, %q) = %v, want %v", tc.folder, tc.dir, got, tc.want)
		}
	}
}
