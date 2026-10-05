package tui

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/persist"
	"github.com/artyomsv/quil/internal/textsafe"
)

// Client side of shared notes. The editor (notes.go) holds the
// text and its version; this file owns every send, every answer, and the
// two rules that keep text from being lost: a save outlives the editor that
// made it (pendingNoteSaves), and quit waits for the answer.

// pendingNoteSave is one unanswered note_set, filed under its request id: a
// reopened editor can save the same pane before the old answer arrives, and
// each answer must settle its own text.
type pendingNoteSave struct {
	dest    string
	paneID  string
	text    string
	baseRev uint64
}

// IPC responses (their Update arms re-arm listenForMessages). An error reply
// to a note request arrives as one of these with only Error set.
type noteRespMsg struct {
	dest string
	id   string
	resp ipc.NoteRespPayload
}
type noteSetRespMsg struct {
	dest string
	id   string
	resp ipc.NoteSetRespPayload
}

// Local timers (must NOT re-arm listenForMessages).
type noteLoadTimeoutMsg struct{ id string }
type noteQuitTimeoutMsg struct{}

// noteLoadTimeout bounds a note_get (8 s, like recentScanTimeout); noteQuitWait
// bounds how long quit waits for unanswered saves (the inputDrainTimeout
// pattern). Vars so the test binary can shorten them.
var (
	noteLoadTimeout = 8 * time.Second
	noteQuitWait    = 2 * time.Second
)

// noteErrCap bounds daemon-written error text in the footer and the flash.
const noteErrCap = 80

// openNotesEditorFor builds the editor for pane: remote (note_get sent) for a
// shared destination, today's local file otherwise.
func (m *Model) openNotesEditorFor(pane *PaneModel) (*NotesEditor, tea.Cmd, error) {
	dest := m.destOfPane(pane.ID)
	if !m.sharedData[dest] {
		ed, err := NewNotesEditor(config.NotesDir(), pane.ID, pane.Name, 1, 1)
		return ed, nil, err
	}
	ed := NewRemoteNotesEditor(pane.ID, pane.Name, 1, 1)
	ed.dest = dest
	// Reading a note is a view; saving one is not.
	ed.viewOnly = m.destReadOnly(dest)
	m.notesEditor = ed
	m.noteSaveID = ""
	_, cmd := m.sendNoteGet(dest, pane.ID)
	return ed, cmd, nil
}

// sendNoteGet asks the daemon for the open editor's note. Synchronous send on
// the Update goroutine (one-shot, user-driven); the returned Cmd is only the
// timeout tick; id is the request's, "" when nothing was sent. The answer may
// NOT replace an edited buffer — only reloadNote's confirmed Ctrl+R may.
//
// A send that fails turns only a FIRST load into the read-only error editor;
// a failed reload leaves the loaded text editable and says so in the flash.
func (m *Model) sendNoteGet(dest, paneID string) (id string, cmd tea.Cmd) {
	msg, err := ipc.NewMessage(ipc.MsgNoteGet, ipc.NoteGetPayload{PaneID: paneID})
	if err != nil {
		log.Printf("notes: encode note_get: %v", err)
		return "", nil
	}
	msg.ID = "note-" + m.nextReqGen()
	m.noteLoadID, m.noteLoadDiscards, m.noteLoadSnapshot, m.noteLoadSnapSave = msg.ID, false, "", 0
	if err := m.sendForDestStrict(dest, msg); err != nil {
		m.noteLoadID = ""
		if m.notesEditor.Loading() {
			m.notesEditor.ApplyLoadError(err.Error())
			return "", nil
		}
		m.setFlash("Note not reloaded: " + err.Error())
		return "", m.flashCmd()
	}
	id = msg.ID
	return id, tea.Tick(noteLoadTimeout, func(time.Time) tea.Msg { return noteLoadTimeoutMsg{id: id} })
}

// reloadNote is the confirmed Ctrl+R: the one load whose answer may replace
// the user's edits, because they chose to discard them. What they chose to
// discard is the buffer AS IT IS NOW, so it is snapshotted: typing after the
// confirmation, or an overwrite sent after it, is not part of that choice.
func (m *Model) reloadNote() tea.Cmd {
	ed := m.notesEditor
	id, cmd := m.sendNoteGet(ed.Dest(), ed.PaneID())
	m.noteLoadDiscards = id != ""
	if m.noteLoadDiscards {
		m.noteLoadSnapshot, m.noteLoadSnapSave = ed.Content(), m.noteSavesTaken
	}
	return cmd
}

