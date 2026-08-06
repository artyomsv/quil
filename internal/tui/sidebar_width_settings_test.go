package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
)

// settingsFieldByLabel finds one Settings row, failing the test if the row is
// missing rather than nil-panicking three lines later.
func settingsFieldByLabel(t *testing.T, label string) settingsField {
	t.Helper()
	for _, f := range settingsFields() {
		if f.label == label {
			return f
		}
	}
	t.Fatalf("no %q row in settingsFields()", label)
	return settingsField{}
}

// The Settings row must apply the width LIVE, not only on the next launch.
// settingsFields' own comment says changes take effect on the next launch,
// which is right for the log level (its file handle lives in main.go and is
// not re-plumbed into the Model) and wrong here: the sidebar width is read
// from m.sidebarWidth on every render, so a visible layout control that did
// nothing until relaunch would read as a broken dialog.
func TestSettingsField_SidebarWidth_AppliesLiveAndPersists(t *testing.T) {
	field := settingsFieldByLabel(t, "Sidebar width")

	cfg := config.Default()
	cfg.UI.SidebarWidth = 22
	m := &Model{cfg: cfg, width: 200, height: 50, sidebarOpen: true, sidebarWidth: 22}

	if got := field.get(m); got != "22" {
		t.Errorf("get() = %q, want \"22\"", got)
	}

	field.set(m, "34")
	if m.sidebarWidth != 34 {
		t.Errorf("m.sidebarWidth = %d, want 34 (live application)", m.sidebarWidth)
	}
	if m.cfg.UI.SidebarWidth != 34 {
		t.Errorf("cfg.UI.SidebarWidth = %d, want 34 (persisted)", m.cfg.UI.SidebarWidth)
	}
	if !m.configChanged {
		t.Error("configChanged not set — the edit would be dropped on exit")
	}
}

// Driven through handleSettingsKey rather than by calling set directly: a
// setter test and its mutation can both pass against a call site that never
// reaches the relayout. The width change is worthless without the resize —
// every background tab would keep its pre-edit PTY size.
func TestHandleSettingsKey_SidebarWidthEditTriggersARelayout(t *testing.T) {
	idx := -1
	for i, f := range settingsFields() {
		if f.label == "Sidebar width" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("no \"Sidebar width\" row in settingsFields()")
	}

	cfg := config.Default()
	cfg.UI.SidebarWidth = 22
	m := Model{
		cfg: cfg, width: 200, height: 50,
		sidebarOpen: true, sidebarWidth: 22,
		dialog: dialogSettings, dialogEdit: true,
		dialogCursor: idx, dialogInput: "34",
	}

	out, cmd := m.handleSettingsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := out.(Model)

	if got.sidebarWidth != 34 {
		t.Errorf("m.sidebarWidth = %d after Enter, want 34", got.sidebarWidth)
	}
	if cmd == nil {
		t.Fatal("no command returned — the panes would keep their pre-edit size")
	}
	if got.dialogEdit {
		t.Error("still in edit mode after Enter")
	}
}

// A row with no relayout flag must not pay for one: the resize is a full
// ClearScreen plus a PTY resize for every pane in every tab.
func TestHandleSettingsKey_OrdinaryEditDoesNotRelayout(t *testing.T) {
	idx := -1
	for i, f := range settingsFields() {
		if f.label == "Ghost buffer lines" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("no \"Ghost buffer lines\" row in settingsFields()")
	}

	m := Model{
		cfg: config.Default(), width: 200, height: 50,
		dialog: dialogSettings, dialogEdit: true,
		dialogCursor: idx, dialogInput: "5000",
	}

	_, cmd := m.handleSettingsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Error("an ordinary settings edit returned a relayout command")
	}
}

// A value the clamp would reject outright is refused at the setter rather than
// written and silently corrected at render, so the dialog never displays a
// number the layout is not using.
func TestSettingsField_SidebarWidth_RejectsUnusableValues(t *testing.T) {
	field := settingsFieldByLabel(t, "Sidebar width")

	for _, val := range []string{"0", "-5", "abc", ""} {
		cfg := config.Default()
		cfg.UI.SidebarWidth = 22
		m := &Model{cfg: cfg, width: 200, height: 50, sidebarOpen: true, sidebarWidth: 22}

		field.set(m, val)
		if m.sidebarWidth != 22 {
			t.Errorf("set(%q) changed the width to %d, want it left at 22", val, m.sidebarWidth)
		}
		if m.cfg.UI.SidebarWidth != 22 {
			t.Errorf("set(%q) wrote %d to config, want it left at 22", val, m.cfg.UI.SidebarWidth)
		}
		if m.configChanged {
			t.Errorf("set(%q) flagged configChanged for a rejected value", val)
		}
	}
}
