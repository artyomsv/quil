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
	// The boot bookkeeping of its panes goes too: a pane the Model still
	// holds for dest, or one already gone with it. A host added again starts
	// from a snapshot, like a reattach.
	m.pruneFixBootFor(func(id string) bool { return m.findPane(id) == nil || m.destOfPane(id) == dest })
	delete(m.fixBootSeeded, dest)
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
		id, err := m.sendPluginFixReq(d, b.fix.Name, "")
		if cmd := m.refusePluginSend(d, err); cmd != nil {
			return m, cmd
		}
		boot := m.notePluginBootReq(id, d.paneID, "Running "+b.label+"…")
		m.closePluginDialog()
		m.setFlash("starting " + b.label + "…")
		return m, tea.Batch(boot, m.flashCmd())
	case pluginButtonUpdate:
		d.confirming = true
		return m, nil
	case pluginButtonNotNow:
		_, err := m.sendPluginFixReq(d, ipc.PluginFixNotNow, d.update.Offered)
		if cmd := m.refusePluginSend(d, err); cmd != nil {
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
	offered := sanitizeRemoteText(d.update.Offered)
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
	boot := m.notePluginBootReq(req.ID, d.paneID, "Running Update to "+offered+"…")
	m.closePluginDialog()
	m.setFlash("updating " + sanitizeRemoteText(d.plugin) + " to " + offered + "…")
	return m, tea.Batch(boot, m.flashCmd())
}

// sendPluginFixReq sends one plugin_fix_req for the dialog's pane. ID-bearing
// so the refusal, if any, comes back as plugin_fix_resp rather than silence;
// the id is returned so the boot mark can be keyed by it.
func (m *Model) sendPluginFixReq(d *pluginDialog, fix, version string) (string, error) {
	req, err := ipc.NewMessage(ipc.MsgPluginFixReq, ipc.PluginFixReqPayload{
		PaneID: d.paneID, Fix: fix, Version: version,
	})
	if err != nil {
		return "", err
	}
	req.ID = "pfix-" + m.nextReqGen()
	return req.ID, m.sendPluginRequest(d, req)
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
	ID     string
	Resp   ipc.PluginFixRespPayload
	Update bool
}

// applyPluginFixResp surfaces a refusal and takes down the boot mark the
// request put up; a success needs nothing — the pane's FixLabel arrives on
// the next broadcast and the pane border says so.
func (m *Model) applyPluginFixResp(msg pluginFixRespMsg) tea.Cmd {
	paneID, marked := m.pluginBootReqs[msg.ID]
	delete(m.pluginBootReqs, msg.ID)
	if msg.Resp.OK {
		return nil
	}
	if marked {
		m.clearPaneBoot(paneID)
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

// --- boot indicator: a fix starting, and the restart after it ---
//
// Both are blank-screen gaps the owner measured live: from the button press
// until the fix child's first byte (a GitHub lookup, `stripe login` starting),
// and from the fix's clean exit until the restarted command paints. The pane
// reuses the boot indicator a freshly created pane shows (`preparing`, the
// spinner chain, restoreSettled's clear-on-content), with its own words.

// markPaneBoot puts the boot indicator on a pane with label, waiting for the
// run's first output before the blank-screen rule may settle it: generation
// runGen when the broadcast named it, the next one after the current when it
// did not (a press, which precedes the spawn). Returns the spinner chain's
// first tick when none is running for the pane.
//
// The daemon spawns the child and only THEN broadcasts, and the child's
// first flush is 2 ms behind the spawn, so a broadcast edge can land after
// the run it describes has already painted. Marking then would wait for a
// generation that never comes — a working pane with "Starting Stripe…" on
// its border for the length of the safety cap. So a run that has reached
// this client (outputGeneration >= runGen) is not marked, and a mark already
// up for it is settled instead. A mark already up (the press's, or the fix's
// when its restart follows) is not re-armed from scratch: it takes the new
// words and, when the broadcast names a run that has not arrived, waits for
// exactly that one — the fix's own output must not settle the restart's
// mark — while an edge from an older daemon (no RunGen) leaves its wait as
// it is.
func (m *Model) markPaneBoot(paneID, label string, runGen uint64) tea.Cmd {
	pane := m.findPane(paneID)
	if pane == nil {
		return nil
	}
	if runGen != 0 && pane.outputGeneration >= runGen {
		m.settlePaneBoot(pane)
		return nil
	}
	if pane.bootLabel != "" && pane.preparing {
		pane.bootLabel = label
		if runGen != 0 {
			pane.bootAwaitRun = true
			pane.bootAwaitGen = runGen
		}
		return nil
	}
	pane.bootOwnsPreparing = !pane.preparing
	pane.preparing = true
	pane.bootLabel = label
	pane.bootAwaitRun = true
	pane.bootAwaitGen = runGen
	pane.resumeStart = time.Now()
	if pane.spinnerTickRunning {
		return nil
	}
	pane.spinnerTickRunning = true
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return spinnerTickMsg{paneID: paneID, frame: 1}
	})
}

// settlePaneBoot takes a markPaneBoot mark down — only what the mark set:
// `preparing` goes only when the mark raised it, never a fresh pane's own.
// A running spinner chain ends itself on its next tick.
func (m *Model) settlePaneBoot(pane *PaneModel) {
	if pane.bootLabel == "" {
		return
	}
	if pane.bootOwnsPreparing {
		pane.preparing = false
	}
	pane.clearBootLabel()
}

