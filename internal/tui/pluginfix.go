package tui

import (
	"log"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// pluginDialog is one plugin dialog for one pane: an error handler's buttons,
// the tool's update notice, or both merged into one box. It is HELD in
// Model.pluginDialogs (keyed by pane id) from the moment the frame arrives
// until the pane is on screen and nothing else owns the keyboard — the dialog
// opens on its own, from pane output, and it must neither replace what the
// user is in the middle of (the openApplyConfirm rule) nor describe a pane
// they cannot see. Model.pluginDialogOpen names the one on screen.
type pluginDialog struct {
	paneID, dest, plugin string
	// title/message belong to the ERROR handler's frame and hasError says one
	// arrived. The update notice keeps its own pair, so the error reads first
	// whichever of the two frames the daemon sent first (it sends the update
	// one from a goroutine).
	title, message             string
	hasError                   bool
	updateTitle, updateMessage string
	fixes                      []ipc.PluginFixInfo
	update                     *ipc.PluginUpdateInfo
	// unplaced says the pane was not in this Model when the frame arrived
	// (the frame overtook the broadcast that creates it). Such a dialog is
	// eligible to open at once; the next broadcast of its dest either places
	// the pane or drops the dialog.
	unplaced bool
	// focus is the button the next Enter activates; confirming is the
	// "Replace <path>? y/n" (or "Update with <manager>? y/n") step after
	// Update; openedAt starts the letter grace (pluginLetterGrace); notice is
	// a send failure shown in the box.
	focus      int
	confirming bool
	openedAt   time.Time
	notice     string
}

// Bounds at ingest, like boundBranch for PreparingWorktree: every string here
// is re-sanitized on every frame, and under --remote it came off a host the
// user may not control.
const (
	maxPluginDialogFixes  = 8
	pluginDialogFieldCap  = 1024
	pluginDialogPathWidth = 40
	// pluginDialogInnerWidth is what the dialog box shows per row (border +
	// padding on each side); pluginDialogIndent is the two cells every body
	// line starts with, pluginDialogLineWidth what is left for its text, and
	// pluginButtonWidth clamps a button label so a row always fits one.
	pluginDialogInnerWidth = dialogWidth - 6
	pluginDialogIndent     = "  "
	pluginDialogLineWidth  = pluginDialogInnerWidth - len(pluginDialogIndent)
	pluginButtonWidth      = 24
	// fixLabelCap bounds PaneInfo.FixLabel, the pane border's "running fix:"
	// label, which is sanitized on every frame while the fix runs.
	fixLabelCap = 128
)

// pluginLetterGrace is how long after the dialog opens its letter keys stay
// inert. The dialog opens from pane OUTPUT, under a user who may be typing
// into that pane: the "l" of "ls" or the "n" of "npm" must not become Log in
// or Not now. Arrows, Tab, Enter on the (safe) focused button and Esc are live
// at once.
const pluginLetterGrace = 750 * time.Millisecond

// pluginButtonKind is what activating a button does.
type pluginButtonKind int

const (
	pluginButtonFix pluginButtonKind = iota
	pluginButtonUpdate
	pluginButtonNotNow
	pluginButtonClose
)

// pluginButton is one button on the dialog, in display order.
type pluginButton struct {
	kind  pluginButtonKind
	fix   ipc.PluginFixInfo // kind == pluginButtonFix
	label string            // sanitized and clamped
	key   rune              // the letter that activates it, 0 for none
}

// The three fixed buttons own their letters ahead of every fix, whether or not
// they are on this dialog: a fix's letter must not change when an update
// notice merges in and brings Not now with it.
const (
	pluginKeyUpdate = 'u'
	pluginKeyNotNow = 'n'
	pluginKeyClose  = 'c'
)

// buttons is the dialog's row in order: fixes…, Update, Not now, Close. Not
// now is present for an update notice; Close always is, last, so the default
// focus is always the last button and a viewer whose every other button is
// greyed still has a visible way out (Esc alone is not discoverable).
func (d *pluginDialog) buttons() []pluginButton {
	taken := map[rune]bool{pluginKeyUpdate: true, pluginKeyNotNow: true, pluginKeyClose: true}
	var out []pluginButton
	for _, f := range d.fixes {
		label := truncateToWidth(sanitizeRemoteText(f.Label), pluginButtonWidth)
		out = append(out, pluginButton{kind: pluginButtonFix, fix: f, label: label, key: pluginFixLetter(label, taken)})
	}
	if d.update != nil {
		out = append(out, pluginButton{kind: pluginButtonUpdate, label: "Update", key: pluginKeyUpdate})
		out = append(out, pluginButton{kind: pluginButtonNotNow, label: "Not now", key: pluginKeyNotNow})
	}
	out = append(out, pluginButton{kind: pluginButtonClose, label: "Close", key: pluginKeyClose})
	return out
}

// pluginFixLetter picks the first ASCII letter of label no other button has
// taken, case-insensitively, and takes it. 0 when every letter is taken or
// the label has none; the button is then reachable by arrows only.
func pluginFixLetter(label string, taken map[rune]bool) rune {
	for _, r := range label {
		l := asciiLower(r)
		if l == 0 || taken[l] {
			continue
		}
		taken[l] = true
		return l
	}
	return 0
}

// asciiLower folds an ASCII letter to lower case and answers 0 for anything
// else — the key match is on what the terminal reports for a plain keypress,
// which is a single ASCII letter.
func asciiLower(r rune) rune {
	switch {
	case r >= 'a' && r <= 'z':
		return r
	case r >= 'A' && r <= 'Z':
		return r + ('a' - 'A')
	}
	return 0
}

// bracketLetter shows the button's letter in its label: "[L]og in". The first
// occurrence of the letter is bracketed, which is the one pluginFixLetter
// chose, since it scans in the same order.
func bracketLetter(label string, key rune) string {
	if key == 0 {
		return label
	}
	for i, r := range label {
		if asciiLower(r) == key {
			return label[:i] + "[" + string(r) + "]" + label[i+len(string(r)):]
		}
	}
	return label
}

// The reasons shown beside greyed buttons. Update is admin-class (it replaces
// an executable on the daemon host), so a standard token keeps its fixes and
// Not now and loses only that one; a fix and Not now are act-class, so a
// read-only token keeps only Close. An overlay pane (lazygit's slot) keeps
// only Close too: the daemon refuses every plugin_fix_req and
// plugin_update_req for one — an overlay is destroyed the moment its child
// exits, so a fix there has no pane to come back to — and a button that is
// offered and then refused is worse than one that says why it is greyed.
const (
	updateDisabledReason  = "needs a full-access token"
	actDisabledReason     = "read-only connection"
	overlayDisabledReason = "not available in an overlay"
)

// pluginUpdateEnabled reports whether this client may send plugin_update_req
// to the dialog's daemon; pluginActEnabled the same for plugin_fix_req.
func (m Model) pluginUpdateEnabled(d *pluginDialog) bool {
	return d.update != nil && m.destCanAdmin(d.dest)
}

func (m Model) pluginActEnabled(d *pluginDialog) bool { return !m.destReadOnly(d.dest) }

// pluginButtonDisabledReason is the one place a button's gates are read, for
// the key handler and the renderer alike: "" when the button is live, else
// the reason the renderer prints beside it. The overlay gate comes first —
// it holds whatever the token says.
func (m Model) pluginButtonDisabledReason(d *pluginDialog, b pluginButton) string {
	switch b.kind {
	case pluginButtonClose:
		return ""
	}
	if m.paneIsOverlay(d.paneID) {
		return overlayDisabledReason
	}
	switch b.kind {
	case pluginButtonUpdate:
		if !m.pluginUpdateEnabled(d) {
			return updateDisabledReason
		}
	case pluginButtonFix, pluginButtonNotNow:
		if !m.pluginActEnabled(d) {
			return actDisabledReason
		}
	}
	return ""
}

// pluginButtonEnabled reports whether activating b does anything.
func (m Model) pluginButtonEnabled(d *pluginDialog, b pluginButton) bool {
	return m.pluginButtonDisabledReason(d, b) == ""
}

// paneIsOverlay reports whether paneID is a tab's overlay pane rather than a
// leaf of its layout tree, in any project.
func (m *Model) paneIsOverlay(paneID string) bool {
	for _, p := range m.projects {
		if p == nil {
			continue
		}
		for _, t := range p.tabs {
			if t != nil && t.overlayPane != nil && t.overlayPane.ID == paneID {
				return true
			}
		}
	}
	return false
}

// --- arrival, hold and merge ---

// notePluginDialog files an arriving plugin_error frame under its pane,
// merging into a dialog already held or open for that pane: fixes not yet
// present are appended, an update notice is set, and the first error's title
// and message are kept. It never opens anything — openHeldPluginDialog does,
// from Update's choke point, once the pane is on screen and nothing else owns
// the keyboard. A frame for a pane this Model cannot place on a destination
// it does not know is dropped: nothing will ever place it.
func (m *Model) notePluginDialog(msg PluginErrorMsg) {
	if m.pluginDialogs == nil {
		m.pluginDialogs = make(map[string]*pluginDialog)
	}
	d := m.pluginDialogs[msg.PaneID]
	if d == nil {
		unplaced := m.findPane(msg.PaneID) == nil
		if unplaced && !m.destKnown(msg.Dest) {
			log.Printf("plugin dialog for unknown pane %q on unknown dest %q dropped", msg.PaneID, msg.Dest)
			return
		}
		d = &pluginDialog{paneID: msg.PaneID, dest: msg.Dest, unplaced: unplaced}
		m.pluginDialogs[msg.PaneID] = d
	}
	if msg.Plugin != "" {
		d.plugin = truncateBytes(msg.Plugin, pluginDialogFieldCap)
	}
	if msg.Update != nil {
		u := *msg.Update
		u.Installed = truncateBytes(u.Installed, pluginDialogFieldCap)
		u.Offered = truncateBytes(u.Offered, pluginDialogFieldCap)
		u.Target = truncateBytes(u.Target, pluginDialogFieldCap)
		u.Manager = truncateBytes(u.Manager, pluginDialogFieldCap)
		d.update = &u
		d.updateTitle = truncateBytes(msg.Title, pluginDialogFieldCap)
		d.updateMessage = truncateBytes(msg.Message, pluginDialogFieldCap)
	} else {
		if !d.hasError {
			d.hasError = true
			d.title = truncateBytes(msg.Title, pluginDialogFieldCap)
			d.message = truncateBytes(msg.Message, pluginDialogFieldCap)
		}
		for _, f := range msg.Fixes {
			if len(d.fixes) >= maxPluginDialogFixes || f.Name == "" || d.hasFix(f.Name) {
				continue
			}
			f.Name = truncateBytes(f.Name, pluginDialogFieldCap)
			f.Label = truncateBytes(f.Label, pluginDialogFieldCap)
			d.fixes = append(d.fixes, f)
		}
	}
	// A merge moves the buttons under the user's focus, so it goes back to the
	// safe one — Close or Not now — and leaves any y/n step. The letter grace
	// restarts too: new buttons arrived under typing fingers.
	d.focus = len(d.buttons()) - 1
	d.confirming = false
	d.notice = ""
	if m.pluginDialogOpen == msg.PaneID {
		d.openedAt = m.clock()
	}
}

func (d *pluginDialog) hasFix(name string) bool {
	for _, f := range d.fixes {
		if f.Name == name {
			return true
		}
	}
	return false
}

// findPane answers the pane wherever it sits — a layout tree or an overlay
// slot, in any project — or nil.
func (m *Model) findPane(paneID string) *PaneModel {
	for _, p := range m.projects {
		if p == nil {
			continue
		}
		for _, t := range p.tabs {
			if t == nil {
				continue
			}
			if leaf := t.Root.FindLeaf(paneID); leaf != nil {
				return leaf.Pane
			}
			if t.overlayPane != nil && t.overlayPane.ID == paneID {
				return t.overlayPane
			}
		}
	}
	return nil
}

// destKnown reports whether this client has a connection for dest.
func (m *Model) destKnown(dest string) bool {
	for _, d := range m.knownDests() {
		if d == dest {
			return true
		}
	}
	return false
}

// paneOnActiveTab is overlayOnScreen's test for an ordinary pane: it sits in
// the active tab of the active project. One tab's tree is walked, never the
// workspace — this runs from Update's choke point.
func (m *Model) paneOnActiveTab(paneID string) bool {
	tab := m.activeTabModel()
	if tab == nil {
		return false
	}
	return tab.Root.FindLeaf(paneID) != nil || (tab.overlayPane != nil && tab.overlayPane.ID == paneID)
}

// keyboardOwned reports whether something other than the panes has the
// keyboard: an open dialog, a rename, the context menu, the notes editor
// with focus, or a drag in progress. A held dialog waits for all of them —
// opening over a rename would eat its next keystrokes, and opening mid-drag
// leaves the drag with no release.
func (m *Model) keyboardOwned() bool {
	return m.dialog != dialogNone ||
		m.renaming || m.renamingPane ||
		m.ctxMenu.open() ||
		(m.notesMode && m.notesEditor != nil && !m.notesPaneFocused) ||
		m.paneDrag.active() || m.tabDragFromIdx >= 0 || m.splitDragNode != nil
}

// openHeldPluginDialog opens the first held dialog whose pane is on the
// active tab (or unplaced), when nothing owns the keyboard. It runs from
// Update's named-return choke point on EVERY message, so the common case —
// nothing held — must cost one map length, and the next — dialogs held for
// background panes — one tree walk of the active tab per dialog. It reports
// whether it opened one, so the caller can store the changed Model and force
// the repaint every other dialog open does.
//
// A choke point rather than a call at each site that could make a held
// dialog eligible: a dialog closing (any of a dozen arms), a rename ending, a
// tab switch, a project switch, a broadcast that moved the active tab — each
// is one a future path forgets. Lowest pane id first, so two eligible dialogs
// open in the same order on every client.
func (m *Model) openHeldPluginDialog() bool {
	if len(m.pluginDialogs) == 0 || m.keyboardOwned() {
		return false
	}
	var eligible []string
	for id, d := range m.pluginDialogs {
		if d.unplaced || m.paneOnActiveTab(id) {
			eligible = append(eligible, id)
		}
	}
	if len(eligible) == 0 {
		return false
	}
	sort.Strings(eligible)
	d := m.pluginDialogs[eligible[0]]
	m.dialog = dialogPluginError
	m.pluginDialogOpen = d.paneID
	d.openedAt = m.clock()
	m.cancelSequence()
	return true
}

// closePluginDialog drops the open dialog. The next held one, if any, opens
// from the choke point on the way out of this Update.
func (m *Model) closePluginDialog() {
	if m.dialog == dialogPluginError {
		m.dialog = dialogNone
	}
	delete(m.pluginDialogs, m.pluginDialogOpen)
	m.pluginDialogOpen = ""
}

// dropPluginDialog removes one dialog, held or open, and reports whether the
// open one went with it.
func (m *Model) dropPluginDialog(id string) (closedOpen bool) {
	delete(m.pluginDialogs, id)
	if m.pluginDialogOpen != id {
		return false
	}
	m.pluginDialogOpen = ""
	if m.dialog == dialogPluginError {
		m.dialog = dialogNone
		return true
	}
	return false
}

// reconcilePluginDialogs runs on every workspace_state from dest: a dialog
// for a pane that has started a fix (FixLabel set — any client's press, or
// an MCP client's) closes on this client too, one for a pane the broadcast
// no longer lists is dropped, and an unplaced one whose pane has now arrived
// is placed. Returns the repaint a close needs.
func (m *Model) reconcilePluginDialogs(msg WorkspaceStateMsg) tea.Cmd {
	if len(m.pluginDialogs) == 0 {
		return nil
	}
	fixing := make(map[string]bool, len(msg.Panes))
	for _, p := range msg.Panes {
		fixing[p.ID] = p.FixLabel != ""
	}
	var closed bool
	for id, d := range m.pluginDialogs {
		if d.dest != msg.Dest {
			continue
		}
		if fix, present := fixing[id]; present && !fix {
			d.unplaced = false
			continue
		}
		if m.dropPluginDialog(id) {
			closed = true
		}
	}
	if closed {
		return tea.ClearScreen
	}
	return nil
}

// dropPluginDialogsFor forgets every dialog of a destination this client is
// leaving — disconnected by the user, or whose link was lost. Its panes are
// gone from the screen with it, and a dialog that opened for one later would
// send to a connection that no longer exists. Returns the repaint a close
// needs.
func (m *Model) dropPluginDialogsFor(dest string) tea.Cmd {
	var closed bool
	for id, d := range m.pluginDialogs {
		if d.dest == dest && m.dropPluginDialog(id) {
			closed = true
		}
	}
	if closed {
		return tea.ClearScreen
	}
	return nil
}

// --- keys ---

// handlePluginErrorKey drives the open dialog. Focus starts on Close (or Not
// now), so a stray Enter — the dialog opens while the user may be typing —
// only ever closes or postpones; a fix needs arrows + Enter or its letter;
// Update asks once more and takes only y.
func (m Model) handlePluginErrorKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	d := m.pluginDialogs[m.pluginDialogOpen]
	if d == nil {
		// Nothing to drive: the dialog was set without a frame (a test), or
		// the broadcast dropped its pane. Enter and Esc close, as they always
		// did.
		switch msg.String() {
		case "esc", "enter":
			m.dialog = dialogNone
			m.pluginDialogOpen = ""
		}
		return m, nil
	}
	if d.confirming {
		d.confirming = false
		if msg.String() == "y" {
			return m.sendPluginUpdate(d)
		}
		return m, nil
	}
	btns := d.buttons()
	if d.focus < 0 || d.focus >= len(btns) {
		d.focus = len(btns) - 1
	}
	switch msg.String() {
	case "esc":
		m.closePluginDialog()
		return m, nil
	case "left", "shift+tab":
		d.focus = (d.focus + len(btns) - 1) % len(btns)
		return m, nil
	case "right", "tab":
		d.focus = (d.focus + 1) % len(btns)
		return m, nil
	case "enter":
		return m.activatePluginButton(d, btns[d.focus])
	}
	if m.clock().Sub(d.openedAt) < pluginLetterGrace {
		// Typed at the pane, in all likelihood — see pluginLetterGrace.
		return m, nil
	}
	if key := pluginKeyLetter(msg); key != 0 {
		for _, b := range btns {
			if b.key == key {
				return m.activatePluginButton(d, b)
			}
		}
	}
	return m, nil
}

