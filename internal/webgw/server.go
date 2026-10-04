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
	"sync"
	"time"

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
)

var errTooManyTabs = errors.New("too many browser tabs")

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

	// limits and lease are fields so tests can shrink them.
	limits bridgeLimits
	lease  time.Duration

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	tabs    map[string]*tab // live and held, by client id
	dialing int             // tabs being dialled; they count toward the limit
	stopped bool
	srv     *http.Server
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
		cfg:    cfg,
		auth:   newAuthStore(cfg.Rand, cfg.Sleep),
		leases: newLeases(cfg.Rand),
		budget: newReplayBudget(replayTotalMax),
		limits: defaultBridgeLimits(cfg.Version),
		lease:  resyncLease,
		tabs:   map[string]*tab{},
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

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	session := sessionOf(r)
	if !s.auth.Valid(session) {
		s.cfg.Logf("websocket refused: no session")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if !originAllowed(r.Header.Get("Origin"), r.Host) {
		s.cfg.Logf("websocket refused: foreign origin")
		w.WriteHeader(http.StatusForbidden)
		return
	}
	s.mu.Lock()
	full := len(s.tabs)+s.dialing >= maxBridges
	stopped := s.stopped
	s.mu.Unlock()
	if full || stopped {
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
		return
	}
	c.SetReadLimit(pageFrameMax)
	s.serveTab(r.Context(), c, session)
}

func (s *Server) serveTab(parent context.Context, c *websocket.Conn, session string) {
	ctx, cancel := context.WithCancel(parent)
	mine := &sockRef{cancel: cancel, done: make(chan struct{})}
	defer func() {
		cancel()
		close(mine.done)
	}()
	page := wsPage{c}

	// The first frame is web_open with the id the page had and its key.
	rctx, rcancel := context.WithTimeout(ctx, openTimeout)
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

	t, old, err := s.tabFor(ctx, open.ClientIDHint, session, mine)
	if err != nil {
		code := CloseDaemonUnavailable
		switch {
		case errors.Is(err, ErrTokenRefused):
			code = CloseTokenRefused
		case errors.Is(err, ErrVersionMismatch):
			code = CloseVersionMismatch
		case errors.Is(err, errTooManyTabs):
			code = int(websocket.StatusTryAgainLater)
		}
		s.cfg.Logf("tab not opened: %v", err)
		page.Close(code, closeReason(code))
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

	t.b.setLease(t.id)
	welcome, err := ipc.NewMessage(MsgWebWelcome, WebWelcomePayload{ClientID: t.id, Rights: t.rights, Version: s.cfg.Version})
	var wb []byte
	if err == nil {
		wb, err = json.Marshal(welcome)
	}
	if err != nil {
		t.b.close(CloseGoingAway, "page closed")
		return
	}
	gen, err := t.b.attachPageFirst(page, wb)
	if err != nil {
		page.Close(CloseDaemonUnavailable, closeReason(CloseDaemonUnavailable))
		return
	}
	s.cfg.Logf("tab opened: %s (%s)", t.id, t.rights)

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
			s.cfg.Logf("tab %s: %v", t.id, err)
		}
	}
}

// tabFor returns the tab for a page that offered hint. A held bridge of the
// same session and id is reclaimed (its previous socket is returned so the
// caller can stop it); anything else dials a new daemon connection.
func (s *Server) tabFor(ctx context.Context, hint, session string, mine *sockRef) (t *tab, old *sockRef, err error) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil, nil, errors.New("the web server is stopping")
	}
	if h := s.tabs[hint]; h != nil && h.held && h.session == session {
		h.held = false
		if h.timer != nil {
			h.timer.Stop()
		}
		old, h.sock = h.sock, mine
		s.mu.Unlock()
		return h, old, nil
	}
	if len(s.tabs)+s.dialing >= maxBridges {
		s.mu.Unlock()
		return nil, nil, errTooManyTabs
	}
	s.dialing++
	s.mu.Unlock()

	id := s.leases.acquire(hint, session)
	// Shutdown cancels a dial in flight as well as the page going away.
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	stopWatch := context.AfterFunc(s.ctx, cancel)
	conn, rights, err := s.cfg.Dial(dctx, id)
	stopWatch()
	cancel()
	s.mu.Lock()
	s.dialing--
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
	s.tabs[id] = t
	s.mu.Unlock()

	go func() {
		b.run(s.ctx)
		b.close(CloseGoingAway, "web server stopped")
		s.mu.Lock()
		if s.tabs[id] == t {
			delete(s.tabs, id)
		}
		if t.timer != nil {
			t.timer.Stop()
		}
		t.held = false
		s.mu.Unlock()
		s.leases.release(id)
	}()
	return t, nil, nil
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

// Shutdown detaches every tab, live or held, and stops serving. It waits for
// the detaches no longer than ctx allows.
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
			t.b.close(CloseGoingAway, "the web server stopped")
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
