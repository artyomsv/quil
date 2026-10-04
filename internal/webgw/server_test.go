package webgw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/coder/websocket"
)

// logSink collects gateway log lines; a test's own t.Logf would panic when a
// tab goroutine logs after the test returned.
type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSink) logf(f string, a ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(f, a...))
	l.mu.Unlock()
}

func (l *logSink) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

type wsHarness struct {
	t     *testing.T
	s     *Server
	ts    *httptest.Server
	host  string
	logs  *logSink
	dials atomic.Int32

	mu       sync.Mutex
	daemons  []*fakeDaemon
	ids      []string
	dialErr  error
	dialHook func(ctx context.Context) error
}

func newWSHarness(t *testing.T, tune func(*Server)) *wsHarness {
	t.Helper()
	h := &wsHarness{t: t, logs: &logSink{}}
	h.s = New(Config{Dial: h.dial, Version: "9.9.9", Logf: h.logs.logf, Sleep: func(time.Duration) {}})
	if tune != nil {
		tune(h.s)
	}
	h.ts = httptest.NewServer(h.s.Handler())
	h.host = h.ts.Listener.Addr().String()
	t.Cleanup(h.ts.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.s.Shutdown(ctx)
	})
	return h
}

func (h *wsHarness) dial(ctx context.Context, id string) (DaemonConn, string, error) {
	h.dials.Add(1)
	h.mu.Lock()
	hook := h.dialHook
	h.mu.Unlock()
	if hook != nil {
		if err := hook(ctx); err != nil {
			return nil, "", err
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dialErr != nil {
		return nil, "", h.dialErr
	}
	d := newFakeDaemon()
	h.daemons = append(h.daemons, d)
	h.ids = append(h.ids, id)
	return d, "full", nil
}

func (h *wsHarness) daemon(i int) *fakeDaemon {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.daemons[i]
}

func (h *wsHarness) origin() string { return "http://" + h.host }

type session struct {
	cookie string
	key    string
	code   string
}

func (h *wsHarness) login() session {
	h.t.Helper()
	code, err := h.s.NewCode()
	if err != nil {
		h.t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"code": code})
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/login", bytes.NewReader(body))
	req.Header.Set("Origin", h.origin())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("login: %s", resp.Status)
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatal(err)
	}
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookie {
			return session{cookie: c.Value, key: out.Key, code: code}
		}
	}
	h.t.Fatal("login set no cookie")
	return session{}
}

func (h *wsHarness) header(cookie, origin string) http.Header {
	hdr := http.Header{}
	if origin != "" {
		hdr.Set("Origin", origin)
	}
	if cookie != "" {
		hdr.Set("Cookie", SessionCookie+"="+cookie)
	}
	return hdr
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// connect upgrades /ws and registers the cleanup.
func (h *wsHarness) connect(ctx context.Context, cookie string) (*websocket.Conn, *http.Response, error) {
	c, resp, err := websocket.Dial(ctx, "ws://"+h.host+"/ws", &websocket.DialOptions{HTTPHeader: h.header(cookie, h.origin())})
	if err == nil {
		h.t.Cleanup(func() { c.CloseNow() })
	}
	return c, resp, err
}

func sendMsg(t *testing.T, ctx context.Context, c *websocket.Conn, typ, id string, payload any) {
	t.Helper()
	m, err := ipc.NewMessage(typ, payload)
	if err != nil {
		t.Fatal(err)
	}
	m.ID = id
	raw, _ := json.Marshal(m)
	if err := c.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatal(err)
	}
}

func readMsg(t *testing.T, ctx context.Context, c *websocket.Conn) *ipc.Message {
	t.Helper()
	typ, raw, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("expected a text frame, got binary")
	}
	var m ipc.Message
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return &m
}

// closeOf reads until the socket fails and returns its close code and reason.
func closeOf(t *testing.T, ctx context.Context, c *websocket.Conn) (int, string) {
	t.Helper()
	for {
		if _, _, err := c.Read(ctx); err != nil {
			var ce websocket.CloseError
			if !errors.As(err, &ce) {
				t.Fatalf("socket failed without a close frame: %v", err)
			}
			return int(ce.Code), ce.Reason
		}
	}
}

// open connects, sends web_open and reads the welcome.
func (h *wsHarness) open(ctx context.Context, s session, hint string) (*websocket.Conn, WebWelcomePayload) {
	h.t.Helper()
	c, _, err := h.connect(ctx, s.cookie)
	if err != nil {
		h.t.Fatalf("connect: %v", err)
	}
	sendMsg(h.t, ctx, c, MsgWebOpen, "", WebOpenPayload{ClientIDHint: hint, Key: s.key})
	m := readMsg(h.t, ctx, c)
	if m.Type != MsgWebWelcome {
		h.t.Fatalf("first frame is %s, want web_welcome", m.Type)
	}
	var w WebWelcomePayload
	if err := m.DecodePayload(&w); err != nil {
		h.t.Fatal(err)
	}
	return c, w
}

