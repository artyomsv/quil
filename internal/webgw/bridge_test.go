package webgw

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

type fakeDaemon struct {
	mu              sync.Mutex
	in              chan *ipc.Message
	sent            []*ipc.Message
	flushed, closed bool
}

func newFakeDaemon() *fakeDaemon { return &fakeDaemon{in: make(chan *ipc.Message, 1024)} }

func (f *fakeDaemon) Send(m *ipc.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errors.New("closed")
	}
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeDaemon) Receive() (*ipc.Message, error) {
	m, ok := <-f.in
	if !ok {
		return nil, errors.New("eof")
	}
	return m, nil
}

func (f *fakeDaemon) Flush(time.Duration) bool {
	f.mu.Lock()
	f.flushed = true
	f.mu.Unlock()
	return true
}

func (f *fakeDaemon) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.in)
	}
	return nil
}

func (f *fakeDaemon) sentTypes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.sent {
		out = append(out, m.Type)
	}
	return out
}

func (f *fakeDaemon) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

type pageFrame struct {
	binary bool
	b      []byte
}

type fakePage struct {
	mu     sync.Mutex
	frames []pageFrame
	closed int
	reason string
	block  chan struct{} // non-nil: writes wait on it
	gen    uint64        // set by attach

	closeBlock chan struct{} // non-nil: Close records its code, then waits on it
}

func (p *fakePage) write(bin bool, b []byte) error {
	if p.block != nil {
		<-p.block
	}
	p.mu.Lock()
	p.frames = append(p.frames, pageFrame{bin, append([]byte(nil), b...)})
	p.mu.Unlock()
	return nil
}

func (p *fakePage) WriteText(_ context.Context, b []byte) error   { return p.write(false, b) }
func (p *fakePage) WriteBinary(_ context.Context, b []byte) error { return p.write(true, b) }
func (p *fakePage) Close(code int, reason string) {
	p.mu.Lock()
	p.closed, p.reason = code, reason
	p.mu.Unlock()
	if p.closeBlock != nil {
		<-p.closeBlock
	}
}
func (p *fakePage) closeCode() int      { p.mu.Lock(); defer p.mu.Unlock(); return p.closed }
func (p *fakePage) closeReason() string { p.mu.Lock(); defer p.mu.Unlock(); return p.reason }
func (p *fakePage) count() int          { p.mu.Lock(); defer p.mu.Unlock(); return len(p.frames) }

func testLimits() bridgeLimits {
	return bridgeLimits{ControlMax: 512, LiveUnackedMax: 2 << 20, ReplayTabMax: 64 << 20, WriteTimeout: 10 * time.Second, ResyncsPerMin: 3, Version: "9.9.9"}
}

// waitFor polls cond until it holds; the deadline only bounds a failure.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func outputMsg(t *testing.T, pane string, data []byte, ghost bool, gen uint64) *ipc.Message {
	t.Helper()
	m, err := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{PaneID: pane, Data: data, Ghost: ghost, Generation: gen})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// attach attaches p and records the generation fromPage needs for it.
func attach(t *testing.T, b *bridge, p *fakePage) {
	t.Helper()
	gen, err := b.attachPage(p)
	if err != nil {
		t.Fatal(err)
	}
	p.gen = gen
}

func startBridgeAt(t *testing.T, lim bridgeLimits, now func() time.Time, p *fakePage) (*bridge, *fakeDaemon) {
	t.Helper()
	return startBridgeWith(t, lim, newReplayBudget(128<<20), now, p)
}

func startBridgeWith(t *testing.T, lim bridgeLimits, budget *replayBudget, now func() time.Time, p *fakePage) (*bridge, *fakeDaemon) {
	t.Helper()
	d := newFakeDaemon()
	b := newBridge(d, lim, budget, now, t.Logf)
	attach(t, b, p)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		b.close(CloseGoingAway, "test over")
		<-done
	})
	return b, d
}

func startBridge(t *testing.T, lim bridgeLimits) (*bridge, *fakeDaemon, *fakePage) {
	t.Helper()
	p := &fakePage{}
	b, d := startBridgeAt(t, lim, time.Now, p)
	return b, d, p
}

