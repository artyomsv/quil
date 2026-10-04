package webgw

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/coder/websocket"
)

// Dialer opens one browser tab's daemon connection, logged in under clientID
// in token mode. It returns the rights for web_welcome ("full" locally).
// Errors wrapping ErrTokenRefused or ErrVersionMismatch are permanent.
type Dialer func(ctx context.Context, clientID string) (DaemonConn, string, error)

var (
	ErrTokenRefused    = errors.New("token refused")
	ErrVersionMismatch = errors.New("version mismatch")
)

// errTabsFull refuses a socket let in over the cap to reclaim a held tab
// when it asks for anything else.
var errTabsFull = fmt.Errorf("%d browser tabs already open", maxBridges)

const (
	maxBridges        = 16
	resyncLease       = 10 * time.Second
	pingEvery         = 20 * time.Second
	pongBudget        = 60 * time.Second
	pageFrameMax      = 1 << 20
	replayTotalMax    = 128 << 20
	dialTimeout       = 15 * time.Second
	openTimeout       = 10 * time.Second
	readHeaderTimeout = 10 * time.Second
	// closeLoginRequired is the policy-violation close a page gets when its
	// key does not match its session; the page answers it with the login form.
	closeLoginRequired = int(websocket.StatusPolicyViolation)
	// maxCloseReason is the most a WebSocket close reason can carry: a control
	// frame holds 125 bytes and the close code takes two of them.
	maxCloseReason = 123
)

type Config struct {
	Dial    Dialer
	Version string               // shown in web_welcome
	Logf    func(string, ...any) // web.log
	Rand    io.Reader            // crypto/rand in production
	Now     func() time.Time
	Sleep   func(time.Duration)
}

// sockRef is the WebSocket currently serving a tab: cancel stops its reader
// and done closes once that reader has returned.
type sockRef struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// tab is one browser tab's bridge and who owns it. held is true between a
// resync and the page's return (or the lease running out).
type tab struct {
	id      string
	b       *bridge
	session string
	rights  string

	// guarded by Server.mu
	sock  *sockRef
	held  bool
	timer *time.Timer
	// holdGen numbers the holds so an old timer cannot close a newer one.
	holdGen uint64
}

type Server struct {
	cfg    Config
	auth   *authStore
	leases *leases
	budget *replayBudget
	mux    *http.ServeMux

	// limits, lease and openWait are fields so tests can shrink them.
	limits   bridgeLimits
	lease    time.Duration
	openWait time.Duration

	ctx    context.Context
	cancel context.CancelFunc

	mu   sync.Mutex
	tabs map[string]*tab // live and held, by client id
	// pending counts sockets accepted and not yet a tab: waiting for web_open
	// or being dialled. They count toward the limit.
	pending int
	// reclaimPending counts, per session, sockets let in over the limit to
	// reclaim a held tab and not yet past tabFor. A session gets at most one
	// per tab held for it.
	reclaimPending map[string]int
	stopped        bool
	srv            *http.Server
}

func New(cfg Config) *Server {
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.Reader
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = time.Sleep
	}
	s := &Server{
		cfg:      cfg,
		auth:     newAuthStore(cfg.Rand, cfg.Sleep),
		leases:   newLeases(cfg.Rand),
		budget:   newReplayBudget(replayTotalMax),
		limits:   defaultBridgeLimits(cfg.Version),
		lease:    resyncLease,
		openWait: openTimeout,
		tabs:     map[string]*tab{},

		reclaimPending: map[string]int{},
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.mux = http.NewServeMux()
	s.mux.Handle("/", StaticHandler())
	s.mux.HandleFunc("/login", loginHandler(s.auth, cfg.Logf))
	s.mux.HandleFunc("/session", sessionHandler(s.auth))
	s.mux.HandleFunc("/ws", s.handleWS)
	return s
}

// Handler serves every route behind the host check and the security headers.
func (s *Server) Handler() http.Handler { return withSecurity(s.mux) }

// NewCode replaces any unused login code with a fresh one.
func (s *Server) NewCode() (string, error) { return s.auth.NewCode() }

// Serve serves HTTP on l until Shutdown. A client that is slow to send its
// request headers is dropped after readHeaderTimeout.
func (s *Server) Serve(l net.Listener) error {
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: readHeaderTimeout}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return http.ErrServerClosed
	}
	s.srv = srv
	s.mu.Unlock()
	return srv.Serve(l)
}

