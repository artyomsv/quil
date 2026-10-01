package daemon

import (
	"errors"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/artyomsv/quil/internal/clientauth"
	"github.com/artyomsv/quil/internal/debugserver"
	"github.com/artyomsv/quil/internal/ipc"
)

const (
	// defaultLoginStepTimeout bounds each login step: waiting for the first
	// frame, then for auth_proof. A TIMER, not a read deadline, so markDead's
	// use of the read deadline is untouched.
	defaultLoginStepTimeout = 5 * time.Second
	// The guessing backoff is min(base * 2^(n-1), cap).
	defaultBackoffBase  = 250 * time.Millisecond
	defaultBackoffCap   = 2 * time.Second
	refusalFlushTimeout = time.Second
	// connDrainTimeout bounds how long Stop waits for the conns' disconnect
	// callbacks before it closes the audit log they write to.
	connDrainTimeout = 2 * time.Second
)

// The login timer, the step timeout and the backoff base/cap are package vars
// so timing tests run short. initAuth COPIES them into the authService before
// any conn exists: no login goroutine reads a var, so a test that sets one
// before newAuthHarness and restores it in t.Cleanup cannot race a live login
// under -race.
var (
	loginStepTimeout = defaultLoginStepTimeout
	loginBackoffBase = defaultBackoffBase
	loginBackoffCap  = defaultBackoffCap
)

// listenerWhy is the [listener] tcp reason in LoopbackAddr's non-loopback
// error (each caller keeps its own wording).
const listenerWhy = "the TCP listener is loopback-only until it supports TLS; reach it from another machine with ssh -L"

// Login states. The FIRST frame moves pending → checking with a CAS, which is
// what clears the deadline: a timer that fires afterwards finds the state
// moved and does nothing. The same holds for the proof step.
//
// loginRefusing: a refusal is being flushed. The session stays registered in
// this state until onTCPDisconnect forgets it, so a frame that arrives during
// the 1 s flush matches no CAS in handlePreLogin and is ignored — nothing but
// refuseLogin's own Close ends the conn.
const (
	loginPending int32 = iota
	loginChecking
	loginProofPending
	loginDone
	loginRefusing
)

type loginSession struct {
	state atomic.Int32

	tmu   sync.Mutex
	timer *time.Timer

	// refused is closed once refuseLogin has flushed and closed the conn.
	refused     chan struct{}
	refusedOnce sync.Once

	// Written on the conn's dispatch goroutine before the proof step's timer
	// is armed, and read after that — by the dispatch goroutine or by the
	// timer, never both (the state CAS lets exactly one of them proceed).
	hello    *ipc.Message
	clientID string
	kind     string
	tokenID  string
	nonceC   string
	nonceS   string
}

func (s *loginSession) arm(d time.Duration, f func()) {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(d, f)
}

func (s *loginSession) disarm() {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
}

// requestID is the ID every refusal after the hello carries: the client reads
// the login's answers by the hello's ID and drops anything else, so a refusal
// under another ID would reach the user as a bare EOF.
func (s *loginSession) requestID(msg *ipc.Message) string {
	if s != nil && s.hello != nil {
		return s.hello.ID
	}
	if msg != nil {
		return msg.ID
	}
	return ""
}

// authService holds the TCP logins in progress, the tokenID → conns index
// and the guessing backoff. Lock order: the token store's lock, THEN idxMu
// (Admit/Revoke/ExpireSweep call in under the store lock); mu and persistMu
// are leaves. Nothing here calls into the Store while holding any of them.
type authService struct {
	mu     sync.Mutex
	logins map[*ipc.Conn]*loginSession

	idxMu       sync.Mutex
	index       map[string]map[*ipc.Conn]bool
	expiredSeen map[string]bool

	failures atomic.Int64
	// Copied from the package vars by initAuth; a zero field (an authService
	// built bare, as TestBackoff_Curve does) reads its default.
	stepTimeout time.Duration
	backoffBase time.Duration
	backoffCap  time.Duration
	sleep       func(time.Duration) // nil = time.Sleep

	// The last_used writes running on workers; closeAuth waits for them so a
	// shutdown never leaves one half-written.
	persistMu     sync.Mutex
	persistClosed bool
	persistWG     sync.WaitGroup

	// afterRevokeMark is a test seam between revoke phases 1 and 2.
	afterRevokeMark func()
	// beforeRefusalFlush is a test seam between a refusal's send and its
	// flush. Atomic: the login timer's goroutine reads it.
	beforeRefusalFlush atomic.Pointer[func()]
}

