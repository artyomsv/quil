package tui

import (
	"fmt"
	"log"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// submitNewProject creates a project on the ACTIVE project's daemon. A
// project is a name plus a root directory, and a root directory lives on
// exactly one machine — so the new project belongs wherever the user is
// currently working. sendForDest (not a raw Origin assignment) is what
// makes this correct when the active project is local: its Dest is "",
// which stampDest maps to the destLocal sentinel, so an unstamped send is
// never confused with one that deliberately named the local daemon.
func (m *Model) submitNewProject(name, rootDir string) tea.Cmd {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	msg, err := ipc.NewMessage(ipc.MsgCreateProject, ipc.CreateProjectPayload{
		Name:    name,
		RootDir: strings.TrimSpace(rootDir),
	})
	if err != nil {
		log.Printf("create project: encode: %v", err)
		return nil
	}
	if sendErr := m.sendForDest(m.activeDest(), msg); sendErr != nil {
		log.Printf("create project: send: %v", sendErr)
	}
	m.dialog = dialogNone
	return nil
}

// submitRenameProject updates an EXISTING project's name/root dir. Unlike
// submitNewProject, the target daemon is the project's OWN dest — resolved
// by ID rather than assumed to be the active one, because Rename can be
// opened on a background project via the sidebar context menu without first
// switching to it.
func (m *Model) submitRenameProject(id, name, rootDir string) tea.Cmd {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	msg, err := ipc.NewMessage(ipc.MsgUpdateProject, ipc.UpdateProjectPayload{
		ProjectID: id,
		Name:      name,
		RootDir:   strings.TrimSpace(rootDir),
	})
	if err != nil {
		log.Printf("rename project %s: encode: %v", id, err)
		return nil
	}
	if sendErr := m.sendForDest(m.destOfProject(id), msg); sendErr != nil {
		log.Printf("rename project %s: send: %v", id, sendErr)
	}
	m.dialog = dialogNone
	return nil
}

// confirmDestroyProject opens the shared confirm dialog. Destroying a
// project takes every tab and pane under it, so it never fires straight off
// a keystroke — the accept path lives in dialog.go's handleConfirmKey
// if-chain (confirmKindDestroyProject), matching the existing convention
// (openClosePaneConfirm et al.): set dialog + confirmKind/ID/Name, no
// callback fields on Model.
func (m *Model) confirmDestroyProject(id string) tea.Cmd {
	p := m.projectByID(id)
	if p == nil {
		return nil
	}
	m.dialog = dialogConfirm
	m.confirmKind = confirmKindDestroyProject
	m.confirmID = id
	m.confirmName = p.Name
	return nil
}

// openNewProjectDialog resets the shared project-form state for a fresh
// project and kicks off the root-dir browser at the active daemon's default
// directory (an empty path — same starting point requestBrowseDir's callers
// use before any pre-fill chain narrows it — since a new project has no
// existing location to anchor on).
func (m Model) openNewProjectDialog() (tea.Model, tea.Cmd) {
	m.dialog = dialogProjectNew
	m.projectFormID = ""
	m.projectFormName = ""
	m.projectFormCursor = 0
	m.projectFormErr = ""
	m.projectFormDest = m.activeDest()
	return m, m.requestBrowseDirForDest(m.projectFormDest, "", "", "")
}

// beginProjectRename opens the Rename dialog pre-filled from the target
// project — reachable from the sidebar context menu, so id is not
// necessarily the active project.
func (m Model) beginProjectRename(id string) (tea.Model, tea.Cmd) {
	p := m.projectByID(id)
	if p == nil {
		return m, nil
	}
	m.dialog = dialogProjectRename
	m.projectFormID = p.ID
	m.projectFormName = p.Name
	m.projectFormCursor = 0
	m.projectFormErr = ""
	m.projectFormDest = p.Dest
	return m, m.requestBrowseDirForDest(m.projectFormDest, p.RootDir, "", "")
}

// projectBrowseTo/projectBrowseUp mirror dialog.go's browseTo/browseUp for
// the project form's root-dir field. They cannot reuse those directly:
// browseTo's requestBrowseDir call is unstamped (resolves to whatever
// project is currently active), and this field's browse target is
// m.projectFormDest — the OWNING project's dest for Rename, which the active
// project need not be. showRootsList and browseLeaf ARE reused as-is: both
// are pure functions of the daemon's own answers (roots, parent), with no
// notion of which dialog or destination is asking.
func (m *Model) projectBrowseTo(path, child, selectName string) tea.Cmd {
	m.cwdInputError = ""
	return m.requestBrowseDirForDest(m.projectFormDest, path, child, selectName)
}

func (m *Model) projectBrowseUp() tea.Cmd {
	if m.cwdBrowseDir == "" {
		return nil // already showing the root list; nothing above it
	}
	if m.cwdBrowseParent == "" {
		if len(m.cwdBrowseRoots) > 0 {
			m.showRootsList()
		}
		return nil
	}
	return m.projectBrowseTo(m.cwdBrowseParent, "", browseLeaf(m.cwdBrowseDir, m.cwdBrowseParent))
}

// handleProjectDialogKey drives both dialogProjectNew and dialogProjectRename
// — m.projectFormID ("" vs a real ID) is what submitProjectForm uses to tell
// them apart; the key handling itself is identical. Three focusable rows:
// 0 = Name (typed text), 1 = Root directory (the daemon-side browser,
// mirroring the pane-setup dialog's CWD field), 2 = the submit button.
func (m Model) handleProjectDialogKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch key {
	case "esc":
		m.dialog = dialogNone
		return m, tea.ClearScreen
	case "tab":
		m.projectFormCursor = (m.projectFormCursor + 1) % 3
		return m, nil
	case "shift+tab":
		m.projectFormCursor = (m.projectFormCursor + 2) % 3
		return m, nil
	}

	switch m.projectFormCursor {
	case 0:
		return m.handleProjectNameKey(key)
	case 1:
		return m.handleProjectRootDirKey(key)
	case 2:
		switch key {
		case "up", "k":
			m.projectFormCursor = 1
			return m, nil
		case "down", "j":
			m.projectFormCursor = 0
			return m, nil
		case "enter":
			return m.submitProjectForm()
		}
	}
	return m, nil
}

