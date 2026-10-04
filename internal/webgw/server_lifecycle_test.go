package webgw

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/coder/websocket"
)

// A close reason is printable text cut to the 123 bytes a close frame holds,
// on a rune boundary.
func TestPrintable_CutsOnARuneBoundaryAndDropsControls(t *testing.T) {
	long := "daemon 1.2.3 " + strings.Repeat("é", 100)
	got := printable(long, maxCloseReason)
	if len(got) != maxCloseReason || !utf8.ValidString(got) || !strings.HasPrefix(got, "daemon 1.2.3 ") {
		t.Fatalf("cut to %d bytes, valid=%v: %q", len(got), utf8.ValidString(got), got)
	}
	odd := "xy" + strings.Repeat("é", 100) // the 123rd byte would split a rune
	if got := printable(odd, maxCloseReason); len(got) != maxCloseReason-1 || !utf8.ValidString(got) {
		t.Fatalf("cut to %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
	esc, rlo := string(rune(0x1b)), string(rune(0x202e))
	if got := printable("bad"+esc+"[31m"+rlo+"\n\ttoken\xff", 50); got != "bad[31mtoken" {
		t.Fatalf("printable = %q", got)
	}
}

// A version mismatch names the daemon's version and the gateway's; a long
// detail still fits the close frame.
func TestDialCloseReason_NamesTheVersionsAndFits(t *testing.T) {
	s := &Server{cfg: Config{Version: "9.9.9"}}
	got := s.dialCloseReason(CloseVersionMismatch, fmt.Errorf("%w: daemon 1.2.3", ErrVersionMismatch))
	if got != "daemon 1.2.3; quil web is 9.9.9" {
		t.Fatalf("reason = %q", got)
	}
	long := fmt.Errorf("%w: daemon 1.2.3 %s", ErrVersionMismatch, strings.Repeat("é", 200))
	got = s.dialCloseReason(CloseVersionMismatch, long)
	if len(got) > maxCloseReason || !utf8.ValidString(got) || !strings.HasPrefix(got, "daemon 1.2.3") {
		t.Fatalf("long reason: %d bytes, valid=%v: %q", len(got), utf8.ValidString(got), got)
	}
	if got := s.dialCloseReason(CloseDaemonUnavailable, errors.New("\x1b\x07")); got != "daemon unavailable" {
		t.Fatalf("unprintable detail = %q, want the code's own reason", got)
	}
}

// The daemon refuses the page's attach because another principal holds the
// client id: the gateway mints a new id on the same daemon connection and
// sends a fresh welcome, and the page's hello and attach under it go through.
func TestWS_ClientIDInUseRenewsTheID(t *testing.T) {
	h := newWSHarness(t, nil)
	ctx := testCtx(t)
	c, w := h.open(ctx, h.login(), "")
	d := h.daemon(0)
	sendMsg(t, ctx, c, ipc.MsgHello, "h1", webHello(w.ClientID))
	sendMsg(t, ctx, c, ipc.MsgAttach, "a1", ipc.AttachPayload{ClientID: w.ClientID})
	waitFor(t, "hello and attach at the daemon", func() bool { return len(d.sentTypes()) == 2 })

	d.in <- errorMsg(t, "a1", "client id in use")
	if m := readMsg(t, ctx, c); m.Type != ipc.MsgError || m.ID != "a1" {
		t.Fatalf("got %s %q, want the refusal first", m.Type, m.ID)
	}
	m := readMsg(t, ctx, c)
	var w2 WebWelcomePayload
	if m.Type != MsgWebWelcome || m.DecodePayload(&w2) != nil {
		t.Fatalf("got %s %s, want web_welcome", m.Type, m.Payload)
	}
	if w2.ClientID == w.ClientID || !strings.HasPrefix(w2.ClientID, "web-"+h.s.leases.prefix()+"-") {
		t.Fatalf("renewed id %q (old %q)", w2.ClientID, w.ClientID)
	}

	sendMsg(t, ctx, c, ipc.MsgHello, "h2", webHello(w2.ClientID))
	sendMsg(t, ctx, c, ipc.MsgAttach, "a2", ipc.AttachPayload{ClientID: w2.ClientID})
	waitFor(t, "the second hello and attach", func() bool { return len(d.sentTypes()) == 4 })
	d.mu.Lock()
	var a ipc.AttachPayload
	err := d.sent[3].DecodePayload(&a)
	d.mu.Unlock()
	if err != nil || a.ClientID != w2.ClientID {
		t.Fatalf("forwarded attach names %q (%v), want %q", a.ClientID, err, w2.ClientID)
	}
	h.s.mu.Lock()
	_, oldKept := h.s.tabs[w.ClientID]
	_, newKept := h.s.tabs[w2.ClientID]
	h.s.mu.Unlock()
	if oldKept || !newKept {
		t.Fatalf("tab table: old id kept=%v, new id kept=%v", oldKept, newKept)
	}
	if n := h.dials.Load(); n != 1 || d.isClosed() {
		t.Fatalf("dials=%d closed=%v, want the same daemon connection", n, d.isClosed())
	}
}

// Shutdown returns only after every page's 1001 close has finished: the
// process exits right after it, and http.Server.Shutdown does not wait for
// hijacked WebSocket connections.
func TestShutdown_WaitsForEveryPageClose(t *testing.T) {
	s := New(Config{Dial: func(context.Context, string) (DaemonConn, string, error) {
		return nil, "", errors.New("not used")
	}})
	var pages []*fakePage
	for i := 0; i < 2; i++ {
		p := &fakePage{closeBlock: make(chan struct{})}
		b := newBridge(newFakeDaemon(), testLimits(), s.budget, time.Now, func(string, ...any) {})
		if _, err := b.attachPage(p); err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("web-test-%d", i)
		s.tabs[id] = &tab{id: id, b: b}
		pages = append(pages, p)
	}
	done := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.Shutdown(ctx)
		close(done)
	}()
	for i, p := range pages {
		waitFor(t, fmt.Sprintf("page %d's 1001", i), func() bool { return p.closeCode() == CloseGoingAway })
	}
	returned := func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
	if returned() {
		t.Fatal("Shutdown returned while both page closes were running")
	}
	close(pages[0].closeBlock)
	if returned() {
		t.Fatal("Shutdown returned while a page close was running")
	}
	close(pages[1].closeBlock)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return after every page closed")
	}
}