// pluginKeyLetter is the plain letter a keypress typed, folded to lower
// case, or 0: a modified key reports as "ctrl+l" and never matches.
func pluginKeyLetter(msg tea.KeyPressMsg) rune {
	s := msg.Text
	if s == "" {
		s = msg.String()
	}
	if len(s) != 1 {
		return 0
	}
	return asciiLower(rune(s[0]))
}

// activatePluginButton does what the button says. A button its rights gate
// greys does nothing — the reason is already on screen beside it.
func (m Model) activatePluginButton(d *pluginDialog, b pluginButton) (tea.Model, tea.Cmd) {
	if !m.pluginButtonEnabled(d, b) {
		return m, nil
	}
	switch b.kind {
	case pluginButtonFix:
		if cmd := m.refusePluginSend(d, m.sendPluginFixReq(d, b.fix.Name, "")); cmd != nil {
			return m, cmd
		}
		m.closePluginDialog()
		m.setFlash("starting " + b.label + "…")
		return m, m.flashCmd()
	case pluginButtonUpdate:
		d.confirming = true
		return m, nil
	case pluginButtonNotNow:
		if cmd := m.refusePluginSend(d, m.sendPluginFixReq(d, ipc.PluginFixNotNow, d.update.Offered)); cmd != nil {
			return m, cmd
		}
		m.closePluginDialog()
		return m, nil
	}
	m.closePluginDialog()
	return m, nil
}

