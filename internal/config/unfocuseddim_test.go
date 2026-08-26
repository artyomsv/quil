package config

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestUnfocusedDimLevel_ClampsOutOfRangeValues(t *testing.T) {
	tests := []struct {
		name string
		set  float64
		want float64
	}{
		{"zero disables", 0, 0},
		{"negative is off, never a brightening blend", -0.5, 0},
		{"an ordinary value passes through", 0.45, 0.45},
		{"the maximum passes through", MaxUnfocusedDim, MaxUnfocusedDim},
		{"a full blend is clamped short of invisible", 1.0, MaxUnfocusedDim},
		{"a percentage-shaped typo is clamped, not honoured", 45, MaxUnfocusedDim},
		{
			// TOML accepts the literal `nan`, and NaN fails BOTH comparisons in
			// an ordinary clamp — so it would pass straight through and reach
			// the blend, where uint8(NaN) is undefined. The frame currently
			// survives that only because the caller happens to gate on
			// `amount > 0`, which NaN also fails. Relying on a downstream guard
			// for a value this one is supposed to have made safe is the kind of
			// accident that a later refactor of the caller silently removes.
			"a NaN is off rather than passed through", math.NaN(), 0,
		},
		{"an infinity is clamped like any oversized value", math.Inf(1), MaxUnfocusedDim},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// UnfocusedDimEnabled is deliberately left at its zero value: the
			// LEVEL is what the switch is off ABOUT, so it must clamp the same
			// way whether the dim is currently switched on or not. That is also
			// what lets the Settings dialog show the preserved level while the
			// dim reads "off".
			u := UIConfig{UnfocusedDim: tt.set}
			if got := u.UnfocusedDimLevel(); got != tt.want {
				t.Errorf("UIConfig{UnfocusedDim: %v}.UnfocusedDimLevel() = %v, want %v", tt.set, got, tt.want)
			}
		})
	}
}

func TestUnfocusedDimAmount_HonoursTheSwitchAndTheLevel(t *testing.T) {
	// Amount is what View() blends with, so it must be zero if EITHER the
	// switch is off or the level says off. The two are separate keys precisely
	// so that switching off can preserve a customised level — which means the
	// off-with-a-level row is the one that matters most here.
	tests := []struct {
		name    string
		enabled bool
		level   float64
		want    float64
	}{
		{"on with a level dims by that level", true, 0.35, 0.35},
		{"off with a level does not dim", false, 0.35, 0},
		{"on with a zero level does not dim", true, 0, 0},
		{"off with a zero level does not dim", false, 0, 0},
		{"the switch does not defeat the clamp", true, 1.0, MaxUnfocusedDim},
		{"off beats even an oversized level", false, 1.0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := UIConfig{UnfocusedDimEnabled: tt.enabled, UnfocusedDim: tt.level}
			if got := u.UnfocusedDimAmount(); got != tt.want {
				t.Errorf("UIConfig{Enabled: %v, Dim: %v}.UnfocusedDimAmount() = %v, want %v",
					tt.enabled, tt.level, got, tt.want)
			}
		})
	}
}

func TestUnfocusedDimLevel_SurvivesBeingSwitchedOff(t *testing.T) {
	// The whole reason the switch is a separate key: toggling the dim off and
	// back on must land on the level the user chose, not on the default. If
	// Level ever started consulting the switch, an off/on round trip through
	// the Settings dialog would silently reset a customised level to 0.6.
	u := UIConfig{UnfocusedDimEnabled: false, UnfocusedDim: 0.35}
	if got := u.UnfocusedDimLevel(); got != 0.35 {
		t.Errorf("UnfocusedDimLevel() while switched off = %v, want the preserved 0.35", got)
	}
	if got := u.UnfocusedDimAmount(); got != 0 {
		t.Errorf("UnfocusedDimAmount() while switched off = %v, want 0", got)
	}
}

func TestDefault_EnablesUnfocusedDim(t *testing.T) {
	// The feature exists to be noticed without being configured. A default of
	// 0 would ship it switched off, which for a visual affordance means
	// shipping it to nobody.
	d := Default().UI
	if !d.UnfocusedDimEnabled {
		t.Error("Default().UI.UnfocusedDimEnabled = false, want true")
	}
	got := d.UnfocusedDimAmount()
	if got != DefaultUnfocusedDim {
		t.Errorf("Default().UI.UnfocusedDimAmount() = %v, want %v", got, DefaultUnfocusedDim)
	}
	if got <= 0 {
		t.Error("the shipped default must actually dim")
	}
}

func TestLoad_AbsentEnabledKeyKeepsTheDimOn(t *testing.T) {
	// Every config.toml written before this key existed lacks it, and a bool
	// decodes to false when absent from the file. Load starts from Default()
	// and lets the decoder overwrite only the keys the file names, which is
	// what keeps the dim on for those installs — this pins that, because the
	// alternative is the feature silently switching itself off on upgrade for
	// everyone who has ever saved a config.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\nunfocused_dim = 0.45\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.UI.UnfocusedDimEnabled {
		t.Error("UnfocusedDimEnabled = false after loading a config that predates the key, want true")
	}
	if got := c.UI.UnfocusedDimAmount(); got != 0.45 {
		t.Errorf("UnfocusedDimAmount() = %v, want the file's 0.45", got)
	}
}

func TestLoad_LegacyZeroStillDisablesTheDim(t *testing.T) {
	// `unfocused_dim = 0` was the ONLY way to switch the dim off before this
	// key existed, so an install that used it must stay off after the upgrade
	// even though the new switch defaults to on.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\nunfocused_dim = 0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.UI.UnfocusedDimAmount(); got != 0 {
		t.Errorf("UnfocusedDimAmount() = %v after a legacy `unfocused_dim = 0`, want 0", got)
	}
}

func TestUnfocusedDim_TOMLRoundTrip(t *testing.T) {
	// Save serialises the whole struct, so a switched-off dim with a preserved
	// level has to survive the write the TUI performs on exit.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	in := Default()
	in.UI.UnfocusedDimEnabled = false
	in.UI.UnfocusedDim = 0.35
	if err := Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.UI.UnfocusedDimEnabled {
		t.Error("UnfocusedDimEnabled came back true, want the saved false")
	}
	if out.UI.UnfocusedDim != 0.35 {
		t.Errorf("UnfocusedDim = %v, want the saved 0.35", out.UI.UnfocusedDim)
	}
}
