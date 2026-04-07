package persist_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/persist"
)

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workspace.json")

	state := map[string]any{
		"active_tab": "tab-001",
		"tabs":       []any{map[string]any{"id": "tab-001", "name": "Shell"}},
		"panes":      []any{map[string]any{"id": "pane-001", "tab_id": "tab-001", "cwd": "/tmp"}},
	}

	if err := persist.Save(path, state); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := persist.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded["active_tab"] != "tab-001" {
		t.Errorf("active_tab = %v, want tab-001", loaded["active_tab"])
	}

	tabs := loaded["tabs"].([]any)
	if len(tabs) != 1 {
		t.Errorf("len(tabs) = %d, want 1", len(tabs))
	}
}

func TestSaveCreatesBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workspace.json")

	state1 := map[string]any{"active_tab": "tab-001"}
	state2 := map[string]any{"active_tab": "tab-002"}

	persist.Save(path, state1)
	persist.Save(path, state2)

	// Primary should have state2
	loaded, _ := persist.Load(path)
	if loaded["active_tab"] != "tab-002" {
		t.Errorf("primary: active_tab = %v, want tab-002", loaded["active_tab"])
	}

	// Backup should have state1
	bakPath := path + ".bak"
	bakData, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if len(bakData) == 0 {
		t.Error("backup file is empty")
	}
}

func TestLoadFallsBackToBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workspace.json")

	state := map[string]any{"active_tab": "tab-bak"}

	// Save twice so the second save rotates the first to .bak
	persist.Save(path, state)
	persist.Save(path, state)

	// Corrupt the primary — Load should fall back to .bak
	os.WriteFile(path, []byte("corrupt"), 0600)

	loaded, err := persist.Load(path)
	if err != nil {
		t.Fatalf("Load with corrupt primary: %v", err)
	}
	if loaded["active_tab"] != "tab-bak" {
		t.Errorf("active_tab = %v, want tab-bak", loaded["active_tab"])
	}
}

func TestLoadReturnsNilForFreshWorkspace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.json")

	loaded, err := persist.Load(path)
	if err != nil {
		t.Fatalf("Load nonexistent: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil for fresh workspace, got %v", loaded)
	}
}