// step is the login step timeout.
func (a *authService) step() time.Duration {
	if a.stepTimeout > 0 {
		return a.stepTimeout
	}
	return defaultLoginStepTimeout
}

func (a *authService) begin(conn *ipc.Conn) *loginSession {
	s := &loginSession{refused: make(chan struct{})}
	a.mu.Lock()
	if a.logins == nil {
		a.logins = make(map[*ipc.Conn]*loginSession)
	}
	a.logins[conn] = s
	a.mu.Unlock()
	return s
}

func (a *authService) session(conn *ipc.Conn) *loginSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.logins[conn]
}

func (a *authService) forget(conn *ipc.Conn) {
	a.mu.Lock()
	s := a.logins[conn]
	delete(a.logins, conn)
	a.mu.Unlock()
	if s != nil {
		s.disarm()
	}
}

// indexAdd runs inside Store.Admit's callback, i.e. under the store lock.
func (a *authService) indexAdd(tokenID string, conn *ipc.Conn) {
	a.idxMu.Lock()
	defer a.idxMu.Unlock()
	if a.index == nil {
		a.index = make(map[string]map[*ipc.Conn]bool)
	}
	if a.index[tokenID] == nil {
		a.index[tokenID] = make(map[*ipc.Conn]bool)
	}
	a.index[tokenID][conn] = true
}

func (a *authService) indexRemove(conn *ipc.Conn) {
	a.idxMu.Lock()
	defer a.idxMu.Unlock()
	for id, conns := range a.index {
		delete(conns, conn)
		if len(conns) == 0 {
			delete(a.index, id)
		}
	}
}

// markRevoked is revoke/expiry phase 1 for one token: every live conn of it
// is silenced at once (broadcasts skipped, requests refused). Runs under the
// store lock; returns the conns phase 2 closes.
func (a *authService) markRevoked(tokenID string) []*ipc.Conn {
	a.idxMu.Lock()
	defer a.idxMu.Unlock()
	var out []*ipc.Conn
	for c := range a.index[tokenID] {
		if st := c.Auth(); st != nil {
			st.Revoke()
		}
		out = append(out, c)
	}
	return out
}

// firstExpiry reports true the first time a token is seen expired.
func (a *authService) firstExpiry(tokenID string) bool {
	a.idxMu.Lock()
	defer a.idxMu.Unlock()
	if a.expiredSeen == nil {
		a.expiredSeen = make(map[string]bool)
	}
	if a.expiredSeen[tokenID] {
		return false
	}
	a.expiredSeen[tokenID] = true
	return true
}

// backoff is min(base * 2^(n-1), cap) after n consecutive failures; by
// default min(250 ms * 2^(n-1), 2 s).
func (a *authService) backoff() time.Duration {
	n := a.failures.Load()
	if n <= 0 {
		return 0
	}
	d, ceiling := a.backoffBase, a.backoffCap
	if d <= 0 {
		d = defaultBackoffBase
	}
	if ceiling <= 0 {
		ceiling = defaultBackoffCap
	}
	for i := int64(1); i < n && d < ceiling; i++ {
		d *= 2
	}
	return min(d, ceiling)
}

func (a *authService) wait(d time.Duration) {
	if a.sleep != nil {
		a.sleep(d)
		return
	}
	time.Sleep(d)
}

// goPersist runs f on a worker closeAuth waits for. After closeAuth it runs
// nothing: the daemon is going down and the store's last write has been made.
func (a *authService) goPersist(f func()) {
	a.persistMu.Lock()
	defer a.persistMu.Unlock()
	if a.persistClosed {
		return
	}
	a.persistWG.Add(1)
	go func() {
		defer a.persistWG.Done()
		f()
	}()
}

func (a *authService) closePersist() {
	a.persistMu.Lock()
	a.persistClosed = true
	a.persistMu.Unlock()
	a.persistWG.Wait()
}