// Daemon messages leave in receive order across JSON and binary frames: a
// pane_sizes announcement must precede the repaint that follows it.
func TestBridge_PreservesReceiveOrder(t *testing.T) {
	_, d, p := startBridge(t, testLimits())
	sizes, _ := ipc.NewMessage(ipc.MsgPaneSizes, ipc.PaneSizesPayload{})
	d.in <- outputMsg(t, "p1", []byte("ghost"), true, 0)
	d.in <- sizes
	d.in <- outputMsg(t, "p1", []byte("repaint"), false, 3)
	waitFor(t, "3 frames", func() bool { return p.count() == 3 })
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.frames[0].binary || p.frames[1].binary || !p.frames[2].binary {
		t.Fatalf("order/kinds wrong: %+v", p.frames)
	}
	var m ipc.Message
	_ = json.Unmarshal(p.frames[1].b, &m)
	if m.Type != ipc.MsgPaneSizes {
		t.Fatalf("middle frame is %q", m.Type)
	}
}

// Live output past the unacknowledged limit is dropped and the page resynced;
// the daemon connection stays open (the page re-attaches on it).
func TestBridge_LiveOverflowResyncsAndKeepsTheDaemonConn(t *testing.T) {
	lim := testLimits()
	lim.LiveUnackedMax = 10
	_, d, p := startBridge(t, lim)
	d.in <- outputMsg(t, "p1", []byte("0123456789"), false, 1)
	d.in <- outputMsg(t, "p1", []byte("X"), false, 1)
	waitFor(t, "close 4001", func() bool { return p.closeCode() == CloseResync })
	if d.isClosed() {
		t.Fatal("daemon conn closed on a resync")
	}
}

// Acknowledged bytes free room: a page that keeps up never resyncs.
func TestBridge_AcksFreeRoom(t *testing.T) {
	lim := testLimits()
	lim.LiveUnackedMax = 10
	b, d, p := startBridge(t, lim)
	for i := 0; i < 20; i++ {
		d.in <- outputMsg(t, "p1", []byte("01234"), false, 1)
		waitFor(t, "frame", func() bool { return p.count() == i+1 })
		ack, _ := json.Marshal(map[string]any{"type": MsgWebAck, "payload": WebAckPayload{Bytes: 5}})
		if err := b.fromPage(p.gen, ack); err != nil {
			t.Fatal(err)
		}
	}
	if p.closeCode() != 0 {
		t.Fatalf("closed %d despite acks", p.closeCode())
	}
}

// Replay is buffered, never dropped, and does not count toward the live
// limit: a large attach completes.
func TestBridge_ReplayIsNotCountedAsLive(t *testing.T) {
	lim := testLimits()
	lim.LiveUnackedMax = 10
	_, d, p := startBridge(t, lim)
	for i := 0; i < 50; i++ {
		d.in <- outputMsg(t, "p1", []byte("ghost-bytes"), true, 0)
	}
	waitFor(t, "50 frames", func() bool { return p.count() == 50 })
	if p.closeCode() != 0 {
		t.Fatalf("replay closed the page with %d", p.closeCode())
	}
}

func TestBridge_ReplayOverTheTabCapClosesTooSlow(t *testing.T) {
	lim := testLimits()
	lim.ReplayTabMax = 20
	p := &fakePage{block: make(chan struct{})} // the page reads nothing: replay accumulates
	_, d := startBridgeAt(t, lim, time.Now, p)
	for i := 0; i < 5; i++ {
		d.in <- outputMsg(t, "p1", []byte("0123456789"), true, 0)
	}
	waitFor(t, "close 4002", func() bool { return p.closeCode() == CloseTooSlow })
	close(p.block)
}

func TestBridge_ReplayOverTheSharedBudgetClosesTooSlow(t *testing.T) {
	budget := newReplayBudget(15)
	p := &fakePage{block: make(chan struct{})}
	_, d := startBridgeWith(t, testLimits(), budget, time.Now, p)
	d.in <- outputMsg(t, "p1", []byte("0123456789"), true, 0)
	d.in <- outputMsg(t, "p1", []byte("0123456789"), true, 0)
	waitFor(t, "close 4002", func() bool { return p.closeCode() == CloseTooSlow })
	close(p.block)
	waitFor(t, "budget returned", func() bool {
		budget.mu.Lock()
		defer budget.mu.Unlock()
		return budget.used == 0
	})
}

