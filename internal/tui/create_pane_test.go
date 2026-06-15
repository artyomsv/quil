package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/plugin"
)

// toolsRegistry loads two "tools" plugins — an available one and an
// unavailable one — so the create-pane dialog can be exercised against a
// category that mixes both states.
func toolsRegistry(t *testing.T) *plugin.Registry {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"lazygit.toml": "[plugin]\nname = \"lazygit\"\ndisplay_name = \"Lazygit\"\ncategory = \"tools\"\n[command]\ncmd = \"lazygit\"\n",
		"k9s.toml":     "[plugin]\nname = \"k9s\"\ndisplay_name = \"k9s\"\ncategory = \"tools\"\nhomepage = \"https://github.com/derailed/k9s\"\n[command]\ncmd = \"k9s\"\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	r := plugin.NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	r.Get("lazygit").Available = true
	r.Get("k9s").Available = false
	return r
}

func TestCreatePaneCategories_IncludesUnavailableAvailableFirst(t *testing.T) {
	m := &Model{pluginRegistry: toolsRegistry(t)}
	cats := m.createPaneCategories()

	var tools []*plugin.PanePlugin
	for _, c := range cats {
		if c.key == "tools" {
			tools = c.plugins
		}
	}
	if len(tools) != 2 {
		t.Fatalf("tools category has %d plugins, want 2 (unavailable shown, not hidden)", len(tools))
	}
	if !tools[0].Available || tools[0].Name != "lazygit" {
		t.Errorf("first = %q (available=%v), want available lazygit first", tools[0].Name, tools[0].Available)
	}
	if tools[1].Available || tools[1].Name != "k9s" {
		t.Errorf("second = %q (available=%v), want unavailable k9s last", tools[1].Name, tools[1].Available)
	}
}

func TestHandleCreatePaneSelect_UnavailablePluginBlocked(t *testing.T) {
	m := &Model{pluginRegistry: toolsRegistry(t)}
	cats := m.createPaneCategories()
	toolsIdx := -1
	for i, c := range cats {
		if c.key == "tools" {
			toolsIdx = i
		}
	}
	if toolsIdx < 0 {
		t.Fatal("tools category missing")
	}

	// Cursor on the unavailable k9s row (sorted last).
	m.createPaneStep = 1
	m.selectedCategory = toolsIdx
	m.dialogCursor = 1 // k9s

	out, _ := m.handleCreatePaneSelect()
	got := out.(Model)
	if got.selectedPlugin == "k9s" {
		t.Error("selecting an unavailable plugin must be blocked, but it advanced")
	}
	if got.createPaneStep != 1 {
		t.Errorf("createPaneStep = %d, want 1 (stayed on plugin list)", got.createPaneStep)
	}
}
