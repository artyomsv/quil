package tui

import (
	"runtime"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/notify"
)

func desktopRow(t *testing.T) settingsField {
	t.Helper()
	for _, f := range settingsFields() {
		if f.label == "Desktop notifications" {
			return f
		}
	}
	t.Fatal("no 'Desktop notifications' row in settingsFields()")
	return settingsField{}
}

func TestSettingsDesktopRow_TogglesAndPersists(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := Model{}
	m.cfg.Notification.Desktop.Enabled = true
	m.configChanged = false

	row := desktopRow(t)
	if !row.isBool {
		t.Error("the row must be a bool toggle, not a text field")
	}

	row.set(&m, "")
	if m.cfg.Notification.Desktop.Enabled {
		t.Error("set() did not flip Enabled")
	}
	// Without this the edit is silently discarded when the TUI exits — the
	// defect that once dropped every Settings change except the disclaimer.
	if !m.configChanged {
		t.Error("set() must mark configChanged so the value reaches config.toml")
	}

	row.set(&m, "")
	if !m.cfg.Notification.Desktop.Enabled {
		t.Error("set() is not a toggle — a second call did not flip it back")
	}
}

// With enabled defaulting true, "on but not registered" is the DEFAULT state on
// a fresh Windows install, not an edge case. Rendering a bare "on" there would
// tell the user something is working when nothing is.
func TestSettingsDesktopRow_ReportsStateNotFlag(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := Model{}
	row := desktopRow(t)

	m.cfg.Notification.Desktop.Enabled = false
	if got := row.get(&m); got != "off" {
		t.Errorf("disabled row = %q, want \"off\"", got)
	}

	m.cfg.Notification.Desktop.Enabled = true
	got := row.get(&m)
	switch {
	case got == "on":
	case strings.Contains(got, "notify setup"):
	case got == "unsupported":
	default:
		t.Errorf("enabled row = %q, want on / on (run notify setup) / unsupported", got)
	}
}

// CI runs Linux, where there is nothing to register and the row must say so
// rather than claiming toasts are on.
func TestSettingsDesktopRow_UnsupportedOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this asserts the non-Windows rendering")
	}
	t.Setenv("QUIL_HOME", t.TempDir())
	m := Model{}
	m.cfg.Notification.Desktop.Enabled = true
	m.SetDesktopNotifier(nil, notify.Variant(false), 1)
	if got := desktopRow(t).get(&m); got != "unsupported" {
		t.Errorf("row on a non-Windows host = %q, want \"unsupported\"", got)
	}
}

// Disabled outranks unsupported: a user who turned it off does not need to be
// told about a platform capability they are not using.
func TestSettingsDesktopRow_OffOutranksUnsupported(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := Model{}
	m.cfg.Notification.Desktop.Enabled = false
	m.SetDesktopNotifier(nil, notify.Variant(false), 1)
	if got := desktopRow(t).get(&m); got != "off" {
		t.Errorf("disabled row = %q, want \"off\" regardless of platform", got)
	}
}

// Every state must render something distinguishable, or the row can report a
// state the user cannot act on.
func TestDesktopNotifyState_EveryStateHasALabel(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range []desktopNotifyState{desktopOff, desktopOn, desktopNeedsSetup, desktopUnsupported} {
		l := s.label()
		if l == "" {
			t.Errorf("state %d has an empty label", s)
		}
		if seen[l] {
			t.Errorf("label %q is used by more than one state", l)
		}
		seen[l] = true
	}
}