// initAuth opens the token store and the audit log. Either failing leaves the
// TCP listener off (startConfiguredListener checks) and the unix socket up.
func (d *Daemon) initAuth(home string) error {
	// Before any conn exists: login goroutines read these fields, never the
	// package vars.
	d.auth.stepTimeout = loginStepTimeout
	d.auth.backoffBase = loginBackoffBase
	d.auth.backoffCap = loginBackoffCap
	var errs []error
	if store, err := clientauth.OpenStore(filepath.Join(home, "tokens.json")); err != nil {
		errs = append(errs, fmt.Errorf("token store: %w", err))
	} else {
		d.tokens = store
		if n := store.Dropped(); n > 0 {
			// The count only: a dropped entry is by definition one this build
			// could not trust, and its fields have no business in the log.
			log.Printf("warning: tokens.json: %d unreadable entries skipped", n)
		}
	}
	if audit, err := openAuditLog(home); err != nil {
		errs = append(errs, fmt.Errorf("audit log: %w", err))
	} else {
		d.audit = audit
	}
	// Read on THIS goroutine and handed over as arguments: the loop never
	// touches the package vars a test restores in t.Cleanup.
	go d.expiryLoop(expiryTick, expiryClock)
	return errors.Join(errs...)
}

func (d *Daemon) closeAuth() {
	d.auth.closePersist()
	if err := d.audit.Close(); err != nil {
		log.Printf("audit: close: %v", err)
	}
}

// startTCPListener validates addr as loopback-only BEFORE anything binds, then
// opens the listener. ipc.StartTCP refuses a non-loopback bind as well, but
// only after binding it; the check here means nothing ever listens on a
// non-loopback address, even for a moment.
func (d *Daemon) startTCPListener(raw string) (net.Addr, error) {
	addr, ok, err := debugserver.LoopbackAddr("[listener] tcp", listenerWhy, raw)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("[listener] tcp is empty")
	}
	return d.server.StartTCP(addr, ipc.TCPHooks{Accepted: d.onTCPAccepted, Rejected: d.onTCPRejected})
}

// startConfiguredListener opens [listener] tcp when it is set, valid, and
// the audit log and token store are open (the audit log is the only record of
// network logins). Any failure leaves only the unix socket.
func (d *Daemon) startConfiguredListener() {
	raw := d.cfg.Listener.TCP
	if raw == "" {
		return
	}
	if d.audit == nil || d.tokens == nil {
		log.Printf("listener: the audit log or token store could not be opened — no TCP listener; the unix socket is unaffected")
		return
	}
	bound, err := d.startTCPListener(raw)
	if err != nil {
		log.Printf("listener: %v — no TCP listener; the unix socket is unaffected", err)
		return
	}
	log.Printf("listener: token-authenticated TCP listener on %s", bound)
}

// auditField makes a client-chosen value safe to keep: valid UTF-8 (so the
// JSON encoder cannot grow it past the cap by replacing bytes), then cut.
func auditField(s string) string {
	return truncateField(strings.ToValidUTF8(s, string(utf8.RuneError)), maxAuditField)
}

// writeAudit cuts every client-supplied field before the line is written: the
// pre-login frame cap bounds a frame, not a field.
func (d *Daemon) writeAudit(e auditEntry) {
	e.ClientID = auditField(e.ClientID)
	e.Kind = auditField(e.Kind)
	e.TokenID = auditField(e.TokenID)
	e.TokenName = auditField(e.TokenName)
	e.Rights = auditField(e.Rights)
	e.Type = auditField(e.Type)
	e.Reason = auditField(e.Reason)
	d.audit.write(e)
}

func (d *Daemon) onTCPAccepted(conn *ipc.Conn) {
	d.writeAudit(auditEntry{Event: "tcp_connect", Transport: ipc.TransportTCP})
	s := d.auth.begin(conn)
	s.arm(d.auth.step(), func() { d.loginTimedOut(conn, s, loginPending) })
}

// onTCPRejected is the ipc layer turning a TCP conn away before login: at
// accept (too many, conn nil) or for an oversized frame. It runs on the conn's
// dispatch goroutine, and the conn is closed as soon as it returns.
func (d *Daemon) onTCPRejected(reason string, conn *ipc.Conn) {
	if conn == nil {
		d.writeAudit(loginFailed(nil, nil, reason))
		return
	}
	// A logged-in conn's oversized frame ends that conn, but it is not a
	// failed login.
	if conn.Auth() != nil {
		return
	}
	s := d.auth.session(conn)
	if s == nil {
		d.writeAudit(loginFailed(nil, nil, reason))
		return
	}
	switch {
	case s.state.CompareAndSwap(loginPending, loginRefusing):
		// The first frame: no request ID exists yet, so there is nothing a
		// refusal frame could be matched to. Closed at once, with no frame.
		s.disarm()
		d.writeAudit(loginFailed(nil, s, reason))
	case s.state.CompareAndSwap(loginProofPending, loginRefusing):
		// The client is waiting on the hello's ID: tell it why.
		d.refuseLogin(conn, nil, s, reason, reason)
	default:
		// A refusal is already flushing on the timer's goroutine. The conn is
		// closed as soon as this returns, which would cut that flush short.
		select {
		case <-s.refused:
		case <-time.After(2 * refusalFlushTimeout):
		}
	}
}

