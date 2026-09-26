package tui

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

func TestLimitedDaemon_StatusBarAndOneFlashPerAttach(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := Model{
		cfg: config.Default(), width: 120, height: 40,
		notifications: NewNotificationCenter(30, 50),
		projects:      []*ProjectModel{{ID: "proj-1", Dest: ""}},
	}

	m.applyWorkspaceState(WorkspaceStateMsg{ActiveTab: "tab-0", DaemonLimited: true}, "")
	if !strings.Contains(m.renderStatusBar(), "[limited]") {
		t.Errorf("status bar lacks [limited]: %q", m.renderStatusBar())
	}
	if m.flashText != limitedDaemonFlash {
		t.Errorf("flash = %q, want the limited explanation", m.flashText)
	}
	if bar := m.renderStatusBar(); !strings.Contains(bar, "[limited]") || !strings.Contains(bar, limitedDaemonFlash) {
		t.Errorf("status bar missing marker or flash text: %q", bar)
	}

	m.flashText = ""
	m.applyWorkspaceState(WorkspaceStateMsg{ActiveTab: "tab-0", DaemonLimited: true}, "")
	if m.flashText != "" {
		t.Errorf("flashed again on a second broadcast: %q", m.flashText)
	}

	m.armReattachReset("")
	m.applyWorkspaceState(WorkspaceStateMsg{ActiveTab: "tab-0", DaemonLimited: true}, "")
	if m.flashText != limitedDaemonFlash {
		t.Errorf("no flash after reattach")
	}

	m.applyWorkspaceState(WorkspaceStateMsg{ActiveTab: "tab-0"}, "")
	if strings.Contains(m.renderStatusBar(), "[limited]") {
		t.Errorf("[limited] stayed after the daemon stopped being limited")
	}
}

func TestParseWorkspaceState_ReadsDaemonLimited(t *testing.T) {
	state := parseWorkspaceState(map[string]any{"daemon_limited": true})
	if !state.DaemonLimited {
		t.Error("DaemonLimited = false, want true when the key is present and true")
	}

	state = parseWorkspaceState(map[string]any{})
	if state.DaemonLimited {
		t.Error("DaemonLimited = true, want false when the key is absent")
	}
}