// handleWS checks Origin and then the cookie (Host was checked by
// withSecurity), and takes a pending slot before the upgrade: a socket that
// never sends web_open still holds one of the 16 places until it goes.
//
// When every place is taken, a session that holds a resynced tab is still let
// in, without a slot: its page is coming back for that tab, and reclaiming it
// adds none. Until web_open names the tab this cannot be known, so tabFor
// makes the final decision and refuses such a socket anything else. Such
// sockets are bounded too: one waiting per tab held for the session.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !originAllowed(r.Header.Get("Origin"), r.Host) {
		s.cfg.Logf("websocket refused: foreign origin")
		w.WriteHeader(http.StatusForbidden)
		return
	}
	session := sessionOf(r)
	if !s.auth.Valid(session) {
		s.cfg.Logf("websocket refused: no session")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	reserved := len(s.tabs)+s.pending < maxBridges
	admit := !s.stopped && (reserved || s.heldTabsLocked(session) > s.reclaimPending[session])
	if admit {
		s.acquireLocked(reserved, session)
	}
	s.mu.Unlock()
	if !admit {
		s.cfg.Logf("websocket refused: %d browser tabs already open", maxBridges)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	// Origin was checked above; InsecureSkipVerify only stops the library from
	// repeating a stricter check of its own against our exact-match rule.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionNoContextTakeover,
	})
	if err != nil {
		s.release(reserved, session)
		return
	}
	c.SetReadLimit(pageFrameMax)
	s.serveTab(r.Context(), c, session, reserved)
}

// heldTabsLocked counts the resynced tabs held for session. Caller holds s.mu.
func (s *Server) heldTabsLocked(session string) int {
	n := 0
	for _, t := range s.tabs {
		if t.held && t.session == session {
			n++
		}
	}
	return n
}

// acquireLocked takes a socket's place: a pending slot when reserved, else
// one of its session's reclaim places. Caller holds s.mu.
func (s *Server) acquireLocked(reserved bool, session string) {
	if reserved {
		s.pending++
		return
	}
	s.reclaimPending[session]++
}

// releaseLocked gives back what acquireLocked took. Caller holds s.mu.
func (s *Server) releaseLocked(reserved bool, session string) {
	if reserved {
		s.pending--
		return
	}
	if s.reclaimPending[session] <= 1 {
		delete(s.reclaimPending, session)
		return
	}
	s.reclaimPending[session]--
}

func (s *Server) release(reserved bool, session string) {
	s.mu.Lock()
	s.releaseLocked(reserved, session)
	s.mu.Unlock()
}

// serveTab runs one socket. It holds the place handleWS took (a pending slot
// when reserved, else a reclaim place) until it hands it to tabFor, which
// gives it back on every path.
func (s *Server) serveTab(parent context.Context, c *websocket.Conn, session string, reserved bool) {
	ctx, cancel := context.WithCancel(parent)
	mine := &sockRef{cancel: cancel, done: make(chan struct{})}
	holding := true
	defer func() {
		if holding {
			s.release(reserved, session)
		}
		cancel()
		close(mine.done)
	}()
	page := wsPage{c}

	// The first frame is web_open with the id the page had and its key.
	rctx, rcancel := context.WithTimeout(ctx, s.openWait)
	typ, raw, err := c.Read(rctx)
	rcancel()
	if err != nil || typ != websocket.MessageText {
		page.Close(int(websocket.StatusPolicyViolation), "expected web_open")
		return
	}
	var m ipc.Message
	var open WebOpenPayload
	if json.Unmarshal(raw, &m) != nil || m.Type != MsgWebOpen || json.Unmarshal(m.Payload, &open) != nil {
		page.Close(int(websocket.StatusPolicyViolation), "expected web_open")
		return
	}
	// The cookie is sent to every port on this host; the key is not. Nothing
	// dials the daemon for a page that cannot show the key.
	if !s.auth.KeyValid(session, open.Key) {
		s.cfg.Logf("tab refused: login required")
		page.Close(closeLoginRequired, "login required")
		return
	}

	holding = false
	t, old, err := s.tabFor(ctx, open.ClientIDHint, session, mine, reserved)
	if err != nil {
		code := CloseDaemonUnavailable
		switch {
		case errors.Is(err, ErrTokenRefused):
			code = CloseTokenRefused
		case errors.Is(err, ErrVersionMismatch):
			code = CloseVersionMismatch
		}
		s.cfg.Logf("tab not opened: %v", err)
		page.Close(code, s.dialCloseReason(code, err))
		return
	}
	// A page re-attaching on a held bridge: its previous socket's reader must
	// be gone before the new page attaches.
	if old != nil {
		old.cancel()
		select {
		case <-old.done:
		case <-ctx.Done():
		}
	}

	id := s.idOf(t)
	t.b.setLease(id)
	wb, err := s.welcomeFrame(id, t.rights)
	if err != nil {
		t.b.close(CloseGoingAway, "page closed")
		return
	}
	gen, err := t.b.attachPageFirst(page, wb)
	if err != nil {
		page.Close(CloseDaemonUnavailable, closeReason(CloseDaemonUnavailable))
		return
	}
	s.cfg.Logf("tab opened: %s (%s)", id, t.rights)

	go s.pinger(ctx, cancel, c)
	for {
		typ, raw, err := c.Read(ctx)
		if err != nil {
			t.b.closePage(gen, CloseGoingAway, "page closed")
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		if err := t.b.fromPage(gen, raw); err != nil {
			s.cfg.Logf("tab %s: %v", id, err)
		}
	}
}

// idOf reads a tab's client id, which renewID can change.
func (s *Server) idOf(t *tab) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return t.id
}