func (d *Daemon) onTCPDisconnect(conn *ipc.Conn) {
	d.auth.forget(conn)
	d.auth.indexRemove(conn)
	e := auditEntry{Event: "tcp_disconnect", Transport: ipc.TransportTCP}
	if a := conn.Auth(); a != nil {
		e.TokenID, e.TokenName, e.Rights = a.TokenID, a.TokenName, a.Level
	}
	d.writeAudit(e)
}

// loginTimedOut is the step timer. It acts only if the conn is still in the
// state it was armed for: a frame that arrived first already moved it.
func (d *Daemon) loginTimedOut(conn *ipc.Conn, s *loginSession, from int32) {
	if !s.state.CompareAndSwap(from, loginRefusing) {
		return
	}
	d.refuseLogin(conn, nil, s, "login timeout", "timeout")
}

// handlePreLogin is the ONLY code an unauthenticated TCP conn reaches: not
// handleMessage's switch, not its log line. It never closes a conn itself:
// every close goes through refuseLogin, which flushes the refusal frame first.
func (d *Daemon) handlePreLogin(conn *ipc.Conn, msg *ipc.Message) {
	s := d.auth.session(conn)
	if s == nil {
		// Not reachable: acceptTCP runs onTCPAccepted (which begins the
		// session) before the conn's first read, and a refused session stays
		// registered until onTCPDisconnect. Fail closed through the ordinary
		// refusal — frame, flush, close.
		s = d.auth.begin(conn)
		s.state.Store(loginRefusing)
		d.refuseLogin(conn, msg, s, "login required", "login required")
		return
	}
	switch {
	case s.state.CompareAndSwap(loginPending, loginChecking):
		s.disarm()
		d.loginHello(conn, s, msg)
	case s.state.CompareAndSwap(loginProofPending, loginChecking):
		s.disarm()
		d.loginProof(conn, s, msg)
	}
	// Any other state: loginRefusing (the timer or a bad frame refused this
	// conn and the refusal is flushing) — the frame is ignored and the conn
	// is left to refuseLogin's own Close. loginChecking and loginDone are
	// unreachable here: one conn's frames are dispatched one at a time on its
	// own goroutine, so no frame arrives while one is being checked, and a
	// done conn is authenticated and never routed to this function.
}

func (d *Daemon) loginHello(conn *ipc.Conn, s *loginSession, msg *ipc.Message) {
	if msg.Type != ipc.MsgHello || msg.ID == "" {
		d.refuseLogin(conn, msg, s, "login required", "login required")
		return
	}
	var p ipc.HelloPayload
	// The token id is checked for its exact shape here, before the store is
	// asked: AuthMessage joins the id and both nonces with commas, so its
	// framing is only unambiguous for an id that cannot contain one.
	if err := msg.DecodePayload(&p); err != nil || p.Kind == "" || p.Proto < 1 ||
		!clientauth.ValidID(p.TokenID) || !clientauth.ValidNonce(p.Nonce) {
		d.refuseLogin(conn, msg, s, "login required", "login required")
		return
	}
	nonceS, err := clientauth.NewNonce()
	if err != nil {
		log.Printf("login: server nonce: %v", err)
		d.refuseLogin(conn, msg, s, "login required", "internal error")
		return
	}
	s.hello = msg
	s.clientID = auditField(p.ClientID)
	s.kind = auditField(p.Kind)
	s.tokenID = p.TokenID
	s.nonceC, s.nonceS = p.Nonce, nonceS
	// An unknown token id is challenged exactly like a known one (dummy key
	// in Store.Admit), so the answer does not reveal which ids exist.
	respondTo(conn, msg.ID, ipc.MsgAuthChallenge, ipc.AuthChallengePayload{Nonce: nonceS})
	s.state.Store(loginProofPending)
	s.arm(d.auth.step(), func() { d.loginTimedOut(conn, s, loginProofPending) })
}

