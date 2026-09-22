package tui

import (
	"fmt"
	"log"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/google/uuid"
)

// The four rows of the dialog, in tab order.
//
// templateRowDirectory is a directory BROWSER, not a text field: it drives the
// same daemon-side listing (cwdBrowse*) the pane-setup and project dialogs use,
// because the local disk is the wrong machine whenever the daemon is remote.
// That is also why it is the one row whose Enter does not submit.
const (
	templateRowTemplate = iota
	templateRowTask
	templateRowDirectory
	templateRowBranch
	templateRowCount
)

type templateDialogState struct {
	dest, projectID, projectName string
	// root is only the browser's OPENING directory. The directory actually
	// submitted is m.cwdBrowseDir, which the browser keeps live — the same
	// commit point submitProjectForm uses.
	root, branch             string
	templates                []config.Template
	selected, row            int
	editor                   *TextEditor
	requestID, err, focusTab string
	pending                  bool
}

type templateReplyMsg struct{ msg *ipc.Message }
type templateRequestTimeoutMsg string

// templateEditorRows keeps the task editor small enough that the directory
// browser below it still fits. The browser height is fixed (browserVisibleRows)
// so the box does not jump as the listing changes, matching both other dialogs.
func templateEditorRows(height int) int {
	return max(2, min(5, height-26))
}

func (m Model) openNewTemplate() (tea.Model, tea.Cmd) {
	f := templateDialogState{dest: m.activeDest()}
	if p := m.activeProjectModel(); p != nil {
		f.projectID, f.projectName, f.root = p.ID, p.Name, p.RootDir
	}
	templates, err := config.LoadTemplates()
	if err != nil {
		f.err = "Cannot load templates: " + err.Error()
	} else {
		f.templates = templates.Templates
	}
	f.editor = NewTextEditor("", "", max(12, dialogWidth-12), templateEditorRows(m.height))
	f.editor.Highlight, f.editor.SoftWrap = HighlightPlain, true
	m.templateUI, m.dialog = f, dialogNewTemplate
	// Synchronous, ahead of the request, for the reason that helper documents:
	// the browser state is shared with two other dialogs and this one commits
	// m.cwdBrowseDir, so a previous session's directory must not survive into
	// the window before the answer lands.
	m.resetDirBrowseState()
	var browse tea.Cmd
	if m.client != nil {
		// An empty root is a LEGAL request, not a skipped one: the daemon
		// answers it with its own default directory, which is the honest
		// starting point for a project that has no root recorded.
		browse = m.requestBrowseDirForDest(f.dest, f.root, "", "")
	}
	return m, tea.Batch(tea.ClearScreen, browse)
}

// This is the single focus decision for typing and both paste transports.
// The directory row is absent deliberately — it is a browser, so a paste there
// is a path to navigate to (handled in handleTemplateBrowseKey), never text
// appended to a field.
func (m Model) templateTextTarget() string {
	if m.dialog != dialogNewTemplate {
		return ""
	}
	switch m.templateUI.row {
	case templateRowTask:
		return "task"
	case templateRowBranch:
		return "branch"
	}
	return ""
}

func (m *Model) templateInsertText(text string) {
	switch m.templateTextTarget() {
	case "task":
		m.templateUI.editor.InsertMultiLine(text)
		m.templateUI.editor.Dirty = true
		m.templateUI.editor.ensureCursorVisible()
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
	// Even the rows with no text field (template selector, directory browser)
	// consume paste. It must never reach the live pane behind the dialog.
	return true
}

func (m Model) handleTemplateDialogKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	f := &m.templateUI
	key := msg.String()
	if key == "esc" {
		f.requestID = ""
		m.resetDirBrowseState()
		m.dialog = dialogNone
		return m, tea.ClearScreen
	}
	if f.pending {
		return m, nil
	}
	switch key {
	case "ctrl+s":
		return m.submitTemplateDialog()
	case "tab", "shift+tab":
		delta := 1
		if key == "shift+tab" {
			delta = templateRowCount - 1
		}
		f.row = (f.row + delta) % templateRowCount
		return m, nil
	}

	if f.row == templateRowDirectory {
		return m.handleTemplateBrowseKey(key)
	}

	// Enter creates from every row but the task editor, where it is a newline.
	// Ctrl+S above works from all four, including that one.
	if key == "enter" && f.row != templateRowTask {
		return m.submitTemplateDialog()
	}
	if key == "ctrl+v" {
		return m, f.editor.pasteCmd()
	}
	if f.row == templateRowTemplate && (key == "left" || key == "right") && len(f.templates) > 0 {
		delta := 1
		if key == "left" {
			delta = -1
		}
		f.selected = (f.selected + delta + len(f.templates)) % len(f.templates)
		return m, nil
	}
	if msg.Text != "" {
		// String() describes keys (including "space"); Text is their content.
		m.templateInsertText(msg.Text)
		return m, nil
	}
	if f.row == templateRowTask {
		_, _, cmd := f.editor.HandleKey(key)
		return m, cmd
	}
	if key == "backspace" && f.row == templateRowBranch {
		runes := []rune(f.branch)
		if len(runes) > 0 {
			f.branch = string(runes[:len(runes)-1])
		}
	}
	return m, nil
}

