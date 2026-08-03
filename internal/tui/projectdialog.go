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
	// projectFormDest, not activeDest: the Host field may have pointed this
	// form at a different machine — possibly one connected seconds ago — and
	// the root dir below was browsed on THAT filesystem. Creating the project
	// on the active daemon instead would pair a name with a path that does not
	// exist there.
	if sendErr := m.sendForDest(m.projectFormDest, msg); sendErr != nil {
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
	m.projectFormHost = ""
	m.projectFormUser = ""
	m.projectFormRemote = false
	m.projectFormDialing = ""
	m.projectFormCursor = 0
	m.projectFormErr = ""
	m.projectFormDest = m.activeDest()
	m.resetProjectBrowseState()
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
	m.projectFormRemote = p.Dest != ""
	m.projectFormUser, m.projectFormHost = splitSSHDest(p.Dest)
	m.projectFormDialing = ""
	m.projectFormCursor = 0
	m.projectFormErr = ""
	m.projectFormDest = p.Dest
	m.resetProjectBrowseState()
	return m, m.requestBrowseDirForDest(m.projectFormDest, p.RootDir, "", "")
}

// resetProjectBrowseState clears the root-dir browser SYNCHRONOUSLY, before
// the (async) browse request is even sent — mirrors enterSetupOrSplit's
// reset for exactly the same reason. requestBrowseDirForDest's send is a
// real IPC round trip (an SSH-hop TCP handshake plus auth on the first
// request to a remote daemon), and submitProjectForm reads m.cwdBrowseDir as
// the committed root dir. Without this, "open rename, fix the name, press
// Enter" — before the round trip lands — submits whatever cwdBrowseDir held
// from the PREVIOUS dialog session: another project's root, or the
// pane-setup dialog's last browsed CWD. The daemon's UpdateProject has no
// unchanged-value guard, so a rename that only touched the name would
// silently overwrite RootDir with that stale value.
func (m *Model) resetProjectBrowseState() {
	m.cwdBrowseDir = ""
	m.cwdBrowseEntries = nil
	m.cwdBrowseCursor = 0
	m.cwdBrowseScroll = 0
	m.cwdBrowseParent = ""
	m.cwdBrowseRoots = nil
	m.cwdBrowseTruncated = false
	m.cwdBrowseRootsTruncated = false
	m.cwdInputError = ""
	// Also drop any in-flight browse from a previous dialog session, so its
	// answer cannot land in THIS one. Redundant with requestBrowseDirForDest,
	// which overwrites m.browse again right after this call returns — kept
	// anyway so this helper is a complete, self-contained reset on its own,
	// matching enterSetupOrSplit's shape (its callers rely on the same
	// guarantee independent of what runs after it).
	m.browse = browseState{}
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
// them apart; the key handling itself is identical. Four focusable rows:
// Name (typed text), Host (an ssh destination, or empty for this machine),
// Root directory (the daemon-side browser), and the submit button.
func (m Model) handleProjectDialogKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch key {
	case "esc":
		m.dialog = dialogNone
		return m, tea.ClearScreen
	case "tab":
		n := len(m.projectFormVisibleRows())
		m.projectFormCursor = (m.projectFormCursor + 1) % n
		return m, nil
	case "shift+tab":
		n := len(m.projectFormVisibleRows())
		m.projectFormCursor = (m.projectFormCursor + n - 1) % n
		return m, nil
	}

	switch m.projectFormRowKind() {
	case projectRowName:
		return m.handleProjectNameKey(key)
	case projectRowRemote:
		return m.handleProjectRemoteKey(key)
	case projectRowUser:
		return m.handleProjectUserKey(key)
	case projectRowHost:
		return m.handleProjectHostKey(key)
	case projectRowRootDir:
		return m.handleProjectRootDirKey(key)
	case projectRowSubmit:
		// Positional, not by kind: the row above Submit is Root directory
		// whether or not the ssh fields are showing, and len-2 says that
		// without the arm having to know which rows are visible.
		switch key {
		case "up", "k":
			m.projectFormCursor = len(m.projectFormVisibleRows()) - 2
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

// The form's row kinds. They sit between Name and Root directory because the
// ORDER is the flow: a root directory lives on exactly one machine, so the
// host has to be settled — and connected — before the browser below it can ask
// anything meaningful.
const (
	projectRowName = iota
	projectRowRemote
	projectRowUser
	projectRowHost
	projectRowRootDir
	projectRowSubmit
)

// projectFormVisibleRows lists the row kinds currently on screen, in order.
// projectFormCursor indexes into THIS, not into the constants — the ssh
// fields only exist while the Remote toggle is on, and a local project is the
// common case that should not have to tab past two empty fields.
//
// One list drives focus, key dispatch and render together. Three separate
// notions of "which row is where" is how a form grows a case where the cursor
// highlights one field and typing lands in another.
func (m Model) projectFormVisibleRows() []int {
	rows := []int{projectRowName, projectRowRemote}
	if m.projectFormRemote {
		rows = append(rows, projectRowUser, projectRowHost)
	}
	return append(rows, projectRowRootDir, projectRowSubmit)
}

// projectFormRowKind resolves the focused row's kind, clamping a cursor left
// past the end by toggling Remote off.
func (m Model) projectFormRowKind() int {
	rows := m.projectFormVisibleRows()
	if m.projectFormCursor < 0 || m.projectFormCursor >= len(rows) {
		return projectRowName
	}
	return rows[m.projectFormCursor]
}

// projectFormDestFromFields composes the ssh destination the user described.
// Empty when the form is local, which is what makes "" the local daemon
// everywhere downstream.
//
// user@host is assembled here rather than asking the user to type it, but the
// result is still handed to ssh VERBATIM — so an ~/.ssh/config Host alias
// typed into Host alone keeps its own User/HostName/Port, and a user typed
// beside it overrides that alias's User exactly as `ssh user@alias` would.
func (m Model) projectFormDestFromFields() string {
	host := strings.TrimSpace(m.projectFormHost)
	if !m.projectFormRemote || host == "" {
		return ""
	}
	if user := strings.TrimSpace(m.projectFormUser); user != "" {
		return user + "@" + host
	}
	return host
}

// handleProjectHostKey edits the Host field. Empty means the local daemon;
// anything else is an ssh destination, passed to ssh verbatim exactly like
// [[destinations]] and --remote, so an ~/.ssh/config alias keeps its
// HostName/Port/User/ProxyJump.
//
// Enter CONNECTS rather than submitting the form. That is the one place this
// field departs from the Name row's convention, and it is deliberate: the
// root-dir browser underneath asks whichever daemon projectFormDest names, so
// submitting before the connection exists would create the project on the
// wrong machine with a path browsed from a third one.
func (m Model) handleProjectHostKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "enter":
		return m.connectProjectHost()
	case "backspace":
		if len(m.projectFormHost) > 0 {
			m.projectFormHost = m.projectFormHost[:len(m.projectFormHost)-1]
			m.projectFormErr = ""
		}
		return m, nil
	default:
		// No space case, unlike Name: an ssh destination cannot contain one,
		// and accepting it would only produce a host that fails to resolve.
		if len(key) == 1 && key != " " {
			m.projectFormHost += key
			m.projectFormErr = ""
		}
		return m, nil
	}
}

// connectProjectHost points the form at the typed host, dialling it first when
// it is not already connected. An empty host means local, which needs no dial.
// handleProjectRemoteKey toggles the Remote checkbox. Turning it off drops
// back to the local daemon immediately rather than leaving the form pointed at
// a host whose fields are no longer visible — a hidden field that still
// decides where the project lands is the failure this toggle would otherwise
// introduce.
func (m Model) handleProjectRemoteKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "enter", "space", " ":
		m.projectFormRemote = !m.projectFormRemote
		m.projectFormErr = ""
		if !m.projectFormRemote {
			m.projectFormHost = ""
			m.projectFormUser = ""
			m.projectFormDialing = ""
			m.projectFormDest = ""
			m.resetProjectBrowseState()
			return m, m.requestBrowseDirForDest("", "", "", "")
		}
		// Turning Remote ON blanks the listing and requests NOTHING. The
		// entries on screen describe this machine, and leaving them under a
		// form that now says "remote" invites picking a path that exists here
		// and nowhere else — the browser has no host to ask until one is
		// connected, and saying so is more honest than showing the wrong
		// filesystem. destDialedMsg repopulates it against the real host.
		m.projectFormDest = ""
		m.resetProjectBrowseState()
		m.projectFormCursor = 2 // the User row, which is where the flow goes next
		return m, nil
	}
	return m, nil
}

