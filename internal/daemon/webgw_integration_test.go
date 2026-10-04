package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/clientauth"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/logger"
	"github.com/artyomsv/quil/internal/webgw"
	"github.com/coder/websocket"
)

// These tests put the web gateway in front of a real daemon: the browser side
// is a real WebSocket, the daemon side a real unix-socket connection per tab.

// webHarness is a daemon with the fake PTY path and fake grace timers, so no
// real timer outlives the test when a conn closes.
func webHarness(t *testing.T) *authHarness {
	t.Helper()
	return newAuthHarnessWith(t, func(d *Daemon) {
		(&clientsHarness{t: t}).install(d, testGrace)
	})
}

type webRig struct {
	srv    *webgw.Server
	host   string
	cookie string
	key    string
}

// newWebRig serves a gateway whose dialer connects each tab to h's daemon
// socket, and logs in.
func newWebRig(t *testing.T, h *authHarness) *webRig {
	t.Helper()
	return newWebRigWith(t, func(ctx context.Context, clientID string) (webgw.DaemonConn, string, error) {
		c, err := ipc.NewClient(h.sock)
		if err != nil {
			return nil, "", err
		}
		return c, ipc.RightsFull, nil
	})
}

// tokenWebDial logs each tab in to h's TCP listener with token, as quil web
// --connect does.
func tokenWebDial(h *authHarness, token string) webgw.Dialer {
	return func(ctx context.Context, clientID string) (webgw.DaemonConn, string, error) {
		c, err := ipc.NewClientWithDialer(ctx, func(ctx context.Context) (net.Conn, error) {
			var dd net.Dialer
			return dd.DialContext(ctx, "tcp", h.addr)
		})
		if err != nil {
			return nil, "", err
		}
		hello := ipc.HelloPayload{Kind: "web", Proto: ipc.ProtocolVersion, ClientID: clientID}
		resp, err := clientauth.ClientLogin(c, token, hello, 5*time.Second)
		if err != nil {
			c.Close()
			return nil, "", fmt.Errorf("%w: %v", webgw.ErrTokenRefused, err)
		}
		return c, resp.Rights, nil
	}
}

// newWebRigWith serves a gateway with dial, and logs in.
func newWebRigWith(t *testing.T, dial webgw.Dialer) *webRig {
	t.Helper()
	srv := webgw.New(webgw.Config{
		Dial:    dial,
		Version: "test",
		Logf:    func(string, ...any) {},
		Sleep:   func(time.Duration) {},
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	})
	r := &webRig{srv: srv, host: ts.Listener.Addr().String()}

	code, err := srv.NewCode()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"code": code})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/login", bytes.NewReader(body))
	req.Header.Set("Origin", "http://"+r.host)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("web login: %s", resp.Status)
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	r.key = out.Key
	for _, c := range resp.Cookies() {
		if c.Name == webgw.SessionCookie {
			r.cookie = c.Value
		}
	}
	if r.cookie == "" || r.key == "" {
		t.Fatal("web login returned no cookie or key")
	}
	return r
}

// webFrame is one frame the page received: a JSON message, or a decoded
// terminal-output frame.
type webFrame struct {
	msg    *ipc.Message
	binary bool
	pane   string
	ghost  bool
	data   []byte
}

type webTab struct {
	t  *testing.T
	c  *websocket.Conn
	id string
}

// open connects a new tab and reads its welcome.
func (r *webRig) open(t *testing.T) *webTab { t.Helper(); return r.openAs(t, "") }

// openAs connects a tab offering hint, the id the page held before, as a page
// does when it reconnects.
func (r *webRig) openAs(t *testing.T, hint string) *webTab {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hdr := http.Header{}
	hdr.Set("Origin", "http://"+r.host)
	hdr.Set("Cookie", webgw.SessionCookie+"="+r.cookie)
	c, _, err := websocket.Dial(ctx, "ws://"+r.host+"/ws", &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("web socket: %v", err)
	}
	t.Cleanup(func() { c.CloseNow() })
	// A browser reads frames of any size; the library's 32 KiB default would
	// refuse a large output frame.
	c.SetReadLimit(1 << 20)
	w := &webTab{t: t, c: c}
	w.send("web_open", "", webgw.WebOpenPayload{ClientIDHint: hint, Key: r.key})
	f, err := w.next(ctx)
	if err != nil || f.msg == nil || f.msg.Type != webgw.MsgWebWelcome {
		t.Fatalf("first frame = %+v, %v; want web_welcome", f, err)
	}
	var welcome webgw.WebWelcomePayload
	if err := f.msg.DecodePayload(&welcome); err != nil {
		t.Fatal(err)
	}
	w.id = welcome.ClientID
	return w
}

func (w *webTab) send(typ, id string, payload any) {
	w.t.Helper()
	m, err := ipc.NewMessage(typ, payload)
	if err != nil {
		w.t.Fatal(err)
	}
	m.ID = id
	raw, _ := json.Marshal(m)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.c.Write(ctx, websocket.MessageText, raw); err != nil {
		w.t.Fatalf("send %s: %v", typ, err)
	}
}

