package daemon

import (
	"sync"
	"time"

	"github.com/artyomsv/quil/internal/clientauth"
	"github.com/artyomsv/quil/internal/ipc"
)

// maxParkedPerConn bounds the requests that park a goroutine on one TCP
// conn: each holds a goroutine and a timer for up to five minutes, so an
// unbounded number is a memory lever for any token holder. The local socket
// is not capped — it keeps the trust it always had.
const maxParkedPerConn = 4

// parksGoroutine reports a request whose handler can leave a goroutine
// waiting on the conn's behalf after the dispatch returns.
func parksGoroutine(msgType string) bool {
	return msgType == ipc.MsgWatchNotificationsReq || msgType == ipc.MsgWaitTaskReq
}

// privilegedTypes are audited from ANY transport, beside a create that
// carries raw instance arguments: they act on the daemon itself, so the
// record must not depend on which door they came through.
var privilegedTypes = map[string]bool{
	ipc.MsgShutdown: true, ipc.MsgReloadPlugins: true, ipc.MsgOverlayPolicy: true,
	ipc.MsgKillProcessReq: true, ipc.MsgStageUpdateReq: true, ipc.MsgUpdateCheckReq: true,
}

// admitRequest is the ONE rights check, run first for every frame of an
// authenticated conn. release returns a parked slot; it is a no-op for
// anything that does not park. ok=false means the request was refused and
// must not reach the switch.
func (d *Daemon) admitRequest(conn *ipc.Conn, auth *ipc.AuthState, msg *ipc.Message) (func(), bool) {
	if auth.Revoked() {
		d.refuseRequest(conn, auth, msg, "token revoked")
		return nil, false
	}
	if ok, reason := clientauth.Allows(clientauth.Level(auth.Level), auth.Transport, msg); !ok {
		d.refuseRequest(conn, auth, msg, reason)
		return nil, false
	}
	d.auditPrivileged(conn, auth, msg)
	if auth.Transport != ipc.TransportTCP || !parksGoroutine(msg.Type) {
		return func() {}, true
	}
	return d.admitParked(conn, auth, msg)
}

// admitParked applies the per-conn cap on parked requests. The parked
// counter holds the waits; the conn's watch is counted by its PRESENCE in
// the event queue instead, because a conn holds at most one watcher. That
// gives a watch two properties a counted slot could not without a race:
// one that only replaces the conn's own watch passes even at the cap (it
// adds no goroutine — the evicted one ends at once), and its slot comes back
// the moment the watcher leaves the queue, which every way out does before
// answering.
//
// The replacement needs no branch of its own: while a watcher is registered
// waits are capped one lower, so the waits alone never fill the cap and the
// room check below always passes for it.
func (d *Daemon) admitParked(conn *ipc.Conn, auth *ipc.AuthState, msg *ipc.Message) (func(), bool) {
	watching := d.events.HasWatcher(conn)
	if msg.Type == ipc.MsgWatchNotificationsReq {
		// Room for one more: claim and hand straight back, since the
		// watcher itself will hold the slot. One conn's frames dispatch in
		// order, so nothing else of this conn can claim in between.
		if !auth.TryPark(maxParkedPerConn) {
			d.refuseRequest(conn, auth, msg, "too many waiting requests")
			return nil, false
		}
		auth.Unpark()
		return func() {}, true
	}
	limit := int32(maxParkedPerConn)
	if watching {
		limit--
	}
	if !auth.TryPark(limit) {
		d.refuseRequest(conn, auth, msg, "too many waiting requests")
		return nil, false
	}
	var once sync.Once
	return func() { once.Do(auth.Unpark) }, true
}

// unclassifiedAuditKey is the refusal-audit rate key for every type in no
// class: the type is a client-chosen string, so keying on it would let a
// sender write one line per invented name.
const unclassifiedAuditKey = "unclassified"

// refuseRequest answers an id-bearing request with `error refused` INSTEAD of
// its usual response — no OpRespPayload{ok:false}, no pane_input_resp, since
// a client reading either would take the refusal for an ordinary failure of
// the operation — and drops an id-less one. Audited at most once per
// (conn, type) per minute; the rate state lives on the conn's auth state and
// dies with the conn.
//
// A LOCAL conn is refused only for a never-class type; it keeps the rule that
// a conn which never said hello gets silence, via replyError.
func (d *Daemon) refuseRequest(conn *ipc.Conn, auth *ipc.AuthState, msg *ipc.Message, reason string) {
	key := msg.Type
	if !clientauth.Classified(msg.Type) {
		key = unclassifiedAuditKey
	}
	if auth.ShouldAuditRefusal(key, time.Now()) {
		d.writeAudit(d.auditFor(conn, auth, auditEntry{Event: "refused", Type: msg.Type, Reason: reason}))
	}
	if auth.Transport == ipc.TransportLocal {
		d.replyError(conn, msg, ipc.ErrCodeRefused, reason)
		return
	}
	if msg.ID == "" {
		return
	}
	sendError(conn, msg.ID, truncateField(msg.Type, maxAuditField), ipc.ErrCodeRefused, reason)
}

func (d *Daemon) auditPrivileged(conn *ipc.Conn, auth *ipc.AuthState, msg *ipc.Message) {
	reason := ""
	switch {
	case privilegedTypes[msg.Type]:
	case clientauth.CarriesRawArgs(msg):
		reason = "raw instance arguments"
	default:
		return
	}
	d.writeAudit(d.auditFor(conn, auth, auditEntry{Event: "privileged", Type: msg.Type, Reason: reason}))
}

// auditFor fills an entry's transport, client and token fields. The caller
// writes it through writeAudit, which cuts every client-supplied field.
func (d *Daemon) auditFor(conn *ipc.Conn, auth *ipc.AuthState, e auditEntry) auditEntry {
	e.Transport = conn.Transport()
	if rec, ok := d.clientByConn(conn); ok {
		e.ClientID = rec.id
	}
	if d.hellos != nil {
		e.Kind = d.hellos.roleOf(conn)
	}
	if auth != nil && auth.Transport == ipc.TransportTCP {
		e.TokenID, e.TokenName, e.Rights = auth.TokenID, auth.TokenName, auth.Level
	}
	return e
}
