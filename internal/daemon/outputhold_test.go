package daemon

import (
	"bytes"
	"encoding/binary"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// The output hold (outputhold.go) is driven through a real ipc.Server: the
// hold is a flag on the SERVER side of a conn, and what matters is which frames
// reach the client's socket, in which order. ipc.Conn has no exported
// constructor, so the harness learns each server-side conn from a probe frame
// the test client sends first.

const msgHoldProbe = "hold_test_probe"

type holdProbePayload struct {
	Tag string `json:"tag"`
}

type holdHarness struct {
	t    *testing.T
	d    *Daemon
	sock string

	mu    sync.Mutex
	conns map[string]*ipc.Conn
}

func newHoldHarness(t *testing.T) *holdHarness {
	t.Helper()
	home := t.TempDir()
	t.Setenv("QUIL_HOME", home)
	h := &holdHarness{t: t, d: New(config.Default()), sock: filepath.Join(home, "s.sock"), conns: map[string]*ipc.Conn{}}
	h.d.server = ipc.NewServer(h.sock, func(c *ipc.Conn, m *ipc.Message) {
		if m.Type == msgHoldProbe {
			var p holdProbePayload
			if err := m.DecodePayload(&p); err == nil {
				h.mu.Lock()
				h.conns[p.Tag] = c
				h.mu.Unlock()
			}
			return
		}
		h.d.handleMessage(c, m)
	}, h.d.onClientDisconnect)
	if err := h.d.server.Start(); err != nil {
		t.Fatalf("start IPC server: %v", err)
	}
	t.Cleanup(func() { h.d.server.Stop() })
	return h
}

// dial connects a client and returns it with its server-side conn.
func (h *holdHarness) dial(tag string) (*ipc.Client, *ipc.Conn) {
	h.t.Helper()
	c, err := ipc.NewClient(h.sock)
	if err != nil {
		h.t.Fatalf("dial: %v", err)
	}
	h.t.Cleanup(func() { c.Close() })
	sendClientMsg(h.t, c, msgHoldProbe, holdProbePayload{Tag: tag})
	var conn *ipc.Conn
	waitUntil(h.t, "server conn for "+tag, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		conn = h.conns[tag]
		return conn != nil
	})
	return c, conn
}

func (h *holdHarness) pane(tabID, typ string) *Pane {
	h.t.Helper()
	p, err := h.d.session.CreatePane(tabID, "")
	if err != nil {
		h.t.Fatalf("create pane: %v", err)
	}
	p.PluginMu.Lock()
	p.Type = typ
	p.PluginMu.Unlock()
	return p
}

func (h *holdHarness) holdOf(c *ipc.Conn) (held bool, chunks int, lost map[string]bool) {
	h.d.holdMu.Lock()
	defer h.d.holdMu.Unlock()
	hold := h.d.holds[c]
	if hold == nil {
		return false, 0, nil
	}
	lost = map[string]bool{}
	for k, v := range hold.lost {
		lost[k] = v
	}
	return true, len(hold.chunks), lost
}

func (h *holdHarness) holdCount() int {
	h.d.holdMu.Lock()
	defer h.d.holdMu.Unlock()
	return len(h.d.holds)
}

// readOutput reads c's pane_output frames until stop accepts one, returning
// them all (the accepted one last). Other frame types are skipped.
func readOutput(t *testing.T, c *ipc.Client, stop func(ipc.PaneOutputPayload) bool) []ipc.PaneOutputPayload {
	t.Helper()
	var got []ipc.PaneOutputPayload
	if err := c.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	for {
		m, err := c.Receive()
		if err != nil {
			t.Fatalf("read after %d pane_output frames: %v", len(got), err)
		}
		if m.Type != ipc.MsgPaneOutput {
			continue
		}
		var p ipc.PaneOutputPayload
		if err := m.DecodePayload(&p); err != nil {
			t.Fatalf("decode pane_output: %v", err)
		}
		got = append(got, p)
		if stop(p) {
			return got
		}
	}
}