func (s *Server) welcomeFrame(id, rights string) ([]byte, error) {
	m, err := ipc.NewMessage(MsgWebWelcome, WebWelcomePayload{ClientID: id, Rights: rights, Version: s.cfg.Version})
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// tabFor returns the tab for a page that offered hint. A held bridge of the
// same session and id is reclaimed (its previous socket is returned so the
// caller can stop it); anything else dials a new daemon connection. The
// caller's pending slot is given back on every path: a reclaimed tab is
// already counted, and a dialled one is counted as a tab from then on.
//
// A socket handleWS let in without a slot (reserved false) gives its reclaim
// place back at once, under the same lock as the reclaim itself. It may
// reclaim, and may dial only if a place is free now, which it then takes like
// any other.
func (s *Server) tabFor(ctx context.Context, hint, session string, mine *sockRef, reserved bool) (t *tab, old *sockRef, err error) {
	s.mu.Lock()
	if !reserved {
		s.releaseLocked(false, session)
	}
	if s.stopped {
		if reserved {
			s.pending--
		}
		s.mu.Unlock()
		return nil, nil, errors.New("the web server is stopping")
	}
	if h := s.tabs[hint]; h != nil && h.held && h.session == session {
		if reserved {
			s.pending--
		}
		h.held = false
		if h.timer != nil {
			h.timer.Stop()
		}
		old, h.sock = h.sock, mine
		s.mu.Unlock()
		return h, old, nil
	}
	if !reserved {
		if len(s.tabs)+s.pending >= maxBridges {
			s.mu.Unlock()
			return nil, nil, errTabsFull
		}
		s.pending++
	}
	s.mu.Unlock()

	id := s.leases.acquire(hint, session)
	// Shutdown cancels a dial in flight as well as the page going away.
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	stopWatch := context.AfterFunc(s.ctx, cancel)
	conn, rights, err := s.cfg.Dial(dctx, id)
	stopWatch()
	cancel()
	s.mu.Lock()
	s.pending--
	stopping := err == nil && s.stopped
	if err != nil || stopping {
		s.mu.Unlock()
		if stopping {
			_ = conn.Close()
			err = errors.New("the web server is stopping")
		}
		s.leases.release(id)
		return nil, nil, err
	}
	b := newBridge(conn, s.limits, s.budget, s.cfg.Now, func(f string, a ...any) {
		s.cfg.Logf("tab "+id+": "+f, a...)
	})
	t = &tab{id: id, b: b, session: session, rights: rights, sock: mine}
	b.onResync = func() { s.hold(t) }
	b.onIDInUse = func() { s.renewID(t) }
	s.tabs[id] = t
	s.mu.Unlock()

	go s.runTab(t)
	return t, nil, nil
}

// runTab reads the tab's daemon connection until it fails or the server
// stops, then takes the tab out of the table. On a stop the close is left to
// Shutdown, which waits for the page's 1001; closing here would race it and
// let Shutdown return before that close was written. Every tab live at the
// stop is Shutdown's to close: tabFor adds none once stopped is set.
func (s *Server) runTab(t *tab) {
	b := t.b
	b.run(s.ctx)
	if s.ctx.Err() == nil {
		b.close(CloseGoingAway, "web server stopped")
	}
	s.mu.Lock()
	cur := t.id
	if s.tabs[cur] == t {
		delete(s.tabs, cur)
	}
	if t.timer != nil {
		t.timer.Stop()
	}
	t.held = false
	s.mu.Unlock()
	s.leases.release(cur)
}

// renewID gives a tab whose client id the daemon refused as in use (another
// principal holds it) a freshly minted id, and sends its page a new
// web_welcome. The page then says hello and attaches again under that id.
func (s *Server) renewID(t *tab) {
	s.mu.Lock()
	if s.stopped || s.tabs[t.id] != t {
		s.mu.Unlock()
		return
	}
	old := t.id
	id := s.leases.renew(old, t.session)
	delete(s.tabs, old)
	t.id = id
	s.tabs[id] = t
	s.mu.Unlock()
	t.b.relabel(id)
	s.cfg.Logf("tab %s: client id in use; renewed as %s", old, id)
	if wb, err := s.welcomeFrame(id, t.rights); err == nil {
		t.b.enqueueControl(wb)
	}
}

// hold keeps a resynced bridge for the lease so its page can come back on the
// same daemon connection; after that the bridge detaches and closes.
func (s *Server) hold(t *tab) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.tabs[t.id] != t {
		return
	}
	t.held = true
	t.holdGen++
	gen := t.holdGen
	t.timer = time.AfterFunc(s.lease, func() { s.expire(t, gen) })
}