func webHello(id string) ipc.HelloPayload {
	return ipc.HelloPayload{Kind: "web", Proto: ipc.ProtocolVersion, ClientID: id}
}

func TestWS_NoSessionRefusedBeforeDial(t *testing.T) {
	h := newWSHarness(t, nil)
	ctx := testCtx(t)
	s := h.login()

	_, resp, err := websocket.Dial(ctx, "ws://"+h.host+"/ws", &websocket.DialOptions{HTTPHeader: h.header("", h.origin())})
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no cookie: err=%v resp=%v, want 401", err, resp)
	}
	_, resp, err = websocket.Dial(ctx, "ws://"+h.host+"/ws", &websocket.DialOptions{HTTPHeader: h.header(s.cookie, "http://evil.example")})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: err=%v resp=%v, want 403", err, resp)
	}
	_, resp, err = websocket.Dial(ctx, "ws://"+h.host+"/ws", &websocket.DialOptions{HTTPHeader: h.header(s.cookie, "")})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no origin: err=%v resp=%v, want 403", err, resp)
	}
	if n := h.dials.Load(); n != 0 {
		t.Fatalf("daemon dialled %d times for refused sockets", n)
	}
}

func TestWS_WrongOrMissingKeyNeverDials(t *testing.T) {
	h := newWSHarness(t, nil)
	ctx := testCtx(t)
	s := h.login()
	for name, key := range map[string]string{"missing": "", "wrong": "not-the-key", "other session's": h.login().key} {
		c, _, err := h.connect(ctx, s.cookie)
		if err != nil {
			t.Fatal(err)
		}
		sendMsg(t, ctx, c, MsgWebOpen, "", WebOpenPayload{Key: key})
		code, reason := closeOf(t, ctx, c)
		if code != 1008 || reason != "login required" {
			t.Fatalf("%s key: closed %d %q, want 1008 \"login required\"", name, code, reason)
		}
	}
	if n := h.dials.Load(); n != 0 {
		t.Fatalf("daemon dialled %d times for a page without the key", n)
	}
}

func TestWS_OpenWelcomeAndForward(t *testing.T) {
	h := newWSHarness(t, nil)
	ctx := testCtx(t)
	c, w := h.open(ctx, h.login(), "")
	if !strings.HasPrefix(w.ClientID, "web-"+h.s.leases.prefix()+"-") || w.Rights != "full" || w.Version != "9.9.9" {
		t.Fatalf("welcome = %+v", w)
	}
	d := h.daemon(0)

	sendMsg(t, ctx, c, ipc.MsgHello, "h1", webHello(w.ClientID))
	waitFor(t, "hello at the daemon", func() bool { return strings.Join(d.sentTypes(), ",") == ipc.MsgHello })

	sendMsg(t, ctx, c, ipc.MsgTokenCreateReq, "t1", struct{}{})
	m := readMsg(t, ctx, c)
	var ep ipc.ErrorPayload
	if m.Type != ipc.MsgError || m.ID != "t1" || m.DecodePayload(&ep) != nil || ep.Code != ipc.ErrCodeRefused {
		t.Fatalf("got %s %s, want error refused for t1", m.Type, m.Payload)
	}
	if got := d.sentTypes(); len(got) != 1 {
		t.Fatalf("daemon received %v, want only the hello", got)
	}
}

// pushLive makes the fake daemon print more live output than the test's cap.
func pushLive(t *testing.T, d *fakeDaemon) {
	t.Helper()
	d.in <- outputMsg(t, "p1", bytes.Repeat([]byte("x"), 100), false, 1)
}

func smallLiveCap(s *Server) { s.limits.LiveUnackedMax = 10 }

func TestWS_ResyncReattachesOnTheSameDaemonConn(t *testing.T) {
	h := newWSHarness(t, smallLiveCap)
	ctx := testCtx(t)
	s := h.login()
	c, w := h.open(ctx, s, "")
	d := h.daemon(0)

	pushLive(t, d)
	if code, _ := closeOf(t, ctx, c); code != CloseResync {
		t.Fatalf("closed %d, want 4001", code)
	}

	c2, w2 := h.open(ctx, s, w.ClientID)
	if w2.ClientID != w.ClientID {
		t.Fatalf("re-attached under %s, want %s", w2.ClientID, w.ClientID)
	}
	if n := h.dials.Load(); n != 1 {
		t.Fatalf("daemon dialled %d times, want 1", n)
	}
	sendMsg(t, ctx, c2, ipc.MsgHello, "h2", webHello(w2.ClientID))
	waitFor(t, "hello on the kept connection", func() bool { return strings.Join(d.sentTypes(), ",") == ipc.MsgHello })
	if d.isClosed() {
		t.Fatal("the kept daemon connection was closed")
	}
}