func (m *Model) applyNoteResp(msg noteRespMsg) tea.Cmd {
	ed := m.notesEditor
	// An answer from a daemon other than the editor's is not the answer: ids
	// are this client's counter, so another host can hold a matching one. It
	// must not take the load id, or the real answer is dropped after it.
	if ed == nil || !ed.Remote() || msg.id == "" || msg.id != m.noteLoadID || msg.dest != ed.Dest() {
		return nil
	}
	// The confirmed reload discards only the buffer the user confirmed on, and
	// never under a save in flight: that save's answer would then land on the
	// reloaded buffer (ApplyLoaded clears saveInFlight while noteSaveID stays
	// set) and report as clean a text the daemon no longer holds. A save
	// answered since the confirmation made the buffer the daemon's newer
	// text, not the one the user agreed to discard. That is counted on this
	// client, not read off the editor's rev: a restarted daemon can hand the
	// save the very rev the editor held at the confirmation.
	confirmed := m.noteLoadDiscards
	savedSince := confirmed && m.noteSavesTaken != m.noteLoadSnapSave
	discards := confirmed && !ed.SaveInFlight() && ed.Content() == m.noteLoadSnapshot && !savedSince
	m.noteLoadID, m.noteLoadDiscards, m.noteLoadSnapshot, m.noteLoadSnapSave = "", false, "", 0
	if msg.resp.Error != "" {
		// A reload that fails leaves the loaded text as it was; only a first
		// load has nothing to show and becomes the read-only error editor.
		if ed.Loading() {
			ed.ApplyLoadError(elideEnd(sanitizeRemoteText(msg.resp.Error), noteErrCap))
		}
		return nil
	}
	// Revisions only grow on one daemon run, so an answer older than what the
	// editor holds is stale — an overwrite sent after this get can answer
	// first (each note request runs on its own daemon worker), and that answer
	// is the newer. The confirmed reload is exempt: a daemon that crashed
	// inside its snapshot debounce restores a LOWER rev, and the user asked
	// for the daemon's text whatever its number. A confirmed reload that is
	// dropped says so, since the user asked for it; a silent one does not —
	// the editor already holds the newer text, so there is nothing to tell.
	if !ed.Loading() && !discards && msg.resp.Rev < ed.Rev() {
		switch {
		case savedSince:
			m.setFlash("Note reload replaced by a newer save")
		case confirmed:
			m.setFlash("Note reload dropped: older than the editor")
		default:
			return nil
		}
		return m.flashCmd()
	}
	// A silent reload (clean editor, newer frame rev) that finds the user
	// typing since it was sent must not replace the typing: it is a conflict.
	// That holds even when the editor is ALREADY conflicted — a later frame,
	// or a Ctrl+S answer overtaking this one, can mark it so while the reload
	// is in flight. Only the confirmed Ctrl+R reload discards. A save in
	// flight counts as unsaved text too: its buffer is not the daemon's yet.
	if !ed.Loading() && (ed.Dirty() || ed.SaveInFlight()) && !discards {
		ed.MarkConflict(msg.resp.Rev)
		return nil
	}
	ed.ApplyLoaded(sanitizeRemoteNote(msg.resp.Text), msg.resp.Rev)
	return nil
}

// sanitizeRemoteNote is the remote-text rule for a note: the
// editor draws the text without a VT emulator, so escapes, C1 and bidi
// controls go; unlike a one-row name, line breaks and tabs are the note's
// own structure and stay. It runs when the note is LOADED, not only when it
// is drawn, so the editor holds the cleaned text and a later save stores it
// on the daemon in place of the original.
func sanitizeRemoteNote(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if textsafe.IsStripped(r) {
			return -1
		}
		return r
	}, s)
}

func (m *Model) applyNoteLoadTimeout(msg noteLoadTimeoutMsg) {
	if m.notesEditor == nil || msg.id == "" || msg.id != m.noteLoadID {
		return
	}
	m.noteLoadID = ""
	if m.notesEditor.Loading() {
		m.notesEditor.ApplyLoadError("no answer from the daemon")
	}
}

