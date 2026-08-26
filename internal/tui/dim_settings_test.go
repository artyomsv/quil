package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
)

// settingsIdx locates a Settings row by label. Tests drive the rows through
// handleSettingsKey rather than calling the setter directly, because a setter
// that works and a cursor index that names a different row both pass a
// direct-call test.
func settingsIdx(t *testing.T, label string) int {
	t.Helper()
	for i, f := range settingsFields() {
		if f.label == label {
			return i
		}
	}
	t.Fatalf("settingsFields has no %q row", label)
	return -1
}

// commitSettingsEdit drives the real two-key edit path for a string row:
// Enter opens the editor pre-filled from get(), the typed value replaces it,
// Enter commits through set().
func commitSettingsEdit(t *testing.T, m Model, label, typed string) Model {
	t.Helper()
	m.dialog = dialogSettings
	m.dialogCursor = settingsIdx(t, label)
	out, _ := m.handleSettingsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	edit, ok := out.(Model)
	if !ok {
		t.Fatalf("returned model type = %T", out)
	}
	if !edit.dialogEdit {
		t.Fatalf("Enter on %q did not open the editor", label)
	}
	edit.dialogInput = typed
	out, _ = edit.handleSettingsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	done, ok := out.(Model)
	if !ok {
		t.Fatalf("returned model type = %T", out)
	}
	return done
}

func toggleSetting(t *testing.T, m Model, label string) Model {
	t.Helper()
	m.dialog = dialogSettings
	m.dialogCursor = settingsIdx(t, label)
	out, _ := m.handleSettingsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got, ok := out.(Model)
	if !ok {
		t.Fatalf("returned model type = %T", out)
	}
	return got
}

// TestSettingsKey_UnfocusedDimToggleIsStateBased is the reason the row exists
// at all. The toggle must act on what the user SEES ("off"), not on the raw
// flag: with a legacy `unfocused_dim = 0` the flag is already true, so a
// flag-flipping toggle would turn an off-looking row further off and read as
// a dead control.
func TestSettingsKey_UnfocusedDimToggleIsStateBased(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		enabled     bool
		level       float64
		wantEnabled bool
		wantLevel   float64
	}{
		{
			"on switches off and keeps the level",
			true, config.DefaultUnfocusedDim, false, config.DefaultUnfocusedDim,
		},
		{
			// The entire justification for a second config key. If this ever
			// lands on DefaultUnfocusedDim the two-key design has bought
			// nothing and should be collapsed back to one.
			"off switches on and restores the CUSTOM level, not the default",
			false, 0.35, true, 0.35,
		},
		{
			// Legacy off: the flag says on, the level says off. Switching on
			// has to supply a level or the toggle is a no-op that never dims.
			"a legacy zero level switches on with the default level",
			true, 0, true, config.DefaultUnfocusedDim,
		},
		{
			"off with a zero level also gains a usable level",
			false, 0, true, config.DefaultUnfocusedDim,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := config.Default()
			cfg.UI.UnfocusedDimEnabled = tt.enabled
			cfg.UI.UnfocusedDim = tt.level
			got := toggleSetting(t, Model{cfg: cfg}, "Unfocused dim")

			if got.cfg.UI.UnfocusedDimEnabled != tt.wantEnabled {
				t.Errorf("UnfocusedDimEnabled = %v, want %v", got.cfg.UI.UnfocusedDimEnabled, tt.wantEnabled)
			}
			if got.cfg.UI.UnfocusedDim != tt.wantLevel {
				t.Errorf("UnfocusedDim = %v, want %v", got.cfg.UI.UnfocusedDim, tt.wantLevel)
			}
			if !got.configChanged {
				t.Error("configChanged not set — the edit would be lost on exit")
			}
		})
	}
}

// TestSettingsKey_UnfocusedDimToggleRoundTripPreservesLevel walks the full
// off-and-back-on the user performs, which is the sequence the single-key
// design could not survive.
func TestSettingsKey_UnfocusedDimToggleRoundTripPreservesLevel(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.UI.UnfocusedDim = 0.35

	off := toggleSetting(t, Model{cfg: cfg}, "Unfocused dim")
	if off.cfg.UI.UnfocusedDimAmount() != 0 {
		t.Fatalf("after switching off, Amount = %v, want 0", off.cfg.UI.UnfocusedDimAmount())
	}
	on := toggleSetting(t, off, "Unfocused dim")
	if got := on.cfg.UI.UnfocusedDimAmount(); got != 0.35 {
		t.Errorf("after off/on, Amount = %v, want the preserved 0.35", got)
	}
}