// handleTemplateBrowseKey drives the directory row. Mirrors
// handleProjectRootDirKey: no pick-list mode, and navigation goes through
// templateBrowseTo/Up so every request is stamped for the destination this
// dialog pinned at open, not for whatever project happens to be active when a
// key is pressed.
func (m Model) handleTemplateBrowseKey(key string) (tea.Model, tea.Cmd) {
	if len(m.cwdBrowseEntries) == 0 {
		switch key {
		case "enter":
			// Nothing to navigate — Enter still creates, using whatever the
			// browser resolved (commonly the project root; empty means the
			// daemon's own default).
			return m.submitTemplateDialog()
		case "ctrl+v":
			// Falls through to the paste branch below. After a listing that
			// failed, a pasted path is the only way out of an empty browser.
		default:
			return m, nil
		}
	}

	switch key {
	case "up", "k":
		if m.cwdBrowseCursor > 0 {
			m.cwdBrowseCursor--
			m.adjustBrowseScroll()
		}
		return m, nil

	case "down", "j":
		if m.cwdBrowseCursor < len(m.cwdBrowseEntries)-1 {
			m.cwdBrowseCursor++
			m.adjustBrowseScroll()
		}
		return m, nil

	case "pgup":
		m.cwdBrowseCursor -= browserVisibleRows
		if m.cwdBrowseCursor < 0 {
			m.cwdBrowseCursor = 0
		}
		m.adjustBrowseScroll()
		return m, nil

	case "pgdown":
		m.cwdBrowseCursor += browserVisibleRows
		if m.cwdBrowseCursor > len(m.cwdBrowseEntries)-1 {
			m.cwdBrowseCursor = len(m.cwdBrowseEntries) - 1
		}
		m.adjustBrowseScroll()
		return m, nil

	case "home":
		m.cwdBrowseCursor = 0
		m.adjustBrowseScroll()
		return m, nil

	case "end":
		m.cwdBrowseCursor = len(m.cwdBrowseEntries) - 1
		m.adjustBrowseScroll()
		return m, nil

	case "enter", "right", "l":
		entry := m.cwdBrowseEntries[m.cwdBrowseCursor]
		switch {
		case entry == "..":
			return m, m.templateBrowseUp()
		case m.cwdBrowseDir == "":
			// Root list: every row is already a full root path.
			return m, m.templateBrowseTo(entry, "", "")
		default:
			// Child, not a join — the daemon joins with its own separator, so
			// a Windows TUI against a Linux daemon cannot ask for a path
			// shaped like its own filesystem.
			return m, m.templateBrowseTo(m.cwdBrowseDir, entry, "")
		}

	case "backspace", "left", "h":
		return m, m.templateBrowseUp()

	case "ctrl+v":
		text, err := clipboardReadText()
		if err != nil {
			log.Printf("template dialog: clipboard read: %v", err)
			m.cwdInputError = fmt.Sprintf("clipboard: %v", err)
			return m, nil
		}
		path := sanitizePastedPath(text)
		if path == "" {
			return m, nil
		}
		return m, m.templateBrowseTo(path, "", "")
	}
	return m, nil
}

// templateBrowseTo/templateBrowseUp mirror projectBrowseTo/projectBrowseUp:
// the request is stamped for m.templateUI.dest, which openNewTemplate pinned,
// because the tab is created on that daemon whatever the user does to the
// active project while the dialog is open. showRootsList and browseLeaf are
// reused as-is — both are pure functions of the daemon's own answers.
func (m *Model) templateBrowseTo(path, child, selectName string) tea.Cmd {
	m.cwdInputError = ""
	return m.requestBrowseDirForDest(m.templateUI.dest, path, child, selectName)
}

func (m *Model) templateBrowseUp() tea.Cmd {
	if m.cwdBrowseDir == "" {
		return nil // already showing the root list; nothing above it
	}
	if m.cwdBrowseParent == "" {
		if len(m.cwdBrowseRoots) > 0 {
			m.showRootsList()
		}
		return nil
	}
	return m.templateBrowseTo(m.cwdBrowseParent, "", browseLeaf(m.cwdBrowseDir, m.cwdBrowseParent))
}

