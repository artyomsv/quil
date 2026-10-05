package config

import (
	"os"
	"testing"
)

func TestActiveBindings_MissingFileMeansLegacy(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	if _, legacy, err := ActiveBindings(); !legacy || err != nil {
		t.Fatalf("legacy = %v, err = %v; want the legacy table and no error", legacy, err)
	}
}

func TestActiveBindings_ReadableFileWins(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	if err := os.WriteFile(BindingsPath(), []byte("preset = \"tmux\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, legacy, err := ActiveBindings()
	if legacy || err != nil || b.Preset != "tmux" {
		t.Fatalf("b = %+v, legacy = %v, err = %v", b, legacy, err)
	}
}

func TestActiveBindings_UnreadableFileMeansLegacyWithError(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	if err := os.WriteFile(BindingsPath(), []byte("[bindings\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, legacy, err := ActiveBindings(); !legacy || err == nil {
		t.Fatalf("legacy = %v, err = %v; want the legacy table and the parse error", legacy, err)
	}
}

func TestLegacyKeymap_AppliesTheConfigTable(t *testing.T) {
	kb := Default().Keybindings
	kb.Quit = "ctrl+alt+q"
	km, _ := LegacyKeymap(kb)
	if got := km.Keys("app.quit"); len(got) != 1 || got[0] != "ctrl+alt+q" {
		t.Fatalf("app.quit = %v", got)
	}
	// The registry layer underneath still binds what the table has no field for.
	if got := km.Keys("tab.switch_1"); len(got) != 1 || got[0] != "alt+1" {
		t.Fatalf("tab.switch_1 = %v", got)
	}
}