// lateFrameDaemon delivers one frame only once the server's context is
// cancelled, the moment Shutdown has cancelled and not yet closed the tabs;
// after that it is an ordinary fake daemon.
type lateFrameDaemon struct {
	*fakeDaemon
	stop  <-chan struct{}
	frame *ipc.Message
	once  sync.Once
}

func (d *lateFrameDaemon) Receive() (*ipc.Message, error) {
	var first bool
	d.once.Do(func() { first = true })
	if first {
		<-d.stop
		return d.frame, nil
	}
	return d.fakeDaemon.Receive()
}

// A frame arriving between Shutdown's cancel and its page closes ends the
// tab's reader on the cancelled context. The reader must leave the close to
// Shutdown: Shutdown still returns only after the page's 1001 has finished.
func TestShutdown_WaitsForThePageWhenAFrameArrivesAfterCancel(t *testing.T) {
	s := New(Config{Dial: func(context.Context, string) (DaemonConn, string, error) {
		return nil, "", errors.New("not used")
	}})
	frame, err := ipc.NewMessage(ipc.MsgWorkspaceState, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	d := &lateFrameDaemon{fakeDaemon: newFakeDaemon(), stop: s.ctx.Done(), frame: frame}
	p := &fakePage{closeBlock: make(chan struct{})}
	b := newBridge(d, testLimits(), s.budget, time.Now, func(string, ...any) {})
	if _, err := b.attachPage(p); err != nil {
		t.Fatal(err)
	}
	tb := &tab{id: "web-test-0", b: b}
	s.tabs[tb.id] = tb
	ran := make(chan struct{})
	go func() { s.runTab(tb); close(ran) }()

	done := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.Shutdown(ctx)
		close(done)
	}()
	waitFor(t, "the page's 1001", func() bool { return p.closeCode() == CloseGoingAway })
	// The reader has seen the late frame on a cancelled context by now or
	// will; either way Shutdown owns the close and is still inside it.
	select {
	case <-done:
		t.Fatal("Shutdown returned while the page close was running")
	case <-time.After(50 * time.Millisecond):
	}
	close(p.closeBlock)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return after the page closed")
	}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the tab's reader did not end")
	}
	s.mu.Lock()
	left := len(s.tabs)
	s.mu.Unlock()
	if left != 0 || !d.isClosed() {
		t.Fatalf("tabs left %d, daemon closed %v", left, d.isClosed())
	}
}

// A socket that has not sent web_open yet holds one of the 16 places.
func TestWS_SocketsWaitingForWebOpenCountTowardTheCap(t *testing.T) {
	h := newWSHarness(t, nil)
	ctx := testCtx(t)
	s := h.login()
	for i := 0; i < maxBridges; i++ {
		if _, _, err := h.connect(ctx, s.cookie); err != nil {
			t.Fatalf("socket %d: %v", i, err)
		}
	}
	_, resp, err := websocket.Dial(ctx, "ws://"+h.host+"/ws", &websocket.DialOptions{HTTPHeader: h.header(s.cookie, h.origin())})
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("17th socket: err=%v resp=%v, want 503", err, resp)
	}
	if n := h.dials.Load(); n != 0 {
		t.Fatalf("daemon dialled %d times", n)
	}
}

// web.log records a refused message by its type alone and a closed tab with
// its close code.
func TestWS_LogRecordsRefusalsAndCloseCodes(t *testing.T) {
	h := newWSHarness(t, nil)
	ctx := testCtx(t)
	c, w := h.open(ctx, h.login(), "")
	sendMsg(t, ctx, c, ipc.MsgHello, "h", webHello(w.ClientID))
	sendMsg(t, ctx, c, ipc.MsgTokenCreateReq, "t1", map[string]string{"name": "SECRETPAYLOAD"})
	if m := readMsg(t, ctx, c); m.Type != ipc.MsgError {
		t.Fatalf("got %s, want the refusal", m.Type)
	}
	c.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "the close in the log", func() bool { return strings.Contains(h.logs.text(), "closed (1001") })
	logs := h.logs.text()
	if !strings.Contains(logs, "refused "+ipc.MsgTokenCreateReq+" from the page") {
		t.Fatalf("no refusal line:\n%s", logs)
	}
	if strings.Contains(logs, "SECRETPAYLOAD") {
		t.Fatalf("the log holds a refused message's payload:\n%s", logs)
	}
}
