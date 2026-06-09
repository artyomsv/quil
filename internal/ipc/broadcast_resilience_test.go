package ipc_test

import (
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

	fast, err := ipc.NewClient(sockPath)
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
	const fastReceives = 50

	gotFast := make(chan int, broadcasts)
	go func() {
		count := 0
		for {
			if _, err := fast.Receive(); err != nil {
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

	broadcastStart := time.Now()
	for i := 0; i < broadcasts; i++ {
		msg, _ := ipc.NewMessage(ipc.MsgStateUpdate, payload)
		srv.Broadcast(msg)
	}
	broadcastDur := time.Since(broadcastStart)

	// All Broadcast calls must return promptly even though one peer is
	// stalled. The real failure mode this guards against is a *wedged* fan-out:
	// a blocked Broadcast would hang on the slow conn until the 30s write
	// deadline (or forever). The actual healthy cost is microseconds; the
	// generous 5s ceiling tolerates `-race` instrumentation + loaded-CI jitter
	// (each broadcast marshals+clones a 4 KiB frame and does atomic-guarded
	// dual-queue enqueues, all heavily instrumented under the race detector)
	// while still being far below the seconds-to-30s signature of a real wedge.
	if broadcastDur > 5*time.Second {
		t.Errorf("Broadcast loop blocked: %d broadcasts took %v (want < 5s) — slow client wedged the fan-out", broadcasts, broadcastDur)
	}

	// Fast client must drain enough messages to demonstrate it's still being
	// served. Healthy peers never stall.
	timeout := time.After(3 * time.Second)
	for {
		select {
		case n, ok := <-gotFast:
			if !ok {
				t.Fatal("fast client got an error before reaching the expected message count")
			}
			if n >= fastReceives {
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

	fast, err := ipc.NewClient(sockPath)
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
		for {
			if _, err := fast.Receive(); err != nil {
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
