package daemon

import (
	"log"
	"os"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/persist"
)

// Pane notes on the daemon (phase 3b, #237). The text lives in
// QUIL_HOME/notes/<paneID>.md on the DAEMON's machine, same format and same
// persist functions the TUI used locally; only Pane.NoteRev rides the frame.
//
// Every read and write runs on a worker goroutine — file I/O on the conn's
// dispatch goroutine blocks that client's input — and holds the pane's
// noteMu across the version check AND the file write, so two saves from one
// base cannot both apply. PluginMu is never taken here.

// maxPersistedNoteRev is the largest note_rev restore accepts from
// workspace.json: the JSON decode yields a float64, which holds every integer
// only up to 2^53.
const maxPersistedNoteRev = 1 << 53

// release returns the conn's waiting-request slot (admitParked) on every way
// out, before the answer. The worker keeps only the request ID, never the
// message, so a padded payload is not held while the note lock is awaited.
func (d *Daemon) handleNoteGet(conn *ipc.Conn, msg *ipc.Message, release func()) {
	var p ipc.NoteGetPayload
	if err := msg.DecodePayload(&p); err != nil {
		release()
		d.replyError(conn, msg, ipc.ErrCodeBadPayload, err.Error())
		return
	}
	pane := d.session.Pane(p.PaneID)
	if pane == nil {
		release()
		d.replyError(conn, msg, ipc.ErrCodeBadPayload, "no such pane: "+p.PaneID)
		return
	}
	id := msg.ID
	go func() {
		pane.noteMu.Lock()
		text, err := persist.LoadNotes(config.NotesDir(), pane.ID)
		rev := pane.NoteRev.Load()
		pane.noteMu.Unlock()
		resp := ipc.NoteRespPayload{PaneID: pane.ID, Text: text, Rev: rev}
		if err != nil {
			log.Printf("note_get %s: %v", pane.ID, err)
			resp = ipc.NoteRespPayload{PaneID: pane.ID, Error: err.Error()}
		}
		release()
		respondTo(conn, id, ipc.MsgNoteResp, resp)
	}()
}

// handleNoteSet frees its slot the same way as handleNoteGet: reply runs
// release before every answer. The decoded text (capped at MaxNoteBytes for
// growth) is what the worker keeps, not the raw message.
func (d *Daemon) handleNoteSet(conn *ipc.Conn, msg *ipc.Message, release func()) {
	var p ipc.NoteSetPayload
	if err := msg.DecodePayload(&p); err != nil {
		release()
		d.replyError(conn, msg, ipc.ErrCodeBadPayload, err.Error())
		return
	}
	pane := d.session.Pane(p.PaneID)
	if pane == nil {
		release()
		d.replyError(conn, msg, ipc.ErrCodeBadPayload, "no such pane: "+p.PaneID)
		return
	}
	id := msg.ID
	reply := func(resp ipc.NoteSetRespPayload) {
		release()
		respondTo(conn, id, ipc.MsgNoteSetResp, resp)
	}
	go func() {
		pane.noteMu.Lock()
		// The cap applies to growth only. A note written before the cap
		// existed can be larger, and restore adopts it as it is; refusing
		// every save of it would make it uneditable. So a save up to the
		// stored note's own size is accepted, which lets the user edit and
		// shorten it, but not grow it further.
		if len(p.Text) > ipc.MaxNoteBytes && int64(len(p.Text)) > noteFileSize(pane.ID) {
			pane.noteMu.Unlock()
			reply(ipc.NoteSetRespPayload{PaneID: pane.ID, Error: "too large"})
			return
		}
		cur := pane.NoteRev.Load()
		if p.BaseRev != cur {
			pane.noteMu.Unlock()
			reply(ipc.NoteSetRespPayload{PaneID: pane.ID, Conflict: true, CurrentRev: cur})
			return
		}
		var err error
		if p.Text == "" {
			err = persist.DeleteNotes(config.NotesDir(), pane.ID)
		} else {
			err = persist.SaveNotes(config.NotesDir(), pane.ID, p.Text)
		}
		if err != nil {
			pane.noteMu.Unlock()
			log.Printf("note_set %s: %v", pane.ID, err)
			reply(ipc.NoteSetRespPayload{PaneID: pane.ID, Error: err.Error()})
			return
		}
		// NoteRev is monotonic: a delete increments it exactly like a save,
		// never resets to 0 — "no note" is an empty file/text, not rev 0, so a
		// stale save cannot be reissued against a rev a delete already
		// invalidated.
		newRev := cur + 1
		// Proven by program order rather than by noteMu: the
		// workspace-state build reads NoteRev with a bare Load() and takes no
		// lock (daemon.go), so nothing stops it from racing this write. Store
		// runs strictly AFTER respondTo's enqueue call returns, on this same
		// goroutine — so any Load() anywhere that ever observes newRev
		// necessarily happens after our response was already enqueued, and a
		// state frame built from that value can only land behind it on this
		// conn's FIFO must-deliver queue, never ahead of it.
		reply(ipc.NoteSetRespPayload{PaneID: pane.ID, OK: true, Rev: newRev})
		pane.NoteRev.Store(newRev)
		pane.noteMu.Unlock()
		d.requestBroadcast()
		d.requestSnapshot()
	}()
}

// noteFileExists reports whether a regular note file exists for paneID —
// restore uses it to adopt a pre-3b local note as rev 1.
func noteFileExists(paneID string) bool {
	path, err := persist.NotesPath(config.NotesDir(), paneID)
	if err != nil {
		return false
	}
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular()
}

// noteFileSize is the size of paneID's regular note file, or 0 when there is
// none — the ceiling for a save of a note already over the cap.
func noteFileSize(paneID string) int64 {
	path, err := persist.NotesPath(config.NotesDir(), paneID)
	if err != nil {
		return 0
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return 0
	}
	return fi.Size()
}
