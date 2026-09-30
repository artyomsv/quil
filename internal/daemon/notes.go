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

func (d *Daemon) handleNoteGet(conn *ipc.Conn, msg *ipc.Message) {
	var p ipc.NoteGetPayload
	if err := msg.DecodePayload(&p); err != nil {
		d.replyError(conn, msg, ipc.ErrCodeBadPayload, err.Error())
		return
	}
	pane := d.session.Pane(p.PaneID)
	if pane == nil {
		d.replyError(conn, msg, ipc.ErrCodeBadPayload, "no such pane: "+p.PaneID)
		return
	}
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
		respondTo(conn, msg.ID, ipc.MsgNoteResp, resp)
	}()
}

func (d *Daemon) handleNoteSet(conn *ipc.Conn, msg *ipc.Message) {
	var p ipc.NoteSetPayload
	if err := msg.DecodePayload(&p); err != nil {
		d.replyError(conn, msg, ipc.ErrCodeBadPayload, err.Error())
		return
	}
	pane := d.session.Pane(p.PaneID)
	if pane == nil {
		d.replyError(conn, msg, ipc.ErrCodeBadPayload, "no such pane: "+p.PaneID)
		return
	}
	if len(p.Text) > ipc.MaxNoteBytes {
		respondTo(conn, msg.ID, ipc.MsgNoteSetResp, ipc.NoteSetRespPayload{PaneID: pane.ID, Error: "too large"})
		return
	}
	go func() {
		pane.noteMu.Lock()
		cur := pane.NoteRev.Load()
		if p.BaseRev != cur {
			pane.noteMu.Unlock()
			respondTo(conn, msg.ID, ipc.MsgNoteSetResp, ipc.NoteSetRespPayload{PaneID: pane.ID, Conflict: true, CurrentRev: cur})
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
			respondTo(conn, msg.ID, ipc.MsgNoteSetResp, ipc.NoteSetRespPayload{PaneID: pane.ID, Error: err.Error()})
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
		respondTo(conn, msg.ID, ipc.MsgNoteSetResp, ipc.NoteSetRespPayload{PaneID: pane.ID, OK: true, Rev: newRev})
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
