package tui

import (
	"errors"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/artyomsv/quil/internal/config"
)

func (m Model) openTemplateSettings() (tea.Model, tea.Cmd) {
	path := config.TemplatesPath()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data = []byte(config.DefaultTemplatesText())
	} else if err != nil {
		m.setFlash("Cannot read templates: " + err.Error())
		return m, m.flashCmd()
	}
	m.tomlEditor = NewTextEditor(string(data), path, m.width, max(2, m.height-2))
	m.tomlEditor.SaveContent = config.WriteTemplatesSource
	m.templateEditor, m.dialog = true, dialogTOMLEditor
	return m, tea.ClearScreen
}