func isChunk(paneID, data string) func(ipc.PaneOutputPayload) bool {
	return func(p ipc.PaneOutputPayload) bool { return p.PaneID == paneID && string(p.Data) == data }
}

// paneData concatenates one pane's frames and fails on an empty frame: a
// release that re-encodes a fully replayed chunk as zero bytes is a bug even
// though it adds no bytes.
func paneData(t *testing.T, frames []ipc.PaneOutputPayload, paneID string) []byte {
	t.Helper()
	var b []byte
	for _, f := range frames {
		if f.PaneID != paneID {
			continue
		}
		if len(f.Data) == 0 {
			t.Fatalf("empty pane_output frame for pane %s", paneID)
		}
		b = append(b, f.Data...)
	}
	return b
}

// counterRun is n consecutive little-endian uint64 counters starting at first.
func counterRun(first uint64, n int) []byte {
	b := make([]byte, 8*n)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint64(b[8*i:], first+uint64(i))
	}
	return b
}

// counterBytes is chunk i of a counter stream: 64 bytes, counters 8i..8i+7.
func counterBytes(i int) []byte { return counterRun(uint64(i)*8, 8) }

// checkCounterRun asserts b is exactly the counters first..last, in order:
// no duplicate, no gap, nothing extra at either end.
func checkCounterRun(t *testing.T, b []byte, first, last uint64) {
	t.Helper()
	if len(b)%8 != 0 {
		t.Fatalf("stream is %d bytes, not a whole number of counters", len(b))
	}
	if len(b) == 0 {
		t.Fatal("stream is empty")
	}
	for i := 0; i < len(b); i += 8 {
		want := first + uint64(i/8)
		if got := binary.LittleEndian.Uint64(b[i:]); got != want {
			t.Fatalf("counter #%d = %d, want %d (a duplicate or a gap)", i/8, got, want)
		}
	}
	if got := binary.LittleEndian.Uint64(b[len(b)-8:]); got != last {
		t.Fatalf("stream ends at %d, want %d", got, last)
	}
}

// A pane whose held bytes pass the bound loses them and is kicked once to
// repaint; the other pane's held bytes still arrive, and the conn stays open.
func TestHold_OverflowDropsPaneAndKicks(t *testing.T) {
	h := newHoldHarness(t)
	loadRedrawKeyPlugin(t, h.d, "kicky")
	tab := h.d.session.CreateTab("T")
	p := h.pane(tab.ID, "kicky")
	p.PluginMu.Lock()
	p.PTY = &resizeProbeSession{}
	p.PluginMu.Unlock()
	t.Cleanup(p.StopInput)
	q := h.pane(tab.ID, "terminal")

	client, conn := h.dial("B")
	h.d.beginOutputHold(conn)

	h.d.flushPaneOutput(q.ID, []byte("q1"))
	big := bytes.Repeat([]byte{'p'}, 64<<10)
	for sent := 0; sent <= outputHoldLimit; sent += len(big) {
		h.d.flushPaneOutput(p.ID, big)
	}
	h.d.flushPaneOutput(p.ID, []byte("after-overflow"))
	h.d.flushPaneOutput(q.ID, []byte("q2"))

	if _, _, lost := h.holdOf(conn); !lost[p.ID] {
		t.Fatalf("pane P not marked lost after passing %d held bytes", outputHoldLimit)
	}
	if n := p.inputEnqueued.Load(); n != 0 {
		t.Fatalf("P kicked %d times before the release, want 0", n)
	}

	h.d.releaseOutputHold(conn, nil)
	h.d.flushPaneOutput(p.ID, []byte("live"))

	frames := readOutput(t, client, isChunk(p.ID, "live"))
	if got := string(paneData(t, frames, q.ID)); got != "q1q2" {
		t.Errorf("Q received %q, want %q", got, "q1q2")
	}
	if got := string(paneData(t, frames, p.ID)); got != "live" {
		t.Errorf("P received %d held bytes, want only the live frame after release", len(got)-len("live"))
	}
	waitUntil(t, "one redraw kick for P", func() bool { return p.inputEnqueued.Load() >= 1 })
	time.Sleep(50 * time.Millisecond)
	if n := p.inputEnqueued.Load(); n != 1 {
		t.Errorf("P kicked %d times, want 1", n)
	}
	if n := h.holdCount(); n != 0 {
		t.Errorf("%d holds left after release, want 0", n)
	}
}