// sendNoteSave sends the open remote editor's text and records it as pending
// until the daemon answers — the record is what survives the editor closing.
// overwrite saves from the daemon's rev after a conflict.
func (m *Model) sendNoteSave(overwrite bool) tea.Cmd {
	ed := m.notesEditor
	if ed == nil {
		return nil
	}
	text, base, ok := ed.TakeSave(overwrite)
	if !ok {
		return nil
	}
	dest := ed.Dest()
	msg, err := ipc.NewMessage(ipc.MsgNoteSet, ipc.NoteSetPayload{PaneID: ed.PaneID(), Text: text, BaseRev: base})
	if err != nil {
		ed.AbandonSave(err.Error())
		return nil
	}
	msg.ID = "note-" + m.nextReqGen()
	if m.pendingNoteSaves == nil {
		m.pendingNoteSaves = map[string]pendingNoteSave{}
	}
	m.pendingNoteSaves[msg.ID] = pendingNoteSave{dest: dest, paneID: ed.PaneID(), text: text, baseRev: base}
	m.noteSaveID = msg.ID
	if err := m.sendForDestStrict(dest, msg); err != nil {
		delete(m.pendingNoteSaves, msg.ID)
		m.noteSaveID = ""
		ed.AbandonSave(err.Error())
	}
	return nil
}

// settleNoteSavesFor settles every save pending on dest when its link is lost
// or reattached: the old connection's answers will never arrive, and an
// unanswered save otherwise leaves the open editor "saving…" for good —
// refusing saves, ignoring frames, blocking Ctrl+R. The open editor keeps its
// text dirty and sends it again from the same base once the link is back; a
// closed editor's text goes to notes-conflicts.
func (m *Model) settleNoteSavesFor(dest, reason string) {
	for id, p := range m.pendingNoteSaves {
		if p.dest != dest {
			continue
		}
		delete(m.pendingNoteSaves, id)
		if ed := m.notesEditor; ed != nil && id == m.noteSaveID {
			m.noteSaveID = ""
			ed.AbandonSave(reason)
			continue
		}
		m.keepNoteText(p.dest, p.paneID, p.text, "Note save lost with the link to "+hostLabel(dest))
	}
}

// flushRemoteNotesInPlace is the remote editor's Close(): a dirty editor
// sends its text, and the pending record outlives the editor. Text no save
// can carry — a conflict the user did not overwrite, an edit made after a
// save still unanswered, a send that failed — is kept in notes-conflicts.
func (m *Model) flushRemoteNotesInPlace() {
	ed := m.notesEditor
	if ed == nil || !ed.Remote() {
		return
	}
	m.sendNoteSave(false)
	if text, ok := ed.Unsent(); ok {
		m.keepNoteText(ed.Dest(), ed.PaneID(), text, "Note not saved on the daemon")
	}
	// The closed editor's answers are settled through pendingNoteSaves alone.
	m.noteSaveID, m.noteLoadID, m.noteLoadDiscards, m.noteLoadSnapshot, m.noteLoadSnapSave = "", "", false, "", 0
}

// keepNoteText writes text the daemon will not hold to notes-conflicts and
// says so; reason leads the flash.
func (m *Model) keepNoteText(dest, paneID, text, reason string) {
	path, err := writeNotesConflict(dest, paneID, text)
	if err != nil {
		log.Printf("notes: %s: text for %s could not be kept: %v", reason, paneID, err)
		m.setFlash(reason + " and could not be kept — see quil.log")
		return
	}
	log.Printf("notes: %s: text for %s kept in %s", reason, paneID, path)
	m.setFlash(reason + " — your text is in notes-conflicts")
}

// applyNoteSetResp settles a pending save. With the editor that sent it still
// open, the editor takes the result; otherwise a refusal writes the text to
// notes-conflicts. While quitting, the last answer quits.
func (m *Model) applyNoteSetResp(msg noteSetRespMsg) tea.Cmd {
	pending, ok := m.pendingNoteSaves[msg.id]
	// An answer from a daemon other than the one asked is not the answer: ids
	// are this client's counter.
	if !ok || pending.dest != msg.dest {
		return nil
	}
	delete(m.pendingNoteSaves, msg.id)
	var cmd tea.Cmd
	if ed := m.notesEditor; ed != nil && msg.id == m.noteSaveID {
		m.noteSaveID = ""
		resp := msg.resp
		resp.Error = elideEnd(sanitizeRemoteText(resp.Error), noteErrCap)
		ed.ApplySaveResult(resp)
		if resp.OK {
			m.noteSavesTaken++
		}
	} else if !msg.resp.OK {
		reason := "Note changed elsewhere"
		if !msg.resp.Conflict {
			reason = "Note save refused (" + elideEnd(sanitizeRemoteText(msg.resp.Error), noteErrCap) + ")"
		}
		m.keepNoteText(pending.dest, pending.paneID, pending.text, reason)
		cmd = m.flashCmd()
	}
	if m.quitWaiting && len(m.pendingNoteSaves) == 0 {
		return tea.Quit
	}
	return cmd
}

