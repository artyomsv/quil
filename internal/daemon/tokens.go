package daemon

import (
	"errors"
	"time"

	"github.com/artyomsv/quil/internal/clientauth"
	"github.com/artyomsv/quil/internal/ipc"
)

// expiryTick is how often expired tokens' live conns are closed, and
// expiryClock is the DAEMON's clock the sweep compares against. Vars so tests
// can shorten the tick and inject the clock; initAuth passes both to
// expiryLoop as arguments, so the loop never reads them itself.
var (
	expiryTick  = time.Minute
	expiryClock = time.Now
)

const storeUnavailable = "the token store is not available (see quild.log)"

var errStoreUnavailable = errors.New(storeUnavailable)

func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// handleTokenCreateReq mints a token. The answer is the only place the token
// ever leaves the daemon: it is not logged and not audited.
func (d *Daemon) handleTokenCreateReq(conn *ipc.Conn, msg *ipc.Message) {
	answer := func(p ipc.TokenCreateRespPayload) { respondTo(conn, msg.ID, ipc.MsgTokenCreateResp, p) }
	var req ipc.TokenCreateReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		answer(ipc.TokenCreateRespPayload{Error: "malformed payload: " + err.Error()})
		return
	}
	if d.tokens == nil {
		answer(ipc.TokenCreateRespPayload{Error: storeUnavailable})
		return
	}
	level, err := clientauth.ParseLevel(req.Rights)
	if err != nil {
		answer(ipc.TokenCreateRespPayload{Error: err.Error()})
		return
	}
	expires, err := clientauth.ExpiryFrom(req.Expires, time.Now())
	if err != nil {
		answer(ipc.TokenCreateRespPayload{Error: err.Error()})
		return
	}
	token, e, err := d.tokens.Create(req.Name, level, expires)
	if err != nil {
		answer(ipc.TokenCreateRespPayload{Error: err.Error()})
		return
	}
	d.writeAudit(auditEntry{Event: "token_created", Transport: conn.Transport(),
		TokenID: e.ID, TokenName: e.Name, Rights: string(e.Rights)})
	answer(ipc.TokenCreateRespPayload{Token: token, ID: e.ID, Name: e.Name, Rights: string(e.Rights), Expires: formatTime(e.Expires)})
}

// handleTokenListReq lists every token by its public fields only: the stored
// keys are a verifier, and nothing outside tokens.json needs them.
func (d *Daemon) handleTokenListReq(conn *ipc.Conn, msg *ipc.Message) {
	if d.tokens == nil {
		respondTo(conn, msg.ID, ipc.MsgTokenListResp, ipc.TokenListRespPayload{Error: storeUnavailable})
		return
	}
	entries := d.tokens.List()
	out := make([]ipc.TokenInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, ipc.TokenInfo{
			ID: e.ID, Name: e.Name, Rights: string(e.Rights),
			Created: e.Created.UTC().Format(time.RFC3339), Expires: formatTime(e.Expires), LastUsed: formatTime(e.LastUsed),
		})
	}
	respondTo(conn, msg.ID, ipc.MsgTokenListResp, ipc.TokenListRespPayload{Tokens: out})
}

func (d *Daemon) handleTokenRevokeReq(conn *ipc.Conn, msg *ipc.Message) {
	answer := func(p ipc.TokenRevokeRespPayload) { respondTo(conn, msg.ID, ipc.MsgTokenRevokeResp, p) }
	var req ipc.TokenRevokeReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		answer(ipc.TokenRevokeRespPayload{Error: "malformed payload: " + err.Error()})
		return
	}
	if d.tokens == nil {
		answer(ipc.TokenRevokeRespPayload{Error: storeUnavailable})
		return
	}
	e, closed, err := d.revokeToken(req.Target)
	if err != nil {
		answer(ipc.TokenRevokeRespPayload{Error: err.Error()})
		return
	}
	answer(ipc.TokenRevokeRespPayload{ID: e.ID, Name: e.Name, Closed: closed})
}

// revokeToken runs in two phases. Phase 1, under the admission lock: remove
// the entry, write tokens.json, and — only after the write succeeded — mark
// every live conn of the token revoked, which silences it at once. Phase 2,
// after the lock: send each the reason, flush, close.
func (d *Daemon) revokeToken(target string) (clientauth.Entry, int, error) {
	if d.tokens == nil {
		return clientauth.Entry{}, 0, errStoreUnavailable
	}
	var victims []*ipc.Conn
	e, err := d.tokens.Revoke(target, func(e clientauth.Entry) {
		victims = d.auth.markRevoked(e.ID)
	})
	if err != nil {
		return clientauth.Entry{}, 0, err
	}
	if d.auth.afterRevokeMark != nil {
		d.auth.afterRevokeMark()
	}
	d.writeAudit(auditEntry{Event: "token_revoked", Transport: ipc.TransportLocal,
		TokenID: e.ID, TokenName: e.Name, Rights: string(e.Rights)})
	d.closeAuthConns(victims, "token revoked")
	return e, len(victims), nil
}

// closeAuthConns is phase 2. Close reaches onClientDisconnect, so the
// attached-client cleanup is the existing path. Each conn on its own worker:
// a 1 s flush per conn must not stall the requesting conn.
func (d *Daemon) closeAuthConns(conns []*ipc.Conn, reason string) {
	for _, c := range conns {
		go func(c *ipc.Conn) {
			sendError(c, "", "", ipc.ErrCodeRefused, reason)
			c.Flush(refusalFlushTimeout)
			c.Close()
		}(c)
	}
}

// expireTokens closes the live conns of every expired token, like a revoke
// (reason "token expired"), and audits token_expired ONCE per token. The
// entry stays in tokens.json, listed as expired; login refuses it.
func (d *Daemon) expireTokens(now time.Time) {
	if d.tokens == nil {
		return
	}
	var expired []clientauth.Entry
	var victims []*ipc.Conn
	d.tokens.ExpireSweep(now, func(e clientauth.Entry) {
		expired = append(expired, e)
		victims = append(victims, d.auth.markRevoked(e.ID)...)
	})
	for _, e := range expired {
		if d.auth.firstExpiry(e.ID) {
			d.writeAudit(auditEntry{Event: "token_expired", TokenID: e.ID, TokenName: e.Name, Rights: string(e.Rights)})
		}
	}
	d.closeAuthConns(victims, "token expired")
}

// expiryLoop also reports, once a minute, how many pre-login audit lines the
// cap suppressed in a window that has ended. It returns on daemon shutdown or
// when stop closes (closeAuth).
func (d *Daemon) expiryLoop(tick time.Duration, now func() time.Time, stop <-chan struct{}) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			d.expireTokens(now())
			d.flushPreLoginAudit(time.Now(), false)
		case <-stop:
			return
		case <-d.shutdown:
			return
		}
	}
}