// refusePluginSend keeps the dialog open when its request could not leave
// and says why, in the box (a flash is behind the dialog) and in the status
// bar (for when it closes). nil when the request left.
func (m *Model) refusePluginSend(d *pluginDialog, err error) tea.Cmd {
	if err == nil {
		return nil
	}
	d.notice = "could not send: " + sanitizeRemoteText(err.Error())
	m.setErrorFlash(d.notice)
	return m.flashCmd()
}

// sendPluginUpdate is the y of the Replace step.
func (m Model) sendPluginUpdate(d *pluginDialog) (tea.Model, tea.Cmd) {
	req, err := ipc.NewMessage(ipc.MsgPluginUpdateReq, ipc.PluginUpdateReqPayload{
		PaneID: d.paneID, Version: d.update.Offered,
	})
	if err == nil {
		req.ID = "pupd-" + m.nextReqGen()
		err = m.sendPluginRequest(d, req)
	}
	if cmd := m.refusePluginSend(d, err); cmd != nil {
		return m, cmd
	}
	m.closePluginDialog()
	m.setFlash("updating " + sanitizeRemoteText(d.plugin) + " to " + sanitizeRemoteText(d.update.Offered) + "…")
	return m, m.flashCmd()
}

// sendPluginFixReq sends one plugin_fix_req for the dialog's pane. ID-bearing
// so the refusal, if any, comes back as plugin_fix_resp rather than silence.
func (m *Model) sendPluginFixReq(d *pluginDialog, fix, version string) error {
	req, err := ipc.NewMessage(ipc.MsgPluginFixReq, ipc.PluginFixReqPayload{
		PaneID: d.paneID, Fix: fix, Version: version,
	})
	if err != nil {
		return err
	}
	req.ID = "pfix-" + m.nextReqGen()
	return m.sendPluginRequest(d, req)
}