// expire closes a tab whose hold number gen is still the current one; a timer
// left over from an earlier hold (the page came back and was resynced again)
// acts on nothing.
func (s *Server) expire(t *tab, gen uint64) {
	s.mu.Lock()
	if !t.held || t.holdGen != gen {
		s.mu.Unlock()
		return
	}
	t.held = false
	s.mu.Unlock()
	t.b.close(CloseGoingAway, "lease expired")
}

// pinger pings the page every pingEvery and drops the socket when no pong
// comes within pongBudget; the reader then sees the failure and ends the tab.
func (s *Server) pinger(ctx context.Context, drop context.CancelFunc, c *websocket.Conn) {
	tick := time.NewTicker(pingEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		pctx, cancel := context.WithTimeout(ctx, pongBudget)
		err := c.Ping(pctx)
		cancel()
		if err != nil {
			drop()
			return
		}
	}
}

// Shutdown detaches every tab, live or held, closes every page with 1001 and
// stops serving. It waits for the detaches and the page closes no longer than
// ctx allows. The page close is waited for here because http.Server.Shutdown
// does not wait for hijacked WebSocket connections: the process could exit
// before the 1001 was written, and the page would read 1006 and retry a port
// nobody listens on.
func (s *Server) Shutdown(ctx context.Context) {
	s.mu.Lock()
	s.stopped = true
	tabs := make([]*tab, 0, len(s.tabs))
	for _, t := range s.tabs {
		tabs = append(tabs, t)
		if t.timer != nil {
			t.timer.Stop()
		}
	}
	srv := s.srv
	s.mu.Unlock()
	s.cancel()

	var wg sync.WaitGroup
	for _, t := range tabs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t.b.closeWait(CloseGoingAway, "the web server stopped")
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	if srv != nil {
		_ = srv.Shutdown(ctx)
	}
}

func closeReason(code int) string {
	switch code {
	case CloseResync:
		return "resync"
	case CloseTooSlow:
		return "too slow"
	case CloseDaemonUnavailable:
		return "daemon unavailable"
	case CloseTokenRefused:
		return "token refused"
	case CloseVersionMismatch:
		return "version mismatch"
	case CloseByAgent:
		return "closed by an agent"
	case CloseGoingAway:
		return "going away"
	}
	return fmt.Sprintf("closed (%d)", code)
}

// dialCloseReason is the close reason for a tab whose dial failed: what went
// wrong, without the sentinel's own words (the page names the code), and for
// a version mismatch both versions.
func (s *Server) dialCloseReason(code int, err error) string {
	detail := err.Error()
	for _, sentinel := range []error{ErrTokenRefused, ErrVersionMismatch} {
		if errors.Is(err, sentinel) {
			detail = strings.TrimPrefix(detail, sentinel.Error()+": ")
		}
	}
	if code == CloseVersionMismatch && s.cfg.Version != "" {
		detail += "; quil web is " + s.cfg.Version
	}
	return reasonText(detail, code)
}

// reasonText makes text fit a close frame, or falls back to the code's
// fixed reason when nothing printable is left.
func reasonText(text string, code int) string {
	if r := printable(text, maxCloseReason); r != "" {
		return r
	}
	return closeReason(code)
}

// printable keeps the printable runes of s and drops the rest (control and
// format characters, bidi overrides, invalid UTF-8), cutting the result to at
// most max bytes on a rune boundary. Text from the daemon or the page goes
// through it before it reaches a close frame or the log.
func printable(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > max {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}