// TestSettingsFields_UnfocusedDimRowReportsState pins the same rule the
// Desktop-notifications row states: report what the renderer is doing, not
// what the flag holds. A row reading "on" while nothing dims is a lie the
// user can only resolve by editing config.toml.
func TestSettingsFields_UnfocusedDimRowReportsState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		enabled bool
		level   float64
		want    string
	}{
		{"switched on with a level", true, 0.6, "on"},
		{"switched off", false, 0.6, "off"},
		{"switched on but a legacy zero level", true, 0, "off"},
		{"off with a zero level", false, 0, "off"},
	}
	row := settingsFields()[settingsIdx(t, "Unfocused dim")]
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := config.Default()
			cfg.UI.UnfocusedDimEnabled = tt.enabled
			cfg.UI.UnfocusedDim = tt.level
			m := &Model{cfg: cfg}
			if got := row.get(m); got != tt.want {
				t.Errorf("get() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSettingsFields_UnfocusedDimLevelShowsWhatTheRendererWouldUse holds the
// level row to Sidebar width's rule: never display a number the renderer is
// not using. A hand-edited 1.5 in config.toml is clamped to 0.9 at render, so
// 1.5 must not be what the dialog shows.
func TestSettingsFields_UnfocusedDimLevelShowsWhatTheRendererWouldUse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		enabled bool
		level   float64
		want    string
	}{
		{"an ordinary level", true, 0.35, "0.35"},
		{"the default", true, config.DefaultUnfocusedDim, "0.60"},
		{"an oversized level shows the clamp, not the file", true, 1.5, "0.90"},
		{
			// The level survives the switch, so the row keeps showing it —
			// that is what the user turns back on to.
			"a switched-off dim still shows its preserved level", false, 0.35, "0.35",
		},
	}
	row := settingsFields()[settingsIdx(t, "Unfocused dim level")]
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := config.Default()
			cfg.UI.UnfocusedDimEnabled = tt.enabled
			cfg.UI.UnfocusedDim = tt.level
			m := &Model{cfg: cfg}
			if got := row.get(m); got != tt.want {
				t.Errorf("get() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSettingsKey_UnfocusedDimLevelRefusesUnusableValues — refused rather than
// clamped-on-write, again per Sidebar width: a stored value the renderer would
// not honour must never be displayed back. 0 is refused specifically because
// the row above it is what "off" means; accepting it here would leave the two
// rows disagreeing about the same state.
func TestSettingsKey_UnfocusedDimLevelRefusesUnusableValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		typed string
	}{
		{"zero belongs to the toggle, not the level", "0"},
		{"a negative would blend the wrong way", "-0.2"},
		{"past the clamp", "1.5"},
		{"a percentage-shaped typo", "60"},
		{"not a number", "abc"},
		{"empty", ""},
		{"NaN survives an ordinary clamp, so it is refused by name", "NaN"},
		{"an infinity", "Inf"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := config.Default()
			cfg.UI.UnfocusedDim = 0.35
			got := commitSettingsEdit(t, Model{cfg: cfg}, "Unfocused dim level", tt.typed)

			if got.cfg.UI.UnfocusedDim != 0.35 {
				t.Errorf("UnfocusedDim = %v after typing %q, want the untouched 0.35", got.cfg.UI.UnfocusedDim, tt.typed)
			}
			if got.configChanged {
				t.Errorf("configChanged set by a refused value %q — it would be written to disk", tt.typed)
			}
		})
	}
}

func TestSettingsKey_UnfocusedDimLevelAcceptsUsableValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		typed string
		want  float64
	}{
		{"0.35", 0.35},
		{"0.9", config.MaxUnfocusedDim},
		{".25", 0.25},
		{"0.05", 0.05},
	}

	for _, tt := range tests {
		t.Run(tt.typed, func(t *testing.T) {
			t.Parallel()
			cfg := config.Default()
			got := commitSettingsEdit(t, Model{cfg: cfg}, "Unfocused dim level", tt.typed)

			if got.cfg.UI.UnfocusedDim != tt.want {
				t.Errorf("UnfocusedDim = %v after typing %q, want %v", got.cfg.UI.UnfocusedDim, tt.typed, tt.want)
			}
			if !got.configChanged {
				t.Error("configChanged not set — the edit would be lost on exit")
			}
		})
	}
}

// TestSettingsKey_UnfocusedDimLevelEditsWhileSwitchedOff pins that setting a
// level does NOT secretly switch the dim on. The level row and the toggle row
// own different keys; a level edit that flipped the switch would make the
// toggle impossible to keep off.
func TestSettingsKey_UnfocusedDimLevelEditsWhileSwitchedOff(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.UI.UnfocusedDimEnabled = false
	got := commitSettingsEdit(t, Model{cfg: cfg}, "Unfocused dim level", "0.4")

	if got.cfg.UI.UnfocusedDimEnabled {
		t.Error("editing the level switched the dim on — the toggle owns that")
	}
	if got.cfg.UI.UnfocusedDim != 0.4 {
		t.Errorf("UnfocusedDim = %v, want the typed 0.4", got.cfg.UI.UnfocusedDim)
	}
}