// Every close except a resync sends detach and flushes it before closing the
// daemon connection, so a held master slot is released at once.
func TestBridge_CloseDetachesAndFlushes(t *testing.T) {
	b, d, p := startBridge(t, testLimits())
	b.close(CloseGoingAway, "going away")
	types := d.sentTypes()
	if len(types) == 0 || types[len(types)-1] != ipc.MsgDetach {
		t.Fatalf("sent %v, want detach last", types)
	}
	d.mu.Lock()
	flushed, closed := d.flushed, d.closed
	d.mu.Unlock()
	if !flushed || !closed {
		t.Fatalf("flushed=%v closed=%v", flushed, closed)
	}
	waitFor(t, "close 1001", func() bool { return p.closeCode() == CloseGoingAway })
}

func TestBridge_CloseTUIClosesWithCloseByAgent(t *testing.T) {
	_, d, p := startBridge(t, testLimits())
	m, _ := ipc.NewMessage(ipc.MsgCloseTUI, struct{}{})
	d.in <- m
	waitFor(t, "close 4006", func() bool { return p.closeCode() == CloseByAgent })
	if got := d.sentTypes(); len(got) == 0 || got[len(got)-1] != ipc.MsgDetach {
		t.Fatalf("no detach before the agent close: %v", got)
	}
}

func TestBridge_DaemonLossClosesWith4003(t *testing.T) {
	_, d, p := startBridge(t, testLimits())
	_ = d.Close()
	waitFor(t, "close 4003", func() bool { return p.closeCode() == CloseDaemonUnavailable })
}

func errorMsg(t *testing.T, id, message string) *ipc.Message {
	t.Helper()
	m, err := ipc.NewMessage(ipc.MsgError, ipc.ErrorPayload{Code: ipc.ErrCodeRefused, Message: message})
	if err != nil {
		t.Fatal(err)
	}
	m.ID = id
	return m
}

// A revoked token: the daemon sends an id-less refusal, then closes. The page
// is closed with 4004 and the daemon's reason, not with 4003.
func TestBridge_RevokeThenLossClosesWith4004(t *testing.T) {
	_, d, p := startBridge(t, testLimits())
	d.in <- errorMsg(t, "", "token revoked")
	_ = d.Close()
	waitFor(t, "a close", func() bool { return p.closeCode() != 0 })
	if p.closeCode() != CloseTokenRefused || p.closeReason() != "token revoked" {
		t.Fatalf("closed %d %q, want 4004 \"token revoked\"", p.closeCode(), p.closeReason())
	}
}

// A refusal that answers a request is not the daemon's last word: a loss after
// it is still 4003.
func TestBridge_AnsweredRefusalThenLossClosesWith4003(t *testing.T) {
	_, d, p := startBridge(t, testLimits())
	d.in <- errorMsg(t, "r1", "not allowed")
	_ = d.Close()
	waitFor(t, "close 4003", func() bool { return p.closeCode() == CloseDaemonUnavailable })
}

// After a resync the counters start at zero and output for the old page is
// gone: the new page starts clean.
func TestBridge_ReattachAfterResyncStartsClean(t *testing.T) {
	lim := testLimits()
	lim.LiveUnackedMax = 10
	b, d, p := startBridge(t, lim)
	d.in <- outputMsg(t, "p1", []byte("0123456789"), false, 1)
	d.in <- outputMsg(t, "p1", []byte("X"), false, 1)
	waitFor(t, "resync", func() bool { return p.closeCode() == CloseResync })
	p2 := &fakePage{}
	attach(t, b, p2)
	d.in <- outputMsg(t, "p1", []byte("01234"), false, 1)
	waitFor(t, "frame on the new page", func() bool { return p2.count() == 1 })
	if p2.closeCode() != 0 {
		t.Fatalf("new page closed with %d", p2.closeCode())
	}
}

