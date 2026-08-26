package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
)

// paletteRows collects every row carrying an action, in registry order.
func paletteRows(m *Model, action paletteAction) []paletteCommand {
	var out []paletteCommand
	for _, c := range m.buildPaletteCommands() {
		if c.action == action {
			out = append(out, c)
		}
	}
	return out
}

func paletteRowByArg(t *testing.T, m *Model, action paletteAction, arg string) paletteCommand {
	t.Helper()
	for _, c := range paletteRows(m, action) {
		if c.arg == arg {
			return c
		}
	}
	t.Fatalf("palette has no row for action %v arg %q", action, arg)
	return paletteCommand{}
}

func TestPalette_DimLevelPresetsAreOfferedOnce(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)

	rows := paletteRows(m, palActDimLevel)
	if len(rows) != len(dimLevelPresets) {
		t.Fatalf("palette offers %d dim-level rows, want %d", len(rows), len(dimLevelPresets))
	}
	for i, p := range dimLevelPresets {
		if rows[i].arg != formatDimLevel(p.level) {
			t.Errorf("row[%d].arg = %q, want %q", i, rows[i].arg, formatDimLevel(p.level))
		}
		if !rows[i].enabled {
			t.Errorf("row[%d] (%s) is disabled — a preset is always settable", i, p.name)
		}
		// The number belongs in the label: "strong" alone does not tell the
		// user what they are about to store, and the level row in Settings
		// shows the same two-decimal form.
		if !strings.Contains(rows[i].label, formatDimLevel(p.level)) {
			t.Errorf("row[%d].label = %q, want it to name the level %s", i, rows[i].label, formatDimLevel(p.level))
		}
	}

	toggles := paletteRows(m, palActDimToggle)
	if len(toggles) != 1 {
		t.Fatalf("palette offers %d dim toggle rows, want exactly 1", len(toggles))
	}

	// The rows need their section header: in browse mode the palette is one
	// long list, and four ungrouped rows appended after "System" read as more
	// system commands.
	header := false
	for _, c := range m.buildPaletteCommands() {
		if c.header && c.label == "Appearance" {
			header = true
		}
	}
	if !header {
		t.Error("no Appearance section header — the dim rows would render ungrouped")
	}
}

// TestPalette_DimToggleLabelNamesTheAction follows sidebarToggleLabel's rule:
// a row labelled with the current state leaves the user working out which way
// Enter moves it.
func TestPalette_DimToggleLabelNamesTheAction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		enabled bool
		level   float64
		want    string
	}{
		{"currently dimming", true, 0.6, "Turn unfocused dim off"},
		{"switched off", false, 0.6, "Turn unfocused dim on"},
		// State, not flag — the same legacy config the Settings row handles.
		{"a legacy zero level", true, 0, "Turn unfocused dim on"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newSplitDragTestModel(t)
			m.cfg.UI.UnfocusedDimEnabled = tt.enabled
			m.cfg.UI.UnfocusedDim = tt.level
			rows := paletteRows(m, palActDimToggle)
			if len(rows) != 1 {
				t.Fatalf("got %d toggle rows, want 1", len(rows))
			}
			if rows[0].label != tt.want {
				t.Errorf("label = %q, want %q", rows[0].label, tt.want)
			}
		})
	}
}

// TestPalette_DimLevelMarksTheCurrentPreset — the marker is what stops the
// list reading as four equally-plausible options with no indication of where
// the user already is.
func TestPalette_DimLevelMarksTheCurrentPreset(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.cfg.UI.UnfocusedDimEnabled = true
	m.cfg.UI.UnfocusedDim = dimLevelPresets[1].level

	// Matched against the marker constant, not against "non-empty": the same
	// column also carries the turns-dim-on hint, so a non-empty test would
	// count that as "current" and pass while the row said something else.
	marked := 0
	for _, c := range paletteRows(m, palActDimLevel) {
		if c.detail != dimPresetCurrentDetail {
			continue
		}
		marked++
		if c.arg != formatDimLevel(dimLevelPresets[1].level) {
			t.Errorf("marked row is %q, want the one for %v", c.arg, dimLevelPresets[1].level)
		}
	}
	if marked != 1 {
		t.Errorf("%d rows marked current, want exactly 1", marked)
	}

	// A custom level matches no preset, and claiming one would misreport what
	// is stored.
	m.cfg.UI.UnfocusedDim = 0.37
	for _, c := range paletteRows(m, palActDimLevel) {
		if c.detail == dimPresetCurrentDetail {
			t.Errorf("row %q marked current while the level is a custom 0.37", c.arg)
		}
	}

	// Switched off, no preset is in effect however the level reads — and the
	// column says what Enter will also do instead.
	m.cfg.UI.UnfocusedDim = dimLevelPresets[1].level
	m.cfg.UI.UnfocusedDimEnabled = false
	for _, c := range paletteRows(m, palActDimLevel) {
		if c.detail == dimPresetCurrentDetail {
			t.Errorf("row %q marked current while the dim is switched off", c.arg)
		}
		if c.detail != dimPresetTurnsOnDetail {
			t.Errorf("row %q detail = %q while the dim is off, want %q — nothing would tell the user Enter also switches it on",
				c.arg, c.detail, dimPresetTurnsOnDetail)
		}
	}
}