func (w *webTab) next(ctx context.Context) (webFrame, error) {
	typ, raw, err := w.c.Read(ctx)
	if err != nil {
		return webFrame{}, err
	}
	if typ == websocket.MessageBinary {
		if len(raw) < 11 || len(raw) < 11+int(raw[10]) {
			return webFrame{}, errors.New("short binary frame")
		}
		n := int(raw[10])
		return webFrame{binary: true, ghost: raw[1]&1 != 0, pane: string(raw[11 : 11+n]), data: raw[11+n:]}, nil
	}
	var m ipc.Message
	if err := json.Unmarshal(raw, &m); err != nil {
		return webFrame{}, err
	}
	return webFrame{msg: &m}, nil
}

// until reads frames until match holds and returns everything read.
func (w *webTab) until(what string, match func(webFrame) bool) []webFrame {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got []webFrame
	for {
		f, err := w.next(ctx)
		if err != nil {
			w.t.Fatalf("waiting for %s: %v (read %d frames)", what, err, len(got))
		}
		got = append(got, f)
		if match(f) {
			return got
		}
	}
}

// closeCode reads until the socket fails and returns its close code.
func (w *webTab) closeCode(ctx context.Context) int {
	w.t.Helper()
	code, _ := w.closeOf(ctx)
	return code
}

// closeOf reads until the socket fails and returns its close code and reason.
func (w *webTab) closeOf(ctx context.Context) (int, string) {
	w.t.Helper()
	code, reason, err := w.readToClose(ctx)
	if err != nil {
		w.t.Fatalf("the socket failed without a close frame: %v", err)
	}
	return code, reason
}

// readToClose is closeOf for a goroutine other than the test's: it reports a
// socket that failed without a close frame as an error.
func (w *webTab) readToClose(ctx context.Context) (int, string, error) {
	for {
		if _, err := w.next(ctx); err != nil {
			var ce websocket.CloseError
			if !errors.As(err, &ce) {
				return 0, "", err
			}
			return int(ce.Code), ce.Reason, nil
		}
	}
}

// attach says hello and attaches at a 120x40 window, then waits for the
// daemon to finish the attach: a state_req is answered only after the attach
// before it has been handled. It returns every frame read.
func (w *webTab) attach() []webFrame {
	w.t.Helper()
	return w.attachSized(120, 40)
}

// attachSized is attach at a winCols x winRows window, with the pane
// interior a current client reports beside it.
func (w *webTab) attachSized(winCols, winRows int) []webFrame {
	w.t.Helper()
	w.send(ipc.MsgHello, "w-hello", ipc.HelloPayload{Kind: "web", Proto: ipc.ProtocolVersion, ClientID: w.id})
	w.send(ipc.MsgAttach, "", ipc.AttachPayload{ClientID: w.id, Cols: winCols - 2, Rows: winRows - 4, WinCols: winCols, WinRows: winRows})
	w.send(ipc.MsgStateReq, "w-state", struct{}{})
	return w.until("the state_req answer", func(f webFrame) bool {
		return f.msg != nil && f.msg.Type == ipc.MsgWorkspaceState && f.msg.ID == "w-state"
	})
}

// ack acknowledges a terminal-output frame, as the page does once xterm has
// written it.
func (w *webTab) ack(f webFrame) {
	w.t.Helper()
	if f.binary && len(f.data) > 0 {
		w.send(webgw.MsgWebAck, "", webgw.WebAckPayload{Bytes: int64(len(f.data))})
	}
}

// drainClient reads c until it fails, so the daemon never blocks on a client the
// test does not read.
func drainClient(c *ipc.Client) {
	go func() {
		for {
			if _, err := c.Receive(); err != nil {
				return
			}
		}
	}()
}

// floodUntilClosed prints live output into pane until w (which reads and never
// acknowledges) is closed, and returns the close code.
func floodUntilClosed(t *testing.T, h *authHarness, paneID string, w *webTab) int {
	t.Helper()
	var stop atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		chunk := bytes.Repeat([]byte("x"), 8192)
		deadline := time.Now().Add(10 * time.Second)
		for !stop.Load() && time.Now().Before(deadline) {
			h.d.flushPaneOutput(paneID, chunk)
			time.Sleep(200 * time.Microsecond)
		}
	}()
	defer func() { stop.Store(true); <-done }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return w.closeCode(ctx)
}

func newTerminalPane(t *testing.T, h *authHarness) *Pane {
	t.Helper()
	tab := h.d.session.CreateTab("T")
	pane, err := h.d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	setPaneType(pane, "terminal")
	return pane
}

func isWebState(f webFrame) bool { return f.msg != nil && f.msg.Type == ipc.MsgWorkspaceState }

