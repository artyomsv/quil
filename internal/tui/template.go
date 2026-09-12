package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/google/uuid"
)

type templateDialogState struct {
	dest, projectID, projectName string
	cwd, branch                  string
	templates                    []config.Template
	selected, row                int
	repos                        []string
	editor                       *TextEditor
	requestID, err, focusTab     string
	pending                      bool
}

type templateReplyMsg struct{ msg *ipc.Message }
type templateRequestTimeoutMsg string

func (m Model) openNewTemplate() (tea.Model, tea.Cmd) {
	f := templateDialogState{dest: m.activeDest()}
	if p := m.activeProjectModel(); p != nil {
		f.projectID, f.projectName, f.cwd = p.ID, p.Name, p.RootDir
	}
	templates, err := config.LoadTemplates()
	if err != nil {
		f.err = "Cannot load templates: " + err.Error()
	} else {
		f.templates = templates.Templates
	}
	f.editor = NewTextEditor("", "", max(12, dialogWidth-12), max(2, min(8, m.height-16)))
	f.editor.Highlight, f.editor.SoftWrap = HighlightPlain, true
	m.templateUI, m.dialog = f, dialogNewTemplate
	var scan tea.Cmd
	if m.client != nil && f.cwd != "" {
		scan = m.requestGitRepos(f.dest, f.cwd, "", repoScanTemplate, "")
	}
	return m, tea.Batch(tea.ClearScreen, scan)
}

// This is the single focus decision for typing and both paste transports.
func (m Model) templateTextTarget() string {
	if m.dialog != dialogNewTemplate {
		return ""
	}
	switch m.templateUI.row {
	case 1:
		return "task"
	case 2:
		return "directory"
	case 3:
		return "branch"
	}
	return ""
}

func (m *Model) templateInsertText(text string) {
	switch m.templateTextTarget() {
	case "task":
		m.templateUI.editor.InsertMultiLine(strings.ReplaceAll(text, "\r", ""))
		m.templateUI.editor.Dirty = true
		m.templateUI.editor.ensureCursorVisible()
	case "directory":
		m.templateUI.cwd += sanitizeDialogInput(text)
	case "branch":
		m.templateUI.branch += sanitizeDialogInput(text)
	}
}

func (m *Model) templatePaste(text string) bool {
	if m.dialog != dialogNewTemplate {
		return false
	}
	if !m.templateUI.pending {
		m.templateInsertText(text)
	}
	// Even the template selector (no text field) consumes paste. It must
	// never reach the live pane behind the dialog.
	return true
}

func (m Model) handleTemplateDialogKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	f := &m.templateUI
	key := msg.String()
	if key == "esc" {
		f.requestID = ""
		if m.repoScan.purpose == repoScanTemplate {
			m.repoScan = repoScanState{}
		}
		m.dialog = dialogNone
		return m, tea.ClearScreen
	}
	if f.pending {
		return m, nil
	}
	switch key {
	case "ctrl+s":
		if len(f.templates) == 0 {
			return m, nil
		}
		msg, err := ipc.NewMessage(ipc.MsgCreateFromTemplateReq, ipc.CreateFromTemplateReqPayload{
			Template: f.templates[f.selected].Name, Task: f.editor.Content(),
			CWD: f.cwd, Branch: f.branch, ProjectID: f.projectID,
		})
		f.requestID = "template-ui-" + uuid.NewString()
		if err == nil {
			msg.ID = f.requestID
			if m.client == nil {
				err = fmt.Errorf("daemon is not connected")
			} else {
				err = m.sendForDestStrict(f.dest, msg)
			}
		}
		if err != nil {
			f.err = err.Error()
			return m, nil
		}
		f.pending, f.err = true, ""
		id := f.requestID
		return m, tea.Tick(15*time.Second, func(time.Time) tea.Msg { return templateRequestTimeoutMsg(id) })
	case "tab", "shift+tab":
		delta := 1
		if key == "shift+tab" {
			delta = 3
		}
		f.row = (f.row + delta) % 4
		return m, nil
	case "ctrl+v":
		return m, f.editor.pasteCmd()
	}
	if (f.row == 0 || f.row == 2) && (key == "left" || key == "right") {
		delta := 1
		if key == "left" {
			delta = -1
		}
		if f.row == 0 && len(f.templates) > 0 {
			f.selected = (f.selected + delta + len(f.templates)) % len(f.templates)
		} else if f.row == 2 && len(f.repos) > 0 {
			idx := -1
			for i, repo := range f.repos {
				if repo == f.cwd {
					idx = i
					break
				}
			}
			if idx < 0 {
				idx = 0
				if delta < 0 {
					idx = len(f.repos) - 1
				}
			} else {
				idx = (idx + delta + len(f.repos)) % len(f.repos)
			}
			f.cwd = f.repos[idx]
		}
		return m, nil
	}
	if msg.Text != "" {
		// String() describes keys (including "space"); Text is their content.
		m.templateInsertText(msg.Text)
		return m, nil
	}
	if m.templateTextTarget() == "task" {
		_, _, cmd := f.editor.HandleKey(key)
		return m, cmd
	}
	if key == "backspace" {
		var field *string
		switch m.templateTextTarget() {
		case "directory":
			field = &f.cwd
		case "branch":
			field = &f.branch
		}
		if field != nil {
			runes := []rune(*field)
			if len(runes) > 0 {
				*field = string(runes[:len(runes)-1])
			}
		}
	}
	return m, nil
}