func TestBridge_ControlQueueOverflowClosesTooSlow(t *testing.T) {
	lim := testLimits()
	lim.ControlMax = 3
	p := &fakePage{block: make(chan struct{})}
	_, d := startBridgeAt(t, lim, time.Now, p)
	for i := 0; i < 5; i++ {
		m, _ := ipc.NewMessage(ipc.MsgWorkspaceState, struct{}{})
		d.in <- m
	}
	waitFor(t, "close 4002", func() bool { return p.closeCode() == CloseTooSlow })
	waitFor(t, "detach", func() bool {
		types := d.sentTypes()
		return len(types) > 0 && types[len(types)-1] == ipc.MsgDetach
	})
	close(p.block)
}

// Three resyncs within a minute close with 4001; the fourth closes the tab
// with 4002 instead.
func TestBridge_ResyncLimitTurnsIntoTooSlow(t *testing.T) {
	lim := testLimits()
	lim.LiveUnackedMax = 10
	var mu sync.Mutex
	clock := time.Unix(1000, 0)
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	p := &fakePage{}
	b, d := startBridgeAt(t, lim, now, p)
	for i := 0; i < 3; i++ {
		d.in <- outputMsg(t, "p1", []byte("0123456789"), false, 1)
		d.in <- outputMsg(t, "p1", []byte("X"), false, 1)
		waitFor(t, "close 4001", func() bool { return p.closeCode() == CloseResync })
		if got := b.resyncsInLastMinute(); got != i+1 {
			t.Fatalf("resyncs in the last minute = %d, want %d", got, i+1)
		}
		p = &fakePage{}
		attach(t, b, p)
	}
	d.in <- outputMsg(t, "p1", []byte("0123456789"), false, 1)
	d.in <- outputMsg(t, "p1", []byte("X"), false, 1)
	waitFor(t, "close 4002", func() bool { return p.closeCode() == CloseTooSlow })
	if !d.isClosed() {
		t.Fatal("daemon conn still open after too slow")
	}
}

// An acknowledgement is gateway-local: it works before any hello and is never
// forwarded.
func TestBridge_AckBeforeHelloIsHandledLocally(t *testing.T) {
	b, d, p := startBridge(t, testLimits())
	ack, _ := json.Marshal(map[string]any{"type": MsgWebAck, "payload": WebAckPayload{Bytes: 5}})
	if err := b.fromPage(p.gen, ack); err != nil {
		t.Fatalf("ack before hello: %v", err)
	}
	if got := d.sentTypes(); len(got) != 0 {
		t.Fatalf("an ack reached the daemon: %v", got)
	}
}