func TestWeb_AttachReplayAndLiveReachThePage(t *testing.T) {
	h := webHarness(t)
	r := newWebRig(t, h)
	tab := h.d.session.CreateTab("T")
	pane, err := h.d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	setPaneType(pane, "terminal")
	pane.OutputBuf.Write([]byte("replay-me"))

	w := r.open(t)
	w.send(ipc.MsgHello, "w-hello", ipc.HelloPayload{Kind: "web", Proto: ipc.ProtocolVersion, ClientID: w.id})
	w.send(ipc.MsgAttach, "", ipc.AttachPayload{ClientID: w.id, Cols: 118, Rows: 36, WinCols: 120, WinRows: 40})

	frames := w.until("the ghost replay", func(f webFrame) bool {
		return f.binary && f.ghost && f.pane == pane.ID && bytes.Contains(f.data, []byte("replay-me"))
	})
	var sawState bool
	for _, f := range frames {
		if isWebState(f) {
			sawState = true
		}
		if f.binary && !f.ghost {
			t.Fatalf("a live frame %q arrived before the replay", f.data)
		}
	}
	if !sawState {
		t.Fatal("no workspace_state arrived before the replay")
	}

	h.d.flushPaneOutput(pane.ID, []byte("live-bytes"))
	w.until("the live frame", func(f webFrame) bool {
		return f.binary && !f.ghost && f.pane == pane.ID && bytes.Equal(f.data, []byte("live-bytes"))
	})
}

func TestWeb_PaneInputIsIDlessAndNoRespArrives(t *testing.T) {
	h := webHarness(t)
	r := newWebRig(t, h)
	pane, sess := livePane(t, h)
	w := r.open(t)
	w.attach()

	before, _ := sess.counts()
	w.send(ipc.MsgPaneInput, "k1", ipc.PaneInputPayload{PaneID: pane.ID, Data: []byte("x")})
	waitUntil(t, "the input to reach the pane", func() bool {
		n, _ := sess.counts()
		return n > before
	})

	// The gateway strips the ID, so the daemon never answers it. This is the
	// last read on the socket: an expired read context closes it.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	for {
		f, err := w.next(ctx)
		if err != nil {
			break
		}
		if f.msg != nil && f.msg.Type == ipc.MsgPaneInputResp {
			t.Fatalf("a pane_input_resp reached the page: %s", f.msg.Payload)
		}
	}
}

func TestWeb_TabCloseReleasesMaster(t *testing.T) {
	h := webHarness(t)
	r := newWebRig(t, h)
	tui := attachTUIAs(t, h.sock, "TUI", 40, 10, false)
	sendClientMsg(t, tui, ipc.MsgClientGeometry, ipc.ClientGeometryPayload{Cols: 40, Rows: 10})
	waitUntil(t, "the TUI is master", func() bool { return h.d.masterID() == "TUI" })

	w := r.open(t)
	w.attach()
	waitUntil(t, "both clients attached", func() bool { return h.d.clientCount() == 2 })
	w.send(ipc.MsgTakeControl, "", struct{}{})
	waitUntil(t, "the web tab is master", func() bool { return h.d.masterID() == w.id })
	readUntil(t, tui, "a state naming the web tab master", func(m *ipc.Message) bool {
		var s map[string]any
		return m.Type == ipc.MsgWorkspaceState && m.DecodePayload(&s) == nil && s["size_master"] == w.id
	})

	start := time.Now()
	if err := w.c.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatalf("close: %v", err)
	}
	readUntil(t, tui, "a state without the web tab as master", func(m *ipc.Message) bool {
		var s map[string]any
		return m.Type == ipc.MsgWorkspaceState && m.DecodePayload(&s) == nil && s["size_master"] != w.id
	})
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("the master slot took %v to release, want no grace", d)
	}
}

func TestWeb_SlowPageDoesNotOverflowTheDaemon(t *testing.T) {
	var logs safeBuffer
	logger.Init("warn", &logs)
	t.Cleanup(func() { logger.Init("info", io.Discard) })

	h := webHarness(t)
	r := newWebRig(t, h)
	tab := h.d.session.CreateTab("T")
	pane, err := h.d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	setPaneType(pane, "terminal")
	w := r.open(t)
	w.attach()

	// The page reads but never acknowledges.
	var stop atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		chunk := bytes.Repeat([]byte("x"), 8192)
		deadline := time.Now().Add(2 * time.Second)
		for !stop.Load() && time.Now().Before(deadline) {
			h.d.flushPaneOutput(pane.ID, chunk)
			time.Sleep(200 * time.Microsecond)
		}
	}()
	t.Cleanup(func() { stop.Store(true); <-done })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if code := w.closeCode(ctx); code != webgw.CloseResync && code != webgw.CloseTooSlow {
		t.Fatalf("the page closed with %d, want 4001 or 4002", code)
	}
	stop.Store(true)
	<-done
	if strings.Contains(logs.String(), "dropping slow client") {
		t.Fatalf("the daemon dropped the gateway's connection:\n%s", logs.String())
	}
}

func TestWeb_DaemonRestartClosesWith4003(t *testing.T) {
	h := webHarness(t)
	r := newWebRig(t, h)
	h.d.session.CreateTab("T")
	w := r.open(t)
	w.attach()

	h.d.server.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if code := w.closeCode(ctx); code != webgw.CloseDaemonUnavailable {
		t.Fatalf("the page closed with %d, want 4003", code)
	}
}