func TestPalette_ExecuteDimToggle(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.cfg.UI.UnfocusedDimEnabled = true
	m.cfg.UI.UnfocusedDim = 0.35

	row := paletteRows(m, palActDimToggle)[0]
	out, _ := m.executePaletteCommand(row)
	off := out.(Model)
	if off.cfg.UI.UnfocusedDimAmount() != 0 {
		t.Errorf("Amount = %v after the toggle, want 0", off.cfg.UI.UnfocusedDimAmount())
	}
	if off.cfg.UI.UnfocusedDim != 0.35 {
		t.Errorf("level = %v, want the preserved 0.35", off.cfg.UI.UnfocusedDim)
	}
	if !off.configChanged {
		t.Error("configChanged not set — the palette edit would be lost on exit")
	}
}

// TestPalette_ExecuteDimLevelSwitchesTheDimOn is the row's whole correctness
// condition. Choosing "strong" while the dim is off must not store a level
// that never renders — a command that silently does nothing observable is the
// confidently-wrong answer this codebase keeps refusing.
func TestPalette_ExecuteDimLevelSwitchesTheDimOn(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.cfg.UI.UnfocusedDimEnabled = false
	m.cfg.UI.UnfocusedDim = 0.6

	want := dimLevelPresets[2].level
	row := paletteRowByArg(t, m, palActDimLevel, formatDimLevel(want))
	out, _ := m.executePaletteCommand(row)
	got := out.(Model)

	if !got.cfg.UI.UnfocusedDimEnabled {
		t.Error("picking a level left the dim switched off — the command would do nothing visible")
	}
	if got.cfg.UI.UnfocusedDim != want {
		t.Errorf("level = %v, want %v", got.cfg.UI.UnfocusedDim, want)
	}
	if got.cfg.UI.UnfocusedDimAmount() != want {
		t.Errorf("Amount = %v, want %v", got.cfg.UI.UnfocusedDimAmount(), want)
	}
	if !got.configChanged {
		t.Error("configChanged not set — the palette edit would be lost on exit")
	}
}

// TestView_PaletteDimLevelReachesTheFrame closes the loop at the call site:
// the config values above are only meaningful if View actually blends with
// them. A correct executePaletteCommand writing a field View never reads
// passes every test above.
func TestView_PaletteDimLevelReachesTheFrame(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newSplitDragTestModel(t)
	m.cfg.UI.UnfocusedDimEnabled = false
	m.termFocused = true

	blurred, _ := m.Update(tea.BlurMsg{})
	undimmed := blurred.(Model).View().Content

	row := paletteRowByArg(t, m, palActDimLevel, formatDimLevel(dimLevelPresets[2].level))
	out, _ := blurred.(Model).executePaletteCommand(row)
	dimmed := out.(Model).View().Content

	if dimmed == undimmed {
		t.Fatal("choosing a dim level from the palette did not change the unfocused frame")
	}
	if got, want := stripANSI(dimmed), stripANSI(undimmed); got != want {
		t.Error("the palette-set dim altered frame text, not just colour")
	}
}

// TestView_DimSwitchGatesTheBlend covers the new key at the seam it guards:
// a level the renderer would honour must not render while the switch is off.
func TestView_DimSwitchGatesTheBlend(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newSplitDragTestModel(t)
	m.cfg.UI.UnfocusedDimEnabled = false
	m.cfg.UI.UnfocusedDim = 0.5
	m.termFocused = true

	focused := m.View().Content
	blurred, _ := m.Update(tea.BlurMsg{})
	if got := blurred.(Model).View().Content; got != focused {
		t.Error("a switched-off dim must leave the unfocused frame untouched")
	}
}

func TestDimLevelPresets_AreWithinTheSettableRange(t *testing.T) {
	t.Parallel()
	// The presets are the same values the Settings level row would have to
	// accept. A preset outside parseDimLevel's range would be settable from
	// the palette and refused in the dialog, which is the two front doors
	// disagreeing about what is legal.
	seen := map[string]bool{}
	for _, p := range dimLevelPresets {
		// IDENTITY, not merely ok. buildPaletteCommands marks the current
		// preset by exact float equality against this literal, while dispatch
		// matches the FORMATTED arg — so a preset that does not survive
		// format/parse unchanged (0.125 renders "0.12") silently loses its
		// marker the first time the level is re-accepted through Settings.
		if got, ok := parseDimLevel(formatDimLevel(p.level)); !ok || got != p.level {
			t.Errorf("preset %s (%v) does not survive format/parse: got %v ok=%v", p.name, p.level, got, ok)
		}
		if p.level > config.MaxUnfocusedDim || p.level < minDimLevel {
			t.Errorf("preset %s = %v, outside [%v, %v]", p.name, p.level, minDimLevel, config.MaxUnfocusedDim)
		}
		// Keyed on the FORMATTED value: dispatch resolves a row by string
		// compare, so two presets that merely render alike leave the second
		// one permanently undispatchable — its Enter falls through to a
		// silent no-op.
		if key := formatDimLevel(p.level); seen[key] {
			t.Errorf("preset %s renders as %q, which another preset already uses", p.name, key)
		} else {
			seen[key] = true
		}
	}
	// The shipped default must be reachable without typing a number, or a
	// user who moved off it from the palette cannot get back from the palette.
	if !seen[formatDimLevel(config.DefaultUnfocusedDim)] {
		t.Errorf("no preset offers the default level %v", config.DefaultUnfocusedDim)
	}
}
