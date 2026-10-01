package daemon

import (
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/logger"
	"github.com/artyomsv/quil/internal/version"
)

// handleHello registers a client's self-description and answers it. On a
// logged-in TCP conn (a second hello) the answer also states the conn's
// rights; token_id and nonce are ignored there.
//
// It must stay synchronous: frames on one conn are dispatched in order
// (ipc server.go calls the handler per frame), and that is what guarantees a
// request sent right after hello sees the conn as non-legacy. Do not move the
// registry write into a goroutine.
func (d *Daemon) handleHello(conn *ipc.Conn, msg *ipc.Message) {
	var extra ipc.HelloRespPayload
	if a := conn.Auth(); a != nil && a.Transport == ipc.TransportTCP {
		extra.Rights, extra.TokenName = a.Level, a.TokenName
	}
	d.answerHello(conn, msg, extra)
}

// answerHello is the hello path shared by an ordinary hello and the end of a
// TCP login, which adds rights, token_name and server_sig.
func (d *Daemon) answerHello(conn *ipc.Conn, msg *ipc.Message, extra ipc.HelloRespPayload) {
	if msg.ID == "" {
		logger.Debug("hello without an id: ignored")
		return
	}
	var p ipc.HelloPayload
	if err := msg.DecodePayload(&p); err != nil || p.Kind == "" || p.Proto < 1 {
		// The one error reply to a conn that is not (yet) non-legacy: whoever
		// sends hello understands error.
		sendError(conn, msg.ID, msg.Type, ipc.ErrCodeBadPayload, "hello needs a kind and proto >= 1")
		return
	}
	// A login's token id and nonce are spent: never kept in the registry.
	p.TokenID, p.Nonce = "", ""
	p.Kind = truncateField(p.Kind, maxHelloField)
	p.Version = truncateField(p.Version, maxHelloField)
	p.ExeName = truncateField(p.ExeName, maxHelloField)
	p.ClientID = truncateField(p.ClientID, maxHelloField)
	d.hellos.putHello(conn, p)
	respondTo(conn, msg.ID, ipc.MsgHelloResp, ipc.HelloRespPayload{
		Version:   version.Current(),
		Proto:     ipc.ProtocolVersion,
		RunID:     d.runID,
		Caps:      ipc.DaemonCaps(),
		Rights:    extra.Rights,
		TokenName: extra.TokenName,
		ServerSig: extra.ServerSig,
	})
}

// replyError answers an id-bearing request with an error — but only to a
// conn whose hello registered a protocol. A legacy conn (a v1.81.0 bridge)
// reads any frame with its request ID as the answer, so it keeps today's
// silence.
func (d *Daemon) replyError(conn *ipc.Conn, msg *ipc.Message, code, text string) {
	if msg.ID == "" || d.hellos.protoOf(conn) < 1 {
		return
	}
	sendError(conn, msg.ID, msg.Type, code, text)
}

func sendError(conn *ipc.Conn, id, typ, code, text string) {
	respondTo(conn, id, ipc.MsgError, ipc.ErrorPayload{Code: code, Message: text, Type: typ})
}