// Review Focus 3: a restart during the hold. Held chunks of both generations
// arrive in order with their own generation, and the cut at the replay's end
// is by stream position only — the old generation's bytes past it are kept.
func TestHold_RestartDuringHoldKeepsGenerations(t *testing.T) {
	h := newHoldHarness(t)
	tab := h.d.session.CreateTab("T")
	p := h.pane(tab.ID, "terminal")
	p.PluginMu.Lock()
	p.PTY = &fakeSession{}
	p.ptyGen = 1
	p.PluginMu.Unlock()

	client, conn := h.dial("B")
	h.d.beginOutputHold(conn)

	h.d.flushPaneOutputGeneration(p.ID, []byte("a1"), 1)
	p.PluginMu.Lock()
	end := p.outPos // the replay ended after a1
	p.PluginMu.Unlock()
	h.d.flushPaneOutputGeneration(p.ID, []byte("a2"), 1)
	p.PluginMu.Lock()
	p.ptyGen = 2 // restarted
	p.PluginMu.Unlock()
	h.d.flushPaneOutputGeneration(p.ID, []byte("b1"), 2)

	h.d.releaseOutputHold(conn, map[string]uint64{p.ID: end})
	h.d.flushPaneOutputGeneration(p.ID, []byte("b2"), 2)

	frames := readOutput(t, client, isChunk(p.ID, "b2"))
	type fr struct {
		data string
		gen  uint64
	}
	var got []fr
	for _, f := range frames {
		got = append(got, fr{string(f.Data), f.Generation})
	}
	want := []fr{{"a2", 1}, {"b1", 2}, {"b2", 2}}
	if len(got) != len(want) {
		t.Fatalf("frames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("frames = %v, want %v", got, want)
		}
	}
}

// The cut at the replay's end: a chunk wholly before it is dropped (no empty
// frame), one straddling it is trimmed, one after it is kept whole.
func TestHold_ReleaseCutsWhatTheReplayCovered(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  uint64
		want []string
	}{
		{"end on a chunk boundary", 8, []string{"ijkl"}},
		{"end inside a chunk", 6, []string{"gh", "ijkl"}},
		{"end at the start", 0, []string{"abcd", "efgh", "ijkl"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHoldHarness(t)
			tab := h.d.session.CreateTab("T")
			p := h.pane(tab.ID, "terminal")
			client, conn := h.dial("B")
			h.d.beginOutputHold(conn)
			for _, s := range []string{"abcd", "efgh", "ijkl"} {
				h.d.flushPaneOutput(p.ID, []byte(s))
			}
			h.d.releaseOutputHold(conn, map[string]uint64{p.ID: tc.end})
			h.d.flushPaneOutput(p.ID, []byte("Z"))

			frames := readOutput(t, client, isChunk(p.ID, "Z"))
			var got []string
			for _, f := range frames[:len(frames)-1] {
				got = append(got, string(f.Data))
			}
			if len(got) != len(tc.want) {
				t.Fatalf("held frames = %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("held frames = %q, want %q", got, tc.want)
				}
			}
		})
	}
}

