package tui

import (
	"errors"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

func TestTemplateSettings_F1Editor_ValidatesSavesAndReloads(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := paletteModelWithProjects(t)
	m.initKeymap()
	m = templateUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyF1})
	m = templateUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // Settings is the first F1 row.
	if m.dialog != dialogSettings {
		t.Fatal("F1 did not open Settings")
	}
	found := false
	for i, field := range settingsFields() {
		if field.label == "Templates" {
			m.dialogCursor, found = i, true
		}
	}
	if !found {
		t.Fatal("Templates settings row is missing")
	}
	m = templateUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.dialog != dialogTOMLEditor || !m.templateEditor || m.tomlEditor.Content() != config.DefaultTemplatesText() {
		t.Fatal("missing file did not open embedded default in TOML editor")
	}
	if _, err := os.Stat(config.TemplatesPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("opening editor wrote the file", err)
	}
	conn := m.client.(*fakeConn)
	for _, invalid := range []string{"templates = [", "[[templates]]\nname = 'bad'\nlayout = 'diagonal'\n[[templates.panes]]\ntype = 'terminal'"} {
		m.tomlEditor.Lines = strings.Split(invalid, "\n")
		m = templateUpdate(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		if m.dialog != dialogTOMLEditor || m.tomlEditor.SaveErr == "" || !strings.Contains(m.renderTOMLEditorFullScreen(), "Error:") {
			t.Fatal("invalid template did not stay open with a visible error")
		}
		if _, err := os.Stat(config.TemplatesPath()); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("invalid save wrote a file", err)
		}
		conn.mu.Lock()
		for _, msg := range conn.sent {
			if msg.Type == ipc.MsgReloadPlugins {
				t.Error("invalid save sent reload")
			}
		}
		conn.mu.Unlock()
	}
	valid := "# Keep this comment and formatting\n[[templates]]\nname = 'my-template'\nlayout = 'grid'\n[[templates.panes]]\ntype = 'terminal'\nprompt = '''First line\nSecond line'''\n"
	m.tomlEditor.Lines = strings.Split(valid, "\n")
	m = templateUpdate(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.dialog != dialogSettings || m.tomlEditor != nil || m.templateEditor {
		t.Fatal("valid save did not return to Settings")
	}
	data, err := os.ReadFile(config.TemplatesPath())
	if err != nil || string(data) != valid {
		t.Fatalf("save changed source: %q, %v", data, err)
	}
	loaded, err := config.LoadTemplates()
	if err != nil || len(loaded.Templates) != 1 || loaded.Templates[0].Name != "my-template" {
		t.Fatal(loaded, err)
	}
	if sent := conn.lastSent(); sent == nil || sent.Type != ipc.MsgReloadPlugins {
		t.Fatal("valid save did not reload daemon", sent)
	}
	// Invalid replacement must preserve a previously saved file as well.
	next, _ := m.openTemplateSettings()
	m = next.(Model)
	m.tomlEditor.Lines = []string{"templates = []"}
	m = templateUpdate(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	data, err = os.ReadFile(config.TemplatesPath())
	if err != nil || string(data) != valid {
		t.Fatal("invalid replacement changed the previous file", err)
	}
	m = templateUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.dialog != dialogSettings {
		t.Fatal("Escape did not return to Settings")
	}
}

func TestTemplateSettings_RemoteProject_ReloadsOnlyTheLocalFileOwner(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	local, remote := newFakeConn(), newFakeConn()
	router := NewRouter(map[string]Client{"": local, "gpu01": remote})
	t.Cleanup(func() { router.Remove(""); router.Remove("gpu01") })
	m := paletteModelWithProjects(t)
	m.initKeymap()
	m.client, m.activeProject = router, 1
	next, _ := m.openTemplateSettings()
	m = templateUpdate(t, next.(Model), tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.dialog != dialogSettings || remote.sentCount() != 0 || local.lastSent() == nil || local.lastSent().Type != ipc.MsgReloadPlugins {
		t.Fatal("local template save reloaded the wrong daemon")
	}
}
