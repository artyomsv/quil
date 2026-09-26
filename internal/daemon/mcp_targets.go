package daemon

import (
	"log"

	"github.com/artyomsv/quil/internal/ipc"
)

// Multi-client sync: aiming an MCP command that used to broadcast to every
// attached TUI at exactly ONE client instead, plus the list_clients_req that
// lets an agent name one explicitly.

// targetConn resolves which attached client's conn an untargeted MCP command
// (set_active_pane, close_tui) should reach.
//
// A non-empty id names an EXACT client: attached, its conn; not attached,
// nil — a stale id (the client detached, or was never real) must not
// silently redirect the command to whichever other window happens to be
// open. Empty means the IMPLICIT target: whichever client typed most
// recently, or — when nobody has typed anything at all — the OLDEST attached
// client, so an untargeted command against a freshly-attached workspace
// lands on a predictable client rather than on whichever TUI happened to
// dial last.
//
// That specific tie-break is NOT mostRecentlyActiveConn's own "nobody typed"
// answer (the newest attached client, per its own doc comment) — this
// function has its own logic rather than delegating, because the two
// callers want different defaults for the same "nobody has typed" state.
// With no client attached at all, nil.
func (d *Daemon) targetConn(clientID string) *ipc.Conn {
	if clientID != "" {
		d.clients.mu.Lock()
		rec := d.clients.recordByID(clientID)
		d.clients.mu.Unlock()
		if rec == nil {
			return nil
		}
		return rec.conn
	}
	d.clients.mu.Lock()
	defer d.clients.mu.Unlock()
	recs := d.clients.sortedRecordsLocked()
	if len(recs) == 0 {
		return nil
	}
	best := recs[0] // oldest attached — the default while nobody has input
	for _, rec := range recs[1:] {
		if rec.lastInputAt.After(best.lastInputAt) {
			best = rec
		}
	}
	return best.conn
}

// handleCloseTUI answers MsgCloseTUI: ask ONE client to exit — the one named
// by CloseTUIPayload.Client, or the implicit target when it is empty or the
// payload is absent entirely (an older bridge sends nil, per the payload's
// own doc comment).
//
// This used to be a bare d.broadcast(msg), which was fine before several
// TUIs could share a daemon and became "close somebody else's window" the
// moment they could. With no attached client at all — a headless daemon —
// there is nothing to close; the command is dropped with a log line rather
// than panicking on a nil conn.
func (d *Daemon) handleCloseTUI(conn *ipc.Conn, msg *ipc.Message) {
	var payload ipc.CloseTUIPayload
	// A decode failure is treated exactly like an absent payload: older
	// bridges send nil, and refusing the command over a malformed-but-empty
	// frame would be a regression for every one of them.
	_ = msg.DecodePayload(&payload)
	target := d.targetConn(payload.Client)
	if target == nil {
		if payload.Client != "" {
			log.Printf("close_tui: no attached client %q; dropping", payload.Client)
		} else {
			log.Printf("close_tui: no attached client; nothing to close")
		}
		return
	}
	target.Send(msg)
}

// handleListClientsReq answers with every attached client — who is attached,
// since when, at what size, whether they hold size master, and when they
// last typed. listClients (clients.go) already merges the hello registry by
// conn and formats times as RFC 3339 UTC.
func (d *Daemon) handleListClientsReq(conn *ipc.Conn, msg *ipc.Message) {
	respondTo(conn, msg.ID, ipc.MsgListClientsResp, ipc.ListClientsRespPayload{
		Clients: d.listClients(),
	})
}