// handleProjectNameKey handles the Name field. Append/backspace on raw
// bytes, no separate edit-mode toggle — mirrors handleRenameKey's tab/pane
// rename input, the existing convention for a single always-editable text
// field in this package.
func (m Model) handleProjectNameKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "enter":
		return m.submitProjectForm()
	case "backspace":
		if len(m.projectFormName) > 0 {
			m.projectFormName = m.projectFormName[:len(m.projectFormName)-1]
		}
		return m, nil
	default:
		if len(key) == 1 {
			m.projectFormName += key
			m.projectFormErr = ""
		} else if key == "space" {
			m.projectFormName += " "
			m.projectFormErr = ""
		}
		return m, nil
	}
}

// handleProjectRootDirKey handles the Root directory field: the daemon-side
// directory browser, reusing the same cwdBrowse* listing the pane-setup
// dialog's CWD field populates (see the Model field doc comment) — but with
// no pick-list mode (no git/recent-CWD candidates; a project's root is
// picked once, not on every pane spawn) and through projectBrowseTo/Up
// rather than browseTo/browseUp, so the request is stamped for
// m.projectFormDest. Mirrors handleSetupCWDKey.
func (m Model) handleProjectRootDirKey(key string) (tea.Model, tea.Cmd) {
	if len(m.cwdBrowseEntries) == 0 {
		switch key {
		case "enter":
			// Browser failed to load — Enter still moves on, submitting
			// with whatever cwdBrowseDir currently holds (commonly "").
			return m.submitProjectForm()
		case "ctrl+v":
			// Falls through to the paste branch below — there is nothing to
			// navigate, but a pasted path can still get the browser
			// somewhere.
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
			return m, m.projectBrowseUp()
		case m.cwdBrowseDir == "":
			// Root list: every row is already a full root path.
			return m, m.projectBrowseTo(entry, "", "")
		default:
			// Child, not a join — the daemon joins with its own separator.
			return m, m.projectBrowseTo(m.cwdBrowseDir, entry, "")
		}

	case "backspace", "left", "h":
		return m, m.projectBrowseUp()

	case "ctrl+v":
		text, err := clipboardReadText()
		if err != nil {
			log.Printf("project dialog: clipboard read: %v", err)
			m.cwdInputError = fmt.Sprintf("clipboard: %v", err)
			return m, nil
		}
		path := sanitizePastedPath(text)
		if path == "" {
			return m, nil
		}
		return m, m.projectBrowseTo(path, "", "")
	}
	return m, nil
}

