package config

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/keymap"
)

// Every test here sets QUIL_HOME. dev.sh test runs with a throwaway /root, so a
// test that writes to the real home is green in Docker and pollutes ~/.quil
// everywhere else.
func bindingsHome(t *testing.T) {
	t.Helper()
	t.Setenv("QUIL_HOME", t.TempDir())
}

func TestLoadBindings_RoundTrip(t *testing.T) {
	bindingsHome(t)

	want := Bindings{
		Preset:          "tmux",
		Prefix:          "ctrl+a",
		SequenceTimeout: 500 * time.Millisecond,
		Overrides:       map[keymap.ActionID]string{"pane.rename": "alt+shift+r"},
	}
	if err := WriteBindings(want); err != nil {
		t.Fatalf("WriteBindings: %v", err)
	}
	got, err := LoadBindings()
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	if got.Preset != want.Preset || got.Prefix != want.Prefix {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got.SequenceTimeout != want.SequenceTimeout {
		t.Errorf("SequenceTimeout = %v, want %v", got.SequenceTimeout, want.SequenceTimeout)
	}
	if got.Overrides["pane.rename"] != "alt+shift+r" {
		t.Errorf("Overrides = %v", got.Overrides)
	}
}

// A missing file is the normal first-launch state, not an error.
func TestLoadBindings_MissingFileIsNotAnError(t *testing.T) {
	bindingsHome(t)
	got, err := LoadBindings()
	if err != nil {
		t.Fatalf("a missing bindings.toml must not error: %v", err)
	}
	if got.Preset != keymap.DefaultPresetName {
		t.Errorf("Preset = %q, want %q", got.Preset, keymap.DefaultPresetName)
	}
}

// Zero means off, matching tmux, and must survive as zero rather than becoming
// a parse error or a default.
func TestLoadBindings_ZeroTimeoutIsOff(t *testing.T) {
	bindingsHome(t)
	if err := os.WriteFile(BindingsPath(), []byte("sequence_timeout = \"0\"\n"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := LoadBindings()
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	if got.SequenceTimeout != 0 {
		t.Errorf("SequenceTimeout = %v, want 0 (off)", got.SequenceTimeout)
	}
}

// Two TUI clients can attach concurrently; the create must not race.
func TestWriteBindingsExclusive_RefusesToClobber(t *testing.T) {
	bindingsHome(t)
	if err := WriteBindingsExclusive(Bindings{Preset: "default"}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	err := WriteBindingsExclusive(Bindings{Preset: "tmux"})
	if err == nil {
		t.Fatal("a second exclusive write must fail rather than overwrite")
	}
	if !os.IsExist(err) {
		t.Errorf("err = %v, want an os.IsExist error so callers can tell a race from a real failure", err)
	}
}

// The whole point of the diff. A config carrying all 42 fields at their shipped
// values must migrate to ZERO overrides — copying instead of diffing pins the
// user to today's defaults forever, which is the trap this move exists to
// escape.
func TestMigrateBindings_UntouchedConfigYieldsNoOverrides(t *testing.T) {
	bindingsHome(t)

	migrated, err := MigrateBindings(Default(), nil)
	if err != nil {
		t.Fatalf("MigrateBindings: %v", err)
	}
	if !migrated {
		t.Fatal("a first launch with no bindings.toml must migrate")
	}
	got, err := LoadBindings()
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	if len(got.Overrides) != 0 {
		t.Errorf("Overrides = %v, want none", got.Overrides)
	}
	if got.Preset != keymap.DefaultPresetName {
		t.Errorf("Preset = %q, want %q", got.Preset, keymap.DefaultPresetName)
	}
}

func TestMigrateBindings_OneCustomizationYieldsOneOverride(t *testing.T) {
	bindingsHome(t)

	cfg := Default()
	cfg.Keybindings.NewTab = "ctrl+shift+t"
	if _, err := MigrateBindings(cfg, nil); err != nil {
		t.Fatalf("MigrateBindings: %v", err)
	}
	got, _ := LoadBindings()
	if len(got.Overrides) != 1 || got.Overrides["tab.new"] != "ctrl+shift+t" {
		t.Errorf("Overrides = %v, want exactly {tab.new: ctrl+shift+t}", got.Overrides)
	}
}

// A malformed config.toml would otherwise migrate as pure defaults and
// permanently discard the user's customizations.
func TestMigrateBindings_LoadErrorWritesNoFile(t *testing.T) {
	bindingsHome(t)

	migrated, err := MigrateBindings(Default(), errors.New("parse error"))
	if err == nil {
		t.Error("a Load error must be reported, not swallowed")
	}
	if migrated {
		t.Error("a Load error must not migrate")
	}
	if _, statErr := os.Stat(BindingsPath()); !os.IsNotExist(statErr) {
		t.Error("no bindings.toml may be written when Load errored")
	}
}

// A MISSING config.toml is an ordinary first launch, not a malformed one.
// Treating it as an error leaves a fresh install with no bindings.toml on every
// subsequent launch, forever, while logging the same failure each time.
func TestMigrateBindings_MissingConfigStillMigrates(t *testing.T) {
	bindingsHome(t)

	missing := &os.PathError{Op: "open", Path: ConfigPath(), Err: os.ErrNotExist}
	migrated, err := MigrateBindings(Default(), missing)
	if err != nil {
		t.Fatalf("a missing config.toml must not block migration: %v", err)
	}
	if !migrated {
		t.Fatal("a fresh install must still get a bindings.toml")
	}
}

func TestMigrateBindings_SecondLaunchIsANoOp(t *testing.T) {
	bindingsHome(t)

	if _, err := MigrateBindings(Default(), nil); err != nil {
		t.Fatalf("first: %v", err)
	}
	migrated, err := MigrateBindings(Default(), nil)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if migrated {
		t.Error("a second launch must not migrate again")
	}
}

// Release N: Save must still emit [keybindings] so a rollback to the previous
// binary — which cannot read bindings.toml — still finds the user's keys.
// Inverting this assertion is the first step of the release N+1 change.
func TestSave_StillEmitsLegacyKeybindings(t *testing.T) {
	bindingsHome(t)

	path := ConfigPath()
	if err := Save(path, Default()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "[keybindings]") {
		t.Error("Save dropped [keybindings]; release N must keep it as the rollback source")
	}
}