func TestWS_ResyncLeaseIsSessionBound(t *testing.T) {
	h := newWSHarness(t, smallLiveCap)
	ctx := testCtx(t)
	c, w := h.open(ctx, h.login(), "")
	pushLive(t, h.daemon(0))
	if code, _ := closeOf(t, ctx, c); code != CloseResync {
		t.Fatalf("closed %d, want 4001", code)
	}
	// Another session offering the held id gets its own connection and id.
	_, w2 := h.open(ctx, h.login(), w.ClientID)
	if w2.ClientID == w.ClientID {
		t.Fatalf("another session took over %s", w.ClientID)
	}
	if n := h.dials.Load(); n != 2 {
		t.Fatalf("daemon dialled %d times, want 2", n)
	}
}

func TestWS_ResyncLeaseExpiresIntoDetach(t *testing.T) {
	h := newWSHarness(t, func(s *Server) {
		smallLiveCap(s)
		s.lease = 50 * time.Millisecond
	})
	ctx := testCtx(t)
	c, _ := h.open(ctx, h.login(), "")
	d := h.daemon(0)
	pushLive(t, d)
	if code, _ := closeOf(t, ctx, c); code != CloseResync {
		t.Fatalf("closed %d, want 4001", code)
	}
	waitFor(t, "the held connection to close", d.isClosed)
	d.mu.Lock()
	flushed := d.flushed
	d.mu.Unlock()
	if got := d.sentTypes(); strings.Join(got, ",") != ipc.MsgDetach || !flushed {
		t.Fatalf("daemon got %v (flushed %v), want a flushed detach", got, flushed)
	}
	waitFor(t, "the tab to leave the table", func() bool {
		h.s.mu.Lock()
		defer h.s.mu.Unlock()
		return len(h.s.tabs) == 0
	})
}

func TestWS_SixteenTabsThenRefused(t *testing.T) {
	h := newWSHarness(t, nil)
	ctx := testCtx(t)
	s := h.login()
	for i := 0; i < maxBridges; i++ {
		h.open(ctx, s, "")
	}
	_, resp, err := websocket.Dial(ctx, "ws://"+h.host+"/ws", &websocket.DialOptions{HTTPHeader: h.header(s.cookie, h.origin())})
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("17th tab: err=%v resp=%v, want 503", err, resp)
	}
}

func TestWS_DuplicatedTabGetsANewID(t *testing.T) {
	h := newWSHarness(t, nil)
	ctx := testCtx(t)
	s := h.login()
	_, a := h.open(ctx, s, "")
	_, b := h.open(ctx, s, a.ClientID)
	if a.ClientID == b.ClientID {
		t.Fatalf("two live tabs share %s", a.ClientID)
	}
	if n := h.dials.Load(); n != 2 {
		t.Fatalf("daemon dialled %d times, want 2", n)
	}
}

func TestWS_PermanentDialErrorsMapToCloseCodes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
	}{
		{fmt.Errorf("%w: x", ErrTokenRefused), CloseTokenRefused},
		{fmt.Errorf("%w: x", ErrVersionMismatch), CloseVersionMismatch},
		{errors.New("connection refused"), CloseDaemonUnavailable},
	} {
		h := newWSHarness(t, nil)
		h.mu.Lock()
		h.dialErr = tc.err
		h.mu.Unlock()
		ctx := testCtx(t)
		s := h.login()
		c, _, err := h.connect(ctx, s.cookie)
		if err != nil {
			t.Fatal(err)
		}
		sendMsg(t, ctx, c, MsgWebOpen, "", WebOpenPayload{Key: s.key})
		if code, _ := closeOf(t, ctx, c); code != tc.code {
			t.Fatalf("%v: closed %d, want %d", tc.err, code, tc.code)
		}
		h.s.mu.Lock()
		n := len(h.s.tabs) + h.s.dialing
		h.s.mu.Unlock()
		if n != 0 {
			t.Fatalf("%v: %d tabs left after a failed dial", tc.err, n)
		}
	}
}