// reconcileNoteRev applies a frame's note_rev to the open remote editor: a
// newer rev reloads a clean editor silently and marks a dirty one conflicted.
// Ignored while this client's own save is unanswered. A rev lower than
// or EQUAL to the editor's is stale — a frame built before our own save's
// answer can still arrive after it — and revs never go down (a delete bumps
// them too), so only a higher one means another client changed the note.
func (m *Model) reconcileNoteRev(msg WorkspaceStateMsg) tea.Cmd {
	ed := m.notesEditor
	if ed == nil || !ed.Remote() || ed.Loading() || ed.SaveInFlight() || msg.Dest != ed.Dest() {
		return nil
	}
	for _, p := range msg.Panes {
		if p.ID != ed.PaneID() || p.NoteRev <= ed.Rev() {
			continue
		}
		if ed.Dirty() {
			ed.MarkConflict(p.NoteRev)
			return nil
		}
		_, cmd := m.sendNoteGet(msg.Dest, ed.PaneID())
		return cmd
	}
	return nil
}

// requestQuit is every app.quit arm's exit: flush the open editor, then quit
// at once when nothing is pending, else wait up to noteQuitWait for the
// answers. A remote editor is closed here, so its answers settle
// through pendingNoteSaves and a refusal lands in notes-conflicts rather than
// on an editor about to vanish. A second quit while waiting stops waiting. A
// conflict or no answer ends in notes-conflicts, never in silently lost text.
func (m Model) requestQuit() (tea.Model, tea.Cmd) {
	if m.quitWaiting {
		m.writePendingNoteConflicts()
		return m, tea.Quit
	}
	if m.notesMode && m.notesEditor != nil {
		if m.notesEditor.Remote() {
			m.exitNotesModeInPlace()
		} else if err := m.notesEditor.Close(); err != nil {
			log.Printf("save notes on quit: %v", err)
		}
	}
	if len(m.pendingNoteSaves) == 0 {
		return m, tea.Quit
	}
	m.quitWaiting = true
	m.setFlash("Saving notes…")
	return m, tea.Tick(noteQuitWait, func(time.Time) tea.Msg { return noteQuitTimeoutMsg{} })
}

// writePendingNoteConflicts keeps every unanswered save's text on disk and
// forgets the records. Quit's timeout and FlushNotes' backstop both call it.
func (m *Model) writePendingNoteConflicts() {
	for id, p := range m.pendingNoteSaves {
		if path, err := writeNotesConflict(p.dest, p.paneID, p.text); err != nil {
			log.Printf("notes: unanswered save for %s could not be kept: %v", p.paneID, err)
		} else {
			log.Printf("notes: unanswered save for %s kept in %s", p.paneID, path)
		}
		delete(m.pendingNoteSaves, id)
	}
}

// writeNotesConflict writes text to
// QUIL_HOME/notes-conflicts/<dest>-<pane>-<UTC>.md through persist.SaveNotes,
// whose name validation matters here: the pane id came from a daemon. A name
// already taken gets a counter — SaveNotes replaces, and two texts for one
// pane can be written within one clock tick (writePendingNoteConflicts).
func writeNotesConflict(dest, paneID, text string) (string, error) {
	dir := config.NotesConflictsDir()
	base := fmt.Sprintf("%s-%s-%s", config.DestFileKey(dest), paneID, time.Now().UTC().Format("20060102T150405.000000000Z"))
	name := base
	for i := 2; ; i++ {
		path, err := persist.NotesPath(dir, name)
		if err != nil {
			return "", err
		}
		_, err = os.Lstat(path)
		if err == nil {
			name = fmt.Sprintf("%s-%d", base, i)
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err := persist.SaveNotes(dir, name, text); err != nil {
			return "", err
		}
		return path, nil
	}
}