// sendPluginRequest sends to the daemon the frame came from — a dialog for a
// pane on another host answers that host — through the STRICT form: a
// one-shot the user pressed a button for has to be able to say it failed,
// and Router.Send drops a message for a destination it has no conn for and
// returns nil. Sent on the Update goroutine like sendStageUpdateReq: it is a
// request, not ordered pane input, and a synchronous send is what a test can
// observe.
func (m *Model) sendPluginRequest(d *pluginDialog, req *ipc.Message) error {
	if m.client == nil {
		return nil
	}
	if err := m.sendForDestStrict(d.dest, req); err != nil {
		log.Printf("plugin %s: send: %v", req.Type, err)
		return err
	}
	return nil
}

// pluginFixRespMsg is the daemon's answer to plugin_fix_req or
// plugin_update_req (Update says which). The dialog closed when the request
// left, so a refusal lands in the status bar, where it can be seen.
type pluginFixRespMsg struct {
	Resp   ipc.PluginFixRespPayload
	Update bool
}

// applyPluginFixResp surfaces a refusal; a success needs nothing — the pane's
// FixLabel arrives on the next broadcast and the pane border says so.
func (m *Model) applyPluginFixResp(msg pluginFixRespMsg) tea.Cmd {
	if msg.Resp.OK {
		return nil
	}
	what := "fix"
	if msg.Update {
		what = "update"
	}
	reason := sanitizeRemoteText(msg.Resp.Error)
	if reason == "" {
		reason = "refused"
	}
	m.setErrorFlash(what + " not started: " + reason)
	return m.flashCmd()
}