// submitTemplateDialog sends the creation request. The committed directory is
// m.cwdBrowseDir, exactly as submitProjectForm commits it.
//
// Blocked while m.browse.pending for the same reason that one is: the round
// trip resolving the directory may still be in flight, and cwdBrowseDir is ""
// until it lands — so submitting now would create the tab against the daemon's
// default directory rather than the one the user is looking at. It is a wait,
// not a dead end: pending always resolves, from a response or from
// requestBrowseDirForDest's own timeout.
func (m Model) submitTemplateDialog() (tea.Model, tea.Cmd) {
	f := &m.templateUI
	if len(f.templates) == 0 {
		return m, nil
	}
	if m.browse.pending {
		f.err = "Reading the directory… try again in a moment."
		return m, nil
	}
	msg, err := ipc.NewMessage(ipc.MsgCreateFromTemplateReq, ipc.CreateFromTemplateReqPayload{
		Template: f.templates[f.selected].Name, Task: f.editor.Content(),
		CWD: m.cwdBrowseDir, Branch: f.branch, ProjectID: f.projectID,
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
	m.resetDirBrowseState()
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
		b.WriteString(dialogErrorStyle.Render(sanitizeRemoteText(f.err)) + "\n")
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
	b.WriteString(mark(templateRowTemplate) + "Template: " + sanitizeRemoteText(name) + "\n")

	b.WriteString(mark(templateRowTask) + "Task:\n")
	if f.editor != nil {
		e := *f.editor
		e.ViewWidth, e.ViewHeight = inner, templateEditorRows(m.height)
		e.Lines = append([]string(nil), e.Lines...)
		for i := range e.Lines {
			e.Lines[i] = sanitizeRemoteText(e.Lines[i])
		}
		b.WriteString(e.Render())
	}

	b.WriteString(mark(templateRowDirectory) + "Directory:\n")
	b.WriteString(m.renderTemplateBrowser(inner))

	b.WriteString(mark(templateRowBranch) + "New branch: " + sanitizeRemoteText(f.branch) + "\n")
	b.WriteString("Tab: next field · Ctrl+S or Enter: create · Esc: cancel")
	return b.String()
}

// renderTemplateBrowser paints the directory row's listing. Always allocates
// browserVisibleRows lines so the dialog height stays stable across
// navigation, matching the other two browsers.
func (m Model) renderTemplateBrowser(textWidth int) string {
	var b strings.Builder
	focused := m.templateUI.row == templateRowDirectory

	path, prefix := sanitizeRemoteText(m.cwdBrowseDir), "    "
	switch {
	case path == "" && len(m.cwdBrowseEntries) > 0:
		path = dialogSubtle.Render("Select drive:")
	case path == "":
		path = dialogSubtle.Render("(no directory loaded — daemon default will be used)")
	case focused:
		path = dialogValStyle.Render(truncateToWidth(path, textWidth-setupRowIndent))
	default:
		prefix = setupRowIdleMark
		path = dialogSelectedIdle.Render(truncateToWidth(path, textWidth-setupRowIndent))
	}
	b.WriteString(prefix + path + "\n")

	if m.cwdInputError != "" {
		b.WriteString("    " + dialogErrorStyle.Render("✗ "+m.cwdInputError) + "\n")
	}

	entries := m.cwdBrowseEntries
	for i := 0; i < browserVisibleRows; i++ {
		idx := m.cwdBrowseScroll + i
		if idx >= len(entries) {
			b.WriteString("\n")
			continue
		}
		displayName := entries[idx]
		if displayName != ".." && !strings.HasSuffix(displayName, `\`) {
			displayName += "/"
		}
		displayName = truncateToWidth(sanitizeRemoteText(displayName), textWidth-setupRowIndent)
		if focused && idx == m.cwdBrowseCursor {
			b.WriteString("  > " + dialogSelected.Render(displayName) + "\n")
		} else {
			b.WriteString("    " + dialogNormal.Render(displayName) + "\n")
		}
	}

	switch {
	case m.browse.err != "":
		b.WriteString(dialogSubtle.Render("    ✗ "+sanitizeRemoteText(m.browse.err)) + "\n")
	case m.browse.pending:
		b.WriteString(dialogSubtle.Render("    (loading…)") + "\n")
	case len(entries) > 0:
		hint := "↑↓ move  Enter descend  ← up  Ctrl+V paste"
		if len(entries) > browserVisibleRows {
			hint = fmt.Sprintf("%d/%d  %s", m.cwdBrowseCursor+1, len(entries), hint)
		}
		if m.cwdBrowseTruncated {
			hint = truncatedHintPrefix + hint
		}
		b.WriteString(dialogSubtle.Render("    "+hint) + "\n")
	case m.cwdBrowseTruncated:
		b.WriteString(dialogSubtle.Render("    "+truncatedHintPrefix) + "\n")
	default:
		b.WriteString(dialogSubtle.Render("    (empty directory)") + "\n")
	}
	return b.String()
}