// clearPaneBoot is settlePaneBoot by pane id.
func (m *Model) clearPaneBoot(paneID string) {
	if pane := m.findPane(paneID); pane != nil {
		m.settlePaneBoot(pane)
	}
}

// pruneFixBootFor forgets the boot bookkeeping of panes that are gone:
// fixBootSeen (last FixLabel seen) and pluginBootReqs (request id → pane).
// gone says whether a pane id no longer exists for the caller's purpose.
func (m *Model) pruneFixBootFor(gone func(paneID string) bool) {
	for id := range m.fixBootSeen {
		if gone(id) {
			delete(m.fixBootSeen, id)
		}
	}
	for req, id := range m.pluginBootReqs {
		if gone(id) {
			delete(m.pluginBootReqs, req)
		}
	}
}

// notePluginBootReq marks the pane for a request this client just sent and
// remembers the request id, so the daemon's refusal (plugin_fix_resp /
// plugin_update_resp with OK false) can take the mark down again.
func (m *Model) notePluginBootReq(reqID, paneID, label string) tea.Cmd {
	if m.pluginBootReqs == nil {
		m.pluginBootReqs = make(map[string]string)
	}
	m.pluginBootReqs[reqID] = paneID
	return m.markPaneBoot(paneID, label, 0)
}

// reconcileFixBoot runs on every workspace_state: a pane whose FixLabel turns
// non-empty is marked "Running <label>…" (every client, from the daemon's
// own label); one whose FixLabel turns empty while the pane still runs is
// marked "Starting <plugin>…" for the restart; one whose fix ended with the
// child exited (Exited — no restart, the daemon wrote "fix failed") or whose
// restart failed to spawn (SpawnError) loses the mark so its output stays
// the last word. fixBootSeen holds the labels seen last, so only the edges
// act; RunGen tells markPaneBoot whether the run has already arrived. Panes
// the broadcast no longer lists, and that no other destination holds, drop
// their bookkeeping.
//
// The FIRST state from a destination — at attach, and again after a reattach
// (armReattachReset clears fixBootSeeded) — is a snapshot, not an edge: it
// only seeds fixBootSeen, and a mark still up on one of its panes is settled,
// because a fix that started or ended while this client was not connected
// has no gap left to cover and its run may have painted before the outage.
func (m *Model) reconcileFixBoot(msg WorkspaceStateMsg) tea.Cmd {
	var cmds []tea.Cmd
	seeded := m.fixBootSeeded[msg.Dest]
	if !seeded {
		if m.fixBootSeeded == nil {
			m.fixBootSeeded = make(map[string]bool)
		}
		m.fixBootSeeded[msg.Dest] = true
	}
	listed := make(map[string]bool, len(msg.Panes))
	for _, info := range msg.Panes {
		listed[info.ID] = true
		prev, had := m.fixBootSeen[info.ID]
		if !seeded {
			switch {
			case info.FixLabel != "":
				if m.fixBootSeen == nil {
					m.fixBootSeen = make(map[string]string)
				}
				m.fixBootSeen[info.ID] = info.FixLabel
			case had:
				delete(m.fixBootSeen, info.ID)
			}
			if pane := m.findPane(info.ID); pane != nil {
				m.settlePaneBoot(pane)
			}
			continue
		}
		switch {
		case info.FixLabel != "":
			if info.FixLabel == prev {
				continue
			}
			if m.fixBootSeen == nil {
				m.fixBootSeen = make(map[string]string)
			}
			m.fixBootSeen[info.ID] = info.FixLabel
			cmds = append(cmds, m.markPaneBoot(info.ID, "Running "+sanitizeRemoteText(info.FixLabel)+"…", info.RunGen))
		case had:
			delete(m.fixBootSeen, info.ID)
			if info.Exited || info.SpawnError != "" {
				m.clearPaneBoot(info.ID)
				continue
			}
			cmds = append(cmds, m.markPaneBoot(info.ID, "Starting "+m.pluginDisplayName(info.Type)+"…", info.RunGen))
		}
	}
	if len(m.fixBootSeen) > 0 || len(m.pluginBootReqs) > 0 {
		m.pruneFixBootFor(func(id string) bool { return !listed[id] && m.findPane(id) == nil })
	}
	return tea.Batch(cmds...)
}

// pluginDisplayName is the plugin's display name for a pane type, the type
// itself when the registry does not know it.
func (m *Model) pluginDisplayName(paneType string) string {
	if m.pluginRegistry != nil {
		if p := m.pluginRegistry.Get(paneType); p != nil && p.DisplayName != "" {
			return p.DisplayName
		}
	}
	if paneType == "" {
		return "pane"
	}
	return paneType
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
		for _, row := range wrapPluginLine(sanitizeRemoteText(line), pluginDialogLineWidth) {
			b.WriteString(pluginDialogIndent + style.Render(row) + "\n")
		}
	}
}

// wrapPluginLine breaks one message line at spaces into rows of at most w
// cells, so a plugin author's sentence is read whole rather than cut at the
// dialog edge. A single word wider than w is cut, as before.
func wrapPluginLine(line string, w int) []string {
	words := strings.Fields(line)
	if len(words) == 0 {
		return []string{""}
	}
	var rows []string
	cur := ""
	for _, word := range words {
		word = truncateToWidth(word, w)
		switch {
		case cur == "":
			cur = word
		case lipgloss.Width(cur)+1+lipgloss.Width(word) <= w:
			cur += " " + word
		default:
			rows = append(rows, cur)
			cur = word
		}
	}
	return append(rows, cur)
}
