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
	block  chan struct{} // non-nil: writes wait on it
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
func (p *fakePage) Close(code int, _ string)                      { p.mu.Lock(); p.closed = code; p.mu.Unlock() }
func (p *fakePage) closeCode() int                                { p.mu.Lock(); defer p.mu.Unlock(); return p.closed }
func (p *fakePage) count() int                                    { p.mu.Lock(); defer p.mu.Unlock(); return len(p.frames) }

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

func startBridgeAt(t *testing.T, lim bridgeLimits, now func() time.Time, p *fakePage) (*bridge, *fakeDaemon) {
	t.Helper()
	d := newFakeDaemon()
	b := newBridge(d, lim, newReplayBudget(128<<20), now, t.Logf)
	b.attachPage(p)
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
		if err := b.fromPage(ack); err != nil {
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
	d := newFakeDaemon()
	budget := newReplayBudget(15)
	b := newBridge(d, testLimits(), budget, time.Now, t.Logf)
	p := &fakePage{block: make(chan struct{})}
	b.attachPage(p)
	ctx, cancel := context.WithCancel(context.Background())
	go b.run(ctx)
	t.Cleanup(func() { cancel(); b.close(CloseGoingAway, "test over") })
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
	defer d.mu.Unlock()
	if !d.flushed || !d.closed {
		t.Fatalf("flushed=%v closed=%v", d.flushed, d.closed)
	}
	if p.closeCode() != CloseGoingAway {
		t.Fatalf("page closed with %d", p.closeCode())
	}
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
	b.attachPage(p2)
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
		b.attachPage(p)
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
	b, d, _ := startBridge(t, testLimits())
	ack, _ := json.Marshal(map[string]any{"type": MsgWebAck, "payload": WebAckPayload{Bytes: 5}})
	if err := b.fromPage(ack); err != nil {
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
	b, d, _ := startBridge(t, testLimits())
	b.setLease("web-a-1")
	if err := b.fromPage(helloFrame(t, "web-a-1")); err != nil {
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
	if err := b.fromPage(helloFrame(t, "web-a-1")); err == nil {
		t.Fatal("hello naming the old lease was accepted after a new lease")
	}
}

func TestBridge_FatalFirstMessageClosesTheTab(t *testing.T) {
	b, d, p := startBridge(t, testLimits())
	b.setLease("web-a-1")
	stateReq, _ := json.Marshal(&ipc.Message{Type: ipc.MsgStateReq})
	if err := b.fromPage(stateReq); err == nil {
		t.Fatal("a first message other than hello was accepted")
	}
	waitFor(t, "page closed", func() bool { return p.closeCode() != 0 })
	if !d.isClosed() {
		t.Fatal("daemon conn still open")
	}
	if err := b.fromPage(stateReq); !errors.Is(err, errClosed) {
		t.Fatalf("frame after close: %v, want errClosed", err)
	}
}