// --- rendering ---

// renderPluginErrorDialog draws the open dialog: the error's title and
// message, the update notice under it when both are present, then the
// buttons — or, after Update, the Replace question.
func (m Model) renderPluginErrorDialog() string {
	var b strings.Builder
	d := m.pluginDialogs[m.pluginDialogOpen]
	if d == nil {
		b.WriteString(dialogTitle.Render("Plugin"))
		b.WriteString("\n\n")
		b.WriteString(dialogSubtle.Render("Enter/Esc dismiss"))
		return b.String()
	}

	title, message := d.title, d.message
	if !d.hasError {
		title, message = d.updateTitle, d.updateMessage
	}
	b.WriteString(dialogTitle.Render(truncateToWidth(sanitizeRemoteText(title), pluginDialogInnerWidth)))
	b.WriteString("\n\n")
	writePluginLines(&b, dialogNormal, message)
	if d.hasError && d.update != nil {
		b.WriteByte('\n')
		writePluginLines(&b, dialogNormal, d.updateTitle)
		writePluginLines(&b, dialogNormal, d.updateMessage)
	}
	b.WriteByte('\n')

	if d.confirming {
		// The question says what the daemon will really do: a package manager
		// that owns the binary runs its own upgrade, and nothing is replaced
		// by Quil — "Replace <path>?" would be a false promise there.
		var question string
		verb := "replace"
		switch {
		case d.update != nil && d.update.Manager != "":
			question = "Update with " + sanitizeRemoteText(d.update.Manager) + "?"
			verb = "update"
		case d.update != nil && d.update.Target != "":
			question = "Replace " + elideMiddle(sanitizeRemoteText(d.update.Target), pluginDialogPathWidth) + "?"
		default:
			question = "Replace the installed binary?"
		}
		writePluginLines(&b, dialogNormal, question+" y/n")
		b.WriteByte('\n')
		b.WriteString(dialogSubtle.Render("y " + verb + "    any other key cancels"))
		return b.String()
	}

	// The buttons wrap onto further rows at the inner width — eight fixes do
	// not fit one row — measured on the plain label, which the styles do not
	// widen. A row never exceeds the width: the label clamp keeps one button
	// narrower than a row.
	btns := d.buttons()
	var rows []string
	var row []string
	rowWidth := len(pluginDialogIndent)
	// The greyed buttons' labels, grouped under their reason in the order the
	// reasons first appear, so one line per reason names the buttons it
	// greys: "Log in, Not now: read-only connection".
	var reasons []string
	greyed := map[string][]string{}
	for i, btn := range btns {
		label := " " + bracketLetter(btn.label, btn.key) + " "
		w := lipgloss.Width(label)
		if len(row) > 0 && rowWidth+2+w > pluginDialogInnerWidth {
			rows = append(rows, pluginDialogIndent+strings.Join(row, "  "))
			row, rowWidth = nil, len(pluginDialogIndent)
		}
		if len(row) > 0 {
			rowWidth += 2
		}
		rowWidth += w
		switch reason := m.pluginButtonDisabledReason(d, btn); {
		case reason != "":
			if _, seen := greyed[reason]; !seen {
				reasons = append(reasons, reason)
			}
			greyed[reason] = append(greyed[reason], btn.label)
			row = append(row, dialogSubtle.Render(label))
		case i == d.focus:
			row = append(row, dialogSelected.Render(label))
		default:
			row = append(row, dialogNormal.Render(label))
		}
	}
	rows = append(rows, pluginDialogIndent+strings.Join(row, "  "))
	b.WriteString(strings.Join(rows, "\n") + "\n")
	for _, reason := range reasons {
		writePluginLines(&b, dialogSubtle, strings.Join(greyed[reason], ", ")+": "+reason)
	}
	if d.notice != "" {
		writePluginLines(&b, dialogNormal, d.notice)
	}
	b.WriteByte('\n')
	// 52 cells: it has to fit the inner width unindented.
	b.WriteString(dialogSubtle.Render("←/→ move  Enter activate  letter = button  Esc close"))
	return b.String()
}

// writePluginLines renders a body text, one indented row per line, each
// sanitized and clamped to pluginDialogLineWidth so the indent plus the text
// never exceeds the box: the text matched a pattern in pane output and under
// --remote came off another host.
func writePluginLines(b *strings.Builder, style lipgloss.Style, text string) {
	for _, line := range strings.Split(text, "\n") {
		b.WriteString(pluginDialogIndent + style.Render(truncateToWidth(sanitizeRemoteText(line), pluginDialogLineWidth)) + "\n")
	}
}