func (m *Model) applyTemplateReply(reply templateReplyMsg) tea.Cmd {
	msg := reply.msg
	f := &m.templateUI
	if f.requestID == "" || msg.ID != f.requestID || msg.Origin != f.dest {
		return nil
	}
	f.pending, f.requestID = false, ""
	var resp ipc.CreateFromTemplateRespPayload
	if err := msg.DecodePayload(&resp); err != nil {
		f.err = err.Error()
		return nil
	}
	if resp.Error != "" {
		f.err = resp.Error
		return nil
	}
	m.dialog, f.focusTab = dialogNone, resp.TabID
	return tea.Batch(m.focusNewTemplateTab(), tea.ClearScreen)
}

// Only this client's matching response arms focus, whether it arrives before
// or after the workspace frame. Other clients merely adopt the new tab.
func (m *Model) focusNewTemplateTab() tea.Cmd {
	f := &m.templateUI
	if f.focusTab == "" {
		return nil
	}
	for pi, project := range m.projects {
		if project.Dest != f.dest {
			continue
		}
		for ti, tab := range project.tabs {
			if tab.ID != f.focusTab {
				continue
			}
			projectCmd := m.switchProject(pi)
			tabCmd := m.switchTab(ti)
			m.finalizeTabPanes(tab)
			f.focusTab = ""
			return tea.Batch(projectCmd, tabCmd)
		}
	}
	return nil
}

func (m Model) renderTemplateDialog() string {
	f := m.templateUI
	inner := dialogInnerWidth(m.width, dialogWidth)
	var b strings.Builder
	b.WriteString(dialogTitle.Render("New from template") + "\n")
	b.WriteString("Project: " + sanitizeRemoteText(f.projectName) + "\n")
	if f.pending {
		b.WriteString("Waiting for daemon…\n")
	}
	if f.err != "" {
		b.WriteString(sanitizeRemoteText(f.err) + "\n")
	}
	mark := func(row int) string {
		if row == f.row {
			return "> "
		}
		return "  "
	}
	name := "(no templates available)"
	if len(f.templates) > 0 {
		tpl := f.templates[f.selected]
		name = tpl.Name + " — " + tpl.Description
	}
	b.WriteString(mark(0) + "Template: " + sanitizeRemoteText(name) + "\n")
	b.WriteString(mark(1) + "Task:\n")
	if f.editor != nil {
		e := *f.editor
		e.ViewWidth, e.ViewHeight = inner, max(2, min(8, m.height-16))
		e.Lines = append([]string(nil), e.Lines...)
		for i := range e.Lines {
			e.Lines[i] = sanitizeRemoteText(e.Lines[i])
		}
		b.WriteString(e.Render())
	}
	b.WriteString(mark(2) + "Directory: " + sanitizeRemoteText(f.cwd) + "\n")
	b.WriteString(mark(3) + "New branch: " + sanitizeRemoteText(f.branch) + "\n")
	b.WriteString("Tab: next field · ←→: template / directory · Ctrl+S: create · Esc: cancel")
	return b.String()
}
