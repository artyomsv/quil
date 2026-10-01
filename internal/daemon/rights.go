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
	if !auth.TryPark(maxParkedPerConn) {
		d.refuseRequest(conn, auth, msg, "too many waiting requests")
		return nil, false
	}
	var once sync.Once
	return func() { once.Do(auth.Unpark) }, true
}

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
	if auth.ShouldAuditRefusal(msg.Type, time.Now()) {
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