// Flushes that land while the release is still draining go out after the
// earlier held bytes, and exactly once: the conn stays held until the hold is
// empty, so no flush reaches it live AND through the hold.
func TestHold_FlushDuringDrainArrivesOnceInOrder(t *testing.T) {
	h := newHoldHarness(t)
	tab := h.d.session.CreateTab("T")
	p := h.pane(tab.ID, "terminal")
	client, conn := h.dial("B")
	h.d.beginOutputHold(conn)

	// 1 MiB held and nobody reading: more than the socket buffer plus the
	// must-deliver queue's headroom, so the release blocks mid-drain.
	const perChunk = 1024 // counters per chunk (8 KiB)
	next := uint64(0)
	flush := func() {
		h.d.flushPaneOutput(p.ID, counterRun(next, perChunk))
		next += perChunk
	}
	for i := 0; i < 128; i++ {
		flush()
	}
	released := make(chan struct{})
	go func() {
		h.d.releaseOutputHold(conn, nil)
		close(released)
	}()
	waitUntil(t, "release to take the first batch", func() bool {
		held, chunks, _ := h.holdOf(conn)
		return held && chunks == 0
	})
	for i := 0; i < 20; i++ {
		flush()
	}
	last := next - 1

	frames := readOutput(t, client, func(f ipc.PaneOutputPayload) bool {
		return f.PaneID == p.ID && len(f.Data) >= 8 &&
			binary.LittleEndian.Uint64(f.Data[len(f.Data)-8:]) == last
	})
	checkCounterRun(t, paneData(t, frames, p.ID), 0, last)
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("release did not return")
	}
	if n := h.holdCount(); n != 0 {
		t.Errorf("%d holds left, want 0", n)
	}
}

// pauseFirstFlush makes the first flush of paneID stop between its hold append
// and its broadcast until resume is closed. paused closes when it stops.
func pauseFirstFlush(d *Daemon, paneID string) (paused, resume chan struct{}) {
	paused, resume = make(chan struct{}), make(chan struct{})
	var once sync.Once
	d.afterHoldOutput = func(id string) {
		if id != paneID {
			return
		}
		once.Do(func() {
			close(paused)
			<-resume
		})
	}
	return paused, resume
}

// A flush caught between its hold append and its broadcast while the release
// ends the hold: its bytes must arrive once, through the hold, and not a
// second time through a broadcast that finds the conn already unheld.
func TestHold_FlushStraddlingTheReleaseArrivesOnce(t *testing.T) {
	h := newHoldHarness(t)
	tab := h.d.session.CreateTab("T")
	p := h.pane(tab.ID, "terminal")
	client, conn := h.dial("B")
	paused, resume := pauseFirstFlush(h.d, p.ID)

	h.d.beginOutputHold(conn)
	flushed := make(chan struct{})
	go func() {
		h.d.flushPaneOutput(p.ID, []byte("s"))
		close(flushed)
	}()
	<-paused
	released := make(chan struct{})
	go func() {
		h.d.releaseOutputHold(conn, nil)
		close(released)
	}()
	waitUntil(t, "release to take the held chunk", func() bool {
		_, chunks, _ := h.holdOf(conn)
		return chunks == 0
	})
	time.Sleep(50 * time.Millisecond) // let the release reach its final clear
	close(resume)
	<-flushed
	<-released

	h.d.flushPaneOutput(p.ID, []byte("Z"))
	if got := string(paneData(t, readOutput(t, client, isChunk(p.ID, "Z")), p.ID)); got != "sZ" {
		t.Fatalf("received %q, want %q", got, "sZ")
	}
}

// The LOSS half of the release straddle: a flush that appends after the
// release saw its last, empty batch but before the hold ends. finishOutputHold
// must notice it and send it; ending the hold anyway would lose it, since the
// flush's broadcast skipped the still-held conn.
func TestHold_FlushAfterLastBatchBeforeFinishArrivesOnce(t *testing.T) {
	h := newHoldHarness(t)
	tab := h.d.session.CreateTab("T")
	p := h.pane(tab.ID, "terminal")
	client, conn := h.dial("B")

	var once sync.Once
	h.d.beforeFinishHold = func(*ipc.Conn) {
		once.Do(func() { h.d.flushPaneOutput(p.ID, []byte("s")) })
	}
	h.d.beginOutputHold(conn)
	h.d.releaseOutputHold(conn, nil)
	if n := h.holdCount(); n != 0 {
		t.Fatalf("%d holds left after release, want 0", n)
	}

	h.d.flushPaneOutput(p.ID, []byte("Z"))
	if got := string(paneData(t, readOutput(t, client, isChunk(p.ID, "Z")), p.ID)); got != "sZ" {
		t.Fatalf("received %q, want %q", got, "sZ")
	}
}