// handleProjectUserKey edits the ssh user. Optional: left empty, the Host
// field alone is handed to ssh, so an ~/.ssh/config alias keeps whatever User
// it declares.
func (m Model) handleProjectUserKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "enter":
		// Same as Host: Enter connects, because the browser below needs a
		// machine before it can answer anything.
		return m.connectProjectHost()
	case "backspace":
		if len(m.projectFormUser) > 0 {
			m.projectFormUser = m.projectFormUser[:len(m.projectFormUser)-1]
			m.projectFormErr = ""
		}
		return m, nil
	default:
		if len(key) == 1 && key != " " {
			m.projectFormUser += key
			m.projectFormErr = ""
		}
		return m, nil
	}
}

func (m Model) connectProjectHost() (tea.Model, tea.Cmd) {
	dest := m.projectFormDestFromFields()
	if m.projectFormRemote && dest == "" {
		m.projectFormErr = "host required for a remote project"
		return m, nil
	}
	if dest == "" || m.destConnected(dest) {
		m.projectFormDest = dest
		m.projectFormCursor = projectRowRootDir
		m.resetProjectBrowseState()
		// Re-browse against the newly chosen machine: the entries on screen
		// describe whichever daemon was asked last, and a path picked from one
		// host's filesystem is meaningless on another.
		return m, m.requestBrowseDirForDest(dest, "", "", "")
	}
	if m.dialDestFn == nil {
		m.projectFormErr = "this build cannot connect new hosts"
		return m, nil
	}
	m.projectFormDialing = dest
	m.projectFormErr = ""
	return m, m.dialDest(dest)
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
//
// Blocked while m.browse.pending: the root-dir round trip may still be in
// flight (resetProjectBrowseState's zeroing happens synchronously, the
// daemon's answer does not), and submitting mid-flight would commit
// whatever cwdBrowseDir currently holds — "" fresh after a reset — as the
// root dir. For New that is merely a premature default; for Rename it would
// blank an existing project's RootDir. Clears itself: pending always
// resolves to false, either from a response or requestBrowseDirForDest's own
// timeout, so this is a wait, not a dead end.
func (m Model) submitProjectForm() (tea.Model, tea.Cmd) {
	if strings.TrimSpace(m.projectFormName) == "" {
		m.projectFormErr = "name required"
		m.projectFormCursor = 0
		return m, nil
	}
	if m.browse.pending {
		m.projectFormErr = "waiting for the root directory to load…"
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
	nameFocused := m.projectFormRowKind() == projectRowName
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

	// Remote toggle, and the ssh fields it reveals. Rendered from the same
	// visible-row list the cursor and key dispatch use, so what is painted and
	// what is focusable cannot disagree.
	kind := m.projectFormRowKind()
	remoteFocused := kind == projectRowRemote
	box := "[ ]"
	if m.projectFormRemote {
		box = "[x]"
	}
	if remoteFocused {
		b.WriteString(dialogSelected.Render("> "+box+" Remote (ssh)") + "\n")
	} else {
		b.WriteString(dialogNormal.Render("  "+box+" Remote (ssh)") + "\n")
	}
	if !m.projectFormRemote {
		b.WriteString("    " + dialogSubtle.Render("(this machine — Space to connect elsewhere)") + "\n")
	}
	b.WriteString("\n")

	if m.projectFormRemote {
		userFocused := kind == projectRowUser
		if userFocused {
			b.WriteString(dialogSelected.Render("> User:") + "\n")
		} else {
			b.WriteString(dialogNormal.Render("  User:") + "\n")
		}
		switch {
		case m.projectFormUser == "":
			b.WriteString("    " + dialogSubtle.Render("(optional — ssh config decides)") + "\n")
		case userFocused:
			b.WriteString("    " + dialogValStyle.Render(truncateToWidth(m.projectFormUser, textWidth-setupRowIndent)) + "\n")
		default:
			b.WriteString(setupRowIdleMark + dialogSelectedIdle.Render(truncateToWidth(m.projectFormUser, textWidth-setupRowIndent)) + "\n")
		}
		b.WriteString("\n")

		// Host. Enter connects rather than submitting — the browser below asks
		// whichever machine this names, so it has to be settled first.
		hostFocused := kind == projectRowHost
		if hostFocused {
			b.WriteString(dialogSelected.Render("> Host:") + "\n")
		} else {
			b.WriteString(dialogNormal.Render("  Host:") + "\n")
		}
		switch {
		case m.projectFormDialing != "":
			b.WriteString("    " + dialogSubtle.Render("connecting to "+
				truncateToWidth(sanitizeRemoteText(m.projectFormDialing), textWidth-setupRowIndent-14)+"…") + "\n")
		case m.projectFormHost == "":
			b.WriteString("    " + dialogSubtle.Render("(required — Enter to connect)") + "\n")
		case hostFocused:
			// The connection state is shown on the field itself: this is the
			// row whose Enter performs the test, so the answer belongs where
			// the user is looking when they press it.
			state := dialogSubtle.Render("  ✗ not connected — Enter to try")
			if m.destConnected(m.projectFormDestFromFields()) {
				state = dialogSubtle.Render("  ✓ connected")
			}
			b.WriteString("    " + dialogValStyle.Render(truncateToWidth(m.projectFormHost, textWidth-setupRowIndent-30)) + state + "\n")
		default:
			// A host that is typed but NOT connected keeps the plain indent, so
			// a user who tabbed past it is not left believing the project will
			// land there.
			mark := setupRowIdleMark
			if !m.destConnected(m.projectFormDestFromFields()) {
				mark = "    "
			}
			b.WriteString(mark + dialogSelectedIdle.Render(truncateToWidth(m.projectFormHost, textWidth-setupRowIndent)) + "\n")
		}
		b.WriteString("\n")
	}

	// Root directory field — the daemon-side browser.
	rootFocused := m.projectFormRowKind() == projectRowRootDir
	if rootFocused {
		b.WriteString(dialogSelected.Render("> Root directory:") + "\n")
	} else {
		b.WriteString(dialogNormal.Render("  Root directory:") + "\n")
	}

	path, prefix := sanitizeRemoteText(m.cwdBrowseDir), "    "
	switch {
	case path == "" && len(m.cwdBrowseEntries) > 0:
		path = dialogSubtle.Render("Select drive:")
	case path == "" && m.projectFormRemote && m.projectFormDest == "":
		// Says WHY it is empty. The generic message below promises the daemon
		// default, which for a remote form that has not connected yet would
		// mean the local daemon's — the wrong machine entirely.
		path = dialogSubtle.Render("(connect a host above to browse it)")
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
	submitFocused := m.projectFormRowKind() == projectRowSubmit
	label := "[ " + submitLabel + " ]"
	if submitFocused {
		b.WriteString(dialogSelected.Render("> "+label) + "\n\n")
	} else {
		b.WriteString(dialogNormal.Render("  "+label) + "\n\n")
	}

	b.WriteString(dialogSubtle.Render("Tab next field    Enter select    Esc cancel"))

	return b.String()
}

// splitSSHDest is the inverse of projectFormDestFromFields, for pre-filling
// Rename from a project's stored dest. Splits on the LAST "@" so an ssh
// destination whose user part contains one (a Kerberos principal, an AAD
// login) round-trips instead of being cut in the middle.
func splitSSHDest(dest string) (user, host string) {
	if i := strings.LastIndex(dest, "@"); i >= 0 {
		return dest[:i], dest[i+1:]
	}
	return "", dest
}
