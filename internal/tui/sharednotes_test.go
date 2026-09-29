package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/persist"
)

// notesTestModel is a connected model with default bindings, one shared
// local pane on screen, and the notes editor OPEN on it (a note_get sent).
func notesTestModel(t *testing.T) (Model, *fakeConn) {
	t.Helper()
	t.Setenv("QUIL_HOME", t.TempDir())
	m, conn := connectedTestModelCapturingSends(t)
	m.SetBindings(config.Bindings{})
	m.width, m.height = 100, 40
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", ""))
	out, cmd := m.toggleNotesMode()
	runCmdNoWait(cmd) // the notes autosave tick sleeps seconds; every send is synchronous
	return out.(Model), conn
}

func lastSent(t *testing.T, conn *fakeConn, msgType string) *ipc.Message {
	t.Helper()
	conn.mu.Lock()
	defer conn.mu.Unlock()
	for i := len(conn.sent) - 1; i >= 0; i-- {
		if conn.sent[i].Type == msgType {
			return conn.sent[i]
		}
	}
	t.Fatalf("no %s was sent; sent: %d messages", msgType, len(conn.sent))
	return nil
}

func ctrl(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code, Mod: tea.ModCtrl} }
func typed(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: rune(s[0]), Text: s} }

// cmdQuits runs cmd and reports whether it (or a batch member) is tea.Quit.
func cmdQuits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); ok {
		return true
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if cmdQuits(c) {
				return true
			}
		}
	}
	return false
}

func noteFrame(rev uint64, noteRev uint64) WorkspaceStateMsg {
	f := sharedFrame("r", rev, "proj-1", "")
	f.Panes[0].NoteRev = noteRev
	return f
}

func conflictFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(config.NotesConflictsDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// loadedNotesModel is notesTestModel with the note_get answered: text at rev.
func loadedNotesModel(t *testing.T, text string, rev uint64) (Model, *fakeConn) {
	t.Helper()
	m, conn := notesTestModel(t)
	id := lastSent(t, conn, ipc.MsgNoteGet).ID
	m = updateWith(t, m, noteRespMsg{dest: "", id: id, resp: ipc.NoteRespPayload{PaneID: "tab-proj-1-pane", Text: text, Rev: rev}})
	return m, conn
}

func TestToggleNotes_SharedDest_SendsNoteGetAndShowsLoading(t *testing.T) {
	m, conn := notesTestModel(t)
	if !m.notesMode || m.notesEditor == nil || !m.notesEditor.Remote() || !m.notesEditor.Loading() {
		t.Fatalf("editor: mode=%v remote=%v loading=%v", m.notesMode, m.notesEditor != nil && m.notesEditor.Remote(), m.notesEditor != nil && m.notesEditor.Loading())
	}
	sent := lastSent(t, conn, ipc.MsgNoteGet)
	var p ipc.NoteGetPayload
	if err := sent.DecodePayload(&p); err != nil || p.PaneID != "tab-proj-1-pane" || sent.ID == "" {
		t.Errorf("note_get = %+v id=%q err=%v", p, sent.ID, err)
	}
}

func TestUpdate_NoteResp_LoadsTextAndRev(t *testing.T) {
	m, conn := notesTestModel(t)
	id := lastSent(t, conn, ipc.MsgNoteGet).ID
	m = updateWith(t, m, noteRespMsg{dest: "", id: id, resp: ipc.NoteRespPayload{PaneID: "tab-proj-1-pane", Text: "hello\n", Rev: 3}})
	if m.notesEditor.Loading() || m.notesEditor.Rev() != 3 || m.notesEditor.Content() != "hello\n" {
		t.Errorf("after load: loading=%v rev=%d content=%q", m.notesEditor.Loading(), m.notesEditor.Rev(), m.notesEditor.Content())
	}
	stale := updateWith(t, m, noteRespMsg{dest: "", id: "note-old", resp: ipc.NoteRespPayload{Text: "wrong", Rev: 9}})
	if stale.notesEditor.Rev() != 3 {
		t.Error("a note_resp with another id was applied")
	}
}

func TestUpdate_NoteLoadTimeout_MakesEditorReadOnly(t *testing.T) {
	m, conn := notesTestModel(t)
	id := lastSent(t, conn, ipc.MsgNoteGet).ID
	m = updateWith(t, m, noteLoadTimeoutMsg{id: id})
	if m.notesEditor.Loading() || m.notesEditor.LoadError() == "" {
		t.Errorf("after timeout: loading=%v err=%q", m.notesEditor.Loading(), m.notesEditor.LoadError())
	}
	m = updateWith(t, m, typed("x"))
	if m.notesEditor.Dirty() {
		t.Error("a read-only editor took input")
	}
}

func TestUpdate_CtrlS_SendsNoteSetWithBaseRev_FrameRevIgnoredWhileInFlight(t *testing.T) {
	m, conn := notesTestModel(t)
	id := lastSent(t, conn, ipc.MsgNoteGet).ID
	m = updateWith(t, m, noteRespMsg{dest: "", id: id, resp: ipc.NoteRespPayload{PaneID: "tab-proj-1-pane", Text: "a\n", Rev: 2}})
	m = updateWith(t, m, typed("x"))
	m = updateWith(t, m, ctrl('s'))
	sent := lastSent(t, conn, ipc.MsgNoteSet)
	var p ipc.NoteSetPayload
	if err := sent.DecodePayload(&p); err != nil || p.BaseRev != 2 || !strings.HasPrefix(p.Text, "xa") || sent.ID == "" {
		t.Fatalf("note_set = %+v id=%q err=%v", p, sent.ID, err)
	}
	if !m.notesEditor.SaveInFlight() {
		t.Fatal("save not marked in flight")
	}
	// F-3: a frame carrying a newer rev while OUR save is unanswered is not a
	// conflict — it is our own save's frame, or one we will learn about from
	// the answer.
	m = updateWith(t, m, noteFrame(2, 3))
	if m.notesEditor.Conflict() {
		t.Error("frame rev during an in-flight save read as a conflict")
	}
	m = updateWith(t, m, noteSetRespMsg{dest: "", id: sent.ID, resp: ipc.NoteSetRespPayload{PaneID: "tab-proj-1-pane", OK: true, Rev: 3}})
	if m.notesEditor.SaveInFlight() || m.notesEditor.Dirty() || m.notesEditor.Rev() != 3 {
		t.Errorf("after ok: inflight=%v dirty=%v rev=%d", m.notesEditor.SaveInFlight(), m.notesEditor.Dirty(), m.notesEditor.Rev())
	}
}

func TestUpdate_NewerFrameRev_CleanReloadsDirtyConflicts(t *testing.T) {
	m, conn := notesTestModel(t)
	id := lastSent(t, conn, ipc.MsgNoteGet).ID
	m = updateWith(t, m, noteRespMsg{dest: "", id: id, resp: ipc.NoteRespPayload{PaneID: "tab-proj-1-pane", Text: "a\n", Rev: 1}})
	before := countSent(conn, ipc.MsgNoteGet)
	m = updateWith(t, m, noteFrame(2, 2)) // clean → silent reload
	if countSent(conn, ipc.MsgNoteGet) != before+1 {
		t.Fatal("a newer rev on a clean editor did not reload")
	}
	id = lastSent(t, conn, ipc.MsgNoteGet).ID
	m = updateWith(t, m, noteRespMsg{dest: "", id: id, resp: ipc.NoteRespPayload{PaneID: "tab-proj-1-pane", Text: "b\n", Rev: 2}})
	m = updateWith(t, m, typed("x"))
	m = updateWith(t, m, noteFrame(3, 4)) // dirty → conflict, text kept
	if !m.notesEditor.Conflict() || !strings.HasPrefix(m.notesEditor.Content(), "xb") {
		t.Errorf("conflict=%v content=%q", m.notesEditor.Conflict(), m.notesEditor.Content())
	}
	if countSent(conn, ipc.MsgNoteGet) != before+1 {
		t.Error("a dirty editor reloaded on its own")
	}
	// Ctrl+S after the conflict OVERWRITES: base_rev is the daemon's rev.
	m = updateWith(t, m, ctrl('s'))
	var p ipc.NoteSetPayload
	if err := lastSent(t, conn, ipc.MsgNoteSet).DecodePayload(&p); err != nil || p.BaseRev != 4 {
		t.Errorf("overwrite base_rev = %d, want 4", p.BaseRev)
	}
}

func TestUpdate_ConflictCtrlRTwice_Reloads(t *testing.T) {
	m, conn := notesTestModel(t)
	id := lastSent(t, conn, ipc.MsgNoteGet).ID
	m = updateWith(t, m, noteRespMsg{dest: "", id: id, resp: ipc.NoteRespPayload{PaneID: "tab-proj-1-pane", Text: "a\n", Rev: 1}})
	m = updateWith(t, m, typed("x"))
	m = updateWith(t, m, noteFrame(2, 2))
	before := countSent(conn, ipc.MsgNoteGet)
	m = updateWith(t, m, ctrl('r'))
	if countSent(conn, ipc.MsgNoteGet) != before {
		t.Fatal("one Ctrl+R reloaded without a confirm")
	}
	m = updateWith(t, m, ctrl('r'))
	if countSent(conn, ipc.MsgNoteGet) != before+1 {
		t.Fatal("two Ctrl+R did not reload")
	}
	// The confirmed reload is the one load that replaces the edits.
	id = lastSent(t, conn, ipc.MsgNoteGet).ID
	m = updateWith(t, m, noteRespMsg{dest: "", id: id, resp: ipc.NoteRespPayload{PaneID: "tab-proj-1-pane", Text: "b\n", Rev: 2}})
	if m.notesEditor.Conflict() || m.notesEditor.Dirty() || m.notesEditor.Content() != "b\n" {
		t.Errorf("after the confirmed reload: conflict=%v dirty=%v content=%q", m.notesEditor.Conflict(), m.notesEditor.Dirty(), m.notesEditor.Content())
	}
}

func TestExitNotes_DirtyRemote_SaveOutlivesEditor_ConflictWritesFile(t *testing.T) {
	m, conn := notesTestModel(t)
	id := lastSent(t, conn, ipc.MsgNoteGet).ID
	m = updateWith(t, m, noteRespMsg{dest: "", id: id, resp: ipc.NoteRespPayload{PaneID: "tab-proj-1-pane", Text: "a\n", Rev: 1}})
	m = updateWith(t, m, typed("x"))
	out, _ := m.exitNotesMode()
	m = out.(Model)
	if m.notesMode || len(m.pendingNoteSaves) != 1 {
		t.Fatalf("mode=%v pending=%d", m.notesMode, len(m.pendingNoteSaves))
	}
	sent := lastSent(t, conn, ipc.MsgNoteSet)
	m = updateWith(t, m, noteSetRespMsg{dest: "", id: sent.ID, resp: ipc.NoteSetRespPayload{PaneID: "tab-proj-1-pane", Conflict: true, CurrentRev: 5}})
	files := conflictFiles(t)
	if len(files) != 1 || !strings.HasPrefix(files[0], "local-tab-proj-1-pane-") {
		t.Fatalf("conflict files = %v", files)
	}
	data, _ := os.ReadFile(filepath.Join(config.NotesConflictsDir(), files[0]))
	if !strings.HasPrefix(string(data), "xa") {
		t.Errorf("conflict file = %q", data)
	}
	if len(m.pendingNoteSaves) != 0 {
		t.Error("pending save not cleared")
	}
}

// Review focus 3.
func TestUpdate_QuitWithPendingSave_WaitsThenWritesConflictFile(t *testing.T) {
	for _, answered := range []bool{true, false} {
		t.Run(map[bool]string{true: "answered ok", false: "no answer"}[answered], func(t *testing.T) {
			m, conn := notesTestModel(t)
			id := lastSent(t, conn, ipc.MsgNoteGet).ID
			m = updateWith(t, m, noteRespMsg{dest: "", id: id, resp: ipc.NoteRespPayload{PaneID: "tab-proj-1-pane", Text: "a\n", Rev: 1}})
			m = updateWith(t, m, typed("x"))
			out, cmd := m.Update(ctrl('q'))
			m = out.(Model)
			if cmdQuits(cmd) {
				t.Fatal("quit with a dirty remote note did not wait")
			}
			sent := lastSent(t, conn, ipc.MsgNoteSet)
			if answered {
				out, cmd = m.Update(noteSetRespMsg{dest: "", id: sent.ID, resp: ipc.NoteSetRespPayload{PaneID: "tab-proj-1-pane", OK: true, Rev: 2}})
				if !cmdQuits(cmd) {
					t.Fatal("the last answer did not quit")
				}
				if n := len(conflictFiles(t)); n != 0 {
					t.Errorf("%d conflict files after a clean save", n)
				}
				return
			}
			out, cmd = m.Update(noteQuitTimeoutMsg{})
			if !cmdQuits(cmd) {
				t.Fatal("the timeout did not quit")
			}
			if n := len(conflictFiles(t)); n != 1 {
				t.Errorf("%d conflict files after the timeout, want 1", n)
			}
		})
	}
}

func TestToggleNotes_LegacyDest_UsesLocalFileAndSendsNothing(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m, conn := connectedTestModelCapturingSends(t)
	m.SetBindings(config.Bindings{})
	m.width, m.height = 100, 40
	m = updateWith(t, m, stateMsg("", 0, "tab-a")) // no shared_data
	if err := persist.SaveNotes(config.NotesDir(), "tab-a-pane", "local\n"); err != nil {
		t.Fatal(err)
	}
	out, cmd := m.toggleNotesMode()
	runCmdNoWait(cmd) // the notes autosave tick sleeps seconds; every send is synchronous
	m = out.(Model)
	if m.notesEditor == nil || m.notesEditor.Remote() || m.notesEditor.Content() != "local\n" {
		t.Fatalf("legacy editor: %+v", m.notesEditor)
	}
	if countSent(conn, ipc.MsgNoteGet) != 0 {
		t.Error("note_get sent to a legacy daemon")
	}
}

// Keys typed while a save is in flight are not covered by it: an OK answer
// must leave the editor dirty, or the next autosave/exit skips that text.
func TestUpdate_OkAnswer_EditsMadeDuringTheSaveStayDirty(t *testing.T) {
	m, conn := loadedNotesModel(t, "a\n", 1)
	m = updateWith(t, m, typed("x"))
	m = updateWith(t, m, ctrl('s'))
	sent := lastSent(t, conn, ipc.MsgNoteSet)
	m = updateWith(t, m, typed("y"))
	m = updateWith(t, m, noteSetRespMsg{dest: "", id: sent.ID, resp: ipc.NoteSetRespPayload{PaneID: "tab-proj-1-pane", OK: true, Rev: 2}})
	if !m.notesEditor.Dirty() || m.notesEditor.Rev() != 2 {
		t.Fatalf("after ok: dirty=%v rev=%d, want dirty at rev 2", m.notesEditor.Dirty(), m.notesEditor.Rev())
	}
	m = updateWith(t, m, ctrl('s'))
	var p ipc.NoteSetPayload
	if err := lastSent(t, conn, ipc.MsgNoteSet).DecodePayload(&p); err != nil || p.BaseRev != 2 || !strings.HasPrefix(p.Text, "xya") {
		t.Errorf("follow-up note_set = %+v err=%v", p, err)
	}
}

// A conflicted editor cannot save without the user choosing to overwrite, so
// leaving it must keep the text on disk rather than drop it.
func TestExitNotes_ConflictedEditor_KeepsTextInConflictsFile(t *testing.T) {
	m, conn := loadedNotesModel(t, "a\n", 1)
	m = updateWith(t, m, typed("x"))
	m = updateWith(t, m, noteFrame(2, 2))
	if !m.notesEditor.Conflict() {
		t.Fatal("setup: no conflict")
	}
	out, _ := m.exitNotesMode()
	m = out.(Model)
	if n := countSent(conn, ipc.MsgNoteSet); n != 0 {
		t.Errorf("%d note_set sent for a conflicted editor on exit", n)
	}
	files := conflictFiles(t)
	if len(files) != 1 {
		t.Fatalf("conflict files = %v, want 1", files)
	}
	data, _ := os.ReadFile(filepath.Join(config.NotesConflictsDir(), files[0]))
	if !strings.HasPrefix(string(data), "xa") {
		t.Errorf("conflict file = %q", data)
	}
}

// R-2: emptying the note is a delete (text ""), and the save after it uses
// the rev the delete returned — never 0.
func TestUpdate_EmptiedNote_SendsDeleteAndNextSaveUsesItsRev(t *testing.T) {
	m, conn := loadedNotesModel(t, "a", 4)
	m = updateWith(t, m, tea.KeyPressMsg{Code: tea.KeyDelete})
	m = updateWith(t, m, ctrl('s'))
	sent := lastSent(t, conn, ipc.MsgNoteSet)
	var p ipc.NoteSetPayload
	if err := sent.DecodePayload(&p); err != nil || p.Text != "" || p.BaseRev != 4 {
		t.Fatalf("delete note_set = %+v err=%v", p, err)
	}
	m = updateWith(t, m, noteSetRespMsg{dest: "", id: sent.ID, resp: ipc.NoteSetRespPayload{PaneID: "tab-proj-1-pane", OK: true, Rev: 5}})
	m = updateWith(t, m, noteFrame(2, 5)) // our own delete's frame: not newer
	if m.notesEditor.Conflict() || countSent(conn, ipc.MsgNoteGet) != 1 {
		t.Fatalf("own delete's frame reacted: conflict=%v gets=%d", m.notesEditor.Conflict(), countSent(conn, ipc.MsgNoteGet))
	}
	m = updateWith(t, m, typed("b"))
	m = updateWith(t, m, ctrl('s'))
	if err := lastSent(t, conn, ipc.MsgNoteSet).DecodePayload(&p); err != nil || p.BaseRev != 5 || p.Text != "b\n" {
		t.Errorf("save after delete = %+v err=%v, want base 5", p, err)
	}
}

// Note text is written by another client of a daemon the user may not
// control, and the editor draws it without the VT emulator: escapes and bidi
// controls are dropped, line breaks and tabs are kept.
func TestUpdate_NoteResp_StripsControlRunesKeepsLineBreaks(t *testing.T) {
	esc, rlo := string(rune(0x1b)), string(rune(0x202e))
	m, _ := loadedNotesModel(t, "a"+esc+"[31mb\n\tc"+rlo+"\n", 1)
	if got, want := m.notesEditor.Content(), "a[31mb\n\tc\n"; got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

// A silent reload answered after the user started typing must not replace
// the typing: the answer is a conflict, not a load.
//
// The frame at rev 3 between the typing and the answer marks the editor
// conflicted while the SILENT reload is still in flight; being conflicted must
// not read as "the user confirmed Ctrl+R" when its answer lands.
func TestUpdate_ReloadAnswerAfterTyping_MarksConflictKeepsText(t *testing.T) {
	m, conn := loadedNotesModel(t, "a\n", 1)
	m = updateWith(t, m, noteFrame(2, 2)) // clean → reload sent
	id := lastSent(t, conn, ipc.MsgNoteGet).ID
	m = updateWith(t, m, typed("x"))
	m = updateWith(t, m, noteFrame(3, 3)) // dirty → conflicted, reload still in flight
	if !m.notesEditor.Conflict() {
		t.Fatal("setup: the rev-3 frame did not mark a conflict")
	}
	m = updateWith(t, m, noteRespMsg{dest: "", id: id, resp: ipc.NoteRespPayload{PaneID: "tab-proj-1-pane", Text: "b\n", Rev: 2}})
	if !m.notesEditor.Conflict() || !strings.HasPrefix(m.notesEditor.Content(), "xa") {
		t.Fatalf("conflict=%v content=%q", m.notesEditor.Conflict(), m.notesEditor.Content())
	}
	// The overwrite names the newest rev known (3), not the older answer's 2.
	m = updateWith(t, m, ctrl('s'))
	var p ipc.NoteSetPayload
	if err := lastSent(t, conn, ipc.MsgNoteSet).DecodePayload(&p); err != nil || p.BaseRev != 3 {
		t.Errorf("overwrite base_rev = %d err=%v, want 3", p.BaseRev, err)
	}
}

// A save whose answer the lost link took away must not leave the editor
// "saving…" forever: it goes dirty again and saves from the same base once the
// link is back. A CLOSED editor's pending text is kept in notes-conflicts.
func TestLinkLost_PendingNoteSaves_AreSettled(t *testing.T) {
	m, conn := loadedNotesModel(t, "a\n", 1)
	m.SetRedialFunc("", func(Client) (Client, error) { return nil, errors.New("unused") })
	m = updateWith(t, m, typed("x"))
	m = updateWith(t, m, ctrl('s'))
	if !m.notesEditor.SaveInFlight() {
		t.Fatal("setup: save not in flight")
	}
	out, _ := m.Update(linkLostMsg{gen: m.clientGen, dest: "", err: errors.New("EOF")})
	m = out.(Model)
	if m.notesEditor.SaveInFlight() || !m.notesEditor.Dirty() || len(m.pendingNoteSaves) != 0 {
		t.Fatalf("after link loss: inflight=%v dirty=%v pending=%d", m.notesEditor.SaveInFlight(), m.notesEditor.Dirty(), len(m.pendingNoteSaves))
	}
	*m.linkFor("") = reconnectState{} // the reattach completed
	m = updateWith(t, m, ctrl('s'))
	if n := countSent(conn, ipc.MsgNoteSet); n != 2 {
		t.Fatalf("%d note_set sent, want the resend after the link came back", n)
	}
	var p ipc.NoteSetPayload
	if err := lastSent(t, conn, ipc.MsgNoteSet).DecodePayload(&p); err != nil || p.BaseRev != 1 {
		t.Errorf("resend = %+v err=%v, want base 1", p, err)
	}

	// Closed editor: the pending save's text is kept on disk.
	out, _ = m.exitNotesMode()
	m = out.(Model)
	if len(m.pendingNoteSaves) != 1 {
		t.Fatalf("setup: pending = %d", len(m.pendingNoteSaves))
	}
	out, _ = m.Update(linkLostMsg{gen: m.clientGen, dest: "", err: errors.New("EOF")})
	m = out.(Model)
	if len(m.pendingNoteSaves) != 0 || len(conflictFiles(t)) != 1 {
		t.Errorf("closed editor after link loss: pending=%d files=%v", len(m.pendingNoteSaves), conflictFiles(t))
	}
}

// twoDestNotes is twoDestModel with a shared project on each destination
// ("proj-1" local, "proj-2" on hostA) and the notes editor open on one of
// them, loaded at rev 1 and edited ("x" typed before "a").
type twoDestNotes struct {
	t             *testing.T
	m             Model
	local, remote *fakeConn
}

func newTwoDestNotes(t *testing.T, openOn string) *twoDestNotes {
	t.Helper()
	t.Setenv("QUIL_HOME", t.TempDir())
	m, local, remote := twoDestModel(t)
	m.SetBindings(config.Bindings{})
	m.width, m.height = 100, 40
	h := &twoDestNotes{t: t, m: m, local: local, remote: remote}
	h.step(sharedFrame("r", 1, "proj-1", ""))
	far := sharedFrame("q", 1, "proj-2", "")
	far.Dest = "hostA"
	far.Tabs[0].ProjectID = "proj-2" // stateMsg files its tab under proj-1
	h.step(far)
	h.activate(openOn)
	out, cmd := h.m.toggleNotesMode()
	runCmdNoWait(cmd) // the notes autosave tick sleeps seconds; every send is synchronous
	h.m = out.(Model)
	conn, pane := local, "tab-proj-1-pane"
	if openOn != "" {
		conn, pane = remote, "tab-proj-2-pane"
	}
	id := lastSent(t, conn, ipc.MsgNoteGet).ID
	h.step(noteRespMsg{dest: openOn, id: id, resp: ipc.NoteRespPayload{PaneID: pane, Text: "a\n", Rev: 1}})
	h.step(typed("x"))
	return h
}

// step runs Update WITHOUT its Cmds: every send asserted on is synchronous,
// and a re-armed listen on this router blocks once both closed conns have
// reported their loss.
func (h *twoDestNotes) step(msg tea.Msg) {
	h.t.Helper()
	out, _ := h.m.Update(msg)
	h.m = out.(Model)
}

// activate makes dest's project the active one.
func (h *twoDestNotes) activate(dest string) {
	h.t.Helper()
	for i, p := range h.m.projects {
		if p.Dest == dest {
			h.m.activeProject = i
			h.m.syncActiveDest()
			return
		}
	}
	h.t.Fatalf("no project for %q", dest)
}

// A closed editor's save goes to the daemon the editor was opened against,
// not to whatever destination is active once its pane has vanished.
func TestExitNotes_PaneVanished_SaveGoesToPinnedDest(t *testing.T) {
	h := newTwoDestNotes(t, "")
	h.activate("hostA") // e.g. an MCP switch, while the editor is open
	gone := sharedFrame("r", 2, "proj-1", "")
	gone.Tabs[0].Panes = []string{"tab-proj-1-other"}
	gone.Panes[0].ID = "tab-proj-1-other"
	h.step(gone)
	if h.m.notesMode {
		t.Fatal("setup: notes mode survived its pane")
	}
	if countSent(h.remote, ipc.MsgNoteSet) != 0 || countSent(h.local, ipc.MsgNoteSet) != 1 {
		t.Errorf("note_set local=%d remote=%d, want 1 to the pinned local daemon", countSent(h.local, ipc.MsgNoteSet), countSent(h.remote, ipc.MsgNoteSet))
	}
}

// Disconnecting a host settles the note saves pending on it at once: a
// closed editor's text is kept in notes-conflicts now, not at quit.
func TestDisconnectHost_PendingNoteSave_KeptInConflictsFile(t *testing.T) {
	h := newTwoDestNotes(t, "hostA")
	out, _ := h.m.exitNotesMode()
	h.m = out.(Model)
	if countSent(h.remote, ipc.MsgNoteSet) != 1 || len(h.m.pendingNoteSaves) != 1 {
		t.Fatalf("setup: note_set=%d pending=%d", countSent(h.remote, ipc.MsgNoteSet), len(h.m.pendingNoteSaves))
	}
	h.m.dialog, h.m.confirmKind, h.m.confirmID = dialogConfirm, confirmKindDisconnectHost, "hostA"
	h.step(typed("y"))
	if h.m.dialog != dialogNone {
		t.Fatal("setup: the disconnect confirm did not run")
	}
	files := conflictFiles(t)
	if len(h.m.pendingNoteSaves) != 0 || len(files) != 1 || !strings.HasPrefix(files[0], config.DestFileKey("hostA")+"-tab-proj-2-pane-") {
		t.Errorf("after disconnect: pending=%d files=%v", len(h.m.pendingNoteSaves), files)
	}
}

// A save made DURING an outage is enqueued on the dead connection and never
// answered; only the reattach settle un-sticks it. The open editor resends
// from the same base on the new connection; a closed editor's text is kept.
func TestReattach_NoteSaveSentDuringOutage_IsSettled(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "open editor", true: "closed editor"}[closed], func(t *testing.T) {
			m, conn := loadedNotesModel(t, "a\n", 1)
			m.SetRedialFunc("", func(Client) (Client, error) { return nil, errors.New("unused") })
			m = updateWith(t, m, typed("x"))
			out, _ := m.Update(linkLostMsg{gen: m.clientGen, dest: "", err: errors.New("EOF")})
			m = out.(Model)
			autosave := func() {
				m.notesEditor.lastEditAt = time.Now().Add(-2 * notesDebounceWindow)
				out, _ := m.Update(notesTickMsg{}) // its Cmd is the next 5 s tick: not run
				m = out.(Model)
			}
			if closed {
				out, _ = m.exitNotesMode()
				m = out.(Model)
			} else {
				autosave()
			}
			if countSent(conn, ipc.MsgNoteSet) != 1 || len(m.pendingNoteSaves) != 1 {
				t.Fatalf("setup: the outage save: sent=%d pending=%d", countSent(conn, ipc.MsgNoteSet), len(m.pendingNoteSaves))
			}

			fresh := newFakeConn()
			close(fresh.recv)
			out, _ = m.Update(redialResultMsg{gen: m.linkOf("").gen, dest: "", client: fresh})
			m = out.(Model)
			if m.linkOf("").active || len(m.pendingNoteSaves) != 0 {
				t.Fatalf("after reattach: link active=%v pending=%d", m.linkOf("").active, len(m.pendingNoteSaves))
			}
			if closed {
				if n := len(conflictFiles(t)); n != 1 {
					t.Errorf("%d conflict files, want the closed editor's text kept", n)
				}
				return
			}
			if m.notesEditor.SaveInFlight() || !m.notesEditor.Dirty() {
				t.Fatalf("editor after reattach: inflight=%v dirty=%v", m.notesEditor.SaveInFlight(), m.notesEditor.Dirty())
			}
			autosave()
			var p ipc.NoteSetPayload
			if err := lastSent(t, fresh, ipc.MsgNoteSet).DecodePayload(&p); err != nil || p.BaseRev != 1 || !strings.HasPrefix(p.Text, "xa") {
				t.Errorf("resend on the new connection = %+v err=%v, want base 1", p, err)
			}
		})
	}
}