func (d *Daemon) loginProof(conn *ipc.Conn, s *loginSession, msg *ipc.Message) {
	if msg.Type != ipc.MsgAuthProof || msg.ID != s.hello.ID {
		d.refuseLogin(conn, msg, s, "login required", "login required")
		return
	}
	var p ipc.AuthProofPayload
	if err := msg.DecodePayload(&p); err != nil {
		d.refuseLogin(conn, msg, s, "token refused", "token refused")
		return
	}
	// The guessing delay runs AFTER the frame arrived, with the deadline
	// already cleared, so it can never push a correct login past it.
	if wait := d.auth.backoff(); wait > 0 {
		d.auth.wait(wait)
	}
	if d.tokens == nil {
		d.refuseLogin(conn, msg, s, "token refused", "token store unavailable")
		return
	}
	authMsg := clientauth.AuthMessage(s.tokenID, s.nonceC, s.nonceS)
	// ONE lock: validate, index, MarkAuthenticated. A revoke holds the same
	// lock, so it either finds this conn in the index or runs before the
	// lookup and leaves nothing to admit.
	entry, verifier, err := d.tokens.Admit(s.tokenID, time.Now(),
		func(v clientauth.Verifier) bool { return clientauth.VerifyProof(v, authMsg, p.Proof) },
		func(e clientauth.Entry) {
			d.auth.indexAdd(e.ID, conn)
			conn.MarkAuthenticated(ipc.NewTokenAuth(e.ID, e.Name, string(e.Rights)))
		})
	if err != nil {
		// Unknown, wrong and expired all read the same: the reason must not
		// tell someone without the key which of them it was.
		d.auth.failures.Add(1)
		d.refuseLogin(conn, msg, s, "token refused", "token refused")
		return
	}
	d.auth.failures.Store(0)
	s.state.Store(loginDone)
	d.auth.forget(conn)
	d.touchLastUsed(entry.ID)
	d.writeAudit(auditEntry{
		Event: "login_ok", Transport: ipc.TransportTCP, ClientID: s.clientID, Kind: s.kind,
		TokenID: entry.ID, TokenName: entry.Name, Rights: string(entry.Rights),
	})
	d.answerHello(conn, s.hello, ipc.HelloRespPayload{
		Rights:    string(entry.Rights),
		TokenName: entry.Name,
		ServerSig: clientauth.ServerSignature(verifier, authMsg),
	})
}

// loginFailed is the audit line of a refused login. It never depends on the
// hello registry: a refused conn was never registered there.
func loginFailed(msg *ipc.Message, s *loginSession, auditReason string) auditEntry {
	e := auditEntry{Event: "login_failed", Transport: ipc.TransportTCP, Reason: auditReason}
	if msg != nil {
		e.Type = msg.Type
	}
	if s != nil {
		e.ClientID, e.Kind, e.TokenID = s.clientID, s.kind, s.tokenID
	}
	return e
}

// refuseLogin sends `error refused` DIRECTLY (the conn is not in the hello
// registry, so replyError's proto gate does not apply), flushes for up to
// 1 s, then closes. After a hello the frame carries the hello's ID, whatever
// frame or timer caused the refusal.
//
// The session is NOT forgotten here: it stays registered in loginRefusing
// until onTCPDisconnect, so a frame dispatched during the flush finds it and
// is ignored. Forgetting it here let that frame find no session and close the
// conn mid-flush, cutting the refusal frame off.
func (d *Daemon) refuseLogin(conn *ipc.Conn, msg *ipc.Message, s *loginSession, reason, auditReason string) {
	typ := ""
	if msg != nil {
		typ = auditField(msg.Type)
	}
	if s != nil {
		s.state.Store(loginRefusing)
		s.disarm()
		defer s.refusedOnce.Do(func() { close(s.refused) })
	}
	d.writeAudit(loginFailed(msg, s, auditReason))
	sendError(conn, s.requestID(msg), typ, ipc.ErrCodeRefused, reason)
	if hook := d.auth.beforeRefusalFlush.Load(); hook != nil {
		(*hook)()
	}
	conn.Flush(refusalFlushTimeout)
	conn.Close()
}

// touchLastUsed updates last_used in memory; the store asks for a write at
// most once a minute per token, done on a worker.
func (d *Daemon) touchLastUsed(id string) {
	if d.tokens == nil || !d.tokens.TouchLastUsed(id, time.Now()) {
		return
	}
	d.auth.goPersist(func() {
		if err := d.tokens.Persist(); err != nil {
			log.Printf("tokens: write last_used: %v", err)
		}
	})
}