// submitProjectForm validates the Name field and dispatches to
// submitNewProject or submitRenameProject depending on projectFormID. The
// committed root dir is simply m.cwdBrowseDir — the field the browser keeps
// live — exactly like submitSetupDialog's selectedCWD = cwdBrowseDir
// capture.
func (m Model) submitProjectForm() (tea.Model, tea.Cmd) {
	if strings.TrimSpace(m.projectFormName) == "" {
		m.projectFormErr = "name required"
		m.projectFormCursor = 0
		return m, nil
	}
	rootDir := m.cwdBrowseDir
	if m.projectFormID == "" {
		return m, m.submitNewProject(m.projectFormName, rootDir)
	}
	return m, m.submitRenameProject(m.projectFormID, m.projectFormName, rootDir)
}

// renderProjectDialog renders both dialogProjectNew and dialogProjectRename
// — a Name field, a Root directory field (the directory browser, always
// showing browserVisibleRows lines so the box height stays stable across
// navigation, matching renderCreatePaneSetupDialog's CWD field), and a
// submit button. Deliberately does not reuse
// renderCreatePaneSetupDialog's CWD block outright: that function is
// entangled with plugin toggles, kube, and the session picker, none of
// which apply here, and threading a plugin-shaped call through it would
// cost more than the small amount of duplicated rendering below.
func (m Model) renderProjectDialog() string {
	var b strings.Builder

	title := "New Project"
	submitLabel := "Create"
	if m.projectFormID != "" {
		title = "Rename Project"
		submitLabel = "Save"
	}
	b.WriteString(dialogTitle.Render(title))
	b.WriteString("\n\n")

	textWidth := dialogInnerWidth(m.width, dialogWidth)

	// Name field.
	nameFocused := m.projectFormCursor == 0
	if nameFocused {
		b.WriteString(dialogSelected.Render("> Name:") + "\n")
	} else {
		b.WriteString(dialogNormal.Render("  Name:") + "\n")
	}
	switch {
	case m.projectFormName == "":
		b.WriteString("    " + dialogSubtle.Render("(required)") + "\n")
	case nameFocused:
		b.WriteString("    " + dialogValStyle.Render(truncateToWidth(m.projectFormName, textWidth-setupRowIndent)) + "\n")
	default:
		b.WriteString(setupRowIdleMark + dialogSelectedIdle.Render(truncateToWidth(m.projectFormName, textWidth-setupRowIndent)) + "\n")
	}
	b.WriteString("\n")

	// Root directory field — the daemon-side browser.
	rootFocused := m.projectFormCursor == 1
	if rootFocused {
		b.WriteString(dialogSelected.Render("> Root directory:") + "\n")
	} else {
		b.WriteString(dialogNormal.Render("  Root directory:") + "\n")
	}

	path, prefix := sanitizeRemoteText(m.cwdBrowseDir), "    "
	switch {
	case path == "" && len(m.cwdBrowseEntries) > 0:
		path = dialogSubtle.Render("Select drive:")
	case path == "":
		path = dialogSubtle.Render("(no directory loaded — daemon default will be used)")
	case rootFocused:
		path = dialogValStyle.Render(path)
	default:
		prefix = setupRowIdleMark
		path = dialogSelectedIdle.Render(path)
	}
	b.WriteString(prefix + path + "\n")

	if m.cwdInputError != "" {
		b.WriteString("    " + dialogErrorStyle.Render("✗ "+m.cwdInputError) + "\n")
	}

	// Listing window — always allocated so the dialog height stays stable
	// whether or not the field is currently focused.
	entries := m.cwdBrowseEntries
	visible := browserVisibleRows
	start := m.cwdBrowseScroll
	for i := 0; i < visible; i++ {
		idx := start + i
		if idx >= len(entries) {
			b.WriteString("\n")
			continue
		}
		name := entries[idx]
		displayName := name
		if name != ".." && !strings.HasSuffix(name, `\`) {
			displayName = name + "/"
		}
		displayName = truncateToWidth(sanitizeRemoteText(displayName), textWidth-setupRowIndent)
		if rootFocused && idx == m.cwdBrowseCursor {
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
		if len(entries) > visible {
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
	b.WriteString("\n")

	if m.projectFormErr != "" {
		b.WriteString("  " + dialogErrorStyle.Render("✗ "+m.projectFormErr) + "\n\n")
	}

	// Submit button.
	submitFocused := m.projectFormCursor == 2
	label := "[ " + submitLabel + " ]"
	if submitFocused {
		b.WriteString(dialogSelected.Render("> "+label) + "\n\n")
	} else {
		b.WriteString(dialogNormal.Render("  "+label) + "\n\n")
	}

	b.WriteString(dialogSubtle.Render("Tab next field    Enter select    Esc cancel"))

	return b.String()
}