// Quit from the overlay key path waits for pending note saves too.
func TestUpdate_QuitFromOverlay_WaitsForPendingSave(t *testing.T) {
	m, _ := loadedNotesModel(t, "a\n", 1)
	m = updateWith(t, m, typed("x"))
	out, _ := m.exitNotesMode()
	m = out.(Model)
	tab := m.activeTabModel()
	overlay := NewPaneModel("pane-o", 1024)
	overlay.Type = overlayPluginLazygit
	tab.overlayPane = overlay
	tab.overlayVisible = true
	out, cmd := m.Update(ctrl('q'))
	m = out.(Model)
	if cmdQuits(cmd) || !m.quitWaiting {
		t.Errorf("quit from the overlay did not wait (waiting=%v)", m.quitWaiting)
	}
}

// A save the daemon refused (too large) is not resent by autosave every tick;
// the next real edit resumes it.
func TestUpdate_RefusedSave_NoAutosaveUntilEdited(t *testing.T) {
	m, conn := loadedNotesModel(t, "a\n", 1)
	m = updateWith(t, m, typed("x"))
	m = updateWith(t, m, ctrl('s'))
	sent := lastSent(t, conn, ipc.MsgNoteSet)
	m = updateWith(t, m, noteSetRespMsg{dest: "", id: sent.ID, resp: ipc.NoteSetRespPayload{PaneID: "tab-proj-1-pane", Error: "too large"}})
	tick := func() {
		m.notesEditor.lastEditAt = time.Now().Add(-2 * notesDebounceWindow)
		out, _ := m.Update(notesTickMsg{}) // its Cmd is the next 5 s tick: not run
		m = out.(Model)
	}
	tick()
	if n := countSent(conn, ipc.MsgNoteSet); n != 1 {
		t.Fatalf("%d note_set after a refusal, want no autosave resend", n)
	}
	m = updateWith(t, m, tea.KeyPressMsg{Code: tea.KeyRight}) // not an edit
	tick()
	if n := countSent(conn, ipc.MsgNoteSet); n != 1 {
		t.Fatalf("%d note_set after a cursor move, want still 1", n)
	}
	m = updateWith(t, m, typed("y"))
	tick()
	if n := countSent(conn, ipc.MsgNoteSet); n != 2 {
		t.Errorf("%d note_set after an edit, want the autosave to resume", n)
	}
}