// A conn that dies while its droppable queue is backed up never drains it, so
// beginOutputHold's wait must end on the close rather than sit out its bound.
func TestHold_BeginDrainAbortsOnConnClose(t *testing.T) {
	h := newHoldHarness(t)
	tab := h.d.session.CreateTab("T")
	p := h.pane(tab.ID, "terminal")
	client, conn := h.dial("B")

	// Nobody reads: the socket fills and the droppable queue backs up.
	chunk := bytes.Repeat([]byte{'x'}, 8<<10)
	for i := 0; conn.Dropped() == 0 && i < 4096; i++ {
		h.d.flushPaneOutput(p.ID, chunk)
	}
	if conn.QueuedOutput() == 0 {
		t.Fatal("setup: the droppable queue is empty")
	}

	returned := make(chan time.Time, 1)
	go func() {
		h.d.beginOutputHold(conn)
		returned <- time.Now()
	}()
	waitUntil(t, "the hold to begin", func() bool { held, _, _ := h.holdOf(conn); return held })
	time.Sleep(20 * time.Millisecond) // let the drain wait start
	closedAt := time.Now()
	client.Close()

	select {
	case at := <-returned:
		if waited := at.Sub(closedAt); waited > holdDrainTimeout/2 {
			t.Fatalf("beginOutputHold returned %v after the close, want well under %v", waited, holdDrainTimeout)
		}
	case <-time.After(2 * holdDrainTimeout):
		t.Fatal("beginOutputHold never returned")
	}
}

// The same straddle as a hold begins: a flush that appended before the hold
// existed must still reach the conn through its broadcast, not find the conn
// held and be skipped with nothing held.
func TestHold_FlushStraddlingTheBeginArrivesOnce(t *testing.T) {
	h := newHoldHarness(t)
	tab := h.d.session.CreateTab("T")
	p := h.pane(tab.ID, "terminal")
	client, conn := h.dial("B")
	paused, resume := pauseFirstFlush(h.d, p.ID)

	flushed := make(chan struct{})
	go func() {
		h.d.flushPaneOutput(p.ID, []byte("s"))
		close(flushed)
	}()
	<-paused
	begun := make(chan struct{})
	go func() {
		h.d.beginOutputHold(conn)
		close(begun)
	}()
	time.Sleep(50 * time.Millisecond) // let the begin set the flag, if it can
	close(resume)
	<-flushed
	<-begun
	h.d.releaseOutputHold(conn, nil)

	h.d.flushPaneOutput(p.ID, []byte("Z"))
	if got := string(paneData(t, readOutput(t, client, isChunk(p.ID, "Z")), p.ID)); got != "sZ" {
		t.Fatalf("received %q, want %q", got, "sZ")
	}
}

func TestHold_DisconnectDropsHold(t *testing.T) {
	h := newHoldHarness(t)
	tab := h.d.session.CreateTab("T")
	p := h.pane(tab.ID, "terminal")
	client, conn := h.dial("B")
	h.d.beginOutputHold(conn)
	h.d.flushPaneOutput(p.ID, []byte("held"))
	if held, chunks, _ := h.holdOf(conn); !held || chunks != 1 {
		t.Fatalf("hold = %v with %d chunks, want a hold with 1", held, chunks)
	}
	client.Close()
	waitUntil(t, "hold dropped on disconnect", func() bool { return h.holdCount() == 0 })
}

// Only the attaching conn is held: another client keeps its live output.
func TestHold_OtherClientsUnaffected(t *testing.T) {
	h := newHoldHarness(t)
	tab := h.d.session.CreateTab("T")
	p := h.pane(tab.ID, "terminal")
	a, _ := h.dial("A")
	b, connB := h.dial("B")
	h.d.beginOutputHold(connB)

	h.d.flushPaneOutput(p.ID, []byte("x"))
	if got := string(paneData(t, readOutput(t, a, isChunk(p.ID, "x")), p.ID)); got != "x" {
		t.Fatalf("A received %q while B was held, want %q", got, "x")
	}
	for _, m := range readFor(b, 200*time.Millisecond) {
		if m.Type == ipc.MsgPaneOutput {
			t.Fatal("held client B received live pane_output")
		}
	}
}
