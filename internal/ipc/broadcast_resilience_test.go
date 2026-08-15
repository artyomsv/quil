package ipc_test

import (
	"encoding/binary"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// waitForConnCount polls the server until it reaches the expected client count
// or the deadline elapses. Replaces fragile time.Sleep-after-connect patterns
// that race the daemon's accept goroutine and can silently lose connections
// under CI load.
func waitForConnCount(t *testing.T, srv *ipc.Server, want int, dl time.Duration) {
	t.Helper()
	deadline := time.Now().Add(dl)
	for {
		if srv.ConnCount() == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waitForConnCount: got %d, want %d within %v", srv.ConnCount(), want, dl)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestBroadcast_SlowConnDoesNotBlockFastConn proves the wedge defense: when
// one client stops reading from its socket, the daemon's broadcast loop must
// continue serving the healthy clients without delay. The Bubble Tea event
// loop on a connected TUI cannot be allowed to stall the entire daemon.
func TestBroadcast_SlowConnDoesNotBlockFastConn(t *testing.T) {
	t.Parallel()
	sockPath := filepath.Join(t.TempDir(), "slow-vs-fast.sock")

	srv := ipc.NewServer(sockPath, func(*ipc.Conn, *ipc.Message) {}, nil)
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	defer srv.Stop()

	fast, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("fast client connect: %v", err)
	}
	defer fast.Close()

	slow, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("slow client connect: %v", err)
	}
	defer slow.Close()

	waitForConnCount(t, srv, 2, 2*time.Second)

	// Slow client deliberately never reads — its kernel socket buffer fills up,
	// then the daemon's 64-slot per-conn queue overflows, then the daemon
	// closes the slow conn. Meanwhile the fast client must keep receiving.

	const broadcasts = 200

	// The fast client drains RAW frames — framing only, no JSON decode.
	//
	// It used to call ipc.Client.Receive, whose envelope unmarshal cost ~320 us
	// on this test's escape-heavy 24 KiB frames. That was never "fast"; it was
	// merely faster than the encoder, which used to spend ~372 us per frame
	// re-scanning an already-encoded payload. When EncodeFrame stopped doing
	// that (2026-08) the producer outran this reader ~13x, so the "fast" client
	// filled its own 64-slot critical queue and was closed by the very overflow
	// policy this test exercises on the slow conn — a deterministic failure
	// asserting the opposite of what the test is named for.
	//
	// The encoder no longer supplies incidental backpressure, and this test must
	// not depend on either side's incidental speed. "Fast" means "drains its
	// socket", which is the layer the broadcast fan-out actually couples to.
	gotFast := make(chan int, broadcasts)
	go func() {
		count := 0
		var hdr [4]byte
		for {
			if _, err := io.ReadFull(fast, hdr[:]); err != nil {
				close(gotFast)
				return
			}
			if _, err := io.CopyN(io.Discard, fast, int64(binary.BigEndian.Uint32(hdr[:]))); err != nil {
				close(gotFast)
				return
			}
			count++
			gotFast <- count
		}
	}()

	// Build a 4 KiB-ish payload so each broadcast meaningfully exercises the
	// per-conn send queue. Pure echo of an arbitrary string.
	payload := map[string]string{"data": string(make([]byte, 4000))}

	// Paced, and the pacing is load-bearing rather than cosmetic. The critical
	// queue has NO drop path, so its only slack is 64 slots plus one ~200 KiB
	// socket buffer — about 72 of these frames. Unpaced, the encoder empties
	// 200 of them in ~5 ms and the remaining ~3 MB must be drained
	// concurrently or the FAST conn overflows its own queue and the server
	// closes it, which is this test's failure message verbatim. That was a
	// real low-rate flake on the non-race run (race instrumentation slows the
	// producer enough to hide it). The sibling test below has always paced for
	// the same reason, which is why it never flaked.
	//
	// Only the Broadcast calls are timed, so the "> 5 s means the fan-out is
	// wedged" assertion still measures what it always did.
	var broadcastDur time.Duration
	for i := 0; i < broadcasts; i++ {
		if i > 0 && i%10 == 0 {
			time.Sleep(200 * time.Microsecond)
		}
		msg, _ := ipc.NewMessage(ipc.MsgStateUpdate, payload)
		start := time.Now()
		srv.Broadcast(msg)
		broadcastDur += time.Since(start)
	}

	// All Broadcast calls must return promptly even though one peer is
	// stalled. The real failure mode this guards against is a *wedged* fan-out:
	// a blocked Broadcast would hang on the slow conn until the 30s write
	// deadline (or forever). The actual healthy cost is microseconds; the
	// generous 5s ceiling tolerates `-race` instrumentation + loaded-CI jitter
	// (each broadcast encodes one frame — shared read-only across conns, never
	// cloned — and does atomic-guarded dual-queue enqueues, all heavily
	// instrumented under the race detector)
	// while still being far below the seconds-to-30s signature of a real wedge.
	if broadcastDur > 5*time.Second {
		t.Errorf("Broadcast loop blocked: %d broadcasts took %v (want < 5s) — slow client wedged the fan-out", broadcasts, broadcastDur)
	}

	// A genuinely draining peer must receive EVERY critical frame — the
	// must-deliver queue has no drop path for a conn that keeps up. The old
	// bar was 50 of 200, which tolerated 150 lost frames.
	timeout := time.After(3 * time.Second)
	for {
		select {
		case n, ok := <-gotFast:
			if !ok {
				t.Fatal("fast client got an error before reaching the expected message count")
			}
			if n >= broadcasts {
				return // success
			}
		case <-timeout:
			t.Fatalf("fast client only drained partway within 3s — broadcast fan-out may be wedged")
		}
	}
}

// TestBroadcast_ContinuesAfterSlowConnDisconnects covers the post-overflow
// state: after the slow conn is torn down, broadcasts to remaining conns
// continue normally. Uses ConnCount-based synchronization (no time.Sleep) so
// CI load doesn't race the connect-registration window.
func TestBroadcast_ContinuesAfterSlowConnDisconnects(t *testing.T) {
	t.Parallel()
	sockPath := filepath.Join(t.TempDir(), "post-overflow.sock")

	srv := ipc.NewServer(sockPath, func(*ipc.Conn, *ipc.Message) {}, nil)
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	defer srv.Stop()

	// Raw frame drain, for the same reason as
	// TestBroadcast_SlowConnDoesNotBlockFastConn: a JSON-decoding reader is not
	// a fast reader, and this test's 1 ms pacing was only ever ahead of it
	// because the encoder was slow too. Under -race the margin was ~30%, so
	// once EncodeFrame stopped re-scanning payloads BOTH conns overflowed and
	// waitForConnCount below saw 0 instead of 1.
	fast, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("fast client: %v", err)
	}
	defer fast.Close()

	slow, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("slow client: %v", err)
	}

	waitForConnCount(t, srv, 2, 2*time.Second)

	var fastCount int
	var fastMu sync.Mutex
	go func() {
		var hdr [4]byte
		for {
			if _, err := io.ReadFull(fast, hdr[:]); err != nil {
				return
			}
			if _, err := io.CopyN(io.Discard, fast, int64(binary.BigEndian.Uint32(hdr[:]))); err != nil {
				return
			}
			fastMu.Lock()
			fastCount++
			fastMu.Unlock()
		}
	}()

	// Paced burst with 4 KiB payloads so the slow client's kernel socket
	// buffer (~200 KiB on Linux/Darwin) actually fills — small payloads
	// would just sit in the buffer and never trigger overflow. ~150 frames
	// × 4 KiB = ~600 KiB, well past the kernel buffer + the 64-slot send
	// queue, so overflow trips deterministically. 1 ms pacing keeps the
	// fast drain goroutine ahead.
	bigPayload := map[string]string{"data": string(make([]byte, 4000))}
	for i := 0; i < 150; i++ {
		msg, _ := ipc.NewMessage(ipc.MsgStateUpdate, bigPayload)
		srv.Broadcast(msg)
		time.Sleep(time.Millisecond)
	}

	// Wait for the slow conn to be torn down via the overflow path. Polling
	// on ConnCount converges as soon as the daemon's removeConn fires —
	// independent of CI load.
	waitForConnCount(t, srv, 1, 3*time.Second)

	slow.Close()

	fastMu.Lock()
	pre := fastCount
	fastMu.Unlock()

	// Issue NEW broadcasts after the slow conn is torn down. Fast must still
	// see them — the absence of slow in the broadcast fan-out is the
	// post-overflow invariant we care about.
	for i := 0; i < 50; i++ {
		msg, _ := ipc.NewMessage(ipc.MsgStateUpdate, bigPayload)
		srv.Broadcast(msg)
		time.Sleep(time.Millisecond)
	}

	// Wait for fast to drain the new wave. With a 1 ms inter-broadcast gap
	// and a receive loop that takes microseconds, a 500 ms ceiling is
	// generous even under race instrumentation.
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		fastMu.Lock()
		post := fastCount
		fastMu.Unlock()
		if post-pre >= 30 {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("after slow conn disconnect, fast client only got %d new messages (want ≥ 30)", post-pre)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestBroadcast_MarshalErrorLogsAndReturns covers the otherwise-untested
// failure path where the message payload can't be JSON-encoded. Broadcast
// must NOT panic and must NOT propagate the bad frame to any conn.
func TestBroadcast_MarshalErrorLogsAndReturns(t *testing.T) {
	t.Parallel()
	sockPath := filepath.Join(t.TempDir(), "bad-marshal.sock")

	srv := ipc.NewServer(sockPath, func(*ipc.Conn, *ipc.Message) {}, nil)
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	defer srv.Stop()

	client, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer client.Close()

	waitForConnCount(t, srv, 1, 2*time.Second)

	// Construct a Message whose Payload is already marshal-valid (json.RawMessage
	// of itself is fine) but whose outer Message.Type ends up triggering an
	// error path through any future Marshal customization. The realistic
	// trigger today: an unencodable payload. We bypass NewMessage and inject
	// invalid JSON into the Payload field directly, so json.Marshal of the
	// Message tries to re-marshal the bad RawMessage and fails.
	bad := &ipc.Message{
		Type:    "bad",
		Payload: []byte("{not valid json"), // truncated JSON object
	}

	// json.Marshal on the Message would fail on the RawMessage's MarshalJSON
	// validator. Broadcast should swallow the error and return.
	srv.Broadcast(bad)

	// Verify the server is still functional — broadcast a good message and
	// the client receives it.
	good, _ := ipc.NewMessage(ipc.MsgStateUpdate, map[string]string{"ok": "yes"})
	srv.Broadcast(good)

	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	got, err := client.Receive()
	if err != nil {
		t.Fatalf("client receive after bad broadcast: %v", err)
	}
	if got.Type != ipc.MsgStateUpdate {
		t.Errorf("expected MsgStateUpdate, got %q", got.Type)
	}
}