// An answer to a save made by a CLOSED editor settles that save, never the
// editor since reopened on the same pane.
func TestUpdate_OldSaveAnswer_DoesNotSettleReopenedEditor(t *testing.T) {
	m, conn := loadedNotesModel(t, "a\n", 1)
	m = updateWith(t, m, typed("x"))
	out, _ := m.exitNotesMode()
	m = out.(Model)
	oldSave := lastSent(t, conn, ipc.MsgNoteSet)
	out, cmd := m.toggleNotesMode()
	runCmdNoWait(cmd) // the notes autosave tick sleeps seconds; every send is synchronous
	m = out.(Model)
	id := lastSent(t, conn, ipc.MsgNoteGet).ID
	m = updateWith(t, m, noteRespMsg{dest: "", id: id, resp: ipc.NoteRespPayload{PaneID: "tab-proj-1-pane", Text: "c\n", Rev: 3}})
	m = updateWith(t, m, noteSetRespMsg{dest: "", id: oldSave.ID, resp: ipc.NoteSetRespPayload{PaneID: "tab-proj-1-pane", Conflict: true, CurrentRev: 3}})
	if m.notesEditor.Conflict() {
		t.Error("the closed editor's answer marked the reopened one conflicted")
	}
	if n := len(conflictFiles(t)); n != 1 {
		t.Errorf("%d conflict files, want 1 for the closed editor's refused text", n)
	}
}