func TestWS_ShutdownDetachesEveryTab(t *testing.T) {
	h := newWSHarness(t, nil)
	ctx := testCtx(t)
	s := h.login()
	a, wa := h.open(ctx, s, "")
	b, wb := h.open(ctx, s, "")
	sendMsg(t, ctx, a, ipc.MsgHello, "h", webHello(wa.ClientID))
	sendMsg(t, ctx, b, ipc.MsgHello, "h", webHello(wb.ClientID))
	waitFor(t, "both hellos", func() bool {
		return len(h.daemon(0).sentTypes()) == 1 && len(h.daemon(1).sentTypes()) == 1
	})

	h.s.Shutdown(ctx)

	for i := 0; i < 2; i++ {
		d := h.daemon(i)
		d.mu.Lock()
		flushed, closed := d.flushed, d.closed
		d.mu.Unlock()
		got := d.sentTypes()
		if len(got) != 2 || got[1] != ipc.MsgDetach || !flushed || !closed {
			t.Fatalf("daemon %d: sent %v flushed=%v closed=%v, want hello then a flushed detach and a close", i, got, flushed, closed)
		}
	}
	for i, c := range []*websocket.Conn{a, b} {
		if code, _ := closeOf(t, ctx, c); code != CloseGoingAway {
			t.Fatalf("page %d closed %d, want 1001", i, code)
		}
	}
}

func TestWS_LogHasNoSecrets(t *testing.T) {
	h := newWSHarness(t, nil)
	ctx := testCtx(t)
	s := h.login()
	c, w := h.open(ctx, s, "")
	sendMsg(t, ctx, c, ipc.MsgHello, "h", webHello(w.ClientID))
	sendMsg(t, ctx, c, ipc.MsgPaneInput, "", ipc.PaneInputPayload{PaneID: "p1", Data: []byte("SECRETDATA")})
	d := h.daemon(0)
	waitFor(t, "input at the daemon", func() bool { return len(d.sentTypes()) == 2 })
	c.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "the tab to close", d.isClosed)

	logs := h.logs.text()
	if logs == "" {
		t.Fatal("nothing was logged")
	}
	for name, secret := range map[string]string{
		"code": s.code, "normalized code": normalizeCode(s.code), "cookie": s.cookie, "key": s.key, "input": "SECRETDATA",
	} {
		if strings.Contains(logs, secret) {
			t.Fatalf("the log contains the %s:\n%s", name, logs)
		}
	}
}

func TestWS_StaleLeaseTimerDoesNotCloseANewerHold(t *testing.T) {
	h := newWSHarness(t, smallLiveCap)
	ctx := testCtx(t)
	s := h.login()
	c, w := h.open(ctx, s, "")
	d := h.daemon(0)
	pushLive(t, d)
	if code, _ := closeOf(t, ctx, c); code != CloseResync {
		t.Fatalf("closed %d, want 4001", code)
	}
	h.s.mu.Lock()
	tb, firstGen := h.s.tabs[w.ClientID], h.s.tabs[w.ClientID].holdGen
	h.s.mu.Unlock()

	// The page comes back and is resynced again: a second hold.
	c2, _ := h.open(ctx, s, w.ClientID)
	pushLive(t, d)
	if code, _ := closeOf(t, ctx, c2); code != CloseResync {
		t.Fatalf("closed %d, want 4001", code)
	}

	// The first hold's timer fires late.
	h.s.expire(tb, firstGen)
	h.s.mu.Lock()
	held := tb.held
	h.s.mu.Unlock()
	if !held || d.isClosed() {
		t.Fatalf("an old hold's timer closed the newer hold (held=%v closed=%v)", held, d.isClosed())
	}
}

func TestWS_ShutdownCancelsADialInFlight(t *testing.T) {
	h := newWSHarness(t, nil)
	ctx := testCtx(t)
	s := h.login()
	started, ended := make(chan struct{}), make(chan struct{})
	h.mu.Lock()
	h.dialHook = func(dctx context.Context) error {
		close(started)
		defer close(ended)
		<-dctx.Done()
		return dctx.Err()
	}
	h.mu.Unlock()

	c, _, err := h.connect(ctx, s.cookie)
	if err != nil {
		t.Fatal(err)
	}
	sendMsg(t, ctx, c, MsgWebOpen, "", WebOpenPayload{Key: s.key})
	<-started
	h.s.Shutdown(ctx)
	select {
	case <-ended:
	case <-ctx.Done():
		t.Fatal("the dial was still running after Shutdown")
	}
	if code, _ := closeOf(t, ctx, c); code != CloseDaemonUnavailable {
		t.Fatalf("closed %d, want 4003", code)
	}
}