func helloFrame(t *testing.T, id string) []byte {
	t.Helper()
	m, err := ipc.NewMessage(ipc.MsgHello, ipc.HelloPayload{Kind: "web", Proto: ipc.ProtocolVersion, ClientID: id, Version: "forged"})
	if err != nil {
		t.Fatal(err)
	}
	m.ID = "h1"
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The hello that reaches the daemon carries the gateway's version, and a new
// lease asks for a fresh hello while keeping that version.
func TestBridge_LeaseGatesTheHelloAndKeepsTheVersion(t *testing.T) {
	b, d, p := startBridge(t, testLimits())
	b.setLease("web-a-1")
	if err := b.fromPage(p.gen, helloFrame(t, "web-a-1")); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	var h ipc.HelloPayload
	err := json.Unmarshal(d.sent[0].Payload, &h)
	d.mu.Unlock()
	if err != nil || h.Version != "9.9.9" {
		t.Fatalf("forwarded hello version = %q (%v), want 9.9.9", h.Version, err)
	}

	b.setLease("web-a-2")
	if err := b.fromPage(p.gen, helloFrame(t, "web-a-1")); err == nil {
		t.Fatal("hello naming the old lease was accepted after a new lease")
	}
}

func TestBridge_FatalFirstMessageClosesTheTab(t *testing.T) {
	b, d, p := startBridge(t, testLimits())
	b.setLease("web-a-1")
	stateReq, _ := json.Marshal(&ipc.Message{Type: ipc.MsgStateReq})
	if err := b.fromPage(p.gen, stateReq); err == nil {
		t.Fatal("a first message other than hello was accepted")
	}
	waitFor(t, "page closed", func() bool { return p.closeCode() != 0 })
	if p.closeCode() != 1008 || p.closeReason() != "protocol error" {
		t.Fatalf("closed %d %q, want 1008 \"protocol error\"", p.closeCode(), p.closeReason())
	}
	if !d.isClosed() {
		t.Fatal("daemon conn still open")
	}
	if err := b.fromPage(p.gen, stateReq); !errors.Is(err, errClosed) {
		t.Fatalf("frame after close: %v, want errClosed", err)
	}
}

// A page close can block (a WebSocket close waits for the peer). The reader
// must keep draining the daemon meanwhile, or the daemon drops the connection.
func TestBridge_ReaderKeepsDrainingWhileAPageCloseBlocks(t *testing.T) {
	lim := testLimits()
	lim.LiveUnackedMax = 10
	p := &fakePage{closeBlock: make(chan struct{})}
	_, d := startBridgeAt(t, lim, time.Now, p)
	t.Cleanup(func() { close(p.closeBlock) })
	d.in <- outputMsg(t, "p1", []byte("0123456789"), false, 1)
	d.in <- outputMsg(t, "p1", []byte("X"), false, 1)
	waitFor(t, "close 4001 started", func() bool { return p.closeCode() == CloseResync })
	for i := 0; i < 20; i++ {
		m, _ := ipc.NewMessage(ipc.MsgWorkspaceState, struct{}{})
		d.in <- m
	}
	waitFor(t, "reader to drain the daemon side", func() bool { return len(d.in) == 0 })
}

func TestBridge_AttachOnAClosedBridgeIsRefused(t *testing.T) {
	b, _, _ := startBridge(t, testLimits())
	b.close(CloseGoingAway, "going away")
	if _, err := b.attachPage(&fakePage{}); !errors.Is(err, errClosed) {
		t.Fatalf("attachPage after close = %v, want errClosed", err)
	}
}

// A re-attach replaces the writer: the old page gets nothing more.
func TestBridge_ReattachMovesOutputToTheNewPage(t *testing.T) {
	b, d, p1 := startBridge(t, testLimits())
	p2 := &fakePage{}
	attach(t, b, p2)
	d.in <- outputMsg(t, "p1", []byte("x"), false, 1)
	waitFor(t, "frame on the new page", func() bool { return p2.count() == 1 })
	if p1.count() != 0 {
		t.Fatalf("old page received %d frames", p1.count())
	}
}

// A late acknowledgement from the socket that was resynced away must not
// credit the page that replaced it.
func TestBridge_StaleAckDoesNotCreditTheNewPage(t *testing.T) {
	lim := testLimits()
	lim.LiveUnackedMax = 10
	b, d, p1 := startBridge(t, lim)
	d.in <- outputMsg(t, "p1", []byte("0123456789"), false, 1)
	d.in <- outputMsg(t, "p1", []byte("X"), false, 1)
	waitFor(t, "resync", func() bool { return p1.closeCode() == CloseResync })
	p2 := &fakePage{}
	attach(t, b, p2)
	d.in <- outputMsg(t, "p1", []byte("0123456789"), false, 1)
	waitFor(t, "frame on the new page", func() bool { return p2.count() == 1 })

	ack, _ := json.Marshal(map[string]any{"type": MsgWebAck, "payload": WebAckPayload{Bytes: 10}})
	if err := b.fromPage(p1.gen, ack); !errors.Is(err, errStalePage) {
		t.Fatalf("stale ack = %v, want errStalePage", err)
	}
	b.mu.Lock()
	live := b.liveOut
	b.mu.Unlock()
	if live != 10 {
		t.Fatalf("liveOut = %d after a stale ack, want 10", live)
	}
	d.in <- outputMsg(t, "p1", []byte("X"), false, 1)
	waitFor(t, "second resync", func() bool { return p2.closeCode() == CloseResync })
}

// An ack larger than what was written is clamped; it cannot drive the
// counters negative and so buy room for later output.
func TestBridge_OverAckIsClamped(t *testing.T) {
	b, d, p := startBridge(t, testLimits())
	d.in <- outputMsg(t, "p1", []byte("01234"), false, 1)
	waitFor(t, "frame", func() bool { return p.count() == 1 })
	ack, _ := json.Marshal(map[string]any{"type": MsgWebAck, "payload": WebAckPayload{Bytes: 100}})
	if err := b.fromPage(p.gen, ack); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	live, inFlight := b.liveOut, len(b.inFlight)
	b.mu.Unlock()
	if live != 0 || inFlight != 0 {
		t.Fatalf("liveOut=%d inFlight=%d after an over-ack, want 0 and 0", live, inFlight)
	}
}

// One acknowledgement can cover replay and live frames written in between.
func TestBridge_OneAckCoversInterleavedReplayAndLive(t *testing.T) {
	budget := newReplayBudget(128 << 20)
	p := &fakePage{}
	b, d := startBridgeWith(t, testLimits(), budget, time.Now, p)
	d.in <- outputMsg(t, "p1", []byte("0123"), true, 0)
	d.in <- outputMsg(t, "p1", []byte("012345"), false, 1)
	d.in <- outputMsg(t, "p2", []byte("012"), true, 0)
	waitFor(t, "3 frames", func() bool { return p.count() == 3 })
	ack, _ := json.Marshal(map[string]any{"type": MsgWebAck, "payload": WebAckPayload{Bytes: 13}})
	if err := b.fromPage(p.gen, ack); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	live, replay, inFlight := b.liveOut, b.replayTab, len(b.inFlight)
	b.mu.Unlock()
	budget.mu.Lock()
	used := budget.used
	budget.mu.Unlock()
	if live != 0 || replay != 0 || inFlight != 0 || used != 0 {
		t.Fatalf("live=%d replay=%d inFlight=%d budget=%d, want all 0", live, replay, inFlight, used)
	}
}

// The close for a page that went away must not take down a bridge that a
// resync already detached and a reloading page is about to reclaim.
func TestClosePage_StalePageLeavesAResyncedBridgeOpen(t *testing.T) {
	b, d, p := startBridge(t, testLimits())
	b.detachPage(CloseResync, "resync")
	b.closePage(p.gen, CloseGoingAway, "page closed")
	if d.isClosed() {
		t.Fatal("a stale page's close shut a held bridge")
	}

	p2 := &fakePage{}
	attach(t, b, p2)
	b.closePage(p2.gen, CloseGoingAway, "page closed")
	waitFor(t, "the current page's close to shut the bridge", d.isClosed)
}

// A pane_input_resp from the daemon frees the paste place its id held.
func TestBridge_PasteAnswerFreesThePlace(t *testing.T) {
	g := newForwardGate("web-p-1", "v", nil)
	g.helloSeen = true
	b := &bridge{gate: g, logf: func(string, ...any) {}}
	g.pastes = map[string]bool{"c1": true, "c2": true}
	ans, _ := ipc.NewMessage(ipc.MsgPaneInputResp, ipc.PaneInputRespPayload{PaneID: "p", Delivered: true})
	ans.ID = "c1"
	b.fromDaemon(ans)
	if g.pastes["c1"] || !g.pastes["c2"] {
		t.Fatalf("pastes after the answer: %v", g.pastes)
	}
}

// A saved instance is expanded from disk before the gate lock is taken, so
// the daemon reader's answered() never waits behind the file reads.
func TestBridge_InstanceExpandsOutsideTheGateLock(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	lim := testLimits()
	lim.ExpandInstance = func(string, string) (string, []string, error) {
		close(entered)
		<-release
		return "box", []string{"u@h"}, nil
	}
	b, d, p := startBridge(t, lim)
	frame := func(m *ipc.Message) []byte {
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	hello := msg(t, ipc.MsgHello, "h1", ipc.HelloPayload{Kind: "web", Proto: ipc.ProtocolVersion})
	if err := b.fromPage(p.gen, frame(hello)); err != nil {
		t.Fatal(err)
	}
	split := msg(t, ipc.MsgSplitPaneReq, "s1", map[string]any{"placement": "right", "pane": map[string]any{"type": "ssh", "instance_id": "i1"}})
	done := make(chan error, 1)
	go func() { done <- b.fromPage(p.gen, frame(split)) }()
	<-entered
	if !b.gateMu.TryLock() {
		close(release)
		t.Fatal("the gate lock is held while the instance is read from disk")
	}
	b.gateMu.Unlock()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the split to reach the daemon", func() bool {
		types := d.sentTypes()
		return len(types) > 0 && types[len(types)-1] == ipc.MsgSplitPaneReq
	})
}