// The daemon answers a note_set for a pane it no longer has with an error
// reply, not a note_set_resp; that reply must settle the pending save.
func TestListen_ErrorReplyToNoteSet_SettlesPendingSave(t *testing.T) {
	m, conn := loadedNotesModel(t, "a\n", 1)
	m = updateWith(t, m, typed("x"))
	out, _ := m.exitNotesMode()
	m = out.(Model)
	sent := lastSent(t, conn, ipc.MsgNoteSet)

	src := newFakeConn()
	reply, err := ipc.NewMessage(ipc.MsgError, ipc.ErrorPayload{Code: ipc.ErrCodeBadPayload, Message: "no such pane", Type: ipc.MsgNoteSet})
	if err != nil {
		t.Fatal(err)
	}
	reply.ID = sent.ID
	src.recv <- reply
	listener := m
	listener.client = src
	arrived := listener.listenForMessages()()
	if _, ok := arrived.(noteSetRespMsg); !ok {
		t.Fatalf("error reply to note_set arrived as %T", arrived)
	}
	m = updateWith(t, m, arrived)
	if len(m.pendingNoteSaves) != 0 {
		t.Error("pending save not settled by the error reply")
	}
	if n := len(conflictFiles(t)); n != 1 {
		t.Errorf("%d conflict files, want 1", n)
	}
}

// A second quit while waiting stops waiting: the text goes to a file now.
func TestUpdate_SecondQuitWhileWaiting_QuitsAndKeepsText(t *testing.T) {
	m, _ := loadedNotesModel(t, "a\n", 1)
	m = updateWith(t, m, typed("x"))
	out, cmd := m.Update(ctrl('q'))
	m = out.(Model)
	if cmdQuits(cmd) {
		t.Fatal("first quit did not wait")
	}
	if _, cmd = m.Update(ctrl('q')); !cmdQuits(cmd) {
		t.Fatal("second quit did not quit")
	}
	if n := len(conflictFiles(t)); n != 1 {
		t.Errorf("%d conflict files, want 1", n)
	}
}

// FlushNotes runs after the program exited (a close_tui, a lost link): no
// answer can arrive, so unanswered saves are kept on disk.
func TestFlushNotes_PendingSave_WritesConflictFile(t *testing.T) {
	m, _ := loadedNotesModel(t, "a\n", 1)
	m = updateWith(t, m, typed("x"))
	m = updateWith(t, m, ctrl('s'))
	m.FlushNotes()
	if n := len(conflictFiles(t)); n != 1 {
		t.Errorf("%d conflict files, want 1", n)
	}
}
