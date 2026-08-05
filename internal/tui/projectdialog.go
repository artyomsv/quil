package tui

import (
	"fmt"
	"log"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// The form's one message line carries three different kinds of news, and
// rendering all of them as a red ✗ said the wrong thing about two of them:
// provisioning a host is progress, and a connected host is good news, but both
// arrived looking like the failure the same line reports when ssh cannot reach
// the machine at all. Reported as "it was installing fine but the message was
// red, which seemed strange".
//
// The colours are the ones this package already assigns these meanings
// (styles.go / sidebar.go): 208 for work under way — the accent restore and MCP
// activity use — and 28 for done, the green a finished pane carries. Amber 214
// is deliberately NOT reused: here it means blocked-on-user, and nothing during
// an install is waiting on the user.
type projectFormMsgKind int

const (
	// The zero value is the error kind on purpose: a message whose severity was
	// never stated is a validation failure, which is what every existing caller
	// of this line reports.
	projectFormMsgError projectFormMsgKind = iota
	projectFormMsgBusy
	projectFormMsgOK
	// projectFormMsgWarn is a consequence the next Enter will carry out. It
	// shares busy's colour because neither is a failure, and differs by glyph
	// because they differ in who is waiting: busy means the machine is working,
	// warn means the user is being asked.
	projectFormMsgWarn
)

func projectFormMsgStyle(kind projectFormMsgKind) (string, lipgloss.Style) {
	switch kind {
	case projectFormMsgBusy:
		return "⟳", lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	case projectFormMsgOK:
		return "✓", lipgloss.NewStyle().Foreground(lipgloss.Color("28"))
	case projectFormMsgWarn:
		return "⚠", lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	default:
		return "✗", dialogErrorStyle
	}
}

// formMsgNameCap bounds a remote-chosen project name interpolated into the
// message line. Wide enough to identify the project the user must rename,
// short enough that the line cannot wrap the box whatever the daemon sends.
const formMsgNameCap = 32

// formMsgDetailCap bounds remote-supplied DIAGNOSTIC text on the same line —
// ssh's own words, which the transport already caps at 2000 bytes. That is
// small enough not to be an attack and far too large for this row: at the
// dialog's width it wraps to some forty lines and pushes the box past the
// terminal. Three lines' worth keeps the reason readable; the full text is in
// quil.log either way.
const formMsgDetailCap = 160

// setFormError / setFormBusy / setFormOK are the ONLY ways to put text on that
// line. Assigning projectFormErr directly is what leaves the previous message's
// colour behind — a failure rendered in the green of the success before it.
func (m *Model) setFormError(s string) { m.projectFormErr, m.projectFormMsgKind = s, projectFormMsgError }
func (m *Model) setFormBusy(s string)  { m.projectFormErr, m.projectFormMsgKind = s, projectFormMsgBusy }
func (m *Model) setFormOK(s string)    { m.projectFormErr, m.projectFormMsgKind = s, projectFormMsgOK }
func (m *Model) setFormWarn(s string)  { m.projectFormErr, m.projectFormMsgKind = s, projectFormMsgWarn }

// projectMergePlan is the fold a SECOND Enter carries out. Naming a project on
// a host that already holds one used to dead-end in "rename it instead" — a
// remedy the dialog offered no route to, and one that does not even work on a
// host holding three, where renaming one still leaves three.
//
// The plan is recomputed and COMPARED on every submit rather than cleared by
// each editing path. The message quotes a name and a tab count, so the property
// that matters is that the user confirms the sentence they are looking at; an
// arm-then-invalidate scheme has to be right in every edit handler, while
// recompute-and-compare is right by construction and re-arms with the new text
// instead of silently executing the old plan.
type projectMergePlan struct {
	dest     string
	into     string   // survivor: keeps its ID, its tabs and its position
	survivor string   // survivor's CURRENT name, quoted in the message
	absorb   []string // projects whose tabs move into the survivor
	name     string
	rootDir  string
	tabs     int // tabs that MOVE — the survivor's own do not
}

func (p *projectMergePlan) sameAs(q *projectMergePlan) bool {
	if p == nil || q == nil {
		return false
	}
	if p.dest != q.dest || p.into != q.into || p.survivor != q.survivor ||
		p.name != q.name || p.rootDir != q.rootDir || p.tabs != q.tabs ||
		len(p.absorb) != len(q.absorb) {
		return false
	}
	for i := range p.absorb {
		if p.absorb[i] != q.absorb[i] {
			return false
		}
	}
	return true
}

// planProjectMerge describes folding every project on a host into one of them.
//
// The survivor is the first project NOBODY invented — the first non-Bootstrap —
// falling back to the first of all. Which one survives decides two things, and
// neither is the name (that is overwritten either way): the root directory
// inherited when the browse has not answered, and the order tabs end up in. A
// bootstrap project's root is whatever CWD the daemon happened to start in,
// while a named project's is one the user chose, so preferring the named one
// keeps the answer that means something. Among equals the first wins, which
// keeps the host's oldest tabs at the front of the tab bar rather than behind
// whichever duplicate was created last.
//
// onHost must be non-empty; the caller has already branched on its length.
func (m *Model) planProjectMerge(dest, name, rootDir string, onHost []*ProjectModel) *projectMergePlan {
	survivor := onHost[0]
	for _, p := range onHost {
		if !p.Bootstrap {
			survivor = p
			break
		}
	}
	plan := &projectMergePlan{
		dest:     dest,
		into:     survivor.ID,
		survivor: survivor.Name,
		name:     name,
		rootDir:  rootDir,
		tabs:     0,
	}
	// An empty root keeps the survivor's OWN, for the reason the adopt path
	// substitutes one: this is a rename on the far side, MergeProjects has no
	// unchanged-value guard any more than UpdateProject does, and submitting
	// before the browse lands would ERASE the root the project already had.
	if plan.rootDir == "" {
		plan.rootDir = survivor.RootDir
	}
	for _, p := range onHost {
		if p.ID == survivor.ID {
			continue
		}
		plan.absorb = append(plan.absorb, p.ID)
		plan.tabs += len(p.tabs)
	}
	return plan
}

// message is what the user reads before pressing Enter a second time, and it
// states the consequence rather than the rule: what will be called what, how
// many tabs move, and — because this is the question a fold actually raises —
// that nothing is closed.
//
// Every interpolated name is bounded as well as sanitised. They are chosen by a
// remote daemon, this is the one value-bearing row with no truncation of its
// own, and lipgloss WRAPS at the box width.
func (p *projectMergePlan) message() string {
	host := sanitizeRemoteText(hostLabel(p.dest))
	want := truncateToWidth(sanitizeRemoteText(p.name), formMsgNameCap)
	if len(p.absorb) == 0 {
		return fmt.Sprintf("%s already has %s — Enter renames it to %s, keeping its tabs",
			host, truncateToWidth(sanitizeRemoteText(p.survivor), formMsgNameCap), want)
	}
	return fmt.Sprintf("%s already has %d projects — Enter folds them into one named %s: %d tabs move, nothing closes",
		host, len(p.absorb)+1, want, p.tabs)
}

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
	// A daemon with no project support accepts this message and does nothing
	// with it, so the dialog would close on a project that never appears —
	// which is how it was reported: "the name I gave was not respected", the
	// only project on the host staying the client's own placeholder. Refuse
	// where the user can see it.
	if !m.destSupportsProjects(m.projectFormDest) {
		m.setFormError("that host runs a quil without project support")
		return nil
	}
	// A remote host holds exactly ONE project, and naming it is how you get it.
	//
	// The daemon does not know it is remote — Project has no Dest field, that is
	// this client's label for the connection a project arrived on — so this is a
	// rule about what CREATE does here, not an invariant the daemon can keep.
	//
	// Three outcomes, decided by how many projects the host presents and — for
	// the single one — the daemon's Bootstrap flag rather than the name.
	if m.projectFormDest != "" {
		onHost := m.projectsOnDest(m.projectFormDest)
		switch {
		case len(onHost) == 1 && onHost[0].Bootstrap:
			adoptable := onHost[0]
			// Rename in place. The host's existing tabs are already inside it,
			// so they end up under the name the user chose instead of beside it
			// as a "Default" they never asked for — which is the whole point,
			// and is why this is an update rather than a create plus a move.
			//
			// An empty root keeps the project's OWN, rather than being passed
			// through. submitProjectForm drops the browse-pending gate on the
			// stated grounds that an empty root is "a real answer for a CREATE
			// and unreachable for a RENAME" — routing a create into a rename is
			// exactly what makes it reachable, and UpdateProject has no
			// unchanged-value guard, so the adopted project's root would be
			// ERASED by an Enter pressed before the browse lands.
			if rootDir == "" {
				rootDir = adoptable.RootDir
			}
			// Conditional on the far side: this client decided to adopt from
			// its own snapshot, and a second client driving the same host can
			// name that project in between. The daemon refuses the update if it
			// is no longer a bootstrap, so the loser reports rather than
			// silently renaming the winner's project.
			return m.sendUpdateProject(adoptable.ID, name, rootDir, true)
		case len(onHost) > 0:
			// Everything else the host can present — one project somebody
			// named, or the several a pre-rule client left behind — resolves
			// the SAME way: fold them into one carrying the name just typed.
			//
			// Refusing here was the shipped behaviour, and it was a dead end
			// twice over. The remedy it named ("rename it instead") had no
			// route from this dialog, and on a host holding three projects
			// renaming one still leaves three — so the message described work
			// that could not fix the state it was complaining about.
			//
			// Confirmed rather than done, because it reassigns tabs and drops
			// project records on a machine the user is not looking at. The
			// first Enter arms and describes; the second carries out exactly
			// what was described, or re-arms if the description has moved on.
			plan := m.planProjectMerge(m.projectFormDest, name, rootDir, onHost)
			if !plan.sameAs(m.projectFormMerge) {
				m.projectFormMerge = plan
				m.setFormWarn(plan.message())
				return nil
			}
			return m.sendMergeProjects(plan)

		case m.attached[m.projectFormDest]:
			// Attached and still silent: this host WILL report a project — a
			// daemon must hold a tab and a tab must hold a project — so the
			// empty answer is "not yet", not "none". Creating here is how the
			// duplicate comes back: the daemon already holds a bootstrap
			// Default this client cannot see, so the create lands beside it,
			// and the NEXT create adopts that Default and renames it. The wait
			// is self-clearing, the wrong answer is not.
			//
			// Reachable without a race: destDialedMsg batches the attach with
			// the root-dir browse, so the listing can paint — and invite an
			// Enter — before the daemon's first workspace_state arrives.
			m.setFormBusy("waiting for " + sanitizeRemoteText(hostLabel(m.projectFormDest)) +
				" to report its projects…")
			return nil
		}
	}
	// Local daemons keep many projects, so the only rule there is that two of
	// them must not be indistinguishable. The sidebar shows name and host and
	// nothing else, so a second row with the same name on the same daemon
	// leaves the user unable to tell which holds their tabs — and removing the
	// wrong one takes them with it.
	if existing := m.projectNamedOnDest(name, m.projectFormDest, ""); existing != nil {
		m.setFormError(sanitizeRemoteText(name) + " already exists on that host")
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
	// Logged on SUCCESS too, not just failure. A create that silently does not
	// arrive is indistinguishable from one that was never sent, and the only
	// way to tell them apart afterwards is a record of what left this side and
	// which daemon it was aimed at.
	if sendErr := m.sendForDest(m.projectFormDest, msg); sendErr != nil {
		log.Printf("create project %q on dest %q: send: %v", name, m.projectFormDest, sendErr)
	} else {
		log.Printf("create project %q on dest %q: sent", name, m.projectFormDest)
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
	return m.sendUpdateProject(id, name, rootDir, false)
}

// sendMergeProjects carries out a confirmed plan and closes the dialog.
//
// Deliberately NOT routed through sendUpdateProject's duplicate-name guard,
// even though the fold ends in a rename: every project that guard would find on
// this host is one this message is about to ABSORB, so it would refuse exactly
// the case it exists to prevent — naming a host after the duplicate being
// folded away is the ordinary way out of this state. The daemon still
// disambiguates against whatever survives, after the deletions.
//
// Two clients folding one host concurrently converge rather than corrupt: the
// loser's absorb IDs no longer resolve and are skipped, leaving its message a
// plain rename of the survivor. Last name wins, exactly as two concurrent
// renames already do.
func (m *Model) sendMergeProjects(plan *projectMergePlan) tea.Cmd {
	msg, err := ipc.NewMessage(ipc.MsgMergeProjects, ipc.MergeProjectsPayload{
		ProjectID: plan.into,
		Absorb:    plan.absorb,
		Name:      plan.name,
		RootDir:   strings.TrimSpace(plan.rootDir),
	})
	if err != nil {
		log.Printf("merge projects into %s: encode: %v", plan.into, err)
		return nil
	}
	// Logged on success too, like the create beside it: a fold that silently
	// does not arrive is indistinguishable from one never sent, and this is the
	// only record afterwards of what left and which daemon it was aimed at.
	if sendErr := m.sendForDest(plan.dest, msg); sendErr != nil {
		log.Printf("merge %d projects into %s on dest %q: send: %v",
			len(plan.absorb), plan.into, plan.dest, sendErr)
	} else {
		log.Printf("merge %d projects into %s on dest %q: sent",
			len(plan.absorb), plan.into, plan.dest)
	}
	m.projectFormMerge = nil
	m.dialog = dialogNone
	return nil
}

// sendUpdateProject is the one sender for MsgUpdateProject. adoptBootstrap
// makes the daemon apply it only while the project is still one it invented —
// see UpdateProjectPayload.AdoptBootstrap. A plain rename passes false.
func (m *Model) sendUpdateProject(id, name, rootDir string, adoptBootstrap bool) tea.Cmd {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	// A rename can recreate the pair a create is refused for. "It is deliberate,
	// so the user knows which is which" does not survive the rename: afterwards
	// the two rows are as indistinguishable as any other duplicate, and removing
	// the wrong one still takes its tabs. Excluding the project itself keeps a
	// rename that only changes the root directory working.
	dest := m.destOfProject(id)
	if existing := m.projectNamedOnDest(name, dest, id); existing != nil {
		m.setFormError(sanitizeRemoteText(name) + " already exists on " +
			sanitizeRemoteText(hostLabel(dest)))
		return nil
	}
	msg, err := ipc.NewMessage(ipc.MsgUpdateProject, ipc.UpdateProjectPayload{
		ProjectID:      id,
		Name:           name,
		RootDir:        strings.TrimSpace(rootDir),
		AdoptBootstrap: adoptBootstrap,
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
	m.projectFormDialing = ""
	m.projectFormCursor = 0
	m.projectFormErr = ""
	// Load-bearing, not tidiness: the fold's second Enter fires when the
	// recomputed plan MATCHES the armed one, and reopening this form against the
	// same host and typing the same name reproduces an identical plan. A plan
	// surviving the close would then execute on the first Enter of the new
	// session, having never shown the user the sentence it was confirming.
	m.projectFormMerge = nil
	m.projectFormDest = m.activeDest()
	// The ssh fields describe the dest, rather than starting blank beside it.
	// They used to say "this machine" while projectFormDest already named the
	// active project's REMOTE host, so the form contradicted itself — harmless
	// while every submit was a create on a host the user had at least typed,
	// and not harmless once naming a project there can RENAME the host's
	// existing one. Same seeding beginProjectRename does, for the same reason.
	m.projectFormRemote = m.projectFormDest != ""
	m.projectFormUser, m.projectFormHost = splitSSHDest(m.projectFormDest)
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
	// See openNewProjectDialog: an armed plan must not outlive the form that
	// described it.
	m.projectFormMerge = nil
	m.projectFormDest = p.Dest
	m.resetProjectBrowseState()
	// Seed the root-dir field with the project's OWN value, so a submit before
	// the browse answers sends what the project already has instead of nothing.
	//
	// This is what the pending-gate below used to buy, and it buys it without
	// blocking: the hazard was submitting a root dir left over from a DIFFERENT
	// dialog session, which the reset above already removes — and against a
	// remote daemon the browse can take seconds, so the gate made renaming a
	// remote project impossible in exactly the window the user is looking at
	// the form (reported 2026-08-03: "changed the name, pressed save, name
	// stayed Default").
	m.cwdBrowseDir = p.RootDir
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
		m.setFormError("host required for a remote project")
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
		m.setFormError("this build cannot connect new hosts")
		return m, nil
	}
	m.projectFormDialing = dest
	// Amber from the moment the dial starts. Whether the host answers is not
	// known yet, so this says "working", and the arms in Update replace it with
	// the green success or the red failure once it is.
	m.setFormBusy("connecting to " + sanitizeRemoteText(dest) + "…")
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
		m.setFormError("name required")
		m.projectFormCursor = 0
		return m, nil
	}
	// The gate exists to stop a STALE root dir being submitted — cwdBrowseDir
	// is scratch state shared with the pane-setup dialog and every other
	// project's rename, so an in-flight browse means the value on screen may
	// still describe the PREVIOUS dialog session.
	//
	// No wait on the browse. The hazard it guarded — submitting a root dir left
	// over from a DIFFERENT dialog session — is gone at the source:
	// resetProjectBrowseState clears the scratch value when either dialog
	// opens, and beginProjectRename then seeds the field with the project's
	// OWN root. So whatever cwdBrowseDir holds here is always one of three
	// safe things: this project's existing value, a directory the user picked
	// in this session, or empty.
	//
	// Empty is a real answer for a CREATE (the daemon falls back to its own
	// default CWD, as it does for a pane) and unreachable for a RENAME, which
	// is what makes dropping the gate safe on both paths.
	//
	// The adopt path routes a CREATE into submitRenameProject, which would make
	// empty reachable on a rename for the first time — so it substitutes the
	// adopted project's own root rather than passing this value through.
	// UpdateProject has no unchanged-value guard, so without that the project's
	// root would be erased by an Enter pressed before the browse lands. Waiting instead made
	// remote projects impossible to create and then to rename: the browse
	// fires at a daemon connected seconds ago, and until it answered the
	// button did nothing the user could see.
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
	// Sanitized at RENDER, never where beginProjectRename seeds the field: this
	// value round-trips back to the daemon on submit, so stripping it in state
	// would silently rewrite a name the user never edited. Render-only is the
	// same rule the remote browse dialogs follow, and for the same reason.
	switch {
	case m.projectFormName == "":
		b.WriteString("    " + dialogSubtle.Render("(required)") + "\n")
	case nameFocused:
		b.WriteString("    " + dialogValStyle.Render(truncateToWidth(sanitizeRemoteText(m.projectFormName), textWidth-setupRowIndent)) + "\n")
	default:
		b.WriteString(setupRowIdleMark + dialogSelectedIdle.Render(truncateToWidth(sanitizeRemoteText(m.projectFormName), textWidth-setupRowIndent)) + "\n")
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
		glyph, style := projectFormMsgStyle(m.projectFormMsgKind)
		// Sanitised HERE as well as at every set site, which is not redundant
		// but the rule the package states: sanitise at RENDER, because this is
		// the one place guaranteed to run for every message, while a set site
		// is one of eight and the ninth is easy to add and easy to forget. It
		// costs nothing when there is nothing to strip — sanitizeRemoteText
		// returns the input unchanged without allocating.
		b.WriteString("  " + style.Render(glyph+" "+sanitizeRemoteText(m.projectFormErr)) + "\n\n")
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

// confirmKindDisconnectHost is the discriminator for the "disconnect host"
// confirm. It confirms — even though nothing on the far side is destroyed —
// because it removes the host from the config as well as the session, so the
// undo is retyping an ssh destination rather than pressing a key.
const confirmKindDisconnectHost = "disconnect-host"

// confirmDisconnectHost opens the confirm for a remote project's host. Keyed
// by the DEST, not the project: disconnecting takes every project on that
// machine, so the one that happened to be right-clicked is not the target.
func (m *Model) confirmDisconnectHost(projectID string) tea.Cmd {
	p := m.projectByID(projectID)
	if p == nil || p.Dest == "" {
		return nil
	}
	m.dialog = dialogConfirm
	m.confirmKind = confirmKindDisconnectHost
	m.confirmID = p.Dest
	m.confirmName = p.Dest
	return nil
}
