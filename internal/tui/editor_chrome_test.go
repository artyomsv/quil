package tui

import (
	"strings"
	"testing"
)

// TestRenderTOMLEditorFullScreen_ReadOnlyChrome verifies the full-screen editor
// labels a read-only buffer "View:" and drops the mutating affordances (save,
// paste, cut) from the status bar — the history entry viewer and the F1 log
// viewer reuse this editor in ReadOnly mode and should not advertise editing.
func TestRenderTOMLEditorFullScreen_ReadOnlyChrome(t *testing.T) {
	m := Model{width: 80, height: 24}

	m.tomlEditor = NewTextEditor("the full prompt body", "Input @ 2026-06-17 09:47:49", 80, 22)
	m.tomlEditor.ReadOnly = true
	ro := m.renderTOMLEditorFullScreen()
	if !strings.Contains(ro, "View: ") {
		t.Errorf("read-only chrome should use 'View:' title, got:\n%s", firstLine(ro))
	}
	for _, banned := range []string{"Ctrl+S save", "Ctrl+V paste", "Edit: "} {
		if strings.Contains(ro, banned) {
			t.Errorf("read-only chrome must not contain %q", banned)
		}
	}
	if !strings.Contains(ro, "Esc close") {
		t.Errorf("read-only chrome should still show 'Esc close'")
	}

	m.tomlEditor = NewTextEditor("x = 1", "plugin.toml", 80, 22)
	m.tomlEditor.ReadOnly = false
	rw := m.renderTOMLEditorFullScreen()
	if !strings.Contains(rw, "Edit: ") {
		t.Errorf("editable chrome should use 'Edit:' title")
	}
	if !strings.Contains(rw, "Ctrl+S save") {
		t.Errorf("editable chrome should still advertise 'Ctrl+S save'")
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
